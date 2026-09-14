package main

// Phase 2b: the three-layer time cap.
//
// The problem it solves: a supporter is forty minutes into a task estimated at
// thirty, the requester has no idea, and the first either of them learns about
// it is the bill. Three layers, each answering a different question:
//
//	LAYER 2  ~5 min left     both parties told. The supporter can ask for more
//	         (warning)       time in one tap; the requester can grant it in one.
//	                         Fires whether or not consent was given, because the
//	                         point is that NOBODY is surprised.
//
//	LAYER 1  the ceiling     billing stops accruing. With auto-extend consent
//	         (auto-extend)   the ceiling is estimate + 15, so those 15 minutes
//	                         bill normally with no interruption at all. Without
//	                         it, the ceiling is the estimate. Past the ceiling
//	                         the supporter keeps working if they judge it right;
//	                         it simply costs nothing.
//
//	LAYER 3  +30 min         ops are emailed. The requester has been asked twice
//	         (unresponsive)  and said nothing; a supporter is working unpaid
//	                         time on someone else's job and a human should look.
//
// NO AUTOMATIC CANCELLATION, EVER. Not at the ceiling, not after the grace
// period, not at any point. The task stays completable at any time and settles
// at the capped amount. A system that ends a task from a timer ends it while
// somebody is standing in a stranger's kitchen.
//
// WHERE THIS RUNS, AND WHY THERE IS NO SCHEDULER
//
// evaluateTimeCap is called from paths that are already happening while a
// supporter works:
//
//	POST /tasks/:id/gps-ping   every 30s from the supporter's phone while
//	                           clocked in — the real heartbeat
//	GET  /tasks/:id/worklogs   both task screens, on focus and on pull
//	POST /tasks/:id/clock-out  the moment a session closes
//	the 6h pre-auth watcher    backstop for a task whose supporter went dark
//
// The GPS ping is the one that matters: it is the only signal that arrives on
// its own while a phone is locked, which is exactly the situation the warning
// exists for. Everything else is belt and braces.
//
// EACH LAYER FIRES ONCE PER TASK, and "once" is enforced by the database, not
// by a flag in memory: each latch is claimed with `UPDATE … WHERE col IS NULL
// RETURNING`, so two pings landing in the same second produce one
// notification. Approving a time extension clears all three latches, so the
// sequence repeats cleanly against the new ceiling.

import (
	"context"
	"fmt"
	"log"
	"time"

	notify "hora-auth/internal/notify"
)

// timeCapState is what the clients render: where the task is against its
// ceiling, right now. Included in the /worklogs payload so the supporter's
// screen can say "time cap reached — awaiting approval" without deriving it.
type timeCapState struct {
	Cap TimeCap `json:"cap"`
	// Logged includes the session currently running, unlike billing's
	// totalClosedMinutes — a warning that waits for a clock-out arrives after
	// the thing it was warning about.
	LoggedMinutes int `json:"logged_minutes"`
	// True once LoggedMinutes has passed CapMinutes: billing has stopped.
	Reached bool `json:"reached"`
	// True between the warning threshold and the ceiling.
	Warning bool `json:"warning"`
	// Minutes of consented time left, floored at zero.
	RemainingMinutes int `json:"remaining_minutes"`
}

// liveLoggedMinutes is total logged time INCLUDING any session still running.
//
// Deliberately a different number from billing's totalClosedMinutes, and the
// difference is the whole point: billing only counts finished sessions, but a
// cap that only notices closed sessions cannot warn anybody while they work.
// Per-session ceil-with-a-one-minute-floor rounding is kept identical so the
// two numbers agree the instant a session closes.
func liveLoggedMinutes(ctx context.Context, taskID string) int {
	var total int
	_ = db.QueryRow(ctx, `
		with x as (
			select ceil(extract(epoch from (coalesce(end_at, now()) - start_at))/60.0)::int as m
			from public.worklogs
			where task_id = $1::uuid and coalesce(end_at, now()) > start_at
		)
		select coalesce(sum(greatest(m,1)), 0) from x
	`, taskID).Scan(&total)
	return total
}

// readTimeCapState answers "where is this task against its ceiling" without
// sending anything. The read half of evaluateTimeCap, used by the settlement
// payload.
func readTimeCapState(ctx context.Context, taskID string) timeCapState {
	capMinutes, detail := taskTimeCapMinutes(ctx, taskID)
	logged := liveLoggedMinutes(ctx, taskID)

	st := timeCapState{Cap: detail, LoggedMinutes: logged}
	if capMinutes <= 0 {
		return st
	}
	st.Reached = logged >= capMinutes
	st.Warning = !st.Reached && logged >= detail.WarnAtMinutes
	if remaining := capMinutes - logged; remaining > 0 {
		st.RemainingMinutes = remaining
	}
	return st
}

// evaluateTimeCap is the whole mechanism: read where the task stands, and fire
// whichever layers have not fired yet.
//
// Cheap enough for the GPS-ping path — one cap read, one minutes read, and a
// latch UPDATE only when something actually has to be said — and it returns
// immediately for the overwhelming majority of calls, where the supporter is
// comfortably inside the estimate.
//
// Best-effort throughout. A notification that fails to send must not fail the
// ping that triggered it.
func evaluateTimeCap(ctx context.Context, taskID string) {
	st := readTimeCapState(ctx, taskID)
	if st.Cap.CapMinutes <= 0 {
		return
	}
	// Only an active task has a cap worth enforcing. A completed or cancelled
	// task's clock has stopped.
	var status string
	var assigneeID *string
	if err := db.QueryRow(ctx,
		`select status, assigned_to_id::text from public.tasks where id=$1::uuid`,
		taskID).Scan(&status, &assigneeID); err != nil {
		return
	}
	if status != "open" || assigneeID == nil {
		return
	}

	if st.Warning || st.Reached {
		fireTimeCapWarning(ctx, taskID, st)
	}
	if st.Reached {
		fireTimeCapReached(ctx, taskID, st)
		fireTimeCapOpsAlert(ctx, taskID, st)
	}
}

// claimLatch takes a once-per-task latch, atomically. Returns false when
// somebody else already has it.
func claimLatch(ctx context.Context, taskID, column string) bool {
	// The column name is a literal from the three call sites below, never user
	// input — a parameter cannot name a column, and the alternative (three
	// near-identical statements) buys nothing.
	sql := fmt.Sprintf(`
		update public.tasks set %s = now()
		 where id = $1::uuid and %s is null
		returning id`, column, column)
	var id string
	return db.QueryRow(ctx, sql, taskID).Scan(&id) == nil
}

// fireTimeCapWarning — Layer 2. Both parties, once.
//
// Fires on Reached as well as Warning: a supporter who clocks a 40-minute
// session in one go against a 30-minute estimate blows straight past the
// warning threshold without any evaluation landing in between, and arriving at
// the ceiling having never been warned is the exact surprise this exists to
// prevent.
func fireTimeCapWarning(ctx context.Context, taskID string, st timeCapState) {
	if !claimLatch(ctx, taskID, "time_cap_warned_at") {
		return
	}
	t, err := loadAdminTask(ctx, taskID)
	if err != nil {
		return
	}
	log.Printf("[timecap] warning task=%s logged=%d cap=%d", taskID, st.LoggedMinutes, st.Cap.CapMinutes)

	supporterBody := fmt.Sprintf(
		"About %d minutes left on the time %s asked for. Need longer? Ask for more in the app — they can approve it in one tap.",
		maxInt(st.RemainingMinutes, 0), displayName(t.RequesterEmail))
	if st.Reached {
		supporterBody = fmt.Sprintf(
			"You've reached the time %s asked for. Need longer? Ask for more in the app.",
			displayName(t.RequesterEmail))
	}
	notifyUser(ctx, derefOrEmpty(t.AssigneeID), t.AssigneeEmail, notify.CreateNotificationInput{
		TaskID:    taskID,
		Type:      "TIME_CAP_WARNING",
		Title:     "About 5 minutes left on this task",
		Body:      supporterBody,
		TaskTitle: t.Title,
	})

	notifyUser(ctx, t.RequesterID, t.RequesterEmail, notify.CreateNotificationInput{
		TaskID: taskID,
		Type:   "TIME_CAP_WARNING",
		Title:  "Your task is close to its time limit",
		Body: fmt.Sprintf(
			"%s is about %d minutes from the %d minutes you asked for on %q. If they need longer they'll ask, and you can approve it in one tap.",
			displayName(t.AssigneeEmail), maxInt(st.RemainingMinutes, 0), st.Cap.CapMinutes, t.Title),
		TaskTitle:     t.Title,
		SupporterName: displayName(t.AssigneeEmail),
	})
}

// fireTimeCapReached — Layer 1's announcement. Billing has stopped.
func fireTimeCapReached(ctx context.Context, taskID string, st timeCapState) {
	if !claimLatch(ctx, taskID, "time_cap_reached_at") {
		return
	}
	t, err := loadAdminTask(ctx, taskID)
	if err != nil {
		return
	}
	log.Printf("[timecap] reached task=%s logged=%d cap=%d", taskID, st.LoggedMinutes, st.Cap.CapMinutes)
	writeAudit(ctx, taskID, systemActorUID, "TIME_CAP_REACHED", "", map[string]any{
		"logged_minutes": st.LoggedMinutes,
		"cap_minutes":    st.Cap.CapMinutes,
		"consent":        st.Cap.AutoExtendConsent,
	})

	notifyUser(ctx, derefOrEmpty(t.AssigneeID), t.AssigneeEmail, notify.CreateNotificationInput{
		TaskID: taskID,
		Type:   "TIME_CAP_REACHED",
		Title:  "Time cap reached",
		Body: fmt.Sprintf(
			"You've reached the %d minutes agreed for %q, so time past this isn't billed. Ask for more time in the app if you need it — and wrap up safely whenever you judge it right.",
			st.Cap.CapMinutes, t.Title),
		TaskTitle: t.Title,
	})

	notifyUser(ctx, t.RequesterID, t.RequesterEmail, notify.CreateNotificationInput{
		TaskID: taskID,
		Type:   "TIME_CAP_REACHED",
		Title:  "Your supporter has reached the time limit",
		Body: fmt.Sprintf(
			"%s has worked the %d minutes agreed for %q and isn't being paid for anything beyond it. Approve 15 or 30 more minutes in the app if the job isn't finished.",
			displayName(t.AssigneeEmail), st.Cap.CapMinutes, t.Title),
		TaskTitle:     t.Title,
		SupporterName: displayName(t.AssigneeEmail),
	})
}

// fireTimeCapOpsAlert — Layer 3. The requester has gone quiet for the whole
// grace period while somebody works unpaid on their task.
//
// Ops get an email, not a cancellation. What a human does with it — ring the
// requester, ring the supporter, adjust the settlement afterwards — is a
// judgement call, and the point of the alert is that a person makes it.
func fireTimeCapOpsAlert(ctx context.Context, taskID string, st timeCapState) {
	var reachedAt *time.Time
	if err := db.QueryRow(ctx,
		`select time_cap_reached_at from public.tasks where id=$1::uuid`, taskID).Scan(&reachedAt); err != nil {
		return
	}
	if reachedAt == nil {
		return
	}
	if time.Since(*reachedAt) < time.Duration(Billing.GracePeriodMinutes)*time.Minute {
		return
	}
	// A pending time request means the requester has not been asked and
	// ignored — they have been asked and the clock is still running on their
	// five minutes. Not an ops matter yet.
	var pending bool
	_ = db.QueryRow(ctx, `
		select exists (
			select 1 from public.extension_requests
			 where task_id = $1::uuid and kind = 'time' and status = 'pending'
		)`, taskID).Scan(&pending)
	if pending {
		return
	}
	if !claimLatch(ctx, taskID, "time_cap_ops_alerted_at") {
		return
	}

	t, err := loadAdminTask(ctx, taskID)
	if err != nil {
		return
	}
	over := st.LoggedMinutes - st.Cap.CapMinutes
	log.Printf("[timecap][OPS] task=%s unresponsive requester — %d min past cap=%d",
		taskID, over, st.Cap.CapMinutes)
	writeAudit(ctx, taskID, systemActorUID, "TIME_CAP_UNRESPONSIVE", "", map[string]any{
		"logged_minutes":  st.LoggedMinutes,
		"cap_minutes":     st.Cap.CapMinutes,
		"over_by_minutes": over,
		"requester_email": t.RequesterEmail,
		"supporter_email": t.AssigneeEmail,
	})

	emailOpsAdmins(
		fmt.Sprintf("[HO:RA] Unresponsive requester — %s past the time cap", formatMinutes(over)),
		fmt.Sprintf(`<p><strong>A supporter is working past the agreed time with no approval.</strong></p>
<p>The cap was reached %s ago and the requester has not responded since. Time past the cap is not
being billed, so the supporter is working unpaid. The task has NOT been cancelled and remains
completable at any time — it will settle at the capped amount.</p>
<ul>
  <li>Task: %s (%s)</li>
  <li>Requester: %s</li>
  <li>Supporter: %s</li>
  <li>Logged: %s against a cap of %s</li>
</ul>
<p>Worth a phone call to both sides.</p>`,
			formatMinutes(Billing.GracePeriodMinutes),
			t.Title, taskID, t.RequesterEmail, t.AssigneeEmail,
			formatMinutes(st.LoggedMinutes), formatMinutes(st.Cap.CapMinutes)))
}

// sweepTimeCaps is the backstop in the existing 6h watcher — no new scheduler.
//
// It catches the task whose supporter stopped pinging (phone dead, app killed)
// and whose screens nobody has opened since. Six-hour granularity is coarse
// for a 30-minute grace window, and deliberately so: this is not the primary
// path, the GPS heartbeat is. It exists so a task cannot sit past its cap
// forever with nobody told.
func sweepTimeCaps(ctx context.Context) {
	rows, err := db.Query(ctx, `
		select distinct t.id::text
		  from public.tasks t
		  join public.worklogs w on w.task_id = t.id
		 where t.status = 'open'
		   and t.assigned_to_id is not null
		   and coalesce(t.estimated_minutes, 0) > 0
		   and t.time_cap_ops_alerted_at is null
	`)
	if err != nil {
		log.Printf("[timecap][sweep] %v", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rows.Close()

	for _, id := range ids {
		evaluateTimeCap(ctx, id)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// watchPhase2bBacklog is the shared backstop ticker: expire what nobody
// answered, and re-evaluate caps for tasks whose supporter went quiet.
//
// Deliberately the SAME cadence as watchExpiringPreAuths and deliberately not
// its own scheduler. Neither job is time-critical — the request a supporter is
// waiting on is expired by their own polling screen within seconds, and a cap
// is evaluated on every GPS ping — so this exists only so that a task nobody
// is looking at cannot sit in a pending state forever.
func watchPhase2bBacklog(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepExpiredExtensions(ctx)
			sweepTimeCaps(ctx)
		}
	}
}
