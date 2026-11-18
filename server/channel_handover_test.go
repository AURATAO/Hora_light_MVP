package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"hora-auth/internal/channel"
)

// chatHandOvers against a real Postgres: a notice is the ops action's audit
// row plus the chat's own message for that task right after it — the
// reassign's row as the real handler writes it.

const handOverThreadFixture = `
DROP TABLE IF EXISTS public.channel_messages CASCADE;
CREATE TABLE public.channel_messages (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	channel text NOT NULL,
	address text NOT NULL,
	direction text NOT NULL,
	body text,
	task_id uuid,
	relay boolean NOT NULL DEFAULT false,
	created_at timestamptz NOT NULL DEFAULT now()
);`

const hoAddr = "+12125550123"

func hoSend(t *testing.T, taskID, body string, at time.Time) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `
		INSERT INTO public.channel_messages (channel, address, direction, body, task_id, created_at)
		VALUES ('imessage', $1, 'outbound', $2, NULLIF($3,'')::uuid, $4)`, hoAddr, body, taskID, at); err != nil {
		t.Fatal(err)
	}
}

func TestChatHandOverNotices(t *testing.T) {
	setupReassignDB(t)
	if _, err := db.Exec(context.Background(), handOverThreadFixture); err != nil {
		t.Fatal(err)
	}
	w := seedWorld(t)
	ctx := context.Background()
	h := &chatHandOvers{sqlDB: sqldb}
	since := time.Now().Add(-channel.HandOverWindow)
	notices := func() []channel.HandOverNotice {
		t.Helper()
		ns, err := h.HandOverNotices(ctx, w.requesterID, channel.IMessage, hoAddr, since)
		if err != nil {
			t.Fatal(err)
		}
		return ns
	}

	// A reassign after start, through the real handler.
	taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)
	if _, err := db.Exec(ctx, `INSERT INTO public.worklogs (task_id, "user", start_at, end_at) VALUES ($1::uuid, $2, now() - interval '30 minutes', now() - interval '10 minutes')`, taskID, oldSupporterEmail); err != nil {
		t.Fatal(err)
	}
	if code, out := callReassign(t, taskID, w.adminID, adminEmail, `{"supporter_email":"`+newSupporterEmail+`","void_prior_work":true,"reason":"left"}`); code != http.StatusOK {
		t.Fatalf("reassign %d %v", code, out)
	}
	if got := notices(); len(got) != 0 {
		t.Fatalf("no message reached the thread, yet %d notice(s)", len(got))
	}
	hoSend(t, taskID, "Your supporter couldn't finish \"Pick up a parcel\", so Nina New is taking over.", time.Now())
	got := notices()
	if len(got) != 1 || got[0].Kind != channel.HandOverReassigned || !got[0].Started || got[0].Supporter != "Nina New" || got[0].Closed || got[0].TaskID != taskID {
		t.Fatalf("reassigned notice %+v", got)
	}

	// A cancel for a dropped supporter: the newer notice comes first; the
	// one who dropped out is never named.
	cancelID := seedReassignTask(t, "cancelled", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)
	if _, err := db.Exec(ctx, `UPDATE public.tasks SET location_text = '1232 2nd Ave | 200 E 70th St', category = 'laundry', prepay_amount_cents = 2500, scheduled_at = now() + interval '1 day' WHERE id = $1::uuid`, cancelID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO public.audit_logs (job_id, actor_id, action, reason, meta, created_at) VALUES ($1::uuid, $2::uuid, 'CANCELLED', 'family emergency', '{"cause":"supporter_dropped"}', now())`, cancelID, w.adminID); err != nil {
		t.Fatal(err)
	}
	sent := time.Now().Add(time.Second)
	hoSend(t, cancelID, "Your supporter couldn't finish \"Pick up a parcel\", so we've cancelled it.", sent)
	got = notices()
	if len(got) != 2 || got[0].Kind != channel.HandOverCancelled || got[0].Supporter != "" || got[0].Closed ||
		got[0].Location1 != "1232 2nd Ave" || got[0].Location2 != "200 E 70th St" || got[0].BudgetCents != 2500 || got[0].ScheduledAt == nil || got[0].Category != "laundry" {
		t.Fatalf("cancelled notice %+v", got)
	}
	// Answered: our decline line is tagged with the task.
	hoSend(t, cancelID, "No problem.", sent.Add(time.Minute))
	if got = notices(); !got[0].Closed {
		t.Fatalf("decline did not close it: %+v", got[0])
	}

	// An ordinary ops cancel is no drop-off notice; neither is a message
	// sent long after the action.
	other := seedReassignTask(t, "cancelled", w.requesterID, requesterEmail, "", "")
	if _, err := db.Exec(ctx, `INSERT INTO public.audit_logs (job_id, actor_id, action, reason, meta, created_at) VALUES
		($1::uuid, $2::uuid, 'CANCELLED', 'stale', '{"cause":""}', now() - interval '1 hour')`, other, w.adminID); err != nil {
		t.Fatal(err)
	}
	hoSend(t, other, "Your task has been cancelled by the HO:RA team.", time.Now().Add(-59*time.Minute))
	if got = notices(); len(got) != 2 {
		t.Fatalf("a general cancel counted: %d", len(got))
	}

	// The reassigned task finished: closed.
	if _, err := db.Exec(ctx, `UPDATE public.tasks SET status = 'completed' WHERE id = $1::uuid`, taskID); err != nil {
		t.Fatal(err)
	}
	if got = notices(); !got[1].Closed {
		t.Fatalf("completed task still open: %+v", got[1])
	}
}
