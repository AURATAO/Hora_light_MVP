package main

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"hora-auth/internal/channel"
)

// The named authentication controls of the chat routes (S-13, D-16 R-2). Each
// route in channel_webhooks.go / channel_setup.go names exactly one of these
// (or dualAuth), so what protects it is visible where it is registered.
//
//	providerSignatureAuth   POST /webhooks/channel/{sendblue,whatsapp}
//	whatsappSubscribeAuth   GET  /webhooks/channel/whatsapp (Meta handshake)
//	setupTokenAuth          POST /channel/setup/check, /channel/setup/link
//	publicByDesign          GET  /channel/setup, /channel/setup.js, /channel/setup/config
//
// Every one fails closed: an unconfigured secret rejects everything.

const (
	ctxSetupToken    = "channel_setup_token"
	ctxSetupIdentity = "channel_setup_identity"
	ctxSetupExpires  = "channel_setup_expires"
	ctxHubChallenge  = "channel_hub_challenge"
	maxWebhookBody   = 1 << 20
)

// providerSignatureAuth admits a webhook only if its provider signed it
// (Sendblue shared secret header, Meta X-Hub-Signature-256 HMAC). The body is
// read once, verified over its exact bytes, and handed on unchanged.
func providerSignatureAuth(a channel.Adapter) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxWebhookBody))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "bad request"})
			return
		}
		if err := a.VerifyRequest(c.Request, body); err != nil {
			reason := "verification failed"
			if errors.Is(err, channel.ErrNotConfigured) {
				reason = "adapter not configured"
			}
			log.Printf("[channel] %s webhook rejected: %s", a.Channel(), reason)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Next()
	}
}

// whatsappSubscribeAuth admits Meta's subscription handshake only with our
// verify token. The challenge to echo is passed to the handler.
func whatsappSubscribeAuth(wa *channel.WhatsAppAdapter) gin.HandlerFunc {
	return func(c *gin.Context) {
		challenge, ok := wa.VerifySubscribe(c.Query("hub.mode"), c.Query("hub.verify_token"), c.Query("hub.challenge"))
		if !ok {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Set(ctxHubChallenge, challenge)
		c.Next()
	}
}

// setupTokenAuth admits a request only with a live, unused setup link token —
// the capability that proves the caller can read messages sent to that
// number. It does not use the token up; /channel/setup/link consumes it in
// its own transaction. The token comes from the JSON body, never the URL
// (S-12).
func setupTokenAuth(store *channel.PGStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		var body struct {
			Token string `json:"token"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "missing_token"})
			return
		}
		t := strings.TrimSpace(body.Token)
		// A real token is 43 base64url characters; anything else is not ours.
		if len(t) < 32 || len(t) > 128 {
			c.AbortWithStatusJSON(http.StatusGone, gin.H{"error": "link_expired"})
			return
		}
		id, expires, err := store.PeekSetupToken(c.Request.Context(), channel.HashSetupToken(t))
		if errors.Is(err, channel.ErrTokenInvalid) {
			c.AbortWithStatusJSON(http.StatusGone, gin.H{"error": "link_expired"})
			return
		}
		if err != nil {
			log.Printf("[channel-setup] token check: %v", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
			return
		}
		c.Set(ctxSetupToken, t)
		c.Set(ctxSetupIdentity, id)
		c.Set(ctxSetupExpires, expires)
		c.Next()
	}
}

// publicByDesign marks a route that is deliberately unauthenticated: the setup
// page itself, its script, and the PUBLIC values it needs (Supabase URL and
// publishable key — the same values every web client ships). Nothing behind
// these reads or writes a user's data. Never cached.
func publicByDesign(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Next()
}
