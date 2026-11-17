package channel

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"

	"hora-auth/internal/channel/llm"
)

// Where to bring it (2026-11-17). A pickup or a purchase with no drop-off
// ("a coffee at Blue Bottle", "Tylenol from the CVS on Broadway", "my
// package at the UPS store") went straight to the budget and the summary:
// the supporter would have had the coffee and nowhere to take it. Such an
// errand is asked where to bring it before the budget question and the
// summary — with the person's last drop-off offered when there is one —
// and no summary is ever shown, nor booked, without one. Errands that
// genuinely have one place (waiting in line, checking stock, laundry at the
// shop, companionship, a return or a parcel to mail) are not asked.

const (
	CopyAskDropOff     = "Where should your supporter bring it? An address is enough, like “120 E 7th St”."
	CopyAskDropOffFor  = "Where should your supporter bring “%s”? An address is enough, like “120 E 7th St”."
	CopyAskDropOffLast = "Where should your supporter bring it? Same as last time, %s? Reply YES or send the address."
	// CopyAskDropOffAgain: a YES with nothing suggested, or words that are
	// not a place.
	CopyAskDropOffAgain = "Just the address to bring it to, like “120 E 7th St”."
)

// oneStopPlaybooks: types whose errand happens at one place.
var oneStopPlaybooks = map[string]bool{"queue": true, "check": true, "companionship": true, "laundry": true, "small_services": true}

// oneStopWordsRE: words that make the one stop the destination itself (a
// return, a parcel to mail, a donation) or the whole errand (wait, check).
var oneStopWordsRE = regexp.MustCompile(`(?i)\b(?:return(?:s|ing)?|mail(?:ing)?|ship(?:ping)?|drop(?:ping)?[ -]?off|donat\w*|recycl\w*|wait(?:ing)?\s+(?:in\s+)?line|check(?:ing)?\s+(?:if|whether|on|the|stock)|hold (?:my|a) (?:spot|place)|laundry|dry ?clean\w*)\b`)

// ownPlaceRE: the one stop is the person's own place ("put them away in my
// kitchen at 12 Bedford Ave"): it is where things are brought.
// "bring it to my office", with no address, is still asked.
var ownPlaceRE = regexp.MustCompile(`(?i)\b(?:to|at|in|into) (?:my|our) (?:place|home|house|apartment|apt|kitchen|fridge|room|building|door|office)\s+(?:at|on)\s+\d`)

// needsDropOff: the first errand that picks up or buys something and has
// nowhere to bring it, -1 none.
func needsDropOff(p *PendingDraft) int {
	if p == nil || p.Check || oneStopPlaybooks[p.Playbook] {
		return -1
	}
	for i, it := range p.Items() {
		d := it.Draft
		if !dropOffErrand(d) || strings.TrimSpace(d.Location1) == "" || strings.TrimSpace(d.Location2) != "" {
			continue
		}
		// The words make the one stop the destination ("return it at UPS")
		// — unless something is bought there, which still comes back.
		if oneStopWordsRE.MatchString(d.Title) || ownPlaceRE.MatchString(p.Source) || (!it.NeedsBudget && oneStopWordsRE.MatchString(p.Source)) {
			continue
		}
		return i
	}
	return -1
}

// askDropOff puts the question for errand i, with the last drop-off
// offered when the person has one.
func (r *Router) askDropOff(ctx context.Context, in Inbound, id Identity, conv Conversation, i int) error {
	p := conv.Pending
	items := p.Items()
	p.DropOffAsked, p.DropOffIndex, p.DropOffSuggest = true, i, ""
	text := CopyAskDropOff
	if len(items) > 1 {
		text = fmt.Sprintf(CopyAskDropOffFor, items[i].Draft.Title)
	} else if last := r.lastAddress(ctx, id); last != "" && stopKey(last) != stopKey(items[i].Draft.Location1) {
		p.DropOffSuggest = last
		text = fmt.Sprintf(CopyAskDropOffLast, shortAddress(last))
	}
	conv.State = StateAwaitingConfirm
	lead := r.leadIn(p)
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
		return fmt.Errorf("askDropOff: %w", err)
	}
	if done, err := r.preempted(ctx, in, conv.IdentityID, p); done {
		return err
	}
	log.Printf("[channel] draft identity=%s: drop-off asked (suggested=%t)", id.ID, p.DropOffSuggest != "")
	return r.Reply(ctx, in, lead+text, "")
}

// shortAddress: the street part ("2 Bond St, New York, NY 10012" → "2
// Bond St").
func shortAddress(a string) string {
	s, _, _ := strings.Cut(a, ",")
	return strings.TrimSpace(s)
}

// dropOffQuestion is the question as put again (a hand-back, a YES with no
// drop-off): without the suggestion, which a YES would need armed.
func dropOffQuestion(p *PendingDraft) string {
	if i := needsDropOff(p); i >= 0 && len(p.More) > 0 {
		return fmt.Sprintf(CopyAskDropOffFor, p.Items()[i].Draft.Title)
	}
	return CopyAskDropOff
}

// answerDropOff reads the answer: YES takes the suggestion; an address
// (a building number, or a short answer with a number in it) fills the
// drop-off by rule and the request goes on from there; anything else is
// read as a correction of the request.
func (r *Router) answerDropOff(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	p := *conv.Pending
	i := p.DropOffIndex
	if !p.DropOffAsked {
		i = needsDropOff(&p)
	}
	items := p.Items()
	if i < 0 || i >= len(items) {
		i = 0
	}
	switch {
	case IsDraftDecline(text) || (IsNo(text) && p.DropOffSuggest == ""):
		return r.dropDraft(ctx, in, conv)
	case IsCancelRequest(text) || IsWithdrawal(text):
		return r.dropDraftWith(ctx, in, conv, CopyDroppedCancelHint)
	case IsNo(text):
		// "No" to "Same as last time?": asked without it.
		p.DropOffSuggest = ""
		conv.Pending = &p
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("answerDropOff: %w", err)
		}
		return r.Reply(ctx, in, CopyAskDropOff, "")
	}
	addr := ""
	switch {
	case IsYes(text) || IsBookingYes(text):
		if p.DropOffSuggest == "" {
			return r.Reply(ctx, in, CopyAskDropOffAgain, "")
		}
		addr = p.DropOffSuggest
	case dropOffAnswerLike(text):
		addr = strings.TrimSpace(text)
	default:
		// "actually make it two coffees": the request changes; the
		// question comes back after if still open.
		return r.startDraft(ctx, in, id, conv, p.Source+"\n"+text)
	}
	drafts := make([]llm.TaskDraft, len(items))
	for j, it := range items {
		drafts[j] = it.Draft
	}
	drafts[i].Location2 = addr
	p.DropOffAsked, p.DropOffSuggest = false, ""
	conv.Pending = &p
	log.Printf("[channel] draft identity=%s: drop-off given", id.ID)
	return r.startDraftOpts(ctx, in, id, conv, p.Source+"\nBring it to: "+addr, draftOpts{filled: drafts})
}

// dropOffAnswerLike: the words are a place to bring it — a street address,
// or a short answer with a number in it ("Bond street 200 nyc", "apt 4 at
// 12 bedford").
func dropOffAnswerLike(text string) bool {
	t := strings.TrimSpace(text)
	return looksLikeStreetAddress(t) || (hasDigitRE.MatchString(t) && len(strings.Fields(t)) <= 8 && !changeVerbRE.MatchString(t))
}

// changeVerbRE: a short message with a number that changes the request
// rather than naming a place ("make it 2", "at 4pm", "30 mins").
var changeVerbRE = regexp.MustCompile(`(?i)\b(?:make it|instead|change|\d+\s*(?:am|pm|mins?|minutes?|hours?|hrs?|bucks|dollars)|\$\d)\b|\$\s?\d`)
