// Package channel is the messaging entry point: requesters text HO:RA over
// iMessage (Sendblue) or WhatsApp (Meta Cloud API) instead of using the app.
//
// Business code never sees a provider payload. Each provider is an Adapter that
// turns webhooks into Inbound events and Outbound events into API calls; the
// router and the task/billing logic work only with those two types. Adding a
// channel means writing one adapter. See PLAN.md §4–§8.
//
// S-12: phone numbers and message bodies are personal data. Nothing in this
// package logs them; log provider message ids and Redact()ed numbers only.
package channel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Channel is the route a message travelled, as stored in channel_* tables.
// "imessage" covers Sendblue's SMS/RCS fallback too: the identity is the phone
// number on the Sendblue line, and Inbound.Service records the actual service.
type Channel string

const (
	IMessage Channel = "imessage"
	WhatsApp Channel = "whatsapp"
)

func (c Channel) Valid() bool { return c == IMessage || c == WhatsApp }

// Inbound is one message a person sent us, provider-neutral.
type Inbound struct {
	Channel Channel
	// ProviderMessageID is the provider's id for this message (Sendblue
	// message_handle, WhatsApp wamid). Unique per channel; the dedupe key.
	ProviderMessageID string
	// From is the sender in E.164 (+15551234567).
	From string
	// To is our number that received it, E.164 when the provider gives it.
	To   string
	Text string
	// ReplyToProviderID is the provider id of the message this one replies to
	// (Sendblue reply_to.message_handle, WhatsApp context.id). Empty when the
	// person did not use reply/quote. Router step 5 maps it to a task.
	ReplyToProviderID string
	// Service is what actually carried it: "iMessage", "SMS", "RCS", "whatsapp".
	Service    string
	MediaURLs  []string
	ReceivedAt time.Time
}

// Outbound is one message we send.
type Outbound struct {
	Channel Channel
	// To is the recipient in E.164.
	To   string
	Text string
	// ReplyToProviderID, when set, sends this as an inline reply to that
	// message (iMessage V2 lines and WhatsApp only).
	ReplyToProviderID string
	// StoredText, when set, is what channel_messages.body records instead of
	// Text. A setup link carries a raw token that must never be stored
	// (S-11 amended, condition 3: only its SHA-256 hash is kept).
	StoredText string
	// MediaURL, when set, attaches an image (a supporter's photo relayed
	// from the task chat; a task photo). Text still goes as the
	// caption/body. MediaExpires marks a short-lived signed link (a task
	// photo, D-16 I-R): a channel that cannot carry it as media drops it
	// rather than pasting a link that would die in the chat; a public relay
	// image it cannot carry is pasted as a link, as before.
	MediaURL     string
	MediaExpires bool
	// Relay marks a supporter's chat message passed on to the person. An
	// inline reply to it is for the supporter (relay.go).
	Relay bool
}

// RecordedText is the body to store for o.
func (o Outbound) RecordedText() string {
	if o.StoredText != "" {
		return o.StoredText
	}
	return o.Text
}

// Adapter is one provider. Implementations must be safe for concurrent use.
type Adapter interface {
	Channel() Channel
	// VerifyRequest authenticates a webhook before anything else reads it.
	// body is the exact raw request body. Must fail closed: an adapter with
	// no secret configured rejects every request.
	VerifyRequest(r *http.Request, body []byte) error
	// ParseInbound extracts the person-sent messages from a verified webhook.
	// Delivery receipts, our own outbound echoes and group messages are
	// dropped here, not passed on.
	ParseInbound(body []byte) ([]Inbound, error)
	// Send delivers one message and returns the provider's id for it, which
	// the caller records so a later reply-to can be traced to its task.
	Send(ctx context.Context, out Outbound) (providerMessageID string, err error)
}

// ErrUnverified means a webhook failed authentication. The handler answers 401
// and records nothing.
var ErrUnverified = errors.New("channel: webhook failed verification")

// MediaCarrier is an optional adapter capability: whether a message with
// this media reference and this much text would carry the media as the
// provider accepts it. The router asks BEFORE composing copy, so a photo
// the channel cannot carry is never promised (M3.6, D-16 I-R).
type MediaCarrier interface {
	CanCarryMedia(mediaURL string, textLen int) bool
}

// ProviderStatusError is a provider's non-2xx answer, with its status, so a
// caller can tell a refusal of the request (4xx) from a transport failure
// where the message may have gone through. Its text is already scrubbed.
type ProviderStatusError struct {
	Provider string
	Status   int
	Text     string // ProviderErrorText of the body
}

func (e *ProviderStatusError) Error() string {
	return fmt.Sprintf("%s send: status %d: %s", e.Provider, e.Status, e.Text)
}

// ErrNotConfigured means the adapter is missing credentials it needs.
var ErrNotConfigured = errors.New("channel: adapter not configured")

// wrapErr adds context to err and keeps nil as nil (S-31), for returns where
// err may legitimately be nil.
func wrapErr(where string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", where, err)
}
