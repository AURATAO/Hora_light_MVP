package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"hora-auth/helpers"
	"hora-auth/internal/notify"
)

// Supporter push dispatch (D-24, Aura 2026-11-06).
//
// A task that goes live is pushed to the supporters who can take it:
//
//	round 1   available supporters who serve the task's borough
//	+3 min    every other available supporter (round 2)
//	+5 min    ops, by email — immediate tasks only; a scheduled one has
//	          already reached ops as "New task posted"
//
// First accept wins (acceptTask's claim). Nothing here assigns anything.
// The times live in task_dispatches, so a restart or a deploy loses no step:
// the sweep picks up where the row says (D-20). Off unless
// DISPATCH_PUSH_ENABLED=1.

const (
	dispatchWidenAfter  = 3 * time.Minute
	dispatchOpsAfter    = 5 * time.Minute
	dispatchSweepEvery  = 30 * time.Second
	availabilityLasts   = 4 * time.Hour
	dispatchStepTimeout = 20 * time.Second // the reads and writes of one step
	dispatchPushTimeout = 10 * time.Second // one supporter's push
	dispatchPushWorkers = 8
	// A row older than this is closed by the sweep without acting: a task
	// nobody took for a day is ops' already.
	dispatchGiveUpAfter = 24 * time.Hour
)

const pushTypeNewTaskOffer = "NEW_TASK_OFFER"

func dispatchEnabled() bool {
	return strings.TrimSpace(os.Getenv("DISPATCH_PUSH_ENABLED")) == "1"
}

// Seams for tests.
var (
	dispatchPushFn = func(ctx context.Context, userID, taskID, title, body string) {
		notify.SendPushData(ctx, sqldb, userID, taskID, pushTypeNewTaskOffer, title, body,
			map[string]string{"url": "/task/" + taskID})
	}
	dispatchOpsFn = func(taskID, title string, minutes, pushed int) {
		notify.NotifyAdminUnclaimed(taskID, title, minutes, pushed)
	}
	// The borough of the task's first stop, from the same service-area
	// lookup the post passed. "" when it cannot be told: round 1 is then
	// skipped and the task goes to everyone available.
	dispatchBoroughFn = func(ctx context.Context, locationText string) string {
		stops := taskStops(locationText)
		if len(stops) == 0 {
			return ""
		}
		lctx, cancel := context.WithTimeout(ctx, areaCheckTimeout)
		defer cancel()
		res, err := serviceAreaFn(lctx, stops[0])
		if err != nil || res.Verdict != helpers.AreaInside {
			return ""
		}
		return res.Borough
	}
)

// dispatchWG lets tests wait for a dispatch started in the background.
var dispatchWG sync.WaitGroup

// startDispatch runs round 1 for a task that just went live, off the request
// path. Called from announceNewTaskInput, which every path that makes a task
// live goes through.
func startDispatch(taskID string) {
	if !dispatchEnabled() {
		return
	}
	dispatchWG.Add(1)
	go func() {
		defer dispatchWG.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[dispatch] task=%s: panic: %v", taskID, r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), dispatchStepTimeout)
		defer cancel()
		if err := dispatchRound1(ctx, taskID); err != nil {
			log.Printf("[dispatch] task=%s round 1: %v", taskID, err)
		}
	}()
}

// dispatchTask is what a push says about a task. Lock-screen safe: no
// address, no requester, no description.
type dispatchTask struct {
	ID          string
	RequesterID string
	Title       string
	Category    string
	Minutes     int
	RateCents   int
	Immediate   bool
	ScheduledAt *time.Time
	Location    string
	Open        bool
}

func loadDispatchTask(ctx context.Context, taskID string) (dispatchTask, error) {
	t := dispatchTask{ID: taskID}
	var status string
	var assigned *string
	err := db.QueryRow(ctx, `
		select coalesce(requester_id::text, ''), coalesce(title, ''), coalesce(category, ''),
		       coalesce(estimated_minutes, 0), coalesce(rate_cents_per_min, 0),
		       coalesce(is_immediate, false), scheduled_at, coalesce(location_text, ''),
		       coalesce(status, ''), assigned_to_id::text
		  from public.tasks where id = $1::uuid`, taskID,
	).Scan(&t.RequesterID, &t.Title, &t.Category, &t.Minutes, &t.RateCents,
		&t.Immediate, &t.ScheduledAt, &t.Location, &status, &assigned)
	if err != nil {
		return t, fmt.Errorf("read task: %w", err)
	}
	t.Open = status == "open" && assigned == nil
	return t, nil
}

func dispatchRound1(ctx context.Context, taskID string) error {
	t, err := loadDispatchTask(ctx, taskID)
	if err != nil {
		return err
	}
	if !t.Open || t.RequesterID == "" {
		return nil
	}
	borough := dispatchBoroughFn(ctx, t.Location)

	// One dispatch per task, whichever path announced it first.
	var claimed bool
	if err := db.QueryRow(ctx, `
		insert into public.task_dispatches (task_id, borough) values ($1::uuid, $2)
		on conflict (task_id) do nothing
		returning true`, taskID, borough).Scan(&claimed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("record dispatch: %w", err)
	}

	// Who is reached is written BEFORE anyone is pushed (review r1): a crash
	// between the two can then only miss a push, never repeat one in round 2.
	if borough == "" {
		// Nothing to match on: everyone available, now; round 2 has run.
		ids, err := eligibleSupporters(ctx, t.RequesterID, "", nil)
		if err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `
			update public.task_dispatches set widened_at = now(),
			       round1_user_ids = $2::text[]::uuid[]
			 where task_id = $1::uuid`, taskID, nonNil(ids)); err != nil {
			return fmt.Errorf("record round: %w", err)
		}
		dispatchSend(t, "", ids)
		log.Printf("[dispatch] task=%s round=all (no borough) pushed=%d", taskID, len(ids))
		return nil
	}
	ids, err := eligibleSupporters(ctx, t.RequesterID, borough, nil)
	if err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `
		update public.task_dispatches set round1_user_ids = $2::text[]::uuid[]
		 where task_id = $1::uuid`, taskID, nonNil(ids)); err != nil {
		return fmt.Errorf("record round 1: %w", err)
	}
	dispatchSend(t, borough, ids)
	log.Printf("[dispatch] task=%s round=1 borough=%s pushed=%d", taskID, borough, len(ids))
	return nil
}

func nonNil(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

// eligibleSupporters: verified, available now, with a device to push to, not
// the requester, neither side blocking the other, and on the requester's
// side of the App Review sandbox wall. borough != "" keeps those who serve
// it; except drops ids already reached. Payout readiness is checked in Go,
// the same gate acceptTask applies.
func eligibleSupporters(ctx context.Context, requesterID, borough string, except []string) ([]string, error) {
	if except == nil {
		except = []string{}
	}
	rows, err := db.Query(ctx, `
		select u.id::text
		  from public.users u
		  join public.profiles p on p.id = u.id
		 where coalesce(p.is_verified_supporter, false)
		   and p.available_until > now()
		   and u.id <> $1::uuid
		   and ($2 = '' or $2 = any(p.dispatch_boroughs))
		   and not (u.id::text = any($3::text[]))
		   and exists (select 1 from public.device_push_tokens d where d.user_id = u.id)
		   and coalesce(u.is_sandbox, false)
		       = coalesce((select r.is_sandbox from public.users r where r.id = $1::uuid), false)
		   and not `+usersBlockedSQL("u.id", "$1::uuid")+`
		 order by p.available_until desc
		 limit 200`, requesterID, borough, except)
	if err != nil {
		return nil, fmt.Errorf("eligible supporters: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := ids[:0]
	for _, id := range ids {
		ready, err := supporterPayoutsReady(ctx, id)
		if err != nil {
			log.Printf("[dispatch] supporter=%s payout readiness unreadable: %v", id, err)
			continue
		}
		if ready {
			out = append(out, id)
		}
	}
	return out, nil
}

// dispatchSend pushes to each id, dispatchPushWorkers at a time, each with
// its own deadline (review r1: one shared deadline let a slow Expo call
// starve the rest). The push itself is best effort and logs its failures.
func dispatchSend(t dispatchTask, borough string, ids []string) {
	if len(ids) == 0 {
		return
	}
	title, body := dispatchCopy(t, borough)
	work := make(chan string)
	var wg sync.WaitGroup
	for w := 0; w < dispatchPushWorkers && w < len(ids); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range work {
				func() {
					defer func() {
						if r := recover(); r != nil {
							log.Printf("[dispatch] task=%s push panic: %v", t.ID, r)
						}
					}()
					pctx, cancel := context.WithTimeout(context.Background(), dispatchPushTimeout)
					defer cancel()
					dispatchPushFn(pctx, id, t.ID, title, body)
				}()
			}
		}()
	}
	for _, id := range ids {
		work <- id
	}
	close(work)
	wg.Wait()
}

// dispatchCopy: "New task near you · ~$14" / "Pick up a box · Manhattan · Now".
// The amount is what the supporter would earn at the estimate: base plus
// estimated time, less the platform fee — never the requester's total.
func dispatchCopy(t dispatchTask, borough string) (string, string) {
	title := "New task near you"
	if t.Minutes > 0 {
		q := quoteTask(t.Category, t.Minutes, 0, t.RateCents)
		pay := supporterPayFor(q.BaseFeeCents+q.TimeCostCents, 0).ServiceNetCents()
		if pay > 0 {
			title = fmt.Sprintf("New task near you · ~%s", formatCentsUSD(pay))
		}
	}
	// The category, never the requester's title: a title is free text and
	// often carries the address ("Pick up a package at 350 5th Ave"), and a
	// push is read on a lock screen (review r1).
	parts := []string{dispatchCategoryLabel(t.Category)}
	if borough != "" {
		parts = append(parts, borough)
	}
	parts = append(parts, dispatchWhen(t))
	return title, strings.Join(parts, " · ")
}

func dispatchWhen(t dispatchTask) string {
	if t.Immediate || t.ScheduledAt == nil {
		return "Now"
	}
	return t.ScheduledAt.In(nycLocation).Format("Mon 3:04 PM")
}

func dispatchCategoryLabel(category string) string {
	switch category {
	case "delivery":
		return "Pickup & delivery"
	case "grocery":
		return "Grocery run"
	case "laundry":
		return "Laundry"
	case "queue":
		return "Wait in line"
	case "companionship", "companion":
		return "Companionship"
	}
	return "Errand"
}

// ── The sweep ──────────────────────────────────────────────────────────────

func watchDispatches(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[dispatch][sweep] panic: %v", r)
					}
				}()
				sctx, cancel := context.WithTimeout(ctx, every)
				defer cancel()
				sweepDispatches(sctx, 0)
			}()
		}
	}
}

// sweepDispatches runs each step that is due. Every step claims its row with
// a compare-and-set on its own column first, so two instances (a deploy's
// overlap) never both widen or both email. Claimed before it is performed:
// a crash in between skips that step rather than repeating it (a push
// twice is worse than once missed; ops still had "New task posted").
//
// Ages come from the database's clock, the one round1_at was written with
// (review r1: Go's clock against the DB's shifted the marks by any skew).
// shift adds to every age; only tests pass one.
func sweepDispatches(ctx context.Context, shift time.Duration) {
	rows, err := db.Query(ctx, `
		select d.task_id::text, d.borough, d.round1_user_ids::text[],
		       extract(epoch from (now() - d.round1_at))::float8,
		       d.widened_at is not null, d.ops_alerted_at is not null
		  from public.task_dispatches d
		 where d.closed_at is null
		 order by d.round1_at
		 limit 200`)
	if err != nil {
		dispatchSweepErr(err)
		return
	}
	type due struct {
		taskID, borough  string
		round1           []string
		ageSec           float64
		widened, alerted bool
	}
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.taskID, &d.borough, &d.round1, &d.ageSec, &d.widened, &d.alerted); err != nil {
			log.Printf("[dispatch][sweep] row: %v", err)
			continue
		}
		list = append(list, d)
	}
	rows.Close()

	for _, d := range list {
		t, err := loadDispatchTask(ctx, d.taskID)
		if err != nil {
			log.Printf("[dispatch][sweep] task=%s: %v", d.taskID, err)
			continue
		}
		age := time.Duration(d.ageSec*float64(time.Second)) + shift
		if !t.Open || age > dispatchGiveUpAfter {
			closeDispatch(ctx, d.taskID)
			continue
		}
		if !d.widened && age >= dispatchWidenAfter {
			if claimDispatchStep(ctx, d.taskID, "widened_at") {
				ids, err := eligibleSupporters(ctx, t.RequesterID, "", d.round1)
				if err != nil {
					log.Printf("[dispatch] task=%s round 2: %v", d.taskID, err)
				}
				_, _ = db.Exec(ctx, `update public.task_dispatches set widened_count = $2 where task_id = $1::uuid`, d.taskID, len(ids))
				dispatchSend(t, d.borough, ids)
				log.Printf("[dispatch] task=%s round=2 pushed=%d", d.taskID, len(ids))
			}
			d.widened = true
		}
		if !d.alerted && age >= dispatchOpsAfter {
			if !t.Immediate {
				// Scheduled: ops had "New task posted"; nothing is urgent yet.
				closeDispatch(ctx, d.taskID)
				continue
			}
			if claimDispatchStep(ctx, d.taskID, "ops_alerted_at") {
				var pushed int
				_ = db.QueryRow(ctx, `select coalesce(array_length(round1_user_ids, 1), 0) + widened_count
				  from public.task_dispatches where task_id = $1::uuid`, d.taskID).Scan(&pushed)
				dispatchOpsFn(d.taskID, t.Title, int(age.Minutes()), pushed)
				log.Printf("[dispatch] task=%s unclaimed after %s: ops alerted", d.taskID, age.Round(time.Second))
			}
			closeDispatch(ctx, d.taskID)
		}
	}
}

// dispatchSweepErr logs a failed scan at most every 10 minutes: with the
// flag on and the migration missing it would otherwise fire every 30 s.
func dispatchSweepErr(err error) {
	dispatchSweepErrMu.Lock()
	defer dispatchSweepErrMu.Unlock()
	if time.Since(dispatchSweepErrAt) < 10*time.Minute {
		return
	}
	dispatchSweepErrAt = time.Now()
	log.Printf("[dispatch][sweep][ERROR] scan: %v (is migration 20261106120000 applied?)", err)
}

var (
	dispatchSweepErrMu sync.Mutex
	dispatchSweepErrAt time.Time
)

// claimDispatchStep sets col to now() if it is still null and reports whether
// this call did. col is one of two literals above, never input.
func claimDispatchStep(ctx context.Context, taskID, col string) bool {
	if col != "widened_at" && col != "ops_alerted_at" {
		return false
	}
	tag, err := db.Exec(ctx, `update public.task_dispatches set `+col+` = now()
		 where task_id = $1::uuid and `+col+` is null and closed_at is null`, taskID)
	if err != nil {
		log.Printf("[dispatch] task=%s claim %s: %v", taskID, col, err)
		return false
	}
	return tag.RowsAffected() == 1
}

func closeDispatch(ctx context.Context, taskID string) {
	if _, err := db.Exec(ctx, `update public.task_dispatches set closed_at = now()
		 where task_id = $1::uuid and closed_at is null`, taskID); err != nil {
		log.Printf("[dispatch] task=%s close: %v", taskID, err)
	}
}

// ── The supporter's settings ───────────────────────────────────────────────

type dispatchSettings struct {
	Boroughs       []string   `json:"boroughs"`
	Available      bool       `json:"available"`
	AvailableUntil *time.Time `json:"available_until"`
	Enabled        bool       `json:"enabled"`
	AllBoroughs    []string   `json:"all_boroughs"`
}

func readDispatchSettings(ctx context.Context, uid string) (dispatchSettings, error) {
	s := dispatchSettings{Enabled: dispatchEnabled(), AllBoroughs: helpers.BoroughNames()}
	err := db.QueryRow(ctx, `
		select coalesce(dispatch_boroughs, '{}'), available_until
		  from public.profiles where id = $1::uuid`, uid).Scan(&s.Boroughs, &s.AvailableUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		s.Boroughs = []string{}
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.Available = s.AvailableUntil != nil && s.AvailableUntil.After(time.Now())
	if !s.Available {
		s.AvailableUntil = nil
	}
	return s, nil
}

// GET /me/dispatch
func getMyDispatch(c *gin.Context) {
	s, err := readDispatchSettings(c.Request.Context(), c.GetString("uid"))
	if err != nil {
		log.Printf("[dispatch] read settings: %v", err)
		c.JSON(500, gin.H{"error": "db error"})
		return
	}
	c.JSON(200, s)
}

// PUT /me/dispatch {"boroughs": ["Manhattan", ...]} — the whole list.
func putMyDispatch(c *gin.Context) {
	var in struct {
		Boroughs []string `json:"boroughs"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Boroughs == nil {
		c.JSON(400, gin.H{"error": "boroughs is required"})
		return
	}
	clean, ok := normalizeBoroughs(in.Boroughs)
	if !ok {
		c.JSON(400, gin.H{"error": "unknown borough", "all_boroughs": helpers.BoroughNames()})
		return
	}
	ctx := c.Request.Context()
	uid := c.GetString("uid")
	tag, err := db.Exec(ctx, `update public.profiles set dispatch_boroughs = $2 where id = $1::uuid`, uid, clean)
	if err != nil {
		log.Printf("[dispatch] save boroughs: %v", err)
		c.JSON(500, gin.H{"error": "db error"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(404, gin.H{"error": "no profile"})
		return
	}
	getMyDispatch(c)
}

// normalizeBoroughs maps each name to the server's spelling, case-blind and
// accepting "Bronx" for "the Bronx"; any unknown name fails the whole list.
func normalizeBoroughs(in []string) ([]string, bool) {
	names := helpers.BoroughNames()
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range in {
		r := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "the "))
		match := ""
		for _, n := range names {
			if strings.TrimPrefix(strings.ToLower(n), "the ") == r {
				match = n
			}
		}
		if match == "" {
			return nil, false
		}
		if !seen[match] {
			seen[match] = true
			out = append(out, match)
		}
	}
	return out, true
}

// POST /me/availability {"on": true|false}. On lasts availabilityLasts.
// Verified supporters only: nobody else can be dispatched to.
func postMyAvailability(c *gin.Context) {
	var in struct {
		On *bool `json:"on"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.On == nil {
		c.JSON(400, gin.H{"error": "on is required"})
		return
	}
	ctx := c.Request.Context()
	uid := c.GetString("uid")
	var verified bool
	if err := db.QueryRow(ctx, `select coalesce((select is_verified_supporter from public.profiles where id = $1::uuid), false)`,
		uid).Scan(&verified); err != nil {
		c.JSON(500, gin.H{"error": "db error"})
		return
	}
	if !verified {
		c.JSON(http.StatusForbidden, gin.H{"error": "supporters_only", "message": "Only approved supporters can go available."})
		return
	}
	var until *time.Time
	if *in.On {
		u := time.Now().Add(availabilityLasts)
		until = &u
	}
	if _, err := db.Exec(ctx, `update public.profiles set available_until = $2 where id = $1::uuid`, uid, until); err != nil {
		log.Printf("[dispatch] save availability: %v", err)
		c.JSON(500, gin.H{"error": "db error"})
		return
	}
	getMyDispatch(c)
}

// GET /config — what the apps may show. No auth: nothing in it is private.
func getAppConfig(c *gin.Context) {
	c.JSON(200, gin.H{"dispatch": dispatchEnabled()})
}
