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
