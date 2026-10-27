package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"hora-auth/internal/channel"
	"hora-auth/internal/notify"
)

// S-13: every ops route of the chat refuses a request with no session (401
// from dualAuth) before touching anything — no store, no router needed to
// prove it, which is the point: the control is on the route, not in the
// handler.
func TestChannelAdminRoutesRequireASession(t *testing.T) {
	r := gin.New()
	registerChannelAdminRoutes(r, nil, &channel.PGStore{}, &channel.Router{})
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/admin/channel/handoffs"},
		{http.MethodGet, "/admin/channel/00000000-0000-0000-0000-000000000001/thread"},
		{http.MethodPost, "/admin/channel/00000000-0000-0000-0000-000000000001/send"},
		{http.MethodPost, "/admin/channel/00000000-0000-0000-0000-000000000001/release"},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(rt.method, rt.path, strings.NewReader(`{"text":"hi"}`)))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session: %d, want 401", rt.method, rt.path, w.Code)
		}
	}
}

// A signed-in non-admin is refused by requireOpsAdmin (403) before the
// handler runs.
func TestChannelAdminRoutesRequireOpsAdmin(t *testing.T) {
	prev := opsAdmins
	opsAdmins = parseAdminEmails("", defaultOpsAdmins)
	t.Cleanup(func() { opsAdmins = prev })

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/channel/x/send", strings.NewReader(`{"text":"hi"}`))
	c.Set("uid", "user-1")
	c.Set("email", "nobody@example.com")
	requireOpsAdmin()(c)
	if !c.IsAborted() || w.Code != http.StatusForbidden {
		t.Fatalf("non-admin: aborted=%v code=%d", c.IsAborted(), w.Code)
	}
}

// The ops email names the person by hint and account, never by number, and
// escapes the thread (user-typed text lands in HTML).
func TestOpsNoticeCarriesNoNumberAndEscapes(t *testing.T) {
	n := channel.HandoffNotice{
		IdentityID: "id-1", Channel: channel.IMessage, PhoneHint: "•••• 4567",
		Reason: channel.HandoffHumanRequested,
		Thread: []channel.ThreadMessage{{Direction: "inbound", Text: "<script>alert(1)</script> my number is +15551234567"}},
	}
	// Build the body the same way NotifyOps does, without sending.
	body := opsNoticeHTML(n, "not linked to an account")
	if strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("thread text must be HTML-escaped")
	}
	if !strings.Contains(body, "•••• 4567") || !strings.Contains(body, "asked for a person") {
		t.Fatalf("%s", body)
	}
}

// Staging 2026-10-24: every ops email failed (no Postmark token) yet the
// notifier reported success. It now sends in line and reports whether
// anyone was reached; an SMS number counts; CHANNEL_OPS_EMAILS replaces the
// admin list for hand-off notices.
func TestOpsNotifierReportsReach(t *testing.T) {
	prev := sendOpsEmail
	defer func() { sendOpsEmail = prev }()
	var mu sync.Mutex
	var sent []string
	fail := true
	sendOpsEmail = func(p notify.EmailPayload) error {
		if fail {
			return errors.New("POSTMARK_API_TOKEN not set")
		}
		mu.Lock()
		sent = append(sent, p.To)
		mu.Unlock()
		return nil
	}
	t.Setenv("CHANNEL_OPS_EMAILS", "ops-a@example.com, Ops-B@example.com")
	t.Setenv("CHANNEL_OPS_NUMBERS", "")
	n := newOpsNotifier(nil, nil, nil)
	notice := channel.HandoffNotice{IdentityID: "i", Channel: channel.IMessage, PhoneHint: "•••• 0100", Reason: channel.HandoffHumanRequested}
	if err := n.NotifyOps(context.Background(), notice); err == nil {
		t.Fatal("no one reached must be an error")
	}
	fail = false
	if err := n.NotifyOps(context.Background(), notice); err != nil || len(sent) != 2 {
		t.Fatalf("err %v sent %v", err, sent)
	}
	sort.Strings(sent)
	if sent[1] != "ops-b@example.com" {
		t.Fatalf("addresses are lower-cased: %v", sent)
	}
	// Email down, an SMS number reachable: reached — and stamped first.
	fail = true
	t.Setenv("CHANNEL_OPS_NUMBERS", "+1 (555) 010-0199")
	sms := &stubAdapter{}
	touch := &stubToucher{}
	n = newOpsNotifier(nil, sms, touch)
	n.lookup = func(context.Context, string) (string, error) { return "+15550100777", nil }
	if err := n.NotifyOps(context.Background(), notice); err != nil || len(sms.sent) != 1 || sms.sent[0].To != "+15550100199" || !strings.Contains(sms.sent[0].Text, "0100") {
		t.Fatalf("err %v sms %+v", err, sms.sent)
	}
	if len(touch.stamped) != 1 || touch.stamped[0] != "+15550100199" {
		t.Fatalf("the text must be stamped before it is sent: %v", touch.stamped)
	}
	// Held-thread updates are not texted.
	notice.Update = true
	if err := n.NotifyOps(context.Background(), notice); err == nil || len(sms.sent) != 1 {
		t.Fatalf("update: err %v texts %d", err, len(sms.sent))
	}
	// Review 10/24 #4: no stamp, no text (fail closed) — and no stamper at all
	// is the same.
	notice.Update = false
	touch.err = errors.New("db down")
	if err := n.NotifyOps(context.Background(), notice); err == nil || len(sms.sent) != 1 {
		t.Fatalf("stamp failed: err %v texts %d", err, len(sms.sent))
	}
	noStamp := newOpsNotifier(nil, sms, nil)
	noStamp.lookup = n.lookup
	if err := noStamp.NotifyOps(context.Background(), notice); err == nil || len(sms.sent) != 1 {
		t.Fatalf("no stamper: err %v texts %d", err, len(sms.sent))
	}
	// The handed-off number itself is not texted (and is not "reached").
	touch.err = nil
	n.lookup = func(context.Context, string) (string, error) { return "+15550100199", nil }
	if err := n.NotifyOps(context.Background(), notice); err == nil || len(sms.sent) != 1 {
		t.Fatalf("own number: err %v texts %d", err, len(sms.sent))
	}
	// Lookup failed: cannot tell — no texts (fail closed).
	n.lookup = func(context.Context, string) (string, error) { return "", errors.New("db down") }
	if err := n.NotifyOps(context.Background(), notice); err == nil || len(sms.sent) != 1 {
		t.Fatalf("lookup failed: err %v texts %d", err, len(sms.sent))
	}
}

// Review 10/24 #8: the staging failure itself — the real notify.SendEmail
// with no Postmark token — is "reached no one", not success.
func TestOpsNotifierRealSendWithoutToken(t *testing.T) {
	t.Setenv("POSTMARK_API_TOKEN", "")
	t.Setenv("CHANNEL_OPS_EMAILS", "ops-a@example.com")
	t.Setenv("CHANNEL_OPS_NUMBERS", "")
	n := newOpsNotifier(nil, nil, nil)
	err := n.NotifyOps(context.Background(), channel.HandoffNotice{IdentityID: "i", Channel: channel.IMessage, Reason: channel.HandoffHumanRequested})
	if err == nil {
		t.Fatal("no token must report that no one was reached")
	}
}

// A hung email send does not hold the person's reply past opsSendTimeout.
func TestOpsNotifierEmailTimeout(t *testing.T) {
	prev := sendOpsEmail
	defer func() { sendOpsEmail = prev }()
	block := make(chan struct{})
	defer close(block)
	sendOpsEmail = func(notify.EmailPayload) error { <-block; return nil }
	prevT := opsSendTimeout
	opsSendTimeout = 200 * time.Millisecond
	defer func() { opsSendTimeout = prevT }()
	t.Setenv("CHANNEL_OPS_EMAILS", "ops-a@example.com")
	t.Setenv("CHANNEL_OPS_NUMBERS", "")
	start := time.Now()
	err := newOpsNotifier(nil, nil, nil).NotifyOps(context.Background(), channel.HandoffNotice{IdentityID: "i", Channel: channel.IMessage})
	if err == nil || time.Since(start) > opsSendTimeout+2*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(start))
	}
}

type stubToucher struct {
	stamped []string
	err     error
}

func (s *stubToucher) TouchOutbound(_ context.Context, _ channel.Channel, addr string) (time.Time, error) {
	if s.err != nil {
		return time.Time{}, s.err
	}
	s.stamped = append(s.stamped, addr)
	return time.Now(), nil
}

type stubAdapter struct{ sent []channel.Outbound }

func (s *stubAdapter) Channel() channel.Channel                       { return channel.IMessage }
func (s *stubAdapter) VerifyRequest(*http.Request, []byte) error      { return nil }
func (s *stubAdapter) ParseInbound([]byte) ([]channel.Inbound, error) { return nil, nil }
func (s *stubAdapter) Send(_ context.Context, out channel.Outbound) (string, error) {
	s.sent = append(s.sent, out)
	return "pid", nil
}
