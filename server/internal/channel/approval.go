package channel

import (
	"context"
	"fmt"
	"log"
	"strings"
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

// approvalText turns the app's request notification into the chat's
// question. The app copy ends with an instruction to act in the app; the
// chat replaces it with its own.
func approvalText(ev StatusEvent) (string, bool) {
	body := strings.TrimSpace(ev.Body)
	if body == "" || ev.ExtensionID == "" {
		return "", false
	}
	if i := strings.Index(body, " Approve or decline in the app"); i >= 0 {
		body = body[:i]
	}
	mins := ev.ApprovalMinutes
	if mins <= 0 {
		return body + "\n\n" + CopyApprovalAsk, true
	}
	return body + "\n\n" + fmt.Sprintf(CopyApprovalAskTimed, mins), true
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
	if conv.Pending != nil {
		// Back to the request they were confirming.
		reply += "\n\n" + CopyBackToDraft + "\n" + r.Summary(conv.Pending)
	}
	return r.Reply(ctx, in, reply, res.TaskID)
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
	case StateIdle, StateAwaitingConfirm, StateAwaitingApproval:
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
