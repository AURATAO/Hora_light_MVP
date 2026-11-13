package helpers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// googleResult is a Geocoding API result in the shape the API returns: a
// building unless the caller says otherwise.
type googleResult struct {
	types, locationType, county, state, country string
	locality, route, formatted                  string
	partial                                     bool
}

func (g googleResult) json() string {
	types, locType, country := g.types, g.locationType, g.country
	if types == "" {
		types = "street_address"
	}
	if locType == "" {
		locType = "ROOFTOP"
	}
	if country == "" {
		country = "US"
	}
	comps := []string{fmt.Sprintf(`{"long_name":"United States","short_name":%q,"types":["country","political"]}`, country)}
	if g.state != "" {
		comps = append(comps, fmt.Sprintf(`{"long_name":"State","short_name":%q,"types":["administrative_area_level_1","political"]}`, g.state))
	}
	if g.county != "" {
		comps = append(comps, fmt.Sprintf(`{"long_name":%q,"short_name":"County","types":["administrative_area_level_2","political"]}`, g.county))
	}
	if g.locality != "" {
		comps = append(comps, fmt.Sprintf(`{"long_name":%q,"short_name":%q,"types":["locality","political"]}`, g.locality, g.locality))
	}
	if g.route != "" {
		comps = append(comps, fmt.Sprintf(`{"long_name":%q,"short_name":%q,"types":["route"]}`, g.route, g.route))
	}
	return fmt.Sprintf(`{"types":[%q],"partial_match":%v,"formatted_address":%q,"address_components":[%s],"geometry":{"location":{"lat":40.7,"lng":-74.0},"location_type":%q}}`,
		types, g.partial, g.formatted, strings.Join(comps, ","), locType)
}

func googleSays(results ...googleResult) string {
	parts := make([]string, len(results))
	for i, r := range results {
		parts[i] = r.json()
	}
	return `{"status":"OK","results":[` + strings.Join(parts, ",") + `]}`
}

func TestServiceAreaEachBoroughPasses(t *testing.T) {
	for address, county := range map[string]string{
		"350 5th Ave, Manhattan":        "New York County",
		"12 Bedford Ave, Brooklyn":      "Kings County",
		"37-11 74th St, Queens":         "Queens County",
		"1 E 161st St, the Bronx":       "Bronx County",
		"1 Bay St, Staten Island":       "Richmond County",
		"5 Marble Hill Ave (mainland)":  "New York County",
		"JFK Terminal 4 (street level)": "Queens County",
	} {
		geocodeServer(t, googleSays(googleResult{county: county, state: "NY"}), 200)
		if r, err := ServiceArea(context.Background(), address); r.Verdict != AreaInside || err != nil {
			t.Errorf("%s: %+v, %v", address, r, err)
		}
	}
}

func TestServiceAreaOutsideIsRefused(t *testing.T) {
	for _, c := range []struct {
		address string
		res     googleResult
	}{
		{"30 Hudson St, Jersey City", googleResult{county: "Hudson County", state: "NJ"}},
		{"Jersey City", googleResult{types: "locality", locationType: "APPROXIMATE", county: "Hudson County", state: "NJ", locality: "Jersey City"}},
		{"Hoboken Terminal", googleResult{county: "Hudson County", state: "NJ", locality: "Hoboken"}},
		{"Hoboken", googleResult{types: "locality", locationType: "APPROXIMATE", county: "Hudson County", state: "NJ", locality: "Hoboken"}},
		{"1 Larkin Plaza, Yonkers", googleResult{county: "Westchester County", state: "NY"}},
		{"Yonkers", googleResult{types: "locality", locationType: "APPROXIMATE", county: "Westchester County", state: "NY", locality: "Yonkers"}},
		{"358 Broadway, Hicksville", googleResult{county: "Nassau County", state: "NY"}},
		{"1 Main St, Southampton", googleResult{county: "Suffolk County", state: "NY"}},
		// The same county names, elsewhere.
		{"530 Greene St, Augusta", googleResult{county: "Richmond County", state: "GA"}},
		{"500 4th Ave, Seattle", googleResult{county: "Kings County", state: "WA"}},
		{"1 Yonge St, Toronto", googleResult{state: "ON", country: "CA"}},
	} {
		geocodeServer(t, googleSays(c.res), 200)
		if r, err := ServiceArea(context.Background(), c.address); r.Verdict != AreaOutside || err != nil {
			t.Errorf("%s: %+v, %v, want outside", c.address, r, err)
		}
	}
}

func TestServiceAreaUnclearIsNotBooked(t *testing.T) {
	inside := googleResult{county: "Kings County", state: "NY"}
	outside := googleResult{county: "Hudson County", state: "NJ"}
	for name, body := range map[string]string{
		"unresolvable":            `{"status":"ZERO_RESULTS","results":[]}`,
		"no results":              `{"status":"OK","results":[]}`,
		"Long Island, the island": googleSays(googleResult{types: "natural_feature", locationType: "APPROXIMATE", state: "NY"}),
		"New York, the state":     googleSays(googleResult{types: "administrative_area_level_1", locationType: "APPROXIMATE", state: "NY"}),
		"no state in the answer":  googleSays(googleResult{county: "Kings County"}),
		"a borough, no address":   googleSays(googleResult{types: "sublocality_level_1", locationType: "APPROXIMATE", county: "Kings County", state: "NY"}),
		"a bare street":           googleSays(googleResult{types: "route", locationType: "GEOMETRIC_CENTER", county: "New York County", state: "NY"}),
		"a neighbourhood":         googleSays(googleResult{types: "neighborhood", locationType: "APPROXIMATE", county: "Kings County", state: "NY"}),
		"both sides, NYC first":   googleSays(inside, outside),
		"both sides, NJ first":    googleSays(outside, inside),
	} {
		geocodeServer(t, body, 200)
		if r, err := ServiceArea(context.Background(), "12 Main St, Somewhere"); r.Verdict != AreaUnclear || err != nil {
			t.Errorf("%s: %+v, %v, want unclear", name, r, err)
		}
	}
}

// Several results on the same side are not ambiguous about the service area.
func TestServiceAreaSeveralResultsOneSide(t *testing.T) {
	geocodeServer(t, googleSays(googleResult{county: "Queens County", state: "NY"}, googleResult{county: "Richmond County", state: "NY"}), 200)
	if r, err := ServiceArea(context.Background(), "12 Main St, Elmhurst"); r.Verdict != AreaInside || err != nil {
		t.Fatalf("%+v, %v", r, err)
	}
}

// The check failing is not the address's fault: an error, never a verdict the
// caller could book on or blame the person for.
func TestServiceAreaCheckFailureIsAnError(t *testing.T) {
	geocodeServer(t, `{"status":"REQUEST_DENIED"}`, 200)
	r, err := ServiceArea(context.Background(), "12 Bedford Ave")
	if err == nil || r.Verdict != AreaUnclear || !strings.Contains(err.Error(), "REQUEST_DENIED") {
		t.Fatalf("denied: %+v, %v", r, err)
	}
	geocodeServer(t, `oops`, 500)
	if r, err := ServiceArea(context.Background(), "12 Bedford Ave"); err == nil || r.Verdict != AreaUnclear {
		t.Fatalf("http 500: %+v, %v", r, err)
	}
	t.Setenv("GOOGLE_MAPS_API_KEY", "")
	if r, err := ServiceArea(context.Background(), "12 Bedford Ave"); err == nil || r.Verdict != AreaUnclear {
		t.Fatalf("no key: %+v, %v", r, err)
	}
}

// biasedServer answers differently with and without nycBounds.
func biasedServer(t *testing.T, biased, unbiased string) *int {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("bounds") != "" {
			_, _ = w.Write([]byte(biased))
			return
		}
		_, _ = w.Write([]byte(unbiased))
	}))
	t.Cleanup(srv.Close)
	prev := GeocodeURL
	GeocodeURL = srv.URL
	t.Cleanup(func() { GeocodeURL = prev })
	t.Setenv("GOOGLE_MAPS_API_KEY", "test-key-not-real")
	return &calls
}

// nycBounds must not be able to walk a Jersey City address onto the Brooklyn
// street of the same name.
func TestServiceAreaPartialMatchIsNotLedByTheBias(t *testing.T) {
	brooklyn := googleResult{county: "Kings County", state: "NY", partial: true, locationType: "RANGE_INTERPOLATED"}
	biasedServer(t, googleSays(brooklyn), googleSays(googleResult{types: "locality", locationType: "APPROXIMATE", county: "Hudson County", state: "NJ", partial: true}))
	if r, err := ServiceArea(context.Background(), "12 Bedford Ave, Jersey City NJ"); r.Verdict != AreaUnclear || err != nil {
		t.Fatalf("%+v, %v, want unclear", r, err)
	}
	// A typo in a real NYC address, or an apartment Google can't match, is the
	// same place either way.
	biasedServer(t, googleSays(brooklyn), googleSays(brooklyn))
	if r, err := ServiceArea(context.Background(), "12 Bedfrod Ave, Apt 3B, Brooklyn"); r.Verdict != AreaInside || err != nil {
		t.Fatalf("%+v, %v, want inside", r, err)
	}
	biasedServer(t, googleSays(brooklyn), `{"status":"ZERO_RESULTS","results":[]}`)
	if r, err := ServiceArea(context.Background(), "12 Bedfrod Ave, Brooklyn"); r.Verdict != AreaUnclear || err != nil {
		t.Fatalf("%+v, %v, want unclear", r, err)
	}
	// An exact match costs one lookup, not two.
	calls := biasedServer(t, googleSays(googleResult{county: "Kings County", state: "NY"}), `oops`)
	if r, err := ServiceArea(context.Background(), "12 Bedford Ave, Brooklyn"); r.Verdict != AreaInside || err != nil || *calls != 1 {
		t.Fatalf("%+v, %v, calls %d", r, err, *calls)
	}
}

// ── an address with no city ─────────────────────────────────────────────────

const noResults = `{"status":"ZERO_RESULTS","results":[]}`

// boroughGoogle answers the unfiltered lookup with plain, and each borough's
// filtered lookup from byCounty; a county not listed gets a partial match
// elsewhere, as Google does: the filter is a lean, not a wall.
func boroughGoogle(t *testing.T, plain string, byCounty map[string]string) *[]url.Values {
	t.Helper()
	var mu sync.Mutex
	var asked []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		asked = append(asked, q)
		mu.Unlock()
		f := q.Get("components")
		if f == "" {
			_, _ = w.Write([]byte(plain))
			return
		}
		for county, body := range byCounty {
			if strings.Contains(f, "administrative_area:"+county) {
				_, _ = w.Write([]byte(body))
				return
			}
		}
		_, _ = w.Write([]byte(googleSays(googleResult{county: "Hudson County", state: "NJ", partial: true})))
	}))
	t.Cleanup(srv.Close)
	prev := GeocodeURL
	GeocodeURL = srv.URL
	t.Cleanup(func() { GeocodeURL = prev })
	t.Setenv("GOOGLE_MAPS_API_KEY", "test-key-not-real")
	return &asked
}

func TestNoCityOneBoroughIsAccepted(t *testing.T) {
	// The bias alone ranks Hoboken first, and that must not count.
	hoboken := googleResult{county: "Hudson County", state: "NJ", locality: "Hoboken", route: "Washington St", formatted: "100 Washington St, Hoboken, NJ 07030, USA"}
	asked := boroughGoogle(t, googleSays(hoboken), map[string]string{
		"New York County": googleSays(googleResult{county: "New York County", state: "NY", locality: "New York", route: "Washington St", formatted: "100 Washington St, New York, NY 10006, USA"}),
		"Kings County":    googleSays(googleResult{county: "Kings County", state: "NY", partial: true, route: "Washington Ave", formatted: "100 Washington Ave, Brooklyn, NY 11205, USA"}),
	})
	r, err := ServiceArea(context.Background(), "100 Washington St")
	if err != nil || r.Verdict != AreaInside || r.Resolved != "100 Washington St, New York, NY 10006" {
		t.Fatalf("%+v, %v", r, err)
	}
	if len(*asked) != 6 {
		t.Fatalf("%d lookups, want one plus one per borough", len(*asked))
	}
}

func TestNoCitySeveralBoroughsAsksWhich(t *testing.T) {
	street := func(county string) string {
		return googleSays(googleResult{county: county, state: "NY", route: "Broadway", formatted: "100 Broadway, " + county + ", NY, USA"})
	}
	boroughGoogle(t, street("New York County"), map[string]string{
		"New York County": street("New York County"), "Kings County": street("Kings County"), "Queens County": street("Queens County"),
	})
	r, err := ServiceArea(context.Background(), "Broadway 100")
	if err != nil || r.Verdict != AreaWhichBorough || !reflect.DeepEqual(r.Boroughs, []string{"Manhattan", "Brooklyn", "Queens"}) {
		t.Fatalf("%+v, %v", r, err)
	}
}

// Two of it in one borough: naming the borough wouldn't help.
func TestNoCitySeveralInOneBoroughIsUnclear(t *testing.T) {
	a := googleResult{county: "Queens County", state: "NY", formatted: "12 Main St, Queens, NY 11354, USA"}
	b := googleResult{county: "Queens County", state: "NY", formatted: "12 Main St, Queens, NY 11101, USA"}
	boroughGoogle(t, googleSays(a), map[string]string{"Queens County": googleSays(a, b)})
	if r, err := ServiceArea(context.Background(), "12 Main St"); err != nil || r.Verdict != AreaUnclear {
		t.Fatalf("%+v, %v", r, err)
	}
}

func TestNoCityNothingInNYCIsUnclear(t *testing.T) {
	boroughGoogle(t, noResults, nil) // every borough: a partial match in NJ
	if r, err := ServiceArea(context.Background(), "asdf qwerty zzz"); err != nil || r.Verdict != AreaUnclear {
		t.Fatalf("%+v, %v", r, err)
	}
	// A partial match in the right county is not the street it was asked for.
	boroughGoogle(t, noResults, map[string]string{"Kings County": googleSays(googleResult{county: "Kings County", state: "NY", partial: true})})
	if r, err := ServiceArea(context.Background(), "100 Washingtn St"); err != nil || r.Verdict != AreaUnclear {
		t.Fatalf("%+v, %v", r, err)
	}
	// A borough's centroid is not the address either.
	boroughGoogle(t, noResults, map[string]string{"Kings County": googleSays(googleResult{types: "sublocality_level_1", locationType: "APPROXIMATE", county: "Kings County", state: "NY"})})
	if r, err := ServiceArea(context.Background(), "the market"); err != nil || r.Verdict != AreaUnclear {
		t.Fatalf("%+v, %v", r, err)
	}
}

func TestNoCityCheckFailureIsAnError(t *testing.T) {
	boroughGoogle(t, noResults, map[string]string{"Bronx County": `{"status":"OVER_QUERY_LIMIT"}`})
	if r, err := ServiceArea(context.Background(), "100 Washington St"); err == nil || r.Verdict != AreaUnclear {
		t.Fatalf("%+v, %v", r, err)
	}
}

// A place Google can't match the apartment of, and a name it doesn't repeat,
// keep what the person said.
func TestResolvedKeepsTheUnitAndTheName(t *testing.T) {
	bowery := googleResult{county: "New York County", state: "NY", route: "Bowery", formatted: "100 Bowery, New York, NY 10013, USA"}
	asked := boroughGoogle(t, noResults, map[string]string{"New York County": googleSays(bowery)})
	r, err := ServiceArea(context.Background(), "100 Bowery apt 3B")
	if err != nil || r.Resolved != "100 Bowery, apt 3B, New York, NY 10013" {
		t.Fatalf("%+v, %v", r, err)
	}
	for _, q := range *asked {
		if q.Get("components") != "" && strings.Contains(q.Get("address"), "3B") {
			t.Fatalf("the unit went to the street search: %q", q.Get("address"))
		}
	}
	bowery.types, bowery.formatted = "establishment", "142 E 14th St, New York, NY 10003, USA"
	boroughGoogle(t, noResults, map[string]string{"New York County": googleSays(bowery)})
	if r, err := ServiceArea(context.Background(), "Trader Joe's"); err != nil || r.Resolved != "Trader Joe's, 142 E 14th St, New York, NY 10003" {
		t.Fatalf("%+v, %v", r, err)
	}
}

// Rule 2: an address that says where it is goes as written, even with the
// borough lookups available.
func TestAddressThatNamesAPlaceGoesAsWritten(t *testing.T) {
	flushing := googleResult{county: "Queens County", state: "NY", formatted: "1 Main St, Flushing, NY 11354, USA"}
	for _, c := range []struct {
		address string
		res     googleResult
		want    AreaVerdict
	}{
		{"100 Washington St, Hoboken", googleResult{county: "Hudson County", state: "NJ"}, AreaOutside},
		{"100 Main St, Richmond VA", googleResult{county: "Richmond City", state: "VA"}, AreaOutside},
		{"100 Main St Richmond VA", googleResult{county: "Richmond City", state: "VA"}, AreaOutside},
		{"100 Washington St Hoboken", googleResult{county: "Hudson County", state: "NJ"}, AreaOutside},
		{"1 Main St, Flushing", flushing, AreaInside},
		{"1 Main St Flushing", flushing, AreaInside},
	} {
		asked := boroughGoogle(t, googleSays(c.res), nil)
		r, err := ServiceArea(context.Background(), c.address)
		if err != nil || r.Verdict != c.want {
			t.Errorf("%s: %+v, %v", c.address, r, err)
		}
		if len(*asked) != 1 {
			t.Errorf("%s: %d lookups", c.address, len(*asked))
		}
	}
}

func TestAddressNamesPlace(t *testing.T) {
	for address, want := range map[string]bool{
		"100 Washington St":          false,
		"100 Washington St.":         false,
		"100 Washington St apt 3B":   false,
		"100 Washington St #4":       false,
		"Broadway 100":               false,
		"100 Court St":               false,
		"350 5th Ave":                false,
		"145 E 32nd St":              false,
		"100 Park Ave South":         false,
		"12345 Main St":              false,
		"Trader Joe's":               false,
		"the Starbucks on 8th Ave":   false,
		"pick up near me":            false,
		"100 Washington St, Hoboken": true,
		"100 Washington St Hoboken":  true,
		"1 Main St Flushing":         true,
		"100 Main St, Richmond VA":   true,
		"100 Main St Richmond VA":    true,
		"100 Washington St 10006":    true,
		"100 Washington St, 10006":   true,
		"10006":                      true,
		"100 Washington St NY":       true,
		"100 Washington St, apt 3":   false,
	} {
		if got := addressNamesPlace(address); got != want {
			t.Errorf("%q: %v", address, got)
		}
	}
}

// The street's own name is not a city.
func TestEchoesPlace(t *testing.T) {
	yonkers := geocodeResult{AddressComponents: []geocodeComponent{
		{LongName: "Yonkers Avenue", ShortName: "Yonkers Ave", Types: []string{"route"}},
		{LongName: "Yonkers", ShortName: "Yonkers", Types: []string{"locality"}},
	}}
	if echoesPlace("100 Yonkers Ave", yonkers) {
		t.Error("a street named for a city")
	}
	if !echoesPlace("Yonkers", yonkers) || !echoesPlace("the mall in yonkers", yonkers) {
		t.Error("the city itself")
	}
}

// Six lookups at once: a stop with no city waits for the slowest, not the sum.
func TestNoCityLookupsRunTogether(t *testing.T) {
	const each = 150 * time.Millisecond
	var mu sync.Mutex
	inFlight, most, calls := 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		inFlight++
		if inFlight > most {
			most = inFlight
		}
		mu.Unlock()
		time.Sleep(each)
		mu.Lock()
		inFlight--
		mu.Unlock()
		_, _ = w.Write([]byte(noResults))
	}))
	t.Cleanup(srv.Close)
	prev := GeocodeURL
	GeocodeURL = srv.URL
	t.Cleanup(func() { GeocodeURL = prev })
	t.Setenv("GOOGLE_MAPS_API_KEY", "test-key-not-real")

	start := time.Now()
	if _, err := ServiceArea(context.Background(), "100 Washington St"); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	if calls != 6 || most != 6 || took >= 2*each {
		t.Fatalf("%d lookups, %d at once, %v (one takes %v)", calls, most, took, each)
	}
}

// A stuck Google ends at the caller's deadline, as an error nothing books on.
func TestNoCityLookupsStopAtTheDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	prev := GeocodeURL
	GeocodeURL = srv.URL
	t.Cleanup(func() { GeocodeURL = prev })
	t.Setenv("GOOGLE_MAPS_API_KEY", "test-key-not-real")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	r, err := ServiceArea(ctx, "100 Washington St")
	if err == nil || r.Verdict != AreaUnclear || time.Since(start) > time.Second {
		t.Fatalf("%+v, %v, %v", r, err, time.Since(start))
	}
}

// A place found by the echo, not the words, still refuses: the borough lookups
// beside it don't count.
func TestEchoWinsOverTheBoroughs(t *testing.T) {
	hoboken := googleResult{types: "locality", locationType: "APPROXIMATE", county: "Hudson County", state: "NJ", locality: "Hoboken"}
	boroughGoogle(t, googleSays(hoboken), map[string]string{
		"Kings County": googleSays(googleResult{county: "Kings County", state: "NY", formatted: "1 Hoboken St, Brooklyn, NY, USA"}),
	})
	if r, err := ServiceArea(context.Background(), "Hoboken"); err != nil || r.Verdict != AreaOutside {
		t.Fatalf("%+v, %v", r, err)
	}
}

// A NYC neighbourhood is unclear (no door to stand at) and named as one;
// a borough or a place outside is not (2026-11-12, "East Village").
func TestServiceAreaNamesANeighborhood(t *testing.T) {
	hood := `{"status":"OK","results":[{"types":["neighborhood","political"],"partial_match":false,"formatted_address":"East Village, New York, NY, USA",` +
		`"address_components":[{"long_name":"East Village","short_name":"East Village","types":["neighborhood","political"]},` +
		`{"long_name":"Manhattan","short_name":"Manhattan","types":["sublocality_level_1","sublocality","political"]},` +
		`{"long_name":"New York","short_name":"New York","types":["locality","political"]},` +
		`{"long_name":"New York County","short_name":"New York County","types":["administrative_area_level_2","political"]},` +
		`{"long_name":"New York","short_name":"NY","types":["administrative_area_level_1","political"]},` +
		`{"long_name":"United States","short_name":"US","types":["country","political"]}],` +
		`"geometry":{"location":{"lat":40.726,"lng":-73.981},"location_type":"APPROXIMATE"}}]}`
	geocodeServer(t, hood, 200)
	r, err := ServiceArea(context.Background(), "East Village")
	if err != nil || r.Verdict != AreaUnclear || r.Neighborhood != "East Village" {
		t.Fatalf("%+v, %v", r, err)
	}
	geocodeServer(t, googleSays(googleResult{county: "Kings County", state: "NY", formatted: "12 Bedford Ave, Brooklyn, NY 11211, USA"}), 200)
	if r, err := ServiceArea(context.Background(), "12 Bedford Ave, Brooklyn"); err != nil || r.Verdict != AreaInside || r.Neighborhood != "" {
		t.Fatalf("an address: %+v, %v", r, err)
	}
	jc := strings.Replace(strings.Replace(hood, "New York County", "Hudson County", 2), `"short_name":"NY"`, `"short_name":"NJ"`, 1)
	geocodeServer(t, jc, 200)
	if r, err := ServiceArea(context.Background(), "East Village"); err != nil || r.Neighborhood != "" {
		t.Fatalf("outside: %+v, %v", r, err)
	}
}
