package main

import (
	"context"
	"testing"
	"time"
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
