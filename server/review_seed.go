package main

// THE REVIEW BOARD. The app is supporter-only from the next build (PLAN
// §13b-3): there is no Post screen, so an App Reviewer can no longer post a
// task and then accept it. Without this the reviewer signs in to an empty
// Tasks tab and nothing in the app can be exercised.
//
// So the review login keeps a few open tasks on the board, posted BY THE
// REVIEW ACCOUNT ITSELF. That is deliberate, and it is the narrowest choice:
//
//   - it is the same row shape the old walkthrough produced when the reviewer
//     posted from Home, so every later path (accept, on my way, clock, complete,
//     capture, payout) is the one already exercised by review builds;
//   - canSelfAccept already lets a sandbox account accept its own task, and
//     the board already shows a sandbox account its own tasks (getAvailableTasks);
//   - the sandbox wall keeps them off every real supporter's board, and
//     because the requester is the sandbox account, the hold is a Stripe TEST
//     hold on its seeded test card (stripeKeyForUser) — never live money.
//
// Each seeded task gets a real hold through CreatePreAuth, exactly like a post,
// because a sandbox user's payments are always enforced and completion
// captures that hold. No hold, no task: a seed that cannot hold posts nothing.
//
// Not announced and not dispatched: the only possible recipient is the
// reviewer, who is the requester, and the dispatch excludes the requester.
//
// Holds expire after about a week, so an open seed older than
// reviewSeedMaxAge is released and cancelled on the next login and replaced,
// rather than left to fail at capture on the reviewer's completion.

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

const (
	reviewSeedTarget  = 3
	reviewSeedMaxAge  = 5 * 24 * time.Hour
	reviewSeedTimeout = 20 * time.Second
)

type reviewSeedTask struct {
	Title, Description, Category, Location string
	Minutes                                int
}

// Public places, never a home address. Short and cheap, so the test hold is
// small and the walkthrough is quick.
var reviewSeedTasks = []reviewSeedTask{
	{
		Title:       "Pick up a coffee order",
		Description: "Collect a pre-paid coffee order and bring it to the front desk. Demo task for App Review.",
		Category:    "quick_errand",
		Location:    "Bryant Park, New York, NY 10018",
		Minutes:     30,
	},
	{
		Title:       "Hold a place in line",
		Description: "Wait in line at the box office until I arrive. Demo task for App Review.",
		Category:    "queue",
		Location:    "Lincoln Center, New York, NY 10023",
		Minutes:     45,
	},
	{
		Title:       "Drop off a package",
		Description: "Take a small boxed package to the post office counter. Demo task for App Review.",
		Category:    "delivery",
		Location:    "James A. Farley Building, 421 8th Ave, New York, NY 10001",
		Minutes:     30,
	},
}

// One top-up at a time: two logins racing would otherwise both see "1 open"
// and both post two. The server is a single instance, so a mutex is the lock.
var reviewSeedMu sync.Mutex

// seedReviewBoard is best-effort and never fails the login: the worst case is
// a board with fewer tasks, which the reviewer can still pull to refresh.
func seedReviewBoard(parent context.Context, uid, email string) {
	reviewSeedMu.Lock()
	defer reviewSeedMu.Unlock()

	// Detached from the request: a reviewer's slow network closing the login
	// request must not leave a hold half-placed. Bounded instead.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), reviewSeedTimeout)
	defer cancel()

	if !paymentsEnabled() {
		log.Printf("[review-seed] skipped: Stripe not configured, and sandbox tasks need a test hold")
		return
	}

	retireStaleReviewSeeds(ctx, uid)

	var open int
	if err := db.QueryRow(ctx, `
		select count(*) from public.tasks
		 where requester_id = $1::uuid and status = 'open' and assigned_to_id is null
	`, uid).Scan(&open); err != nil {
		log.Printf("[review-seed] count open: %v", err)
		return
	}
	need := reviewSeedTarget - open
	if need <= 0 {
		return
	}

	payCtx, err := resolvePreAuthContext(ctx, uid, email)
	if err != nil {
		log.Printf("[review-seed] skipped: no card to hold against: %v", err)
		return
	}

	posted := 0
	for i := 0; i < len(reviewSeedTasks) && posted < need; i++ {
		if err := postReviewSeed(ctx, uid, email, reviewSeedTasks[(open+i)%len(reviewSeedTasks)], payCtx); err != nil {
			log.Printf("[review-seed] post: %v", err)
			continue
		}
		posted++
	}
	if posted > 0 {
		log.Printf("[review-seed] posted %d task(s) for the review account", posted)
	}
}

func postReviewSeed(ctx context.Context, uid, email string, t reviewSeedTask, payCtx preAuthContext) error {
	rateCents := resolveRateCentsPerMin(time.Now())
	var taskID string
	if err := db.QueryRow(ctx, `
		insert into public.tasks
		  (title, description, category, location_text,
		   estimated_minutes, prepay_amount_cents, shopping_budget_approved_cents,
		   is_immediate, scheduled_at,
		   requester, requester_id, status, assigned_to, assigned_to_id,
		   transport_required, rate_cents_per_min)
		values
		  ($1, $2, $3, $4,
		   $5, 0, 0,
		   true, null,
		   $6, $7::uuid, $8, '', null,
		   'none', $9)
		returning id
	`, t.Title, t.Description, t.Category, t.Location, t.Minutes,
		email, uid, taskStatusPendingPayment, rateCents).Scan(&taskID); err != nil {
		return err
	}

	p, err := CreatePreAuth(ctx, PreAuthInput{
		TaskID:                taskID,
		RequesterID:           uid,
		Category:              t.Category,
		EstimatedMinutes:      t.Minutes,
		RateCents:             rateCents,
		StripeCustomerID:      payCtx.CustomerID,
		StripePaymentMethodID: payCtx.PaymentMethodID,
	})
	var pe *PreAuthError
	if err == nil && p.Status == paymentStatusAuthorized {
		if err := promoteTaskToOpen(ctx, taskID, p.ID); err != nil {
			releaseTaskHold(ctx, taskID, uid, "review_seed_post_failed")
			discardUnpaidTask(ctx, taskID)
			return err
		}
		geocodeTaskAsync(taskID, t.Location)
		return nil
	}
	if errors.As(err, &pe) && pe.Uncertain {
		// Kept parked (invisible) like an uncertain post: discarding it could
		// strand a hold nobody can name. Expires with the hold.
		return err
	}
	discardUnpaidTask(ctx, taskID)
	if err == nil {
		return errors.New("hold came back " + string(p.Status))
	}
	return err
}

// retireStaleReviewSeeds releases and cancels the review account's own open,
// unaccepted tasks that are older than a hold safely lives.
func retireStaleReviewSeeds(ctx context.Context, uid string) {
	rows, err := db.Query(ctx, `
		select id::text from public.tasks
		 where requester_id = $1::uuid and status = 'open' and assigned_to_id is null
		   and created_at < now() - make_interval(secs => $2)
	`, uid, reviewSeedMaxAge.Seconds())
	if err != nil {
		log.Printf("[review-seed] list stale: %v", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		tag, err := db.Exec(ctx, `
			update public.tasks
			   set status = 'cancelled', cancelled_at = now(), cancel_reason = 'review_seed_expired'
			 where id = $1::uuid and status = 'open' and assigned_to_id is null
		`, id)
		if err != nil || tag.RowsAffected() == 0 {
			continue // accepted in the meantime, or a db error: leave it
		}
		releaseTaskHold(ctx, id, uid, "review_seed_expired")
	}
}
