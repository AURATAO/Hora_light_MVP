package channel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ── phone ──────────────────────────────────────────────────────────────────

func TestNormalizeE164(t *testing.T) {
	ok := map[string]string{
		"+15551234567":      "+15551234567",
		"(555) 123-4567":    "+15551234567",
		"555.123.4567":      "+15551234567",
		"15551234567":       "+15551234567",  // WhatsApp sends US numbers like this
		"393331234567":      "+393331234567", // WhatsApp, Italian, no '+'
		" +44 20 7946 0958": "+442079460958",
	}
	for in, want := range ok {
		got, err := NormalizeE164(in)
		if err != nil || got != want {
			t.Errorf("NormalizeE164(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "12345", "+0123456789", "555-CALL-NOW", "+1234567890123456"} {
		if got, err := NormalizeE164(in); err == nil {
			t.Errorf("NormalizeE164(%q) = %q, want error", in, got)
		}
	}
}

func TestRedactKeepsOnlyLastTwoDigits(t *testing.T) {
	got := Redact("+15551234567")
	if got != "+*********67" || strings.Contains(got, "555") {
		t.Fatalf("Redact = %q", got)
	}
}

// ── Sendblue ───────────────────────────────────────────────────────────────

func TestSendblueVerifyFailsClosed(t *testing.T) {
	a := &SendblueAdapter{} // no secret configured
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set(defaultSendblueSecretHeader, "anything")
	if err := a.VerifyRequest(r, nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("unconfigured adapter must reject, got %v", err)
	}
}

func TestSendblueVerify(t *testing.T) {
	a := &SendblueAdapter{WebhookSecret: "s3cret", WebhookSecretHeader: "X-Test-Secret"}
	cases := map[string]bool{"s3cret": true, "s3cre": false, "": false, "s3cret ": false}
	for val, want := range cases {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		if val != "" {
			r.Header.Set("X-Test-Secret", val)
		}
		err := a.VerifyRequest(r, nil)
		if (err == nil) != want {
			t.Errorf("secret %q: err=%v, want ok=%v", val, err, want)
		}
	}
}

const sendblueInbound = `{
  "accountEmail": "ops@example.com",
  "content": "  Need someone to grab my laundry at 3pm  ",
  "is_outbound": false,
  "status": "RECEIVED",
  "message_handle": "5a17319e-cbcf-443e-897e-d8b0c04b1b09",
  "date_sent": "2026-10-19T14:00:00.000Z",
  "from_number": "+15551234567",
  "number": "+15551234567",
  "to_number": "+15559990000",
  "sendblue_number": "+15559990000",
  "service": "iMessage",
  "media_url": "",
  "group_id": "",
  "reply_to": {"message_handle": "99DCC379-DD76-4712-BA65-11EFB33B8CD6", "part_index": 0}
}`

func TestSendblueParseInbound(t *testing.T) {
	a := &SendblueAdapter{}
	msgs, err := a.ParseInbound([]byte(sendblueInbound))
	if err != nil || len(msgs) != 1 {
		t.Fatalf("parse: %v, %d msgs", err, len(msgs))
	}
	m := msgs[0]
	if m.Channel != IMessage || m.ProviderMessageID != "5a17319e-cbcf-443e-897e-d8b0c04b1b09" ||
		m.From != "+15551234567" || m.To != "+15559990000" ||
		m.Text != "Need someone to grab my laundry at 3pm" || m.Service != "iMessage" ||
		m.ReplyToProviderID != "99DCC379-DD76-4712-BA65-11EFB33B8CD6" || m.ReceivedAt.IsZero() {
		t.Fatalf("unexpected inbound: %+v", m)
	}
}

func TestSendblueParseDropsOutboundAndGroups(t *testing.T) {
	a := &SendblueAdapter{}
	for _, mut := range []func(map[string]any){
		func(p map[string]any) { p["is_outbound"] = true },
		func(p map[string]any) { p["group_id"] = "g-1" },
	} {
		var p map[string]any
		if err := json.Unmarshal([]byte(sendblueInbound), &p); err != nil {
			t.Fatal(err)
		}
		mut(p)
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		msgs, err := a.ParseInbound(b)
		if err != nil || len(msgs) != 0 {
			t.Fatalf("expected drop, got %v %+v", err, msgs)
		}
	}
}

func TestSendblueSend(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/send-message" ||
			r.Header.Get("sb-api-key-id") != "k" || r.Header.Get("sb-api-secret-key") != "s" {
			http.Error(w, "bad", 401)
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if err := json.Unmarshal(b, &got); err != nil {
			t.Error(err)
		}
		if _, err := w.Write([]byte(`{"message_handle":"out-1","status":"QUEUED"}`)); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()

	a := &SendblueAdapter{APIKey: "k", APISecret: "s", FromNumber: "+15559990000", BaseURL: srv.URL}
	id, err := a.Send(context.Background(), Outbound{Channel: IMessage, To: "+15551234567", Text: "Hi", ReplyToProviderID: "in-1"})
	if err != nil || id != "out-1" {
		t.Fatalf("send: %q %v", id, err)
	}
	if got["number"] != "+15551234567" || got["from_number"] != "+15559990000" || got["content"] != "Hi" {
		t.Fatalf("request body: %+v", got)
	}
	if rt, _ := got["reply_to"].(map[string]any); rt["message_handle"] != "in-1" {
		t.Fatalf("reply_to not sent: %+v", got)
	}
}

func TestSendblueSendDeclineIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"contact not verified"}`, 400)
	}))
	defer srv.Close()
	a := &SendblueAdapter{APIKey: "k", APISecret: "s", FromNumber: "+15559990000", BaseURL: srv.URL}
	if _, err := a.Send(context.Background(), Outbound{To: "+15551234567", Text: "x"}); err == nil {
		t.Fatal("400 must be an error")
	}
}

// ── WhatsApp ───────────────────────────────────────────────────────────────

const waInbound = `{"object":"whatsapp_business_account","entry":[{"id":"1","changes":[{"field":"messages","value":{
 "messaging_product":"whatsapp",
 "metadata":{"display_phone_number":"15559990000","phone_number_id":"111"},
 "contacts":[{"wa_id":"15551234567","profile":{"name":"Test"}}],
 "messages":[
   {"from":"15551234567","id":"wamid.A","timestamp":"1760882400","type":"text","text":{"body":"YES"},"context":{"from":"15559990000","id":"wamid.OUT"}},
   {"from":"15551234567","id":"wamid.B","timestamp":"1760882401","type":"interactive","interactive":{"type":"button_reply","button_reply":{"id":"no","title":"NO"}}}
 ]}}]}]}`

func sign(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	if _, err := m.Write([]byte(body)); err != nil {
		panic(err) // test helper: a failed in-memory hash is a broken test
	}
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestWhatsAppVerify(t *testing.T) {
	a := &WhatsAppAdapter{AppSecret: "app-secret"}
	body := []byte(waInbound)
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("X-Hub-Signature-256", sign("app-secret", waInbound))
	if err := a.VerifyRequest(r, body); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	// Any change to the body breaks it.
	if err := a.VerifyRequest(r, append(body, ' ')); err == nil {
		t.Fatal("tampered body accepted")
	}
	r.Header.Set("X-Hub-Signature-256", sign("wrong", waInbound))
	if err := a.VerifyRequest(r, body); err == nil {
		t.Fatal("wrong secret accepted")
	}
	if err := (&WhatsAppAdapter{}).VerifyRequest(r, body); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("unconfigured adapter must reject, got %v", err)
	}
}

func TestWhatsAppParseInbound(t *testing.T) {
	msgs, err := (&WhatsAppAdapter{}).ParseInbound([]byte(waInbound))
	if err != nil || len(msgs) != 2 {
		t.Fatalf("parse: %v, %d", err, len(msgs))
	}
	if m := msgs[0]; m.From != "+15551234567" || m.To != "+15559990000" || m.Text != "YES" ||
		m.ProviderMessageID != "wamid.A" || m.ReplyToProviderID != "wamid.OUT" || m.Channel != WhatsApp {
		t.Fatalf("msg 0: %+v", m)
	}
	if msgs[1].Text != "NO" {
		t.Fatalf("button reply text: %+v", msgs[1])
	}
}

func TestWhatsAppVerifySubscribe(t *testing.T) {
	a := &WhatsAppAdapter{VerifyToken: "tok"}
	if c, ok := a.VerifySubscribe("subscribe", "tok", "123"); !ok || c != "123" {
		t.Fatal("valid handshake rejected")
	}
	if _, ok := a.VerifySubscribe("subscribe", "bad", "123"); ok {
		t.Fatal("bad token accepted")
	}
	if _, ok := (&WhatsAppAdapter{}).VerifySubscribe("subscribe", "", "123"); ok {
		t.Fatal("unconfigured token accepted an empty token")
	}
}

func TestWhatsAppSend(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v21.0/111/messages" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "bad", 401)
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if err := json.Unmarshal(b, &got); err != nil {
			t.Error(err)
		}
		if _, err := w.Write([]byte(`{"messaging_product":"whatsapp","messages":[{"id":"wamid.NEW"}]}`)); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	a := &WhatsAppAdapter{AccessToken: "tok", PhoneNumberID: "111", BaseURL: srv.URL}
	id, err := a.Send(context.Background(), Outbound{Channel: WhatsApp, To: "+15551234567", Text: "Hi", ReplyToProviderID: "wamid.A"})
	if err != nil || id != "wamid.NEW" {
		t.Fatalf("send: %q %v", id, err)
	}
	if got["to"] != "15551234567" {
		t.Fatalf("number must be sent without '+': %+v", got)
	}
	if ctx, _ := got["context"].(map[string]any); ctx["message_id"] != "wamid.A" {
		t.Fatalf("context not sent: %+v", got)
	}
}

// ── WebhookHandler ─────────────────────────────────────────────────────────

type fakeAdapter struct {
	verifyErr error
	msgs      []Inbound
	parseErr  error
}

func (f *fakeAdapter) Channel() Channel                               { return IMessage }
func (f *fakeAdapter) VerifyRequest(*http.Request, []byte) error      { return f.verifyErr }
func (f *fakeAdapter) ParseInbound([]byte) ([]Inbound, error)         { return f.msgs, f.parseErr }
func (f *fakeAdapter) Send(context.Context, Outbound) (string, error) { return "", nil }

type fakeStore struct {
	mu        sync.Mutex
	seen      map[string]bool
	processed []string
	outbound  int
	lastTask  string
	lastID    string
	lastRelay bool
	stored    []string
	err       error
}

func (s *fakeStore) RecordInbound(_ context.Context, in Inbound) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	if s.seen[in.ProviderMessageID] {
		return false, nil
	}
	s.seen[in.ProviderMessageID] = true
	return true, nil
}
func (s *fakeStore) MarkProcessed(_ context.Context, _ Channel, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.processed = append(s.processed, id)
	return nil
}
func (s *fakeStore) RecordOutbound(_ context.Context, o Outbound, providerID string, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outbound++
	s.lastTask = taskID
	s.lastID = providerID
	s.lastRelay = o.Relay
	s.stored = append(s.stored, o.RecordedText())
	return nil
}
func (s *fakeStore) storedBodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.stored...)
}
func (s *fakeStore) lastOutboundRelay() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRelay
}
func (s *fakeStore) lastOutboundID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastID
}
func (s *fakeStore) lastOutboundTask() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTask
}

type fakeRouter struct {
	got []string
	err error
}

func (r *fakeRouter) HandleInbound(_ context.Context, in Inbound) error {
	r.got = append(r.got, in.ProviderMessageID)
	return r.err
}

func newHandler(a Adapter, s Store, r InboundRouter) *WebhookHandler {
	return &WebhookHandler{Adapter: a, Store: s, Router: r, async: func(f func()) { f() }}
}

func post(h http.Handler) int {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
	return w.Code
}

func TestHandlerRejectsUnverifiedAndStoresNothing(t *testing.T) {
	st, rt := &fakeStore{}, &fakeRouter{}
	a := &fakeAdapter{verifyErr: ErrUnverified, msgs: []Inbound{{ProviderMessageID: "m1"}}}
	if code := post(newHandler(a, st, rt)); code != http.StatusUnauthorized {
		t.Fatalf("code %d", code)
	}
	if len(st.seen) != 0 || len(rt.got) != 0 {
		t.Fatal("unverified webhook was stored or routed")
	}
}

func TestHandlerDedupesRetries(t *testing.T) {
	st, rt := &fakeStore{}, &fakeRouter{}
	h := newHandler(&fakeAdapter{msgs: []Inbound{{Channel: IMessage, ProviderMessageID: "m1"}}}, st, rt)
	for i := 0; i < 3; i++ { // provider retried twice
		if code := post(h); code != http.StatusOK {
			t.Fatalf("attempt %d: code %d", i, code)
		}
	}
	if len(rt.got) != 1 || len(st.processed) != 1 {
		t.Fatalf("routed %d times, processed %d; want exactly once", len(rt.got), len(st.processed))
	}
}

func TestHandlerStoreFailureAsksForRetry(t *testing.T) {
	st, rt := &fakeStore{err: errors.New("db down")}, &fakeRouter{}
	h := newHandler(&fakeAdapter{msgs: []Inbound{{ProviderMessageID: "m1"}}}, st, rt)
	if code := post(h); code != http.StatusInternalServerError {
		t.Fatalf("code %d, want 500 so the provider retries", code)
	}
	if len(rt.got) != 0 {
		t.Fatal("routed a message that was never recorded")
	}
}

func TestHandlerRouteFailureLeavesUnprocessed(t *testing.T) {
	st, rt := &fakeStore{}, &fakeRouter{err: errors.New("llm timeout")}
	h := newHandler(&fakeAdapter{msgs: []Inbound{{ProviderMessageID: "m1"}}}, st, rt)
	if code := post(h); code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
	if len(st.processed) != 0 {
		t.Fatal("failed route was marked processed; the recovery sweep would skip it")
	}
}

func TestHandlerParseErrorIsAcked(t *testing.T) {
	h := newHandler(&fakeAdapter{parseErr: errors.New("unknown shape")}, &fakeStore{}, &fakeRouter{})
	if code := post(h); code != http.StatusOK {
		t.Fatalf("code %d; an unreadable verified payload must not be retried forever", code)
	}
}

func TestHandlerRejectsGet(t *testing.T) {
	w := httptest.NewRecorder()
	newHandler(&fakeAdapter{}, &fakeStore{}, &fakeRouter{}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code %d", w.Code)
	}
}

// ── media (M3.2 relay: a supporter's photo rides along) ────────────────────

func capturePayload(t *testing.T, reply string) (*httptest.Server, *map[string]any) {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = map[string]any{}
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestSendblueSendMedia(t *testing.T) {
	srv, got := capturePayload(t, `{"message_handle":"out-2","status":"QUEUED"}`)
	a := &SendblueAdapter{APIKey: "k", APISecret: "s", FromNumber: "+15559990000", BaseURL: srv.URL}
	if _, err := a.Send(context.Background(), Outbound{To: "+15551234567", Text: "Maya · Laundry: done", MediaURL: "https://cdn.example/p.jpg"}); err != nil {
		t.Fatal(err)
	}
	if (*got)["media_url"] != "https://cdn.example/p.jpg" || (*got)["content"] != "Maya · Laundry: done" {
		t.Fatalf("%+v", *got)
	}
	a.Send(context.Background(), Outbound{To: "+15551234567", Text: "plain"})
	if _, has := (*got)["media_url"]; has {
		t.Fatalf("no media_url without media: %+v", *got)
	}
}

func TestWhatsAppSendMedia(t *testing.T) {
	srv, got := capturePayload(t, `{"messages":[{"id":"wamid.M"}]}`)
	a := &WhatsAppAdapter{AccessToken: "tok", PhoneNumberID: "111", BaseURL: srv.URL}
	if _, err := a.Send(context.Background(), Outbound{To: "+15551234567", Text: "Maya · Laundry: done", MediaURL: "https://cdn.example/p.jpg?sig=1"}); err != nil {
		t.Fatal(err)
	}
	img, _ := (*got)["image"].(map[string]any)
	if (*got)["type"] != "image" || img["link"] != "https://cdn.example/p.jpg?sig=1" || img["caption"] != "Maya · Laundry: done" {
		t.Fatalf("%+v", *got)
	}
	if _, has := (*got)["text"]; has {
		t.Fatalf("image message carries no text block: %+v", *got)
	}
	// Not an image (a PDF receipt): text with the link, never an image
	// message Meta would reject — and the words are not lost.
	if _, err := a.Send(context.Background(), Outbound{To: "+15551234567", Text: "Receipt", MediaURL: "https://cdn.example/r.pdf"}); err != nil {
		t.Fatal(err)
	}
	txt, _ := (*got)["text"].(map[string]any)
	if (*got)["type"] != "text" || txt["body"] != "Receipt\nhttps://cdn.example/r.pdf" {
		t.Fatalf("%+v", *got)
	}
	// A caption over Meta's 1024 cap falls back the same way.
	long := strings.Repeat("x", 1100)
	a.Send(context.Background(), Outbound{To: "+15551234567", Text: long, MediaURL: "https://cdn.example/p.png"})
	if (*got)["type"] != "text" {
		t.Fatalf("long caption must fall back to text: %v", (*got)["type"])
	}
}
