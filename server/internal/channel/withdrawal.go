package channel

// Withdrawal guard (2026-11-03). With a new booking's question open — the
// summary, the budget question, the first-name question, the setup link —
// a message that only takes the request back drops the draft, by rule,
// before any model is asked.
//
// Production 2026-11-03 21:58 (Rome): with the summary open, "Oh sorry I
// don't need to task anymore thanks" was labelled "decline" by the agent,
// re-drafted anyway (changeWordsRE matched the word "to"), titled "Pick up
// cake and deliver to office (CANCELLED)" by the parser and put back with
// "Reply YES to book". A YES would have booked and charged it. On Nov 2
// "never mind I don't need that anymore" also got the summary again.
//
// Dropping an unbooked draft moves no money and the person can text again,
// so the match is generous: fillers and politeness are ignored, typos like
// "need to task" are read as meant. What it never swallows is a change: a
// message with anything left over once the withdrawal words are taken out
// ("never mind the milk, just eggs", "no, make it 4pm") is not one.

import (
	"regexp"
	"strings"

	"hora-auth/internal/channel/llm"
)

var (
	// withdrawFillerRE: words that never change what a withdrawal means.
	withdrawFiller = `(?:oh+|ah+|sorry|so sorry|i'?m sorry|apologies|my bad|ok(?:ay)?|k|actually|hmm+|um+|uh+|so|well|wait|please|pls|plz|` +
		`thanks|thank you|thank u|thx|ty|cheers|no|nah|nope|yeah|yes|hey|hi|lol|haha|just|really|i think|i guess|guess|` +
		`but|and|then|anyway|anyways|after all|for now|right now|now|today|anymore|any more|either|at all|all|in the end|` +
		`i'?m good|all good|it'?s fine|that'?s fine|it'?s ok(?:ay)?|that'?s ok(?:ay)?)`
	// withdrawObject: what is being taken back — the draft, however named.
	withdrawObject = `(?:(?:the|this|that|my|your|a)\s+)?(?:it|that|this|them|one|this one|that one|thing|task|errand|booking|order|request|job|help|service|pickup|delivery|ride|supporter|someone|anyone|anything|you)`
	// withdrawCore: the withdrawal itself.
	withdrawCore = `(?:` +
		`never ?mind|nvm|forget (?:about )?(?:it|that|this|the whole thing|everything|` + withdrawObject + `)|forget it|` +
		`(?:i|we)?\s*(?:do not|don'?t|dont|didn'?t|no longer|not|won'?t|wont)\s+(?:really\s+|even\s+|actually\s+)?(?:need|want|require)(?:ed)?(?:\s+to)?(?:\s+` + withdrawObject + `)?|` +
		`no longer (?:need(?:ed)?|necessary|required)|not (?:needed|necessary|required)|not anymore|not any more|no need|` +
		`(?:i'?ve |i |we )?change(?:d)? (?:my|our) minds?|changed? mind|` +
		`cancel(?:l?ing)?(?:\s+` + withdrawObject + `)?|scratch (?:that|it)|skip (?:it|that)|drop (?:it|that)|` +
		`(?:let'?s )?(?:not|leave it|leave that)|don'?t book(?:\s+` + withdrawObject + `)?|do not book(?:\s+` + withdrawObject + `)?|` +
		`(?:i'?ll|we'?ll|i will) (?:do it|handle it|manage|sort it) myself` +
		`)`
	withdrawTokenRE = regexp.MustCompile(`^(?:(?:` + withdrawCore + `|` + withdrawFiller + `)\s+)+$`)
	withdrawCoreRE  = regexp.MustCompile(`(?:^|\s)` + withdrawCore + `\s`)
	// withdrawHanRE: the Chinese forms, with or without particles and thanks.
	// 不要 / 不用 alone (also 不要啦) are a plain no — the D-17 rules read
	// those (I-S: maybe for the supporter); with 了 they take it back.
	withdrawHanRE     = regexp.MustCompile(`^(?:(?:算了|不用了|不要了|不需要了|先不用了?|先不要了?|沒事了?|没事了?|不必了|取消(?:任務|任务|吧)?|不訂了|不订了|謝謝|谢谢|抱歉|不好意思|對不起|对不起|喔|哦|噢|啊|呀|啦|吧|嗯|好)\s*)+$`)
	withdrawHanCoreRE = regexp.MustCompile(`算了|不用了|不要了|不需要了|先不用|先不要|沒事|没事|不必了|取消|不訂了|不订了`)
	withdrawPunctRE   = regexp.MustCompile(`[^\p{L}\p{N}'\s]+`)
)

// IsWithdrawal reports a message that only takes a not-yet-booked request
// back: "never mind", "oh sorry I don't need it anymore thanks", "I changed
// my mind", "cancel task", "算了". By rule; the caller decides it applies
// (a new booking's question is open).
func IsWithdrawal(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	t = strings.NewReplacer("’", "'", "‘", "'", "`", "'").Replace(t)
	if t == "" {
		return false
	}
	han := withdrawPunctRE.ReplaceAllString(t, " ")
	if withdrawHanCoreRE.MatchString(han) && withdrawHanRE.MatchString(strings.Join(strings.Fields(han), " ")) {
		return true
	}
	words := strings.Join(strings.Fields(withdrawPunctRE.ReplaceAllString(t, " ")), " ") + " "
	if words == " " {
		return false
	}
	return withdrawCoreRE.MatchString(" "+words) && withdrawTokenRE.MatchString(words)
}

// newBookingQuestion: the states where a new, unbooked request is waiting
// on the person — a withdrawal there drops it.
func newBookingQuestion(s State) bool {
	switch s {
	case StateAwaitingConfirm, StateAwaitingBudget, StateAwaitingName, StateAwaitingSetup:
		return true
	}
	return false
}

// statusTitleRE: words that describe a task's state, not an errand. A
// re-draft titled "Pick up cake (CANCELLED)" is the parser reading a
// withdrawal as a correction.
var statusTitleRE = regexp.MustCompile(`(?i)\b(?:cancel(?:l?ed)?|booked|done|completed|void(?:ed)?)\b`)

// statusWordAdded reports a status word in a re-drafted title that the
// draft it replaces did not already have ("Get nails done" stays "done").
func statusWordAdded(prev []DraftItem, items []DraftItem) bool {
	had := map[string]bool{}
	for _, it := range prev {
		for _, w := range statusTitleRE.FindAllString(it.Draft.Title, -1) {
			had[strings.ToLower(w)] = true
		}
	}
	for _, it := range items {
		for _, w := range statusTitleRE.FindAllString(it.Draft.Title, -1) {
			if !had[strings.ToLower(w)] {
				return true
			}
		}
	}
	return false
}

// intentDecline: the agent's "decline" while a new booking's question is
// open (routeInContextLabel) — the caller drops the draft.
const intentDecline llm.Intent = "decline_booking"

// declineCarriesChange: a no that still changes the booking ("no, make it
// 4pm", "forget it, do laundry instead") — re-drafted, never dropped.
// Narrow on purpose: the prepositions and verbs every errand sentence has
// ("to", "for", "pick") are not a change.
var declineChangeRE = regexp.MustCompile(`(?i)\d|\b(?:make it|instead|change|rather|tomorrow|tonight|later|morning|afternoon|evening|noon|pm|am|o'?clock|address|street|ave|budget|minutes?|mins?|hours?|different|wrong)\b|[改換换點点]`)

func declineCarriesChange(text string) bool { return declineChangeRE.MatchString(text) }

// isDraftWithdrawal: a new booking waits and the message only takes it back.
// At the budget question, "not needed" / "no thanks" answer it (nothing to
// buy) and are not a withdrawal.
func (r *Router) isDraftWithdrawal(conv Conversation, text string) bool {
	if conv.Pending == nil || !newBookingQuestion(conv.State) || !IsWithdrawal(text) {
		return false
	}
	if conv.State == StateAwaitingBudget && (IsNoBudget(text) || budgetNoThanksRE.MatchString(text)) {
		return false
	}
	return true
}
