package helpers

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"unicode"
)

// AreaVerdict is what ServiceArea says about one address. The zero value
// refuses: nothing books on an answer nobody gave.
type AreaVerdict int

const (
	AreaUnclear      AreaVerdict = iota // can't be placed, or could be more than one place
	AreaInside                          // in one of the five boroughs
	AreaOutside                         // somewhere else
	AreaWhichBorough                    // no city given, and it exists in several boroughs
)

// AreaResult: Resolved is the address as found, on Inside: street, borough or
// city, ZIP. Boroughs names the candidates, on AreaWhichBorough.
type AreaResult struct {
	Verdict  AreaVerdict
	Resolved string
	Boroughs []string
}

// nycBoroughs are the five boroughs by the county Google names them with in
// administrative_area_level_2. A county line IS a borough line, so Marble Hill
// and JFK come out right, and it takes no boundary data to keep current. Other
// states have a Richmond, Kings and Queens County too: the state is checked.
var nycBoroughs = []struct{ county, name string }{
	{"New York County", "Manhattan"},
	{"Kings County", "Brooklyn"},
	{"Queens County", "Queens"},
	{"Bronx County", "the Bronx"},
	{"Richmond County", "Staten Island"},
}

func isNYCCounty(county string) bool {
	for _, b := range nycBoroughs {
		if strings.EqualFold(strings.TrimSpace(county), b.county) {
			return true
		}
	}
	return false
}

// ServiceArea decides by rule whether an address is in the five boroughs, from
// the county and state Google returns. HO:RA only runs in NYC, so an address
// that names no city, state or ZIP is taken to be in NYC and looked for in each
// borough; one that names any of them is looked up as written. An error means
// nothing is known (no key, REQUEST_DENIED, quota, a timeout): the caller must
// not book on it, and must not tell the person their address was the problem.
func ServiceArea(ctx context.Context, address string) (AreaResult, error) {
	results, err := geocodeLookup(ctx, address, nycBias)
	switch {
	case err != nil && !errors.Is(err, ErrNotGeocodable):
		return AreaResult{}, err
	case addressNamesPlace(address), err == nil && echoesPlace(address, results[0]):
		if err != nil {
			return AreaResult{}, nil
		}
		return asWritten(ctx, address, results)
	}
	return inTheBoroughs(ctx, address)
}

// asWritten reads the lookup of an address that says where it is.
func asWritten(ctx context.Context, address string, results []geocodeResult) (AreaResult, error) {
	top := classifyArea(results[0])
	inside := AreaResult{Verdict: top}
	if top == AreaInside {
		inside.Resolved = resolvedAddress(address, results[0])
	}
	// Google offering places on both sides of the line is an ambiguous
	// address, whichever it ranked first.
	for _, res := range results[1:] {
		if v := classifyArea(res); v != AreaUnclear && v != top {
			return AreaResult{}, nil
		}
	}
	if top != AreaInside || !results[0].PartialMatch {
		return inside, nil
	}
	// A partial match is Google guessing, and nycBounds leans the guess toward
	// New York: an address in Jersey City on a street it doesn't have can land
	// on the Brooklyn street of that name. Asked again without the lean, a
	// typo in a real NYC address stays put; that one does not.
	results, err := geocodeLookup(ctx, address, nil)
	if errors.Is(err, ErrNotGeocodable) {
		return AreaResult{}, nil
	}
	if err != nil {
		return AreaResult{}, err
	}
	if classifyArea(results[0]) != AreaInside {
		return AreaResult{}, nil
	}
	return inside, nil
}

// inTheBoroughs looks for an address with no city in each borough. The filter
// only leans the search (Google enforces postal_code and country, not
// administrative_area, and has no strict bounds), so the rule is on what comes
// back: an exact match, in the county that was asked for. A partial match is
// how Google says the street isn't in that borough.
func inTheBoroughs(ctx context.Context, address string) (AreaResult, error) {
	street := strings.TrimSpace(unitRE.ReplaceAllString(address, ""))
	found := make([]map[string]geocodeResult, len(nycBoroughs))
	errs := make([]error, len(nycBoroughs))
	var wg sync.WaitGroup
	for i, b := range nycBoroughs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := geocodeLookup(ctx, street, url.Values{
				"components": {"country:US|administrative_area:" + b.county},
			})
			if err != nil {
				errs[i] = err
				return
			}
			found[i] = map[string]geocodeResult{}
			for _, res := range results {
				_, _, county := areaParts(res)
				if !res.PartialMatch && classifyArea(res) == AreaInside && strings.EqualFold(county, b.county) {
					found[i][firstNonBlank(res.PlaceID, res.FormattedAddress)] = res
				}
			}
		}()
	}
	wg.Wait()
	var boroughs []string
	var matches []geocodeResult
	for i, b := range nycBoroughs {
		if errs[i] != nil && !errors.Is(errs[i], ErrNotGeocodable) {
			return AreaResult{}, errs[i]
		}
		if len(found[i]) > 0 {
			boroughs = append(boroughs, b.name)
		}
		for _, res := range found[i] {
			matches = append(matches, res)
		}
	}
	switch {
	case len(boroughs) > 1:
		return AreaResult{Verdict: AreaWhichBorough, Boroughs: boroughs}, nil
	case len(matches) == 1:
		return AreaResult{Verdict: AreaInside, Resolved: resolvedAddress(address, matches[0])}, nil
	}
	return AreaResult{}, nil // nothing in NYC, or several in one borough: the borough wouldn't help
}

func firstNonBlank(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var (
	zipRE = regexp.MustCompile(`(?:^|[\s,])\d{5}(?:-\d{4})?\s*(?:,|$)`)
	// A state by its postal code, in capitals and at the end, so that "near me"
	// and "or" are not Maine and Oregon.
	stateRE = regexp.MustCompile(`[\s,](?:A[KLRZ]|C[AOT]|D[CE]|FL|GA|HI|I[ADLN]|K[SY]|LA|M[ADEINOST]|N[CDEHJMVY]|O[HKR]|PA|RI|S[CD]|T[NX]|UT|V[AT]|W[AIVY])\.?\s*(?:,|\d|$)`)
	// streetWordRE: where a street's name ends, so anything after it is a city
	// ("1 Main St Flushing"). The last one counts: "100 Court St".
	streetWordRE = regexp.MustCompile(`(?i)\b(?:st|street|ave?|avenue|rd|road|blvd|boulevard|pl|place|dr|drive|ln|lane|ct|court|ter|terrace|pkwy|parkway|hwy|highway|sq|square|plz|plaza|broadway|way)\b\.?`)
	directionRE  = regexp.MustCompile(`(?i)^(?:n|s|e|w|north|south|east|west)\.?$`)
	// unitRE: an apartment or suite, which is not a place Google can vouch for.
	unitRE = regexp.MustCompile(`(?i)[\s,]*((?:\b(?:apt|apartment|unit|suite|ste|floor|fl|rm|room)\b\.?\s*#?\s*[\w-]+)|#\s*[\w-]+)`)
)

// addressNamesPlace reads the words: a ZIP, a state, anything after the street.
func addressNamesPlace(address string) bool {
	t := unitRE.ReplaceAllString(address, "")
	if zipRE.MatchString(t) || stateRE.MatchString(t) {
		return true
	}
	parts := strings.Split(t, ",")
	for _, p := range parts[1:] {
		if strings.IndexFunc(p, unicode.IsLetter) >= 0 {
			return true
		}
	}
	ends := streetWordRE.FindAllStringIndex(parts[0], -1)
	if len(ends) == 0 {
		return false
	}
	tail := strings.TrimSpace(parts[0][ends[len(ends)-1][1]:])
	return len([]rune(tail)) > 1 && strings.IndexFunc(tail, unicode.IsLetter) >= 0 && !directionRE.MatchString(tail)
}

// echoedTypes: the parts of a result a person might have written as the place.
var echoedTypes = map[string]bool{
	"locality": true, "postal_town": true, "sublocality": true, "sublocality_level_1": true,
	"neighborhood": true, "administrative_area_level_1": true, "administrative_area_level_3": true,
}

// echoesPlace catches what no pattern can: "Hoboken", "Hoboken Terminal". The
// words name a city if the result's own city is among them, once the street's
// name is out of the way ("100 Yonkers Ave" names a street).
func echoesPlace(address string, res geocodeResult) bool {
	text := strings.ToLower(address)
	for _, c := range res.AddressComponents {
		if hasType(c, "route") {
			text = strings.ReplaceAll(strings.ReplaceAll(text, strings.ToLower(c.LongName), " "), strings.ToLower(c.ShortName), " ")
		}
	}
	for _, c := range res.AddressComponents {
		for _, ty := range c.Types {
			if echoedTypes[ty] && regexp.MustCompile(`\b`+regexp.QuoteMeta(strings.ToLower(c.LongName))+`\b`).MatchString(text) {
				return true
			}
		}
	}
	return false
}

func hasType(c geocodeComponent, want string) bool {
	for _, ty := range c.Types {
		if ty == want {
			return true
		}
	}
	return false
}

// resolvedAddress is the result as a person reads it. Google drops what it
// can't match, so the words that matter and aren't in it are put back: the
// apartment, and the name of a place ("Trader Joe's").
func resolvedAddress(original string, res geocodeResult) string {
	full := strings.TrimSuffix(strings.TrimSpace(res.FormattedAddress), ", USA")
	if full == "" {
		return ""
	}
	street, _, _ := strings.Cut(full, ",")
	if m := unitRE.FindStringSubmatch(original); m != nil && !strings.Contains(strings.ToLower(full), strings.ToLower(m[1])) {
		full = street + ", " + m[1] + full[len(street):]
	}
	if isEstablishment(res) && !strings.Contains(strings.ToLower(original), strings.ToLower(street)) {
		name, _, _ := strings.Cut(original, ",")
		full = strings.TrimSpace(name) + ", " + full
	}
	return full
}

func isEstablishment(res geocodeResult) bool {
	for _, ty := range res.Types {
		if ty == "establishment" || ty == "point_of_interest" {
			return true
		}
	}
	return false
}

func areaParts(res geocodeResult) (country, state, county string) {
	for _, c := range res.AddressComponents {
		switch {
		case hasType(c, "country"):
			country = c.ShortName
		case hasType(c, "administrative_area_level_1"):
			state = c.ShortName
		case hasType(c, "administrative_area_level_2"):
			county = c.LongName
		}
	}
	return country, state, county
}

// classifyArea reads one result. Where it is comes first, so "Hoboken" (a city,
// not a door) is outside rather than unclear; inside the boroughs it must also
// be a place to stand, by the bar the task's coordinates are held to.
func classifyArea(res geocodeResult) AreaVerdict {
	country, state, county := areaParts(res)
	switch {
	case country != "" && country != "US":
		return AreaOutside
	case state == "":
		return AreaUnclear
	case state != "NY":
		return AreaOutside
	case county == "":
		return AreaUnclear // "New York" the state, "Long Island" the island
	case !isNYCCounty(county):
		return AreaOutside
	case !placeable(res):
		return AreaUnclear
	}
	return AreaInside
}
