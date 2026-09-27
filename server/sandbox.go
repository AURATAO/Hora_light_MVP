package main

// The review sandbox: the App Review account, walled off in two directions.
//
// ONE: ITS MONEY IS NEVER REAL. Every Stripe call made on behalf of a sandbox
// user — its Customer, its saved card, the hold on its tasks, the capture, the
// balance charge, the transfer to its connected account, that account's
// onboarding and status — is made with a TEST secret key, whatever the
// platform's own key is. Today the platform runs on test keys and the two are
// the same key; after the October switch to live keys they are not, and
// nothing about this file changes. The key is chosen per call by stripeKeyFor*,
// and the sandbox branch refuses outright (errSandboxKeyUnavailable) rather
// than falling back to a live key: a reviewer who cannot pay is a failed
// review, a reviewer who pays real money is an incident.
//
// Payments are also ALWAYS ENFORCED for a sandbox user, whatever
// PAYMENTS_ENFORCED says (paymentsEnforcedFor), so the reviewer walks the same
// hold → capture → payout path a real user does rather than a payments-off
// shortcut that production no longer has.
//
// TWO: IT NEVER MEETS A REAL USER. A sandbox task is invisible to and
// un-acceptable by every non-sandbox account, and a sandbox account sees and
// accepts only sandbox tasks (sameSandboxPartitionSQL). Without this a
// reviewer in Cupertino could accept a real requester's errand in Brooklyn,
// and — the money half again — a live-mode charge would be paid out to a
// test-mode connected account, or the reverse, which Stripe refuses.
//
// Inside the partition the one-login walkthrough needs one more exemption: a
// sandbox user may accept its OWN task (canSelfAccept), so a single reviewer
// login can play both sides. No real account can.
//
// The NYC service area needs no exemption, because the backend does not have
// one: nothing in createTask checks where a task is, and the clients only BIAS
// address autocomplete toward New York. The partition above is what keeps a
// reviewer's California task off real supporters' boards.
//
// users.is_sandbox is set by the review-login seed and nothing else. It is on
// the row, not derived from REVIEW_ACCOUNT_EMAIL, because the Stripe objects
// the account owns stay test-mode objects after the env vars are removed.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/stripe/stripe-go/v86"
)

var errSandboxKeyUnavailable = errors.New(
	"payments: the review sandbox needs a Stripe TEST key (STRIPE_SANDBOX_SECRET_KEY) and none is configured")

func isStripeTestSecretKey(k string) bool {
	return strings.HasPrefix(k, "sk_test_") || strings.HasPrefix(k, "rk_test_")
}

// sandboxStripeKey is the secret key every sandbox call uses.
//
// STRIPE_SANDBOX_SECRET_KEY when set; otherwise the platform key, but ONLY
// while that is itself a test key — which is today's posture, and why nothing
// has to be configured before October. A live key in either place is refused.
func sandboxStripeKey() (string, error) {
	if k := strings.TrimSpace(os.Getenv("STRIPE_SANDBOX_SECRET_KEY")); k != "" {
		if !isStripeTestSecretKey(k) {
			return "", fmt.Errorf("payments: STRIPE_SANDBOX_SECRET_KEY is not a test key — refusing to use it: %w",
				errSandboxKeyUnavailable)
		}
		return k, nil
	}
	initStripe()
	if isStripeTestSecretKey(stripe.Key) {
		return stripe.Key, nil
	}
	return "", errSandboxKeyUnavailable
}

// sandboxPublishableKey pairs with sandboxStripeKey for the one client-side
// use (a 3DS challenge or the card sheet). Same rule: test only, never live.
func sandboxPublishableKey() string {
	if k := strings.TrimSpace(os.Getenv("STRIPE_SANDBOX_PUBLISHABLE_KEY")); strings.HasPrefix(k, "pk_test_") {
		return k
	}
	if k := stripePublishableKey(); strings.HasPrefix(k, "pk_test_") {
		return k
	}
	return ""
}

// isSandboxUser reads users.is_sandbox. An unknown user is not a sandbox user;
// a database error is returned, never guessed at, because the caller is
// usually about to pick a Stripe key with the answer.
func isSandboxUser(ctx context.Context, uid string) (bool, error) {
	if strings.TrimSpace(uid) == "" {
		return false, nil
	}
	var sandbox bool
	err := db.QueryRow(ctx,
		`select coalesce((select is_sandbox from public.users where id = $1::uuid), false)`, uid,
	).Scan(&sandbox)
	if err != nil {
		return false, fmt.Errorf("read sandbox flag for %s: %w", uid, err)
	}
	return sandbox, nil
}

// isSandboxTask reports whether the task's requester is a sandbox user — the
// task's money is theirs, so its mode is theirs.
func isSandboxTask(ctx context.Context, taskID string) (bool, error) {
	var sandbox bool
	err := db.QueryRow(ctx, `
		select coalesce((
			select u.is_sandbox from public.tasks t
			  join public.users u on u.id = t.requester_id
			 where t.id = $1::uuid), false)
	`, taskID).Scan(&sandbox)
	if err != nil {
		return false, fmt.Errorf("read sandbox flag for task %s: %w", taskID, err)
	}
	return sandbox, nil
}

// stripeKeyForUser is the secret key for a call made on behalf of uid: the
// sandbox test key for the review account, the platform key for everyone else.
func stripeKeyForUser(ctx context.Context, uid string) (string, error) {
	if !paymentsEnabled() {
		return "", errPaymentsDisabled
	}
	sandbox, err := isSandboxUser(ctx, uid)
	if err != nil {
		return "", err
	}
	if sandbox {
		return sandboxStripeKey()
	}
	return stripe.Key, nil
}

// stripeKeyForTask is stripeKeyForUser for the task's requester. Used by the
// calls that move a task's money: hold, capture, release, 3DS read-back,
// balance charge, and the transfer funded by that charge.
func stripeKeyForTask(ctx context.Context, taskID string) (string, error) {
	if !paymentsEnabled() {
		return "", errPaymentsDisabled
	}
	sandbox, err := isSandboxTask(ctx, taskID)
	if err != nil {
		return "", err
	}
	if sandbox {
		return sandboxStripeKey()
	}
	return stripe.Key, nil
}

// publishableKeyForUser is the client-side key matching stripeKeyForUser.
func publishableKeyForUser(ctx context.Context, uid string) string {
	if sandbox, err := isSandboxUser(ctx, uid); err == nil && sandbox {
		return sandboxPublishableKey()
	}
	return stripePublishableKey()
}

// paymentsEnforcedFor is paymentsEnforced, plus: always on for the sandbox.
// A read error counts as "not sandbox" here — the worst case is the flag's own
// value, which is what every non-review account gets anyway.
func paymentsEnforcedFor(ctx context.Context, uid string) bool {
	if paymentsEnforced() {
		return true
	}
	sandbox, _ := isSandboxUser(ctx, uid)
	return sandbox
}

// sameSandboxPartitionSQL is the task-feed predicate: the task's requester is
// on the same side of the sandbox wall as the viewer. `taskAlias` is the tasks
// table's alias in the enclosing query (or "public.tasks"), `viewerParam` a
// placeholder bound to the viewer's users.id.
func sameSandboxPartitionSQL(taskAlias, viewerParam string) string {
	return fmt.Sprintf(`coalesce((select ru.is_sandbox from public.users ru where ru.id = %s.requester_id), false)
	  = coalesce((select vu.is_sandbox from public.users vu where vu.id = %s::uuid), false)`, taskAlias, viewerParam)
}

// sandboxPartitionAllows is the same wall as a Go check, for single-task paths
// (read, accept, reassign): may viewer see / take a task posted by requester?
func sandboxPartitionAllows(ctx context.Context, requesterID, viewerID string) (bool, error) {
	a, err := isSandboxUser(ctx, requesterID)
	if err != nil {
		return false, err
	}
	b, err := isSandboxUser(ctx, viewerID)
	if err != nil {
		return false, err
	}
	return a == b, nil
}

// canSelfAccept is the one-login walkthrough's exemption: a sandbox account
// may accept a task it posted. Nobody else may.
func canSelfAccept(ctx context.Context, uid string) bool {
	sandbox, err := isSandboxUser(ctx, uid)
	return err == nil && sandbox
}

// stripeBackend is the API backend the per-key resource clients use. The
// package-level stripe-go functions read the global stripe.Key; the
// per-resource Client{B, Key} values below are how a call names its own key.
func stripeBackend() stripe.Backend {
	return stripe.GetBackend(stripe.APIBackend)
}

// stripeKeyForAccount is stripeKeyForUser for the owner of a connected
// account (users.stripe_account_id). An account no row names yet falls to the
// platform key; every sandbox account is recorded on its user before any call
// that is not its own creation.
func stripeKeyForAccount(ctx context.Context, accountID string) (string, error) {
	if !paymentsEnabled() {
		return "", errPaymentsDisabled
	}
	var uid string
	err := db.QueryRow(ctx,
		`select coalesce((select id::text from public.users where stripe_account_id = $1 limit 1), '')`,
		accountID).Scan(&uid)
	if err != nil {
		return "", fmt.Errorf("read owner of account %s: %w", accountID, err)
	}
	if uid == "" {
		return stripe.Key, nil
	}
	return stripeKeyForUser(ctx, uid)
}

// stripeKeyForTransfer is the key a transfer was created with: its task's.
func stripeKeyForTransfer(ctx context.Context, transferID string) (string, error) {
	var taskID string
	err := db.QueryRow(ctx,
		`select coalesce((select task_id::text from public.payouts where stripe_transfer_id = $1 limit 1), '')`,
		transferID).Scan(&taskID)
	if err != nil {
		return "", fmt.Errorf("read task of transfer %s: %w", transferID, err)
	}
	if taskID == "" {
		if !paymentsEnabled() {
			return "", errPaymentsDisabled
		}
		return stripe.Key, nil
	}
	return stripeKeyForTask(ctx, taskID)
}
