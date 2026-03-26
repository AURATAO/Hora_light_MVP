package helpers

import (
	"os"
	"testing"
)

func TestSendWhatsApp(t *testing.T) {
	if os.Getenv("META_WHATSAPP_TOKEN") == "" {
		t.Skip("META_WHATSAPP_TOKEN not set — skipping live WhatsApp test")
	}
	to := os.Getenv("TEST_WHATSAPP_NUMBER")
	if to == "" {
		t.Skip("TEST_WHATSAPP_NUMBER not set — skipping live WhatsApp test")
	}
	err := SendWhatsApp(to, "Hi! This is Hora. WhatsApp integration is working!")
	if err != nil {
		t.Fatalf("SendWhatsApp failed: %v", err)
	}
}
