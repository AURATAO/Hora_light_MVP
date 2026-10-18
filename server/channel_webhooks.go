package main

import (
	"database/sql"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"hora-auth/internal/channel"
)

// RegisterChannelWebhooks mounts the messaging entry point (PLAN.md):
//
//	POST /webhooks/channel/sendblue   iMessage via Sendblue
//	POST /webhooks/channel/whatsapp   WhatsApp Cloud API
//	GET  /webhooks/channel/whatsapp   Meta subscribe handshake
//
// Nothing is mounted unless CHANNEL_WEBHOOKS_ENABLED=1, so this file is inert
// in production until it is switched on deliberately. Auth (S-13): these are
// provider-to-server routes with no user session; each one is authenticated by
// its adapter's VerifyRequest (shared secret / HMAC) before anything is read.
//
// The paths are new on purpose. The legacy stub at /webhooks/whatsapp
// (whatsapp_webhook.go) stays untouched during the App Store review freeze and
// is removed when this route goes live.
func RegisterChannelWebhooks(r *gin.Engine, sqlDB *sql.DB) {
	if !channel.Enabled() {
		return
	}
	store := &channel.PGStore{DB: sqlDB}
	router := channel.LogRouter{} // M0: record + log only; real router in M1–M3

	sb := &channel.WebhookHandler{Adapter: channel.SendblueFromEnv(), Store: store, Router: router}
	wa := channel.WhatsAppFromEnv()
	waHandler := &channel.WebhookHandler{Adapter: wa, Store: store, Router: router}

	r.POST("/webhooks/channel/sendblue", gin.WrapH(sb))
	r.POST("/webhooks/channel/whatsapp", gin.WrapH(waHandler))
	r.GET("/webhooks/channel/whatsapp", func(c *gin.Context) {
		challenge, ok := wa.VerifySubscribe(c.Query("hub.mode"), c.Query("hub.verify_token"), c.Query("hub.challenge"))
		if !ok {
			c.Status(http.StatusForbidden)
			return
		}
		c.String(http.StatusOK, challenge)
	})
	log.Printf("[channel] webhooks enabled: sendblue, whatsapp")
}
