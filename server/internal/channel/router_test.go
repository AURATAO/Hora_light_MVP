package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// ── fakes ──────────────────────────────────────────────────────────────────

type memIdentities struct {
	mu       sync.Mutex
	ids      map[string]Identity // key channel|address
	tokens   []memToken
	attached map[string]string
	touched  map[string]time.Time
}

type memToken struct {
	identityID, hash string
	created, expires time.Time
	used             bool
}

func newMemIdentities() *memIdentities {
	return &memIdentities{ids: map[string]Identity{}, attached: map[string]string{}, touched: map[string]time.Time{}}
}

func (m *memIdentities) UpsertIdentity(_ context.Context, ch Channel, addr string) (Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := string(ch) + "|" + addr
	if id, ok := m.ids[k]; ok {
		return id, nil
	}
	id := Identity{ID: "id-" + addr, Channel: ch, Address: addr}
	m.ids[k] = id
	return id, nil
}
func (m *memIdentities) link(addr, user string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := "imessage|" + addr
	id := m.ids[k]
	if id.ID == "" {
		id = Identity{ID: "id-" + addr, Channel: IMessage, Address: addr}
	}
	id.UserID = user
	m.ids[k] = id
}
func (m *memIdentities) AttachMessage(_ context.Context, _ Channel, pid, iid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attached[pid] = iid
	return nil
}
func (m *memIdentities) TouchConversation(_ context.Context, iid string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.touched[iid] = at
	return nil
}
func (m *memIdentities) SetupTokenIssuedSince(_ context.Context, iid string, since time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if t.identityID == iid && t.created.After(since) && !t.used {
			return true, nil
		}
	}
	return false, nil
}
func (m *memIdentities) CreateSetupToken(_ context.Context, iid, hash string, exp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens = append(m.tokens, memToken{identityID: iid, hash: hash, created: time.Now(), expires: exp})
	return nil
}
func (m *memIdentities) PeekSetupToken(context.Context, string) (Identity, time.Time, error) {
	return Identity{}, time.Time{}, ErrTokenInvalid
}
func (m *memIdentities) ConsumeSetupTokenAndLink(context.Context, string, string) (Identity, error) {
	return Identity{}, ErrTokenInvalid
}

// memConversations mirrors PGStore's semantics, including the atomic claim.
type memConversations struct {
	mu       sync.Mutex
	convs    map[string]Conversation
	ids      *memIdentities
	claimErr error // when set, ClaimForPosting fails like a database would
}

func (m *memConversations) GetConversation(_ context.Context, iid string) (Conversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.convs[iid]
	if !ok {
		return Conversation{IdentityID: iid, State: StateIdle}, nil
	}
	if c.Pending != nil {
		p := *c.Pending
		c.Pending = &p
	}
	return c, nil
}
func (m *memConversations) SaveConversation(_ context.Context, c Conversation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.convs[c.IdentityID]; ok && old.TermsAcceptedAt != nil {
		c.TermsAcceptedAt = old.TermsAcceptedAt // COALESCE, like the SQL
	}
	m.convs[c.IdentityID] = c
	return nil
}
func (m *memConversations) ConversationsForUser(_ context.Context, user string) ([]Conversation, []Identity, error) {
	m.ids.mu.Lock()
	var ids []Identity
	for _, id := range m.ids.ids {
		if id.UserID == user {
			ids = append(ids, id)
		}
	}
	m.ids.mu.Unlock()
	var cs []Conversation
	for _, id := range ids {
		c, err := m.GetConversation(context.Background(), id.ID)
		if err != nil {
			return nil, nil, err
		}
		cs = append(cs, c)
	}
	return cs, ids, nil
}
func (m *memConversations) AcceptTerms(_ context.Context, iid string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.convs[iid]
	if !ok {
		c = Conversation{IdentityID: iid, State: StateIdle}
	}
	if c.TermsAcceptedAt == nil {
		c.TermsAcceptedAt = &at
	}
	m.convs[iid] = c
	return nil
}
func (m *memConversations) ClaimForPosting(_ context.Context, iid string, from State) (*PendingDraft, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimErr != nil {
		return nil, false, m.claimErr
	}
	c, ok := m.convs[iid]
	if !ok || c.State != from || c.Pending == nil {
		return nil, false, nil
	}
	c.State = StatePosting
	m.convs[iid] = c
	p := *c.Pending
	return &p, true, nil
}

type fakeParser struct {
	draft   llm.TaskDraft
	err     error
	sources []string
	// byText, when set, answers per errand text (multi-errand tests); a
	// text with no entry fails to parse.
	byText map[string]llm.TaskDraft
}

func (f *fakeParser) ParseTask(_ context.Context, _ string, text string) (llm.TaskDraft, llm.Usage, error) {
	f.sources = append(f.sources, text)
	if f.byText != nil {
		d, ok := f.byText[text]
		if !ok {
			return llm.TaskDraft{}, llm.Usage{Model: "fake"}, errors.New("cannot parse")
		}
		return d, llm.Usage{Model: "fake"}, nil
	}
	return f.draft, llm.Usage{Model: "fake"}, f.err
}

// fakeSplitter splits on " and then ".
type fakeSplitter struct{ err error }

func (s fakeSplitter) SplitErrands(_ context.Context, text string) ([]string, llm.Usage, error) {
	if s.err != nil {
		return []string{text}, llm.Usage{Model: "fake"}, s.err
	}
	return strings.Split(text, " and then "), llm.Usage{Model: "fake"}, nil
}

type fakeClassifier struct{ intent llm.Intent }

func (f fakeClassifier) Classify(context.Context, string) (llm.Intent, error) { return f.intent, nil }

type fakeBooker struct {
	mu      sync.Mutex
	quotes  int
	posts   int
	result  PostResult
	delay   time.Duration
	applied []string
	// approvals (M3)
	extResult ExtensionResult
	resolved  []string
	// multi-errand (M3): titles posted in order; per-call results when set
	posted  []string
	results []PostResult
	opts    []PostOptions
}

func (b *fakeBooker) Quote(_ context.Context, _ llm.TaskDraft, budgetCents int) (int, int, error) {
	b.mu.Lock()
	b.quotes++
	b.mu.Unlock()
	return 1200 + budgetCents, 30, nil
}
func (b *fakeBooker) Post(_ context.Context, _ string, d llm.TaskDraft, opts PostOptions) PostResult {
	time.Sleep(b.delay)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.posts++
	b.posted = append(b.posted, d.Title)
	b.opts = append(b.opts, opts)
	if len(b.results) > 0 {
		r := b.results[0]
		b.results = b.results[1:]
		return r
	}
	return b.result
}
func (b *fakeBooker) ResolveExtension(_ context.Context, user, ext string, approve bool) ExtensionResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resolved = append(b.resolved, fmt.Sprintf("%s:%s:%t", user, ext, approve))
	if b.extResult.Outcome == ExtResolved && b.extResult.Status == "" {
		r := b.extResult
		if approve {
			r.Status = "approved"
		} else {
			r.Status = "denied"
		}
		return r
	}
	return b.extResult
}
func (b *fakeBooker) ApplyChatProfile(_ context.Context, user, name string, _ *time.Time) error {
	b.applied = append(b.applied, user+":"+name)
	return nil
}

// fakeSendAdapter records sends.
type fakeSendAdapter struct {
	fakeAdapter
	mu   sync.Mutex
	sent []Outbound
	err  error
}

func (a *fakeSendAdapter) Send(_ context.Context, o Outbound) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return "", a.err
	}
	a.sent = append(a.sent, o)
	return "out-" + string(rune('a'+len(a.sent))), nil
}
func (a *fakeSendAdapter) last() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.sent) == 0 {
		return ""
	}
	return a.sent[len(a.sent)-1].Text
}

type rig struct {
	r      *Router
	ids    *memIdentities
	convs  *memConversations
	ad     *fakeSendAdapter
	st     *fakeStore
	parser *fakeParser
	booker *fakeBooker
}

var laundry = llm.TaskDraft{Title: "Laundry pickup", Category: "laundry", Location1: "12 Bedford Ave", DurationMinutes: 30}

func newRig(intent llm.Intent) *rig {
	ids := newMemIdentities()
	convs := &memConversations{convs: map[string]Conversation{}, ids: ids}
	ad := &fakeSendAdapter{}
	st := &fakeStore{}
	p := &fakeParser{draft: laundry}
	b := &fakeBooker{result: PostResult{Outcome: PostBooked, TaskID: "task-1"},
		extResult: ExtensionResult{Outcome: ExtResolved, Ask: "15 more minutes on \u201cLaundry pickup\u201d", TaskID: "task-1"}}
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	r := &Router{
		Messages: st, Identities: ids, Conversations: convs,
		Adapters:   map[Channel]Adapter{IMessage: ad},
		Parser:     p,
		Classifier: fakeClassifier{intent: intent},
		Booker:     b,
		Prompt:     func(time.Time) string { return "prompt" },
		SetupURL:   func(t string) string { return "https://example.test/channel/setup#t=" + t },
		Location:   nyc,
		Go:         func(f func()) { f() },
		// the server passes its billing value; tests use the same figure
		AutoExtendMinutes: 15,
	}
	return &rig{r: r, ids: ids, convs: convs, ad: ad, st: st, parser: p, booker: b}
}

const me = "+15551234567"

func (g *rig) say(t *testing.T, text string) string {
	t.Helper()
	in := Inbound{Channel: IMessage, ProviderMessageID: text + time.Now().String(), From: me, Text: text, ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatalf("say %q: %v", text, err)
	}
	return g.ad.last()
}

func (g *rig) state() State {
	c, err := g.convs.GetConversation(context.Background(), "id-"+me)
	if err != nil {
		panic(err)
	}
	return c.State
}

// ── flow ───────────────────────────────────────────────────────────────────

func TestGreetingGetsHelpNotALink(t *testing.T) {
	g := newRig(llm.IntentGreeting)
	if got := g.say(t, "hi"); got != CopyWelcome {
		t.Fatalf("reply %q", got)
	}
	if len(g.ids.tokens) != 0 {
		t.Fatal("a greeting must never produce a setup link")
	}
}

func TestFirstTimerFullFlow(t *testing.T) {
	g := newRig(llm.IntentNewTask)

	// Request → asks the name, no link yet.
	if got := g.say(t, "pick up my laundry at 12 Bedford Ave"); !strings.Contains(got, "Laundry pickup") || !strings.Contains(got, "first name") {
		t.Fatalf("expected name question, got %q", got)
	}
	if g.state() != StateAwaitingName || len(g.ids.tokens) != 0 {
		t.Fatalf("state %s tokens %d", g.state(), len(g.ids.tokens))
	}

	// Name → priced summary with terms.
	got := g.say(t, "I'm Aura")
	for _, want := range []string{"Laundry pickup", "Estimate: $12", "Reply YES", "Terms: " + TermsURL, "12 Bedford Ave"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
	if g.state() != StateAwaitingConfirm {
		t.Fatalf("state %s", g.state())
	}

	// YES → the one link; consent recorded; nothing posted yet.
	got = g.say(t, "YES")
	if !strings.Contains(got, "https://example.test/channel/setup#t=") || len(g.ids.tokens) != 1 {
		t.Fatalf("expected one setup link, got %q (tokens %d)", got, len(g.ids.tokens))
	}
	c, err := g.convs.GetConversation(context.Background(), "id-"+me)
	if err != nil {
		t.Fatal(err)
	}
	if c.State != StateAwaitingSetup || c.TermsAcceptedAt == nil || c.PendingName != "Aura" || c.Pending == nil {
		t.Fatalf("conversation after YES: %+v", c)
	}
	if g.booker.posts != 0 {
		t.Fatal("posted before the person had an account")
	}

	// Texting again right away: no second link.
	if got := g.say(t, "hello?"); got != CopySetupAlreadySent || len(g.ids.tokens) != 1 {
		t.Fatalf("got %q tokens %d", got, len(g.ids.tokens))
	}

	// Page finishes → booked once, confirmation in the chat.
	g.ids.link(me, "user-1")
	booked, _, err := g.r.FinishSetup(context.Background(), "user-1")
	if err != nil || booked != 1 || g.booker.posts != 1 || g.ad.last() != CopyBooked || g.state() != StateIdle {
		t.Fatalf("finish: booked=%d posts=%d last=%q state=%s err=%v", booked, g.booker.posts, g.ad.last(), g.state(), err)
	}
	// A second tap on the page books nothing more.
	booked, _, err = g.r.FinishSetup(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if booked != 0 || g.booker.posts != 1 {
		t.Fatalf("second finish posted again: booked=%d posts=%d", booked, g.booker.posts)
	}
}

func TestLinkedCustomerBooksInChat(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	if got := g.say(t, "laundry pickup"); !strings.Contains(got, "Reply YES") {
		t.Fatalf("returning customer should go straight to the summary, got %q", got)
	}
	if got := g.say(t, "yes"); got != CopyBooked {
		t.Fatalf("got %q", got)
	}
	if g.booker.posts != 1 || len(g.ids.tokens) != 0 || g.state() != StateIdle {
		t.Fatalf("posts=%d tokens=%d state=%s", g.booker.posts, len(g.ids.tokens), g.state())
	}
}

func TestParallelYesBooksOnce(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.say(t, "laundry pickup")
	g.booker.delay = 50 * time.Millisecond // the first YES is still posting

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := Inbound{Channel: IMessage, ProviderMessageID: "yes-" + string(rune('a'+i)), From: me, Text: "YES", ReceivedAt: time.Now()}
			if err := g.r.HandleInbound(context.Background(), in); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if g.booker.posts != 1 {
		t.Fatalf("%d parallel YESes posted %d times", 5, g.booker.posts)
	}
}

func TestNoDropsTheDraft(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.say(t, "laundry pickup")
	if got := g.say(t, "cancel"); got != CopyDropped || g.state() != StateIdle {
		t.Fatalf("got %q state %s", got, g.state())
	}
	if g.booker.posts != 0 {
		t.Fatal("cancel posted a task")
	}
}

func TestCorrectionRedraftsWithContext(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.say(t, "laundry pickup at 3pm")
	g.say(t, "actually make it 4pm")
	last := g.parser.sources[len(g.parser.sources)-1]
	if !strings.Contains(last, "laundry pickup at 3pm") || !strings.Contains(last, "make it 4pm") {
		t.Fatalf("re-draft lost context: %q", last)
	}
	if g.state() != StateAwaitingConfirm || g.booker.posts != 0 {
		t.Fatal("a correction must re-ask, never book")
	}
}

func TestNeedsCardSendsLink(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.booker.result = PostResult{Outcome: PostNeedsCard}
	g.say(t, "laundry pickup")
	got := g.say(t, "yes")
	if !strings.Contains(got, "add a card") || g.state() != StateAwaitingSetup || len(g.ids.tokens) != 1 {
		t.Fatalf("got %q state %s", got, g.state())
	}
}

func TestDeclineKeepsDraftForRetry(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.booker.result = PostResult{Outcome: PostDeclined, Message: "Your card was declined."}
	g.say(t, "laundry pickup")
	got := g.say(t, "yes")
	if !strings.Contains(got, "Your card was declined.") || g.state() != StateAwaitingConfirm {
		t.Fatalf("got %q state %s", got, g.state())
	}
}

func TestParseFailureAsksAgain(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.parser.err = errors.New("model down")
	if got := g.say(t, "blah"); got != CopyParseFailed || g.state() != StateIdle {
		t.Fatalf("got %q state %s", got, g.state())
	}
}

func TestSendFailureIsReturned(t *testing.T) {
	g := newRig(llm.IntentGreeting)
	g.ad.err = errors.New("sendblue 400")
	in := Inbound{Channel: IMessage, ProviderMessageID: "m1", From: me, Text: "hi", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err == nil {
		t.Fatal("a failed send must surface so the message stays unprocessed")
	}
	if g.st.outbound != 0 {
		t.Fatal("a failed send was recorded as outbound")
	}
}

// ── deterministic matchers ─────────────────────────────────────────────────

func TestYesNoAreStrict(t *testing.T) {
	for _, s := range []string{"yes", "YES", "Yes!", "y", "ok", "okay", "book it", "👍", "好", "sure"} {
		if !IsYes(s) {
			t.Errorf("%q should be yes", s)
		}
	}
	for _, s := range []string{"yes but cheaper", "maybe", "not yet", "yes?? how much", "yesterday", "ok but 4pm"} {
		if IsYes(s) {
			t.Errorf("%q must not be read as a yes", s)
		}
	}
	for _, s := range []string{"no", "NO", "cancel", "never mind", "取消"} {
		if !IsNo(s) {
			t.Errorf("%q should be no", s)
		}
	}
}

func TestCleanName(t *testing.T) {
	cases := map[string]string{"Aura": "Aura", "I'm Aura": "Aura", "my name is Aura Tao": "Aura Tao", "aura.": "aura"}
	for in, want := range cases {
		if got := CleanName(in); got != want {
			t.Errorf("CleanName(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"pick up my laundry at 3pm please thanks", "555-1234", "https://x.y", ""} {
		if got := CleanName(in); got != "" {
			t.Errorf("CleanName(%q) = %q, want empty", in, got)
		}
	}
}

func TestSummaryTimeFormatting(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	nyc := g.r.Location
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, nyc)
	g.r.Now = func() time.Time { return now }
	cases := map[string]string{
		time.Date(2026, 10, 20, 15, 0, 0, 0, nyc).Format(time.RFC3339): "Today, 3:00 PM",
		time.Date(2026, 10, 21, 9, 30, 0, 0, nyc).Format(time.RFC3339): "Tomorrow, 9:30 AM",
		time.Date(2026, 10, 24, 18, 0, 0, 0, nyc).Format(time.RFC3339): "Sat, Oct 24, 6:00 PM",
	}
	for rfc, want := range cases {
		if got := g.r.when(true, rfc); got != want {
			t.Errorf("when(%s) = %q, want %q", rfc, got, want)
		}
	}
	if got := g.r.when(false, ""); got != "As soon as possible" {
		t.Errorf("asap: %q", got)
	}
	if Dollars(1200) != "$12" || Dollars(1250) != "$12.50" || Dollars(5) != "$0.05" {
		t.Error("Dollars formatting")
	}
}

func TestSetupTokensAreUniqueAndUnguessable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok, hash, err := NewSetupToken()
		if err != nil {
			t.Fatal(err)
		}
		if len(tok) != 43 || seen[tok] || hash != HashSetupToken(tok) || len(hash) != 64 {
			t.Fatalf("bad token %q / %q", tok, hash)
		}
		seen[tok] = true
	}
}

func TestFirstRepliesStayShort(t *testing.T) {
	for _, s := range []string{CopyWelcome, CopyAbout, CopyParseFailed, CopyAskNameAgain, CopySetupAlreadySent} {
		if n := len([]rune(s)); n > 300 {
			t.Errorf("%d chars: %q", n, s)
		}
	}
}

// A customer reacting to a status update ("good", "thanks") or saying hi is
// not re-introduced to the service; a stranger still is.
func TestLinkedCustomerIsNotReintroduced(t *testing.T) {
	g := newRig(llm.IntentOther)
	g.ids.link(me, "user-1")
	if got := g.say(t, "good"); got != CopyAck {
		t.Fatalf("linked + other: %q", got)
	}
	g = newRig(llm.IntentGreeting)
	g.ids.link(me, "user-1")
	if got := g.say(t, "hi"); got != CopyHelloAgain {
		t.Fatalf("linked + greeting: %q", got)
	}
	g = newRig(llm.IntentOther)
	if got := g.say(t, "good"); got != CopyWelcome {
		t.Fatalf("stranger + other: %q", got)
	}
}

// Regression (D-16 review round 3, S-11 condition 3): the person receives the
// real setup link, but the stored copy of that message never holds the raw
// token — only its hash exists, in channel_setup_tokens.
func TestSetupLinkTokenIsNeverStored(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.say(t, "laundry pickup at 12 Bedford Ave")
	g.say(t, "Aura")
	sent := g.say(t, "YES")
	i := strings.Index(sent, "#t=")
	if i < 0 {
		t.Fatalf("no link sent: %q", sent)
	}
	token := strings.Fields(sent[i+3:])[0]
	if len(token) < 32 {
		t.Fatalf("token looks wrong: %q", token)
	}
	for _, body := range g.st.storedBodies() {
		if strings.Contains(body, token) || strings.Contains(body, "#t=") {
			t.Fatalf("raw setup token stored: %q", body)
		}
	}
	found := false
	for _, body := range g.st.storedBodies() {
		if strings.Contains(body, SetupLinkPlaceholder) {
			found = true
		}
	}
	if !found {
		t.Fatal("the stored copy should keep the message with a placeholder")
	}
}

// Regression (D-16 review round 3, S-31): a database failure while claiming
// the booking is an error for the setup page, never "nothing to book".
func TestFinishSetupClaimFailureIsAnError(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.say(t, "laundry pickup at 12 Bedford Ave")
	g.say(t, "Aura")
	g.say(t, "YES") // → awaiting_setup with a draft
	g.ids.link(me, "user-1")
	g.convs.mu.Lock()
	g.convs.claimErr = errors.New("db down")
	g.convs.mu.Unlock()
	if _, _, err := g.r.FinishSetup(context.Background(), "user-1"); err == nil {
		t.Fatal("a claim failure must surface as an error")
	}
	if g.booker.posts != 0 {
		t.Fatal("nothing may be posted when the claim failed")
	}
}
