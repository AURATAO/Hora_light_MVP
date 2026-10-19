package channel

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// SetupRouter is the M1 router (PLAN §6 steps 1–3):
//
//   - an unknown number gets a one-time setup link (and no more than one
//     every resendAfter, however many times it texts)
//   - a linked number gets an acknowledgement; drafting a task from the
//     message arrives in M2 through Linked
//
// Every reply is sent on the channel the message came in on and recorded as
// an outbound row, so later replies-to-a-reply resolve.
type SetupRouter struct {
	Messages   Store
	Identities IdentityStore
	Adapters   map[Channel]Adapter
	// SetupURL builds the link for a raw token, e.g.
	// https://<backend>/channel/setup?t=<token>.
	SetupURL func(token string) string
	// Linked handles a message from a set-up number. Nil → acknowledgement.
	Linked func(ctx context.Context, in Inbound, id Identity) error
	Now    func() time.Time
}

const resendAfter = 2 * time.Minute

func (r *SetupRouter) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *SetupRouter) HandleInbound(ctx context.Context, in Inbound) error {
	id, err := r.Identities.UpsertIdentity(ctx, in.Channel, in.From)
	if err != nil {
		return err
	}
	// Bookkeeping failures are logged, not fatal: the person still deserves
	// an answer, and neither row gates anything money-related.
	if err := r.Identities.AttachMessage(ctx, in.Channel, in.ProviderMessageID, id.ID); err != nil {
		log.Printf("[channel] attach message id=%s: %v", in.ProviderMessageID, err)
	}
	if err := r.Identities.TouchConversation(ctx, id.ID, in.ReceivedAt); err != nil {
		log.Printf("[channel] touch conversation identity=%s: %v", id.ID, err)
	}

	if !id.Linked() {
		return r.sendSetupLink(ctx, in, id)
	}
	if r.Linked != nil {
		return r.Linked(ctx, in, id)
	}
	return r.Reply(ctx, in, CopyLinkedAck, "")
}

func (r *SetupRouter) sendSetupLink(ctx context.Context, in Inbound, id Identity) error {
	recent, err := r.Identities.SetupTokenIssuedSince(ctx, id.ID, r.now().Add(-resendAfter))
	if err != nil {
		return err
	}
	if recent {
		return r.Reply(ctx, in, CopySetupAlreadySent, "")
	}
	if r.SetupURL == nil {
		return fmt.Errorf("send setup link: SetupURL not configured")
	}
	token, hash, err := NewSetupToken()
	if err != nil {
		return err
	}
	if err := r.Identities.CreateSetupToken(ctx, id.ID, hash, r.now().Add(SetupTokenTTL)); err != nil {
		return err
	}
	return r.Reply(ctx, in, fmt.Sprintf(CopySetupLink, r.SetupURL(token)), "")
}

// Reply answers an inbound message on its own channel.
func (r *SetupRouter) Reply(ctx context.Context, in Inbound, text, taskID string) error {
	return r.Send(ctx, Outbound{Channel: in.Channel, To: in.From, Text: text}, taskID)
}

// Send delivers one message and records it. A send failure is returned so the
// inbound row stays unprocessed for the recovery sweep.
func (r *SetupRouter) Send(ctx context.Context, out Outbound, taskID string) error {
	a, ok := r.Adapters[out.Channel]
	if !ok {
		return fmt.Errorf("send: no adapter for channel %q", out.Channel)
	}
	pid, err := a.Send(ctx, out)
	if err != nil {
		return fmt.Errorf("send to %s: %w", Redact(out.To), err)
	}
	if err := r.Messages.RecordOutbound(ctx, out, pid, taskID); err != nil {
		// Sent but not recorded: the person got the message; only reply-to
		// resolution for it is lost. Log, don't resend.
		log.Printf("[channel] record outbound id=%s: %v", pid, err)
	}
	log.Printf("[channel] outbound channel=%s id=%s to=%s len=%d", out.Channel, pid, Redact(out.To), len(out.Text))
	return nil
}

// ── Copy ───────────────────────────────────────────────────────────────────
//
// Plain, short, no emoji walls. Before a person has replied, Sendblue allows
// at most 300 characters and no links; every message here answers a message
// the person sent first, so links are fine — but stay well under 300 anyway.

const (
	CopySetupLink = "Hi, this is HO:RA. Real people in NYC who run your errands, from groceries to laundry.\n\n" +
		"Set up once (about a minute): %s\n\nThe link works for 30 minutes."
	CopySetupAlreadySent = "I just sent you a setup link. Open it to finish (it works for 30 minutes). Lost it? Text me again in a couple of minutes for a new one."
	CopyLinkedAck        = "Got it. Posting tasks by text is almost ready here. For now you're all set up."
	CopySetupDone        = "You're all set%s. Text me what you need and when, for example: \"pick up my laundry at 3pm, 12 Bedford Ave\"."
)

// SetupDoneText is the welcome after setup, with the person's first name when
// we have one.
func SetupDoneText(name string) string {
	first := strings.TrimSpace(name)
	if i := strings.IndexByte(first, ' '); i > 0 {
		first = first[:i]
	}
	if first != "" {
		first = ", " + first
	}
	return fmt.Sprintf(CopySetupDone, first)
}
