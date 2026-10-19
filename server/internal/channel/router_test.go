package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ── fakes ──────────────────────────────────────────────────────────────────

type memIdentities struct {
	ids       map[string]Identity // key channel|address
	tokens    []memToken
	attached  map[string]string
	touched   map[string]time.Time
	failUpser error
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
	if m.failUpser != nil {
		return Identity{}, m.failUpser
	}
	k := string(ch) + "|" + addr
	if id, ok := m.ids[k]; ok {
		return id, nil
	}
	id := Identity{ID: "id-" + addr, Channel: ch, Address: addr}
	m.ids[k] = id
	return id, nil
}
func (m *memIdentities) AttachMessage(_ context.Context, _ Channel, pid, iid string) error {
	m.attached[pid] = iid
	return nil
}
func (m *memIdentities) TouchConversation(_ context.Context, iid string, at time.Time) error {
	m.touched[iid] = at
	return nil
}
func (m *memIdentities) SetupTokenIssuedSince(_ context.Context, iid string, since time.Time) (bool, error) {
	for _, t := range m.tokens {
		if t.identityID == iid && t.created.After(since) && !t.used {
			return true, nil
		}
	}
	return false, nil
}
func (m *memIdentities) CreateSetupToken(_ context.Context, iid, hash string, exp time.Time) error {
	m.tokens = append(m.tokens, memToken{identityID: iid, hash: hash, created: time.Now(), expires: exp})
	return nil
}
func (m *memIdentities) PeekSetupToken(context.Context, string) (Identity, time.Time, error) {
	return Identity{}, time.Time{}, ErrTokenInvalid
}
func (m *memIdentities) ConsumeSetupTokenAndLink(context.Context, string, string) (Identity, error) {
	return Identity{}, ErrTokenInvalid
}

// fakeSendAdapter records sends and returns sequential provider ids.
type fakeSendAdapter struct {
	fakeAdapter
	sent []Outbound
	err  error
}

func (a *fakeSendAdapter) Send(_ context.Context, o Outbound) (string, error) {
	if a.err != nil {
		return "", a.err
	}
	a.sent = append(a.sent, o)
	return "out-" + string(rune('a'+len(a.sent))), nil
}

// ── tests ──────────────────────────────────────────────────────────────────

func newTestRouter() (*SetupRouter, *memIdentities, *fakeSendAdapter, *fakeStore) {
	ids := newMemIdentities()
	ad := &fakeSendAdapter{}
	st := &fakeStore{}
	r := &SetupRouter{
		Messages:   st,
		Identities: ids,
		Adapters:   map[Channel]Adapter{IMessage: ad},
		SetupURL:   func(t string) string { return "https://example.test/channel/setup?t=" + t },
	}
	return r, ids, ad, st
}

func inbound(id string) Inbound {
	return Inbound{Channel: IMessage, ProviderMessageID: id, From: "+15551234567", Text: "hi", ReceivedAt: time.Now()}
}

func TestRouterUnknownNumberGetsOneSetupLink(t *testing.T) {
	r, ids, ad, _ := newTestRouter()
	if err := r.HandleInbound(context.Background(), inbound("m1")); err != nil {
		t.Fatal(err)
	}
	if len(ad.sent) != 1 || !strings.Contains(ad.sent[0].Text, "https://example.test/channel/setup?t=") {
		t.Fatalf("expected one setup link, got %+v", ad.sent)
	}
	if ad.sent[0].To != "+15551234567" || ad.sent[0].Channel != IMessage {
		t.Fatalf("reply went to the wrong place: %+v", ad.sent[0])
	}
	if len(ids.tokens) != 1 {
		t.Fatalf("tokens stored: %d", len(ids.tokens))
	}
	// The raw token in the link must hash to what was stored, and the raw
	// token itself must never be stored.
	raw := ad.sent[0].Text[strings.Index(ad.sent[0].Text, "?t=")+3:]
	raw = strings.Fields(raw)[0]
	if HashSetupToken(raw) != ids.tokens[0].hash || ids.tokens[0].hash == raw {
		t.Fatal("stored hash does not match the link token, or the raw token was stored")
	}
	if ids.attached["m1"] == "" || ids.touched["id-+15551234567"].IsZero() {
		t.Fatal("message not attached to identity or conversation not touched")
	}

	// Texting again right away does not mint a second link.
	if err := r.HandleInbound(context.Background(), inbound("m2")); err != nil {
		t.Fatal(err)
	}
	if len(ids.tokens) != 1 || len(ad.sent) != 2 || ad.sent[1].Text != CopySetupAlreadySent {
		t.Fatalf("second message: tokens=%d sent=%+v", len(ids.tokens), ad.sent)
	}
}

func TestRouterNewLinkAfterResendWindow(t *testing.T) {
	r, ids, _, _ := newTestRouter()
	clock := time.Now()
	r.Now = func() time.Time { return clock }
	_ = r.HandleInbound(context.Background(), inbound("m1"))
	ids.tokens[0].created = clock.Add(-3 * time.Minute) // first link is old now
	_ = r.HandleInbound(context.Background(), inbound("m2"))
	if len(ids.tokens) != 2 {
		t.Fatalf("expected a fresh link after %v, tokens=%d", resendAfter, len(ids.tokens))
	}
}

func TestRouterLinkedNumberIsAcknowledged(t *testing.T) {
	r, ids, ad, _ := newTestRouter()
	ids.ids["imessage|+15551234567"] = Identity{ID: "id-1", Channel: IMessage, Address: "+15551234567", UserID: "user-1"}
	if err := r.HandleInbound(context.Background(), inbound("m1")); err != nil {
		t.Fatal(err)
	}
	if len(ids.tokens) != 0 {
		t.Fatal("a linked number must not get a setup link")
	}
	if len(ad.sent) != 1 || ad.sent[0].Text != CopyLinkedAck {
		t.Fatalf("sent %+v", ad.sent)
	}
}

func TestRouterLinkedHookTakesOver(t *testing.T) {
	r, ids, ad, _ := newTestRouter()
	ids.ids["imessage|+15551234567"] = Identity{ID: "id-1", Channel: IMessage, Address: "+15551234567", UserID: "user-1"}
	var got Identity
	r.Linked = func(_ context.Context, _ Inbound, id Identity) error { got = id; return nil }
	_ = r.HandleInbound(context.Background(), inbound("m1"))
	if got.UserID != "user-1" || len(ad.sent) != 0 {
		t.Fatalf("hook not used: got=%+v sent=%d", got, len(ad.sent))
	}
}

func TestRouterSendFailureIsReturned(t *testing.T) {
	r, _, ad, st := newTestRouter()
	ad.err = errors.New("sendblue 400")
	if err := r.HandleInbound(context.Background(), inbound("m1")); err == nil {
		t.Fatal("send failure must surface so the message stays unprocessed")
	}
	if st.outbound != 0 {
		t.Fatal("a failed send was recorded as outbound")
	}
}

func TestRouterRecordsOutbound(t *testing.T) {
	r, _, _, st := newTestRouter()
	_ = r.HandleInbound(context.Background(), inbound("m1"))
	if st.outbound != 1 {
		t.Fatalf("outbound recorded %d times", st.outbound)
	}
}

func TestSetupDoneText(t *testing.T) {
	if got := SetupDoneText("Aura Tao"); !strings.HasPrefix(got, "You're all set, Aura.") {
		t.Fatalf("%q", got)
	}
	if got := SetupDoneText(""); !strings.HasPrefix(got, "You're all set.") {
		t.Fatalf("%q", got)
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

func TestCopyFitsBeforeReplyLimit(t *testing.T) {
	// Sendblue caps pre-reply messages at 300 chars. Ours always answer the
	// person, but keep the setup message under it with a realistic URL.
	url := "https://hora-staging-messaging.onrender.com/channel/setup?t=" + strings.Repeat("x", 43)
	for _, s := range []string{fmtSetup(url), CopySetupAlreadySent, CopyLinkedAck, SetupDoneText("Alexandra")} {
		if n := len([]rune(s)); n > 300 {
			t.Errorf("%d chars: %q", n, s)
		}
	}
}

func fmtSetup(url string) string { return strings.Replace(CopySetupLink, "%s", url, 1) }
