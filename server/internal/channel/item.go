package channel

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Item questions by text (Aura 2026-11-07; the app's item.go). A supporter
// found an item out of stock and an alternative inside the budget; the
// requester is asked one question, through the same one-at-a-time queue as
// the YES/NO approvals:
//
//	"<store> is out of <item>. Your supporter found <alternative> ($<price>).
//	 Reply 1 for <alternative>, 2 to skip it."
//
// Answered by a digit and nothing else (rule-matched, never the model):
// 1 = the alternative (the request is approved), 2 = skip it (declined).
// Digits because they are never opt-out words (STOP, CANCEL are handled
// before any of this, optout.go), and because "yes" to "1 or 2?" says
// nothing. A YES or NO is asked again in digits. Nothing here moves money:
// an item question has no amount to approve (the alternative fits the
// budget, or it would have been a budget ask).

// itemChoiceRE: "1", "2", "1.", "2)", the keycap emoji, with spaces.
var itemChoiceRE = regexp.MustCompile(`^\s*([12])\s*(?:\x{FE0F}?\x{20E3})?\s*[.)!]?\s*$`)

// ItemChoice is 1 or 2 for a message that is exactly that answer, else 0.
func ItemChoice(text string) int {
	m := itemChoiceRE.FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	if m[1] == "1" {
		return 1
	}
	return 2
}

// DecideItemChoice is DecideApproval for an item question: the same delivery
// rules (unconfirmed, sent before the question, a swipe on another message),
// then the digit.
func DecideItemChoice(f ApprovalFacts) ApprovalVerdict {
	text := stripFiller(f.Text)
	choice := ItemChoice(f.Text)
	plainYes, _ := approvalYes(text)
	answerish := choice != 0 || approvalAnswerish(text)
	switch {
	case answerish && f.Unconfirmed:
		return VerdictUnconfirmed
	case answerish && f.SentBefore:
		return VerdictSentBefore
	case choice != 0 && f.SwipedElsewhere:
		return VerdictReask
	case choice == 1:
		return VerdictApprove
	case choice == 2:
		return VerdictDecline
	case plainYes || approvalNo(text):
		// A yes or no to "1 or 2?": asked again, in digits.
		return VerdictReask
	}
	return VerdictNotAnAnswer
}

// inlineText makes a supporter's free text safe inside our sentence: one
// line, printable, no quotation marks, capped.
func inlineText(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	var b strings.Builder
	for _, c := range s {
		if unicode.IsPrint(c) && c != '“' && c != '”' && c != '"' {
			b.WriteRune(c)
		}
	}
	s = strings.TrimSpace(b.String())
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max]) + "…"
	}
	return s
}

// itemQuestionText composes the item question from the structured request.
// Without an item and an alternative nothing is sent (the app still has it).
func itemQuestionText(a *PendingApproval, _ string, minutes int) (string, bool) {
	item, alt, store := inlineText(a.Item, 80), inlineText(a.Alternative, 80), inlineText(a.Store, 60)
	if item == "" || alt == "" || a.Cents < 0 {
		return "", false
	}
	var b strings.Builder
	if store != "" {
		fmt.Fprintf(&b, "%s is out of %s.", store, item)
	} else {
		fmt.Fprintf(&b, "%s is out of stock.", capFirst(item))
	}
	fmt.Fprintf(&b, " Your supporter found %s (%s).\n\n", alt, Dollars(a.Cents))
	fmt.Fprintf(&b, CopyItemAsk, alt)
	if minutes > 0 {
		b.WriteString(" " + fmt.Sprintf(CopyItemAskTimed, minutes))
	}
	return b.String(), true
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// itemReask puts the item question again, in digits.
func itemReask(conv Conversation, item PendingApproval) string {
	if conv.Unconfirmed() {
		return CopyApprovalOnItsWay
	}
	alt := inlineText(item.Alternative, 80)
	if alt == "" {
		alt = "the alternative"
	}
	return fmt.Sprintf(CopyItemReask, alt)
}

// armedItem is the armed question when it is an item question.
func (r *Router) armedItem(ctx context.Context, id Identity, conv Conversation) (PendingApproval, bool) {
	if r.Booker == nil || conv.PendingExtensionID == "" {
		return PendingApproval{}, false
	}
	q, ok, err := r.Booker.ItemQuestion(ctx, id.UserID, conv.PendingExtensionID)
	if err != nil {
		// Unknown kind: treated as a YES/NO question, which can only re-ask
		// or decline — and a decline of an item question is "skip it",
		// which spends nothing.
		log.Printf("[channel] approval identity=%s ext=%s: item lookup: %v", id.ID, conv.PendingExtensionID, err)
		return PendingApproval{}, false
	}
	return q, ok
}
