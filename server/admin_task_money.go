package main

// The ops view of one task's money.
//
// Until this existed the only way to answer "what did the supporter spend,
// where is the receipt, and have they been paid back" was a SQL session
// against tasks, payments and payouts — which meant one person could answer
// it. The ops feed now carries the at-a-glance columns (ops/ops_routes.go:
// receipt amount, whether there is a photo, the transfer state); this is the
// detail behind them, fetched per task because the receipt photo needs a
// signed link and a signed link is minted for the person about to look at it,
// not for all 500 rows of a feed.
//
// READ ONLY. Nothing here moves money or changes a row. Retrying a failed
// transfer stays POST /admin/payouts/:id/retry.
//
// No new numbers either (S-05): every amount is read from the row that the
// settlement wrote. The one derived value is the state label, and it is the
// same rule the supporter's Earnings screen uses — "on its way" until the bank
// leg is confirmed, "paid out" after.

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// How long a receipt link minted for the ops panel lives. Long enough to look
// at it and compare it with the amount; short enough that a link pasted into a
// chat is dead by the afternoon.
const opsReceiptLinkTTL = 15 * time.Minute

type adminMoneyPayment struct {
	Kind            string    `json:"kind"`
	Status          string    `json:"status"`
	AuthorizedCents *int      `json:"authorized_cents"`
	CapturedCents   *int      `json:"captured_cents"`
	CreatedAt       time.Time `json:"created_at"`
}

type adminMoneyPayout struct {
	ID                 string `json:"id"`
	AmountCents        int    `json:"amount_cents"`
	ServiceCents       *int   `json:"service_cents"`
	FeeCents           *int   `json:"fee_cents"`
	ReimbursementCents *int   `json:"reimbursement_cents"`
	Funding            string `json:"funding"`
	Status             string `json:"status"`
	// none | pending | on_its_way | paid_out | failed
	State      string     `json:"state"`
	CreatedAt  time.Time  `json:"created_at"`
	BankPaidAt *time.Time `json:"bank_paid_at"`
	// The Stripe transfer id — what ops paste into the Stripe dashboard's
	// search box. An identifier, not a credential.
	StripeTransferID string `json:"stripe_transfer_id"`
}

// payoutRowState names where one transfer stands, in the words the supporter
// sees for the same row.
func payoutRowState(status string, bankPaidAt *time.Time) string {
	switch status {
	case payoutStatusFailed:
		return "failed"
	case payoutStatusPending:
		return "pending"
	case payoutStatusPaid:
		if bankPaidAt != nil {
			return "paid_out"
		}
		return "on_its_way"
	}
	return status
}

// payoutOverallState is the worst state across a task's transfers: one failed
// transfer must never be hidden behind a paid one.
func payoutOverallState(states []string) string {
	if len(states) == 0 {
		return "none"
	}
	rank := map[string]int{"paid_out": 0, "on_its_way": 1, "pending": 2, "failed": 3}
	worst := states[0]
	for _, s := range states[1:] {
		if rank[s] > rank[worst] {
			worst = s
		}
	}
	return worst
}

// GET /admin/tasks/:id/money
func adminTaskMoney(c *gin.Context) {
	taskID := c.Param("id")
	ctx := c.Request.Context()

	var (
		title                     string
		status                    string
		budgetCents, receiptCents *int
		receiptPhoto              *string
		settledTotal, settledTime *int
		settledAt                 *time.Time
		supporterEmail            *string
	)
	if err := db.QueryRow(ctx, `
		select coalesce(title,''), status,
		       shopping_budget_approved_cents, receipt_amount_cents, receipt_photo_url,
		       settled_total_cents, settled_time_cost_cents, settled_at,
		       nullif(assigned_to,'')
		from public.tasks where id = $1::uuid
	`, taskID).Scan(&title, &status, &budgetCents, &receiptCents, &receiptPhoto,
		&settledTotal, &settledTime, &settledAt, &supporterEmail); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	payments := []adminMoneyPayment{}
	prow, err := db.Query(ctx, `
		select kind, status, authorized_cents, captured_cents, created_at
		from public.payments where task_id = $1::uuid
		order by created_at asc
	`, taskID)
	if err != nil {
		log.Printf("[admin.money][payments] task=%s err=%v", taskID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	for prow.Next() {
		var p adminMoneyPayment
		if err := prow.Scan(&p.Kind, &p.Status, &p.AuthorizedCents, &p.CapturedCents, &p.CreatedAt); err != nil {
			prow.Close()
			log.Printf("[admin.money][payments.scan] task=%s err=%v", taskID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		payments = append(payments, p)
	}
	prow.Close()

	payouts := []adminMoneyPayout{}
	states := []string{}
	orow, err := db.Query(ctx, `
		select id::text, amount_cents, service_cents, fee_cents, reimbursement_cents,
		       funding, status, created_at, bank_paid_at, coalesce(stripe_transfer_id,'')
		from public.payouts where task_id = $1::uuid
		order by created_at asc
	`, taskID)
	if err != nil {
		log.Printf("[admin.money][payouts] task=%s err=%v", taskID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	for orow.Next() {
		var p adminMoneyPayout
		if err := orow.Scan(&p.ID, &p.AmountCents, &p.ServiceCents, &p.FeeCents, &p.ReimbursementCents,
			&p.Funding, &p.Status, &p.CreatedAt, &p.BankPaidAt, &p.StripeTransferID); err != nil {
			orow.Close()
			log.Printf("[admin.money][payouts.scan] task=%s err=%v", taskID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		p.State = payoutRowState(p.Status, p.BankPaidAt)
		states = append(states, p.State)
		payouts = append(payouts, p)
	}
	orow.Close()

	photoURL := ""
	if receiptPhoto != nil && *receiptPhoto != "" {
		photoURL = signTaskPhotoURL(*receiptPhoto, opsReceiptLinkTTL)
	}

	c.JSON(http.StatusOK, gin.H{
		"task_id":                 taskID,
		"title":                   title,
		"status":                  status,
		"supporter_email":         supporterEmail,
		"budget_cents":            budgetCents,
		"receipt_cents":           receiptCents,
		"receipt_photo_url":       photoURL,
		"settled_total_cents":     settledTotal,
		"settled_time_cost_cents": settledTime,
		"settled_at":              settledAt,
		"payments":                payments,
		"payouts":                 payouts,
		"payout_state":            payoutOverallState(states),
	})
}
