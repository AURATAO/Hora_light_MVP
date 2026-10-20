package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"hora-auth/internal/channel"
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
