package main

// Phase 2b: the three-layer time cap.
//
// The problem it solves: a supporter is forty minutes into a task estimated at
// thirty, the requester has no idea, and the first either of them learns about
// it is the bill. Three layers, each answering a different question:
//
//	LAYER 2  ~5 min before   both parties told. The supporter can ask for more
//	         the ESTIMATE    time in one tap; the requester can grant it in one.
//	         (warning)       Fires whether or not consent was given, because the
//	                         point is that NOBODY is surprised.
//
//	                         ANCHORED TO THE ESTIMATE, NOT THE CEILING. With
//	                         auto-extend on, a 30-minute task warns at 25 and
//	                         stops billing at 45. Warning at 40 — which is what
//	                         ceiling-minus-5 did — announced the estimate ten
//	                         minutes after it had passed. See
//	                         timeCapWarningMinutes.
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
// THE CLOCK, HOWEVER, STOPS AT THE CEILING (Arbiter 2026-10-24 23:34, after
// a staging session ran 80 minutes against a 60-minute cap). Reaching the
// cap clocks the running session out at the cap minute — the task stays
// open, the supporter can ask for more time and clock in again once it is
// approved. Nothing is cancelled; the record simply does not say the person
// worked time nobody agreed to.
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

	// The clock stops at the ceiling (D-06, Arbiter 2026-10-24 23:34) —
	// FIRST, and on every evaluation, not behind the once-per-task latch:
	// a session clocked in again without an approval, or one running when
	// a failure spent the latch, is cut just the same. Nothing is written
	// when no session is open.
	var closed int
	var stillOpen bool
	if st.Reached {
		var err error
		closed, stillOpen, err = clockOutAtCap(ctx, taskID, st.Cap.CapMinutes)
		if err != nil {
			// Not claimed: the next evaluation (a minute away while a
			// session runs) cuts and tells, with words that are true.
			log.Printf("[timecap][ERROR] clock out at cap task=%s: %v", taskID, err)
			return
		}
	}
	if st.Warning || st.Reached {
		fireTimeCapWarning(ctx, taskID, st)
	}
	if st.Reached {
		fireTimeCapReached(ctx, taskID, st, closed)
		fireTimeCapOpsAlert(ctx, taskID, st, stillOpen)
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

	// THREE SHAPES, and which one is sent is decided by what is actually true
	// about this task's ceiling — not by one sentence bent to cover all of
	// them.
	//
	// The auto-extend case is the one this exists for. The warning now fires
	// at the ESTIMATE (billing.go timeCapWarningMinutes), so on a 30-minute
	// task with consent it lands at 25 logged minutes with 20 minutes of
	// ceiling still above it. "About 20 minutes left" would be true of the
	// ceiling and useless as a warning: the supporter needs to know they are
	// at the number the requester actually planned around, and that the fuse
	// above it is 15 minutes long and not a fresh estimate.
	supporterTitle := "About 5 minutes left on this task"
	supporterBody := fmt.Sprintf(
		"About %d minutes left on the time %s asked for. Need longer? Ask for more in the app — they can approve it in one tap.",
		maxInt(st.RemainingMinutes, 0), t.RequesterName)

	requesterTitle := "Your task is close to its time limit"
	requesterBody := fmt.Sprintf(
		"%s is about %d minutes from the %d minutes you asked for on %q. If they need longer they'll ask, and you can approve it in one tap.",
		t.AssigneeName, maxInt(st.RemainingMinutes, 0), st.Cap.CapMinutes, t.Title)

	switch {
	case st.Reached:
		supporterTitle = "Time cap in sight"
		supporterBody = fmt.Sprintf(
			"You've reached the time %s asked for. Need longer? Ask for more in the app.",
			t.RequesterName)
	case st.Cap.AutoExtendMinutes > 0:
		supporterTitle = fmt.Sprintf("Approaching the %d-min estimate", st.Cap.AgreedMinutes)
		supporterBody = fmt.Sprintf(
			"Approaching the %d-min estimate — up to %d more minutes are covered by auto-extend; request more if you'll need beyond that.",
			st.Cap.AgreedMinutes, st.Cap.AutoExtendMinutes)

		requesterTitle = "Your task is nearing its estimate"
		requesterBody = fmt.Sprintf(
			"%s is nearing your %d-min estimate; the extra %d min you allowed will start counting.",
			t.AssigneeName, st.Cap.AgreedMinutes, st.Cap.AutoExtendMinutes)
	}

	notifyUser(ctx, derefOrEmpty(t.AssigneeID), t.AssigneeEmail, notify.CreateNotificationInput{
		TaskID:    taskID,
		Type:      "TIME_CAP_WARNING",
		Title:     supporterTitle,
		Body:      supporterBody,
		TaskTitle: t.Title,
	})

	notifyUser(ctx, t.RequesterID, t.RequesterEmail, notify.CreateNotificationInput{
		TaskID:        taskID,
		Type:          "TIME_CAP_WARNING",
		Title:         requesterTitle,
		Body:          requesterBody,
		TaskTitle:     t.Title,
		SupporterName: t.AssigneeName,
	})
}

// fireTimeCapReached — Layer 1's announcement. Billing has stopped.
func fireTimeCapReached(ctx context.Context, taskID string, st timeCapState, closed int) {
	if !claimLatch(ctx, taskID, "time_cap_reached_at") {
		return
	}
	t, err := loadAdminTask(ctx, taskID)
	if err != nil {
		return
	}
	log.Printf("[timecap] reached task=%s logged=%d cap=%d sessions_closed=%d", taskID, st.LoggedMinutes, st.Cap.CapMinutes, closed)
	writeAudit(ctx, taskID, systemActorUID, "TIME_CAP_REACHED", "", map[string]any{
		"logged_minutes":  st.LoggedMinutes,
		"cap_minutes":     st.Cap.CapMinutes,
		"consent":         st.Cap.AutoExtendConsent,
		"sessions_closed": closed,
	})

	// The words match what happened: a session was cut at the cap, or
	// there was none running (a manual clock-out past the cap).
	supporterBody := fmt.Sprintf(
		"You've reached the %d minutes agreed for %q, so your session has been clocked out there — time past it isn't billed. If the job isn't done, ask for more time in the app and clock in again once it's approved.",
		st.Cap.CapMinutes, t.Title)
	requesterBody := fmt.Sprintf(
		"%s has worked the %d minutes agreed for %q, so the clock has been stopped there. Approve 15 or 30 more minutes in the app if the job isn't finished.",
		t.AssigneeName, st.Cap.CapMinutes, t.Title)
	if closed == 0 {
		supporterBody = fmt.Sprintf(
			"You've reached the %d minutes agreed for %q — time past it isn't billed. If the job isn't done, ask for more time in the app before clocking in again.",
			st.Cap.CapMinutes, t.Title)
		requesterBody = fmt.Sprintf(
			"%s has worked the %d minutes agreed for %q and isn't being paid for anything beyond it. Approve 15 or 30 more minutes in the app if the job isn't finished.",
			t.AssigneeName, st.Cap.CapMinutes, t.Title)
	}
	notifyUser(ctx, derefOrEmpty(t.AssigneeID), t.AssigneeEmail, notify.CreateNotificationInput{
		TaskID: taskID, Type: "TIME_CAP_REACHED", Title: "Time cap reached", Body: supporterBody, TaskTitle: t.Title,
	})
	notifyUser(ctx, t.RequesterID, t.RequesterEmail, notify.CreateNotificationInput{
		TaskID: taskID, Type: "TIME_CAP_REACHED", Title: "Your supporter has reached the time limit", Body: requesterBody,
		TaskTitle: t.Title, SupporterName: t.AssigneeName,
	})
}

// clockOutAtCap ends the running session at the cap minute: end_at is the
// session's start plus whatever of the cap the earlier closed sessions left,
// never in the future. A session started with nothing left (clocked in
// again without an approval) is closed at its start: zero minutes, so it
// neither bills nor eats into the next approval. Returns the sessions
// closed and whether one is still open afterwards (the UPDATE failed, or a
// clock-in landed in between — the sweep returns in a minute).
func clockOutAtCap(ctx context.Context, taskID string, capMinutes int) (closed int, stillOpen bool, err error) {
	var open int
	if err := db.QueryRow(ctx,
		`select count(*) from public.worklogs where task_id = $1::uuid and end_at is null`, taskID).Scan(&open); err != nil {
		return 0, false, fmt.Errorf("open sessions: %w", err)
	}
	if open == 0 {
		return 0, false, nil
	}
	closedBefore, err := totalClosedMinutes(ctx, taskID)
	if err != nil {
		return 0, true, fmt.Errorf("minutes: %w", err)
	}
	remaining := maxInt(capMinutes-closedBefore, 0)
	tag, err := db.Exec(ctx, `
		update public.worklogs
		   set end_at = least(now(), start_at + make_interval(mins => $2)), updated_at = now()
		 where task_id = $1::uuid and end_at is null
	`, taskID, remaining)
	if err != nil {
		return 0, true, fmt.Errorf("update: %w", err)
	}
	closed = int(tag.RowsAffected())
	if closed > 0 {
		log.Printf("[timecap] clock out at cap task=%s cap=%d remaining=%d sessions_closed=%d", taskID, capMinutes, remaining, closed)
	}
	if err := db.QueryRow(ctx,
		`select exists (select 1 from public.worklogs where task_id = $1::uuid and end_at is null)`, taskID).Scan(&stillOpen); err != nil {
		return closed, true, fmt.Errorf("recheck: %w", err)
	}
	return closed, stillOpen, nil
}

// fireTimeCapOpsAlert — Layer 3. The requester has gone quiet for the whole
// grace period while somebody works unpaid on their task.
//
// Ops get an email, not a cancellation. What a human does with it — ring the
// requester, ring the supporter, adjust the settlement afterwards — is a
// judgement call, and the point of the alert is that a person makes it.
//
// Two shapes since the clock stops at the cap (D-06 addendum 2): a session
// the cut could not close (someone IS working unpaid), or a task stalled at
// the cap — clock stopped, job not finished, no request pending, nobody
// answering. The words say which.
func fireTimeCapOpsAlert(ctx context.Context, taskID string, st timeCapState, stillOpen bool) {
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
	over := 0
	if stillOpen { // figures after the cut, not the state read before it
		over = maxInt(liveLoggedMinutes(ctx, taskID)-st.Cap.CapMinutes, 0)
	}
	log.Printf("[timecap][OPS] task=%s stalled at cap=%d logged=%d session_open=%t",
		taskID, st.Cap.CapMinutes, st.LoggedMinutes, stillOpen)
	writeAudit(ctx, taskID, systemActorUID, "TIME_CAP_UNRESPONSIVE", "", map[string]any{
		"logged_minutes":  st.LoggedMinutes,
		"cap_minutes":     st.Cap.CapMinutes,
		"over_by_minutes": over,
		"session_open":    stillOpen,
		"requester_email": t.RequesterEmail,
		"supporter_email": t.AssigneeEmail,
	})

	subject := "[HO:RA] Task stalled at its time cap — no answer from the requester"
	lead := `<p><strong>The clock was stopped at the agreed time; the job is not marked done and nobody has answered.</strong></p>
<p>The cap was reached %s ago. The session was clocked out there and nothing past it is billed; no time request is
pending. The task has NOT been cancelled and remains completable at any time — it will settle at the capped amount.</p>`
	if stillOpen {
		subject = fmt.Sprintf("[HO:RA] Supporter still clocked in — %s past the time cap", formatMinutes(over))
		lead = `<p><strong>A session is still running past the agreed time and could not be clocked out.</strong></p>
<p>The cap was reached %s ago and the requester has not responded since. Time past the cap is not
being billed, so the supporter is working unpaid. The task has NOT been cancelled and remains
completable at any time — it will settle at the capped amount.</p>`
	}
	emailOpsAdmins(subject, fmt.Sprintf(lead+`
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
			log.Printf("[timecap][sweep] scan: %v", err)
			break
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[timecap][sweep] rows: %v", err)
	}
	rows.Close()

	for _, id := range ids {
		evaluateTimeCap(ctx, id)
	}
}

// sweepLiveTimeCaps is the minute-by-minute pass over tasks with a session
// RUNNING right now (end_at is null). The GPS heartbeat only comes from the
// mobile app; a supporter working from the web app sends none, and on
// staging (2026-10-24) a 60-minute cap was announced at minute 73 — when a
// screen happened to be opened. Billing was capped correctly; the people
// were told late. With a session open, the clock is the signal: one cheap
// query a minute, evaluate each. Idle tasks (no session) stay with the 6h
// backstop.
func sweepLiveTimeCaps(ctx context.Context) {
	rows, err := db.Query(ctx, `
		select distinct t.id::text
		  from public.tasks t
		  join public.worklogs w on w.task_id = t.id and w.end_at is null
		 where t.status = 'open'
		   and t.assigned_to_id is not null
		   and coalesce(t.estimated_minutes, 0) > 0
	`)
	if err != nil {
		log.Printf("[timecap][live-sweep] %v", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			log.Printf("[timecap][live-sweep] scan: %v", err)
			break
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[timecap][live-sweep] rows: %v", err)
	}
	rows.Close()
	for _, id := range ids {
		evaluateTimeCap(ctx, id)
	}
}

// watchLiveTimeCaps runs sweepLiveTimeCaps every `every` (a minute).
func watchLiveTimeCaps(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			func() {
				defer func() {
					if p := recover(); p != nil {
						log.Printf("[timecap][live-sweep] panic: %v", p)
					}
				}()
				sweepLiveTimeCaps(ctx)
			}()
		}
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
