package channel

// HELP / INFO keyword (2026-11-04). Carriers expect a message that is only
// HELP to get a fixed answer: who we are, how to reach us, how to stop. It
// is sent by rule, never by a model.
//
// On HO:RA "help" may also mean someone is in trouble, so the info line is
// the narrow case:
//   - "help" with anything to it ("help me", "help!!", "I need help",
//     "can someone help") is a plea: a hand-off to ops by rule, the same as
//     "talk to a person" (IsHumanRequest);
//   - a bare HELP from someone with a task under way (posted, being worked
//     on, waiting on a payment step) is a plea too;
//   - only a bare HELP or INFO with nothing under way gets the info line.
//     INFO never reads as trouble and always gets it.
// A help word inside an errand ("I need help moving a couch") is neither:
// it is a request, read as usual.
//
// A stopped number never gets here (optout.go runs first): its HELP is
// forwarded to ops as urgent (OptedOutUrgency) and nothing is sent back.

import (
	"context"
	"regexp"
	"strings"
)

// CopyHelpInfo answers a bare HELP / INFO. At most 160 characters, plain
// ASCII quotes: one SMS segment on Sendblue's SMS fallback (a curly quote
// would make it UCS-2, 70 a segment).
const CopyHelpInfo = `HO:RA: NYC errands. Say what you need to book, "cancel task" to cancel, "talk to a person" for a human. info@my-hora.com. STOP to end. Msg&data rates may apply.`

var (
	helpTrimRE = regexp.MustCompile(`^[\s.?,;:"'“”‘’()*~-]+|[\s.?,;:"'“”‘’()*~-]+$`)
	helpWordRE = regexp.MustCompile(`[a-z']+`)
	// helpPleaWords: what may stand around "help" in a plea. A word outside
	// it ("moving", "groceries") makes the message an errand.
	helpPleaWords = map[string]bool{
		"help": true, "me": true, "us": true, "i": true, "we": true, "i'm": true, "im": true,
		"need": true, "needs": true, "please": true, "pls": true, "plz": true,
		"someone": true, "somebody": true, "anyone": true, "anybody": true,
		"can": true, "could": true, "you": true, "now": true, "asap": true,
		"urgent": true, "urgently": true, "sos": true, "hey": true, "hi": true,
		"hello": true, "hora": true, "your": true, "some": true, "a": true,
		"in": true, "trouble": true, "the": true, "is": true, "there": true,
	}
)

// IsHelpKeyword reports a message that is only HELP: any case, with
// optional "." or "?" around it. "!" is not optional punctuation here —
// "help!" is a plea (IsHelpPlea).
func IsHelpKeyword(text string) bool {
	return strings.ToLower(helpTrimRE.ReplaceAllString(text, "")) == "help"
}

// IsInfoKeyword reports a message that is only INFO, any case and
// punctuation.
func IsInfoKeyword(text string) bool {
	return strings.ToLower(helpTrimRE.ReplaceAllString(strings.TrimRight(text, "! "), "")) == "info"
}

// IsHelpPlea reports "help" with anything to it that is not an errand:
// "help!", "help me", "I need help", "can someone help us please".
func IsHelpPlea(text string) bool {
	if strings.Contains(text, "救命") {
		return true
	}
	if IsHelpKeyword(text) {
		return false
	}
	t := strings.ToLower(strings.ReplaceAll(text, "’", "'"))
	words := helpWordRE.FindAllString(t, -1)
	hasHelp := false
	for _, w := range words {
		w = strings.Trim(w, "'")
		if w == "help" {
			hasHelp = true
		}
		if w != "" && !helpPleaWords[w] {
			return false
		}
	}
	return hasHelp
}

// helpUnderWay: whether a bare HELP may be someone in trouble — they have a
// task under way, or we could not tell. A number with no account has none.
func (r *Router) helpUnderWay(ctx context.Context, id Identity, conv Conversation) bool {
	if conv.State == StateAwaitingApproval || len(conv.Deferred) > 0 {
		return true // a supporter is asking them something: a task is on
	}
	open := r.openTasksFor(ctx, id)
	return open.openTasksUnknown || len(open.openTasks) > 0
}

// routeHelp answers HELP / INFO by rule. handled is false for anything
// else. The info line is the iMessage line's only (it names STOP, a
// carrier keyword WhatsApp does not have); there a bare HELP or INFO with
// nothing under way is read as usual.
func (r *Router) routeHelp(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) (handled bool, err error) {
	help := IsHelpKeyword(text)
	if !help && !IsInfoKeyword(text) {
		return false, nil
	}
	if help && r.helpUnderWay(ctx, id, conv) {
		return true, r.handOff(ctx, in, id, conv, HandoffHumanRequested)
	}
	if !optOutApplies(in.Channel) {
		return false, nil
	}
	return true, r.Reply(ctx, in, CopyHelpInfo, "")
}
