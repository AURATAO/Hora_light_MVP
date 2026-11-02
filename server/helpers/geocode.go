package helpers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// ErrNotGeocodable: Google answered, and the address has no usable result
// (ZERO_RESULTS, or nothing in the response). A data condition, not a fault.
var ErrNotGeocodable = errors.New("address not geocodable")

// ErrGeocodeNoKey: GOOGLE_MAPS_API_KEY is not set on this service.
var ErrGeocodeNoKey = errors.New("GOOGLE_MAPS_API_KEY not set")

// GeocodeURL is the Geocoding API endpoint; a variable so tests can point it
// at a local server. Never set outside tests.
var GeocodeURL = "https://maps.googleapis.com/maps/api/geocode/json"

// nycBounds biases results toward New York City — a bias, not a filter: an
// address elsewhere still resolves. The rectangle takes in parts of New
// Jersey, Westchester and Long Island, so it says nothing about whether an
// address is in the service area; that is ServiceArea's job.
const nycBounds = "40.4774,-74.2591|40.9176,-73.7004"

type geocodeComponent struct {
	LongName  string   `json:"long_name"`
	ShortName string   `json:"short_name"`
	Types     []string `json:"types"`
}

type geocodeResult struct {
	Types             []string           `json:"types"`
	PartialMatch      bool               `json:"partial_match"`
	AddressComponents []geocodeComponent `json:"address_components"`
	Geometry          struct {
		Location struct {
			Lat float64 `json:"lat"`
			Lng float64 `json:"lng"`
		} `json:"location"`
		LocationType string `json:"location_type"`
	} `json:"geometry"`
}

type geocodeResponse struct {
	Status  string          `json:"status"`
	Results []geocodeResult `json:"results"`
}

// geocodeLookup asks the Google Geocoding API about an address, using the
// same key as the Distance Matrix call (GOOGLE_MAPS_API_KEY — the key must
// have the Geocoding API enabled). The address is never logged (it is the
// requester's), and errors carry the API's status only, never the key or the
// address. biased: lean toward NYC with nycBounds. At least one result, or an
// error.
func geocodeLookup(ctx context.Context, address string, biased bool) ([]geocodeResult, error) {
	key := strings.TrimSpace(os.Getenv("GOOGLE_MAPS_API_KEY"))
	if key == "" {
		return nil, ErrGeocodeNoKey
	}
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, ErrNotGeocodable
	}
	q := url.Values{}
	q.Set("address", address)
	if biased {
		q.Set("bounds", nycBounds)
	}
	q.Set("region", "us")
	q.Set("key", key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, GeocodeURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("geocode: build request")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("geocode: request failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("geocode: read response")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("geocode: http %d", resp.StatusCode)
	}
	var r geocodeResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("geocode: bad response")
	}
	switch r.Status {
	case "OK":
	case "ZERO_RESULTS":
		return nil, ErrNotGeocodable
	default:
		// REQUEST_DENIED (the Geocoding API not enabled for this key),
		// OVER_QUERY_LIMIT, INVALID_REQUEST, UNKNOWN_ERROR.
		return nil, fmt.Errorf("geocode: status %s", r.Status)
	}
	if len(r.Results) == 0 {
		return nil, ErrNotGeocodable
	}
	return r.Results, nil
}

// GeocodeAddress turns a street address into coordinates.
func GeocodeAddress(ctx context.Context, address string) (float64, float64, error) {
	results, err := geocodeLookup(ctx, address, true)
	if err != nil {
		return 0, 0, err
	}
	res := results[0]
	if !placeable(res) {
		return 0, 0, ErrNotGeocodable
	}
	loc := res.Geometry.Location
	return loc.Lat, loc.Lng, nil
}

// placeable: the result names a place to stand.
//
// A wrong point is worse than none: the chat states distances and "at the
// door" as facts (review). "Brooklyn" or "home" come back APPROXIMATE (a
// centroid); a bare street ("Broadway") comes back as a route at its
// GEOMETRIC_CENTER — the middle of a 13-mile street; a neighbourhood, a ZIP, a
// park, a city likewise name an area, not a door. A real address with a typo
// still matches a building (ROOFTOP or RANGE_INTERPOLATED, type street_address
// / premise) and is kept.
func placeable(res geocodeResult) bool {
	loc := res.Geometry.Location
	if loc.Lat == 0 && loc.Lng == 0 {
		return false
	}
	for _, ty := range res.Types {
		if areaTypes[ty] {
			return false
		}
	}
	switch {
	case res.Geometry.LocationType == "APPROXIMATE":
		return false
	case res.PartialMatch && res.Geometry.LocationType == "GEOMETRIC_CENTER":
		return false
	}
	return true
}

// areaTypes: result types that name an area or a line, not a place to stand.
var areaTypes = map[string]bool{
	"route": true, "intersection": true, "locality": true, "sublocality": true,
	"sublocality_level_1": true, "sublocality_level_2": true, "neighborhood": true,
	"postal_code": true, "administrative_area_level_1": true, "administrative_area_level_2": true,
	"administrative_area_level_3": true, "country": true, "political": true, "colloquial_area": true,
	"park": true, "natural_feature": true,
}
