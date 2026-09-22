package main

// Live supporter tracking — the requester's view of "where are they, right
// now", from the moment the supporter taps "On my way" until they clock out.
//
// Three pieces, and the boundaries between them are the whole design:
//
//  1. THE WINDOW. Pings have always required an open worklog, so nothing was
//     visible before clock-in — which is the moment the supporter ARRIVES.
//     POST /tasks/:id/enroute opens a second, narrower window: assigned task,
//     still open, no worklog yet. saveGpsPing accepts source='enroute' pings
//     inside that window and nowhere else (enrouteWindowOpen below), so the
//     exception cannot be used to keep sharing after clock-out.
//
//  2. THE STATE. GET /tasks/:id/live turns a raw coordinate into the one word
//     the requester actually wants. Derived HERE, on the server, from the
//     distance to the task location — never on the client, which would mean
//     shipping the client the arithmetic and trusting it to round the same way
//     the arrival notification does.
//
//  3. THE PRIVACY BOUNDARY. The live payload is REQUESTER-ONLY. Not
//     requester-or-assignee like /gps-latest: a supporter has no business
//     reading a distance-to-door derived from their own movements, and the
//     endpoint carries the task's coordinates, which the assignee gets from
//     the task itself rather than from here.
//
// The coordinates leave the server, because the map needs them; the COPY never
// does. Clients render "0.8 mi away", never a lat/lng — see the note on
// distance_m.

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"sync"
	"time"

	notify "hora-auth/internal/notify"

	"github.com/gin-gonic/gin"
)

// The distance bands, in metres from the task location.
//
// Chosen to be legible rather than precise: consumer GPS is good to 10-50m in
// a city, so a band narrower than ~100m would flap between two labels while
// somebody stands still. 500m is roughly a five-minute walk — near enough that
// "almost there" is true, far enough that it is not yet "at the door".
const (
	// Below this, the supporter is at the address. Also the arrival-push
	// threshold: the notification and the 'at_door' label are the same event,
	// so they must be the same number.
	arrivalRadiusMeters = 100.0
	// Below this (and at or above arrivalRadiusMeters), they are close.
	almostThereMeters = 500.0
)

// A position older than this is not a position, it is a memory. Two minutes is
// four missed pings at the 30s cadence — long enough that a tunnel, a lift or
// an iOS batching hiccup does not read as "unavailable", short enough that a
// phone that has genuinely stopped reporting is called out before the
// requester has made a decision based on a stale dot.
const livePingStaleAfter = 2 * time.Minute

// The states, as the clients switch on them. Closed set: a client that meets an
// unknown value has no copy for it.
const (
	liveStateOnTheWay    = "on_the_way"
	liveStateAlmostThere = "almost_there"
	liveStateAtDoor      = "at_door"
	liveStateWorking     = "working"
	liveStateUnavailable = "unavailable"
)

// haversineMeters is the great-circle distance between two WGS84 points.
//
// Not Google Distance Matrix (helpers/maps.go), deliberately: this runs on
// every poll from every watching requester, and a billed API call per poll to
// refine "0.8 mi away" into "0.9 mi by road" buys nothing the copy expresses.
// Straight-line distance is also the honest thing to draw two markers from.
func haversineMeters(lat1, lng1, lat2, lng2 float64) float64 {
	const earthRadiusMeters = 6371000.0
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := rad(lat2 - lat1)
	dLng := rad(lng2 - lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthRadiusMeters * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// liveState is the whole state machine, as one pure function — so the
// thresholds and their boundaries can be tested without a database.
//
// The order of the branches is the specification:
//
//   - CLOCKED IN WINS, ALWAYS. Once a worklog is open the supporter is at the
//     address and working; that is a fact from the clock, not from the GPS, and
//     a phone that stopped reporting does not make it less true. Reporting
//     'unavailable' to a requester whose supporter is demonstrably on the job
//     would be worse than useless.
//
//   - Otherwise a stale or missing fix is 'unavailable'. Pre-clock-in there is
//     nothing else to go on, and a five-minute-old dot presented as current is
//     exactly the failure this state exists to prevent. The caller still ships
//     the last known position alongside it.
//
//   - Otherwise, distance. Unknown distance (the task has no coordinates —
//     job_lat/job_lng are nullable and older tasks have none) falls back to
//     'on_the_way', which is the one thing we do know about a supporter who has
//     tapped the button and not yet clocked in.
func liveState(distanceMeters *float64, clockedIn bool, fresh bool) string {
	if clockedIn {
		return liveStateWorking
	}
	if !fresh {
		return liveStateUnavailable
	}
	if distanceMeters == nil {
		return liveStateOnTheWay
	}
	switch {
	case *distanceMeters < arrivalRadiusMeters:
		return liveStateAtDoor
	case *distanceMeters <= almostThereMeters:
		return liveStateAlmostThere
	default:
		return liveStateOnTheWay
	}
}

// ── The enroute window ─────────────────────────────────────────────────────

// enrouteWindowOpen answers the one question saveGpsPing's exception turns on:
// may this user post a position for this task with NO open worklog?
//
// Yes only when all four hold:
//
//	the task is still open        — not completed, cancelled or removed
//	assigned to this user         — nobody else's movements, ever
//	enroute_at is set             — they opted in; this is the privacy default
//	no worklog exists at all      — the window ENDS at the first clock-in
//
// That last clause is what makes the window self-closing, and it is why it
// counts worklogs rather than open ones. After clock-out a worklog exists but
// is closed: the open-worklog path is shut and this one is too, so sharing
// stops at clock-out exactly as the supporter was told it would. Without it,
// enroute_at — which is never cleared — would re-open sharing for the rest of
// the task's life the moment the clock stopped.
func enrouteWindowOpen(ctx context.Context, taskID, uid string) (bool, error) {
	var open bool
	err := db.QueryRow(ctx, `
		select exists (
			select 1 from public.tasks t
			where t.id = $1::uuid
			  and t.status = 'open'
			  and t.assigned_to_id = $2::uuid
			  and t.enroute_at is not null
			  and not exists (select 1 from public.worklogs w where w.task_id = t.id)
		)
	`, taskID, uid).Scan(&open)
	return open, err
}

// POST /tasks/:id/enroute — the supporter says they have set off.
//
// Idempotent: a second tap returns the first tap's timestamp rather than
// resetting it, so a double-tap or a retry after a dropped response cannot
// restart the window. Assignee only; the requester cannot start sharing on
// somebody else's behalf, which is the entire point of the privacy default.
func postEnroute(c *gin.Context) {
	taskID := c.Param("id")
	uid := c.GetString("uid")
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	var status string
	var assignedToID *string
	var enrouteAt *time.Time
	if err := db.QueryRow(ctx, `
		select status, assigned_to_id::text, enroute_at
		from public.tasks where id = $1::uuid
	`, taskID).Scan(&status, &assignedToID, &enrouteAt); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if assignedToID == nil || *assignedToID != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "only the assigned supporter can share their location"})
		return
	}
	if status == "removed" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "task_removed", "message": "This task has been removed."})
		return
	}
	if status != "open" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "task is not active"})
		return
	}

	// Already clocked in — or already clocked out. Either way the enroute
	// window is over (see enrouteWindowOpen), and letting the tap succeed
	// would put a button in a state that shares nothing.
	var hasWorklog bool
	_ = db.QueryRow(ctx, `
		select exists (select 1 from public.worklogs where task_id = $1::uuid)
	`, taskID).Scan(&hasWorklog)
	if hasWorklog {
		c.JSON(http.StatusBadRequest, gin.H{"error": "already_started", "message": "You have already started this task."})
		return
	}

	if enrouteAt == nil {
		// Guarded so two taps racing each other still produce one timestamp.
		if err := db.QueryRow(ctx, `
			update public.tasks set enroute_at = now()
			where id = $1::uuid and enroute_at is null
			returning enroute_at
		`, taskID).Scan(&enrouteAt); err != nil {
			// Lost the race: somebody else's UPDATE matched first. Read theirs.
			if err := db.QueryRow(ctx,
				`select enroute_at from public.tasks where id = $1::uuid`, taskID).Scan(&enrouteAt); err != nil {
				log.Printf("[enroute] task=%s: %v", taskID, err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
				return
			}
		}
	}

	log.Printf("[enroute] task=%s supporter=%s at=%v", taskID, uid, enrouteAt)
	c.JSON(http.StatusOK, gin.H{"enroute_at": enrouteAt})
}

// ── The arrival notification ───────────────────────────────────────────────

// maybeNotifyArrival fires "«name» has arrived" the first time a pre-clock-in
// ping lands inside arrivalRadiusMeters, and never again for that task.
//
// Called from saveGpsPing on the enroute path only. Exactly-once is the
// guarded UPDATE on tasks.arrival_notified_at: two pings crossing the
// threshold concurrently both run it, one matches, and only that one notifies.
// A row count, not a read-then-write — the read-then-write version of this
// sends two pushes under exactly the load it was written for.
//
// Every failure here is swallowed into a log line. This runs inside a ping
// handler whose contract is "best effort"; a notification that cannot be sent
// must never turn a stored position into a 500 the supporter's phone retries.
func maybeNotifyArrival(c *gin.Context, taskID, supporterEmail string, lat, lng float64) {
	ctx := c.Request.Context()

	var jobLat, jobLng *float64
	var taskTitle string
	if err := db.QueryRow(ctx, `
		select job_lat, job_lng, coalesce(title,'')
		from public.tasks where id = $1::uuid
	`, taskID).Scan(&jobLat, &jobLng, &taskTitle); err != nil {
		return
	}
	// No destination, no arrival. A task posted without coordinates simply
	// never fires this — silence is the right failure here, since the
	// alternative is announcing an arrival we cannot actually verify.
	if jobLat == nil || jobLng == nil {
		return
	}
	if haversineMeters(lat, lng, *jobLat, *jobLng) >= arrivalRadiusMeters {
		return
	}

	tag, err := db.Exec(ctx, `
		update public.tasks set arrival_notified_at = now()
		where id = $1::uuid and arrival_notified_at is null
	`, taskID)
	if err != nil {
		log.Printf("[arrival] task=%s latch failed: %v", taskID, err)
		return
	}
	if tag.RowsAffected() == 0 {
		return // Already announced. This is the common case on every later ping.
	}

	name := resolveDisplayName(ctx, supporterEmail)
	log.Printf("[arrival] task=%s supporter=%s within %.0fm", taskID, supporterEmail, arrivalRadiusMeters)
	notifyRequesterRich(c, notify.CreateNotificationInput{
		TaskID:        taskID,
		Type:          "SUPPORTER_ARRIVED",
		Title:         fmt.Sprintf("%s has arrived", name),
		Body:          fmt.Sprintf("%s is at your address for \"%s\".", name, taskTitle),
		SupporterName: name,
		TaskTitle:     taskTitle,
	})
}

// ── The polled endpoint ────────────────────────────────────────────────────

// This endpoint is polled every 15 seconds by every open requester screen, so
// it gets a limiter — not to defend against an attacker (it is auth-gated and
// scoped to one task the caller owns) but against the ordinary accident: a
// screen whose interval is never cleared, a client that retries on error
// without backing off, a requester with the task open on a phone and two tabs.
//
// A fixed window, in memory, keyed per (user, task). In memory is the right
// scope even with several instances behind a load balancer: the budget is
// per-instance, so N instances allow N× — which is still a bound, and the
// thing being bounded is a runaway client, not a distributed attacker. Redis
// for this would be a dependency bought with nothing.
const (
	liveRateWindow = 30 * time.Second
	// 15s polling is 2 per window. 12 leaves room for a second device, a
	// re-mount storm, and the immediate poll each screen fires on open,
	// while still catching an interval that has come loose.
	liveRateBurst = 12
)

type liveRateEntry struct {
	windowStart time.Time
	count       int
}

var liveRate = struct {
	sync.Mutex
	seen map[string]liveRateEntry
	// Swept rather than expired per-key: one pass over a map that holds one
	// entry per actively-watched task is cheaper than a timer per entry.
	lastSweep time.Time
}{seen: map[string]liveRateEntry{}}

// liveRateAllow reports whether this caller may read this task's position now,
// and how long to wait if not.
func liveRateAllow(key string, now time.Time) (bool, time.Duration) {
	liveRate.Lock()
	defer liveRate.Unlock()

	if now.Sub(liveRate.lastSweep) > 10*time.Minute {
		for k, e := range liveRate.seen {
			if now.Sub(e.windowStart) > liveRateWindow {
				delete(liveRate.seen, k)
			}
		}
		liveRate.lastSweep = now
	}

	e, ok := liveRate.seen[key]
	if !ok || now.Sub(e.windowStart) >= liveRateWindow {
		liveRate.seen[key] = liveRateEntry{windowStart: now, count: 1}
		return true, 0
	}
	if e.count >= liveRateBurst {
		return false, liveRateWindow - now.Sub(e.windowStart)
	}
	e.count++
	liveRate.seen[key] = e
	return true, 0
}

// GET /tasks/:id/live — REQUESTER ONLY.
//
// Answers with the supporter's last known position, the derived state, the
// distance to the task location, and enough about the supporter to draw their
// marker. 200 in every non-error case, including "nothing to show yet" — a
// requester who opens the screen before the supporter has done anything gets
// state 'unavailable' with a null position, not a 404 the client has to
// special-case.
func getLiveLocation(c *gin.Context) {
	taskID := c.Param("id")
	uid := c.GetString("uid")
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	var requesterID, status string
	var assignedToID, assignedTo *string
	var jobLat, jobLng *float64
	var enrouteAt *time.Time
	if err := db.QueryRow(ctx, `
		select requester_id::text, status, assigned_to_id::text,
		       nullif(assigned_to,''), job_lat, job_lng, enroute_at
		from public.tasks where id = $1::uuid
	`, taskID).Scan(&requesterID, &status, &assignedToID, &assignedTo, &jobLat, &jobLng, &enrouteAt); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	// The privacy boundary, and the only one. 404 rather than 403 for everyone
	// else including the assignee: a supporter probing this endpoint should not
	// be able to distinguish "you may not read this" from "there is nothing
	// here", and there is no legitimate client that asks.
	if requesterID != uid {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	// Tracking exists for a task somebody is on their way to or working on.
	// Outside that — unassigned, completed, cancelled, removed — there is
	// nothing live, and the last position from a finished task is not the
	// requester's to keep watching.
	if status != "open" || assignedToID == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	if ok, retryAfter := liveRateAllow(uid+":"+taskID, time.Now()); !ok {
		c.Header("Retry-After", fmt.Sprintf("%d", int(math.Ceil(retryAfter.Seconds()))))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "slow down"})
		return
	}

	// Clocked in? The strongest signal in the payload, and the one that
	// overrides staleness (see liveState).
	var clockedIn bool
	_ = db.QueryRow(ctx, `
		select exists (select 1 from public.worklogs where task_id = $1::uuid and end_at is null)
	`, taskID).Scan(&clockedIn)

	var lat, lng *float64
	var updatedAt *time.Time
	var accuracy *int
	err := db.QueryRow(ctx, `
		select lat, lng, accuracy, created_at
		from public.task_gps_pings
		where task_id = $1::uuid
		order by created_at desc limit 1
	`, taskID).Scan(&lat, &lng, &accuracy, &updatedAt)
	if err != nil {
		// No ping yet, or a read that failed. Both are "no position", which the
		// payload expresses directly — and a requester watching a live screen
		// is better served by an honest "unavailable" than by a 500.
		lat, lng, updatedAt = nil, nil, nil
	}

	fresh := updatedAt != nil && time.Since(*updatedAt) <= livePingStaleAfter

	var distance *float64
	if lat != nil && lng != nil && jobLat != nil && jobLng != nil {
		d := haversineMeters(*lat, *lng, *jobLat, *jobLng)
		distance = &d
	}

	// A stale position is still shipped — the requester wants to see where the
	// supporter WAS, labelled honestly as unavailable, rather than an empty
	// map. But its distance is suppressed: "0.8 mi away" about a five-minute-old
	// fix is a claim about the present that we cannot make.
	state := liveState(distance, clockedIn, fresh)
	if state == liveStateUnavailable {
		distance = nil
	}

	// Name and avatar for the map marker. A miss is not an error: the marker
	// falls back to the HO:RA dot, which is the same thing the clients draw for
	// a supporter who has never uploaded a photo.
	var supporterName, supporterAvatar string
	if assignedToID != nil {
		_ = db.QueryRow(ctx, `
			select coalesce(p.avatar_url,'')
			from public.profiles p
			where p.id = $1::uuid
		`, *assignedToID).Scan(&supporterAvatar)
	}
	if assignedTo != nil {
		// The name is the chain (names.go), never users.name — that column is
		// a login-time artefact, not something the supporter chose.
		supporterName = resolveDisplayName(ctx, *assignedTo)
	}

	out := gin.H{
		"state":      state,
		"lat":        lat,
		"lng":        lng,
		"updated_at": updatedAt,
		// Metres, server-derived, and NEVER rendered verbatim: the clients turn
		// it into "0.8 mi away". Coordinates as text are for a debugging tool,
		// not for somebody watching a person approach their home.
		"distance_m": distance,
		"supporter": gin.H{
			"name":       supporterName,
			"avatar_url": supporterAvatar,
		},
		// The other marker. The requester's own address — they posted it — sent
		// here so one poll draws the whole map.
		"destination": nil,
		"enroute_at":  enrouteAt,
		"clocked_in":  clockedIn,
	}
	if jobLat != nil && jobLng != nil {
		out["destination"] = gin.H{"lat": *jobLat, "lng": *jobLng}
	}
	c.JSON(http.StatusOK, out)
}
