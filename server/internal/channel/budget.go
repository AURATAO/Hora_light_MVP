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
func AmountCents(text string) (int, bool) {
	m := amountRE.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	for _, g := range m[1:] {
		if g != "" {
			n, err := strconv.Atoi(g)
			if err != nil || n <= 0 {
				return 0, false
			}
			return n * 100, true
		}
	}
	return 0, false
}

// IsNoBudget reports "nothing to buy" as an answer to the budget question.
func IsNoBudget(text string) bool { return noBudgetRE.MatchString(text) }

// IsNoAutoExtend reports an explicit opt-out of the auto-extension.
func IsNoAutoExtend(text string) bool { return noAutoRE.MatchString(text) }

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
		// The model is a tiebreak only; unavailable → no question. The
		// supporter can still ask for a budget from the app.
		log.Printf("[channel] purchase check fell back to no: %v", err)
		return false
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
			// Not an amount: with a supporter on the job it may be for
			// them (D-17); otherwise ask again.
			if handled, err := r.routeInContext(ctx, in, id, conv, text); handled {
				return err
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
