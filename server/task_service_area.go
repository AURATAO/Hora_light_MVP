package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"hora-auth/helpers"
	"hora-auth/internal/channel"
)

// The service area: a task runs only if EVERY stop is in the five boroughs.
// createTask and updateTask refuse here; the chat asks the same question at
// draft time (chatBooker.CheckArea) and reaches createTask at the post.
//
// Fail-closed and on the request path, the opposite of the coordinates
// (task_geocode.go): a task with no coordinates only loses its distances, and
// a task outside the area is one nobody can run. If Google can't answer, nothing
// is posted.

// areaCheckTimeout bounds one stop, however many lookups it takes: six at once
// with no city, or at most two in a row. Past it the post is refused.
const areaCheckTimeout = 5 * time.Second

// maxTaskStops: the clients offer three stops and the chat two. More than this
// is not a task, and each stop is a billable lookup.
const maxTaskStops = 5

// The refusal's `code`, which postResultFrom branches on.
const (
	areaCodeOutside      = "outside_service_area"
	areaCodeUnclear      = "address_unclear"
	areaCodeWhichBorough = "address_which_borough"
	areaCodeUnavailable  = "address_check_unavailable"
)

const (
	areaMsgOutside     = "HO:RA only runs errands in New York City's five boroughs right now: Manhattan, Brooklyn, Queens, the Bronx and Staten Island."
	areaMsgNoAddress   = "Add the address of your task. HO:RA only runs errands in New York City's five boroughs."
	areaMsgUnavailable = "We can't check that address right now, so your task wasn't posted. Please try again in a few minutes."
)

// serviceAreaFn is the per-address rule; a variable so tests can replace it.
var serviceAreaFn = helpers.ServiceArea

// checkTaskArea is the whole check; TestMain replaces it, so that the rest of
// the suite need not spell out addresses.
var checkTaskArea = checkStops

// areaCheck: Stop is the address that decided anything but Inside ("" when the
// task names none). Resolved, on Inside: the stops in order, as the check found
// them. Err set: the check itself failed; Verdict is Unclear.
type areaCheck struct {
	Verdict  helpers.AreaVerdict
	Stop     string
	Resolved []string
	Err      error
}

// taskStops splits a task's location text into its stops.
func taskStops(locationText string) []string {
	t := locationText
	for _, sep := range stopSeparators[1:] {
		t = strings.ReplaceAll(t, sep, stopSeparators[0])
	}
	var stops []string
	for _, s := range strings.Split(t, stopSeparators[0]) {
		if s = strings.TrimSpace(s); s != "" {
			stops = append(stops, s)
		}
	}
	return stops
}

// checkStops: Outside on the first stop outside, whatever the others are, since
// no clearer address changes that. Then a failed check, then an unclear stop.
// No stops at all is Unclear: a task with no place can't be shown to be in NYC.
func checkStops(ctx context.Context, locationText string) areaCheck {
	stops := taskStops(locationText)
	if len(stops) == 0 || len(stops) > maxTaskStops {
		return areaCheck{Verdict: helpers.AreaUnclear}
	}
	worst := areaCheck{Verdict: helpers.AreaInside}
	var resolved []string
	for _, stop := range stops {
		lctx, cancel := context.WithTimeout(ctx, areaCheckTimeout)
		res, err := serviceAreaFn(lctx, stop)
		cancel()
		switch {
		case err != nil:
			worst = areaCheck{Verdict: helpers.AreaUnclear, Err: err}
		case res.Verdict == helpers.AreaOutside:
			return areaCheck{Verdict: helpers.AreaOutside, Stop: stop}
		case res.Verdict != helpers.AreaInside && worst.Err == nil:
			worst = areaCheck{Verdict: res.Verdict, Stop: stop}
		}
		if res.Resolved != "" {
			stop = res.Resolved
		}
		resolved = append(resolved, stop)
	}
	if worst.Verdict == helpers.AreaInside {
		worst.Resolved = resolved
	}
	return worst
}

// refuseTaskArea writes the refusal when the task's stops don't pass, and
// reports whether it did. On a pass, locationText becomes the addresses as
// resolved, so a stop given with no city is stored as the place it was taken
// to be.
//
// `error` carries the sentence as well as `message`: shipped mobile builds show
// `error` for anything but a 402, and a phrase with spaces is what web's
// apiErrorMessage and those builds both print as is. Branch on `code`.
func refuseTaskArea(c *gin.Context, ctx context.Context, uid string, locationText *string) bool {
	res := checkTaskArea(ctx, *locationText)
	if res.Err == nil && res.Verdict == helpers.AreaInside {
		if len(res.Resolved) > 0 {
			*locationText = strings.Join(res.Resolved, " | ")
		}
		return false
	}
	// The App Review account posts from wherever the reviewer is. Asked only
	// now, so an ordinary post costs no read; a failed read is not exempt.
	if sandbox, err := isSandboxUser(ctx, uid); err == nil && sandbox {
		return false
	}
	status, code, msg := http.StatusUnprocessableEntity, areaCodeUnclear, ""
	switch {
	case res.Err != nil:
		logAreaFailure(res.Err)
		status, code, msg = http.StatusServiceUnavailable, areaCodeUnavailable, areaMsgUnavailable
	case res.Verdict == helpers.AreaOutside:
		code, msg = areaCodeOutside, areaMsgOutside
	case res.Verdict == helpers.AreaWhichBorough:
		code, msg = areaCodeWhichBorough, fmt.Sprintf(channel.CopyWhichBorough, res.Stop)
	case res.Stop == "":
		msg = areaMsgNoAddress
	default:
		msg = fmt.Sprintf(channel.CopyAskClearerAddress, res.Stop)
	}
	c.JSON(status, gin.H{"error": msg, "message": msg, "code": code, "stop": res.Stop})
	return true
}

// logAreaFailure: every time, because each one is a refused booking. The
// address is never logged; a helpers error carries only Google's status.
func logAreaFailure(err error) {
	log.Printf("[service-area] check failed, booking refused: %v", err)
}
