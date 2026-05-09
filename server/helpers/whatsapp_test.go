package helpers

import (
	"os"
	"testing"
)

func TestSendWhatsAppMessage(t *testing.T) {
	if os.Getenv("WHATSAPP_ACCESS_TOKEN") == "" {
		t.Skip("WHATSAPP_ACCESS_TOKEN not set — skipping live WhatsApp test")
	}
	to := os.Getenv("TEST_WHATSAPP_NUMBER")
	if to == "" {
		t.Skip("TEST_WHATSAPP_NUMBER not set — skipping live WhatsApp test")
	}
	err := SendWhatsAppMessage(to, "Hi! This is Hora. WhatsApp integration is working!")
	if err != nil {
		t.Fatalf("SendWhatsAppMessage failed: %v", err)
	}
}
