package main

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
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
// The person reaches this page only after replying YES to a priced task in the
// chat (flow agreed 2026-10-20: help first, leave Messages once). Name and
// Terms consent were already given in the chat and are copied onto the
// profile at link time. Steps on the page: email → code → link → card
// (POST /payments/setup-intent + Stripe.js; skipped when Stripe is not
// configured, as on staging) → finish, which books the pending task through
// the same router the chat uses and confirms it in Messages.

//go:embed channel_setup.html
var channelSetupHTML []byte

//go:embed channel_setup.js
var channelSetupJS []byte

func registerChannelSetupRoutes(r *gin.Engine, sqlDB *sql.DB, store *channel.PGStore, router *channel.Router) {
	r.GET("/channel/setup", publicByDesign, func(c *gin.Context) {
		// Links already texted point here. Once the web app serves this page
		// (CHANNEL_SETUP_WEB_URL, D-16 R-4) those links are forwarded to it
		// rather than being served the older copy embedded in this binary.
		//
		// The token survives the hop without this handler ever seeing it: it
		// is in the fragment, and a user agent re-applies the original
		// fragment to a redirect target that has none of its own. A browser
		// that did not would land on the page with no token, which is the
		// "this link has expired" screen and its "text HO:RA again" way out —
		// wrong-looking but never a silent failure.
		if web := channelSetupWebURL(); web != "" {
			c.Redirect(http.StatusFound, web)
			return
		}
		setupPageHeaders(c)
		c.Data(http.StatusOK, "text/html; charset=utf-8", channelSetupHTML)
	})
	r.GET("/channel/setup.js", publicByDesign, func(c *gin.Context) {
		c.Data(http.StatusOK, "text/javascript; charset=utf-8", channelSetupJS)
	})
	// Public values the page needs to start Supabase email sign-in.
	r.GET("/channel/setup/config", publicByDesign, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"supabase_url":             strings.TrimSuffix(os.Getenv("SUPABASE_PROJECT_URL"), "/"),
			"supabase_publishable_key": strings.TrimSpace(os.Getenv("SUPABASE_PUBLISHABLE_KEY")),
			"terms_url":                "https://www.my-hora.com/terms",
			"privacy_url":              "https://www.my-hora.com/privacy",
		})
	})
	r.POST("/channel/setup/check", setupTokenAuth(store), func(c *gin.Context) {
		id := c.MustGet(ctxSetupIdentity).(channel.Identity)
		expires := c.MustGet(ctxSetupExpires).(time.Time)
		resp := gin.H{
			"valid":      true,
			"expires_at": expires.UTC().Format(time.RFC3339),
			"phone_hint": phoneHint(id.Address),
			"channel":    id.Channel,
		}
		// What they are about to book, so the page can say it back to them.
		if p := router.PendingFor(c.Request.Context(), id.ID); p != nil {
			resp["booking"] = gin.H{"title": p.Headline(), "estimate": channel.Dollars(p.TotalCents())}
		}
		c.JSON(http.StatusOK, resp)
	})
	// Two controls: the session (who) and the setup link (which number).
	r.POST("/channel/setup/link", dualAuth(sqlDB), setupTokenAuth(store), func(c *gin.Context) {
		uid := c.GetString("uid")
		if uid == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
			return
		}
		token := c.GetString(ctxSetupToken)
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

		// Name and Terms consent were given in the chat; copy them onto the
		// profile now that there is one. Blanks only.
		if conv, err := store.GetConversation(ctx, id.ID); err != nil {
			log.Printf("[channel-setup] apply chat profile uid=%s: %v", uid, fmt.Errorf("load conversation: %w", err))
		} else if err := router.Booker.ApplyChatProfile(ctx, uid, conv.PendingName, conv.TermsAcceptedAt); err != nil {
			log.Printf("[channel-setup] apply chat profile uid=%s: %v", uid, err)
		}
		c.JSON(http.StatusOK, gin.H{"linked": true, "phone_hint": phoneHint(id.Address)})
	})
	// The last step of the page: book what they said YES to. Runs the same
	// claim-guarded path as a YES in the chat, so a double tap or a YES sent
	// at the same moment books once. The chat gets the confirmation.
	r.POST("/channel/setup/finish", dualAuth(sqlDB), func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		uid := c.GetString("uid")
		if uid == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
		defer cancel()
		booked, last, err := router.FinishSetup(ctx, uid)
		if err != nil {
			log.Printf("[channel-setup] finish uid=%s: %v", uid, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"booked": booked, "outcome": outcomeName(booked, last)})
	})
}

func outcomeName(booked int, last channel.PostResult) string {
	if booked > 0 {
		return "booked"
	}
	switch last.Outcome {
	case channel.PostNeedsCard:
		return "needs_card"
	case channel.PostDeclined:
		return "declined"
	case channel.PostOwesBalance:
		return "outstanding_balance"
	case channel.PostNeeds3DS:
		return "needs_authentication"
	}
	return "nothing_to_book"
}

func setupPageHeaders(c *gin.Context) {
	h := c.Writer.Header()
	// Defence in depth: the token rides in the fragment (never sent), and
	// the page is still kept out of caches and out of any Referer.
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

// phoneHint shows enough for a person to recognise their own number.
func phoneHint(e164 string) string {
	if len(e164) < 4 {
		return ""
	}
	return "•••• " + e164[len(e164)-4:]
}
