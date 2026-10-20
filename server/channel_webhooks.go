package main

import (
	"database/sql"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"hora-auth/internal/channel"
	"hora-auth/internal/channel/llm"
	"hora-auth/internal/notify"
)

// RegisterChannelWebhooks mounts the messaging entry point (PLAN.md):
//
//	POST /webhooks/channel/sendblue   iMessage via Sendblue
//	POST /webhooks/channel/whatsapp   WhatsApp Cloud API
//	GET  /webhooks/channel/whatsapp   Meta subscribe handshake
//	GET  /channel/setup …             the one page a first-time booker visits
//
// Nothing is mounted unless CHANNEL_WEBHOOKS_ENABLED=1, so this file is inert
// in production until it is switched on deliberately. Auth (S-13): webhooks
// are provider-to-server routes with no user session; each one is
// authenticated by its adapter's VerifyRequest (shared secret / HMAC) before
// anything is read. The setup link/finish routes are behind dualAuth.
//
// The paths are new on purpose. The legacy stub at /webhooks/whatsapp
// (whatsapp_webhook.go) stays untouched during the App Store review freeze and
// is removed when this route goes live.
func RegisterChannelWebhooks(r *gin.Engine, sqlDB *sql.DB) {
	if !channel.Enabled() {
		return
	}
	store := &channel.PGStore{DB: sqlDB}
	sb := channel.SendblueFromEnv()
	wa := channel.WhatsAppFromEnv()
	model := chatModelFromEnv()

	base := setupBaseURL()
	if base == "" {
		log.Printf("[channel] WARNING: no CHANNEL_SETUP_BASE_URL or RENDER_EXTERNAL_URL — setup links cannot be built")
	}
	router := &channel.Router{
		Messages:      store,
		Identities:    store,
		Conversations: store,
		Adapters:      map[channel.Channel]channel.Adapter{channel.IMessage: sb, channel.WhatsApp: wa},
		Parser:        model,
		Splitter:      model,
		Classifier:    model,
		Booker:        &chatBooker{sqlDB: sqlDB},
		// The same extraction prompt as the app's AI posting
		// (ai_task_parser.go), read-only — that file is frozen for review.
		Prompt:   aiTaskParserSystemPrompt,
		Location: nycLocation,
		SetupURL: func(token string) string { return setupLink(base, token) },
		Status:   store,
		Ops:      opsEmailNotifier{sqlDB: sqlDB},
		Thread:   store,
	}
	// Task status updates to the chat (channel_status.go). Set here, before
	// the server starts, and only when the flag is on.
	notify.ChatHook = chatStatusHook(sqlDB, router)

	sbHandler := &channel.WebhookHandler{Adapter: sb, Store: store, Router: router}
	waHandler := &channel.WebhookHandler{Adapter: wa, Store: store, Router: router}

	// Auth (S-13): each route names its control (channel_auth.go). The
	// handler verifies again (WebhookHandler) as defence in depth.
	r.POST("/webhooks/channel/sendblue", providerSignatureAuth(sb), gin.WrapH(sbHandler))
	r.POST("/webhooks/channel/whatsapp", providerSignatureAuth(wa), gin.WrapH(waHandler))
	r.GET("/webhooks/channel/whatsapp", whatsappSubscribeAuth(wa), func(c *gin.Context) {
		c.String(http.StatusOK, c.GetString(ctxHubChallenge))
	})
	registerChannelSetupRoutes(r, sqlDB, store, router)
	registerChannelAdminRoutes(r, sqlDB, store, router)
	log.Printf("[channel] webhooks enabled: sendblue, whatsapp; model=%s; setup base=%s", model.Model, base)
}

// chatModelFromEnv is the language model behind the chat (PLAN §9: chosen by
// the eval, swapped by configuration).
//
//	LLM_BASE_URL   default https://inference.baseten.co/v1
//	LLM_MODEL      default zai-org/GLM-5.3-Flash
//	LLM_API_KEY    falls back to BASETEN_API_KEY
//	LLM_DISABLE_THINKING=1  sends chat_template_kwargs.enable_thinking=false
func chatModelFromEnv() *llm.OpenAICompat {
	get := func(k, def string) string {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
		return def
	}
	m := &llm.OpenAICompat{
		BaseURL: get("LLM_BASE_URL", "https://inference.baseten.co/v1"),
		Model:   get("LLM_MODEL", "zai-org/GLM-5.3-Flash"),
		APIKey:  get("LLM_API_KEY", strings.TrimSpace(os.Getenv("BASETEN_API_KEY"))),
	}
	if os.Getenv("LLM_DISABLE_THINKING") == "1" {
		m.ExtraBody = map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}}
	}
	return m
}

// setupBaseURL is the public origin of this backend, where the setup page is
// served. Render sets RENDER_EXTERNAL_URL on every web service.
func setupBaseURL() string {
	for _, k := range []string{"CHANNEL_SETUP_BASE_URL", "RENDER_EXTERNAL_URL"} {
		if v := strings.TrimRight(strings.TrimSpace(os.Getenv(k)), "/"); v != "" {
			return v
		}
	}
	return ""
}

// setupLink puts the token in the URL fragment, not the query: a fragment is
// never sent to a server, so the token cannot land in a request log, a proxy
// or the link-preview fetch iMessage and WhatsApp make (S-12).
func setupLink(base, token string) string {
	return base + "/channel/setup#t=" + url.QueryEscape(token)
}
