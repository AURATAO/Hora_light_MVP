package llm

import (
	"strings"
	"testing"
)

// W 54th St between 6th and 7th Ave, and Blue Bottles around it.
const w54Lat, w54Lng = 40.7638, -73.9800

func TestSortNearClosestFirstDropsFar(t *testing.T) {
	ps := []Place{
		{ID: "w40", Lat: 40.7527, Lng: -73.9837},  // 54 W 40th St
		{ID: "w15", Lat: 40.7424, Lng: -74.0063},  // 450 W 15th St
		{ID: "rock", Lat: 40.7587, Lng: -73.9787}, // Rockefeller Plaza
		{ID: "nopoint"},
	}
	got := SortNear(ps, w54Lat, w54Lng, NearMiles)
	var ids []string
	for _, p := range got {
		ids = append(ids, p.ID)
	}
	if strings.Join(ids, ",") != "rock,w40" {
		t.Fatalf("order %v", ids)
	}
	if m := Miles(w54Lat, w54Lng, 40.7424, -74.0063); m <= NearMiles {
		t.Fatalf("W 15th St is %.2f miles away on the streets", m)
	}
	// Nothing within reach: the closest alone.
	far := SortNear([]Place{{ID: "a", Lat: 40.60, Lng: -73.95}, {ID: "b", Lat: 40.65, Lng: -73.95}}, w54Lat, w54Lng, NearMiles)
	if len(far) != 1 || far[0].ID != "b" {
		t.Fatalf("far %v", far)
	}
	// No points at all: Google's order.
	if got := SortNear([]Place{{ID: "x"}, {ID: "y"}}, w54Lat, w54Lng, NearMiles); len(got) != 2 || got[0].ID != "x" {
		t.Fatalf("unmeasured %v", got)
	}
}

func TestQueryArea(t *testing.T) {
	for q, want := range map[string]string{
		"Blue Bottle Coffee near W 54th St, Manhattan": "W 54th St, Manhattan",
		"pizza by the slice near 250 Mercer St":        "250 Mercer St",
		"pharmacy around Union Square":                 "Union Square",
		"dry cleaner open late in Brooklyn":            "",
		"Blue Bottle Coffee in Manhattan":              "",
		"laundromat":                                   "",
		"Duane Reade in the East Village":              "the East Village",
	} {
		if got := QueryArea(q); got != want {
			t.Errorf("%q: %q, want %q", q, got, want)
		}
	}
}

func TestSortOptionsNear(t *testing.T) {
	opts := [][]Place{{{ID: "w40", Lat: 40.7527, Lng: -73.9837}}, {{ID: "a"}, {ID: "b"}}, {{ID: "rock", Lat: 40.7587, Lng: -73.9787}}}
	got := sortOptionsNear(opts, w54Lat, w54Lng)
	if got[0][0].ID != "rock" || got[1][0].ID != "w40" || len(got[2]) != 2 {
		t.Fatalf("%v", got)
	}
}
