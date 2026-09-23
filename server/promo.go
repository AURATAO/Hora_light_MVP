package main

// Promo codes: requester-side fixed-amount discounts, absorbed by the platform.
//
// THE RULE, in one line: a code takes a fixed number of cents off what the
// REQUESTER pays and nothing off what the SUPPORTER is paid.
//
//	post        the hold is (estimate + budget) − discount, never below $0
//	settlement  the charge is (time + receipt) − discount, never below $0
//	payout      computed from the UNDISCOUNTED time + receipt; the part the
//	            discount took off the requester's charge is transferred from
//	            the platform's own balance (payments_payouts.go, promo_subsidy)
//
// WHERE THE DISCOUNT LIVES. Not on the task row: on a promo_redemptions row
// keyed by task, written inside the post transaction and carrying a snapshot
// of the code's amount at that moment. Every later read — the hold view, the
// settlement, the receipt — goes through promoDiscountForTask, so there is one
// place the number comes from and deactivating or editing a code afterwards
// cannot change the terms of a task already posted under it.
//
// WHAT "USED" MEANS. A redemption is written at post and deleted when the post
// comes to nothing: the hold was refused and the task discarded, or the task
// was cancelled free (nobody accepted it, or the grace window). Nobody was
// charged, so the code was not used and the requester may try it again. A
// cancel that charges keeps the redemption — the discount was applied to
// that charge.
//
// THE CHECKS, and where each one is actually enforced:
//
//	unknown / inactive        promo_invalid         lookup
//	outside its window        promo_expired         lookup (either edge)
//	max_redemptions reached   promo_exhausted       counted under a row lock
//	                                                 inside the post tx
//	already used by this user promo_used            UNIQUE (code, user) — the
//	                                                 handler's check is advisory
//	first_task_only           promo_not_first_task  any other task by the
//	                                                 requester that ever went
//	                                                 live (status ≠ pending_payment)
//
// Every read here is through database/sql (sqldb), not the pgx pool, because
// the one caller that must run inside a transaction — createTask — holds a
// *sql.Tx, and one implementation that takes either is how the pre-check and
// the in-transaction check cannot disagree.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
)

// PromoCode is one public.promo_codes row, as ops see it.
type PromoCode struct {
	ID             string     `json:"id"`
	Code           string     `json:"code"`
	AmountCents    int        `json:"amount_cents"`
	ValidFrom      *time.Time `json:"valid_from,omitempty"`
	ValidUntil     *time.Time `json:"valid_until,omitempty"`
	MaxRedemptions *int       `json:"max_redemptions,omitempty"`
	FirstTaskOnly  bool       `json:"first_task_only"`
	Active         bool       `json:"active"`
	Note           string     `json:"note,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	DeactivatedAt  *time.Time `json:"deactivated_at,omitempty"`
	// How many tasks have been posted under it (live redemptions).
	Redemptions int `json:"redemptions"`
}

// PromoError is a refused code: the code a client branches on, and the
// sentence it shows. Every message says what happened and what to do (S-33).
type PromoError struct {
	Code    string
	Message string
}

func (e *PromoError) Error() string { return e.Code + ": " + e.Message }

const (
	promoErrInvalid      = "promo_invalid"
	promoErrExpired      = "promo_expired"
	promoErrExhausted    = "promo_exhausted"
	promoErrUsed         = "promo_used"
	promoErrNotFirstTask = "promo_not_first_task"
)

var (
	errPromoInvalid = &PromoError{promoErrInvalid,
		"That promo code isn't valid. Check the spelling, or post without one."}
	errPromoNotYet = &PromoError{promoErrExpired,
		"That promo code isn't active yet. Try it again later, or post without one."}
	errPromoExpired = &PromoError{promoErrExpired,
		"That promo code has expired. Post without one."}
	errPromoExhausted = &PromoError{promoErrExhausted,
		"That promo code has been fully redeemed. Post without one."}
	errPromoUsed = &PromoError{promoErrUsed,
		"You've already used that promo code. Each code works once per account."}
	errPromoNotFirstTask = &PromoError{promoErrNotFirstTask,
		"That promo code is for a first task only, and you've posted before. Post without one."}
)

// stripeMinimumChargeCents is the smallest amount Stripe will authorize or
// charge in USD. A promo that takes a hold or a balance below it leaves an
// amount nobody can collect; see preAuthAfterPromoCents and settleTaskPayment.
const stripeMinimumChargeCents = 50

// rowQuerier is what the checks need: *sql.DB for a pre-check, *sql.Tx for the
// check inside the post transaction.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// normalizePromoCode is what a typed code becomes before it is looked up.
// Case is ignored by the index; whitespace is the user's keyboard.
func normalizePromoCode(raw string) string {
	return strings.TrimSpace(raw)
}

// lookupPromoCode reads a code, case-insensitively. Nil with no error when
// there is no such code.
func lookupPromoCode(ctx context.Context, q rowQuerier, code string) (*PromoCode, error) {
	code = normalizePromoCode(code)
	if code == "" {
		return nil, nil
	}
	var p PromoCode
	var note *string
	err := q.QueryRowContext(ctx, `
		select id::text, code, amount_cents, valid_from, valid_until, max_redemptions,
		       first_task_only, active, note, created_at, deactivated_at
		  from public.promo_codes
		 where lower(code) = lower($1)
	`, code).Scan(&p.ID, &p.Code, &p.AmountCents, &p.ValidFrom, &p.ValidUntil, &p.MaxRedemptions,
		&p.FirstTaskOnly, &p.Active, &note, &p.CreatedAt, &p.DeactivatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("promo: lookup %q: %w", code, err)
	}
	if note != nil {
		p.Note = *note
	}
	return &p, nil
}

// promoRedemptionCount is how many tasks stand posted under a code.
func promoRedemptionCount(ctx context.Context, q rowQuerier, promoID string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx,
		`select count(*) from public.promo_redemptions where promo_code_id = $1::uuid`, promoID).Scan(&n)
	return n, err
}

// checkPromoForUser validates a code for a requester about to post.
//
// userID may be empty (a brand-new account that has no users row yet): such a
// requester has redeemed nothing and posted nothing, so the per-user checks
// pass trivially. excludeTaskID is the task being posted, when the check runs
// after its row exists — it must not count as the requester's "previous task".
//
// Returns the code on success, a *PromoError the client can render on refusal,
// and a plain error only for a database failure.
func checkPromoForUser(ctx context.Context, q rowQuerier, code, userID, excludeTaskID string, now time.Time) (*PromoCode, error) {
	p, err := lookupPromoCode(ctx, q, code)
	if err != nil {
		return nil, err
	}
	if p == nil || !p.Active {
		return nil, errPromoInvalid
	}
	if p.ValidFrom != nil && now.Before(*p.ValidFrom) {
		return nil, errPromoNotYet
	}
	if p.ValidUntil != nil && !now.Before(*p.ValidUntil) {
		return nil, errPromoExpired
	}
	if p.MaxRedemptions != nil {
		n, err := promoRedemptionCount(ctx, q, p.ID)
		if err != nil {
			return nil, fmt.Errorf("promo: count redemptions of %s: %w", p.ID, err)
		}
		if n >= *p.MaxRedemptions {
			return nil, errPromoExhausted
		}
	}
	if userID == "" {
		return p, nil
	}

	// Advisory: the unique index is what actually enforces this. Checked here
	// so the ordinary case answers a sentence rather than a constraint error.
	var used bool
	if err := q.QueryRowContext(ctx, `
		select exists (
		  select 1 from public.promo_redemptions
		   where promo_code_id = $1::uuid and user_id = $2::uuid
		)
	`, p.ID, userID).Scan(&used); err != nil {
		return nil, fmt.Errorf("promo: check use of %s by %s: %w", p.ID, userID, err)
	}
	if used {
		return nil, errPromoUsed
	}

	if p.FirstTaskOnly {
		// "Posted before" means a task that ever went live. A pending_payment
		// row is a post that has not happened yet (a hold in flight or
		// refused), and this task itself, when its row already exists, is the
		// one being posted now.
		var posted bool
		if err := q.QueryRowContext(ctx, `
			select exists (
			  select 1 from public.tasks
			   where requester_id = $1::uuid
			     and status <> $2
			     and ($3 = '' or id <> $3::uuid)
			)
		`, userID, taskStatusPendingPayment, excludeTaskID).Scan(&posted); err != nil {
			return nil, fmt.Errorf("promo: first-task check for %s: %w", userID, err)
		}
		if posted {
			return nil, errPromoNotFirstTask
		}
	}
	return p, nil
}

// lockPromoCode takes the code's row lock for the rest of the transaction, so
// the max_redemptions count below it cannot be raced by a concurrent post
// under the same code. Only meaningful on a *sql.Tx; harmless on a *sql.DB.
func lockPromoCode(ctx context.Context, q rowQuerier, promoID string) error {
	var id string
	return q.QueryRowContext(ctx,
		`select id::text from public.promo_codes where id = $1::uuid for update`, promoID).Scan(&id)
}

// redeemPromo writes the redemption for a task, inside the post transaction.
//
// The whole per-user-once guarantee is the unique index this INSERT hits: a
// second post by the same requester under the same code loses here, whatever
// the advisory check said a moment earlier.
func redeemPromo(ctx context.Context, tx *sql.Tx, p *PromoCode, userID, taskID string) error {
	_, err := tx.ExecContext(ctx, `
		insert into public.promo_redemptions (promo_code_id, user_id, task_id, discount_cents)
		values ($1::uuid, $2::uuid, $3::uuid, $4)
	`, p.ID, userID, taskID, p.AmountCents)
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return errPromoUsed
	}
	return fmt.Errorf("promo: redeem %s for task %s: %w", p.ID, taskID, err)
}

// applyPromoInTx is the whole in-transaction sequence for a post: lock the
// code, re-run the checks against the locked state, write the redemption.
func applyPromoInTx(ctx context.Context, tx *sql.Tx, code, userID, taskID string) (*PromoCode, error) {
	p, err := lookupPromoCode(ctx, tx, code)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errPromoInvalid
	}
	if err := lockPromoCode(ctx, tx, p.ID); err != nil {
		return nil, fmt.Errorf("promo: lock %s: %w", p.ID, err)
	}
	if _, err := checkPromoForUser(ctx, tx, code, userID, taskID, time.Now()); err != nil {
		return nil, err
	}
	if err := redeemPromo(ctx, tx, p, userID, taskID); err != nil {
		return nil, err
	}
	return p, nil
}

// releasePromoRedemption gives the code back: the post came to nothing, so it
// was not used. Best-effort — a failure here costs somebody a second use of a
// code, not money — and logged either way.
func releasePromoRedemption(ctx context.Context, taskID, why string) {
	tag, err := db.Exec(ctx, `delete from public.promo_redemptions where task_id = $1::uuid`, taskID)
	if err != nil {
		log.Printf("[promo][ERROR] could not release redemption for task=%s (%s): %v", taskID, why, err)
		return
	}
	if tag.RowsAffected() > 0 {
		log.Printf("[promo] released redemption task=%s reason=%s", taskID, why)
	}
}

// promoDiscountForTask is the discount a task was posted under: the code and
// the snapshot amount, or "" and 0 for the overwhelming majority of tasks.
// THE one read every money path uses.
func promoDiscountForTask(ctx context.Context, taskID string) (code string, cents int) {
	err := db.QueryRow(ctx, `
		select c.code, r.discount_cents
		  from public.promo_redemptions r
		  join public.promo_codes c on c.id = r.promo_code_id
		 where r.task_id = $1::uuid
	`, taskID).Scan(&code, &cents)
	if err != nil {
		return "", 0
	}
	return code, cents
}

// promoApplied is how much of a discount actually comes off a total: all of
// it, or the whole total when the total is smaller. Never negative, and never
// more than what is being discounted — "never below $0" in one line.
func promoApplied(discountCents, totalCents int) int {
	if discountCents <= 0 || totalCents <= 0 {
		return 0
	}
	if discountCents > totalCents {
		return totalCents
	}
	return discountCents
}

// afterPromo is a total with its discount taken off, floored at zero.
func afterPromo(totalCents, discountCents int) int {
	return totalCents - promoApplied(discountCents, totalCents)
}

// ── POST /promo/validate ───────────────────────────────────────────────────

// validatePromoHandler answers the post form's "Apply". Advisory: POST /tasks
// re-checks inside its transaction, and the unique index has the last word.
func validatePromoHandler(c *gin.Context) {
	uid := c.GetString("uid")
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if err := c.BindJSON(&in); err != nil || normalizePromoCode(in.Code) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": promoErrInvalid, "message": errPromoInvalid.Message})
		return
	}
	p, err := checkPromoForUser(c.Request.Context(), sqldb, in.Code, uid, "", time.Now())
	if err != nil {
		writePromoError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":            p.Code,
		"amount_cents":    p.AmountCents,
		"first_task_only": p.FirstTaskOnly,
		"message":         fmt.Sprintf("%s off with %s.", formatCentsUSD(p.AmountCents), p.Code),
	})
}

// writePromoError renders a refused code as a 400 the client can show in
// place, and anything else as a 500 with the detail kept to the log (S-33).
func writePromoError(c *gin.Context, err error) {
	var pe *PromoError
	if errors.As(err, &pe) {
		c.JSON(http.StatusBadRequest, gin.H{"error": pe.Code, "message": pe.Message})
		return
	}
	log.Printf("[promo][ERROR] %v", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
}

// ── Ops: /admin/promo-codes ────────────────────────────────────────────────
//
// Endpoints rather than a database console, so creating a code is audited and
// validated the same way every other admin action is (requireOpsAdmin, S-14).
// The web ops panel renders these on a Promo codes tab.

// GET /admin/promo-codes — every code, newest first, with its redemption count.
func adminListPromoCodes(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := db.Query(ctx, `
		select c.id::text, c.code, c.amount_cents, c.valid_from, c.valid_until, c.max_redemptions,
		       c.first_task_only, c.active, coalesce(c.note,''), c.created_at, c.deactivated_at,
		       (select count(*) from public.promo_redemptions r where r.promo_code_id = c.id)
		  from public.promo_codes c
		 order by c.created_at desc
	`)
	if err != nil {
		log.Printf("[admin.promo][list] %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()
	out := []PromoCode{}
	for rows.Next() {
		var p PromoCode
		if err := rows.Scan(&p.ID, &p.Code, &p.AmountCents, &p.ValidFrom, &p.ValidUntil, &p.MaxRedemptions,
			&p.FirstTaskOnly, &p.Active, &p.Note, &p.CreatedAt, &p.DeactivatedAt, &p.Redemptions); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "scan error"})
			return
		}
		out = append(out, p)
	}
	c.JSON(http.StatusOK, out)
}

// POST /admin/promo-codes
//
//	{ "code": "WELCOME10", "amount_cents": 1000, "valid_from": …, "valid_until": …,
//	  "max_redemptions": 100, "first_task_only": true, "note": "…" }
func adminCreatePromoCode(c *gin.Context) {
	actorUID := c.GetString("uid")
	var in struct {
		Code           string     `json:"code"`
		AmountCents    int        `json:"amount_cents"`
		ValidFrom      *time.Time `json:"valid_from"`
		ValidUntil     *time.Time `json:"valid_until"`
		MaxRedemptions *int       `json:"max_redemptions"`
		FirstTaskOnly  bool       `json:"first_task_only"`
		Note           string     `json:"note"`
	}
	if err := c.BindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	in.Code = normalizePromoCode(in.Code)
	switch {
	case in.Code == "" || len(in.Code) > 40:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_code", "message": "A code is 1–40 characters."})
		return
	case in.AmountCents <= 0:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_amount", "message": "The discount has to be more than $0."})
		return
	case in.MaxRedemptions != nil && *in.MaxRedemptions <= 0:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_limit", "message": "Max redemptions has to be at least 1, or blank for unlimited."})
		return
	case in.ValidFrom != nil && in.ValidUntil != nil && !in.ValidUntil.After(*in.ValidFrom):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_window", "message": "Valid until has to be after valid from."})
		return
	}

	ctx := c.Request.Context()
	var p PromoCode
	var note *string
	err := db.QueryRow(ctx, `
		insert into public.promo_codes
		       (code, amount_cents, valid_from, valid_until, max_redemptions, first_task_only, note, created_by)
		values ($1, $2, $3, $4, $5, $6, nullif($7,''), $8::uuid)
		returning id::text, code, amount_cents, valid_from, valid_until, max_redemptions,
		          first_task_only, active, note, created_at, deactivated_at
	`, in.Code, in.AmountCents, in.ValidFrom, in.ValidUntil, in.MaxRedemptions, in.FirstTaskOnly,
		strings.TrimSpace(in.Note), actorUID,
	).Scan(&p.ID, &p.Code, &p.AmountCents, &p.ValidFrom, &p.ValidUntil, &p.MaxRedemptions,
		&p.FirstTaskOnly, &p.Active, &note, &p.CreatedAt, &p.DeactivatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			c.JSON(http.StatusConflict, gin.H{"error": "code_exists", "message": "A code with that name already exists (codes are case-insensitive)."})
			return
		}
		log.Printf("[admin.promo][create] %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if note != nil {
		p.Note = *note
	}
	writeAudit(ctx, systemActorUID, actorUID, "PROMO_CODE_CREATED", in.Note, map[string]any{
		"promo_code_id":   p.ID,
		"code":            p.Code,
		"amount_cents":    p.AmountCents,
		"first_task_only": p.FirstTaskOnly,
		"max_redemptions": in.MaxRedemptions,
	})
	log.Printf("[admin.promo] created code=%s amount=%s by=%s", p.Code, formatCentsUSD(p.AmountCents), c.GetString("email"))
	c.JSON(http.StatusCreated, p)
}

// POST /admin/promo-codes/:id/deactivate — no new redemptions from now on.
// Tasks already posted under the code keep their snapshot discount.
func adminDeactivatePromoCode(c *gin.Context) {
	actorUID := c.GetString("uid")
	ctx := c.Request.Context()
	var code string
	err := db.QueryRow(ctx, `
		update public.promo_codes
		   set active = false, deactivated_at = coalesce(deactivated_at, now())
		 where id = $1::uuid
		 returning code
	`, strings.TrimSpace(c.Param("id"))).Scan(&code)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	writeAudit(ctx, systemActorUID, actorUID, "PROMO_CODE_DEACTIVATED", "", map[string]any{
		"promo_code_id": c.Param("id"), "code": code,
	})
	log.Printf("[admin.promo] deactivated code=%s by=%s", code, c.GetString("email"))
	c.JSON(http.StatusOK, gin.H{"ok": true, "code": code, "active": false})
}
