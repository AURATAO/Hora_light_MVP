package channel

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// WhatsAppAdapter is the Meta WhatsApp Cloud API channel.
//
// Webhook auth: Meta signs every POST with X-Hub-Signature-256 =
// "sha256=" + hex(HMAC-SHA256(app secret, raw body)). The GET subscribe
// handshake is separate (VerifySubscribe) and uses the verify token.
//
// This replaces the stub in server/whatsapp_webhook.go, which checks no
// signature and logs sender numbers and message text (S-12). The stub stays
// registered until this adapter's route is switched on (PLAN §8).
type WhatsAppAdapter struct {
	AccessToken   string // WHATSAPP_ACCESS_TOKEN
	PhoneNumberID string // WHATSAPP_PHONE_NUMBER_ID
	AppSecret     string // WHATSAPP_APP_SECRET — signs webhooks
	VerifyToken   string // WHATSAPP_VERIFY_TOKEN — GET handshake only
	GraphVersion  string // default v21.0
	BaseURL       string // default https://graph.facebook.com
	HTTP          *http.Client
}

func (a *WhatsAppAdapter) Channel() Channel { return WhatsApp }

func (a *WhatsAppAdapter) VerifyRequest(r *http.Request, body []byte) error {
	if a.AppSecret == "" {
		return fmt.Errorf("whatsapp verify: %w", ErrNotConfigured)
	}
	sig := r.Header.Get("X-Hub-Signature-256")
	const prefix = "sha256="
	if !strings.HasPrefix(sig, prefix) {
		return ErrUnverified
	}
	got, err := hex.DecodeString(strings.TrimPrefix(sig, prefix))
	if err != nil {
		return ErrUnverified
	}
	mac := hmac.New(sha256.New, []byte(a.AppSecret))
	if _, err := mac.Write(body); err != nil {
		return fmt.Errorf("whatsapp verify: hmac: %w", err)
	}
	if !hmac.Equal(got, mac.Sum(nil)) {
		return ErrUnverified
	}
	return nil
}

// VerifySubscribe answers Meta's GET handshake: returns the challenge to echo
// when mode and token match, ok=false otherwise.
func (a *WhatsAppAdapter) VerifySubscribe(mode, token, challenge string) (string, bool) {
	if a.VerifyToken == "" || mode != "subscribe" {
		return "", false
	}
	if !hmac.Equal([]byte(token), []byte(a.VerifyToken)) {
		return "", false
	}
	return challenge, true
}

type waPayload struct {
	Entry []struct {
		Changes []struct {
			Field string `json:"field"`
			Value struct {
				Metadata struct {
					DisplayPhoneNumber string `json:"display_phone_number"`
				} `json:"metadata"`
				Messages []struct {
					From      string `json:"from"`
					ID        string `json:"id"`
					Timestamp string `json:"timestamp"`
					Type      string `json:"type"`
					Text      *struct {
						Body string `json:"body"`
					} `json:"text"`
					Button *struct {
						Text string `json:"text"`
					} `json:"button"`
					Interactive *struct {
						ButtonReply *struct {
							Title string `json:"title"`
						} `json:"button_reply"`
					} `json:"interactive"`
					Context *struct {
						ID string `json:"id"`
					} `json:"context"`
				} `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

func (a *WhatsAppAdapter) ParseInbound(body []byte) ([]Inbound, error) {
	var p waPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("whatsapp parse: %w", err)
	}
	var out []Inbound
	for _, e := range p.Entry {
		for _, ch := range e.Changes {
			// "statuses" (sent/delivered/read) arrive on the same field with
			// no messages; they are not person-sent and are skipped here.
			if ch.Field != "messages" {
				continue
			}
			to := ""
			if n, err := NormalizeE164(ch.Value.Metadata.DisplayPhoneNumber); err == nil {
				to = n
			} else {
				// Our own business number, informational only; logged
				// without the value.
				log.Printf("[channel] whatsapp parse: business number unreadable: %v", fmt.Errorf("normalize: %w", err))
			}
			for _, m := range ch.Value.Messages {
				text := ""
				switch {
				case m.Text != nil:
					text = m.Text.Body
				case m.Button != nil: // template quick-reply
					text = m.Button.Text
				case m.Interactive != nil && m.Interactive.ButtonReply != nil:
					text = m.Interactive.ButtonReply.Title
				default:
					// media, location, stickers: not handled in v1; the
					// router answers with a "text only" hint.
				}
				if m.ID == "" {
					log.Printf("[channel] whatsapp parse: dropping a message with no id")
					continue
				}
				from, err := NormalizeE164(m.From)
				if err != nil {
					// Unanswerable without a valid sender. Logged by message
					// id only — never the number (S-12).
					log.Printf("[channel] whatsapp parse: dropping id=%s: sender %v", m.ID, fmt.Errorf("normalize: %w", err))
					continue
				}
				in := Inbound{
					Channel:           WhatsApp,
					ProviderMessageID: m.ID,
					From:              from,
					To:                to,
					Text:              strings.TrimSpace(text),
					Service:           "whatsapp",
					ReceivedAt:        unixOrNow(m.Timestamp),
				}
				if m.Context != nil {
					in.ReplyToProviderID = m.Context.ID
				}
				out = append(out, in)
			}
		}
	}
	return out, nil
}

func (a *WhatsAppAdapter) Send(ctx context.Context, o Outbound) (string, error) {
	if a.AccessToken == "" || a.PhoneNumberID == "" {
		return "", fmt.Errorf("whatsapp send: %w", ErrNotConfigured)
	}
	payload := map[string]any{
		"messaging_product": "whatsapp",
		// The Cloud API takes the number without '+'.
		"to":   strings.TrimPrefix(o.To, "+"),
		"type": "text",
		"text": map[string]any{"body": o.Text, "preview_url": false},
	}
	if o.MediaURL != "" {
		// An image message with the text as its caption.
		delete(payload, "text")
		payload["type"] = "image"
		payload["image"] = map[string]any{"link": o.MediaURL, "caption": o.Text}
	}
	if o.ReplyToProviderID != "" {
		payload["context"] = map[string]string{"message_id": o.ReplyToProviderID}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("whatsapp send marshal: %w", err)
	}
	base, ver := a.BaseURL, a.GraphVersion
	if base == "" {
		base = "https://graph.facebook.com"
	}
	if ver == "" {
		ver = "v21.0"
	}
	url := fmt.Sprintf("%s/%s/%s/messages", strings.TrimRight(base, "/"), ver, a.PhoneNumberID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("whatsapp send request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	resp, err := httpClient(a.HTTP).Do(req)
	if err != nil {
		return "", fmt.Errorf("whatsapp send: %w", err)
	}
	defer closeBody("whatsapp send", resp.Body)
	respBody, err := readProviderBody(resp.Body, 64<<10)
	if err != nil {
		return "", fmt.Errorf("whatsapp send: status %d: %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Error 131047 = outside the 24h window; the caller must use a template.
		return "", fmt.Errorf("whatsapp send: status %d: %s", resp.StatusCode, ProviderErrorText(respBody))
	}
	var r struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(respBody, &r); err != nil {
		return "", fmt.Errorf("whatsapp send decode: %w", err)
	}
	if len(r.Messages) == 0 || r.Messages[0].ID == "" {
		return "", fmt.Errorf("whatsapp send: response had no message id")
	}
	return r.Messages[0].ID, nil
}

// unixOrNow reads Meta's unix timestamp; an unreadable one falls back to the
// receive time (ordering only — nothing is decided on it) and is logged.
func unixOrNow(s string) time.Time {
	n, err := strconv.ParseInt(s, 10, 64)
	if err == nil && n > 0 {
		return time.Unix(n, 0).UTC()
	}
	if s != "" {
		log.Printf("[channel] whatsapp parse: unreadable timestamp, using receive time: %v", fmt.Errorf("parse timestamp: %w", err))
	}
	return time.Now().UTC()
}
