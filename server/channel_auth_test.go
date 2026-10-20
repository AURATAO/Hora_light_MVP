package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"hora-auth/internal/channel"
)

func init() { gin.SetMode(gin.TestMode) }

// echoBody answers 200 with the body the handler received, so a test can see
// the middleware handed the exact bytes on.
func echoBody(c *gin.Context) {
	b, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.String(http.StatusOK, string(b))
}

func serve(r *gin.Engine, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestProviderSignatureAuthSendblue(t *testing.T) {
	r := gin.New()
	r.POST("/sb", providerSignatureAuth(&channel.SendblueAdapter{WebhookSecret: "s3cret"}), echoBody)
	r.POST("/sb-off", providerSignatureAuth(&channel.SendblueAdapter{}), echoBody)

	body := `{"content":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/sb", strings.NewReader(body))
	req.Header.Set("sb-signing-secret", "s3cret")
	if w := serve(r, req); w.Code != http.StatusOK || w.Body.String() != body {
		t.Fatalf("signed: %d %q", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/sb", strings.NewReader(body))
	req.Header.Set("sb-signing-secret", "wrong")
	if w := serve(r, req); w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "hi") {
		t.Fatalf("bad secret must be 401 and never reach the handler: %d", w.Code)
	}

	// Fail closed: no secret configured → everything rejected.
	req = httptest.NewRequest(http.MethodPost, "/sb-off", strings.NewReader(body))
	req.Header.Set("sb-signing-secret", "")
	if w := serve(r, req); w.Code != http.StatusUnauthorized {
		t.Fatalf("unconfigured adapter must reject: %d", w.Code)
	}
}

func TestProviderSignatureAuthWhatsApp(t *testing.T) {
	r := gin.New()
	r.POST("/wa", providerSignatureAuth(&channel.WhatsAppAdapter{AppSecret: "app"}), echoBody)
	body := `{"entry":[]}`
	mac := hmac.New(sha256.New, []byte("app"))
	if _, err := mac.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/wa", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	if w := serve(r, req); w.Code != http.StatusOK || w.Body.String() != body {
		t.Fatalf("signed: %d %q", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/wa", strings.NewReader(body+" "))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	if w := serve(r, req); w.Code != http.StatusUnauthorized {
		t.Fatalf("tampered body must be 401: %d", w.Code)
	}
}

func TestWhatsAppSubscribeAuth(t *testing.T) {
	r := gin.New()
	r.GET("/hs", whatsappSubscribeAuth(&channel.WhatsAppAdapter{VerifyToken: "vt"}), func(c *gin.Context) {
		c.String(http.StatusOK, c.GetString(ctxHubChallenge))
	})
	ok := httptest.NewRequest(http.MethodGet, "/hs?hub.mode=subscribe&hub.verify_token=vt&hub.challenge=42", nil)
	if w := serve(r, ok); w.Code != http.StatusOK || w.Body.String() != "42" {
		t.Fatalf("good handshake: %d %q", w.Code, w.Body.String())
	}
	bad := httptest.NewRequest(http.MethodGet, "/hs?hub.mode=subscribe&hub.verify_token=nope&hub.challenge=42", nil)
	if w := serve(r, bad); w.Code != http.StatusForbidden || w.Body.String() == "42" {
		t.Fatalf("wrong token must be 403 and echo nothing: %d", w.Code)
	}
}

// The token is checked before any lookup; malformed input never reaches the
// store (a nil store would panic if it did).
func TestSetupTokenAuthRejectsMalformedWithoutLookup(t *testing.T) {
	r := gin.New()
	r.POST("/check", setupTokenAuth(nil), func(c *gin.Context) { c.Status(http.StatusOK) })
	for body, want := range map[string]int{
		`not json`:          http.StatusBadRequest,
		`{"token":"short"}`: http.StatusGone,
		`{"token":"` + strings.Repeat("x", 200) + `"}`: http.StatusGone,
	} {
		w := serve(r, httptest.NewRequest(http.MethodPost, "/check", strings.NewReader(body)))
		if w.Code != want {
			t.Errorf("%.20s…: got %d want %d", body, w.Code, want)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("token responses must not be cached")
		}
	}
}
