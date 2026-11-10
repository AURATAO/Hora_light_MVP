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

	"hora-auth/internal/channel/llm"
)

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

// scopeGate checks one message. handled: it was refused (and answered).
func (r *Router) scopeGate(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) (bool, error) {
	sr := scopeRules()
	if sr == nil {
		return false, nil
	}
	d, err := sr.Decide(ctx, r.Scope, text)
	if err != nil {
		log.Printf("[channel] scope check identity=%s (allowed): %v", id.ID, err)
	}
	if d.By != "no_scope_words" {
		log.Printf("[channel] scope identity=%s: %s by=%s", id.ID, d.ID(), d.By)
	}
	if d.Allowed() {
		return false, nil
	}
	return true, r.refuseScope(ctx, in, id, conv, d)
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
