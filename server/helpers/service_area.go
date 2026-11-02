package helpers

import (
	"context"
	"errors"
	"strings"
)

// AreaVerdict is what ServiceArea says about one address. The zero value
// refuses: nothing books on an answer nobody gave.
type AreaVerdict int

const (
	AreaUnclear AreaVerdict = iota // can't be placed, or could be more than one place
	AreaInside                     // in one of the five boroughs
	AreaOutside                    // somewhere else
)

// nycCounties are the five boroughs as Google names them in
// administrative_area_level_2. A county line IS a borough line, so Marble Hill
// and JFK come out right, and it takes no boundary data to keep current. Other
// states have a Richmond, Kings and Queens County too: the state is checked.
var nycCounties = map[string]bool{
	"new york county": true, // Manhattan
	"kings county":    true, // Brooklyn
	"queens county":   true,
	"bronx county":    true,
	"richmond county": true, // Staten Island
}

// ServiceArea decides by rule whether an address is in the five boroughs, from
// the county and state Google returns. An error means nothing is known (no
// key, REQUEST_DENIED, quota, a timeout): the caller must not book on it, and
// must not tell the person their address was the problem.
func ServiceArea(ctx context.Context, address string) (AreaVerdict, error) {
	results, err := geocodeLookup(ctx, address, true)
	if errors.Is(err, ErrNotGeocodable) {
		return AreaUnclear, nil
	}
	if err != nil {
		return AreaUnclear, err
	}
	top := classifyArea(results[0])
	// Google offering places on both sides of the line is an ambiguous
	// address, whichever it ranked first.
	for _, res := range results[1:] {
		if v := classifyArea(res); v != AreaUnclear && v != top {
			return AreaUnclear, nil
		}
	}
	if top != AreaInside || !results[0].PartialMatch {
		return top, nil
	}
	// A partial match is Google guessing, and nycBounds leans the guess toward
	// New York: an address in Jersey City on a street it doesn't have can land
	// on the Brooklyn street of that name. Asked again without the lean, a
	// typo in a real NYC address stays put; that one does not.
	results, err = geocodeLookup(ctx, address, false)
	if errors.Is(err, ErrNotGeocodable) {
		return AreaUnclear, nil
	}
	if err != nil {
		return AreaUnclear, err
	}
	if classifyArea(results[0]) != AreaInside {
		return AreaUnclear, nil
	}
	return AreaInside, nil
}

// classifyArea reads one result. Where it is comes first, so "Hoboken" (a city,
// not a door) is outside rather than unclear; inside the boroughs it must also
// be a place to stand, by the bar the task's coordinates are held to.
func classifyArea(res geocodeResult) AreaVerdict {
	var country, state, county string
	for _, c := range res.AddressComponents {
		for _, ty := range c.Types {
			switch ty {
			case "country":
				country = c.ShortName
			case "administrative_area_level_1":
				state = c.ShortName
			case "administrative_area_level_2":
				county = c.LongName
			}
		}
	}
	switch {
	case country != "" && country != "US":
		return AreaOutside
	case state == "":
		return AreaUnclear
	case state != "NY":
		return AreaOutside
	case county == "":
		return AreaUnclear // "New York" the state, "Long Island" the island
	case !nycCounties[strings.ToLower(strings.TrimSpace(county))]:
		return AreaOutside
	case !placeable(res):
		return AreaUnclear
	}
	return AreaInside
}
