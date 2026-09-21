package main

// The cancellation billing policy, in one place.
//
// THE RULE UNDER TEST, in one sentence: the base fee is the supporter's
// guarantee, so once they have committed, cancelling still pays it.
//
// Everything here is a table of (state, what it should cost), because that is
// exactly the shape of the thing that was wrong. Before this, a cancel paid
// the supporter only if a worklog had been CLOSED first, which produced two
// failures on the build 11 device run:
//
//   - a cancel with a 7-minute session still open discarded the session,
//     released the full $12, and paid the supporter nothing for work they were
//     in the middle of;
//   - a cancel on an accepted task with nothing logged was refused outright,
//     leaving ops as the only way out of an accepted task.
//
//	docker run -d --rm --name hora-b12-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55440:5432 postgres:16
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55440/horatest' \
//	  go test ./ -run Cancellation -v

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ── 1. Arithmetic, no DB ───────────────────────────────────────────────────

// What a cancel owes, by state. The base fee is a FLOOR and not a component
// that time can substitute for: a committed cancel with zero minutes owes the
// whole $12, which is the case the old `hadSession` gate answered with zero.
func TestCancellationSettlementByState(t *testing.T) {
	if Billing.BaseFeeDefaultCents != 1200 || Billing.IncludedMinutes != 15 || Billing.PerMinuteRateCents != 50 {
		t.Fatalf("this table is written against $12.00 base / 15 included / 50c per minute; BillingConfig now says %d/%d/%d",
			Billing.BaseFeeDefaultCents, Billing.IncludedMinutes, Billing.PerMinuteRateCents)
	}

	cases := []struct {
		name      string
		category  string
		minutes   int
		committed bool
		want      int
	}{
		// Nobody committed: free, whatever is on the row. The minutes column
		// is deliberately non-zero on the second row — an uncommitted task
		// with logged time is not a real state, and if it ever became one it
		// must not start charging for it silently.
		{"open, unaccepted", "errand", 0, false, 0},
		{"uncommitted with minutes somehow logged", "errand", 40, false, 0},

		// Committed. The base fee is owed regardless, and the first row is the
		// one the old rule got wrong: accepted, travelling, nothing logged.
		{"accepted, nothing logged", "errand", 0, true, 1200},
		{"accepted, inside the included block", "errand", 10, true, 1200},
		{"accepted, exactly at the inclusion", "errand", 15, true, 1200},
		{"accepted, 22 min worked", "errand", 22, true, 1550},
		{"accepted, 40 min worked", "errand", 40, true, 2450},

		// Companionship carries its own floor, and the guarantee is that
		// floor rather than the default one.
		{"companionship, nothing logged", "companionship", 0, true, 2500},
		{"companionship, 40 min worked", "companionship", 40, true, 3750},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cancelSettlementCents(tc.category, tc.minutes, tc.committed, Billing.PerMinuteRateCents)
			if got != tc.want {
				t.Errorf("cancel owes %s, want %s", formatCentsUSD(got), formatCentsUSD(tc.want))
			}
			// The promise, stated separately from the arithmetic that
			// currently satisfies it: a committed cancel is never below the
			// base fee. A future rate change must break this line, not the
			// supporter.
			if tc.committed && got < baseFeeCents(tc.category) {
				t.Errorf("committed cancel paid %s, below the %s base fee it guarantees",
					formatCentsUSD(got), formatCentsUSD(baseFeeCents(tc.category)))
			}
		})
	}
}

// The grace window: an undo, not an option. Measured from acceptance against
// the server's clock, and a task with no accepted_at is NOT inside it.
func TestCancellationGraceWindowBoundary(t *testing.T) {
	if Billing.CancelGraceMinutes != 2 {
		t.Fatalf("this test is written against a 2-minute grace window; BillingConfig says %d",
			Billing.CancelGraceMinutes)
	}
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { ts := now.Add(-d); return &ts }

	cases := []struct {
		name        string
		acceptedAt  *time.Time
		wantInside  bool
		wantLeftSec int
	}{
		{"accepted this instant", at(0), true, 120},
		{"one second in", at(time.Second), true, 119},
		{"one second before the boundary", at(2*time.Minute - time.Second), true, 1},
		// EXACTLY at the boundary is OUTSIDE. Somewhere has to be, and the
		// direction that favours the supporter is the one that matches the
		// promise the window is carved out of.
		{"exactly at the boundary", at(2 * time.Minute), false, 0},
		{"one second past", at(2*time.Minute + time.Second), false, 0},
		{"an hour past", at(time.Hour), false, 0},
		// No acceptance timestamp: a task nobody accepted, or one accepted
		// before the column existed. Not inside — the alternative hands a free
		// cancel to a task accepted three weeks ago.
		{"no accepted_at", nil, false, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cancelIsWithinGrace(tc.acceptedAt, now); got != tc.wantInside {
				t.Errorf("within grace = %v, want %v", got, tc.wantInside)
			}
			if got := int(cancelGraceRemaining(tc.acceptedAt, now).Seconds()); got != tc.wantLeftSec {
				t.Errorf("%d seconds left, want %d", got, tc.wantLeftSec)
			}
			// The countdown and the verdict cannot disagree: a client showing
			// "free for 0:00 more" while the server charges, or the reverse,
			// is the dialog lying at the exact moment somebody commits.
			if (cancelGraceRemaining(tc.acceptedAt, now) > 0) != cancelIsWithinGrace(tc.acceptedAt, now) {
				t.Error("the countdown and the charge decision disagree")
			}
		})
	}
}

// ── 2. The handler, end to end ─────────────────────────────────────────────

// Every state × the amount it charges, through the real handler.
func TestCancellationChargesByState(t *testing.T) {
	cases := []struct {
		name string
		// How the task is set up before the cancel.
		accepted     bool
		acceptedAgo  time.Duration
		sessionMins  int
		sessionOpen  bool
		wantCode     int
		wantBill     int
		wantMinutes  int
		wantRelease  bool
		wantSupPayee bool
	}{
		{
			name:     "open and unaccepted: free release",
			accepted: false, wantCode: http.StatusOK, wantBill: 0, wantRelease: true,
		},
		{
			// RULE D. This was a 400 — "cannot cancel after it has been
			// accepted" — and ops were the only way out.
			name:     "accepted, nothing logged, past the grace window",
			accepted: true, acceptedAgo: time.Hour,
			wantCode: http.StatusOK, wantBill: 1200, wantSupPayee: true,
		},
		{
			// RULE B's grace carve-out.
			name:     "accepted seconds ago: free release",
			accepted: true, acceptedAgo: 10 * time.Second,
			wantCode: http.StatusOK, wantBill: 0, wantRelease: true,
		},
		{
			name:     "accepted, closed session, past the grace window",
			accepted: true, acceptedAgo: time.Hour, sessionMins: 40,
			wantCode: http.StatusOK, wantBill: 2450, wantMinutes: 40, wantSupPayee: true,
		},
		{
			// RULE C. This was a 400 telling the requester to make somebody
			// else clock out. The session is force-closed and billed.
			name:     "accepted, OPEN session, past the grace window",
			accepted: true, acceptedAgo: time.Hour, sessionMins: 7, sessionOpen: true,
			wantCode: http.StatusOK, wantBill: 1200, wantMinutes: 7, wantSupPayee: true,
		},
		{
			// An open session long enough to bill past the inclusion, so the
			// force-close is visible in the money and not just in the minutes.
			name:     "accepted, OPEN session past the inclusion",
			accepted: true, acceptedAgo: time.Hour, sessionMins: 35, sessionOpen: true,
			wantCode: http.StatusOK, wantBill: 2200, wantMinutes: 35, wantSupPayee: true,
		},
		{
			// The grace window covers an open session too: it is a question
			// about WHEN the cancel happened, not about what has been done.
			// The grace window is a question about WHEN the cancel happened,
			// not about what has been done — so the session is still
			// force-closed and still LOGGED, it simply is not charged.
			name:     "accepted seconds ago with an open session: still free",
			accepted: true, acceptedAgo: 10 * time.Second, sessionMins: 1, sessionOpen: true,
			wantCode: http.StatusOK, wantBill: 0, wantMinutes: 1, wantRelease: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupStripeWebhookDB(t)
			w := seedOpsWorld(t, "open")
			ctx := context.Background()

			if tc.accepted {
				setAcceptedAt(t, w.taskID, tc.acceptedAgo)
			} else if _, err := db.Exec(ctx,
				`update public.tasks set assigned_to_id = null, assigned_to = '', accepted_at = null
				  where id=$1::uuid`, w.taskID); err != nil {
				t.Fatalf("unassign: %v", err)
			}
			switch {
			case tc.sessionMins > 0 && tc.sessionOpen:
				seedOpenSession(t, w.taskID, tc.sessionMins)
			case tc.sessionMins > 0:
				seedWorklog(t, w.taskID, tc.sessionMins, false)
			}

			code, body := cancelAs(t, w.taskID, w.requesterID, "plans changed")
			if code != tc.wantCode {
				t.Fatalf("cancel: %d (%v), want %d", code, body, tc.wantCode)
			}
			if got := num(body["bill_cents"]); got != tc.wantBill {
				t.Errorf("bill = %s, want %s", formatCentsUSD(got), formatCentsUSD(tc.wantBill))
			}
			if got := num(body["total_minutes"]); got != tc.wantMinutes {
				t.Errorf("total_minutes = %d, want %d", got, tc.wantMinutes)
			}
			if got := taskStatus(t, w.taskID); got != "cancelled" {
				t.Fatalf("status = %q, want cancelled", got)
			}

			// NO SESSION IS LEFT OPEN. The whole point of rule c: the work was
			// billed rather than discarded, and a task cannot end with a clock
			// still running on it.
			if openSessions(t, w.taskID) != 0 {
				t.Error("the cancel left an open work session behind")
			}

			// The itemization the confirmation renders adds up to the total.
			if base, time := num(body["base_fee_cents"]), num(body["time_cost_cents"]); base+time != tc.wantBill {
				t.Errorf("base %s + time %s != bill %s",
					formatCentsUSD(base), formatCentsUSD(time), formatCentsUSD(tc.wantBill))
			}

			// THE SETTLEMENT VIEW AGREES WITH THE BILL. Checked against the
			// handler rather than against itself: the view used to re-derive
			// the policy ("cancelled with no worklogs owes nothing"), and a
			// second copy of a rule is a second thing to be wrong.
			cost := getWorklogsAs(t, w.taskID, w.requesterID, requesterEmail)["cost"].(map[string]any)
			if got := num(cost["total_cents"]); got != tc.wantBill {
				t.Errorf("the settlement view says %s, the cancel charged %s",
					formatCentsUSD(got), formatCentsUSD(tc.wantBill))
			}

			// THE SUPPORTER IS PAID FOR EVERY CHARGED CANCEL. Payouts are only
			// dispatched once money actually moves, which needs Stripe, so
			// what is asserted here is the thing that decides it: the
			// settlement ran against a supporter the system can still name.
			if tc.wantSupPayee {
				if got := taskSupporterID(ctx, w.taskID); got != w.supporterID {
					t.Errorf("the charged cancel has no payee: taskSupporterID = %q, want the supporter", got)
				}
				if n := countNotifications(t, w.supporterID, "CANCELLED"); n != 1 {
					t.Errorf("%d supporter CANCELLED notifications, want 1", n)
				}
				if b := notificationBody(t, w.supporterID, "CANCELLED"); !strings.Contains(b, formatCentsUSD(tc.wantBill)) {
					t.Errorf("the supporter is not told what they were paid: %q", b)
				}
			}
			if tc.wantRelease && num(body["bill_cents"]) != 0 {
				t.Error("a free cancel charged something")
			}
		})
	}
}

// The supporter keeps their link to a cancelled task, including the case the
// new rule creates: paid, and never clocked in, so no worklog exists to reach
// it through.
func TestCancellationPreservesTheSupportersAccess(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setAcceptedAt(t, w.taskID, time.Hour)

	code, body := cancelAs(t, w.taskID, w.requesterID, "plans changed")
	if code != http.StatusOK {
		t.Fatalf("cancel: %d (%v)", code, body)
	}
	if got := num(body["bill_cents"]); got != 1200 {
		t.Fatalf("bill = %s, want the $12.00 base fee", formatCentsUSD(got))
	}

	ctx := context.Background()
	var assigned, cancelledAssignee *string
	if err := db.QueryRow(ctx, `
		select assigned_to_id::text, cancelled_assignee_id::text
		  from public.tasks where id=$1::uuid`, w.taskID).Scan(&assigned, &cancelledAssignee); err != nil {
		t.Fatalf("read assignment: %v", err)
	}
	if assigned != nil {
		t.Error("the supporter is still attached to a cancelled task — it will sit in their active list")
	}
	if cancelledAssignee == nil || *cancelledAssignee != w.supporterID {
		t.Fatal("the cancel did not record who was on the task — the supporter it just paid cannot reach it")
	}
	// And the detach did not cost them the payout either.
	if got := taskSupporterID(ctx, w.taskID); got != w.supporterID {
		t.Errorf("taskSupporterID = %q after the detach, want the supporter", got)
	}
}

// What the confirmation dialog is shown BEFORE anything happens, and the
// property that matters: it is the same number the cancel then charges.
func TestCancellationPreviewMatchesTheCharge(t *testing.T) {
	cases := []struct {
		name        string
		acceptedAgo time.Duration
		sessionMins int
		sessionOpen bool
	}{
		{"accepted, nothing logged", time.Hour, 0, false},
		{"accepted, closed session", time.Hour, 40, false},
		{"accepted, open session", time.Hour, 35, true},
		{"inside the grace window", 10 * time.Second, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupStripeWebhookDB(t)
			w := seedOpsWorld(t, "open")
			setAcceptedAt(t, w.taskID, tc.acceptedAgo)
			switch {
			case tc.sessionMins > 0 && tc.sessionOpen:
				seedOpenSession(t, w.taskID, tc.sessionMins)
			case tc.sessionMins > 0:
				seedWorklog(t, w.taskID, tc.sessionMins, false)
			}

			ctx := context.Background()
			var assignedTo *string
			if err := db.QueryRow(ctx,
				`select assigned_to_id::text from public.tasks where id=$1::uuid`, w.taskID).Scan(&assignedTo); err != nil {
				t.Fatalf("read assignment: %v", err)
			}
			preview := cancellationPreview(ctx, w.taskID, assignedTo, 0)
			if preview == nil {
				t.Fatal("no cancellation preview on an open task")
			}
			if !preview.Committed {
				t.Error("preview says nobody committed on an accepted task")
			}
			if len(preview.Reasons) == 0 {
				t.Error("the preview ships no reason presets — the dialog has nothing to offer")
			}

			// THE PROPERTY. A preview that can disagree with the thing it
			// previews is worse than no preview: it is a number somebody
			// decided on that turns out to be wrong afterwards.
			code, body := cancelAs(t, w.taskID, w.requesterID, "plans changed")
			if code != http.StatusOK {
				t.Fatalf("cancel: %d (%v)", code, body)
			}
			if got := num(body["bill_cents"]); got != preview.ChargeCents {
				t.Errorf("the dialog promised %s and the cancel charged %s",
					formatCentsUSD(preview.ChargeCents), formatCentsUSD(got))
			}
			if preview.WithinGrace != (num(body["bill_cents"]) == 0) {
				t.Errorf("preview within_grace=%v but the charge was %s",
					preview.WithinGrace, formatCentsUSD(num(body["bill_cents"])))
			}
			// The open session counts in the preview too. A requester quoted a
			// number that ignores the forty minutes their supporter is in the
			// middle of would be quoted the wrong number.
			if tc.sessionMins > 0 && preview.BilledMinutes != tc.sessionMins {
				t.Errorf("preview billed_minutes = %d, want %d — the running session was ignored",
					preview.BilledMinutes, tc.sessionMins)
			}
		})
	}
}

// The countdown the dialog renders comes off the server's clock, as a
// deadline, and it is absent once the window has shut.
func TestCancellationPreviewCarriesTheGraceDeadline(t *testing.T) {
	ctx := context.Background()

	t.Run("inside the window", func(t *testing.T) {
		setupStripeWebhookDB(t)
		w := seedOpsWorld(t, "open")
		setAcceptedAt(t, w.taskID, 30*time.Second)
		assigned := w.supporterID
		preview := cancellationPreview(ctx, w.taskID, &assigned, 5000)
		if !preview.WithinGrace {
			t.Fatal("a task accepted 30 seconds ago is outside the 2-minute window")
		}
		if preview.GraceEndsAt == nil {
			t.Fatal("no deadline to count down to")
		}
		if left := time.Until(*preview.GraceEndsAt); left <= 0 || left > 90*time.Second {
			t.Errorf("deadline is %v away, want something inside the remaining ~90s", left)
		}
		if preview.ChargeCents != 0 {
			t.Errorf("a free cancel previews a %s charge", formatCentsUSD(preview.ChargeCents))
		}
		// AND IT STILL NAMES THE FEE AT STAKE. Found on the build 12 visual
		// pass: zeroing the breakdown along with the charge left the live
		// countdown reading "after that, the base fee goes to your supporter"
		// with no number in it — and the number is the whole sentence.
		if preview.BaseFeeCents != 1200 {
			t.Errorf("base_fee_cents = %s inside the grace window, want the $12.00 the countdown has to name",
				formatCentsUSD(preview.BaseFeeCents))
		}
		if preview.ReleaseCents != 5000 {
			t.Errorf("release = %s, want the whole $50.00 hold", formatCentsUSD(preview.ReleaseCents))
		}
	})

	t.Run("after the window", func(t *testing.T) {
		setupStripeWebhookDB(t)
		w := seedOpsWorld(t, "open")
		setAcceptedAt(t, w.taskID, 10*time.Minute)
		assigned := w.supporterID
		preview := cancellationPreview(ctx, w.taskID, &assigned, 5000)
		if preview.WithinGrace || preview.GraceEndsAt != nil {
			t.Error("the window is still being advertised after it shut")
		}
		if preview.ChargeCents != 1200 {
			t.Errorf("charge = %s, want the $12.00 base fee", formatCentsUSD(preview.ChargeCents))
		}
		if preview.ReleaseCents != 3800 {
			t.Errorf("release = %s, want $38.00 of the $50.00 hold", formatCentsUSD(preview.ReleaseCents))
		}
	})
}

// ── Helpers ────────────────────────────────────────────────────────────────

// setAcceptedAt puts the task's acceptance a given distance in the past, which
// is how every grace-window case is set up. seedOpsWorld assigns a supporter
// but writes no accepted_at — the same shape as a task accepted before the
// column existed.
func setAcceptedAt(t *testing.T, taskID string, ago time.Duration) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `
		update public.tasks set accepted_at = now() - make_interval(secs => $2)
		 where id = $1::uuid
	`, taskID, ago.Seconds()); err != nil {
		t.Fatalf("set accepted_at: %v", err)
	}
}

// seedOpenSession starts a session that is still running, `minutes` long as of
// the moment the cancel force-closes it.
//
// WHY NOT seedWorklog(..., openEnded=true). That starts the session exactly N
// minutes ago, and per-session rounding is ceil-with-a-one-minute-floor — so
// by the time the handler writes end_at = now(), a few milliseconds have
// passed and N minutes has become N+1. The rounding is correct (a supporter is
// not billed down for the seconds a request took) and it is the test that has
// to be honest about it. Starting five seconds short of the mark puts the
// force-close comfortably inside the same ceil bucket.
func seedOpenSession(t *testing.T, taskID string, minutes int) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `
		insert into public.worklogs (task_id, "user", start_at, end_at)
		values ($1::uuid, $2, now() - make_interval(mins => $3, secs => -5), null)
	`, taskID, oldSupporterEmail, minutes); err != nil {
		t.Fatalf("seed open session: %v", err)
	}
}

func openSessions(t *testing.T, taskID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(),
		`select count(*) from public.worklogs where task_id=$1::uuid and end_at is null`,
		taskID).Scan(&n); err != nil {
		t.Fatalf("count open sessions: %v", err)
	}
	return n
}
