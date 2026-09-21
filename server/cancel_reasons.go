package main

import (
	"fmt"
	"strings"
)

// Why a task was cancelled, as a closed set.
//
// TWO AUDIENCES, AND ONLY ONE OF THEM GETS THE FREE TEXT.
//
// tasks.cancel_reason is free text and always has been: mobile sends one of
// its own hardcoded preset strings, web sends whatever the requester typed
// into a textarea, and the ops panel sends whatever an admin typed. Until now
// all three were relayed to the supporter verbatim — "The requester cancelled
// the task. Reason: <anything>". An ops note written for the ops feed ("caller
// says supporter was rude, refunding, see ticket 412") is not a sentence to
// forward to the person it is about.
//
// So: the CODE is what crosses between the parties, and the code resolves to a
// friendly label that a person wrote on purpose. The free text stays on the
// row, in the audit log and in the ops feed, which is where it was useful.
//
// The presets live here rather than in the two clients for the same reason
// budgetReasons does (server/extensions.go): a product vocabulary kept in two
// hardcoded copies drifts the first time a sixth option is added, and the
// supporter's notification has to be able to name the option the requester
// actually picked.
type cancelReason struct {
	Value string `json:"value"`
	Label string `json:"label"`
	// What the OTHER party is told. Written from the outside in: the requester
	// picked "Changed my mind", and their supporter reads "plans changed".
	// Empty means nothing is relayed at all — see cancelReasonForCounterparty.
	Relay string `json:"-"`
}

// Ordered as they render. "Other" is last and deliberately relays nothing:
// whatever a requester types into the note field is for ops, not for the
// supporter, and "Reason: Other" tells nobody anything.
var cancelReasons = []cancelReason{
	{Value: "plans_changed", Label: "Plans changed", Relay: "plans changed"},
	{Value: "no_longer_needed", Label: "No longer needed", Relay: "it's no longer needed"},
	{Value: "found_another_way", Label: "Found another way", Relay: "they found another way"},
	{Value: "wrong_details", Label: "Posted with wrong details", Relay: "the task details were wrong"},
	{Value: "posted_by_mistake", Label: "Posted by mistake", Relay: "it was posted by mistake"},
	{Value: "timing_no_longer_works", Label: "Timing no longer works", Relay: "the timing no longer works"},
	{Value: "other", Label: "Other", Relay: ""},
}

// cancelReasonLabel is the preset's own label, for the party who chose it and
// for the read-only detail both sides can open afterwards. Empty for an
// unknown or absent code.
func cancelReasonLabel(code string) string {
	for _, r := range cancelReasons {
		if r.Value == code {
			return r.Label
		}
	}
	return ""
}

// cancelReasonPublicLabel is the preset's label when the preset actually says
// something, and empty when it does not.
//
// The difference from cancelReasonLabel is "Other", which has a label (it is a
// radio option) and no meaning (it stands for free text nobody else may read).
// Rendering "Reason: Other" to a supporter whose afternoon was just rearranged
// is worse than rendering no reason: it looks like an answer.
func cancelReasonPublicLabel(code string) string {
	for _, r := range cancelReasons {
		if r.Value == code {
			if r.Relay == "" {
				return ""
			}
			return r.Label
		}
	}
	return ""
}

// cancelReasonForCounterparty is the sentence fragment the OTHER party is
// shown: "Reason: plans changed".
//
// Empty for "other", for an unknown code, and for every task cancelled before
// codes existed — all three cases where the only thing on the row is free
// text. An empty answer is the correct one: the notification simply says the
// task was cancelled, which is what it said before this existed, rather than
// forwarding a stranger's typing.
//
// NEVER give this the free text as a fallback. That is the whole point.
func cancelReasonForCounterparty(code string) string {
	for _, r := range cancelReasons {
		if r.Value == code {
			return r.Relay
		}
	}
	return ""
}

// normalizeCancelReasonCode accepts what the clients actually send.
//
// Both clients are being updated to send `reason_code`, but shipped builds are
// not: mobile sends its preset LABEL as free text ("Changed my mind") and web
// sends a typed sentence. Matching a known label back to its code lets a
// shipped mobile build keep producing relayable reasons without an update,
// and anything unrecognised resolves to "" — which relays nothing, which is
// the safe direction.
func normalizeCancelReasonCode(code, text string) string {
	if c := strings.TrimSpace(code); c != "" {
		if cancelReasonLabel(c) != "" {
			return c
		}
		return ""
	}
	t := strings.TrimSpace(text)
	if t == "" {
		return ""
	}
	for _, r := range cancelReasons {
		if strings.EqualFold(r.Label, t) {
			return r.Value
		}
	}
	// The labels shipped mobile build 11 uses, which are not all word-for-word
	// the ones above. Mapped explicitly rather than fuzzily: a near-match that
	// guesses wrong relays the wrong reason to a real person.
	switch strings.ToLower(t) {
	case "changed my mind":
		return "plans_changed"
	case "posted by mistake":
		return "posted_by_mistake"
	case "found another way":
		return "found_another_way"
	case "took too long to get accepted":
		return "timing_no_longer_works"
	}
	return ""
}

// supporterCancelBody is what the supporter is told when a task they accepted
// is called off.
//
// Two sentences at most, and both of them answer a question the build 11 copy
// left open. "Task cancelled, thanks for your time" answered neither: it never
// said WHY, and with no number in it, it read as "and you are getting
// nothing" — which under the current policy is usually false.
//
// The reason is the preset's relay phrase and nothing else. A task cancelled
// under "Other", by an ops admin, or before reason codes existed simply
// carries no reason clause, which is better than forwarding whatever somebody
// typed into a box (see cancelReasonForCounterparty).
func supporterCancelBody(reasonCode string, billCents, loggedMinutes int) string {
	opening := "The requester cancelled the task."
	if relay := cancelReasonForCounterparty(reasonCode); relay != "" {
		opening = fmt.Sprintf("The requester cancelled the task — %s.", relay)
	}
	if billCents <= 0 {
		return opening
	}
	// Whether any clock ran decides how the money is explained, not whether
	// there is any. The base fee is paid because they committed.
	if loggedMinutes <= 0 {
		return fmt.Sprintf(
			"%s You're paid %s — the base fee is yours because you'd already accepted.",
			opening, formatCentsUSD(billCents))
	}
	return fmt.Sprintf(
		"%s You're paid %s — the base fee plus the %s you logged.",
		opening, formatCentsUSD(billCents), formatMinutes(loggedMinutes))
}
