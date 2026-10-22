package channel

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
)

// Ops handoff (PLAN §6 step 8, M3). When the chat cannot help — the person
// asks for a human, two messages in a row could not be understood, or a
// payment needs a step only a person can walk them through — the thread goes
// to the ops team and the bot steps back until ops release it. Deterministic
// triggers only; the model never decides to hand off.
//
// While handed off: every inbound message is forwarded to ops and answered
// with a short holding line; ops reply and release through the admin routes
// (server/channel_admin.go). Nothing is booked, priced or approved in this
// state — a YES here is just text.

// HandoffReason is why the thread went to ops.
type HandoffReason string

const (
	HandoffHumanRequested HandoffReason = "human_requested"
	HandoffNotUnderstood  HandoffReason = "not_understood"
	HandoffPayment3DS     HandoffReason = "payment_3ds"
	HandoffAgentEscalated HandoffReason = "agent_escalated" // D-18: the agent judged a person must handle it
)

// ThreadMessage is one line of the conversation, for ops.
type ThreadMessage struct {
	Direction string // inbound / outbound
	Text      string
	At        time.Time
}

// HandoffNotice is what ops receive.
type HandoffNotice struct {
	IdentityID string
	Channel    Channel
	PhoneHint  string // last four digits only (S-12)
	UserID     string // empty when the number is not linked
	Reason     HandoffReason
	// Thread is the recent conversation, oldest first.
	Thread []ThreadMessage
	// Update is set when the person wrote again during a handoff (the
	// Thread then holds that one message).
	Update bool
}

// OpsNotifier delivers a notice to the people who will take over.
type OpsNotifier interface {
	NotifyOps(ctx context.Context, n HandoffNotice) error
}

// ThreadReader loads recent messages for the notice.
type ThreadReader interface {
	// RecentMessages returns up to limit messages for the number, oldest
	// first.
	RecentMessages(ctx context.Context, ch Channel, address string, limit int) ([]ThreadMessage, error)
}

// handoffThreadLen is how much context ops get on a handoff.
const handoffThreadLen = 12

var humanRequestRE = regexp.MustCompile(`(?i)\b(` +
	`(talk|speak|chat)\s+(to|with)\s+(a\s+|an\s+)?(human|person|real person|someone|agent|rep|representative|operator)` +
	`|(real|actual|live)\s+(human|person)` +
	`|customer\s+(service|support|care)` +
	`|support\s+team` +
	`|(are|r|is)\s+(you|this)\s+(a\s+)?(bot|robot|ai)` +
	`|not\s+a\s+bot` +
	`)\b|真人|客服|人工`)

// IsHumanRequest reports an explicit ask for a person. Matched by rule.
func IsHumanRequest(text string) bool { return humanRequestRE.MatchString(text) }

// PhoneHint shows the last four digits, enough for a person to recognise
// their own number and nothing more.
func PhoneHint(e164 string) string {
	if len(e164) < 4 {
		return ""
	}
	return "•••• " + e164[len(e164)-4:]
}

// handOff moves the conversation to ops and tells both sides. The pending
// draft (if any) is kept for when ops release the thread.
func (r *Router) handOff(ctx context.Context, in Inbound, id Identity, conv Conversation, reason HandoffReason) error {
	conv.State = StateHandoff
	conv.PendingExtensionID = ""
	// A held relay message is dropped: ops now read the thread, and the
	// words are in it.
	conv.PendingTaskChoices, conv.PendingRelayText = nil, ""
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
		return fmt.Errorf("handOff: %w", err)
	}
	log.Printf("[channel] handoff identity=%s reason=%s", id.ID, reason)
	if err := r.notifyOps(ctx, id, reason, false); err != nil {
		// Ops not told is the one failure that must not be silent to the
		// person: say so and let the route fail so the message is retried.
		return fmt.Errorf("handOff: %w", err)
	}
	return r.Reply(ctx, in, CopyHandoffStart, "")
}

// onHandoff is every message while ops hold the thread.
func (r *Router) onHandoff(ctx context.Context, in Inbound, id Identity) error {
	if err := r.notifyOps(ctx, id, "", true); err != nil {
		return fmt.Errorf("onHandoff: %w", err)
	}
	return r.Reply(ctx, in, CopyHandoffHolding, "")
}

func (r *Router) notifyOps(ctx context.Context, id Identity, reason HandoffReason, update bool) error {
	if r.Ops == nil {
		return fmt.Errorf("notify ops: no OpsNotifier configured")
	}
	n := HandoffNotice{IdentityID: id.ID, Channel: id.Channel, PhoneHint: PhoneHint(id.Address), UserID: id.UserID, Reason: reason, Update: update}
	limit := handoffThreadLen
	if update {
		limit = 1
	}
	if r.Thread != nil {
		msgs, err := r.Thread.RecentMessages(ctx, id.Channel, id.Address, limit)
		if err != nil {
			return fmt.Errorf("notify ops: %w", err)
		}
		n.Thread = msgs
	}
	if err := r.Ops.NotifyOps(ctx, n); err != nil {
		return fmt.Errorf("notify ops: %w", err)
	}
	return nil
}

// lastReplyWasParseFailure reports whether the bot's previous message was
// already "I couldn't work that out": the second in a row hands off.
func (r *Router) lastReplyWasParseFailure(ctx context.Context, id Identity) bool {
	if r.Thread == nil {
		return false
	}
	msgs, err := r.Thread.RecentMessages(ctx, id.Channel, id.Address, 3)
	if err != nil {
		log.Printf("[channel] recent messages identity=%s: %v", id.ID, fmt.Errorf("parse-failure check: %w", err))
		return false
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Direction == "outbound" {
			return strings.TrimSpace(msgs[i].Text) == CopyParseFailed
		}
	}
	return false
}

// Release returns a handed-off thread to the bot (ops action). The pending
// draft, if any, is re-shown so the person can pick up where they were.
func (r *Router) Release(ctx context.Context, id Identity) error {
	conv, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return fmt.Errorf("Release: %w", err)
	}
	if conv.State != StateHandoff {
		return nil
	}
	conv.State = conv.ResumeState()
	shown := ShownUnconfirmed // our earlier question is put again below; in flight until delivered (D-17 R-1)
	conv.QuestionShownAt = &shown
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
		return fmt.Errorf("Release: %w", err)
	}
	text := CopyHandoffReleased
	back := r.resumeLine(ctx, id, conv)
	if back != "" {
		text += "\n\n" + back
	}
	// The state change is the action; the message goes off the request
	// path (S-32).
	return r.dispatch(ctx, true, func(c context.Context) error {
		if err := r.Send(c, Outbound{Channel: id.Channel, To: id.Address, Text: text}, ""); err != nil {
			return err
		}
		if back != "" {
			r.markShown(c, id.ID) // our question was put again (R-1)
		}
		r.resumeDeferred(c, id) // a supporter's question held during the handoff
		return nil
	})
}

// OpsSend queues a message from ops to the person, recorded like any
// outbound. The thread must be in handoff: ops do not speak over the bot.
func (r *Router) OpsSend(ctx context.Context, id Identity, text string) error {
	conv, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return fmt.Errorf("OpsSend: %w", err)
	}
	if conv.State != StateHandoff {
		return ErrNotInHandoff
	}
	// Queued, not awaited (S-32): a slow provider must not hold the ops
	// request open. Delivery failures are logged by dispatch.
	return r.dispatch(ctx, true, func(c context.Context) error {
		return r.Send(c, Outbound{Channel: id.Channel, To: id.Address, Text: text}, "")
	})
}

// ErrNotInHandoff: ops may only write to a thread they hold.
var ErrNotInHandoff = fmt.Errorf("channel: conversation is not handed off")
