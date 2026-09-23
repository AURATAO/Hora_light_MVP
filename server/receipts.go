package main

// The requester's receipt.
//
// ONE RULE: a charge never happens without one. The receipt is sent from the
// two places money leaves a requester's card — settleCompletedTask, the one
// call every closing path makes (complete, force-complete, a cancel that
// billed), and runSettlePass, which collects an outstanding balance — and
// from nowhere else. A receipt is a consequence of a charge, so it lives
// beside the charge rather than in the handlers that happen to cause one.
//
// WHAT IT SAYS. The settlement, itemized the way the task screen itemizes it
// and from the same numbers: base fee, billable minutes × the task's rate,
// the reimbursed receipt, the mid-task asks the requester approved, the promo
// discount, the total charged, the card, and — when the hold was larger than
// the charge — what Stripe released back. When the completion took two
// charges (the hold and a completion balance), both are named. Nothing is
// re-priced here: every amount comes from the settlement outcome and the same
// readers the task screen uses (S-05).
//
// Three channels, as every notification: the in-app row, the push, and the
// email — subject "Your HO:RA receipt — $X", body the itemized table
// (notify.receiptEmail).

import (
	"context"
	"fmt"
	"log"
	"strings"

	"hora-auth/internal/notify"
)

// receiptCharge is one movement of money on a receipt: which payments row,
// how much, and what to call it.
type receiptCharge struct {
	// "Reserved amount" for the hold, "Balance" for a completion balance,
	// "Outstanding balance" for a settle-balance charge.
	Label     string
	PaymentID string
	Cents     int
}

// receiptChargesFor lists what a settlement actually took: the hold's capture
// when it took anything (a zero hold under a promo takes nothing and is not a
// charge), and the balance charge when it succeeded.
func receiptChargesFor(out settlementOutcome) []receiptCharge {
	var charges []receiptCharge
	if out.MainPaymentID != "" && out.MainCapturedCents > 0 {
		charges = append(charges, receiptCharge{Label: "Reserved amount", PaymentID: out.MainPaymentID, Cents: out.MainCapturedCents})
	}
	if out.BalancePaymentID != "" && out.BalanceCapturedCents > 0 {
		charges = append(charges, receiptCharge{Label: "Balance", PaymentID: out.BalancePaymentID, Cents: out.BalanceCapturedCents})
	}
	return charges
}

// sendRequesterReceipt builds and sends the receipt for one or more charges on
// a task. Nothing to send when nothing was charged — a receipt for $0.00 is
// not a receipt, it is a notification pretending to be one.
func sendRequesterReceipt(ctx context.Context, taskID string, charges []receiptCharge) {
	total := 0
	for _, ch := range charges {
		total += ch.Cents
	}
	if total <= 0 {
		return
	}

	t, err := loadAdminTask(ctx, taskID)
	if err != nil {
		log.Printf("[receipt][ERROR] task=%s could not be read: %v", taskID, err)
		return
	}

	// The itemization, from the same readers the task screen uses.
	inputs := readSettlementInputs(ctx, taskID)
	quote := inputs.quote()
	promoCode, promoDiscount := promoDiscountForTask(ctx, taskID)
	applied := promoApplied(promoDiscount, quote.TotalCents)

	r := notify.Receipt{
		TaskTitle: t.Title,
		Total:     formatCentsUSD(total),
	}
	r.Lines = append(r.Lines, notify.ReceiptLine{
		Label:  fmt.Sprintf("Base fee (first %d min included)", quote.IncludedMinutes),
		Amount: formatCentsUSD(quote.BaseFeeCents),
	})
	r.Lines = append(r.Lines, notify.ReceiptLine{
		Label:  fmt.Sprintf("%d billable min × %s", quote.BillableMinutes, formatCentsUSD(quote.PerMinuteRateCents)),
		Amount: formatCentsUSD(quote.TimeCostCents),
	})
	if quote.ShoppingReceiptCents > 0 || inputs.ApprovedBudgetCents > 0 {
		r.Lines = append(r.Lines, notify.ReceiptLine{
			Label:  fmt.Sprintf("Receipt reimbursement (budget %s)", formatCentsUSD(inputs.ApprovedBudgetCents)),
			Amount: formatCentsUSD(quote.ShoppingReceiptCents),
		})
	}
	// The mid-task asks the requester said yes to. Already reflected in the
	// lines above (a budget approval in the reimbursement ceiling, a time
	// approval in the billable minutes); named here so the receipt shows the
	// decision behind the number, the way the task screen does.
	for _, rec := range taskExtensionRecords(ctx, taskID) {
		if rec.Status != extensionStatusApproved {
			continue
		}
		if rec.Kind == extensionKindBudget {
			r.Approvals = append(r.Approvals,
				fmt.Sprintf("Approved %s more budget", formatCentsUSD(derefInt(rec.RequestedCents))))
		} else {
			r.Approvals = append(r.Approvals,
				fmt.Sprintf("Approved %d more minutes", derefInt(rec.RequestedMinutes)))
		}
	}
	if applied > 0 {
		r.Lines = append(r.Lines, notify.ReceiptLine{
			Label:  fmt.Sprintf("Promo (%s)", promoCode),
			Amount: "−" + formatCentsUSD(applied),
		})
	}

	// The charges, each on the card it went to.
	var released int
	for _, ch := range charges {
		card, authorized := paymentCardAndAuthorized(ctx, ch.PaymentID)
		r.Charges = append(r.Charges, notify.ReceiptLine{
			Label:  chargeLabel(ch.Label, card),
			Amount: formatCentsUSD(ch.Cents),
		})
		if ch.Label == "Reserved amount" && authorized > ch.Cents {
			released = authorized - ch.Cents
		}
	}
	if released > 0 {
		r.Released = formatCentsUSD(released)
	}

	notifyUser(ctx, t.RequesterID, t.RequesterEmail, notify.CreateNotificationInput{
		TaskID:    taskID,
		Type:      "RECEIPT",
		Title:     fmt.Sprintf("Your HO:RA receipt — %s", formatCentsUSD(total)),
		Body:      receiptBody(t.Title, r, quote, applied, promoCode, charges),
		TaskTitle: t.Title,
		Receipt:   &r,
	})
}

// chargeLabel is "Reserved amount · Visa ••4242", or the label alone when the
// card is unknown — never "(unknown card)".
func chargeLabel(label, card string) string {
	if card == "" {
		return label
	}
	return label + " · " + card
}

// paymentCardAndAuthorized reads the display card and the authorized amount
// off one payments row.
func paymentCardAndAuthorized(ctx context.Context, paymentID string) (card string, authorized int) {
	var brand, last4 string
	if err := db.QueryRow(ctx, `
		select coalesce(card_brand,''), coalesce(card_last4,''), coalesce(authorized_cents, 0)
		  from public.payments where id = $1::uuid
	`, paymentID).Scan(&brand, &last4, &authorized); err != nil {
		return "", 0
	}
	if last4 == "" {
		return "", authorized
	}
	return cardDisplayLabel(brand) + " ••" + last4, authorized
}

// cardDisplayLabel is the same brand map the clients use, so the email and
// the app name the card the same way.
func cardDisplayLabel(brand string) string {
	switch strings.ToLower(brand) {
	case "visa":
		return "Visa"
	case "mastercard":
		return "Mastercard"
	case "amex":
		return "American Express"
	case "discover":
		return "Discover"
	case "diners":
		return "Diners Club"
	case "jcb":
		return "JCB"
	case "unionpay":
		return "UnionPay"
	default:
		return "Card"
	}
}

// receiptBody is the one-paragraph form for the in-app row and the push:
//
//	Charged $9.50 to Visa ••4242 for "Laundry run": $12.00 base + 10 min ×
//	$0.50 − $10.00 promo (WELCOME10). $7.50 of the hold released.
//
// Compact on purpose — the email carries the table.
func receiptBody(title string, r notify.Receipt, quote TaskQuote, promoApplied int, promoCode string, charges []receiptCharge) string {
	parts := []string{fmt.Sprintf("%s base", formatCentsUSD(quote.BaseFeeCents))}
	if quote.TimeCostCents > 0 {
		parts = append(parts, fmt.Sprintf("%d min × %s", quote.BillableMinutes, formatCentsUSD(quote.PerMinuteRateCents)))
	}
	if quote.ShoppingReceiptCents > 0 {
		parts = append(parts, fmt.Sprintf("%s receipt", formatCentsUSD(quote.ShoppingReceiptCents)))
	}
	breakdown := strings.Join(parts, " + ")
	if promoApplied > 0 {
		breakdown += fmt.Sprintf(" − %s promo (%s)", formatCentsUSD(promoApplied), promoCode)
	}

	card := ""
	if len(charges) > 0 {
		card, _ = paymentCardAndAuthorizedCached(charges[0].PaymentID)
	}
	to := ""
	if card != "" {
		to = " to " + card
	}
	// A settle-balance charge is for what an earlier completion could not
	// collect; the sentence says so rather than re-presenting the whole task
	// as if this charge were its price.
	if len(charges) == 1 && charges[0].Label == "Outstanding balance" {
		return fmt.Sprintf("Charged %s%s — the outstanding balance for %q (%s).", r.Total, to, title, breakdown)
	}
	body := fmt.Sprintf("Charged %s%s for %q: %s.", r.Total, to, title, breakdown)
	if len(charges) > 1 {
		var each []string
		for _, ch := range charges {
			each = append(each, fmt.Sprintf("%s %s", strings.ToLower(ch.Label), formatCentsUSD(ch.Cents)))
		}
		body += " Two charges: " + strings.Join(each, " and ") + "."
	}
	if r.Released != "" {
		body += fmt.Sprintf(" %s of the hold released.", r.Released)
	}
	return body
}

// paymentCardAndAuthorizedCached is paymentCardAndAuthorized on a background
// context — the body is built after the rows were already read once, and a
// second read for the card label must not depend on a request context that
// may be gone.
func paymentCardAndAuthorizedCached(paymentID string) (string, int) {
	return paymentCardAndAuthorized(context.Background(), paymentID)
}
