package main

import (
	"context"
	"net/http"
	"testing"
)

// The detached completion chain (completeTask) can die between the status
// write and the capture. The sweep finds that shape and settles it; a
// completion still inside its grace window is left to its own chain.
//
// Offline, like TestPhase2bCaptureFailureStillCompletesTheTask: the hold is
// parked in requires_auth so Capture's status guard produces a real
// settlement outcome (capture_failed + audit row) without the network. What
// is under test is that the sweep RAN settlement on the right task.
func TestResettleSweepSettlesAnOrphanedCompletion(t *testing.T) {
	setupStripeWebhookDB(t)
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_offline_guard_only")
	forceStripeKey(t, "sk_test_offline_guard_only")
	ctx := context.Background()

	seedHold := func(taskID, requesterID string) string {
		var id string
		if err := db.QueryRow(ctx, `
			insert into public.payments (task_id, requester_id, kind, status,
			                             stripe_payment_intent_id, authorized_cents)
			values ($1::uuid, $2::uuid, $3, 'requires_auth', 'pi_orphan_'||$1::text, 7675)
			returning id::text
		`, taskID, requesterID, paymentKindTaskPayment).Scan(&id); err != nil {
			t.Fatalf("seed hold: %v", err)
		}
		return id
	}

	w := seedOpsWorld(t, "open")

	// Orphan: completed 15 minutes ago, hold still live — the chain died.
	oldTask := w.taskID
	seedWorklog(t, oldTask, 30, false)
	oldPayment := seedHold(oldTask, w.requesterID)
	if _, err := db.Exec(ctx, `
		update public.tasks set status='completed', completed_at = now() - interval '15 minutes',
		       completion_photo_url='https://example.test/done.jpg'
		 where id=$1::uuid`, oldTask); err != nil {
		t.Fatal(err)
	}

	// Fresh: completed just now, hold still live — its own chain is running.
	freshTask := seedOpenTaskFor(t, w, "Fresh completion", 60)
	freshPayment := seedHold(freshTask, w.requesterID)
	if _, err := db.Exec(ctx, `
		update public.tasks set status='completed', completed_at = now(),
		       assigned_to_id=$2::uuid, assigned_to=$3,
		       completion_photo_url='https://example.test/done.jpg'
		 where id=$1::uuid`, freshTask, w.supporterID, oldSupporterEmail); err != nil {
		t.Fatal(err)
	}

	sweepUnsettledCompletions(ctx)

	if got := paymentStatusByID(t, oldPayment); got != paymentStatusCaptureFailed {
		t.Errorf("orphan: payment status = %q, want %q (the sweep did not settle it)", got, paymentStatusCaptureFailed)
	}
	if rows := auditRows(t, oldTask, "PAYMENT_CAPTURE_FAILED"); len(rows) != 1 {
		t.Errorf("orphan: %d PAYMENT_CAPTURE_FAILED audit rows, want 1", len(rows))
	}
	if got := paymentStatusByID(t, freshPayment); got != "requires_auth" {
		t.Errorf("fresh: payment status = %q — the sweep touched a completion still in its grace window", got)
	}

	// Once settled (even into capture_failed) the task no longer matches:
	// a second pass is a no-op, not a second audit row.
	sweepUnsettledCompletions(ctx)
	if rows := auditRows(t, oldTask, "PAYMENT_CAPTURE_FAILED"); len(rows) != 1 {
		t.Errorf("second pass: %d PAYMENT_CAPTURE_FAILED audit rows, want 1", len(rows))
	}
}

// Two completes for one task produce one completion: the second finds the
// row no longer open and is told so, and does not start a second chain.
func TestCompleteTwiceIsOneCompletion(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 30, false)

	runs := 0
	prev := completionDetach
	completionDetach = func(fn func()) { runs++; fn() }
	t.Cleanup(func() { completionDetach = prev })

	body := map[string]any{"completion_photo_url": "https://example.test/done.jpg"}
	completeAs(t, w.taskID, w.supporterID, body, http.StatusOK)
	second := completeAs(t, w.taskID, w.supporterID, body, http.StatusBadRequest)
	if second["error"] != "already closed" {
		t.Fatalf("second complete: %v, want already closed", second["error"])
	}
	if runs != 1 {
		t.Fatalf("settlement chains started = %d, want 1", runs)
	}
}

// A completed task with a live hold reports `settling`, not `estimated`:
// the task is not running, and the money has not moved yet.
func TestSettlementStateSettlingWhileHoldIsLive(t *testing.T) {
	setupStripeWebhookDB(t)
	ctx := context.Background()
	w := seedOpsWorld(t, "open")
	if _, err := db.Exec(ctx, `
		insert into public.payments (task_id, requester_id, kind, status, stripe_payment_intent_id, authorized_cents)
		values ($1::uuid, $2::uuid, $3, 'authorized', 'pi_live', 1950)`,
		w.taskID, w.requesterID, paymentKindTaskPayment); err != nil {
		t.Fatal(err)
	}
	if got := settlementState(ctx, w.taskID, "open"); got != "estimated" {
		t.Errorf("open task with a live hold: %q, want estimated", got)
	}
	if got := settlementState(ctx, w.taskID, "completed"); got != "settling" {
		t.Errorf("completed task with a live hold: %q, want settling", got)
	}
}
