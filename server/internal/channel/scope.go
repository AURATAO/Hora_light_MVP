package channel

// Scope rules (2026-11-10): what HO:RA does and does not do about people and
// homes (llm/scope.go, playbooks.json "scope_rules"). Every message the
// person can still act on is checked here, ahead of discovery, drafting and
// relaying to a supporter: a refused request gets its fixed reply and never
// a draft, and the words never reach a supporter.
//
// Rules first; the small model (LLM_SMALL_MODEL, Router.Scope) only for
// words that touch the scope and that no rule decides. Always on: it does
// not wait for CHANNEL_PLAYBOOKS, which only adds the companionship and
// home-entry questions.
//
// A romantic, sexual or physical request (an "alert" category) also goes to
// ops at once, marked urgent, with the open tasks: supporter safety. It is a
// notice, not a hand-off — the chat stays with the bot — and an open draft
// is dropped, so nothing from that thread is booked without ops seeing it.

import (
	"context"
	"log"
	"strings"
	"time"

	"hora-auth/internal/channel/llm"
)

// ScopeTimeout bounds the model's scope reading (the provider's own timeout
// is 45 s; Sonnet answers in ~1.3 s, p90 ~1.6 s). Past it the message fails
// closed: ops, not a draft.
var ScopeTimeout = 8 * time.Second

// HandoffScopeUnchecked: words that touch the scope could not be checked
// (the model failed or timed out). Failing closed: a hand-off, not a draft.
const HandoffScopeUnchecked HandoffReason = "scope_unchecked"

// CopyScopeHolding answers a message the scope check could not read.
const CopyScopeHolding = "A HO:RA teammate will look at this and get back to you shortly."

// HandoffScopeAlert: a request in an alert category was declined. Not a
// hand-off: the person got the decline, the thread stays with the bot.
const HandoffScopeAlert HandoffReason = "scope_alert"

// scopeRules is the embedded set: always present (ParsePlaybooks checks it
// at startup through the tests; a parse error turns the gate off and says so).
func scopeRules() *llm.ScopeRules {
	s, err := llm.Playbooks()
	if err != nil || s == nil {
		log.Printf("[channel] scope rules unavailable: %v", err)
		return nil
	}
	return &s.ScopeRules
}

// ScopeFollowupWindow: how long after a refusal the person's next message
// is first read as a clarification of the refused one ("it's bubble tea").
var ScopeFollowupWindow = 30 * time.Minute

// scopeThreadLines: how much of the conversation the model reads with an
// ambiguous message.
const scopeThreadLines = 6

// scopeContext is the thread before this message, as the model reads it,
// and the message HO:RA declined just before it ("" when our last line was
// not a refusal, or is older than ScopeFollowupWindow). It reads the
// thread only when it can matter: words the rules leave to the model, or
// a reply of ours recent enough to be a refusal being answered.
func (r *Router) scopeContext(ctx context.Context, in Inbound, id Identity, conv Conversation, sr *llm.ScopeRules, text string) (thread []string, refused string) {
	if r.Thread == nil {
		return nil, ""
	}
	recentReply := conv.LastOutboundAt != nil && r.now().Sub(*conv.LastOutboundAt) <= ScopeFollowupWindow
	if sr.Rule(text).Decided && !recentReply {
		return nil, ""
	}
	msgs, err := r.Thread.RecentMessages(ctx, id.Channel, id.Address, scopeThreadLines+1)
	if err != nil {
		log.Printf("[channel] scope thread identity=%s: %v", id.ID, err)
		return nil, ""
	}
	// This message is already recorded: the thread is what came before it.
	if n := len(msgs); n > 0 && msgs[n-1].Direction == "inbound" &&
		(msgs[n-1].ProviderID == in.ProviderMessageID || strings.TrimSpace(msgs[n-1].Text) == strings.TrimSpace(in.Text)) {
		msgs = msgs[:n-1]
	}
	if len(msgs) > scopeThreadLines {
		msgs = msgs[len(msgs)-scopeThreadLines:]
	}
	for _, m := range msgs {
		who := "Customer: "
		if m.Direction == "outbound" {
			who = "HO:RA: "
			if m.Relay {
				who = "Supporter (relayed by HO:RA): "
			}
		}
		if t := strings.TrimSpace(m.Text); t != "" {
			thread = append(thread, who+t)
		}
	}
	// Our last line a refusal, recent, with the person's message before it.
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Direction != "outbound" || m.Relay {
			continue
		}
		if sr.RefusalByReply(m.Text) == nil || (!m.At.IsZero() && r.now().Sub(m.At) > ScopeFollowupWindow) {
			break
		}
		for j := i - 1; j >= 0; j-- {
			if msgs[j].Direction == "inbound" && strings.TrimSpace(msgs[j].Text) != "" {
				refused = strings.TrimSpace(msgs[j].Text)
				break
			}
		}
		break
	}
	return thread, refused
}

// scopeGate checks one message. handled: it was refused (and answered).
// Otherwise text is the words to go on with: the message itself, or —
// when it clarified a request declined just before and the two together
// are allowed — that request with the clarification, so the errand goes
// on as if the first message had been allowed.
func (r *Router) scopeGate(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) (bool, string, error) {
	sr := scopeRules()
	if sr == nil {
		return false, text, nil
	}
	thread, refused := r.scopeContext(ctx, in, id, conv, sr, text)
	sctx, cancel := context.WithTimeout(ctx, ScopeTimeout)
	d, err := sr.DecideIn(sctx, r.Scope, llm.ScopeQuery{Text: text, Thread: thread, Refused: refused})
	cancel()
	if err != nil {
		// Sensitive words and no reading of them: fail closed. Ops take
		// the thread; nothing is drafted. Urgent when an intimate pattern
		// is there at all. The log carries the failure's kind only.
		urgent := sr.MatchesAlertPattern(text)
		log.Printf("[channel] scope check FAILED identity=%s kind=%s urgent=%t: handed to ops", id.ID, llm.ErrorKind(err), urgent)
		return true, text, r.handOffWith(ctx, in, id, conv, HandoffScopeUnchecked, handoffWith{
			copy: CopyScopeHolding, detail: "the scope check could not read a message (" + llm.ErrorKind(err) + ")",
			urgent: urgent, urgentReason: "unchecked message with a romantic/sexual pattern",
		})
	}
	if d.By != "no_scope_words" || refused != "" {
		log.Printf("[channel] scope identity=%s: %s by=%s after_refusal=%t clarifies=%t thread=%d", id.ID, d.ID(), d.By, refused != "", d.Clarifies, len(thread))
	}
	if d.Allowed() {
		if d.Clarifies {
			// The refused request, now understood: it goes on with the
			// clarification as one request.
			return false, llm.JoinClarified(refused, text), nil
		}
		return false, text, nil
	}
	return true, text, r.refuseScope(ctx, in, id, conv, d)
}

// refusedByRule: the rules alone on everything said for a draft (the
// words of several messages together) — no model call.
func refusedByRule(source string) *llm.ScopeRefusal {
	return scopeRules().RefusedByRule(source)
}

// draftStates: where a pending draft is the open question.
func draftState(s State) bool {
	switch s {
	case StateAwaitingConfirm, StateAwaitingName, StateAwaitingSetup, StateAwaitingBudget:
		return true
	}
	return false
}

// refuseScope answers a refused request: the category's fixed reply, never
// a draft. An open draft stays for an ordinary refusal (the person is told
// it waits) and is dropped for an alert one; ops are told after the reply.
func (r *Router) refuseScope(ctx context.Context, in Inbound, id Identity, conv Conversation, d llm.ScopeDecision) error {
	ref := d.Refusal
	reply := ref.Reply
	if conv.Pending != nil && draftState(conv.State) {
		if ref.Alert {
			conv.Pending, conv.State = nil, StateIdle
			if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
				return wrapErr("refuseScope", err)
			}
		} else {
			reply += "\n\n" + CopyBookingStillWaiting
		}
	}
	log.Printf("[channel] scope refused identity=%s category=%s by=%s alert=%t", id.ID, ref.ID, d.By, ref.Alert)
	if err := r.Reply(ctx, in, reply, ""); err != nil {
		return err
	}
	if ref.Alert {
		r.scopeAlert(context.WithoutCancel(ctx), id, ref, d.By)
	}
	return nil
}

// scopeAlert tells ops, urgent, with the recent thread and the person's
// open tasks (a supporter may be with them). A failure is logged: the
// person has their answer either way.
func (r *Router) scopeAlert(ctx context.Context, id Identity, ref *llm.ScopeRefusal, by string) {
	open := r.openTasksFor(ctx, id)
	open.urgent, open.urgentReason, open.detail = true, ref.Name, "declined by "+by+"; no draft was made"
	if err := r.notifyOps(ctx, id, HandoffScopeAlert, false, open); err != nil {
		log.Printf("[channel] scope alert identity=%s category=%s: ops NOT reached: %v", id.ID, ref.ID, err)
		return
	}
	log.Printf("[channel] scope alert identity=%s category=%s: ops told (open tasks %d)", id.ID, ref.ID, len(open.openTasks))
}

// ── home entry (playbooks on) ───────────────────────────────────────────

// homeEntryNote: the supporter note for a draft whose words send the
// supporter inside a home ("" otherwise).
func (r *Router) homeEntryNote(source string) string {
	if r.Playbooks == nil || !r.Playbooks.ScopeRules.EntersHome(source) {
		return ""
	}
	return r.Playbooks.ScopeRules.HomeEntry.SupporterNote
}

// homeEntryLine: the summary line for such a draft.
func (r *Router) homeEntryLine(p *PendingDraft) string {
	if r.Playbooks == nil || p == nil || !r.Playbooks.ScopeRules.EntersHome(p.Source) {
		return ""
	}
	return r.Playbooks.ScopeRules.HomeEntry.SummaryLine
}
