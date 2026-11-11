package channel

// Supporter interest (2026-11-10). Some people text HO:RA's number because
// they want to WORK as a supporter, not book an errand: "are you hiring?",
// "how do I become a supporter?", "do you pay people to run errands?".
// They are sent to the application form; recruiting stays out of the text
// channel.
//
//   - Recognised by rule first (SupporterInterest), the classifier's
//     "supporter_interest" otherwise (Sonnet, like the routing fix). Never a
//     draft.
//   - The first time: CopySupporterApply (what supporters do, the link; no
//     earnings, rate or number of tasks). After that, any question about
//     being a supporter — pay, requirements, "did you get my application?" —
//     gets CopySupporterFollowUp and the link again. Never a hand-off.
//   - A number whose account is an approved supporter: CopySupporterAlready
//     for questions about their own work, CopySupporterShare (the link to
//     pass on) when they ask for the link or about a friend joining
//     (SupporterReferral; the classifier's supporter_referral), and
//     CopySupporterShare too when they ask again right after
//     CopySupporterAlready: the same fixed line never twice in a row
//     (production 2026-11-10 15:20–15:26).
//   - A mixed message ("book a laundry pickup, and are you hiring?"): the
//     errand as usual, then CopySupporterLine once.
//   - Ops get one non-urgent notice per number per New York day (email; the
//     last four digits only, no message text).
//
// Whether the link was already sent, and when, is read from the thread
// itself (ThreadReader): no new column.

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultSupporterApplyURL is the form when SUPPORTER_APPLY_URL is unset.
const DefaultSupporterApplyURL = "https://mvp.horaapp.co/become-supporter"

// HandoffSupporterInterest: someone asked about becoming a supporter. A
// notice, not a hand-off: the bot answered and keeps the thread.
const HandoffSupporterInterest HandoffReason = "supporter_interest"

const (
	// CopySupporterApply: the first answer (under 250 characters with the
	// link). No earnings, hourly rate or number of tasks, ever.
	CopySupporterApply = "Thanks for your interest in joining HO:RA! Supporters run errands around NYC on their own schedule. You can apply here: %s"
	// CopySupporterFollowUp: any later question about being a supporter.
	CopySupporterFollowUp = "All the details about being a supporter are in the application: %s"
	// CopySupporterLine: after a mixed message's errand reply.
	CopySupporterLine = "About becoming a supporter: you can apply here: %s"
	// CopySupporterAlready: the number is an approved supporter's.
	CopySupporterAlready = "You're already a HO:RA supporter! Please use the supporter app for tasks and your account. This number is for booking errands."
	// CopySupporterShare: an approved supporter asking for the link to pass
	// on, or asking again after CopySupporterAlready.
	CopySupporterShare = "Here's the link to share: %s"
)

// SupporterFollowUpWindow: how long after the link a vague question ("how
// much does it pay?") is read as about being a supporter.
const SupporterFollowUpWindow = 24 * time.Hour

// SupporterStatusLookup reads an account's supporter status: "approved",
// "applied", "rejected" or "none" (server/main.go deriveSupporterStatus).
type SupporterStatusLookup interface {
	SupporterStatus(ctx context.Context, userID string) (string, error)
}

func (r *Router) applyURL() string {
	if u := strings.TrimSpace(r.SupporterApplyURL); u != "" {
		return u
	}
	return DefaultSupporterApplyURL
}

var (
	// supporterInterestRE: wanting to work as a supporter, or asking about
	// being one. First person and HO:RA's side: "I need someone to work on
	// my furniture" is an errand, and so is "can I get errands done today?".
	supporterInterestRE = regexp.MustCompile(`(?i)\b(?:` +
		`(?:are|r)\s+(?:you|u|y'?all|you guys|hora|ho:ra)\s+(?:currently\s+|still\s+)?(?:hiring|recruiting|taking (?:on )?(?:new )?(?:people|applicants|supporters|runners)|looking for (?:people|workers|runners|supporters|help|staff|new supporters))` +
		`|(?:i|i'd|i would|i'm|im|i am|we'd|we'?re|we are)\s+(?:really\s+|also\s+|very\s+)?(?:want|wanna|would like|like|love|'d love|am looking|'m looking|looking|interested|hoping|trying|keen)\s+(?:to\s+|in\s+|for\s+)?(?:work(?:ing)?\s+(?:for|with|at)\s+(?:you|hora|ho:ra|your)|work(?:ing)? as a|a job|jobs\b|some work|work\s*$|(?:part[- ]time\s+|flexible\s+|extra\s+|side\s+)(?:work|job|jobs|income|gigs?|hustle)|apply(?:ing)?\b|join(?:ing)?\s+(?:you|hora|ho:ra|your|the team)|be(?:come|coming)?\s+(?:a |an |one of (?:your|the) )?(?:hora |ho:ra )?(?:supporter|runner|tasker|helper|errand runner|courier)s?|run(?:ning)?\s+errands\s+for\s+(?:you|hora|ho:ra|money|cash|pay|people)|do(?:ing)?\s+(?:tasks|errands|gigs)\s+for\s+(?:you|hora|ho:ra|money|cash|pay)|make (?:some )?(?:extra )?money|earn (?:some )?(?:extra )?money)` +
		`|^\W*(?:hi|hey|hello)?\W*(?:looking for|seeking|searching for|need|in need of)\s+(?:a\s+|some\s+)?(?:side\s+|part[- ]time\s+|flexible\s+)?(?:gigs?|hustles?|jobs?|work|employment|extra (?:income|money|cash))\b` +
		`|looking for\s+(?:a\s+|some\s+)?(?:side|part[- ]time|flexible|extra)\s+(?:work|jobs?|gigs?|income|hustles?)` +
		`|(?:student|between jobs|unemployed|retired)\b[^.?!]{0,30}\blooking for\s+(?:a\s+|some\s+)?(?:work|jobs?|gigs?|income)` +
		`|how\s+(?:do|can|would|could)\s+(?:i|one|someone|people|we)\s+(?:become|be|join|sign up(?:\s+(?:to be|as))?|apply|get hired|get a job|start (?:working|as)|work (?:for|with) (?:you|hora|ho:ra))\b` +
		`|(?:become|becoming|be|sign up as|signing up as|apply(?:ing)? (?:to be|as))\s+(?:a |an |one of (?:your|the) )(?:hora |ho:ra )?(?:supporter|runner|tasker|errand runner|courier)` +
		`|can\s+(?:i|we)\s+(?:do|run|take|get|pick up)\s+(?:some\s+)?(?:tasks|errands|jobs|gigs|work)\s+(?:for\s+(?:money|cash|pay|you|hora|ho:ra)|with (?:you|hora|ho:ra))` +
		`|can\s+(?:i|we)\s+(?:work|apply|join)\b` +
		`|do\s+(?:you|u|y'?all|you guys)\s+(?:need|want|take on|hire|accept)\s+(?:more\s+|any\s+|new\s+|extra\s+)?(?:runners|supporters|workers|people|helpers|couriers|staff|applicants)\b` +
		`|do\s+(?:you|u|y'?all|you guys)\s+pay\s+(?:people|supporters|runners|workers|your|folks|students)\b` +
		`|do\s+(?:you|u|y'?all|you guys)\s+have\s+(?:any\s+)?(?:jobs|job openings|openings|positions|vacancies|gigs|work for)\b` +
		`|(?:job|work|employment)\s+(?:openings?|opportunit\w+|positions?|application)|apply for (?:a |the )?(?:job|position|role)|application (?:form|link)|(?:supporter|job)\s+application|my application|i (?:applied|submitted)|already applied` +
		`|(?:make|earn)\s+(?:some\s+)?(?:extra\s+)?(?:money|cash|income)\s+(?:with|on|through|from|for|by working for)\s+(?:you|hora|ho:ra|this|the app)` +
		`|how much\s+(?:do|does|can|would)\s+(?:you|hora|ho:ra)\s+pay\b|how much\s+(?:do|does|can|would)\s+(?:supporters|runners|people|workers|helpers|i|one|someone)\s+(?:earn|make)\b|what do supporters (?:make|earn|get paid)` +
		`|work for (?:you|hora|ho:ra)|join (?:your|the|hora'?s|ho:ra'?s) (?:team|crew|network)|\bhiring\b` +
		`)`)
	// notSupporterRE: the customer side of the same words.
	notSupporterRE = regexp.MustCompile(`(?i)\b(?:i'?m hiring|we'?re hiring|hiring (?:someone|somebody|a|an)\s+(?:mover|cleaner|handyman|person|supporter|plumber)|(?:someone|somebody|a supporter|a person|you)\s+to\s+work\s+on|work (?:done|on my|on the)|(?:tasks|errands) (?:done|for me)|get (?:my |some |a few )?(?:errands|tasks) (?:done|run)|apply (?:for|to) (?:my|a|the) (?:passport|visa|license|permit|loan|lease|apartment)|(?:drop(?:ping)? off|deliver|print|mail|bring|take|fax|submit) (?:my|the|a|this) (?:job |rental |visa |passport |school |college )?application)\b`)
	// referralLinkRE: the application link itself ("send me the apply
	// link", "is there a referral link?").
	referralLinkRE = regexp.MustCompile(`(?i)\b(?:(?:apply|application|applying|sign[- ]?up|supporter|referral|recruiting|join(?:ing)?)\s+(?:form\s+|page\s+)?link|link\s+(?:to|for)\s+(?:apply|the application|applying|become|becoming|join|sign(?:ing)? up)|referral\s+(?:program\w*|bonus\w*|code)|refer(?:ring)?\s+(?:a\s+|my\s+|some\s+)?(?:friends?|someone|somebody|people|others|folks)\b|invit(?:e|ing)\s+(?:a\s+|my\s+|some\s+)?(?:friends?|people|others|folks)\s+to\s+(?:join|apply|work))`)
	// referralWhoRE: someone else who would apply.
	referralWhoRE = regexp.MustCompile(`(?i)\b(?:(?:my|a|our)\s+(?:friends?|buddy|buddies|cousins?|brothers?|sisters?|siblings?|roommates?|partner|wife|husband|girlfriend|boyfriend|son|daughter|kids?|mom|dad|neighbou?rs?|co-?workers?|colleagues?|classmates?|family(?: members?)?)|friends|(?:someone|somebody|people|others|folks)\s+(?:i know|else|who'?d|who would|who want)|others)\b`)
	// referralAskRE: what that someone would do — join, apply, become one —
	// or the link to pass on.
	referralAskRE = regexp.MustCompile(`(?i)\b(?:join(?:\s+(?:you|hora|ho:ra|the team|as|too|in)\b|\s*[?.!]*$)|apply(?:ing)?\b|sign(?:ing)?\s*up\s+(?:as|to be|to work)\b|be(?:come|coming)?\s+(?:a |an |one of (?:your|the) )?(?:hora |ho:ra )?(?:supporter|runner|tasker)s?\b|become one|work(?:ing)?\s+(?:for|with)\s+(?:you|hora|ho:ra)|get hired)`)
	// referralShareRE: passing a link on to someone else ("share the link
	// with my friends"). Which link the words do not say ("send the
	// restaurant link to my friends" is a search's): read so only for an
	// approved supporter or right after the application link went out
	// (shareLinkReferral).
	// A link named as something else ("the restaurant link") is not ours.
	referralShareRE = regexp.MustCompile(`(?i)\b(?:share|send|forward|pass)\b[^.?!]*\b(?:the|a|that|this|your|ur)\s+link\b|\b(?:the|a|that|this|your|ur)\s+link\b[^.?!]*\b(?:share|send|forward|pass)\b`)
	// supporterFollowUpRE: a question about being a supporter, read so only
	// shortly after the link went out.
	supporterFollowUpRE = regexp.MustCompile(`(?i)\b(?:pay|paid|pays|payment|earn\w*|make|money|rate|hourly|per hour|tips?|wages?|salary|requirements?|require\w*|qualif\w*|background|checkr|id|license|licence|insurance|car|bike|vehicle|schedule|hours|shifts?|flexible|full[- ]time|part[- ]time|start|how (?:does|do) (?:it|this|that) work|how it works|apply|applied|application|form|link|hear back|status|approved|approval|interview|age|old enough|visa|work permit|ssn|tax(?:es)?|1099|w-?2|contractor|employee|training|onboarding|payouts?|stripe|areas?|boroughs?|neighbou?rhoods?|requirements|experience|students?)\b`)
	// supporterClauseRE: the interest part of a mixed message, cut before
	// the errand is read (connectors around it go too).
	supporterClauseRE = regexp.MustCompile(`(?i)(?:[,;.!?]?\s*(?:and|also|plus|btw|by the way|oh and|and also)\s*,?\s*)*(?:(?:i'?d|i would|i)\s+(?:also\s+)?(?:like|love|want|wanna)\s+to\s+(?:apply|join|work for (?:you|hora|ho:ra)|become a (?:hora )?(?:supporter|runner))[^.?!]*|(?:also\s+)?(?:become|becoming)\s+a\s+(?:hora )?(?:supporter|runner)[^.?!]*|(?:are|r)\s+(?:you|u|y'?all)\s+(?:also\s+)?(?:hiring|looking for (?:people|supporters|runners))[^.?!]*|how\s+(?:do|can)\s+i\s+(?:become|apply|join)[^.?!]*|do you (?:need|hire) (?:more )?(?:runners|supporters|people)[^.?!]*)[.?!]*`)
	// errandAskRE: what in a mixed message's remainder is an errand.
	errandAskRE = regexp.MustCompile(`(?i)\b(?:book|booking|need (?:someone|somebody|a supporter|help)|can (?:someone|somebody|you|a supporter)|pick(?:ing)? up|drop(?:ping)? off|deliver|bring|buy|grab|wait in line|errands? for me|task for me|laundry|groceries|dry clean)`)
	// offeringRE: the person describing themselves ("I can run errands and
	// drive") — what they offer as a supporter, not an errand.
	offeringRE = regexp.MustCompile(`(?i)^\W*(?:i\s+(?:can|could|am|have|do|run|drive|own|know|speak|love|enjoy)|i'm|im|i've|i'd be|my experience)\b`)
	// customerAskRE: what makes such words an ask after all.
	customerAskRE = regexp.MustCompile(`(?i)\b(?:book|booking|need (?:someone|somebody|a supporter|you)|want (?:someone|somebody|you|a supporter)|can (?:someone|somebody|you|a supporter))\b`)
)

// SupporterInterest: the words ask about working as a supporter (rule),
// for themselves or for someone else (SupporterReferral).
func SupporterInterest(text string) bool {
	t := strings.ReplaceAll(text, "’", "'")
	return (supporterInterestRE.MatchString(t) || referral(t)) && !notSupporterRE.MatchString(t)
}

// SupporterReferral: the words ask for the application link to pass on,
// or about someone else joining ("send me the apply link", "can my friend
// join?", "share the link with my friends") — rule.
func SupporterReferral(text string) bool {
	t := strings.ReplaceAll(text, "’", "'")
	return referral(t) && !notSupporterRE.MatchString(t)
}

func referral(t string) bool {
	if referralLinkRE.MatchString(t) {
		return true
	}
	if !referralWhoRE.MatchString(t) {
		return false
	}
	return referralAskRE.MatchString(t)
}

// shareLinkWords: "share the link with my friends" — a link to pass on,
// which link unsaid.
func shareLinkWords(text string) bool {
	t := strings.ReplaceAll(text, "’", "'")
	return referralShareRE.MatchString(t) && referralWhoRE.MatchString(t) && !notSupporterRE.MatchString(t)
}

// shareLinkReferral: such words are about the application link — the
// number is an approved supporter's, or HO:RA's last line was the link or
// the "already a supporter" line.
func (r *Router) shareLinkReferral(ctx context.Context, id Identity, text string) bool {
	if !shareLinkWords(text) || IsClearErrand(text) {
		return false
	}
	if last := r.lastReply(ctx, id); last == CopySupporterAlready || strings.Contains(last, r.applyURL()) {
		return true
	}
	return r.approved(ctx, id)
}

// approved: the number's account is an approved supporter.
func (r *Router) approved(ctx context.Context, id Identity) bool {
	if !id.Linked() || r.Supporters == nil {
		return false
	}
	st, err := r.Supporters.SupporterStatus(ctx, id.UserID)
	if err != nil {
		log.Printf("[channel] supporter interest identity=%s: status: %v", id.ID, err)
	}
	return st == "approved"
}

// SplitSupporterInterest: a mixed message's errand part, with the interest
// clause cut, and whether there is one. "" and false when the message is
// interest only.
func SplitSupporterInterest(text string) (rest string, mixed bool) {
	t := strings.ReplaceAll(text, "’", "'")
	rest = strings.TrimSpace(supporterClauseRE.ReplaceAllString(t, " "))
	rest = strings.Trim(strings.Join(strings.Fields(rest), " "), " ,;-")
	if rest == "" || !errandAskRE.MatchString(rest) || SupporterInterest(rest) ||
		(offeringRE.MatchString(rest) && !customerAskRE.MatchString(rest)) {
		return "", false
	}
	return rest, true
}

// supporterTail: a mixed message's application line, sent once after the
// errand's reply (routeMessage).
type supporterTail struct {
	mu   sync.Mutex
	want bool
}

type supporterTailKey struct{}

func withSupporterTail(ctx context.Context) (context.Context, *supporterTail) {
	t := &supporterTail{}
	return context.WithValue(ctx, supporterTailKey{}, t), t
}

// wantSupporterLine asks for the application line after this message's
// reply (the classifier's also_supporter_interest).
func wantSupporterLine(ctx context.Context) {
	if t, ok := ctx.Value(supporterTailKey{}).(*supporterTail); ok {
		t.mu.Lock()
		t.want = true
		t.mu.Unlock()
	}
}

// lastApplyLink: when the link last went to this number, nil when never
// (within the recent thread).
func (r *Router) lastApplyLink(ctx context.Context, id Identity) *time.Time {
	if r.Thread == nil {
		return nil
	}
	msgs, err := r.Thread.RecentMessages(ctx, id.Channel, id.Address, 40)
	if err != nil {
		log.Printf("[channel] supporter interest identity=%s: thread: %v", id.ID, err)
		return nil
	}
	url := r.applyURL()
	for i := len(msgs) - 1; i >= 0; i-- {
		if m := msgs[i]; m.Direction == "outbound" && !m.Relay && strings.Contains(m.Text, url) {
			at := m.At
			return &at
		}
	}
	return nil
}

// lastReply: HO:RA's own last message to this number ("" none).
func (r *Router) lastReply(ctx context.Context, id Identity) string {
	if r.Thread == nil {
		return ""
	}
	msgs, err := r.Thread.RecentMessages(ctx, id.Channel, id.Address, 12)
	if err != nil {
		log.Printf("[channel] supporter interest identity=%s: thread: %v", id.ID, err)
		return ""
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if m := msgs[i]; m.Direction == "outbound" && !m.Relay {
			return m.Text
		}
	}
	return ""
}

// supporterFollowUp: a vague question about being a supporter, shortly
// after the link ("how much does it pay?", "do I need a car?").
func (r *Router) supporterFollowUp(ctx context.Context, id Identity, text string) bool {
	if !supporterFollowUpRE.MatchString(text) || IsClearErrand(text) || LooksLikeDiscovery(text) || errandAskRE.MatchString(text) {
		return false
	}
	at := r.lastApplyLink(ctx, id)
	return at != nil && r.now().Sub(*at) < SupporterFollowUpWindow
}

// routeSupporterRules: the rule fast path, ahead of every other reading
// (routeMessage). handled: answered. rest: a mixed message's errand words
// for the usual routing (the line follows its reply).
func (r *Router) routeSupporterRules(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) (rest string, handled bool, err error) {
	if !SupporterInterest(text) {
		if r.shareLinkReferral(ctx, id, text) {
			return "", true, r.answerSupporter(ctx, in, id, "rule", true)
		}
		if r.supporterFollowUp(ctx, id, text) {
			return "", true, r.answerSupporter(ctx, in, id, "follow_up", false)
		}
		return text, false, nil
	}
	// "are you hiring escorts?" is the scope rules' (scope.go), not ours.
	if scopeRules().RefusedByRule(text) != nil {
		return text, false, nil
	}
	if rest, mixed := SplitSupporterInterest(text); mixed {
		wantSupporterLine(ctx)
		log.Printf("[channel] supporter interest identity=%s: mixed, the errand goes on", id.ID)
		return rest, false, nil
	}
	return "", true, r.answerSupporter(ctx, in, id, "rule", SupporterReferral(text))
}

// answerSupporter replies to supporter interest: the approved supporter's
// line (or the link to share: referral, or asked again), the first answer,
// or the follow-up line; then ops, once a day.
func (r *Router) answerSupporter(ctx context.Context, in Inbound, id Identity, by string, referral bool) error {
	if r.approved(ctx, id) {
		// Asking for the link to pass on, or asking again right after the
		// "already" line: the link (never the same line twice in a row).
		if referral || r.lastReply(ctx, id) == CopySupporterAlready {
			log.Printf("[channel] supporter interest identity=%s by=%s referral=%t: already a supporter, the link to share", id.ID, by, referral)
			return r.Reply(ctx, in, fmt.Sprintf(CopySupporterShare, r.applyURL()), "")
		}
		log.Printf("[channel] supporter interest identity=%s by=%s: already a supporter", id.ID, by)
		return r.Reply(ctx, in, CopySupporterAlready, "")
	}
	last := r.lastApplyLink(ctx, id)
	copy, kind := fmt.Sprintf(CopySupporterApply, r.applyURL()), "first"
	if last != nil {
		copy, kind = fmt.Sprintf(CopySupporterFollowUp, r.applyURL()), "follow_up"
	}
	log.Printf("[channel] supporter interest identity=%s by=%s reply=%s", id.ID, by, kind)
	if err := r.Reply(ctx, in, copy, ""); err != nil {
		return err
	}
	r.supporterNotice(ctx, id, last)
	return nil
}

// sendSupporterLine: a mixed message's line, after its errand reply.
func (r *Router) sendSupporterLine(ctx context.Context, in Inbound, id Identity) {
	last := r.lastApplyLink(ctx, id)
	if err := r.Reply(ctx, in, fmt.Sprintf(CopySupporterLine, r.applyURL()), ""); err != nil {
		log.Printf("[channel] supporter line identity=%s: %v", id.ID, err)
		return
	}
	r.supporterNotice(ctx, id, last)
}

// supporterNotice tells ops — once per number per New York day: last is
// when the link went out before this reply; a link today means ops heard.
func (r *Router) supporterNotice(ctx context.Context, id Identity, last *time.Time) {
	if last != nil {
		y1, m1, d1 := last.In(r.loc()).Date()
		y2, m2, d2 := r.now().In(r.loc()).Date()
		if y1 == y2 && m1 == m2 && d1 == d2 {
			return
		}
	}
	if r.Ops == nil {
		return
	}
	// No thread: the words stay in the chat; the notice says what happened.
	if err := r.notifyOps(context.WithoutCancel(ctx), id, HandoffSupporterInterest, false, handoffOpen{thread: []ThreadMessage{}}); err != nil {
		log.Printf("[channel] supporter interest identity=%s: ops notice: %v", id.ID, err)
	}
}
