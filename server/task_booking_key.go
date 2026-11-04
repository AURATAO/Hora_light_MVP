package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// A text booking's idempotency (audit 2026-10-30, gap B.3).
//
// Before: a YES retried after a Stripe timeout ran createTask afresh — a new
// task, a new payments row, and so a new preauth_<payment id> key. If the
// first call had placed its hold before the timeout, the retry placed a
// second one, and the first task had already been discarded with its hold
// still on the card.
//
// Now each drafted errand carries one key for its whole life (channel
// DraftItem.BookingKey), and createTask, given it, looks for the task that
// key already made before making one:
//
//   - posted (open or later): that task is the answer — 201 with its id,
//     nothing new written, nothing asked of Stripe;
//   - parked in pending_payment after an UNCERTAIN hold (a timeout): the
//     same payments row is tried again (ResumePreAuth), so Stripe sees the
//     same idempotency key and parameters and answers with the first
//     attempt's intent — the one hold, never a second;
//   - parked behind a 3DS challenge: answered as a 3DS post again;
//   - parked with no live payments row (the hold was never attempted, or
//     was refused and the discard did not finish): no hold can exist under
//     it, so it is discarded and the post goes ahead as new.
//
// The chat's key reaches createTask in the gin context (channel_booker.go).
// The web form sends its own in the body (booking_key, one per form; fix
// 2026-11-04): it is stored as "web:<key>", so it can never name a chat
// key, and the lookup is by requester, so it can only ever find the
// requester's own task.

// msgPaymentsUncertain: what the person is told when a hold's outcome is
// unknown. The retry is safe: it repeats the same Stripe call.
const msgPaymentsUncertain = "Something went wrong, please try again."

// clientBookingKeyRE: a key a client may name — a UUID or similar token.
var clientBookingKeyRE = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

// clientBookingKey namespaces a key sent in the POST /tasks body.
func clientBookingKey(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if !clientBookingKeyRE.MatchString(raw) {
		return "", false
	}
	return "web:" + raw, true
}

const (
	// ctxBookingKey: the errand's idempotency key (tasks.booking_key).
	ctxBookingKey = "booking_key"
	// ctxMaxQuoteCents: the estimate the person said YES to. createTask
	// refuses (409 quote_changed) a post that would price above it — the
	// rate is resolved at the post, so a surge window opening between the
	// summary and the YES would otherwise raise the price unasked (gap A.3).
	ctxMaxQuoteCents = "max_quote_cents"
)

// resumeBooking answers a post whose booking key already made a task.
// false: no such task (or a dead one cleared away) — post as new.
func resumeBooking(c *gin.Context, ctx context.Context, email, key string) bool {
	var taskID, status, requesterID string
	err := sqldb.QueryRowContext(ctx, `
		select t.id::text, t.status, t.requester_id::text
		  from public.tasks t join public.users u on u.id = t.requester_id
		 where u.email = $1 and t.booking_key = $2`, email, key).Scan(&taskID, &status, &requesterID)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		log.Printf("[createTask] booking key lookup: %v", err)
		c.JSON(500, gin.H{"error": "db error"})
		return true
	}
	if status != taskStatusPendingPayment {
		log.Printf("[createTask] booking key replay task=%s status=%s — no new task", taskID, status)
		c.JSON(http.StatusCreated, gin.H{"id": taskID, "status": status, "replayed": true, "payment": taskPaymentView(ctx, taskID)})
		return true
	}

	p, err := livePaymentForTask(ctx, taskID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// No live payments row: Stripe was never asked under this task, or
		// refused — nothing can be held. Cleared, and posted as new.
		log.Printf("[createTask] booking key task=%s parked with no live payment — discarding, posting afresh", taskID)
		discardUnpaidTask(ctx, taskID)
		return false
	case err != nil:
		log.Printf("[createTask] booking key task=%s payment lookup: %v", taskID, err)
		c.JSON(500, gin.H{"error": "db error"})
		return true
	case p.Status == paymentStatusAuthorized:
		// Held, but the post never finished (a crash between the two writes).
		return postHeldTask(c, ctx, taskID, p.ID)
	case p.StripePaymentIntentID != "":
		// A 3DS challenge is waiting on this task's intent.
		c.JSON(http.StatusPaymentRequired, gin.H{
			"error":   "payment_authentication_required",
			"message": "Your bank needs to confirm this payment. Approve it to post your task.",
			"task_id": taskID,
		})
		return true
	}

	// Uncertain: the first call's outcome is unknown. The same call again.
	var in PreAuthInput
	if err := sqldb.QueryRowContext(ctx, `
		select category, estimated_minutes, coalesce(prepay_amount_cents, 0), coalesce(rate_cents_per_min, 0)
		  from public.tasks where id = $1::uuid`, taskID).
		Scan(&in.Category, &in.EstimatedMinutes, &in.ShoppingBudgetCents, &in.RateCents); err != nil {
		log.Printf("[createTask] booking key task=%s load: %v", taskID, err)
		c.JSON(500, gin.H{"error": "db error"})
		return true
	}
	in.TaskID, in.RequesterID = taskID, requesterID
	payCtx, err := resolvePreAuthContext(ctx, requesterID, email)
	if err != nil {
		// Without the card the first call used, it cannot be repeated as the
		// same call; the task is kept (a hold may exist) for a later retry.
		log.Printf("[createTask][ERROR] booking key task=%s: uncertain hold, card context unavailable: %v", taskID, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "payments_uncertain", "message": msgPaymentsUncertain})
		return true
	}
	in.StripeCustomerID, in.StripePaymentMethodID = payCtx.CustomerID, payCtx.PaymentMethodID
	held, err := ResumePreAuth(ctx, p, in)
	var pe *PreAuthError
	switch {
	case err == nil && held.Status == paymentStatusAuthorized:
		return postHeldTask(c, ctx, taskID, held.ID)
	case errors.As(err, &pe) && pe.RequiresAction:
		c.JSON(http.StatusPaymentRequired, gin.H{
			"error":             "payment_authentication_required",
			"message":           pe.Message,
			"client_secret":     pe.ClientSecret,
			"payment_intent_id": pe.PaymentIntentID,
			"task_id":           taskID,
			"publishable_key":   publishableKeyForUser(ctx, requesterID),
		})
		return true
	case errors.As(err, &pe) && pe.Uncertain:
		log.Printf("[createTask] booking key task=%s: still uncertain, kept: %v", taskID, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "payments_uncertain", "message": msgPaymentsUncertain})
		return true
	default:
		message := "We couldn't place a hold on your card. Try another card."
		if pe != nil {
			message = pe.Message
		}
		if err != nil {
			log.Printf("[createTask] booking key task=%s: resumed pre-auth refused: %v", taskID, err)
		} else {
			log.Printf("[createTask] booking key task=%s: resumed pre-auth came back %q", taskID, held.Status)
		}
		discardUnpaidTask(ctx, taskID)
		c.JSON(http.StatusPaymentRequired, gin.H{"error": "payment_required", "message": message})
		return true
	}
}

// postHeldTask posts a parked task whose hold is in place.
func postHeldTask(c *gin.Context, ctx context.Context, taskID, paymentID string) bool {
	if err := promoteTaskToOpen(ctx, taskID, paymentID); err != nil {
		// The hold stays on the row and the task stays parked: the next retry
		// with the key lands here again.
		log.Printf("[createTask][ERROR] booking key task=%s held by %s but not posted: %v", taskID, paymentID, err)
		c.JSON(500, gin.H{"error": "db error"})
		return true
	}
	log.Printf("[createTask] booking key task=%s posted on its first attempt's hold %s", taskID, paymentID)
	announceNewTask(ctx, taskID)
	c.JSON(http.StatusCreated, gin.H{"id": taskID, "status": "open", "payment": taskPaymentView(ctx, taskID)})
	return true
}

// setBookingKeyTx records the key on the task just inserted, in the post's
// own transaction. A unique violation is a second post with the key racing
// the first (the chat's claim prevents it; this is the database saying so).
func setBookingKeyTx(ctx context.Context, tx *sql.Tx, taskID, key string) (conflict bool, err error) {
	_, err = tx.ExecContext(ctx, `update public.tasks set booking_key = $2 where id = $1::uuid`, taskID, key)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return true, err
	}
	return false, err
}
