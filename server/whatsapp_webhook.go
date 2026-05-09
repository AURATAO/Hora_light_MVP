package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

func RegisterWhatsAppWebhooks(r *gin.Engine) {
	r.GET("/webhooks/whatsapp", verifyWhatsAppWebhook)
	r.POST("/webhooks/whatsapp", handleWhatsAppIncoming)
}

// verifyWhatsAppWebhook handles Meta's webhook verification handshake.
func verifyWhatsAppWebhook(c *gin.Context) {
	mode := c.Query("hub.mode")
	token := c.Query("hub.verify_token")
	challenge := c.Query("hub.challenge")

	if mode == "subscribe" && token == os.Getenv("WHATSAPP_VERIFY_TOKEN") {
		c.String(http.StatusOK, challenge)
		return
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "verification failed"})
}

// handleWhatsAppIncoming parses incoming WhatsApp Cloud API webhook events and logs them.
// Task-creation logic will be added later; for now we just log and ACK.
func handleWhatsAppIncoming(c *gin.Context) {
	var payload waWebhookPayload
	if err := json.NewDecoder(c.Request.Body).Decode(&payload); err != nil {
		log.Printf("[whatsapp][webhook] decode error: %v", err)
		c.Status(http.StatusOK)
		return
	}

	for _, entry := range payload.Entry {
		for _, change := range entry.Changes {
			if change.Field != "messages" {
				continue
			}
			for _, msg := range change.Value.Messages {
				if msg.Type != "text" || msg.Text == nil {
					continue
				}
				log.Printf("[whatsapp][incoming] from=%s id=%s ts=%s text=%q",
					msg.From, msg.ID, msg.Timestamp, msg.Text.Body)
			}
		}
	}

	c.Status(http.StatusOK)
}

// ── Meta Cloud API webhook payload shapes ──────────────────────────────────

type waWebhookPayload struct {
	Object string    `json:"object"`
	Entry  []waEntry `json:"entry"`
}

type waEntry struct {
	ID      string     `json:"id"`
	Changes []waChange `json:"changes"`
}

type waChange struct {
	Field string  `json:"field"`
	Value waValue `json:"value"`
}

type waValue struct {
	Messages []waMessage `json:"messages"`
	Contacts []waContact `json:"contacts"`
}

type waMessage struct {
	From      string  `json:"from"`
	ID        string  `json:"id"`
	Timestamp string  `json:"timestamp"`
	Type      string  `json:"type"`
	Text      *waText `json:"text,omitempty"`
}

type waText struct {
	Body string `json:"body"`
}

type waContact struct {
	WaID    string `json:"wa_id"`
	Profile struct {
		Name string `json:"name"`
	} `json:"profile"`
}
