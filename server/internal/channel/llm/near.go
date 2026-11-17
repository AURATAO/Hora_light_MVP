package llm

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Places near an area (2026-11-17: "near W 54th St" showed a Blue Bottle on
// W 15th St, two miles south). Results are ordered by distance from the
// area the person named, closest first, and anything farther than
// NearMiles is dropped — unless nothing is that close, when the closest
// one alone is kept.

// NearMiles: how far "near" reaches.
const NearMiles = 2.0

// AreaLocator finds the point of an area (a street, a neighbourhood, an
// address) to measure from. ok=false: it could not be placed.
type AreaLocator interface {
	LocateArea(ctx context.Context, area string) (lat, lng float64, ok bool)
}

// NearSearcher is a search biased to a circle around a point.
type NearSearcher interface {
	SearchPlacesNear(ctx context.Context, query string, openNow bool, lat, lng, radiusMeters float64) ([]Place, error)
}

// LocateArea: the area's point when the searcher can place areas.
func LocateArea(ctx context.Context, s PlaceSearcher, area string) (float64, float64, bool) {
	l, ok := s.(AreaLocator)
	if !ok || strings.TrimSpace(area) == "" {
		return 0, 0, false
	}
	return l.LocateArea(ctx, area)
}

// queryAreaRE: the area a search names ("pizzeria near 250 Mercer St",
// "Blue Bottle Coffee in Williamsburg", "pharmacy around Union Square").
// The last marker wins: "pizza by the slice near 250 Mercer St" is near
// 250 Mercer St.
var queryAreaRE = regexp.MustCompile(`(?i)^.*\b(?:near|around|close to|in)\s+(.+)$`)

// boroughRE: a whole borough is not "near" anything — never cut to two
// miles of its middle.
var boroughRE = regexp.MustCompile(`(?i)^(?:the\s+)?(?:manhattan|brooklyn|queens|(?:the\s+)?bronx|staten island|nyc|new york(?: city)?|the city)(?:,?\s*(?:new york|ny|nyc))*(?:,?\s*ny)?\.?$`)

// QueryArea is the area a search query is near, "" none.
func QueryArea(q string) string {
	m := queryAreaRE.FindStringSubmatch(strings.TrimSpace(q))
	if m == nil {
		return ""
	}
	a := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[1]), ", New York, NY"))
	if a == "" || boroughRE.MatchString(a) {
		return ""
	}
	return a
}

// Miles between two points, as the streets go: north–south plus east–west
// (a city-block distance). In a grid city that is the walk, not the crow's
// line: 450 W 15th St is 1.9 miles from W 54th St in a straight line but
// 2.7 on foot, and it is not "near" (2026-11-17).
func Miles(lat1, lng1, lat2, lng2 float64) float64 {
	const perDegLat = 69.0
	mid := (lat1 + lat2) / 2 * math.Pi / 180
	return math.Abs(lat2-lat1)*perDegLat + math.Abs(lng2-lng1)*perDegLat*math.Cos(mid)
}

// IsBorough: a whole borough (or the city) — never "near" anything.
func IsBorough(area string) bool { return boroughRE.MatchString(strings.TrimSpace(area)) }

// SortNear orders places by distance from lat, lng and drops those farther
// than maxMiles; when none is that close, the closest alone is kept.
// Places with no point keep Google's order after the measured ones, and
// are dropped whenever any measured place is near.
func SortNear(places []Place, lat, lng, maxMiles float64) []Place {
	type d struct {
		p Place
		m float64
	}
	var known []d
	var unknown []Place
	for _, p := range places {
		if p.Lat == 0 && p.Lng == 0 {
			unknown = append(unknown, p)
			continue
		}
		known = append(known, d{p, Miles(lat, lng, p.Lat, p.Lng)})
	}
	if len(known) == 0 {
		return places // nothing to measure: Google's order
	}
	sort.SliceStable(known, func(i, j int) bool { return known[i].m < known[j].m })
	var out []Place
	for _, k := range known {
		if k.m <= maxMiles {
			out = append(out, k.p)
		}
	}
	if len(out) == 0 {
		return []Place{known[0].p}
	}
	return out
}

// sortOptionsNear orders single-place options by distance (routes keep
// their place in the order, after the measured ones).
func sortOptionsNear(opts [][]Place, lat, lng float64) [][]Place {
	dist := func(o []Place) float64 {
		if len(o) != 1 || (o[0].Lat == 0 && o[0].Lng == 0) {
			return math.Inf(1)
		}
		return Miles(lat, lng, o[0].Lat, o[0].Lng)
	}
	out := append([][]Place(nil), opts...)
	sort.SliceStable(out, func(i, j int) bool { return dist(out[i]) < dist(out[j]) })
	return out
}
