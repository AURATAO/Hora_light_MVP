package main

// The LAST leg of a supporter's money: connected-account balance → bank.
//
// A payouts row goes 'paid' when Stripe accepts the Transfer — money has left
// the platform and landed in the supporter's connected-account balance. Their
// bank sees it later, when Stripe's payout schedule sweeps that balance in a
// connected-account Payout (po_…). This file is how our rows learn about that
// second hop, so the Earnings screen can say "$X on its way to your bank ·
// $Y paid out" with two real numbers instead of one hedge.
//
// THE JOIN. A Transfer creates a `destination_payment` (py_…) on the
// connected account. A Payout's balance transactions of type `payment` name
// that py_ as their `source`. So: record the py_ on our row when the transfer
// is sent, and when payout.paid arrives, list that payout's balance
// transactions on the connected account and stamp every row whose py_ is
// among them. Rows written before the column existed get their py_ filled in
// from the Transfer on first contact.
//
// STRIPE CALLS BEHIND VARIABLES, so the webhook and the reconciler can be
// tested against real rows without a network: the tests swap the two
// functions for fixtures. Production never reassigns them.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/balancetransaction"
	"github.com/stripe/stripe-go/v86/payout"
	"github.com/stripe/stripe-go/v86/transfer"

	"hora-auth/internal/notify"
)

// payoutDestinationPayments lists the py_… ids a connected-account payout
// carried: its balance transactions of type `payment`, by source.
var payoutDestinationPayments = func(ctx context.Context, accountID, payoutID string) ([]string, error) {
	params := &stripe.BalanceTransactionListParams{
		Payout: stripe.String(payoutID),
		Type:   stripe.String("payment"),
	}
	params.Context = ctx
	params.SetStripeAccount(accountID)
	params.Limit = stripe.Int64(100)
	var out []string
	it := balancetransaction.List(params)
	for it.Next() {
		bt := it.BalanceTransaction()
		if bt.Source != nil && bt.Source.ID != "" {
			out = append(out, bt.Source.ID)
		}
	}
	if err := it.Err(); err != nil {
		return nil, fmt.Errorf("list balance transactions for payout %s on %s: %w", payoutID, accountID, err)
	}
	return out, nil
}

// transferDestinationPayment reads the py_… a Transfer created on its
// destination account, for rows written before that was recorded at send time.
var transferDestinationPayment = func(ctx context.Context, transferID string) (string, error) {
	params := &stripe.TransferParams{}
	params.Context = ctx
	tr, err := transfer.Get(transferID, params)
	if err != nil {
		return "", fmt.Errorf("get transfer %s: %w", transferID, err)
	}
	if tr.DestinationPayment == nil {
		return "", nil
	}
	return tr.DestinationPayment.ID, nil
}

// destinationPaymentOf is what sendTransfer's caller records: the py_ on the
// Transfer it just got back, or "" when Stripe did not include one.
func destinationPaymentOf(tr *stripe.Transfer) string {
	if tr == nil || tr.DestinationPayment == nil {
		return ""
	}
	return tr.DestinationPayment.ID
}

// backfillDestinationPayments fills stripe_destination_payment on this
// supporter's paid rows that predate the column, from the Transfer. One call
// per such row, once; after this they carry the key like every newer row.
func backfillDestinationPayments(ctx context.Context, uid string) error {
	rows, err := db.Query(ctx, `
		select id::text, stripe_transfer_id from public.payouts
		 where supporter_id = $1::uuid and status = $2
		   and stripe_transfer_id is not null and stripe_destination_payment is null
	`, uid, payoutStatusPaid)
	if err != nil {
		return err
	}
	type pending struct{ id, transferID string }
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.transferID); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, p)
	}
	rows.Close()
	for _, p := range todo {
		py, err := transferDestinationPayment(ctx, p.transferID)
		if err != nil {
			return err
		}
		if py == "" {
			continue
		}
		if _, err := db.Exec(ctx, `
			update public.payouts set stripe_destination_payment = $2, updated_at = now()
			 where id = $1::uuid and stripe_destination_payment is null
		`, p.id, py); err != nil {
			return err
		}
	}
	return nil
}

// markPayoutsArrived stamps every paid row of this supporter whose py_ the
// bank payout carried. Returns how many rows it stamped.
//
// Only 'paid' rows and only once (bank_paid_at is null): a failed row is
// never "arrived" whatever Stripe's balance says, because a failed row is one
// where OUR transfer did not happen.
func markPayoutsArrived(ctx context.Context, uid, bankPayoutID string, arrivedAt time.Time, destinationPayments []string) (int, error) {
	if len(destinationPayments) == 0 {
		return 0, nil
	}
	tag, err := db.Exec(ctx, `
		update public.payouts
		   set stripe_bank_payout_id = $2, bank_paid_at = $3, updated_at = now()
		 where supporter_id = $1::uuid
		   and status = $4
		   and bank_paid_at is null
		   and stripe_destination_payment = any($5::text[])
	`, uid, bankPayoutID, arrivedAt, payoutStatusPaid, destinationPayments)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// reconcileBankPayout is the unit of work: one connected-account payout,
// stamped onto the rows it carried.
func reconcileBankPayout(ctx context.Context, uid, accountID, bankPayoutID string, arrivedAt time.Time) (int, error) {
	if err := backfillDestinationPayments(ctx, uid); err != nil {
		return 0, fmt.Errorf("backfill destination payments for %s: %w", uid, err)
	}
	ids, err := payoutDestinationPayments(ctx, accountID, bankPayoutID)
	if err != nil {
		return 0, err
	}
	return markPayoutsArrived(ctx, uid, bankPayoutID, arrivedAt, ids)
}

// reconcileBankPayouts walks every PAID connected-account payout for this
// supporter and stamps what each carried. The catch-up path: for rows that
// arrived before payout.paid was subscribed, and for an operator who wants to
// be sure. Idempotent — a stamped row is skipped by markPayoutsArrived.
func reconcileBankPayouts(ctx context.Context, uid, accountID string) (int, error) {
	// Fill the join key on older rows even when no payout has landed yet, so
	// the first payout.paid can match them without a second Transfer read.
	if err := backfillDestinationPayments(ctx, uid); err != nil {
		return 0, fmt.Errorf("backfill destination payments for %s: %w", uid, err)
	}
	params := &stripe.PayoutListParams{Status: stripe.String(string(stripe.PayoutStatusPaid))}
	params.Context = ctx
	params.SetStripeAccount(accountID)
	params.Limit = stripe.Int64(100)
	total := 0
	it := payout.List(params)
	for it.Next() {
		po := it.Payout()
		arrived := time.Unix(po.ArrivalDate, 0)
		if po.ArrivalDate == 0 {
			arrived = time.Unix(po.Created, 0)
		}
		n, err := reconcileBankPayout(ctx, uid, accountID, po.ID, arrived)
		if err != nil {
			return total, err
		}
		total += n
	}
	if err := it.Err(); err != nil {
		return total, fmt.Errorf("list payouts on %s: %w", accountID, err)
	}
	return total, nil
}

// ── payout.paid ────────────────────────────────────────────────────────────

// onConnectedPayoutPaid is the bank saying yes. CONNECTED-ACCOUNT SCOPED: it
// arrives on the same endpoint as account.updated and payout.failed, and the
// endpoint has to be subscribed to it in the dashboard (see the deploy notes).
//
// A Stripe error here returns an error so Stripe retries: the rows stay
// "on its way", which is the truthful state until the join can be made.
func onConnectedPayoutPaid(ctx context.Context, event *stripe.Event) error {
	var po stripe.Payout
	if err := json.Unmarshal(event.Data.Raw, &po); err != nil {
		return fmt.Errorf("decode payout: %w", err)
	}
	accountID := event.Account
	if accountID == "" || po.ID == "" {
		log.Printf("[stripe][webhook] payout.paid with no account/payout id — ignoring")
		return nil
	}
	var uid string
	if err := db.QueryRow(ctx,
		`select id::text from public.users where stripe_account_id = $1`, accountID,
	).Scan(&uid); err != nil {
		log.Printf("[stripe][webhook] no user for connect account=%s — ignoring payout.paid", accountID)
		return nil
	}
	arrived := time.Unix(po.ArrivalDate, 0)
	if po.ArrivalDate == 0 {
		arrived = time.Now()
	}
	n, err := reconcileBankPayout(ctx, uid, accountID, po.ID, arrived)
	if err != nil {
		return err
	}
	log.Printf("[stripe][webhook] bank payout paid account=%s user=%s payout=%s amount=%d rows_arrived=%d",
		accountID, uid, po.ID, po.Amount, n)
	// The deposit push: once per Stripe payout, and only from this path. The
	// catch-up reconciler (reconcileBankPayouts, from the Earnings screen)
	// stamps the same rows without saying so — a push about a deposit that
	// landed weeks ago is noise, and the screen it runs from already shows
	// the rows as paid. n == 0 means every row this payout carried was
	// already stamped, which is how a second event for the same payout —
	// a different event id, so not caught by the dedupe table — stays silent.
	if n > 0 {
		notifySupporterDeposited(ctx, uid, po.ID, int(po.Amount))
	}
	return nil
}

// notifySupporterDeposited is the bank saying yes, in the supporter's own
// notification feed and as a push: "$37.00 has been deposited to your bank.",
// with the tasks it covered underneath.
//
// One row per Stripe payout, filed against the most recent task it carried
// (notifications.task_id is NOT NULL), so tapping it lands on a task whose
// settlement card reads "paid". No email: the push is the announcement, the
// Earnings screen is the record, and the bank statement is the proof.
//
// The amount is Stripe's — what the bank actually received — with the sum of
// our rows as the fallback for a payload without one.
func notifySupporterDeposited(ctx context.Context, uid, bankPayoutID string, depositCents int) {
	rows, err := db.Query(ctx, `
		select po.task_id::text, coalesce(t.title,''), po.amount_cents
		  from public.payouts po
		  join public.tasks t on t.id = po.task_id
		 where po.supporter_id = $1::uuid and po.stripe_bank_payout_id = $2
		 order by po.bank_paid_at desc, po.created_at desc
	`, uid, bankPayoutID)
	if err != nil {
		log.Printf("[payments][deposit] rows for payout=%s: %v", bankPayoutID, err)
		return
	}
	defer rows.Close()
	var firstTask string
	var titles []string
	seen := map[string]bool{}
	sum := 0
	for rows.Next() {
		var taskID, title string
		var cents int
		if err := rows.Scan(&taskID, &title, &cents); err != nil {
			return
		}
		if firstTask == "" {
			firstTask = taskID
		}
		sum += cents
		if !seen[taskID] {
			seen[taskID] = true
			titles = append(titles, fmt.Sprintf("%q", title))
		}
	}
	if firstTask == "" {
		return
	}
	if depositCents <= 0 {
		depositCents = sum
	}
	var email string
	_ = db.QueryRow(ctx, `select coalesce(email,'') from public.users where id = $1::uuid`, uid).Scan(&email)

	in := notify.CreateNotificationInput{
		DB:     sqldb,
		UserID: uid,
		TaskID: firstTask,
		Type:   "PAYOUT_DEPOSITED",
		Title:  fmt.Sprintf("%s has been deposited to your bank.", formatCentsUSD(depositCents)),
		Body:   "Covers " + joinWithAnd(titles) + ".",
		// Push and the in-app row only — see above.
		SendEmail: false,
		EmailTo:   email,
	}
	if err := notify.Create(ctx, in); err != nil {
		log.Printf("[notify][ERROR] PAYOUT_DEPOSITED user=%s: %v", uid, err)
	}
}

// joinWithAnd renders a list the way a sentence does: "a", "a and b",
// "a, b and c".
func joinWithAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
	}
}
