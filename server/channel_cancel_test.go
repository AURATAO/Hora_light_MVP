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
	setupAdminOpsDB(t)
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
	if q2, err := b.CancelQuote(ctx, w.requesterID, w.taskID); err != nil || !q2.Moving {
		t.Fatalf("session open: %+v err %v", q2, err)
	}
	if _, err := db.Exec(ctx, `delete from public.worklogs where task_id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	if res := b.Cancel(ctx, w.supporterID, w.taskID); res.OK {
		t.Fatal("the supporter cancelled the requester's task")
	}
	res := b.Cancel(ctx, w.requesterID, w.taskID)
	if !res.OK || res.BillCents != q.ChargeCents {
		t.Fatalf("cancel %+v, quoted %d", res, q.ChargeCents)
	}
	var status string
	if err := db.QueryRow(ctx, `select status from public.tasks where id=$1::uuid`, w.taskID).Scan(&status); err != nil || status != "cancelled" {
		t.Fatalf("status %q err %v", status, err)
	}
	if res := b.Cancel(ctx, w.requesterID, w.taskID); res.OK || !res.Gone {
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
