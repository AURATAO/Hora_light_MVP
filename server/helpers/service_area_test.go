package helpers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// googleResult is a Geocoding API result in the shape the API returns: a
// building unless the caller says otherwise.
type googleResult struct {
	types, locationType, county, state, country string
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
	return fmt.Sprintf(`{"types":[%q],"partial_match":%v,"address_components":[%s],"geometry":{"location":{"lat":40.7,"lng":-74.0},"location_type":%q}}`,
		types, g.partial, strings.Join(comps, ","), locType)
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
		if v, err := ServiceArea(context.Background(), address); v != AreaInside || err != nil {
			t.Errorf("%s: %v, %v", address, v, err)
		}
	}
}

func TestServiceAreaOutsideIsRefused(t *testing.T) {
	for _, c := range []struct {
		address string
		res     googleResult
	}{
		{"30 Hudson St, Jersey City", googleResult{county: "Hudson County", state: "NJ"}},
		{"Jersey City", googleResult{types: "locality", locationType: "APPROXIMATE", county: "Hudson County", state: "NJ"}},
		{"Hoboken Terminal", googleResult{county: "Hudson County", state: "NJ"}},
		{"Hoboken", googleResult{types: "locality", locationType: "APPROXIMATE", county: "Hudson County", state: "NJ"}},
		{"1 Larkin Plaza, Yonkers", googleResult{county: "Westchester County", state: "NY"}},
		{"Yonkers", googleResult{types: "locality", locationType: "APPROXIMATE", county: "Westchester County", state: "NY"}},
		{"358 Broadway, Hicksville", googleResult{county: "Nassau County", state: "NY"}},
		{"1 Main St, Southampton", googleResult{county: "Suffolk County", state: "NY"}},
		// The same county names, elsewhere.
		{"530 Greene St, Augusta", googleResult{county: "Richmond County", state: "GA"}},
		{"500 4th Ave, Seattle", googleResult{county: "Kings County", state: "WA"}},
		{"1 Yonge St, Toronto", googleResult{state: "ON", country: "CA"}},
	} {
		geocodeServer(t, googleSays(c.res), 200)
		if v, err := ServiceArea(context.Background(), c.address); v != AreaOutside || err != nil {
			t.Errorf("%s: %v, %v, want outside", c.address, v, err)
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
		if v, err := ServiceArea(context.Background(), "somewhere"); v != AreaUnclear || err != nil {
			t.Errorf("%s: %v, %v, want unclear", name, v, err)
		}
	}
}

// Several results on the same side are not ambiguous about the service area.
func TestServiceAreaSeveralResultsOneSide(t *testing.T) {
	geocodeServer(t, googleSays(googleResult{county: "Queens County", state: "NY"}, googleResult{county: "Richmond County", state: "NY"}), 200)
	if v, err := ServiceArea(context.Background(), "12 Main St"); v != AreaInside || err != nil {
		t.Fatalf("%v, %v", v, err)
	}
}

// The check failing is not the address's fault: an error, never a verdict the
// caller could book on or blame the person for.
func TestServiceAreaCheckFailureIsAnError(t *testing.T) {
	geocodeServer(t, `{"status":"REQUEST_DENIED"}`, 200)
	v, err := ServiceArea(context.Background(), "12 Bedford Ave")
	if err == nil || v != AreaUnclear || !strings.Contains(err.Error(), "REQUEST_DENIED") {
		t.Fatalf("denied: %v, %v", v, err)
	}
	geocodeServer(t, `oops`, 500)
	if v, err := ServiceArea(context.Background(), "12 Bedford Ave"); err == nil || v != AreaUnclear {
		t.Fatalf("http 500: %v, %v", v, err)
	}
	t.Setenv("GOOGLE_MAPS_API_KEY", "")
	if v, err := ServiceArea(context.Background(), "12 Bedford Ave"); err == nil || v != AreaUnclear {
		t.Fatalf("no key: %v, %v", v, err)
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
	if v, err := ServiceArea(context.Background(), "12 Bedford Ave, Jersey City NJ"); v != AreaUnclear || err != nil {
		t.Fatalf("%v, %v, want unclear", v, err)
	}
	// A typo in a real NYC address, or an apartment Google can't match, is the
	// same place either way.
	biasedServer(t, googleSays(brooklyn), googleSays(brooklyn))
	if v, err := ServiceArea(context.Background(), "12 Bedfrod Ave, Apt 3B, Brooklyn"); v != AreaInside || err != nil {
		t.Fatalf("%v, %v, want inside", v, err)
	}
	biasedServer(t, googleSays(brooklyn), `{"status":"ZERO_RESULTS","results":[]}`)
	if v, err := ServiceArea(context.Background(), "12 Bedfrod Ave"); v != AreaUnclear || err != nil {
		t.Fatalf("%v, %v, want unclear", v, err)
	}
	// An exact match costs one lookup, not two.
	calls := biasedServer(t, googleSays(googleResult{county: "Kings County", state: "NY"}), `oops`)
	if v, err := ServiceArea(context.Background(), "12 Bedford Ave, Brooklyn"); v != AreaInside || err != nil || *calls != 1 {
		t.Fatalf("%v, %v, calls %d", v, err, *calls)
	}
}
