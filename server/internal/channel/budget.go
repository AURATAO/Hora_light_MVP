package channel

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	"hora-auth/internal/channel/llm"
)

// Money the supporter spends on the person's behalf (M3.5, incident
// 2026-10-21: "bring me a bagel" booked with no budget, so the supporter had
// no way to claim it). The rule: the requester agrees to a ceiling BEFORE the
// supporter pays anything; the budget is that consent, and it is covered by
// the hold. So when an errand involves buying something and no amount was
// given, the chat asks — one question, answered with an amount — before the
// summary. The amount is parsed by rule; the model only helps decide whether
// a purchase is involved, never how much.
//
// Auto-extend is the other consent the app collects with a checkbox: up to
// AutoExtendMinutes past the estimate without asking. The summary states it
// and the one YES covers it; "no extra time" (rule-matched) turns it off.

// PurchaseChecker says whether an errand involves the supporter paying for
// something. The rule runs first; the model is the tiebreak.
type PurchaseChecker interface {
	NeedsPurchase(ctx context.Context, text string) (bool, error)
}

var (
	purchaseRE = regexp.MustCompile(`(?i)\b(buy|purchase|order|pay for|get me|grab me|bring me|pick me up|fetch me|shop(ping)? for|groceries|grocery|prescription|pharmacy|flowers|coffee|lunch|dinner|breakfast|bagel|sandwich|pizza|takeout|take-out|snacks|drinks|bottle of|a pack of)\b|幫我買|買`)
	// Amount in the person's own words: "$20", "20 dollars", "budget of 25",
	// "up to $30", "around 15 bucks". Whole dollars; cents are rare in chat.
	amountRE   = regexp.MustCompile(`(?i)(?:\$\s?(\d{1,4})(?:\.\d{1,2})?)|(?:\b(\d{1,4})\s?(?:dollars|bucks|usd)\b)|(?:\b(?:budget|up to|max|maximum|limit)(?: of| is|:)?\s?\$?\s?(\d{1,4})\b)`)
	noBudgetRE = regexp.MustCompile(`(?i)^\s*(no|none|nothing|0|\$0|zero|nope)[\s!.]*$|\b(no budget|nothing to buy|don'?t need to buy|not buying|already paid|prepaid|it'?s paid)\b`)
	noAutoRE   = regexp.MustCompile(`(?i)\b(no extra time|no overtime|don'?t go over|do not go over|ask (me )?first|ask before|no auto[- ]?extend|stick to the estimate|not a minute more)\b|不要超時|超時先問`)
)

// LooksLikePurchase is the rule half: the errand involves buying something.
func LooksLikePurchase(text string) bool { return purchaseRE.MatchString(text) }

// AmountCents reads a dollar amount from text; ok=false when there is none.
// "up to 15 mins" is a time, not $15 (review 2026-10-30): a bare number
// after budget/up to/max that is followed by a time unit is skipped.
func AmountCents(text string) (int, bool) {
	for _, m := range amountRE.FindAllStringSubmatchIndex(text, -1) {
		for g := 1; g <= 3; g++ {
			if m[2*g] < 0 {
				continue
			}
			if g == 3 && timeUnitAfterRE.MatchString(text[m[1]:]) {
				break // a duration; look at the next match
			}
			n, err := strconv.Atoi(text[m[2*g]:m[2*g+1]])
			if err != nil || n <= 0 {
				return 0, false
			}
			return n * 100, true
		}
	}
	return 0, false
}

var timeUnitAfterRE = regexp.MustCompile(`(?i)^\s*(?:h|hr|hrs|hours?|m|mins?|minutes?)\b|^\s*(?:分|小時|個小時|鐘頭)`)

// LatestAmountCents: the newest line that names an amount wins. The draft
// source is every line said, oldest first (review 2026-10-30: an old "$20"
// must not outlive the correction after it).
func LatestAmountCents(source string) (int, bool) {
	lines := strings.Split(source, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if c, ok := AmountCents(lines[i]); ok {
			return c, true
		}
	}
	return 0, false
}

// IsNoBudget reports "nothing to buy" as an answer to the budget question.
func IsNoBudget(text string) bool { return noBudgetRE.MatchString(text) }

// IsNoAutoExtend reports an explicit opt-out of the auto-extension.
func IsNoAutoExtend(text string) bool {
	return noAutoRE.MatchString(text) || noExtraRE.MatchString(text)
}

// noExtraRE: "no extra" as the whole message, the summary's own words cut
// short (staging 2026-10-30). Whole-message only: "no extra stops" is not it.
var noExtraRE = regexp.MustCompile(`(?i)^\s*(?:no,?\s*)?(no extra|no extras|no extension|no overtime)(?:\s*(?:please|pls|thanks|thank you))?[\s.!。！]*$`)

// needsPurchase decides for one errand: an amount already given settles it;
// the rule says yes; the model is asked only when the rule says no.
func (r *Router) needsPurchase(ctx context.Context, text string, d llm.TaskDraft) bool {
	if d.Category == "grocery" || LooksLikePurchase(text) {
		return true
	}
	if r.Purchases == nil {
		return false
	}
	yes, err := r.Purchases.NeedsPurchase(ctx, text)
	if err != nil {
		// Unknown → ask (review 10/24): the budget question takes
		// "nothing", while a missed purchase leaves the supporter paying
		// out of pocket with no budget agreed.
		log.Printf("[channel] purchase check fell back to asking: %v", err)
		return true
	}
	return yes
}

// nextBudgetAsk returns the index of the first item that involves a
// purchase and has no budget decided yet, or -1.
func nextBudgetAsk(p *PendingDraft) int {
	for i, it := range p.Items() {
		if it.NeedsBudget && !it.BudgetAsked {
			return i
		}
	}
	return -1
}

// askBudget moves to the budget question for item i.
func (r *Router) askBudget(ctx context.Context, in Inbound, conv Conversation, i int) error {
	items := conv.Pending.Items()
	conv.Pending.BudgetAskIndex = i
	conv.State = StateAwaitingBudget
	lead := r.leadIn(conv.Pending)
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
		return fmt.Errorf("askBudget: %w", err)
	}
	if done, err := r.preempted(ctx, in, conv.IdentityID, conv.Pending); done {
		return err
	}
	if len(items) > 1 {
		return r.Reply(ctx, in, lead+fmt.Sprintf(CopyAskBudgetFor, items[i].Draft.Title), "")
	}
	return r.Reply(ctx, in, lead+CopyAskBudget, "")
}

// onAwaitingBudget reads the amount (or "nothing") for the item being asked
// about, then moves on: the next item that needs one, or the summary.
func (r *Router) onAwaitingBudget(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	if conv.Pending == nil {
		conv.State = StateIdle
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("onAwaitingBudget: %w", err)
		}
		return r.onIdle(ctx, in, id, conv, text)
	}
	// A bare "no" here answers the question ("nothing to buy"); only an
	// explicit cancel/stop drops the draft.
	if t := strings.ToLower(strings.TrimSpace(text)); t == "cancel" || t == "stop" {
		return r.dropDraft(ctx, in, conv)
	}
	items := conv.Pending.Items()
	i := conv.Pending.BudgetAskIndex
	if i < 0 || i >= len(items) {
		i = 0
	}
	switch {
	case IsNoBudget(text):
		items[i].BudgetCents = 0
	default:
		cents, ok := AmountCents(text)
		if !ok {
			// A bare yes answers "will your supporter be buying?" — but
			// only an amount answers the question (step 4: ours).
			if IsYes(text) {
				return r.Reply(ctx, in, CopyAskBudgetAgain, "")
			}
			if IsCancelRequest(text) {
				return r.dropDraftWith(ctx, in, conv, CopyDroppedCancelHint) // never relayed (Arbiter 2026-10-24 23:03)
			}
			// Not an amount: with a supporter on the job it may be for
			// them (D-17); a correction re-drafts; otherwise ask again.
			handled, label, err := r.routeInContextLabel(ctx, in, id, conv, text)
			if handled {
				return err
			}
			if label == llm.IntentCorrection {
				return r.startDraft(ctx, in, id, conv, conv.Pending.Source+"\n"+text)
			}
			return r.Reply(ctx, in, CopyAskBudgetAgain, "")
		}
		items[i].BudgetCents = cents
	}
	items[i].BudgetAsked = true
	// Re-quote with the budget: the hold covers it.
	cents, minutes, err := r.Booker.Quote(ctx, items[i].Draft, items[i].BudgetCents)
	if err != nil {
		return fmt.Errorf("onAwaitingBudget: quote: %w", err)
	}
	items[i].QuoteCents, items[i].QuoteMinutes = cents, minutes
	conv.Pending.SetItems(items)
	conv.Pending.BudgetAskIndex = -1
	return r.continueDraft(ctx, in, id, conv)
}

// continueDraft is the step after the draft is priced and the name is known:
// any budget question first, then the summary.
func (r *Router) continueDraft(ctx context.Context, in Inbound, id Identity, conv Conversation) error {
	if i := nextBudgetAsk(conv.Pending); i >= 0 {
		return r.askBudget(ctx, in, conv, i)
	}
	return r.showSummary(ctx, in, conv)
}
