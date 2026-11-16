package llm

import (
	"os"
	"strings"
)

// Companionship is paused for the pilot (Aura 2026-11-16, after the legal
// review of the pilot scope). It is OFF unless COMPANIONSHIP_ENABLED=1:
// merging this turns it off, and setting the variable turns it back on with
// nothing else to change. The playbook, its scope sentences, its prices and
// its supporter notes all stay in place for that day.
//
// While paused, a request for it is declined with a fixed reply on every
// path a task can come from: the chat's gate (channel/scope.go), the web
// app's and the assistant's task creation, and the AI parse
// (server/task_scope.go). The words that mean "companionship" are the
// companionship playbook's own match words, so the pause and the playbook
// cannot disagree about what it is.

// CompanionshipPausedID is the refusal's id in logs and API answers.
const CompanionshipPausedID = "companionship_paused"

const companionshipPausedReply = "Sorry, HO:RA isn't offering companionship right now. Supporters can run errands for you: pickups, deliveries, groceries, laundry, returns, waiting in line. If you need one of those, send it over."

// CompanionshipEnabled reports whether companionship is offered.
func CompanionshipEnabled() bool {
	return strings.TrimSpace(os.Getenv("COMPANIONSHIP_ENABLED")) == "1"
}

// IsCompanionshipCategory: the two spellings a task's category has for it.
func IsCompanionshipCategory(category string) bool {
	return category == "companionship" || category == "companion"
}

// PausedRefusal is the refusal for words or a category that ask for
// companionship while it is paused; nil when it is offered, or when they
// ask for something else. Never an alert: nothing is wrong with the ask.
func (s *PlaybookSet) PausedRefusal(words, category string) *ScopeRefusal {
	if CompanionshipEnabled() {
		return nil
	}
	asks := IsCompanionshipCategory(category)
	if !asks && s != nil && strings.TrimSpace(words) != "" {
		if pb := s.Match(words); pb != nil && IsCompanionshipCategory(pb.Category) {
			asks = true
		}
	}
	if !asks {
		return nil
	}
	return &ScopeRefusal{
		ID:    CompanionshipPausedID,
		Name:  "companionship (paused for the pilot)",
		Rule:  "Companionship is not offered for now.",
		Reply: companionshipPausedReply,
	}
}
