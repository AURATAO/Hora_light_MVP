package main

// The card-removal refusal, and what it says.
//
// Removing a card with a live hold is correctly refused — detaching it would
// not release the hold, only remove our ability to name what is holding the
// requester's money. What the build 11 device run found was the MESSAGE: "a
// task that hasn't finished yet" told a requester with three live tasks
// nothing about which, and nothing about what to do.
//
// The Stripe half (which card is the charging card) is not under test here;
// liveHoldTasks is the DB half, separable for exactly this reason.
//
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55440/horatest' \
//	  go test ./ -run CardRemoval -v

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// One row per TASK, newest first, title carried — and a task with two payment
// rows (a hold plus a completion balance) counts once.
func TestCardRemovalBlockedListsEachTaskOnce(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	ctx := context.Background()

	older := seedOpenTaskFor(t, w, "Older task with a hold", 60)
	newer := seedOpenTaskFor(t, w, "Newer task with a hold", 10)
	seedPayment(t, older, w.requesterID, "pi_block_1", paymentStatusAuthorized)
	seedPayment(t, newer, w.requesterID, "pi_block_2", paymentStatusAuthorized)
	// A second live row on the same task, of the OTHER kind a task can carry
	// at the same time — an approved budget increase holds its own money
	// beside the task payment (uq_payments_task_kind_live is per task+kind).
	// Two live rows, one task, one line in the refusal.
	seedPaymentOfKind(t, newer, w.requesterID, paymentKindBudgetIncrease, "pi_block_2b", paymentStatusAuthorized)
	// Finished money does not block anything.
	done := seedOpenTaskFor(t, w, "Already captured", 30)
	seedPayment(t, done, w.requesterID, "pi_block_3", paymentStatusCaptured)

	got, err := liveHoldTasks(ctx, w.requesterID)
	if err != nil {
		t.Fatalf("liveHoldTasks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("%d blocking tasks, want 2 (one per task, captured excluded): %+v", len(got), got)
	}
	if got[0].ID != newer || got[1].ID != older {
		t.Errorf("order = [%s, %s], want newest first", got[0].Title, got[1].Title)
	}
	for _, b := range got {
		if b.Title == "" {
			t.Errorf("task %s carries no title — the client would have nothing to link", b.ID)
		}
	}
}

// Nothing live, nothing blocking — and never nil, so a JSON consumer sees
// [] rather than null.
func TestCardRemovalNothingLiveIsEmptyNotNil(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	got, err := liveHoldTasks(context.Background(), w.requesterID)
	if err != nil {
		t.Fatalf("liveHoldTasks: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("got %#v, want an empty, non-nil slice", got)
	}
}

// The sentence carries the count, and the count agrees with English.
func TestCardRemovalMessageNamesTheCount(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{1, "This card has a hold from 1 active task. Complete or cancel it to remove the card."},
		{2, "This card has holds from 2 active tasks. Complete or cancel them to remove the card."},
		{7, "This card has holds from 7 active tasks. Complete or cancel them to remove the card."},
	}
	for _, tc := range cases {
		if got := cardInUseMessage(tc.n); got != tc.want {
			t.Errorf("n=%d:\n got  %q\n want %q", tc.n, got, tc.want)
		}
	}
	// And it never says the old, useless thing.
	if strings.Contains(cardInUseMessage(1), "hasn't finished yet") {
		t.Error("the refusal is back to describing the situation instead of naming it")
	}
}

// seedPaymentOfKind is seedPayment with the kind chosen by the caller.
func seedPaymentOfKind(t *testing.T, taskID, requesterID, kind, intentID, status string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `
		insert into public.payments
		  (task_id, requester_id, kind, stripe_payment_intent_id, status, authorized_cents)
		values ($1::uuid, $2::uuid, $3, $4, $5, 800)
		returning id::text
	`, taskID, requesterID, kind, intentID, status).Scan(&id); err != nil {
		t.Fatalf("seed %s payment: %v", kind, err)
	}
	return id
}

// seedOpenTaskFor writes one open task for the world's requester, created
// `minutesAgo` back so ordering is deterministic. Returns its id.
func seedOpenTaskFor(t *testing.T, w opsWorld, title string, minutesAgo int) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `
		insert into public.tasks
			(title, description, category, location_text, estimated_minutes,
			 requester, requester_id, status, created_at)
		values ($1, 'seeded', 'quick_errand', 'Somewhere | NYC', 30,
			 $2, $3::uuid, 'open', now() - make_interval(mins => $4))
		returning id::text`,
		title, requesterEmail, w.requesterID, minutesAgo,
	).Scan(&id); err != nil {
		t.Fatalf("seed task %q: %v", title, err)
	}
	_ = fmt.Sprintf
	return id
}
