package channel

import (
	"os"
	"strings"
)

// SendblueFromEnv builds the iMessage adapter from environment variables.
// Missing values leave the adapter unconfigured, and it then fails closed:
// VerifyRequest rejects every webhook and Send refuses (ErrNotConfigured).
func SendblueFromEnv() *SendblueAdapter {
	return &SendblueAdapter{
		APIKey:              env("SENDBLUE_API_KEY"),
		APISecret:           env("SENDBLUE_API_SECRET"),
		FromNumber:          normalizedEnv("SENDBLUE_FROM_NUMBER"),
		WebhookSecret:       env("SENDBLUE_WEBHOOK_SECRET"),
		WebhookSecretHeader: env("SENDBLUE_WEBHOOK_SECRET_HEADER"),
		BaseURL:             env("SENDBLUE_BASE_URL"),
	}
}

// WhatsAppFromEnv builds the WhatsApp adapter from environment variables.
func WhatsAppFromEnv() *WhatsAppAdapter {
	return &WhatsAppAdapter{
		AccessToken:   env("WHATSAPP_ACCESS_TOKEN"),
		PhoneNumberID: env("WHATSAPP_PHONE_NUMBER_ID"),
		AppSecret:     env("WHATSAPP_APP_SECRET"),
		VerifyToken:   env("WHATSAPP_VERIFY_TOKEN"),
		GraphVersion:  env("WHATSAPP_GRAPH_VERSION"),
	}
}

// Enabled reports whether the channel routes are switched on. Off unless
// CHANNEL_WEBHOOKS_ENABLED=1, so merging this code changes nothing in
// production until someone deliberately turns it on (PLAN §2).
func Enabled() bool { return env("CHANNEL_WEBHOOKS_ENABLED") == "1" }

func env(k string) string { return strings.TrimSpace(os.Getenv(k)) }

func normalizedEnv(k string) string {
	v := env(k)
	if v == "" {
		return ""
	}
	if n, err := NormalizeE164(v); err == nil {
		return n
	}
	return ""
}
