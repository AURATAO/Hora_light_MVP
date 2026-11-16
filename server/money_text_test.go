package main

import (
	"strings"
	"testing"
)

// The offer's label and the push quote the same amount: both are
// estimatedSupporterPayCents.
func TestPayLabelMatchesThePush(t *testing.T) {
	task := dispatchTask{Category: "delivery", Minutes: 30, RateCents: 60, BaseFeeCents: 1500, PlatformFeeBps: 3000, Immediate: true}
	cents := estimatedSupporterPayCents(task)
	if cents <= 0 {
		t.Fatalf("estimate = %d, want a positive amount", cents)
	}
	label := payLabel(cents, task.Minutes)
	title, _ := dispatchCopy(task, "Manhattan")
	amount := formatCentsUSD(cents)
	if !strings.Contains(label, amount) || !strings.Contains(title, amount) {
		t.Errorf("label %q and push title %q should both carry %s", label, title, amount)
	}
	// $15 base covers the first 15 minutes, then 15 x $0.60 = $24.00; less 30%.
	if amount != "$16.80" {
		t.Errorf("amount = %s, want $16.80", amount)
	}
	// Billed by the minute: the label names the time it is for, never a
	// bare price.
	if label != "About $16.80 for the 30 min booked" {
		t.Errorf("label = %q", label)
	}
}

func TestPayLabelSaysNothingWithoutAnEstimate(t *testing.T) {
	if got := payLabel(estimatedSupporterPayCents(dispatchTask{Category: "delivery", Minutes: 0}), 0); got != "" {
		t.Errorf("label = %q, want empty: no estimate, nothing to promise", got)
	}
}

func TestTransferBreakdownLabel(t *testing.T) {
	for _, tc := range []struct {
		name string
		e    EarningsTransfer
		want string
	}{
		{"service and a receipt", EarningsTransfer{ServiceGrossCents: 2800, PlatformFeeCents: 840, PlatformFeeBps: 3000, TimeCents: 1960, ReceiptCents: 1240},
			"$19.60 service (after the 30% platform fee) + $12.40 reimbursement"},
		{"the rate it was cut at, not today's", EarningsTransfer{ServiceGrossCents: 2400, PlatformFeeCents: 480, PlatformFeeBps: 2000, TimeCents: 1920},
			"$19.20 service (after the 20% platform fee)"},
		{"a row from before the fee", EarningsTransfer{TimeCents: 2700, ReceiptCents: 1240},
			"$27.00 service + $12.40 reimbursement"},
		{"a receipt alone", EarningsTransfer{ReceiptCents: 1240}, "$12.40 reimbursement"},
	} {
		if got := transferBreakdownLabel(tc.e); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
