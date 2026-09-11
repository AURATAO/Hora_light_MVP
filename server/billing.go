package main

// The billing engine. Every number that decides what a requester owes lives in
// this file and nowhere else (S-05: pricing is computed in Go only).
//
// Before this file existed the schedule was two literals scattered through
// main.go — `const overtimeRateCents = 50` next to the worklog handlers, and a
// three-arm switch 600 lines away — plus three hand-copied duplicates in the
// web app. That is exactly how the base fee and the rate drift apart. The rule
// now: a billing constant that is not a field of BillingConfig is a bug.
//
// THE MODEL
//
//	base fee        $12.00, or $25.00 for companionship
//	included        the first 15 minutes are inside the base fee
//	billable        max(total_logged_minutes - 15, 0)
//	time cost       billable x $0.50/min
//	task total      base fee + time cost  (+ verified receipt at settlement)
//
// total_logged_minutes is the sum across ALL closed worklog sessions on the
// task. A laundry-style task clocked in and out three times bills the sum of
// the three, and the 15-minute inclusion is applied ONCE against that sum —
// not once per session. Gaps between sessions are not billed and do not
// consume the inclusion. Session rounding (ceil, minimum 1 minute) stays
// per-session and happens before the sum.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// BillingConfig is the single source of every monetary and time constant in
// the product. Phase 2/3 fields (approval timeout, pre-auth shape, payouts)
// are here already rather than being introduced alongside the code that first
// reads them — the point of a config block is that the whole schedule is
// legible in one screen.
type BillingConfig struct {
	// Base fee by category. Companionship is a different service with a
	// different floor, not a surcharge on the same one.
	BaseFeeDefaultCents       int
	BaseFeeCompanionshipCents int

	// Time. The first IncludedMinutes are inside the base fee; every minute
	// after that bills at PerMinuteRateCents.
	PerMinuteRateCents int
	IncludedMinutes    int

	// Shopping. The requester approves a budget at post; the supporter fronts
	// the money and is reimbursed the verified receipt amount, capped at the
	// approved budget plus OverageToleranceCents. Anything above that needed
	// an approved budget increase BEFORE the purchase — there is no
	// after-the-fact charging, so an unapproved overage is the supporter's own
	// cost.
	ShoppingBudgetCapCents int
	OverageToleranceCents  int

	// Time overrun. A supporter may run AutoExtendMinutes past the estimate
	// without a fresh approval when the requester consented at post
	// (tasks.auto_extend_consent). GracePeriodMinutes is the window in which
	// an overrun is flagged to ops rather than billed silently.
	AutoExtendMinutes  int
	GracePeriodMinutes int

	// How long a requester has to one-tap approve a budget increase before it
	// auto-DENIES and the supporter's pre-selected fallback executes (Phase 2).
	ApprovalTimeoutMinutes int

	// Pre-authorization held at post time:
	//   time_estimate_cost x PreAuthMultiplier + budget + PreAuthBufferCents
	// The hold is deliberately larger than the expected capture; the unused
	// remainder is released, never refunded (Phase 2).
	PreAuthMultiplier  float64
	PreAuthBufferCents int

	// Marketplace take, in basis points of the captured total. Zero during
	// beta: supporters keep 100% of time cost and the full receipt amount.
	// Parameterized now so Phase 3 turns it on with a config edit (Stripe
	// destination charge application_fee_amount), not a formula change.
	ApplicationFeeBasisPoints int

	// ISO 4217, lowercase, as Stripe wants it. The product has always been
	// single-currency and every amount in the codebase is integer USD cents;
	// this field exists so that assumption has one name rather than being
	// spelled "$" in a dozen format strings.
	Currency string
}

// Billing is the live schedule. Changing a value here changes every quote,
// every settlement, and every client-rendered price at once, which is the
// entire point.
var Billing = BillingConfig{
	BaseFeeDefaultCents:       1200, // $12.00
	BaseFeeCompanionshipCents: 2500, // $25.00

	PerMinuteRateCents: 50, // $0.50/min
	IncludedMinutes:    15,

	ShoppingBudgetCapCents: 3000, // $30.00 — enforced in Go AND by a DB CHECK
	OverageToleranceCents:  500,  // $5.00 auto-approved over the budget

	AutoExtendMinutes:  15,
	GracePeriodMinutes: 30,

	ApprovalTimeoutMinutes: 5,

	PreAuthMultiplier:  1.5,
	PreAuthBufferCents: 500, // $5.00

	ApplicationFeeBasisPoints: 0, // beta: platform takes nothing

	Currency: "usd",
}

// preAuthAmountCents is the hold placed on the requester's card at post time:
//
//	(base fee + estimated time cost) x PreAuthMultiplier + budget + buffer
//
// The multiplier applies to the whole time-based estimate, base fee included,
// not to the per-minute portion alone. The alternative reading does not
// survive contact with an example: a 30-minute task estimates $12.00 base +
// $7.50 time = $19.50, and holding 1.5x the $7.50 alone gives $11.25 + $5.00 =
// $16.25 — less than the capture on a task that runs exactly to estimate. A
// hold that cannot cover the happy path is not a hold.
//
// The shopping budget is added at face value rather than multiplied: it is
// already a ceiling the requester set, and the $5 buffer is precisely the
// approved overage tolerance.
func preAuthAmountCents(category string, estimatedMinutes, shoppingBudgetCents int) int {
	timeEstimate := baseFeeCents(category) + timeCostCents(estimatedMinutes)
	held := int(float64(timeEstimate)*Billing.PreAuthMultiplier) + shoppingBudgetCents + Billing.PreAuthBufferCents
	return held
}

// isCompanionship reports whether a category bills at the companionship base
// fee. Both spellings are live: the picker submits "companionship" and Post
// Task normalizes it to "companion", and rows of both exist in production.
func isCompanionship(category string) bool {
	return category == "companionship" || category == "companion"
}

// baseFeeCents is the base fee for a category.
//
// Two tiers, deliberately. The old schedule had a third — $18.00 when
// estimated_minutes > 90 — which was a duration surcharge wearing a category's
// clothes: it double-charged for length that the per-minute rate already
// bills, and it keyed off the requester's *estimate* rather than time actually
// worked, so a task that overran its estimate was cheaper than one that was
// estimated honestly. Removed.
func baseFeeCents(category string) int {
	if isCompanionship(category) {
		return Billing.BaseFeeCompanionshipCents
	}
	return Billing.BaseFeeDefaultCents
}

// billableMinutes applies the included block: the first Billing.IncludedMinutes
// of a task are inside the base fee and bill nothing further.
//
// totalMinutes is the task's summed closed-session time, so the inclusion is
// consumed once per task no matter how many times the supporter clocked in.
func billableMinutes(totalMinutes int) int {
	if totalMinutes <= Billing.IncludedMinutes {
		return 0
	}
	return totalMinutes - Billing.IncludedMinutes
}

// timeCostCents is what the logged time costs on top of the base fee.
func timeCostCents(totalMinutes int) int {
	return billableMinutes(totalMinutes) * Billing.PerMinuteRateCents
}

// TaskQuote is one itemized price. It is what /tasks/estimate returns and what
// every settlement path computes, so a client can render a receipt line for
// line without knowing any of the arithmetic above.
type TaskQuote struct {
	BaseFeeCents    int `json:"base_fee_cents"`
	IncludedMinutes int `json:"included_minutes"`
	// The rate, so a client can label "N billable min x $0.50" without
	// hardcoding the 50. Without this every quote surface keeps one pricing
	// constant of its own, which is the drift S-05 exists to prevent — it is
	// how the web app ended up with three copies of the schedule.
	PerMinuteRateCents int `json:"per_minute_rate_cents"`
	// Minutes the quote was computed against — the requester's estimate on a
	// pre-submission quote, actual logged time at settlement.
	TotalMinutes    int `json:"total_minutes"`
	BillableMinutes int `json:"billable_minutes"`
	TimeCostCents   int `json:"time_cost_cents"`
	// The approved shopping ceiling, not a charge. Nothing is owed here until
	// a receipt is verified at completion (Phase 2).
	ShoppingBudgetCents int `json:"shopping_budget_cents"`
	TotalCents          int `json:"total_cents"`

	// Deprecated: the pre-Phase-1 name for ShoppingBudgetCents. Shipped mobile
	// builds (TaskForm.tsx) read this key, and Phase 1 deploys Go without a
	// mobile build, so removing it would render "$NaN" in every installed app
	// the moment the backend went out. Drop it once the old builds are gone.
	ShoppingCentsLegacy int `json:"shopping_cents"`
}

// quoteTask prices a task: base fee for the category, plus time beyond the
// included block, plus the shopping budget the requester approved.
func quoteTask(category string, totalMinutes, shoppingBudgetCents int) TaskQuote {
	if totalMinutes < 0 {
		totalMinutes = 0
	}
	if shoppingBudgetCents < 0 {
		shoppingBudgetCents = 0
	}
	base := baseFeeCents(category)
	time := timeCostCents(totalMinutes)
	return TaskQuote{
		BaseFeeCents:        base,
		IncludedMinutes:     Billing.IncludedMinutes,
		PerMinuteRateCents:  Billing.PerMinuteRateCents,
		TotalMinutes:        totalMinutes,
		BillableMinutes:     billableMinutes(totalMinutes),
		TimeCostCents:       time,
		ShoppingBudgetCents: shoppingBudgetCents,
		TotalCents:          base + time + shoppingBudgetCents,
		ShoppingCentsLegacy: shoppingBudgetCents,
	}
}

// ── Settlement against real worklogs ───────────────────────────────────────

// totalClosedMinutes is the task's billable clock: the sum over every CLOSED
// session, each rounded up to a whole minute with a one-minute floor.
//
// Rounding stays per-session (a 30-second errand is a minute, twice) but the
// sum is what billing sees, so the 15-minute inclusion applied downstream is
// consumed once per task. Open sessions are excluded — a supporter still on
// the clock has not yet logged anything billable.
func totalClosedMinutes(ctx context.Context, taskID string) (int, error) {
	var total int
	err := db.QueryRow(ctx, `
		with x as (
			select ceil(extract(epoch from (end_at - start_at))/60.0)::int as m
			from public.worklogs
			where task_id=$1 and end_at is not null and end_at > start_at
		)
		select coalesce(sum(greatest(m,1)),0) from x
	`, taskID).Scan(&total)
	return total, err
}

// taskCategory reads the one field pricing depends on. Estimated minutes are
// deliberately not read: since the >90min tier was removed, nothing about the
// requester's estimate affects what they are charged — only time actually
// worked does.
func taskCategory(ctx context.Context, taskID string) string {
	var category string
	_ = db.QueryRow(ctx,
		`SELECT COALESCE(category,'') FROM public.tasks WHERE id=$1::uuid`, taskID,
	).Scan(&category)
	return category
}

// calcTaskCostCents is the service charge in cents for totalMinutes of work on
// taskID: base fee + every minute past the first 15. It does NOT include the
// shopping budget — that settles separately against a verified receipt.
func calcTaskCostCents(ctx context.Context, taskID string, totalMinutes int) int {
	return baseFeeCents(taskCategory(ctx, taskID)) + timeCostCents(totalMinutes)
}

// cancelSettlementCents is what a cancelled task owes.
//
// Nothing, unless a supporter actually started: a task cancelled before any
// clock-in cost nobody anything, so charging its base fee would be billing for
// a service that was never begun. Once there is a closed session the base fee
// is earned (the supporter travelled and showed up) and time past the included
// block bills normally.
//
// The shopping budget is not an input here and never should be. It is an
// authorization ceiling, not money anyone has been charged, so a cancel has
// nothing to net against it.
func cancelSettlementCents(category string, totalMinutes int, hadSession bool) int {
	if !hadSession {
		return 0
	}
	return baseFeeCents(category) + timeCostCents(totalMinutes)
}

// ── POST /tasks/estimate ───────────────────────────────────────────────────

// estimateTaskCost is the pre-submission quote for the Post Task form. No task
// exists yet, so inputs come from the request body instead of a DB row; the
// arithmetic is the same quoteTask that settles a finished task, applied to
// the requester's estimate instead of their supporter's logged time.
//
// It is the only pricing surface clients are allowed to use (S-05). Both the
// web app and mobile render this response verbatim; neither re-derives it.
func estimateTaskCost(c *gin.Context) {
	if c.GetString("uid") == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	var in struct {
		Category          string `json:"category"`
		EstimatedMinutes  int    `json:"estimated_minutes"`
		PrepayAmountCents int    `json:"prepay_amount_cents"`
	}
	if err := c.BindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	if in.EstimatedMinutes < 0 {
		in.EstimatedMinutes = 0
	}
	if in.PrepayAmountCents < 0 {
		in.PrepayAmountCents = 0
	}
	// A quote above the cap is refused rather than silently quoted at the cap:
	// the form would otherwise show a total the requester never asked for.
	if in.PrepayAmountCents > Billing.ShoppingBudgetCapCents {
		c.JSON(http.StatusBadRequest, shoppingBudgetCapError())
		return
	}

	c.JSON(http.StatusOK, quoteTask(in.Category, in.EstimatedMinutes, in.PrepayAmountCents))
}

// shoppingBudgetCapError is the one phrasing of the cap rejection, shared by
// the quote endpoint and by task create/update so a requester cannot be told
// two different limits.
func shoppingBudgetCapError() gin.H {
	return gin.H{
		"error":     "shopping_budget_over_cap",
		"message":   "The maximum shopping budget during beta is " + formatCentsUSD(Billing.ShoppingBudgetCapCents) + ".",
		"cap_cents": Billing.ShoppingBudgetCapCents,
	}
}

// formatCentsUSD renders integer cents for humans. Money is integer cents
// everywhere in this codebase and only ever becomes a float here, at the last
// step before a string — there is no arithmetic downstream of this call.
func formatCentsUSD(cents int) string {
	return fmt.Sprintf("$%.2f", float64(cents)/100.0)
}
