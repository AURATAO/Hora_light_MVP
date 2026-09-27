package main

// In-app account deletion (App Store Guideline 5.1.1(v)).
//
// WHAT IS DELETED, WHAT IS KEPT, AND WHY. The users row itself survives,
// anonymised: payments, payouts, tasks, worklogs, reviews and safety records
// all point at it by uuid, and the money rows are records the platform must
// keep (Stripe disputes run 120 days; tax records longer). Everything that
// identifies the person is removed or replaced:
//
//   users        email → deleted-<8 hex>@deleted.invalid, name/picture cleared,
//                google_sub / supabase_sub cleared (the account can never be
//                signed into again; a fresh sign-in with the same email makes
//                a brand-new account)
//   profiles     name, phone, city, bio, avatar cleared; supporter status
//                withdrawn; email replaced with the same placeholder
//   tasks.requester / tasks.assigned_to / worklogs."user"
//                the legacy email columns (S-60.1/2) → the placeholder, so the
//                task history reads "Deleted user" and joins keep working
//   task_gps_pings, device_push_tokens, notifications
//                deleted outright — location, a push address, and messages
//                addressed to the person
//   Stripe       the Customer is deleted (this detaches every saved card);
//                the Connect account is kept, because Stripe owns that KYC
//                record and it is what past transfers reconcile against
//   Supabase     the auth user is deleted via the admin API, best-effort
//
// Retained, deliberately: task rows and their money (anonymised), reviews the
// person wrote about supporters (the supporter's rating is theirs), and
// user_reports / user_blocks (a block must outlive the account that made it —
// otherwise deleting and re-registering would be a way around it).
//
// REFUSED, WITH A REASON, rather than orphaning money: an open task on either
// side, an outstanding balance, or a payout that has not reached the bank.
// The person is told exactly which, and what clears it.
//
// The whole thing is one transaction for the database half; the Stripe and
// Supabase calls come after the commit and are best-effort, logged, and
// recorded in the audit row so an operator can finish by hand if one failed.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/customer"
)

// deletionBlocker is one reason the account cannot be deleted yet.
type deletionBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   int    `json:"count,omitempty"`
}

// accountDeletionBlockers lists what stands in the way. Empty means go.
func accountDeletionBlockers(ctx context.Context, uid string) ([]deletionBlocker, error) {
	var out []deletionBlocker

	var openAsRequester, openAsSupporter int
	if err := db.QueryRow(ctx, `
		select count(*) filter (where requester_id = $1::uuid),
		       count(*) filter (where assigned_to_id = $1::uuid)
		  from public.tasks where status = 'open'
	`, uid).Scan(&openAsRequester, &openAsSupporter); err != nil {
		return nil, fmt.Errorf("count open tasks: %w", err)
	}
	if openAsRequester > 0 {
		out = append(out, deletionBlocker{
			Code:  "open_tasks_posted",
			Count: openAsRequester,
			Message: fmt.Sprintf("You have %d open task%s. Cancel or complete %s first.",
				openAsRequester, plural(openAsRequester), itThem(openAsRequester)),
		})
	}
	if openAsSupporter > 0 {
		out = append(out, deletionBlocker{
			Code:  "open_tasks_accepted",
			Count: openAsSupporter,
			Message: fmt.Sprintf("You're the supporter on %d task%s still in progress. Finish %s first.",
				openAsSupporter, plural(openAsSupporter), itThem(openAsSupporter)),
		})
	}

	if owed := outstandingBalanceFor(ctx, uid); owed != nil {
		out = append(out, deletionBlocker{
			Code: "outstanding_balance",
			Message: fmt.Sprintf("You have an outstanding balance of %s from %q. Settle it first (Profile → Payment methods).",
				formatCentsUSD(owed.TotalCents), owed.TaskTitle),
		})
	}

	var payoutsInFlight int
	if err := db.QueryRow(ctx, `
		select count(*) from public.payouts
		 where supporter_id = $1::uuid and status in ($2, $3)
	`, uid, payoutStatusPending, payoutStatusFailed).Scan(&payoutsInFlight); err != nil {
		return nil, fmt.Errorf("count payouts: %w", err)
	}
	if payoutsInFlight > 0 {
		out = append(out, deletionBlocker{
			Code:  "payout_in_flight",
			Count: payoutsInFlight,
			Message: "We still owe you a payout. It has to reach your bank before the account can close — " +
				"check Profile → Earnings, or write to us if it's been more than a week.",
		})
	}
	return out, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func itThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// deletedPlaceholderEmail is unique per account and unroutable (.invalid is
// reserved by RFC 2606), so it can never collide with a real sign-in.
func deletedPlaceholderEmail(uid string) string {
	short := strings.ReplaceAll(uid, "-", "")
	if len(short) > 8 {
		short = short[:8]
	}
	return "deleted-" + short + "@deleted.invalid"
}

// GET /profile/deletion — what deleting would run into right now. The
// clients show the confirmation only when this is empty, so the person is
// never asked to confirm something that is then refused.
func accountDeletionPreview(c *gin.Context) {
	uid := c.GetString("uid")
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	blockers, err := accountDeletionBlockers(c.Request.Context(), uid)
	if err != nil {
		log.Printf("[account.delete][preview] uid=%s: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if blockers == nil {
		blockers = []deletionBlocker{}
	}
	c.JSON(http.StatusOK, gin.H{"can_delete": len(blockers) == 0, "blockers": blockers})
}

// DELETE /profile
func deleteMyAccount(c *gin.Context) {
	uid := c.GetString("uid")
	email := strings.TrimSpace(c.GetString("email"))
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	blockers, err := accountDeletionBlockers(ctx, uid)
	if err != nil {
		log.Printf("[account.delete] uid=%s blockers: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if len(blockers) > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error":    "account_not_deletable",
			"message":  blockers[0].Message,
			"blockers": blockers,
		})
		return
	}

	// Read the external handles BEFORE they are cleared: the Stripe Customer
	// to delete and the Supabase user to delete both live on the row.
	var stripeCustomerID, supabaseSub sql.NullString
	if err := db.QueryRow(ctx,
		`select stripe_customer_id, supabase_sub::text from public.users where id = $1::uuid`, uid,
	).Scan(&stripeCustomerID, &supabaseSub); err != nil {
		log.Printf("[account.delete] uid=%s read handles: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	// The key is resolved while the row still says whether this is the review
	// sandbox — after the update it is the same row, but read it first anyway.
	stripeKey, stripeKeyErr := stripeKeyForUser(ctx, uid)

	placeholder := deletedPlaceholderEmail(uid)
	counts, err := anonymiseAccount(ctx, uid, email, placeholder)
	if err != nil {
		log.Printf("[account.delete][ERROR] uid=%s: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	// External systems, after the commit. Best-effort and recorded.
	stripeDeleted := false
	if stripeCustomerID.Valid && stripeCustomerID.String != "" && paymentsEnabled() {
		if stripeKeyErr != nil {
			log.Printf("[account.delete] uid=%s stripe key: %v — customer %s left for ops", uid, stripeKeyErr, stripeCustomerID.String)
		} else if _, err := (customer.Client{B: stripeBackend(), Key: stripeKey}).Del(stripeCustomerID.String, &stripe.CustomerParams{}); err != nil {
			log.Printf("[account.delete] uid=%s delete stripe customer %s: %v", uid, stripeCustomerID.String, err)
		} else {
			stripeDeleted = true
		}
	}
	supabaseDeleted := false
	if supabaseSub.Valid && supabaseSub.String != "" {
		if err := deleteSupabaseAuthUser(ctx, supabaseSub.String); err != nil {
			log.Printf("[account.delete] uid=%s delete supabase user: %v", uid, err)
		} else {
			supabaseDeleted = true
		}
	}

	// The audit row names no task, so job_id is the system placeholder, as
	// promo actions do (promo.go). The email is deliberately NOT in meta
	// (S-12) — the row's actor_id is the anonymised account.
	writeAudit(ctx, systemActorUID, uid, "ACCOUNT_DELETED", "", map[string]any{
		"stripe_customer_deleted":   stripeDeleted,
		"stripe_customer_id":        stripeCustomerID.String,
		"supabase_user_deleted":     supabaseDeleted,
		"gps_pings_deleted":         counts.gpsPings,
		"push_tokens_deleted":       counts.pushTokens,
		"notifications_deleted":     counts.notifications,
		"tasks_anonymised":          counts.tasks,
		"worklogs_anonymised":       counts.worklogs,
		"requested_from_user_agent": c.Request.UserAgent(),
	})
	log.Printf("[account.delete] uid=%s done stripe=%v supabase=%v tasks=%d", uid, stripeDeleted, supabaseDeleted, counts.tasks)

	// A deleted account is signed out on the spot.
	clearHoraSessionCookie(c)
	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"message": "Your account has been deleted.",
	})
}

type anonymiseCounts struct {
	tasks, worklogs, gpsPings, pushTokens, notifications int64
}

// anonymiseAccount is the database half, in one transaction.
func anonymiseAccount(ctx context.Context, uid, email, placeholder string) (anonymiseCounts, error) {
	var n anonymiseCounts
	tx, err := sqldb.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return n, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	exec := func(what string, q string, args ...any) (int64, error) {
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", what, err)
		}
		rows, _ := res.RowsAffected()
		return rows, nil
	}

	if _, err = exec("users", `
		update public.users
		   set email = $2, name = '', picture = null,
		       google_sub = null, supabase_sub = null,
		       stripe_customer_id = null
		 where id = $1::uuid
	`, uid, placeholder); err != nil {
		return n, err
	}
	// profiles is keyed by id = users.id (S-60.3); the email column carries
	// the placeholder so its unique index and every email join stay valid.
	if _, err = exec("profiles", `
		update public.profiles
		   set email = $2, name = '', phone = '', city = '', bio = '', avatar_url = '',
		       is_verified_supporter = false, updated_at = now()
		 where id = $1::uuid
	`, uid, placeholder); err != nil {
		return n, err
	}
	// The legacy email-identity columns. Matched by uuid where one exists and
	// by the old email where only the text column names the person.
	if n.tasks, err = exec("tasks", `
		update public.tasks
		   set requester   = case when requester_id = $1::uuid or requester = $3 then $2 else requester end,
		       assigned_to = case when assigned_to_id = $1::uuid or assigned_to = $3 then $2 else assigned_to end
		 where requester_id = $1::uuid or assigned_to_id = $1::uuid
		    or cancelled_assignee_id = $1::uuid
		    or requester = $3 or assigned_to = $3
	`, uid, placeholder, email); err != nil {
		return n, err
	}
	if n.worklogs, err = exec("worklogs", `
		update public.worklogs set "user" = $2 where "user" = $1
	`, email, placeholder); err != nil && email != "" {
		return n, err
	}
	if n.gpsPings, err = exec("task_gps_pings", `
		delete from public.task_gps_pings where user_id = $1::uuid
	`, uid); err != nil {
		return n, err
	}
	if n.pushTokens, err = exec("device_push_tokens", `
		delete from public.device_push_tokens where user_id = $1::uuid
	`, uid); err != nil {
		return n, err
	}
	if n.notifications, err = exec("notifications", `
		delete from public.notifications where user_id = $1::uuid
	`, uid); err != nil {
		return n, err
	}
	if err := tx.Commit(); err != nil {
		return n, fmt.Errorf("commit: %w", err)
	}
	return n, nil
}

// deleteSupabaseAuthUser removes the auth.users row so the email is no longer
// held by Supabase either. Needs the service role key; without it the row is
// left and the audit meta says so.
func deleteSupabaseAuthUser(ctx context.Context, sub string) error {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("SUPABASE_PROJECT_URL")), "/")
	key := strings.TrimSpace(os.Getenv("SUPABASE_SERVICE_ROLE_KEY"))
	if base == "" || key == "" {
		return errors.New("SUPABASE_PROJECT_URL / SUPABASE_SERVICE_ROLE_KEY not set")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, base+"/auth/v1/admin/users/"+sub, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("apikey", key)
	resp, err := supabaseAdminHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil // already gone
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var body struct {
			Msg string `json:"msg"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return fmt.Errorf("supabase admin delete returned %d: %s", resp.StatusCode, body.Msg)
	}
	return nil
}

var supabaseAdminHTTP = &http.Client{Timeout: 15 * time.Second}

// clearHoraSessionCookie is the logout cookie, shared with POST /auth/logout.
func clearHoraSessionCookie(c *gin.Context) {
	isProd := strings.EqualFold(os.Getenv("APP_ENV"), "prod") || strings.EqualFold(os.Getenv("COOKIE_SECURE"), "true")
	cookie := &http.Cookie{
		Name:     "hora_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isProd,
	}
	if isProd {
		cookie.SameSite = http.SameSiteNoneMode
	} else {
		cookie.SameSite = http.SameSiteLaxMode
	}
	http.SetCookie(c.Writer, cookie)
}
