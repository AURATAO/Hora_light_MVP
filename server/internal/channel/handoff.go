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
// triggers, plus — only with the requester agent on (D-18, CHANNEL_AGENT) — an
// agent escalation for safety, a dispute, legal or medical matters, or a
// repeated misunderstanding (HandoffAgentEscalated).
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
	Direction string    `json:"direction"` // inbound / outbound
	Text      string    `json:"text"`
	At        time.Time `json:"at"`
	// Relay: an outbound line that carried a supporter's words (the
	// store's flag) — not HO:RA's own voice.
	Relay bool `json:"relay"`
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
	// OpenRequestID / OpenRequestTaskID: an approval question that was armed
	// when the thread was handed off. It stays pending in the app and is not
	// asked again by text — ops must know it is there (D-18 review r1).
	OpenRequestID     string
	OpenRequestTaskID string
	// OpenRequestUnconfirmed: the question was claimed but its delivery not
	// confirmed — the hold survives and it IS asked again after release.
	OpenRequestUnconfirmed bool
	// Detail is the agent's escalation reason (safety, dispute, …), "" otherwise.
	Detail string
	// AutoReleased: no ops activity for HandoffTimeout — the thread went
	// back to the bot and the person was told. Emailed, never texted.
	AutoReleased bool
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
	return r.handOffDetail(ctx, in, id, conv, reason, "")
}

// handOffDetail is handOff with the agent's escalation reason for ops.
func (r *Router) handOffDetail(ctx context.Context, in Inbound, id Identity, conv Conversation, reason HandoffReason, detail string) error {
	var open handoffOpen
	if conv.State == StateAwaitingApproval && conv.PendingExtensionID != "" {
		open = handoffOpen{extensionID: conv.PendingExtensionID, taskID: conv.ApprovalTaskID, unconfirmed: conv.Unconfirmed()}
	}
	open.detail = detail
	// Ops are told FIRST. If no one could be reached, the thread is not
	// handed off and the person is told so — never "a teammate will text
	// you" with nobody notified (staging 2026-10-24: every ops email
	// failed, the person was told help was coming).
	if err := r.notifyOps(ctx, id, reason, false, open); err != nil {
		log.Printf("[channel] handoff identity=%s reason=%s: ops NOT reached: %v", id.ID, reason, err)
		return r.Reply(ctx, in, CopyHandoffFailed, "")
	}
	conv.State = StateHandoff
	conv.PendingExtensionID = ""
	// A held relay message is dropped: ops now read the thread, and the
	// words are in it.
	conv.PendingTaskChoices, conv.PendingRelayText = nil, ""
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
		// Ops were told; a returned error would retry the webhook and tell
		// them twice. Logged, and the person gets the true answer (a
		// teammate is coming); the thread is not held, which ops see.
		log.Printf("[channel] handoff identity=%s reason=%s: ops told, thread NOT held: %v", id.ID, reason, err)
		return r.Reply(ctx, in, CopyHandoffStart, "")
	}
	log.Printf("[channel] handoff identity=%s reason=%s", id.ID, reason)
	return r.Reply(ctx, in, CopyHandoffStart, "")
}

// onHandoff is every message while ops hold the thread.
func (r *Router) onHandoff(ctx context.Context, in Inbound, id Identity) error {
	if err := r.notifyOps(ctx, id, "", true, handoffOpen{}); err != nil {
		log.Printf("[channel] handoff update identity=%s: ops NOT reached: %v", id.ID, err)
		return r.Reply(ctx, in, CopyHandoffUpdateFailed, "")
	}
	return r.Reply(ctx, in, CopyHandoffHolding, "")
}

// handoffOpen names an approval question armed at hand-off time.
type handoffOpen struct {
	extensionID, taskID string
	unconfirmed         bool
	detail              string // the agent's escalation reason
	autoReleased        bool   // the thread went back to the bot (HandoffTimeout)
}

func (r *Router) notifyOps(ctx context.Context, id Identity, reason HandoffReason, update bool, open handoffOpen) error {
	if r.Ops == nil {
		return fmt.Errorf("notify ops: no OpsNotifier configured")
	}
	n := HandoffNotice{IdentityID: id.ID, Channel: id.Channel, PhoneHint: PhoneHint(id.Address), UserID: id.UserID, Reason: reason, Update: update,
		OpenRequestID: open.extensionID, OpenRequestTaskID: open.taskID, OpenRequestUnconfirmed: open.unconfirmed, Detail: open.detail,
		AutoReleased: open.autoReleased}
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
// Released on the state alone: an ops message landing meanwhile does not
// make the ops tap a silent no-op.
func (r *Router) Release(ctx context.Context, id Identity) error {
	released, err := r.release(ctx, id, true, CopyHandoffReleased, true, false)
	if err == nil && !released {
		return ErrNotInHandoff // already with the bot (released, or timed out)
	}
	return err
}

// HandoffTimeout: a handed-off thread with no ops activity for this long
// goes back to the bot, and the person is told (Arbiter 2026-10-24 22:39:
// tasks are billed by the minute — two hours is far too long; 10 minutes).
const HandoffTimeout = 10 * time.Minute

// handoffStale: handed off and no ops activity within HandoffTimeout. A
// thread with no clock is not stale here — the sweeper starts its clock
// first (StartHandoffClocks), so a hand-off is never released at once.
func (r *Router) handoffStale(conv Conversation) bool {
	return conv.State == StateHandoff && conv.HandoffActivityAt != nil &&
		r.now().Sub(*conv.HandoffActivityAt) >= HandoffTimeout
}

// AutoRelease hands a stale hand-off back to the bot, tells the person and
// tells ops (the server's sweeper, and the person's next message). async:
// the message off the caller's path. released=false: not stale, or ops
// acted / released meanwhile.
func (r *Router) AutoRelease(ctx context.Context, id Identity, async bool) (bool, error) {
	return r.release(ctx, id, false, CopyHandoffTimedOut, async, true)
}

// release is the one release path. anyClock: an ops release (on the state
// alone); otherwise compare-and-set on the activity time the caller read,
// and only when stale.
func (r *Router) release(ctx context.Context, id Identity, anyClock bool, text string, async, timedOut bool) (bool, error) {
	conv, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return false, fmt.Errorf("release: %w", err)
	}
	if conv.State != StateHandoff || (!anyClock && !r.handoffStale(conv)) {
		return false, nil
	}
	resumed := conv
	resumed.State = conv.ResumeState()
	// Our earlier question is put again — unless a supporter's question is
	// about to be asked (resumeDeferred below): two YES-able questions in
	// one burst invite a YES for the wrong one. Ours comes back after.
	back := ""
	if !r.nextHoldLive(ctx, id, conv) {
		back = r.resumeLine(ctx, id, resumed)
	}
	// R-1: any question state we return to is "in flight" until a question
	// is actually put again (markShown) — even when ours is held back for
	// the supporter's: a YES to something ops asked must not book the old
	// draft (review r2 F2).
	putAgain := back != "" || awaitingAnswer(resumed.State)
	won, err := r.Conversations.ReleaseHandoff(ctx, id.ID, ReleaseOpts{Resume: resumed.State, Seen: conv.HandoffActivityAt, AnyClock: anyClock, PutAgain: putAgain})
	if err != nil {
		return false, fmt.Errorf("release: %w", err)
	}
	if !won {
		return false, nil
	}
	if timedOut {
		log.Printf("[channel] handoff identity=%s: no ops activity for %s, back to the bot", id.ID, HandoffTimeout)
	}
	if back != "" {
		text += "\n\n" + back
	}
	return true, r.dispatch(ctx, async, func(c context.Context) error {
		if err := r.Send(c, Outbound{Channel: id.Channel, To: id.Address, Text: text}, ""); err != nil {
			return err
		}
		if back != "" {
			r.markShown(c, id.ID) // our question was put again (R-1)
		}
		r.resumeDeferred(c, id) // a supporter's question held during the handoff
		if timedOut {
			// Ops still think they hold it: told (after the person, whose
			// reply must not wait on the email), so a late reply is not a
			// surprise 409 and a payment or a report is followed up.
			if err := r.notifyOps(c, id, "", false, handoffOpen{autoReleased: true}); err != nil {
				log.Printf("[channel] handoff identity=%s: auto-release notice: %v", id.ID, err)
			}
		}
		return nil
	})
}

// OpsClaim: ops are on it without writing yet (a call, the app) — the
// HandoffTimeout restarts.
func (r *Router) OpsClaim(ctx context.Context, id Identity) error {
	ok, err := r.Conversations.TouchHandoff(ctx, id.ID)
	if err != nil {
		return fmt.Errorf("OpsClaim: %w", err)
	}
	if !ok {
		return ErrNotInHandoff
	}
	return nil
}

// OpsSend sends ops' words to a handed-off thread (and restarts the
// HandoffTimeout). ErrNotInHandoff when the bot holds it.
func (r *Router) OpsSend(ctx context.Context, id Identity, text string) error {
	conv, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return fmt.Errorf("OpsSend: %w", err)
	}
	if conv.State != StateHandoff {
		return ErrNotInHandoff
	}
	// Ops are on it: the HandoffTimeout restarts — and the thread is still
	// theirs at this moment (the timeout may have just released it).
	ok, err := r.Conversations.TouchHandoff(ctx, id.ID)
	if err != nil {
		return fmt.Errorf("OpsSend: %w", err)
	}
	if !ok {
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
