package channel

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Approvals by text (PLAN §6 step 4, M3). A supporter's request for more
// time or budget reaches the requester's thread as a question; the next
// YES or NO answers it. Rules:
//
//   - YES/NO are matched by rule (IsYes/IsNo), never by the model — this
//     moves money (the approved ceiling). Anything else re-asks.
//   - The decision is made by the app's own extension code through
//     Booker.ResolveExtension; the chat carries the answer and nothing more,
//     so the requester-only check, expiry-first, double-answer safety and
//     the audit row are production's.
//   - An approval request has a clock (auto-declined after
//     Billing.ApprovalTimeoutMinutes), so it takes priority over a draft the
//     person was confirming: the draft is kept and re-shown afterwards.
//   - A request that resolves elsewhere (approved in the app, expired) clears
//     the question here, so a later YES never answers a stale one.

// Event types that carry an approval request / its resolution.
const (
	evTimeRequested   = "TIME_EXTENSION_REQUESTED"
	evBudgetRequested = "BUDGET_INCREASE_REQUESTED"
	evResolved        = "EXTENSION_RESOLVED"
)

func isApprovalRequest(typ string) bool { return typ == evTimeRequested || typ == evBudgetRequested }

// approvalText is the chat's own approval question, composed from the
// structured request: the vetted supporter name, the amount or minutes the
// server recorded, the task title, and the supporter's reason — quoted,
// one line, capped, and labelled as their note, never as HO:RA's words.
// Without the structured request nothing is sent (the app still has it);
// the app's body is never relayed on the one message a YES answers.
func approvalText(ev StatusEvent) (string, bool) {
	a := ev.Approval
	if a == nil || a.ID == "" || ev.ExtensionID == "" {
		return "", false
	}
	who := firstNonEmpty(DisplayName(ev.SupporterName), "Your supporter")
	title := firstNonEmpty(strings.TrimSpace(ev.TaskTitle), "your task")
	var body string
	switch a.Kind {
	case "budget":
		if a.Cents <= 0 {
			return "", false
		}
		body = fmt.Sprintf("%s is asking for %s more for \u201c%s\u201d.", who, Dollars(a.Cents), title)
	default:
		if a.Minutes <= 0 {
			return "", false
		}
		body = fmt.Sprintf("%s is asking for %d more minutes on \u201c%s\u201d.", who, a.Minutes, title)
	}
	if note := quotedNote(a.Reason); note != "" {
		body += "\n" + fmt.Sprintf(CopyApprovalNote, who, note)
	}
	mins := ev.ApprovalMinutes
	if mins <= 0 {
		return body + "\n\n" + CopyApprovalAsk, true
	}
	return body + "\n\n" + fmt.Sprintf(CopyApprovalAskTimed, mins), true
}

// quotedNote makes a supporter's free text safe to show inside our message:
// one line, printable, at most 120 characters, in quotation marks.
func quotedNote(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	var b strings.Builder
	for _, c := range s {
		if unicode.IsPrint(c) && c != '\u201c' && c != '\u201d' && c != '"' {
			b.WriteRune(c)
		}
	}
	s = strings.TrimSpace(b.String())
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) > 120 {
		rs := []rune(s)
		s = string(rs[:120]) + "\u2026"
	}
	return "\u201c" + s + "\u201d"
}

// onAwaitingApproval answers the pending request with this message.
func (r *Router) onAwaitingApproval(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	var approve bool
	switch {
	case (IsYes(text) || IsNo(text)) && conv.Unconfirmed():
		// Claimed but never confirmed delivered: nothing can answer it.
		// Re-sent from the hold once the claim is old enough; meanwhile the
		// person is told a question is waiting.
		log.Printf("[channel] approval identity=%s ext=%s: answer refused, question unconfirmed", id.ID, conv.PendingExtensionID)
		r.resumeDeferred(ctx, id)
		return r.Reply(ctx, in, CopyApprovalReask, "")
	case (IsYes(text) || IsNo(text)) && conv.ApprovalArmedAt != nil && (in.ReceivedAt.IsZero() || in.ReceivedAt.Before(*conv.ApprovalArmedAt)):
		// Sent before the question was asked (a second tap on YES for the
		// booking, say), or with no usable send time: it cannot be taken as
		// answering it. The question is put again; nothing moves (S-11/5).
		// Provider send times vs our clock: the stamp is taken AFTER the
		// send returned, so a tap made after reading is later than it by
		// the person's own reaction time; no margin is applied.
		log.Printf("[channel] approval identity=%s ext=%s: answer refused, sent before the question", id.ID, conv.PendingExtensionID)
		return r.Reply(ctx, in, CopyApprovalReask, "")
	case IsYes(text):
		approve = true
	case IsNo(text):
		approve = false
	default:
		// A sentence while the request is pending may be for the
		// supporter ("why $10 more?") — relayed, the question stays open.
		if handled, err := r.routeInContext(ctx, in, id, conv, text); handled {
			return err
		}
		return r.Reply(ctx, in, CopyApprovalReask, "")
	}
	if r.Booker == nil {
		return fmt.Errorf("onAwaitingApproval: router not fully configured")
	}
	res := r.Booker.ResolveExtension(ctx, id.UserID, conv.PendingExtensionID, approve)
	log.Printf("[channel] approval identity=%s ext=%s approve=%t outcome=%d status=%s",
		id.ID, conv.PendingExtensionID, approve, res.Outcome, res.Status)

	// Whatever happened, this question is answered or gone: resume —
	// compare-and-set on the request, so a question armed meanwhile is
	// left alone.
	ext := conv.PendingExtensionID
	conv.PendingExtensionID = ""
	conv.ApprovalArmedAt = nil
	conv.State = conv.ResumeState()
	if _, err := r.Conversations.ClearApproval(ctx, id.ID, ext, conv.State); err != nil {
		return fmt.Errorf("onAwaitingApproval: %w", err)
	}

	var reply string
	switch res.Outcome {
	case ExtResolved:
		if approve {
			reply = fmt.Sprintf(CopyApproved, res.Ask)
		} else {
			reply = fmt.Sprintf(CopyDeclined, res.Ask)
		}
	case ExtAlready:
		switch res.Status {
		case "expired":
			reply = CopyApprovalExpired
		case "approved", "denied":
			reply = fmt.Sprintf(CopyApprovalAlready, res.Status)
		default:
			reply = CopyApprovalGone
		}
	default:
		reply = CopyApprovalGone
	}
	if back := r.resumeLine(ctx, id, conv); back != "" {
		reply += "\n\n" + back
	}
	if err := r.Reply(ctx, in, reply, res.TaskID); err != nil {
		return err
	}
	r.resumeDeferred(ctx, id) // the next held question, if any
	return nil
}

// resumeLine re-asks whatever our own question was before an interruption
// (D-17 P4), for the state ResumeState chose; "" when nothing was pending.
func (r *Router) resumeLine(ctx context.Context, id Identity, conv Conversation) string {
	switch conv.State {
	case StateAwaitingName:
		if conv.Pending != nil {
			return CopyBackToDraft + "\n" + fmt.Sprintf(CopyAskName, conv.Pending.Headline())
		}
	case StateAwaitingBudget:
		if conv.Pending != nil {
			items := conv.Pending.Items()
			i := conv.Pending.BudgetAskIndex
			if i >= 0 && i < len(items) && len(items) > 1 {
				return CopyBackToDraft + "\n" + fmt.Sprintf(CopyAskBudgetFor, items[i].Draft.Title)
			}
			return CopyBackToDraft + "\n" + CopyAskBudget
		}
	case StateAwaitingConfirm:
		if conv.Pending != nil {
			return CopyBackToDraft + "\n" + r.Summary(conv.Pending)
		}
	case StateAwaitingTaskChoice:
		return CopyBackToHold + "\n" + r.taskList(ctx, id, conv.PendingTaskChoices)
	}
	return ""
}

// canArmApproval: the states an approval question can interrupt. Elsewhere
// (handoff, booking in flight) the question is not sent to the phone
// (D-17 P4, I-O) — the app still has it.
// Not while another question is armed: one at a time, so a YES can never
// land on a request other than the one it was read against.
func canArmApproval(s State) bool {
	for _, a := range armableStates {
		if a == s {
			return true
		}
	}
	return false
}

// clearApproval is called when a request resolved elsewhere (the app, or
// the timeout): a later YES must not answer it. Only the armed question
// for THAT request is cleared — a resolution of another request on the
// same task leaves the asked question armed. When the event does not say
// which request resolved, the armed one is kept unless the booker says it
// is no longer pending.
func (r *Router) clearApproval(ctx context.Context, id Identity, extensionID string) (cleared bool, err error) {
	identityID, userID := id.ID, id.UserID
	conv, err := r.Conversations.GetConversation(ctx, identityID)
	if err != nil {
		return false, fmt.Errorf("clearApproval: %w", err)
	}
	if conv.State != StateAwaitingApproval {
		return false, nil
	}
	if extensionID != "" {
		if conv.PendingExtensionID != extensionID {
			return false, nil
		}
	} else if r.Booker != nil && conv.PendingExtensionID != "" {
		pending, err := r.Booker.ExtensionPending(ctx, userID, conv.PendingExtensionID)
		if err != nil {
			return false, fmt.Errorf("clearApproval: %w", err)
		}
		if pending {
			return false, nil // some other request resolved; this question stands
		}
	}
	// Compare-and-set on the request: a question armed in between (a
	// claim racing this) is left alone. The caller resumes a held
	// question once the person has been told.
	cleared, err = r.Conversations.ClearApproval(ctx, identityID, conv.PendingExtensionID, conv.ResumeState())
	if err != nil {
		return false, fmt.Errorf("clearApproval: %w", err)
	}
	return cleared, nil
}

// resumeDeferred asks the oldest held approval question that is still the
// person's to answer, once the thread can take it; stale ones (timed out by
// the clock, resolved, closed task, another supporter, another user) are
// dropped. One at a time: the next is resumed after the answer. This is
// the ONLY path that arms a question: the hold is claimed atomically in
// the store — state and request set, armed-at = ArmedUnconfirmed, the hold
// kept — so concurrent resume points ask once; then the question is sent;
// then ConfirmArmed stamps the real time and drops the hold. Between claim
// and confirm no message can answer it (every message precedes
// ArmedUnconfirmed), and a crash there is repaired by the next resume
// point, which re-sends from the hold. A failed send undoes the claim on
// a context that outlives the caller's. Errors are logged — the person's
// own flow must not fail over the supporter's question.
func (r *Router) resumeDeferred(ctx context.Context, id Identity) {
	conv, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		log.Printf("[channel] resume deferred identity=%s: %v", id.ID, err)
		return
	}
	if conv.State == StateAwaitingApproval && conv.Unconfirmed() {
		// Claimed, never confirmed sent. A send may still be in flight
		// beside us; only a claim older than any live send is re-sent from
		// the hold. With no hold to re-send from, nothing can answer it and
		// the request's own resolution clears it.
		if r.now().Sub(conv.UpdatedAt) < UnconfirmedResend {
			return
		}
		for _, d := range conv.Deferred {
			if d.ExtensionID == conv.PendingExtensionID {
				log.Printf("[channel] approval ext=%s: claim unconfirmed for %s, re-sending", d.ExtensionID, UnconfirmedResend)
				r.sendClaimed(ctx, id, d)
				return
			}
		}
		return
	}
	if len(conv.Deferred) == 0 || !canArmApproval(conv.State) {
		return
	}
	if id.Channel == WhatsApp && (conv.LastInboundAt == nil || r.now().Sub(*conv.LastInboundAt) > WhatsAppWindow) {
		log.Printf("[channel] deferred approvals identity=%s not sent: outside WhatsApp 24h window", id.ID)
		return // kept; a later resume point may fall inside the window
	}
	for _, d := range conv.Deferred {
		live := d.UserID == id.UserID && r.remainingMinutes(d) != 0
		if live && r.Booker != nil {
			live, err = r.Booker.ExtensionPending(ctx, d.UserID, d.ExtensionID)
			if err != nil {
				// Kept for the next resume point; the others are not held
				// up behind it.
				log.Printf("[channel] resume deferred ext=%s: %v", d.ExtensionID, err)
				continue
			}
		}
		if _, ok := approvalText(r.heldEvent(d)); !live || !ok {
			log.Printf("[channel] deferred approval ext=%s dropped: no longer the person's to answer", d.ExtensionID)
			if err := r.Conversations.RemoveHold(ctx, id.ID, d.ExtensionID); err != nil {
				log.Printf("[channel] resume deferred identity=%s: %v", id.ID, err)
				return
			}
			continue
		}
		claimed, err := r.Conversations.ClaimHold(ctx, id.ID, d.ExtensionID)
		if err != nil {
			log.Printf("[channel] resume deferred ext=%s: %v", d.ExtensionID, err)
			return
		}
		if !claimed {
			return // another resume point got there first, or the state moved
		}
		r.sendClaimed(ctx, id, d)
		return // one question at a time; the rest after the answer
	}
}

// remainingMinutes: what is left of the app's auto-decline window for a
// held request; 0 when none is left (TimeoutMinutes 0 means no window:
// returns -1).
func (r *Router) remainingMinutes(d DeferredApproval) int {
	if d.TimeoutMinutes <= 0 {
		return -1
	}
	left := d.TimeoutMinutes - int(r.now().Sub(time.Unix(d.AtUnix, 0))/time.Minute)
	if left < 0 {
		return 0
	}
	return left
}

func (r *Router) heldEvent(d DeferredApproval) StatusEvent {
	return StatusEvent{Type: d.Type, TaskID: d.TaskID, TaskTitle: d.TaskTitle, SupporterName: d.SupporterName,
		ExtensionID: d.ExtensionID, ApprovalMinutes: r.remainingMinutes(d), Approval: &d.Approval}
}

// sendClaimed delivers a claimed question and confirms it armed; on a
// failed send the claim is undone (the hold stays) for a later resume
// point.
func (r *Router) sendClaimed(ctx context.Context, id Identity, d DeferredApproval) {
	text, _ := approvalText(r.heldEvent(d))
	if err := r.Send(ctx, Outbound{Channel: id.Channel, To: id.Address, Text: text}, d.TaskID); err != nil {
		log.Printf("[channel] resume deferred ext=%s: send failed, hold kept: %v", d.ExtensionID, err)
		undo := context.WithoutCancel(ctx)
		conv, gerr := r.Conversations.GetConversation(undo, id.ID)
		if gerr != nil {
			log.Printf("[channel] resume deferred identity=%s: %v", id.ID, gerr)
			return
		}
		if _, err := r.Conversations.ClearApproval(undo, id.ID, d.ExtensionID, conv.ResumeState()); err != nil {
			log.Printf("[channel] resume deferred identity=%s: %v", id.ID, err)
		}
		return
	}
	if err := r.Conversations.ConfirmArmed(context.WithoutCancel(ctx), id.ID, d.ExtensionID, r.now()); err != nil {
		// Still armed as unconfirmed: unanswerable, re-sent at the next
		// resume point (a repeat over a lost question).
		log.Printf("[channel] resume deferred ext=%s: confirm failed: %v", d.ExtensionID, err)
	}
}
