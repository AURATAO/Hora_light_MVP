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
	// convs, when set, gets last_inbound_at like the PG row (GREATEST).
	convs *memConversations
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
	cv := m.convs
	m.mu.Unlock()
	if cv != nil {
		cv.mu.Lock()
		c := cv.convs[iid]
		if c.LastInboundAt == nil || at.After(*c.LastInboundAt) {
			c.IdentityID, c.LastInboundAt = iid, &at
			if c.State == "" {
				c.State = StateIdle
			}
			cv.convs[iid] = c
		}
		cv.mu.Unlock()
	}
	m.mu.Lock()
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
	touchErr error // when set, TouchOutbound fails
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
	if c.PendingCancel != nil { // a copy, like a row read
		pc := *c.PendingCancel
		c.PendingCancel = &pc
	}
	return c, nil
}
func (m *memConversations) SaveConversation(_ context.Context, c Conversation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.convs[c.IdentityID]; ok {
		if old.TermsAcceptedAt != nil {
			c.TermsAcceptedAt = old.TermsAcceptedAt // COALESCE, like the SQL
		}
		c.Deferred = old.Deferred // not in the upsert; HoldApproval/RemoveHold only
		c.LastInboundAt = old.LastInboundAt
		if old.State == StateAwaitingApproval && c.State != StateHandoff {
			// A stale snapshot must not disarm or restamp a question (the SQL CASE).
			c.State, c.PendingExtensionID, c.ApprovalArmedAt, c.ApprovalClaimedAt = old.State, old.PendingExtensionID, old.ApprovalArmedAt, old.ApprovalClaimedAt
			c.ApprovalTaskID, c.ApprovalQuestionOutAt, c.ApprovalQuestionPID = old.ApprovalTaskID, old.ApprovalQuestionOutAt, old.ApprovalQuestionPID
		} else {
			c.ApprovalClaimedAt, c.ApprovalQuestionOutAt, c.ApprovalTaskID, c.ApprovalQuestionPID = nil, nil, "", ""
		}
		c.LastOutboundAt = old.LastOutboundAt // not in the upsert
		if old.QuestionShownAt != nil && (c.QuestionShownAt == nil || old.QuestionShownAt.After(*c.QuestionShownAt)) {
			c.QuestionShownAt = old.QuestionShownAt // GREATEST, like the SQL
		}
	} else {
		c.Deferred = nil
		c.LastInboundAt = nil
	}
	m.convs[c.IdentityID] = c
	return nil
}
func (m *memConversations) touch(iid string, at time.Time) { m.setLastInbound(iid, &at) }

// TouchOutbound mirrors the SQL: stamps the conversation of the identity
// with this address (the rig's ids are "id-"+address).
func (m *memConversations) TouchOutbound(_ context.Context, _ Channel, address string) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.touchErr != nil {
		return time.Time{}, m.touchErr
	}
	iid := "id-" + address
	c, ok := m.convs[iid]
	if !ok {
		return time.Time{}, nil // like the UPDATE: no row, nothing done
	}
	now := time.Now()
	if c.LastOutboundAt != nil && !now.After(*c.LastOutboundAt) {
		now = c.LastOutboundAt.Add(time.Nanosecond) // strictly later, like clock_timestamp()
	}
	c.LastOutboundAt = &now
	m.convs[iid] = c
	return now, nil
}
func (m *memConversations) MarkQuestionShown(_ context.Context, iid string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.convs[iid]
	if !ok {
		return nil
	}
	if c.QuestionShownAt == nil || !c.QuestionShownAt.Before(ShownUnconfirmed) || at.After(*c.QuestionShownAt) {
		c.QuestionShownAt = &at // the in-flight mark is replaced; otherwise forward only
	}
	m.convs[iid] = c
	return nil
}
func (m *memConversations) setLastInbound(iid string, at *time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.convs[iid]
	c.IdentityID, c.LastInboundAt = iid, at
	if c.State == "" {
		c.State = StateIdle
	}
	m.convs[iid] = c
}
func (m *memConversations) HoldApproval(_ context.Context, iid string, d DeferredApproval) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.convs[iid]
	if !ok {
		c = Conversation{IdentityID: iid, State: StateIdle}
	}
	if c.State == StateAwaitingApproval && c.PendingExtensionID == d.ExtensionID {
		return nil // the armed one is not re-held
	}
	for _, h := range c.Deferred {
		if h.ExtensionID == d.ExtensionID {
			return nil
		}
	}
	c.Deferred = append(append([]DeferredApproval(nil), c.Deferred...), d)
	m.convs[iid] = c
	return nil
}
func (m *memConversations) RemoveHold(_ context.Context, iid, ext string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.convs[iid]
	if !ok || (c.State == StateAwaitingApproval && c.PendingExtensionID == ext) {
		return nil // like the UPDATE: no row, or the armed request's own hold
	}
	var kept []DeferredApproval
	for _, h := range c.Deferred {
		if h.ExtensionID != ext {
			kept = append(kept, h)
		}
	}
	c.Deferred = kept
	m.convs[iid] = c
	return nil
}
func (m *memConversations) ClaimHold(_ context.Context, iid, ext, task string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.convs[iid]
	if !ok || !canArmApproval(c.State) {
		return false, nil
	}
	found := false
	for _, h := range c.Deferred {
		if h.ExtensionID == ext {
			found = true
		}
	}
	if !found {
		return false, nil
	}
	c.State, c.PendingExtensionID, c.ApprovalTaskID, c.ApprovalQuestionOutAt, c.ApprovalQuestionPID = StateAwaitingApproval, ext, task, nil, ""
	c.PendingCancel = nil // like the SQL
	sentinel := ArmedUnconfirmed
	c.ApprovalArmedAt, c.ApprovalClaimedAt = &sentinel, &at
	m.convs[iid] = c
	return true, nil
}
func (m *memConversations) ReclaimUnconfirmed(_ context.Context, iid, ext string, seen *time.Time, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.convs[iid]
	if !ok || !c.Unconfirmed() || c.PendingExtensionID != ext {
		return false, nil
	}
	if (seen == nil) != (c.ApprovalClaimedAt == nil) || (seen != nil && !seen.Equal(*c.ApprovalClaimedAt)) {
		return false, nil
	}
	c.ApprovalClaimedAt = &at
	m.convs[iid] = c
	return true, nil
}
func (m *memConversations) ConfirmArmed(_ context.Context, iid, ext string, at, questionOut time.Time, questionPID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.convs[iid]
	if !ok || c.State != StateAwaitingApproval || c.PendingExtensionID != ext {
		return false, nil
	}
	var kept []DeferredApproval
	for _, h := range c.Deferred {
		if h.ExtensionID != ext {
			kept = append(kept, h)
		}
	}
	c.Deferred, c.ApprovalArmedAt = kept, &at
	c.ApprovalQuestionOutAt, c.ApprovalQuestionPID = nil, questionPID
	if !questionOut.IsZero() {
		c.ApprovalQuestionOutAt = &questionOut // the question's own stamp
	}
	m.convs[iid] = c
	return true, nil
}
func (m *memConversations) ClearApproval(_ context.Context, iid, ext string, resume State, shownAt time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.convs[iid]
	if !ok || c.State != StateAwaitingApproval || c.PendingExtensionID != ext {
		return false, nil
	}
	c.State, c.PendingExtensionID, c.ApprovalArmedAt, c.ApprovalClaimedAt = resume, "", nil, nil
	c.ApprovalTaskID, c.ApprovalQuestionOutAt, c.ApprovalQuestionPID = "", nil, ""
	if c.QuestionShownAt == nil || shownAt.After(*c.QuestionShownAt) {
		c.QuestionShownAt = &shownAt
	}
	m.convs[iid] = c
	return true, nil
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
func (m *memConversations) ClaimCancel(_ context.Context, iid, task string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.convs[iid]
	if !ok || c.State != StateAwaitingCancel || c.PendingCancel == nil || c.PendingCancel.TaskID != task {
		return false, nil
	}
	c.State, c.PendingCancel = StateIdle, nil
	m.convs[iid] = c
	return true, nil
}

func (m *memConversations) AskCancel(_ context.Context, iid string, pc PendingCancel, shownAt time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.convs[iid]
	if !ok || (c.State != StateIdle && c.State != StateAwaitingCancel) {
		return false, nil
	}
	c.State, c.PendingCancel = StateAwaitingCancel, &pc
	if c.QuestionShownAt == nil || shownAt.After(*c.QuestionShownAt) {
		c.QuestionShownAt = &shownAt // GREATEST, like the SQL
	}
	m.convs[iid] = c
	return true, nil
}

func (m *memConversations) HoldDraft(_ context.Context, iid, ext string, d PendingDraft) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.convs[iid]
	if !ok || c.State != StateAwaitingApproval || c.PendingExtensionID != ext || ext == "" {
		return false, nil
	}
	c.Pending = &d
	m.convs[iid] = c
	return true, nil
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
	mu         sync.Mutex
	pendingExt map[string]bool // ExtensionPending answers
	pendingErr error
	during     func() // runs inside Post, while the thread is "posting"
	quotes     int
	posts      int
	result     PostResult
	delay      time.Duration
	applied    []string
	// approvals (M3)
	extResult ExtensionResult
	resolved  []string
	// multi-errand (M3): titles posted in order; per-call results when set
	posted  []string
	results []PostResult
	opts    []PostOptions
	// cancel by chat
	open      []TaskRef
	quote     CancelQuote
	quotes2   []CancelQuote // successive CancelQuote answers when set
	onQuote   func()        // runs at each CancelQuote (a parallel message, in tests)
	onPrice   func()        // runs at each draft Quote
	quoteErr  error         // CancelQuote fails with it when set
	onOpen    func()        // runs at each OpenTasks
	cancelled []string
	cancelRes CancelResult
}

func (b *fakeBooker) OpenTasks(context.Context, string) ([]TaskRef, error) {
	if b.onOpen != nil {
		b.onOpen()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open, nil
}
func (b *fakeBooker) CancelQuote(_ context.Context, _, taskID string) (CancelQuote, error) {
	if b.onQuote != nil {
		b.onQuote()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.quoteErr != nil {
		return CancelQuote{}, b.quoteErr
	}
	if len(b.quotes2) > 0 {
		q := b.quotes2[0]
		b.quotes2 = b.quotes2[1:]
		return q, nil
	}
	return b.quote, nil
}
func (b *fakeBooker) Cancel(_ context.Context, _, taskID string) CancelResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cancelled = append(b.cancelled, taskID)
	return b.cancelRes
}

func (b *fakeBooker) Quote(_ context.Context, _ llm.TaskDraft, budgetCents int) (int, int, error) {
	if b.onPrice != nil {
		b.onPrice()
	}
	b.mu.Lock()
	b.quotes++
	b.mu.Unlock()
	return 1200 + budgetCents, 30, nil
}
func (b *fakeBooker) Post(_ context.Context, _ string, d llm.TaskDraft, opts PostOptions) PostResult {
	time.Sleep(b.delay)
	if b.during != nil {
		b.during()
	}
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
func (b *fakeBooker) ExtensionPending(_ context.Context, _, ext string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pendingErr != nil {
		return false, b.pendingErr
	}
	if b.pendingExt == nil {
		return true, nil // pending unless a test says otherwise
	}
	return b.pendingExt[ext], nil
}
func (b *fakeBooker) ApplyChatProfile(_ context.Context, user, name string, _ *time.Time) error {
	b.applied = append(b.applied, user+":"+name)
	return nil
}

// fakeSendAdapter records sends.
type fakeSendAdapter struct {
	fakeAdapter
	mu    sync.Mutex
	sent  []Outbound
	calls int
	err   error
	// mediaErr fails any send that carries a photo (a provider refusing
	// the media), leaving text-only sends alone.
	mediaErr error
}

func (a *fakeSendAdapter) Send(_ context.Context, o Outbound) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.err != nil {
		return "", a.err
	}
	if a.mediaErr != nil && o.MediaURL != "" {
		return "", a.mediaErr
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
	ids.convs = convs
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

// arm asks an approval question through the real path (hold → claim →
// send → confirm), as NotifyStatus does; the question is the last send.
func (g *rig) arm(t *testing.T, ext string) {
	t.Helper()
	if g.r.Status == nil {
		g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	}
	kind, mins, cents := "budget", 0, 1000
	if strings.HasPrefix(ext, "ext-t") {
		kind, mins, cents = "time", 15, 0
	}
	err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: evBudgetRequested, TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam",
		ExtensionID: ext, ApprovalMinutes: 5, Approval: &PendingApproval{ID: ext, Kind: kind, Minutes: mins, Cents: cents}})
	if err != nil {
		t.Fatal(err)
	}
	if g.state() != StateAwaitingApproval {
		t.Fatalf("arm %s: state %s", ext, g.state())
	}
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

// Staging 2026-10-24: a link's preview arrives as extra empty messages a
// moment later — no "text only" reply to those; a lone empty message (a
// photo with nothing else) still gets it.
func TestLinkPreviewIsSilent(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	now := time.Now()
	for i, text := range []string{"https://maps.app.goo.gl/abc", "", ""} {
		in := Inbound{Channel: IMessage, ProviderMessageID: fmt.Sprintf("lp-%d", i), From: me, Text: text, ReceivedAt: now.Add(time.Duration(i) * time.Second)}
		if err := g.r.HandleInbound(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range g.ad.sent {
		if strings.Contains(m.Text, CopyTextOnly) {
			t.Fatalf("preview answered: %q", m.Text)
		}
	}
	g2 := newRig(llm.IntentNewTask)
	if err := g2.r.HandleInbound(context.Background(), Inbound{Channel: IMessage, ProviderMessageID: "alone", From: me, Text: "", ReceivedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if g2.ad.last() != CopyTextOnly {
		t.Fatalf("lone empty message: %q", g2.ad.last())
	}
}
