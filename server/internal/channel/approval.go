package channel

import (
	"context"
	"fmt"
	"log"
	"strings"
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

	// Whatever happened, this question is answered or gone: resume.
	conv.PendingExtensionID = ""
	conv.State = conv.ResumeState()
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
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
	return r.Reply(ctx, in, reply, res.TaskID)
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
func canArmApproval(s State) bool {
	switch s {
	case StateIdle, StateAwaitingConfirm, StateAwaitingApproval, StateAwaitingTaskChoice, StateAwaitingBudget, StateAwaitingName:
		return true
	}
	return false
}

// askApproval moves the conversation to the approval question, keeping any
// draft underneath. Only from a state where the next message is free to be
// the answer: idle, or confirming a draft (the clock beats the draft).
func (r *Router) askApproval(ctx context.Context, identityID, extensionID string) error {
	conv, err := r.Conversations.GetConversation(ctx, identityID)
	if err != nil {
		return fmt.Errorf("askApproval: %w", err)
	}
	switch conv.State {
	case StateIdle, StateAwaitingConfirm, StateAwaitingApproval, StateAwaitingTaskChoice,
		StateAwaitingBudget, StateAwaitingName:
		// The supporter's request interrupts our own questions (D-17 P4):
		// a draft, a budget or name question, or a held relay message
		// survives underneath — ResumeState brings it back and the answer
		// re-asks it.
		conv.State = StateAwaitingApproval
		conv.PendingExtensionID = extensionID
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("askApproval: %w", err)
		}
		return nil
	default:
		// Mid-booking, mid-setup or handed off: the question was sent, but
		// the next message belongs to that flow. Logged; the app still has
		// the request, and a stale YES here is refused by ResolveExtension.
		log.Printf("[channel] approval ext=%s not armed: conversation in state %s", extensionID, conv.State)
		return nil
	}
}

// clearApproval is called when the request resolved elsewhere (the app, or
// the timeout): a later YES must not answer it.
func (r *Router) clearApproval(ctx context.Context, identityID string) error {
	conv, err := r.Conversations.GetConversation(ctx, identityID)
	if err != nil {
		return fmt.Errorf("clearApproval: %w", err)
	}
	if conv.State != StateAwaitingApproval {
		return nil
	}
	conv.PendingExtensionID = ""
	conv.State = conv.ResumeState()
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
		return fmt.Errorf("clearApproval: %w", err)
	}
	return nil
}

// resumeDeferred sends and arms a held approval question once the thread
// can take it (after a handoff ends, a booking completes, setup finishes),
// if the request is still pending; otherwise it is dropped. Called off the
// request path by the resume points. Errors are logged: the person's
// own flow must not fail over the supporter's question.
func (r *Router) resumeDeferred(ctx context.Context, id Identity) {
	conv, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		log.Printf("[channel] resume deferred identity=%s: %v", id.ID, err)
		return
	}
	d := conv.DeferredApproval
	if d == nil || !canArmApproval(conv.State) {
		return
	}
	pending := false
	if r.Booker != nil {
		pending, err = r.Booker.ExtensionPending(ctx, d.UserID, d.ExtensionID)
		if err != nil {
			log.Printf("[channel] resume deferred ext=%s: %v", d.ExtensionID, err)
			return // retried at the next resume point; the question stays held
		}
	}
	conv.DeferredApproval = nil
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
		log.Printf("[channel] resume deferred identity=%s: %v", id.ID, err)
		return
	}
	if !pending {
		log.Printf("[channel] deferred approval ext=%s dropped: no longer pending", d.ExtensionID)
		return
	}
	if err := r.Send(ctx, Outbound{Channel: id.Channel, To: id.Address, Text: d.Text}, d.TaskID); err != nil {
		log.Printf("[channel] resume deferred ext=%s: %v", d.ExtensionID, err)
		return
	}
	if err := r.askApproval(ctx, id.ID, d.ExtensionID); err != nil {
		log.Printf("[channel] resume deferred ext=%s: %v", d.ExtensionID, err)
	}
}

// clearDeferred drops a held question for the task (the request resolved
// elsewhere, or timed out).
func (r *Router) clearDeferred(ctx context.Context, identityID, taskID string) error {
	conv, err := r.Conversations.GetConversation(ctx, identityID)
	if err != nil {
		return fmt.Errorf("clearDeferred: %w", err)
	}
	if conv.DeferredApproval == nil || (taskID != "" && conv.DeferredApproval.TaskID != taskID) {
		return nil
	}
	conv.DeferredApproval = nil
	return r.Conversations.SaveConversation(ctx, conv)
}
