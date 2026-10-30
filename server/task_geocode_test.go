package main

import (
	"context"
	"errors"
	"testing"

	"hora-auth/helpers"
)

func TestFirstStop(t *testing.T) {
	for in, want := range map[string]string{
		"12 Bedford Ave, Brooklyn":              "12 Bedford Ave, Brooklyn",
		"  12 Bedford Ave | 300 Broadway  ":     "12 Bedford Ave",
		"Whole Foods Union Sq → 12 Bedford Ave": "Whole Foods Union Sq",
		"A -> B":          "A",
		"":                "",
		" | 300 Broadway": "",
	} {
		if got := firstStop(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func fakeGeocode(t *testing.T, lat, lng float64, err error) *[]string {
	t.Helper()
	var asked []string
	prev := geocodeFn
	geocodeFn = func(_ context.Context, address string) (float64, float64, error) {
		asked = append(asked, address)
		return lat, lng, err
	}
	t.Cleanup(func() { geocodeFn = prev })
	return &asked
}

func taskCoords(t *testing.T, taskID string) (*float64, *float64) {
	t.Helper()
	var lat, lng *float64
	if err := db.QueryRow(context.Background(), `select job_lat, job_lng from public.tasks where id = $1::uuid`, taskID).Scan(&lat, &lng); err != nil {
		t.Fatal(err)
	}
	return lat, lng
}

// The first stop is geocoded and written; an edit that moved the address
// meanwhile wins (the write is a compare-and-set on the text it was made
// from); no key or no result writes nothing and is not an error.
func TestGeocodeTaskWritesTheFirstStop(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	ctx := context.Background()
	const text = "12 Bedford Ave, Brooklyn | 300 Broadway"
	if _, err := db.Exec(ctx, `update public.tasks set location_text = $2 where id = $1::uuid`, w.taskID, text); err != nil {
		t.Fatal(err)
	}

	asked := fakeGeocode(t, 40.7, -73.9, nil)
	if err := geocodeTask(ctx, w.taskID, text); err != nil {
		t.Fatal(err)
	}
	if lat, lng := taskCoords(t, w.taskID); lat == nil || *lat != 40.7 || lng == nil || *lng != -73.9 {
		t.Fatalf("coordinates %v,%v", lat, lng)
	}
	if len(*asked) != 1 || (*asked)[0] != "12 Bedford Ave, Brooklyn" {
		t.Fatalf("asked %v, want the first stop only", *asked)
	}

	// Placed already for this text: no second (billable) lookup.
	asked2 := fakeGeocode(t, 1, 1, nil)
	if err := geocodeTask(ctx, w.taskID, text); err != nil || len(*asked2) != 0 {
		t.Fatalf("a placed task was looked up again: %v %v", *asked2, err)
	}

	// Computed from an address the task no longer has: not written.
	fakeGeocode(t, 1, 1, nil)
	if err := geocodeTask(ctx, w.taskID, "an old address"); err != nil {
		t.Fatal(err)
	}
	if lat, _ := taskCoords(t, w.taskID); lat == nil || *lat != 40.7 {
		t.Fatalf("a stale lookup overwrote the coordinates: %v", lat)
	}

	// No key, no result: nothing written, no error (coordinates cleared
	// first, so the lookup is actually made).
	if _, err := db.Exec(ctx, `update public.tasks set job_lat = null, job_lng = null where id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	for _, e := range []error{helpers.ErrGeocodeNoKey, helpers.ErrNotGeocodable} {
		fakeGeocode(t, 9, 9, e)
		if err := geocodeTask(ctx, w.taskID, text); err != nil {
			t.Fatalf("%v: %v", e, err)
		}
		if lat, _ := taskCoords(t, w.taskID); lat != nil {
			t.Fatalf("%v: coordinates written: %v", e, *lat)
		}
	}
	// A real failure is returned (and logged by the caller).
	fakeGeocode(t, 0, 0, errors.New("geocode: status OVER_QUERY_LIMIT"))
	if err := geocodeTask(ctx, w.taskID, text); err == nil {
		t.Fatal("a failure was swallowed")
	}
}

// The background path end to end: a key, a lookup started, the write made;
// geocodeMissing does not start a second lookup inside its window.
func TestGeocodeTaskAsyncAndMissing(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	ctx := context.Background()
	const text = "12 Bedford Ave, Brooklyn"
	if _, err := db.Exec(ctx, `update public.tasks set location_text = $2, job_lat = null, job_lng = null where id = $1::uuid`, w.taskID, text); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_MAPS_API_KEY", "test-key-not-real")
	asked := fakeGeocode(t, 40.7, -73.9, nil)
	geocodeTried.Lock()
	geocodeTried.at = nil
	geocodeTried.Unlock()

	geocodeMissing(w.taskID, text)
	geocodeWG.Wait()
	if lat, _ := taskCoords(t, w.taskID); lat == nil || *lat != 40.7 || len(*asked) != 1 {
		t.Fatalf("async: lat %v asked %v", lat, *asked)
	}
	// Inside the window: not begun again (and placed anyway).
	if _, err := db.Exec(ctx, `update public.tasks set job_lat = null, job_lng = null where id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	geocodeMissing(w.taskID, text)
	geocodeWG.Wait()
	if len(*asked) != 1 {
		t.Fatalf("a second lookup inside the window: %v", *asked)
	}
	// No key: nothing begun.
	t.Setenv("GOOGLE_MAPS_API_KEY", "")
	if geocodeTaskAsync(w.taskID, text) {
		t.Fatal("a lookup started without a key")
	}
}
