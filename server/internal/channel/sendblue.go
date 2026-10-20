package channel

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// SendblueAdapter is the iMessage channel (with Sendblue's SMS/RCS fallback).
//
// Docs: https://docs.sendblue.com/getting-started/webhooks/ (receive payload,
// retries, idempotency on message_handle) and /guides/inline-replies/ (reply_to).
//
// Webhook auth: Sendblue sends the dashboard's "Signing secret" verbatim in
// the sb-signing-secret request header; the check is a constant-time
// comparison of that raw value. Confirmed 2026-10-19 against a real delivery on
// staging (a wrong or missing secret would have answered 401). The header name
// stays overridable via SENDBLUE_WEBHOOK_SECRET_HEADER in case Sendblue changes it.
type SendblueAdapter struct {
	APIKey    string // SENDBLUE_API_KEY    → header sb-api-key-id
	APISecret string // SENDBLUE_API_SECRET → header sb-api-secret-key
	// FromNumber is our Sendblue line in E.164 (SENDBLUE_FROM_NUMBER).
	FromNumber string
	// WebhookSecret / WebhookSecretHeader authenticate inbound webhooks.
	WebhookSecret       string
	WebhookSecretHeader string
	BaseURL             string // default https://api.sendblue.co
	HTTP                *http.Client
}

const defaultSendblueSecretHeader = "sb-signing-secret"

func (a *SendblueAdapter) Channel() Channel { return IMessage }

func (a *SendblueAdapter) VerifyRequest(r *http.Request, _ []byte) error {
	if a.WebhookSecret == "" {
		return fmt.Errorf("sendblue verify: %w", ErrNotConfigured)
	}
	header := a.WebhookSecretHeader
	if header == "" {
		header = defaultSendblueSecretHeader
	}
	got := r.Header.Get(header)
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(a.WebhookSecret)) != 1 {
		return ErrUnverified
	}
	return nil
}

// sendbluePayload is the subset of the receive webhook we use.
type sendbluePayload struct {
	MessageHandle  string `json:"message_handle"`
	Content        string `json:"content"`
	IsOutbound     bool   `json:"is_outbound"`
	FromNumber     string `json:"from_number"`
	ToNumber       string `json:"to_number"`
	SendblueNumber string `json:"sendblue_number"`
	Service        string `json:"service"`
	MediaURL       string `json:"media_url"`
	GroupID        string `json:"group_id"`
	DateSent       string `json:"date_sent"`
	ReplyTo        *struct {
		MessageHandle string `json:"message_handle"`
	} `json:"reply_to"`
}

func (a *SendblueAdapter) ParseInbound(body []byte) ([]Inbound, error) {
	var p sendbluePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("sendblue parse: %w", err)
	}
	// Our own sends come back as status webhooks; group threads are out of
	// scope for v1 (PLAN §3). Neither is a person talking to us.
	if p.IsOutbound || p.GroupID != "" {
		return nil, nil
	}
	if p.MessageHandle == "" {
		return nil, fmt.Errorf("sendblue parse: missing message_handle")
	}
	from, err := NormalizeE164(p.FromNumber)
	if err != nil {
		return nil, fmt.Errorf("sendblue parse from: %w", err)
	}
	to := p.SendblueNumber
	if to == "" {
		to = p.ToNumber
	}
	if n, err := NormalizeE164(to); err == nil {
		to = n
	} else {
		to = ""
	}
	in := Inbound{
		Channel:           IMessage,
		ProviderMessageID: p.MessageHandle,
		From:              from,
		To:                to,
		Text:              strings.TrimSpace(p.Content),
		Service:           p.Service,
		ReceivedAt:        parseTimeOrNow(p.DateSent),
	}
	if p.ReplyTo != nil {
		in.ReplyToProviderID = p.ReplyTo.MessageHandle
	}
	if p.MediaURL != "" {
		in.MediaURLs = []string{p.MediaURL}
	}
	return []Inbound{in}, nil
}

func (a *SendblueAdapter) Send(ctx context.Context, out Outbound) (string, error) {
	if a.APIKey == "" || a.APISecret == "" || a.FromNumber == "" {
		return "", fmt.Errorf("sendblue send: %w", ErrNotConfigured)
	}
	req := map[string]any{
		"number":      out.To,
		"from_number": a.FromNumber,
		"content":     out.Text,
	}
	if out.ReplyToProviderID != "" {
		req["reply_to"] = map[string]string{"message_handle": out.ReplyToProviderID}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("sendblue send marshal: %w", err)
	}
	base := a.BaseURL
	if base == "" {
		base = "https://api.sendblue.co"
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/api/send-message", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("sendblue send request: %w", err)
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("sb-api-key-id", a.APIKey)
	hr.Header.Set("sb-api-secret-key", a.APISecret)

	resp, err := httpClient(a.HTTP).Do(hr)
	if err != nil {
		return "", fmt.Errorf("sendblue send: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := readProviderBody(resp.Body, 64<<10)
	if err != nil {
		return "", fmt.Errorf("sendblue send: status %d: %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Sendblue answers 400 (not 429) for plan-limit declines; never retry
		// those blindly. The body may echo the recipient: scrubbed (S-12).
		return "", fmt.Errorf("sendblue send: status %d: %s", resp.StatusCode, ProviderErrorText(respBody))
	}
	var r struct {
		MessageHandle string `json:"message_handle"`
	}
	if err := json.Unmarshal(respBody, &r); err != nil {
		return "", fmt.Errorf("sendblue send decode: %w", err)
	}
	if r.MessageHandle == "" {
		return "", fmt.Errorf("sendblue send: response had no message_handle")
	}
	return r.MessageHandle, nil
}

func httpClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func parseTimeOrNow(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Now().UTC()
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

var digitRun = regexp.MustCompile(`\+?\d[\d\s().-]{5,}\d`)

// ProviderErrorText is a provider's error body made safe for an error string
// that will be logged: phone-number-shaped digit runs masked (a provider may
// echo the recipient back, S-12) and the length capped.
func ProviderErrorText(b []byte) string {
	return truncate(digitRun.ReplaceAll(b, []byte("[number]")), 200)
}

// readProviderBody reads at most limit bytes of a provider response body.
func readProviderBody(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	return b, nil
}
