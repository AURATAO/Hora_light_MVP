package channel

import (
	"context"
	"errors"
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

// ApprovalVerdict is what the server does with one message while an
// approval question is armed. It is decided by rules only (S-11/5, D-17
// R-4): the router acts on it, and the requester agent's test gate
// (cmd/agenttest, D-18) calls the same function — a model's proposal is
// carried out only when this verdict agrees.
type ApprovalVerdict int

const (
	VerdictNotAnAnswer   ApprovalVerdict = iota // a sentence: the context router decides; never moves money
	VerdictUnconfirmed                          // the question may not have reached them: nothing answers it
	VerdictSentBefore                           // sent before the question was asked: asked again
	VerdictReask                                // a reaction or swipe that may be about another message: asked again
	VerdictApprove                              // approve
	VerdictApprovePassOn                        // approve, then pass the whole message to the supporter (R-3)
	VerdictDecline                              // decline
	VerdictPassOnKeep                           // pass the words on; neither approved nor declined (R-2, R-3)
	VerdictToSupporter                          // I-P: a casual word while the supporter is mid-conversation
)

// ApprovalFacts are the facts the verdict depends on besides the words.
type ApprovalFacts struct {
	Text string
	// Supporter returns the request's supporter's first name; called only
	// when a "yes + message" needs it (round 5). nil = unknown.
	Supporter func() string
	// Unconfirmed: the question was claimed but its delivery not confirmed.
	Unconfirmed bool
	// SentBefore: the message was sent before the question was armed, or
	// has no usable send time (R-1).
	SentBefore bool
	// SwipedElsewhere: a WhatsApp reply swiped on a message other than the
	// question (I-AG).
	SwipedElsewhere bool
	// QuestionIsLast: our last message in the thread is the question (R-3).
	QuestionIsLast bool
	// SupporterLive: the supporter wrote within the I-P window on an active
	// task, and the message was not swiped on one of our prompts. Only the
	// router's earlier I-P step reads it; DecideApproval applies it too so
	// the agent gate sees the whole path.
	SupporterLive bool
}

// DecideApproval is the rule decision for a message answering an armed
// approval question (D-17 R-1…R-3, I-P version B).
func DecideApproval(f ApprovalFacts) ApprovalVerdict {
	text := stripFiller(f.Text)
	answerish := approvalAnswerish(text)
	if f.SupporterLive && approvalCasual(text) {
		return VerdictToSupporter
	}
	plain, reaction := approvalYes(text)
	_, restReaction, conditional, withMessage := yesWithMessage(text)
	if withMessage && conditional && f.Supporter != nil {
		if n := f.Supporter(); n != "" {
			_, restReaction, conditional, withMessage = yesWithMessage(text, n)
		}
	}
	switch {
	case answerish && f.Unconfirmed:
		return VerdictUnconfirmed
	case answerish && f.SentBefore:
		return VerdictSentBefore
	case f.SwipedElsewhere && ((plain && reaction) || (withMessage && !conditional)):
		return VerdictReask
	case (plain && reaction) || (withMessage && !conditional && restReaction):
		if !f.QuestionIsLast {
			return VerdictReask
		}
		if withMessage {
			return VerdictApprovePassOn
		}
		return VerdictApprove
	case plain:
		return VerdictApprove
	case approvalNo(text):
		return VerdictDecline
	case mixedNo(text):
		return VerdictPassOnKeep
	case withMessage && conditional:
		return VerdictPassOnKeep
	case withMessage:
		return VerdictApprovePassOn
	}
	return VerdictNotAnAnswer
}

// onAwaitingApproval answers the pending request with this message.
func (r *Router) onAwaitingApproval(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	var approve bool
	var passOn string // the person's words to pass to the supporter after a YES (R-3)
	facts := ApprovalFacts{
		Text:        text,
		Unconfirmed: conv.Unconfirmed(),
		SentBefore:  conv.ApprovalArmedAt != nil && (in.ReceivedAt.IsZero() || in.ReceivedAt.Before(*conv.ApprovalArmedAt)),
		// A WhatsApp reply swiped on some OTHER message than the question
		// (reply context is real there): a reaction word or an approving
		// yes + message may be about that message.
		SwipedElsewhere: in.Channel == WhatsApp && in.ReplyToProviderID != "" && in.ReplyToProviderID != conv.ApprovalQuestionPID,
		QuestionIsLast:  questionIsLast(conv),
		// SupporterLive stays false: the router's I-P step (casualToSupporter)
		// ran before this state handler.
	}
	if conv.ApprovalTaskID != "" {
		// Only a yes + message needs the supporter's name ("thanks Sam"):
		// looked up then, not on every message (round 5).
		facts.Supporter = func() string { return r.supporterFirstName(ctx, id, conv.ApprovalTaskID) }
	}
	switch DecideApproval(facts) {
	case VerdictUnconfirmed:
		// Claimed but never confirmed delivered: nothing can answer it.
		// Re-sent from the hold once the claim is old enough; until then
		// the person is told a question is on its way — not to reply YES
		// to one they may not have seen.
		log.Printf("[channel] approval identity=%s ext=%s: answer refused, question unconfirmed", id.ID, conv.PendingExtensionID)
		r.resumeDeferred(ctx, id)
		if after, err := r.Conversations.GetConversation(ctx, id.ID); err == nil && !after.Unconfirmed() {
			if after.State == StateAwaitingApproval {
				return nil // re-sent just now: that is the reply
			}
			// Cleared: the request was no longer theirs to answer.
			reply := CopyApprovalGone
			back := r.resumeLine(ctx, id, after)
			if back != "" {
				reply += "\n\n" + back
			}
			if err := r.Reply(ctx, in, reply, ""); err != nil {
				return err
			}
			if back != "" {
				r.markShown(ctx, id.ID)
			}
			return nil
		}
		return r.Reply(ctx, in, CopyApprovalOnItsWay, "")
	case VerdictSentBefore:
		// Sent before the question was asked (a second tap on YES for the
		// booking, say), or with no usable send time: it cannot be taken as
		// answering it. The question is put again; nothing moves (S-11/5).
		// Provider send times vs our clock: the stamp is taken AFTER the
		// send returned, so a tap made after reading is later than it by
		// the person's own reaction time; no margin is applied.
		log.Printf("[channel] approval identity=%s ext=%s: answer refused, sent %s before the question", id.ID, conv.PendingExtensionID, sentBefore(in, conv.ApprovalArmedAt))
		return r.Reply(ctx, in, approvalReask(conv), "")
	case VerdictReask:
		// A swipe on another message (round 4), or a reaction word after a
		// later message of ours (R-3): asked again; nothing moves.
		log.Printf("[channel] approval identity=%s ext=%s: reaction or swipe not on the question, re-asked", id.ID, conv.PendingExtensionID)
		return r.Reply(ctx, in, approvalReask(conv), "")
	case VerdictApprove:
		approve = true
	case VerdictApprovePassOn:
		// "yes but tell him to hurry": approve, then pass the message on —
		// whole and as written (the decision reads the part after the yes).
		approve = true
		passOn = text
	case VerdictDecline:
		approve = false
	case VerdictPassOnKeep:
		// "no! go ahead", "yes but max $5", "yes if it's organic": neither
		// approved nor declined — the words go to the supporter, the
		// request stays open (R-2, R-3).
		return r.passOnKeepingQuestion(ctx, in, id, conv, text)
	default:
		// A new errand while the request is pending is kept for after the
		// answer — approval first, then the draft, then messages (Arbiter
		// 2026-10-24; staging: "buy me an apple" went to the supporter).
		if r.errandForDraft(ctx, in, id, text) {
			source := text
			if conv.Pending != nil {
				source = conv.Pending.Source + "\n" + text // joins a draft that was waiting
			}
			return r.startDraft(ctx, in, id, conv, source)
		}
		// A sentence while the request is pending may be for the
		// supporter ("why $10 more?") — relayed, the question stays open.
		if handled, err := r.routeInContext(ctx, in, id, conv, text); handled {
			return err
		}
		return r.Reply(ctx, in, approvalReask(conv), "")
	}
	if r.Booker == nil {
		return fmt.Errorf("onAwaitingApproval: router not fully configured")
	}
	if passOn != "" && conv.ApprovalTaskID == "" {
		// Nowhere to pass the words: do not approve on half a message.
		return r.Reply(ctx, in, approvalReask(conv), "")
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
	if _, err := r.Conversations.ClearApproval(ctx, id.ID, ext, conv.State, ShownUnconfirmed); err != nil {
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
	if passOn != "" {
		// The person's words around the YES, in the same reply (I-W): passed
		// on only when the approval went through; otherwise they are told
		// it was not.
		who := r.supporterFirstName(ctx, id, conv.ApprovalTaskID)
		// A closed shape goes as a polite fixed line in the person's name
		// (they talk to a bot; the supporter is a person — Arbiter
		// 2026-10-24); anything else as written.
		words, told := passOn, fmt.Sprintf(CopyPassedOn, strings.TrimSpace(passOn), who)
		switch passOnKind(passOn) {
		case PassOnHurry:
			words, told = CopyToSupporterHurry, fmt.Sprintf(CopyAskedToHurry, who)
		case PassOnThanks:
			words, told = CopyToSupporterThanks, fmt.Sprintf(CopyPassedThanks, who)
		case PassOnLetKnow:
			words, told = CopyToSupporterLetKnow, fmt.Sprintf(CopyLetKnow, who)
		}
		switch {
		case res.Outcome != ExtResolved:
			reply += " " + CopyNotPassedOn
		case r.relayQuiet(ctx, in, id, conv.ApprovalTaskID, words):
			reply += " " + told
		default:
			reply += " " + fmt.Sprintf(CopyCouldntPassOn, strings.TrimSpace(passOn), who)
		}
	}
	back := ""
	if !r.nextHoldLive(ctx, id, conv) {
		// Our own question comes back — unless a live held question is
		// about to follow (one question per burst; the draft's prompt
		// returns after that answer).
		back = r.resumeLine(ctx, id, conv)
		if back != "" {
			reply += "\n\n" + back
		}
	}
	if err := r.Reply(ctx, in, reply, res.TaskID); err != nil {
		return err
	}
	if back != "" {
		r.markShown(ctx, id.ID) // our question was put again (R-1)
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
	cleared, err = r.Conversations.ClearApproval(ctx, identityID, conv.PendingExtensionID, conv.ResumeState(), ShownUnconfirmed)
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
// the store — state and request set, armed-at = ArmedUnconfirmed,
// claimed-at stamped, the hold kept — so concurrent resume points ask
// once; then the question is sent; then ConfirmArmed stamps the real time
// and drops the hold. Between claim and confirm no message can answer it
// (every message precedes ArmedUnconfirmed), and a crash there is
// repaired here once the claim is older than UnconfirmedResend: re-sent
// from the hold if the request is still live, cleared otherwise. A failed
// send undoes the claim on a context that outlives the caller's. Errors
// are logged — the person's own flow must not fail over the supporter's
// question.
func (r *Router) resumeDeferred(ctx context.Context, id Identity) {
	conv, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		log.Printf("[channel] resume deferred identity=%s: %v", id.ID, err)
		return
	}
	if conv.Unconfirmed() {
		r.repairUnconfirmed(ctx, id, conv)
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
		live, err := r.holdLive(ctx, id, d)
		if err != nil {
			// Kept for the next resume point; the others are not held up
			// behind it.
			log.Printf("[channel] resume deferred ext=%s: %v", d.ExtensionID, err)
			continue
		}
		if !live {
			log.Printf("[channel] deferred approval ext=%s dropped: no longer the person's to answer", d.ExtensionID)
			if err := r.Conversations.RemoveHold(ctx, id.ID, d.ExtensionID); err != nil {
				log.Printf("[channel] resume deferred identity=%s: %v", id.ID, err)
				return
			}
			continue
		}
		claimed, err := r.Conversations.ClaimHold(ctx, id.ID, d.ExtensionID, d.TaskID, r.now())
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

// repairUnconfirmed: a claim whose delivery was never confirmed. Younger
// than UnconfirmedResend it may still be in flight — left alone. Older, it
// is re-sent from its hold if the request is still live, and otherwise
// cleared (nothing could answer it anyway; the thread gets its state back).
func (r *Router) repairUnconfirmed(ctx context.Context, id Identity, conv Conversation) {
	if conv.ApprovalClaimedAt != nil && r.now().Sub(*conv.ApprovalClaimedAt) < UnconfirmedResend {
		return
	}
	for _, d := range conv.Deferred {
		if d.ExtensionID != conv.PendingExtensionID {
			continue
		}
		live, err := r.holdLive(ctx, id, d)
		if err != nil {
			log.Printf("[channel] approval ext=%s: unconfirmed, liveness unknown: %v", d.ExtensionID, err)
			return
		}
		if live {
			won, err := r.Conversations.ReclaimUnconfirmed(ctx, id.ID, d.ExtensionID, conv.ApprovalClaimedAt, r.now())
			if err != nil {
				log.Printf("[channel] approval ext=%s: %v", d.ExtensionID, err)
				return
			}
			if !won {
				return // another repair is re-sending it
			}
			log.Printf("[channel] approval ext=%s: claim unconfirmed for %s, re-sending", d.ExtensionID, UnconfirmedResend)
			r.sendClaimed(ctx, id, d)
			return
		}
		break
	}
	// No hold to re-send from, or the request is no longer live: end it.
	log.Printf("[channel] approval ext=%s: unconfirmed claim cleared", conv.PendingExtensionID)
	if _, err := r.Conversations.ClearApproval(ctx, id.ID, conv.PendingExtensionID, conv.ResumeState(), ShownUnconfirmed); err != nil {
		log.Printf("[channel] resume deferred identity=%s: %v", id.ID, err)
		return
	}
	if err := r.Conversations.RemoveHold(ctx, id.ID, conv.PendingExtensionID); err != nil {
		log.Printf("[channel] resume deferred identity=%s: %v", id.ID, err)
	}
}

// holdLive: whether a held request is still this person's to answer —
// their own, inside the app's auto-decline window by our clock (expiry is
// lazy), and pending by the booker (pending, task open, same supporter,
// not a blocked pair), with something to ask.
func (r *Router) holdLive(ctx context.Context, id Identity, d DeferredApproval) (bool, error) {
	if d.UserID != id.UserID || r.remainingMinutes(d) == 0 {
		return false, nil
	}
	if _, ok := approvalText(r.heldEvent(d)); !ok {
		return false, nil
	}
	if r.Booker == nil {
		return true, nil
	}
	return r.Booker.ExtensionPending(ctx, d.UserID, d.ExtensionID)
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
// point. If the request ended while the send was in flight (the confirm
// finds the claim gone), the person is told so the question they just
// read is not left standing.
func (r *Router) sendClaimed(ctx context.Context, id Identity, d DeferredApproval) {
	text, _ := approvalText(r.heldEvent(d))
	questionOut, questionPID, err := r.sendStamped(ctx, Outbound{Channel: id.Channel, To: id.Address, Text: text}, d.TaskID)
	if err != nil {
		var pe *ProviderStatusError
		definite := errors.As(err, &pe) && pe.Status >= 400 && pe.Status <= 499 && pe.Status != 408 && pe.Status != 409 && pe.Status != 429
		if !definite {
			// It may have gone through: left unconfirmed, for the repair
			// (re-sent or cleared once the claim is old enough).
			log.Printf("[channel] resume deferred ext=%s: send uncertain, claim kept unconfirmed: %v", d.ExtensionID, err)
			return
		}
		log.Printf("[channel] resume deferred ext=%s: send refused, hold kept: %v", d.ExtensionID, err)
		undo := context.WithoutCancel(ctx)
		conv, gerr := r.Conversations.GetConversation(undo, id.ID)
		if gerr != nil {
			log.Printf("[channel] resume deferred identity=%s: %v", id.ID, gerr)
			return
		}
		if _, err := r.Conversations.ClearApproval(undo, id.ID, d.ExtensionID, conv.ResumeState(), ShownUnconfirmed); err != nil {
			log.Printf("[channel] resume deferred identity=%s: %v", id.ID, err)
		}
		return
	}
	confirmed, err := r.Conversations.ConfirmArmed(context.WithoutCancel(ctx), id.ID, d.ExtensionID, r.now(), questionOut, questionPID)
	if err != nil {
		// Still armed as unconfirmed: unanswerable, re-sent at the next
		// resume point (a repeat over a lost question).
		log.Printf("[channel] resume deferred ext=%s: confirm failed: %v", d.ExtensionID, err)
		return
	}
	if !confirmed {
		// The claim went while the send was in flight: a resolution (the
		// request ended — say so, and bring our own question back), or a
		// handoff (the hold survives and is asked again after release —
		// nothing to say).
		live, err := r.holdLive(ctx, id, d)
		if err != nil || live {
			log.Printf("[channel] resume deferred ext=%s: claim gone while in flight (live=%t): %v", d.ExtensionID, live, err)
			return
		}
		log.Printf("[channel] resume deferred ext=%s: ended while the question was in flight", d.ExtensionID)
		text := CopyApprovalGone
		back := ""
		if conv, err := r.Conversations.GetConversation(ctx, id.ID); err == nil {
			if back = r.resumeLine(ctx, id, conv); back != "" {
				text += "\n\n" + back
			}
		}
		if err := r.Send(ctx, Outbound{Channel: id.Channel, To: id.Address, Text: text}, d.TaskID); err != nil {
			log.Printf("[channel] resume deferred ext=%s: %v", d.ExtensionID, err)
		} else if back != "" {
			r.markShown(ctx, id.ID)
		}
	}
}

// nextHoldLive: whether any held question is still live — i.e. whether one
// will follow the current reply (so our own prompt is kept for later).
func (r *Router) nextHoldLive(ctx context.Context, id Identity, conv Conversation) bool {
	for _, d := range conv.Deferred {
		if live, err := r.holdLive(ctx, id, d); err == nil && live {
			return true
		}
	}
	return false
}

// approvalReask: the re-ask for an armed question — or, while its delivery
// is unconfirmed, that it is on its way (never "reply YES" to a question
// the person may not have).
func approvalReask(conv Conversation) string {
	if conv.Unconfirmed() {
		return CopyApprovalOnItsWay
	}
	return CopyApprovalReask
}

// questionIsLast: nothing of ours has been sent since the armed question
// (D-17 R-3, the reaction-word rule). Unknown → false (asked again).
func questionIsLast(conv Conversation) bool {
	return conv.LastOutboundAt != nil && conv.ApprovalQuestionOutAt != nil &&
		conv.LastOutboundAt.Equal(*conv.ApprovalQuestionOutAt)
}

// passOnKeepingQuestion relays the person's words to the armed request's
// supporter and tells them the question still waits — nothing approved,
// nothing declined (R-3: a yes with a condition).
func (r *Router) passOnKeepingQuestion(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	if conv.ApprovalTaskID == "" || r.Chat == nil {
		return r.Reply(ctx, in, approvalReask(conv), "")
	}
	who := r.supporterFirstName(ctx, id, conv.ApprovalTaskID)
	line := fmt.Sprintf(CopyCouldntPassOn, strings.TrimSpace(text), who)
	if r.relayQuiet(ctx, in, id, conv.ApprovalTaskID, text) {
		line = fmt.Sprintf(CopyPassedOn, strings.TrimSpace(text), who)
	}
	return r.Reply(ctx, in, line+" "+stillWaiting(conv), "") // the question still waits either way
}

// supporterFirstName for a task, for "(Passed … to Alex.)".
func (r *Router) supporterFirstName(ctx context.Context, id Identity, taskID string) string {
	if r.Chat != nil {
		if active, err := r.Chat.ActiveTasks(ctx, id.UserID); err == nil {
			if t, ok := taskIn(active, taskID); ok {
				if n := DisplayName(FirstName(t.SupporterName)); n != "" {
					return n
				}
			}
		}
	}
	return "your supporter"
}

// relayQuiet passes words to a task's supporter and reports whether they
// went; no message of its own (the caller composes one reply).
func (r *Router) relayQuiet(ctx context.Context, in Inbound, id Identity, taskID, text string) bool {
	if r.Chat == nil || taskID == "" {
		return false
	}
	if err := r.Chat.PostAsRequester(ctx, taskID, id.UserID, capText(text), in.ProviderMessageID); err != nil {
		log.Printf("[channel] relay identity=%s task=%s: %v", id.ID, taskID, err)
		return false
	}
	r.noteRelayed(ctx, in, id, taskID, text)
	return true
}
