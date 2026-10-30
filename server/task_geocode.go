package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"hora-auth/helpers"
)

// Task coordinates (2026-10-30). tasks.job_lat / job_lng existed since the
// July baseline and were never written, so live tracking had no place to
// measure against: every "0.8 mi away", "almost there" and "at the door"
// was unreachable, on the app's map and in the chat. Now the task's FIRST
// stop is geocoded server-side when the task is created and whenever its
// address changes — the place a supporter heads to first.
//
// Off the request path: a post never waits on Google, and a geocoding
// failure never fails a post. The write is a compare-and-set on the
// address it was computed from, so an edit made meanwhile wins.

// geocodeFn is the geocoder; a variable so tests can replace it.
var geocodeFn = helpers.GeocodeAddress

// geocodeWG lets tests wait for the background writes.
var geocodeWG sync.WaitGroup

// geocodeSlots bounds lookups in flight (review: a flood of posts or edits
// must not mean unbounded goroutines and calls). Full → skipped; the next
// "where is he?" asks again (geocodeMissing).
var geocodeSlots = make(chan struct{}, 8)

// geocodeTimeout bounds the lookup; the write has its own.
const (
	geocodeTimeout      = 10 * time.Second
	geocodeWriteTimeout = 5 * time.Second
)

// geocodeRetryAfter: a task whose lookup failed is not asked about again
// sooner than this (geocodeMissing).
const geocodeRetryAfter = 10 * time.Minute

var geocodeTried struct {
	sync.Mutex
	at map[string]time.Time
}

// firstStop: the first address of a task's location text. Web and mobile
// join several stops with "|" (" | "); the chat with "→". An empty first
// stop is "" — never a later stop promoted to first (review).
func firstStop(locationText string) string {
	t := locationText
	for _, sep := range []string{"|", "→", "->"} {
		if i := strings.Index(t, sep); i >= 0 {
			t = t[:i]
		}
	}
	return strings.TrimSpace(t)
}

// geocodeTaskAsync geocodes the task's first stop in the background.
// started=false: no lookup was begun (no first stop, no key, all slots busy).
func geocodeTaskAsync(taskID, locationText string) (started bool) {
	if firstStop(locationText) == "" {
		return false
	}
	if strings.TrimSpace(os.Getenv("GOOGLE_MAPS_API_KEY")) == "" {
		// No key, no goroutine (and none in tests, which run without one).
		geocodeNoKeyOnce.Do(func() { log.Printf("[geocode] GOOGLE_MAPS_API_KEY not set — tasks get no coordinates") })
		return false
	}
	select {
	case geocodeSlots <- struct{}{}:
	default:
		log.Printf("[geocode] task=%s: skipped, all %d lookup slots busy", taskID, cap(geocodeSlots))
		return false
	}
	fn := geocodeFn // read here, on the caller's goroutine (a test may swap it)
	geocodeWG.Add(1)
	go func() {
		defer geocodeWG.Done()
		defer func() { <-geocodeSlots }()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[geocode] task=%s: panic: %v", taskID, r)
			}
		}()
		if err := geocodeTaskWith(context.Background(), fn, taskID, locationText); err != nil {
			log.Printf("[geocode] task=%s: %v", taskID, err)
		}
	}()
	return true
}

// geocodeMissing: called when live tracking reads a task with no
// coordinates — tasks posted before this existed, and lookups that failed
// (a deploy, a quota blip). In the background, at most once per
// geocodeRetryAfter per task; the answer being given now is unaffected.
func geocodeMissing(taskID, locationText string) {
	now := time.Now()
	geocodeTried.Lock()
	if geocodeTried.at == nil {
		geocodeTried.at = map[string]time.Time{}
	}
	if next, ok := geocodeTried.at[taskID]; ok && now.Before(next) {
		geocodeTried.Unlock()
		return
	}
	if len(geocodeTried.at) > 1000 {
		for id, next := range geocodeTried.at {
			if now.After(next) {
				delete(geocodeTried.at, id)
			}
		}
	}
	geocodeTried.at[taskID] = now.Add(geocodeRetryAfter)
	geocodeTried.Unlock()
	if !geocodeTaskAsync(taskID, locationText) {
		// Not begun (slots busy, no key): the next question asks again.
		geocodeTried.Lock()
		delete(geocodeTried.at, taskID)
		geocodeTried.Unlock()
	}
}

// geocodeNotPlaceable: an address Google cannot place is not asked about
// again for an hour (review: one billable call per 10 min, forever).
func geocodeNotPlaceable(taskID string) {
	geocodeTried.Lock()
	defer geocodeTried.Unlock()
	if geocodeTried.at == nil {
		geocodeTried.at = map[string]time.Time{}
	}
	geocodeTried.at[taskID] = time.Now().Add(geocodeNotPlaceableFor)
}

const geocodeNotPlaceableFor = time.Hour

var geocodeNoKeyOnce sync.Once

// geocodeTask writes the coordinates of the task's first stop, if the task
// still has the location text they were computed from and no coordinates
// for it yet. The address itself is never logged.
func geocodeTask(ctx context.Context, taskID, locationText string) error {
	return geocodeTaskWith(ctx, geocodeFn, taskID, locationText)
}

func geocodeTaskWith(ctx context.Context, fn func(context.Context, string) (float64, float64, error), taskID, locationText string) error {
	// Already placed for this exact text (an edit that kept the address, a
	// second trigger): no call (review: one billable lookup per address).
	var placed bool
	rctx, rcancel := context.WithTimeout(ctx, geocodeWriteTimeout)
	defer rcancel()
	if err := db.QueryRow(rctx, `
		select exists (select 1 from public.tasks
		 where id = $1::uuid and location_text is not distinct from $2 and job_lat is not null)`,
		taskID, locationText).Scan(&placed); err != nil {
		return fmt.Errorf("read coordinates: %w", err)
	}
	if placed {
		return nil
	}
	lctx, cancel := context.WithTimeout(ctx, geocodeTimeout)
	lat, lng, err := fn(lctx, firstStop(locationText))
	cancel()
	switch {
	case errors.Is(err, helpers.ErrGeocodeNoKey):
		geocodeNoKeyOnce.Do(func() { log.Printf("[geocode] GOOGLE_MAPS_API_KEY not set — tasks get no coordinates") })
		return nil
	case errors.Is(err, helpers.ErrNotGeocodable):
		geocodeNotPlaceable(taskID)
		return nil // nothing to write; distances stay unknown for this task
	case err != nil && strings.Contains(err.Error(), "REQUEST_DENIED"):
		// Every time, not once per process (2026-10-30: the one line was
		// logged at 17:34 and every later denial was silent, so a fix to
		// the key could not be told from no fix). One line per new task.
		log.Printf("[geocode] task=%s: REQUEST_DENIED — enable the Geocoding API for GOOGLE_MAPS_API_KEY's project (and allow it in the key's API restrictions)", taskID)
		return nil
	case err != nil:
		return err
	}
	wctx, wcancel := context.WithTimeout(ctx, geocodeWriteTimeout)
	defer wcancel()
	if _, err := db.Exec(wctx, `
		update public.tasks set job_lat = $2, job_lng = $3
		 where id = $1::uuid and location_text is not distinct from $4`,
		taskID, lat, lng, locationText); err != nil {
		return fmt.Errorf("write coordinates: %w", err)
	}
	return nil
}
