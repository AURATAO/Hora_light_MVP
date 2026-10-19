package main

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"hora-auth/internal/channel"
)

// The one-page setup a person reaches from the link we text them (PLAN §8).
//
// Proof model:
//   - the link token proves they can read messages sent to this number
//     (256-bit, single use, 30 minutes, stored hashed)
//   - Supabase email OTP + POST /auth/exchange proves they own the email —
//     the same sign-in the app uses, so a new person gets a normal account
//     and an existing user lands in their own account. profiles.phone is
//     never consulted (unverified free text).
//
// Only after both does POST /channel/setup/link attach the number to the
// session's internal user id (S-11: identity from dualAuth, never the body).
//
// Steps on the page: email → code → name + terms (PATCH /profile) → link →
// card (POST /payments/setup-intent + Stripe.js). The card step is skipped
// with a note when Stripe is not configured (503), as on staging until test
// keys are set; posting a task asks for a card again in that case.

//go:embed channel_setup.html
var channelSetupHTML []byte

//go:embed channel_setup.js
var channelSetupJS []byte

func registerChannelSetupRoutes(r *gin.Engine, sqlDB *sql.DB, store *channel.PGStore, router *channel.SetupRouter) {
	r.GET("/channel/setup", func(c *gin.Context) {
		setupPageHeaders(c)
		c.Data(http.StatusOK, "text/html; charset=utf-8", channelSetupHTML)
	})
	r.GET("/channel/setup.js", func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Data(http.StatusOK, "text/javascript; charset=utf-8", channelSetupJS)
	})
	// Public values the page needs to start Supabase email sign-in.
	r.GET("/channel/setup/config", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{
			"supabase_url":             strings.TrimSuffix(os.Getenv("SUPABASE_PROJECT_URL"), "/"),
			"supabase_publishable_key": strings.TrimSpace(os.Getenv("SUPABASE_PUBLISHABLE_KEY")),
			"terms_url":                "https://www.my-hora.com/terms",
			"privacy_url":              "https://www.my-hora.com/privacy",
		})
	})
	r.POST("/channel/setup/check", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		token, ok := bindSetupToken(c)
		if !ok {
			return
		}
		id, expires, err := store.PeekSetupToken(c.Request.Context(), channel.HashSetupToken(token))
		if errors.Is(err, channel.ErrTokenInvalid) {
			c.JSON(http.StatusGone, gin.H{"error": "link_expired"})
			return
		}
		if err != nil {
			log.Printf("[channel-setup] check: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"valid":      true,
			"expires_at": expires.UTC().Format(time.RFC3339),
			"phone_hint": phoneHint(id.Address),
			"channel":    id.Channel,
		})
	})
	r.POST("/channel/setup/link", dualAuth(sqlDB), func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		uid := c.GetString("uid")
		if uid == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
			return
		}
		token, ok := bindSetupToken(c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		id, err := store.ConsumeSetupTokenAndLink(ctx, channel.HashSetupToken(token), uid)
		switch {
		case errors.Is(err, channel.ErrTokenInvalid):
			c.JSON(http.StatusGone, gin.H{"error": "link_expired"})
			return
		case errors.Is(err, channel.ErrIdentityTaken):
			c.JSON(http.StatusConflict, gin.H{"error": "number_taken"})
			return
		case err != nil:
			log.Printf("[channel-setup] link uid=%s: %v", uid, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
			return
		}
		log.Printf("[channel-setup] linked identity=%s channel=%s to=%s uid=%s", id.ID, id.Channel, channel.Redact(id.Address), uid)

		name := profileNameForUser(ctx, sqlDB, uid)
		// The welcome is a side effect (S-32): off the request path, best effort.
		go func() {
			sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out := channel.Outbound{Channel: id.Channel, To: id.Address, Text: channel.SetupDoneText(name)}
			if err := router.Send(sctx, out, ""); err != nil {
				log.Printf("[channel-setup] welcome identity=%s: %v", id.ID, err)
			}
		}()
		c.JSON(http.StatusOK, gin.H{"linked": true, "phone_hint": phoneHint(id.Address)})
	})
}

func setupPageHeaders(c *gin.Context) {
	h := c.Writer.Header()
	// The token rides in the query string: keep it out of caches and out of
	// the Referer sent to Stripe/Supabase/CDNs.
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' https://cdn.jsdelivr.net https://js.stripe.com",
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
		"font-src https://fonts.gstatic.com",
		"img-src 'self' data: https://*.stripe.com",
		"connect-src 'self' https://*.supabase.co https://api.stripe.com",
		"frame-src https://js.stripe.com https://hooks.stripe.com",
		"base-uri 'none'",
		"form-action 'none'",
		"frame-ancestors 'none'",
	}, "; "))
}

func bindSetupToken(c *gin.Context) (string, bool) {
	var body struct {
		Token string `json:"token"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_token"})
		return "", false
	}
	t := strings.TrimSpace(body.Token)
	// A real token is 43 base64url characters; anything else is not ours.
	if len(t) < 32 || len(t) > 128 {
		c.JSON(http.StatusGone, gin.H{"error": "link_expired"})
		return "", false
	}
	return t, true
}

// phoneHint shows enough for a person to recognise their own number.
func phoneHint(e164 string) string {
	if len(e164) < 4 {
		return ""
	}
	return "•••• " + e164[len(e164)-4:]
}

// profileNameForUser is the name the person typed on the setup page (or in
// the app). Empty when they have none; never an email prefix.
func profileNameForUser(ctx context.Context, sqlDB *sql.DB, uid string) string {
	var name string
	err := sqlDB.QueryRowContext(ctx, `
		SELECT coalesce(p.name, '')
		FROM public.users u JOIN public.profiles p ON p.email = u.email
		WHERE u.id = $1::uuid
	`, uid).Scan(&name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("[channel-setup] name uid=%s: %v", uid, err)
	}
	return strings.TrimSpace(name)
}
