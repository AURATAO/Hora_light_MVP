package main

import (
	"context"
	"fmt"
)

// Money, in the server's words (PLAN 13c, Aura 2026-11-16).
//
// The supporter app shows these strings when they are present and its own
// copy when they are not. So how a task's pay or a payment is DESCRIBED can
// change here (a flat price, a bonus, a reimbursement paid early from the
// float) without an app release, which needs App Review.
//
//	Task.pay_label / pay_note        an open task, to someone who could take it
//	EarningsTransfer.breakdown_label what a payment was made of
//	EarningsTransfer.note            one more line under it; "" today
//	SupporterEarnings.breakdown_label / note   the same, on a task's settlement

// estimatedSupporterPayCents is what a supporter would earn at the task's
// estimate: base plus estimated time at the prices the task was posted at,
// less the platform fee. Never the requester's total, never a receipt. 0 when
// there is no estimate to price.
func estimatedSupporterPayCents(t dispatchTask) int {
	if t.Minutes <= 0 {
		return 0
	}
	base, bps := t.BaseFeeCents, t.PlatformFeeBps
	if base <= 0 {
		// A task built without a read (tests, a direct call): priced as one
		// posted now would be.
		base, bps = newTaskPrice(t.Category)
	}
	q := quoteTaskBase(base, t.Minutes, 0, t.RateCents)
	return supporterPayForBps(q.BaseFeeCents+q.TimeCostCents, 0, bps).ServiceNetCents()
}

// payLabel is the line an offer carries. "" when there is nothing to say.
//
// HO:RA bills by the minute, so this is never a price: it is what the time
// the requester booked would pay, and it says the time it is for (Aura
// 2026-11-16). payNote, shown with it on the task screen, says why the final
// amount can differ.
func payLabel(cents, minutes int) string {
	if cents <= 0 || minutes <= 0 {
		return ""
	}
	return fmt.Sprintf("About %s for the %d min booked", formatCentsUSD(cents), minutes)
}

const payNote = "You're paid by the minute: less if it takes less time, more if the requester approves extra time. Anything you buy is paid back in full on top."

// taskPayText reads the task and words its pay. Empty on any error: the
// label is an extra, and a board or a task screen must not fail over it.
func taskPayText(ctx context.Context, taskID string) (label, note string) {
	t, err := loadDispatchTask(ctx, taskID)
	if err != nil {
		return "", ""
	}
	if label = payLabel(estimatedSupporterPayCents(t), t.Minutes); label == "" {
		return "", ""
	}
	return label, payNote
}

// transferBreakdownLabel words an Earnings row. A row from before the fee
// was itemized has no gross figure; its TimeCents is the whole service.
func transferBreakdownLabel(e EarningsTransfer) string {
	if e.ServiceGrossCents > 0 || e.PlatformFeeCents > 0 {
		return payoutBreakdownLineBps(e.ServiceGrossCents, e.PlatformFeeCents, e.ReceiptCents, e.PlatformFeeBps)
	}
	return payoutBreakdownLineBps(e.TimeCents, 0, e.ReceiptCents, 0)
}
