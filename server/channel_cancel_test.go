package main

import (
	"context"
	"testing"
	"time"

	"hora-auth/internal/channel"
)

// Cancel by chat (Arbiter 2026-10-24) uses the app's own preview and cancel
// handler: the quote a person agrees to is the charge recorded, and only the
// requester can quote or cancel.
func TestChatCancelQuotesAndCancelsLikeTheApp(t *testing.T) {
	setupStripeWebhookDB(t) // the fuller schema: the time cap reads auto_extend_consent
	w := seedOpsWorld(t, "open")
	setAcceptedAt(t, w.taskID, time.Hour) // committed, outside the grace window
	ctx := context.Background()
	b := &chatBooker{sqlDB: sqldb}

	open, err := b.OpenTasks(ctx, w.requesterID)
	if err != nil || len(open) != 1 || open[0].ID != w.taskID {
		t.Fatalf("open %v err %v", open, err)
	}
	if _, err := b.CancelQuote(ctx, w.supporterID, w.taskID); err == nil {
		t.Fatal("a quote for someone else's task")
	}
	q, err := b.CancelQuote(ctx, w.requesterID, w.taskID)
	if err != nil || !q.Committed || q.ChargeCents <= 0 || q.BaseFeeCents <= 0 {
		t.Fatalf("quote %+v err %v", q, err)
	}
	if q.Moving {
		t.Fatal("no session running, outside grace: not moving")
	}
	// Review 10/24 #9: a running session moves the charge every minute —
	// not quoted by chat.
	if _, err := db.Exec(ctx, `insert into public.worklogs (task_id, "user", start_at, end_at) values ($1::uuid, $2::uuid, now() - interval '5 minutes', null)`, w.taskID, w.supporterID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `update public.tasks set estimated_minutes = 30 where id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	q2, err := b.CancelQuote(ctx, w.requesterID, w.taskID)
	if err != nil || !q2.Moving {
		t.Fatalf("session open: %+v err %v", q2, err)
	}
	// The ceiling: the committed charge with the session taken to the cap
	// (Arbiter 2026-10-25 21:58) — never below the charge as of now.
	capMinutes, _ := taskTimeCapMinutes(ctx, w.taskID)
	want := cancelSettlementCents(taskCategory(ctx, w.taskID), capMinutes, true, taskRateCentsPerMin(ctx, w.taskID))
	if q2.CeilingCents != want || q2.CeilingCents < q2.ChargeCents {
		t.Fatalf("ceiling %d, want %d (charge now %d)", q2.CeilingCents, want, q2.ChargeCents)
	}
	// S-11/5: cancelled WITH the session open and long past the cap, the
	// bill never exceeds the ceiling the person said YES to.
	if _, err := db.Exec(ctx, `update public.worklogs set start_at = now() - interval '3 hours' where task_id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	res0 := b.Cancel(ctx, w.requesterID, w.taskID, "plans_changed")
	if !res0.OK || res0.BillCents > q2.CeilingCents {
		t.Fatalf("bill %d above the ceiling %d (%+v)", res0.BillCents, q2.CeilingCents, res0)
	}
	// Re-seed an open task for the rest (the cancel above closed this one).
	w.taskID = seedReassignTask(t, "open", w.requesterID, requesterEmail, w.supporterID, oldSupporterEmail)
	setAcceptedAt(t, w.taskID, time.Hour)
	if _, err := db.Exec(ctx, `update public.tasks set estimated_minutes = 30 where id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	q, err = b.CancelQuote(ctx, w.requesterID, w.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if res := b.Cancel(ctx, w.supporterID, w.taskID, "plans_changed"); res.OK {
		t.Fatal("the supporter cancelled the requester's task")
	}
	res := b.Cancel(ctx, w.requesterID, w.taskID, "plans_changed")
	if !res.OK || res.BillCents != q.ChargeCents {
		t.Fatalf("cancel %+v, quoted %d", res, q.ChargeCents)
	}
	var reasonCode string
	if err := db.QueryRow(ctx, `select coalesce(cancel_reason_code,'') from public.tasks where id=$1::uuid`, w.taskID).Scan(&reasonCode); err != nil || reasonCode != "plans_changed" {
		t.Fatalf("reason code %q err %v", reasonCode, err)
	}
	var status string
	if err := db.QueryRow(ctx, `select status from public.tasks where id=$1::uuid`, w.taskID).Scan(&status); err != nil || status != "cancelled" {
		t.Fatalf("status %q err %v", status, err)
	}
	if res := b.Cancel(ctx, w.requesterID, w.taskID, "other"); res.OK || !res.Gone {
		t.Fatalf("second cancel: %+v", res)
	}
	if open, err := b.OpenTasks(ctx, w.requesterID); err != nil || len(open) != 0 {
		t.Fatalf("open after cancel %v err %v", open, err)
	}
}

// Review 10/24 #9: Gone only for "past cancelling" answers; a 200 whose
// bill cannot be read is a cancel with an unknown charge, never "no charge".
func TestCancelResultMapping(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   channel.CancelResult
	}{
		{200, `{"bill_cents": 1200}`, channel.CancelResult{OK: true, BillCents: 1200}},
		{200, `{"bill_cents": 0}`, channel.CancelResult{OK: true}},
		{200, `not json`, channel.CancelResult{OK: true, BillUnknown: true}},
		{200, `{}`, channel.CancelResult{OK: true, BillUnknown: true}},
		{400, `{"error": "task already closed"}`, channel.CancelResult{Gone: true}},
		{400, `{"error": "task_removed"}`, channel.CancelResult{Gone: true}},
		{404, `{"error": "not found"}`, channel.CancelResult{}},
		{400, `{"error": "reason required"}`, channel.CancelResult{}},
		{403, `{"error": "only requester can cancel"}`, channel.CancelResult{}},
		{500, `{"error": "calc error"}`, channel.CancelResult{}},
	} {
		if got := cancelResultFrom(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("%d %s: %+v, want %+v", tc.status, tc.body, got, tc.want)
		}
	}
}

// The chat's reason list IS the app's: same codes, same labels, same order.
func TestChatCancelReasonsMatchTheApp(t *testing.T) {
	if len(channel.CancelReasonChoices) != len(cancelReasons) {
		t.Fatalf("chat %d reasons, app %d", len(channel.CancelReasonChoices), len(cancelReasons))
	}
	for i, c := range channel.CancelReasonChoices {
		if c.Code != cancelReasons[i].Value || c.Label != cancelReasons[i].Label {
			t.Errorf("#%d chat %s/%s app %s/%s", i+1, c.Code, c.Label, cancelReasons[i].Value, cancelReasons[i].Label)
		}
	}
}
