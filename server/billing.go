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
	"log"
	"net/http"
	"time"

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
	// after that bills at the task's RESOLVED rate — PerMinuteRateCents
	// normally, SurgeRateCentsPerMin for an evening task.
	PerMinuteRateCents int
	IncludedMinutes    int

	// Evening surge. From SurgeStartHour local time in SurgeTimezone, minutes
	// past the included block bill at SurgeRateCentsPerMin.
	//
	// RESOLVED ONCE, AT POST, from the task's scheduled start, and stored on
	// the task — never re-derived afterwards. A task starting at 20:50 bills
	// at the standard rate for its whole run even if it finishes at 22:30,
	// because the price a requester agreed to must not change while somebody
	// is working. resolveRateCentsPerMin is the one place this is decided, and
	// the seam a future weather or festival surge hangs off.
	SurgeRateCentsPerMin int
	SurgeStartHour       int
	SurgeTimezone        string

	// Shopping. The requester approves a budget at post; the supporter fronts
	// the money and is reimbursed the verified receipt amount, capped at the
	// approved budget plus OverageToleranceCents. Anything above that needed
	// an approved budget increase BEFORE the purchase — there is no
	// after-the-fact charging against the budget, so an unapproved overage is
	// the supporter's own cost.
	//
	// There is NO ceiling on the budget itself, and the $30 one that used to
	// live here (in Go AND in a DB CHECK) was the wrong shape of protection:
	// the entire budget is now reserved on the card at post, so the requester
	// sees and authorizes the exact number before anything happens. A cap only
	// refused legitimate tasks. What replaces it warns rather than refuses —
	// see HighBudgetWarningCents.
	OverageToleranceCents int

	// Where the post form starts warning, in red, that this is a lot of money
	// to reserve. ADVISORY ONLY: nothing refuses a budget above it and the
	// server does not enforce it. It lives here rather than in the clients so
	// both warn at the same number (S-05) and moving it is one edit.
	HighBudgetWarningCents int

	// Time overrun. A supporter may run AutoExtendMinutes past the estimate
	// without a fresh approval when the requester consented at post
	// (tasks.auto_extend_consent). GracePeriodMinutes is the window in which
	// an overrun is flagged to ops rather than billed silently.
	//
	// Note what is NOT here any more: auto-extend does not affect the HOLD.
	// The hold is exactly the estimate plus the budget, and every minute past
	// it — consented or approved — is collected at completion. See
	// preAuthAmountCents.
	AutoExtendMinutes  int
	GracePeriodMinutes int

	// How long a requester has to one-tap approve a budget or time increase
	// before it auto-DENIES and the supporter's pre-selected fallback runs.
	ApprovalTimeoutMinutes int

	// How far ahead of the billable-time ceiling both parties are warned that
	// it is coming. Small on purpose: a warning that fires at half the
	// estimate is noise, and one that fires at the ceiling is not a warning.
	CapWarningLeadMinutes int

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

	PerMinuteRateCents: 50, // $0.50/min, standard
	IncludedMinutes:    15,

	SurgeRateCentsPerMin: 100, // $1.00/min from 21:00 New York
	SurgeStartHour:       21,
	SurgeTimezone:        "America/New_York",

	OverageToleranceCents: 500, // $5.00 auto-approved over the approved budget

	HighBudgetWarningCents: 50000, // $500.00 — warn loudly, refuse nothing

	AutoExtendMinutes:  15,
	GracePeriodMinutes: 30,

	ApprovalTimeoutMinutes: 5,
	CapWarningLeadMinutes:  5,

	ApplicationFeeBasisPoints: 0, // beta: platform takes nothing

	Currency: "usd",
}

// ── The rate ───────────────────────────────────────────────────────────────

// resolveRateCentsPerMin is the ONLY place a task's per-minute rate is
// decided. Called once, at post, from the task's scheduled start (posting time
// for an ASAP task); the answer is stored on tasks.rate_cents_per_min and
// every later read — estimate, ceiling, settlement, copy — uses the stored
// value.
//
// WHY RESOLVE ONCE AND STORE. The alternative, deriving the rate whenever a
// price is computed, silently re-prices a task in flight: a 20:50 job that runs
// to 21:10 would settle partly at a rate nobody quoted, and the same task would
// cost different amounts depending on when somebody happened to open the
// screen. A price is a term of an agreement, so it is fixed when the agreement
// is made.
//
// THIS IS THE SURGE SEAM. Weather, festivals, demand — all of them are "look at
// the start time and the world, return a rate", and all of them belong in this
// function. Nothing else in the codebase may branch on time to decide money.
func resolveRateCentsPerMin(start time.Time) int {
	loc, err := time.LoadLocation(Billing.SurgeTimezone)
	if err != nil {
		// A missing tzdata must not silently double everybody's rate. The
		// standard rate is the safe direction: it under-charges rather than
		// over-charges, and it is what every task billed at before surge
		// existed.
		log.Printf("[billing] timezone %q unavailable (%v) — falling back to the standard rate",
			Billing.SurgeTimezone, err)
		return Billing.PerMinuteRateCents
	}
	if start.In(loc).Hour() >= Billing.SurgeStartHour {
		return Billing.SurgeRateCentsPerMin
	}
	return Billing.PerMinuteRateCents
}

// isSurgeRate reports whether a stored rate is the evening one, so a client can
// be told WHY it is being quoted more without re-deriving anything.
func isSurgeRate(rateCents int) bool {
	return rateCents >= Billing.SurgeRateCentsPerMin
}

// normalizeRate guards every read of a stored rate. Rows written before
// tasks.rate_cents_per_min existed, and any row where it is somehow zero, bill
// at the standard rate rather than free.
func normalizeRate(rateCents int) int {
	if rateCents <= 0 {
		return Billing.PerMinuteRateCents
	}
	return rateCents
}

// taskRateCentsPerMin reads the rate a task was posted at.
func taskRateCentsPerMin(ctx context.Context, taskID string) int {
	var rate int
	_ = db.QueryRow(ctx,
		`select coalesce(rate_cents_per_min, 0) from public.tasks where id = $1::uuid`,
		taskID).Scan(&rate)
	return normalizeRate(rate)
}

// preAuthAmountCents is the hold placed on the requester's card at post:
//
//	base fee + estimated time cost + shopping budget
//
// EXACTLY WHAT THEY WERE SHOWN. No multiplier, no buffer, no headroom for
// auto-extend. The number on the confirmation and the number on their
// statement are the same number, and that is the entire design.
//
// What this replaced held 1.5x the time estimate plus the budget plus $5 —
// which meant a requester quoted $19.50 saw $34.25 disappear from their
// available balance with nothing anywhere explaining the gap. Over-holding is
// cheap for the platform and expensive for the person whose card it is: it is
// their money, frozen, and "we took more than we said in case" is not a thing
// you can put on a confirmation screen.
//
// THE TRADE, stated plainly: a task that runs past its estimate, or comes back
// with a receipt above the approved budget, can now settle for MORE than was
// held. That difference is collected at completion as a second charge against
// the saved card (payments.kind = 'completion_balance'), and if that charge
// fails the requester carries an outstanding balance that blocks further
// posting. See settleTaskPayment. The alternative — keep over-holding so the
// capture always fits — pays for a rare collection failure with a permanent,
// invisible tax on every honest requester's available balance.
//
// Auto-extend consent deliberately does NOT appear here. It governs whether a
// supporter may keep working past the estimate without asking; it has nothing
// to do with what is reserved, and two requesters who asked for the same task
// must see the same hold.
func preAuthAmountCents(category string, estimatedMinutes, shoppingBudgetCents, rateCents int) int {
	return baseFeeCents(category) + timeCostCents(estimatedMinutes, rateCents) + shoppingBudgetCents
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

// timeCostCents is what the logged time costs on top of the base fee, at the
// task's own resolved rate.
//
// The rate is a PARAMETER, never read from Billing here: two tasks running at
// the same moment can be on different rates (one posted at 20:50, one at
// 21:05), so a function that reached for the global would price one of them
// wrong. Callers pass the task's stored rate; taskRateCentsPerMin reads it.
func timeCostCents(totalMinutes, rateCents int) int {
	return billableMinutes(totalMinutes) * normalizeRate(rateCents)
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
	// Whether PerMinuteRateCents is the evening rate, so a client can say WHY
	// it is quoting more without knowing what the evening rate is or when it
	// starts.
	SurgeRate bool `json:"surge_rate"`
	// Minutes the quote was computed against — the requester's estimate on a
	// pre-submission quote, actual logged time at settlement.
	TotalMinutes    int `json:"total_minutes"`
	BillableMinutes int `json:"billable_minutes"`
	TimeCostCents   int `json:"time_cost_cents"`

	// Settlement only (Phase 2b), omitted from a pre-submission quote where
	// there is no ceiling to have hit yet.
	//
	// BilledMinutes is TotalMinutes clamped to CapMinutes. When they differ,
	// the supporter worked longer than the requester agreed to pay for, and a
	// client can say so in those words rather than presenting a total that
	// silently disagrees with the clock on the same screen.
	BilledMinutes int `json:"billed_minutes,omitempty"`
	CapMinutes    int `json:"cap_minutes,omitempty"`
	// The verified receipt, reimbursed up to the approved budget + tolerance.
	// Distinct from ShoppingBudgetCents below, which is the ceiling and not a
	// charge — at settlement this is the number that is actually in the total.
	ShoppingReceiptCents int `json:"shopping_receipt_cents,omitempty"`
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
func quoteTask(category string, totalMinutes, shoppingBudgetCents, rateCents int) TaskQuote {
	if totalMinutes < 0 {
		totalMinutes = 0
	}
	if shoppingBudgetCents < 0 {
		shoppingBudgetCents = 0
	}
	rateCents = normalizeRate(rateCents)
	base := baseFeeCents(category)
	time := timeCostCents(totalMinutes, rateCents)
	return TaskQuote{
		BaseFeeCents:        base,
		IncludedMinutes:     Billing.IncludedMinutes,
		PerMinuteRateCents:  rateCents,
		SurgeRate:           isSurgeRate(rateCents),
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
// taskID: base fee + every minute past the first 15, up to the ceiling the
// requester consented to. It does NOT include the shopping budget — that
// settles separately against a verified receipt.
//
// The cap is applied here rather than at the call sites so that the completion
// path, the force-complete path and the /worklogs breakdown cannot disagree
// about what a task costs. See taskTimeCapMinutes for what the ceiling is.
func calcTaskCostCents(ctx context.Context, taskID string, totalMinutes int) int {
	capMinutes, _ := taskTimeCapMinutes(ctx, taskID)
	return baseFeeCents(taskCategory(ctx, taskID)) +
		timeCostCentsCapped(totalMinutes, capMinutes, taskRateCentsPerMin(ctx, taskID))
}

// ── The billable-time ceiling ──────────────────────────────────────────────
//
// THE RULE, in one line: a requester is never charged for time they did not
// agree to in advance.
//
// What they agreed to is their estimate, plus AutoExtendMinutes if they ticked
// the consent box at post, plus every minute of every extension request they
// approved afterwards. Time worked past that still happens — a supporter is
// never told to abandon somebody mid-task — it simply stops costing anything,
// and both parties are told before it does (server/timecap.go).
//
// THE CEILING BOUNDS TOTAL LOGGED MINUTES, NOT BILLABLE MINUTES, and the
// order matters. Capping billable minutes instead would hand the requester
// back the 15-minute inclusion a second time: on a 30-minute estimate with
// consent, a 60-minute task would bill min(60-15, 45) = 45 billable minutes —
// MORE than the 45-minute ceiling itself is worth. Clamping the logged total
// first gives min(60,45) = 45 logged -> 30 billable, which is exactly "you
// consented to 45 minutes of work, the first 15 of which were in the base
// fee".

// cappedMinutes clamps logged time to a ceiling. A ceiling of zero or less
// means no ceiling at all — the value taskTimeCapMinutes returns when a task
// has no estimate to reason from, where capping to zero would silently make
// the task free.
func cappedMinutes(totalMinutes, capMinutes int) int {
	if totalMinutes < 0 {
		totalMinutes = 0
	}
	if capMinutes <= 0 || totalMinutes <= capMinutes {
		return totalMinutes
	}
	return capMinutes
}

// timeCostCentsCapped is timeCostCents against the consented ceiling.
func timeCostCentsCapped(totalMinutes, capMinutes, rateCents int) int {
	return timeCostCents(cappedMinutes(totalMinutes, capMinutes), rateCents)
}

// timeCapWarningMinutes is the logged total at which Layer 2 fires, measured
// from the time the requester AGREED TO — their estimate plus any extension
// they approved — and never from the ceiling above it.
//
// THE ANCHOR IS THE ESTIMATE, NOT THE CEILING, and the distinction is the
// whole point of the warning. Auto-extend is a fuse, not a new estimate: a
// requester who ticks the box at post is saying "if it runs over, don't stop
// the clock for 15 minutes", not "the job is really 45 minutes". Warning at
// ceiling-minus-5 fired at 40 minutes on a 30-minute task — ten minutes after
// the moment the warning exists to announce, and with the fuse already half
// burnt. Both parties are told when the ESTIMATE is in sight, which is the
// only number either of them ever agreed on.
//
// Approved extensions DO move it, because an approval is the requester saying
// the estimate was wrong and naming a new one. That also keeps the sequence
// sane after resolveExtension clears the latches: without it, a task whose
// requester has just granted +15 would re-warn on the next ping, since it is
// already past the original estimate.
//
// Clamped to at least one minute, because a task estimated at five minutes or
// less would otherwise warn at or before its own start — a warning that has
// already fired when the supporter clocks in tells them nothing.
func timeCapWarningMinutes(agreedMinutes int) int {
	if agreedMinutes <= 0 {
		return 0
	}
	if w := agreedMinutes - Billing.CapWarningLeadMinutes; w >= 1 {
		return w
	}
	return 1
}

// TimeCap is a task's billable-time ceiling, itemized — the same shape the
// settlement payload and the supporter's screen both render, so neither has to
// re-derive "where did 45 come from".
type TimeCap struct {
	// The requester's estimate at post. The base of the ceiling.
	EstimateMinutes int `json:"estimate_minutes"`
	// AutoExtendMinutes, or 0 when the requester declined at post.
	AutoExtendMinutes int `json:"auto_extend_minutes"`
	// Minutes added by extension requests the requester has approved.
	ApprovedExtraMinutes int `json:"approved_extra_minutes"`
	// What the requester actually AGREED the job would take: their estimate
	// plus every minute they later approved. Excludes auto-extend, which is a
	// fuse nobody planned around. This is the number both the early warning
	// and the copy that explains it are anchored to.
	AgreedMinutes int `json:"agreed_minutes"`
	// The sum, and the number billing actually clamps against.
	CapMinutes int `json:"cap_minutes"`
	// Where Layer 2 fires: AgreedMinutes minus the warning lead. Strictly
	// below CapMinutes whenever auto-extend is on, and equal to the old
	// behaviour when it is off.
	WarnAtMinutes int `json:"warn_at_minutes"`
	// Whether the requester consented at post. Rendered as a reason, not used
	// as arithmetic — AutoExtendMinutes above already carries the effect.
	AutoExtendConsent bool `json:"auto_extend_consent"`
}

// taskTimeCapMinutes reads a task's ceiling. Returns (capMinutes, detail).
//
// A task with no usable estimate returns 0, which cappedMinutes reads as "no
// ceiling" — the honest answer for a row that never recorded what was agreed,
// and far better than pricing it at zero.
func taskTimeCapMinutes(ctx context.Context, taskID string) (int, TimeCap) {
	var estimate int
	var consent bool
	if err := db.QueryRow(ctx, `
		select coalesce(estimated_minutes, 0), coalesce(auto_extend_consent, true)
		  from public.tasks where id = $1::uuid
	`, taskID).Scan(&estimate, &consent); err != nil {
		return 0, TimeCap{}
	}

	var approvedExtra int
	// Errors are deliberately swallowed into zero rather than propagated: a
	// failed read here must never invent headroom the requester did not
	// approve. Zero extra is the conservative direction.
	_ = db.QueryRow(ctx, `
		select coalesce(sum(requested_minutes), 0)
		  from public.extension_requests
		 where task_id = $1::uuid and kind = 'time' and status = 'approved'
	`, taskID).Scan(&approvedExtra)

	detail := TimeCap{
		EstimateMinutes:      estimate,
		ApprovedExtraMinutes: approvedExtra,
		AutoExtendConsent:    consent,
	}
	if estimate <= 0 {
		return 0, detail
	}
	if consent {
		detail.AutoExtendMinutes = Billing.AutoExtendMinutes
	}
	detail.AgreedMinutes = estimate + approvedExtra
	detail.CapMinutes = detail.AgreedMinutes + detail.AutoExtendMinutes
	detail.WarnAtMinutes = timeCapWarningMinutes(detail.AgreedMinutes)
	return detail.CapMinutes, detail
}

// ── Settlement ─────────────────────────────────────────────────────────────

// reimbursableReceiptCents is what the platform will pay back against a
// receipt: the verified amount, capped at the approved budget plus the
// tolerance. Anything above that needed an approved increase BEFORE the
// purchase, so it is the supporter's own cost — there is no after-the-fact
// charging, ever.
//
// The handler refuses an over-tolerance receipt with a 400 long before this is
// reached (see completeTask); this is the arithmetic backstop for the paths
// that do not go through it — force-complete above all, where an admin closes
// a task carrying a receipt nobody validated.
func reimbursableReceiptCents(receiptCents, approvedBudgetCents int) int {
	if receiptCents <= 0 {
		return 0
	}
	ceiling := approvedBudgetCents + Billing.OverageToleranceCents
	if receiptCents > ceiling {
		return ceiling
	}
	return receiptCents
}

// receiptWithinTolerance reports whether a receipt can be reimbursed in full.
// The negation is the 400 the supporter gets, and the number they are told.
func receiptWithinTolerance(receiptCents, approvedBudgetCents int) bool {
	return receiptCents <= approvedBudgetCents+Billing.OverageToleranceCents
}

// quoteSettlement prices a finished task: the same itemization quoteTask
// returns, clamped to the consented ceiling and carrying the verified receipt
// instead of the approved budget in the total.
//
// The budget stays on the quote as context — "you approved $20, the receipt
// was $17.40" is the sentence the requester needs — but it is the RECEIPT that
// is added to the total, because the budget was never a charge.
func quoteSettlement(category string, totalMinutes, capMinutes, approvedBudgetCents, receiptCents, rateCents int) TaskQuote {
	billed := cappedMinutes(totalMinutes, capMinutes)
	q := quoteTask(category, billed, approvedBudgetCents, rateCents)
	// quoteTask reports the minutes it priced. Settlement needs both: what was
	// worked, and what was billable after the ceiling.
	q.TotalMinutes = totalMinutes
	q.BilledMinutes = billed
	q.CapMinutes = capMinutes
	q.ShoppingReceiptCents = reimbursableReceiptCents(receiptCents, approvedBudgetCents)
	q.TotalCents = q.BaseFeeCents + q.TimeCostCents + q.ShoppingReceiptCents
	return q
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
func cancelSettlementCents(category string, totalMinutes int, hadSession bool, rateCents int) int {
	if !hadSession {
		return 0
	}
	return baseFeeCents(category) + timeCostCents(totalMinutes, rateCents)
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
		// When the task would start, RFC3339. Decides the rate, because the
		// evening rate is a property of when the work happens, not of when the
		// form was opened — a requester filling in a 21:30 task at 6pm has to
		// be quoted the evening rate. Absent or unparseable means ASAP, which
		// resolves against now.
		ScheduledAt string `json:"scheduled_at"`
		IsImmediate bool   `json:"is_immediate"`
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

	// No cap. A budget larger than the requester expected to type is warned
	// about in the form, never refused here — the whole amount is reserved on
	// their card at post, so they see and authorize the exact number before
	// anything is held. See BillingConfig.HighBudgetWarningCents.
	quote := quoteTask(in.Category, in.EstimatedMinutes, in.PrepayAmountCents,
		resolveRateCentsPerMin(estimateStartAt(in.IsImmediate, in.ScheduledAt)))

	c.JSON(http.StatusOK, gin.H{
		"base_fee_cents":        quote.BaseFeeCents,
		"included_minutes":      quote.IncludedMinutes,
		"per_minute_rate_cents": quote.PerMinuteRateCents,
		"surge_rate":            quote.SurgeRate,
		"total_minutes":         quote.TotalMinutes,
		"billable_minutes":      quote.BillableMinutes,
		"time_cost_cents":       quote.TimeCostCents,
		"shopping_budget_cents": quote.ShoppingBudgetCents,
		"shopping_cents":        quote.ShoppingCentsLegacy,
		"total_cents":           quote.TotalCents,
		// What posting will actually reserve. Identical to total_cents today —
		// the hold IS the estimate plus the budget — and sent as its own field
		// because that identity is a design decision rather than a coincidence,
		// and a client should not have to know it holds in order to render a
		// confirmation.
		"hold_cents": quote.TotalCents,
		// The threshold at which the form warns about a large reservation.
		// Server-owned so both clients warn at the same number (S-05).
		"high_budget_warning_cents": Billing.HighBudgetWarningCents,
	})
}

// estimateStartAt is when the quoted task would begin, for rate resolution.
// An unparseable or absent time is treated as ASAP rather than rejected: a bad
// scheduled_at is the form's problem to report, and quoting at the current
// rate is the honest fallback.
func estimateStartAt(isImmediate bool, scheduledAt string) time.Time {
	if isImmediate || scheduledAt == "" {
		return time.Now()
	}
	if t, err := time.Parse(time.RFC3339, scheduledAt); err == nil {
		return t
	}
	return time.Now()
}

// formatCentsUSD renders integer cents for humans. Money is integer cents
// everywhere in this codebase and only ever becomes a float here, at the last
// step before a string — there is no arithmetic downstream of this call.
func formatCentsUSD(cents int) string {
	return fmt.Sprintf("$%.2f", float64(cents)/100.0)
}
