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

// nycBounds biases results toward New York City (HO:RA's service area) —
// a bias, not a filter: an address elsewhere still resolves.
const nycBounds = "40.4774,-74.2591|40.9176,-73.7004"

type geocodeResponse struct {
	Status  string `json:"status"`
	Results []struct {
		Types        []string `json:"types"`
		PartialMatch bool     `json:"partial_match"`
		Geometry     struct {
			Location struct {
				Lat float64 `json:"lat"`
				Lng float64 `json:"lng"`
			} `json:"location"`
			LocationType string `json:"location_type"`
		} `json:"geometry"`
	} `json:"results"`
}

// GeocodeAddress turns a street address into coordinates with the Google
// Geocoding API, using the same key as the Distance Matrix call
// (GOOGLE_MAPS_API_KEY — the key must have the Geocoding API enabled). The
// address is never logged (it is the requester's), and errors carry the
// API's status only, never the key or the address.
func GeocodeAddress(ctx context.Context, address string) (float64, float64, error) {
	key := strings.TrimSpace(os.Getenv("GOOGLE_MAPS_API_KEY"))
	if key == "" {
		return 0, 0, ErrGeocodeNoKey
	}
	address = strings.TrimSpace(address)
	if address == "" {
		return 0, 0, ErrNotGeocodable
	}
	q := url.Values{}
	q.Set("address", address)
	q.Set("bounds", nycBounds)
	q.Set("region", "us")
	q.Set("key", key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, GeocodeURL+"?"+q.Encode(), nil)
	if err != nil {
		return 0, 0, fmt.Errorf("geocode: build request")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("geocode: request failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, 0, fmt.Errorf("geocode: read response")
	}
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("geocode: http %d", resp.StatusCode)
	}
	var r geocodeResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, 0, fmt.Errorf("geocode: bad response")
	}
	switch r.Status {
	case "OK":
	case "ZERO_RESULTS":
		return 0, 0, ErrNotGeocodable
	default:
		// REQUEST_DENIED (the Geocoding API not enabled for this key),
		// OVER_QUERY_LIMIT, INVALID_REQUEST, UNKNOWN_ERROR.
		return 0, 0, fmt.Errorf("geocode: status %s", r.Status)
	}
	if len(r.Results) == 0 {
		return 0, 0, ErrNotGeocodable
	}
	res := r.Results[0]
	loc := res.Geometry.Location
	if loc.Lat == 0 && loc.Lng == 0 {
		return 0, 0, ErrNotGeocodable
	}
	// A wrong point is worse than none: the chat states distances and
	// "at the door" as facts (review). "Brooklyn" or "home" come back
	// APPROXIMATE (a centroid); a bare street ("Broadway") comes back as a
	// route at its GEOMETRIC_CENTER — the middle of a 13-mile street; a
	// neighbourhood, a ZIP, a park, a city likewise name an area, not a
	// door. A real address with a typo still matches a building (ROOFTOP
	// or RANGE_INTERPOLATED, type street_address / premise) and is kept.
	for _, ty := range res.Types {
		if areaTypes[ty] {
			return 0, 0, ErrNotGeocodable
		}
	}
	switch {
	case res.Geometry.LocationType == "APPROXIMATE":
		return 0, 0, ErrNotGeocodable
	case res.PartialMatch && res.Geometry.LocationType == "GEOMETRIC_CENTER":
		return 0, 0, ErrNotGeocodable
	}
	return loc.Lat, loc.Lng, nil
}

// areaTypes: result types that name an area or a line, not a place to stand.
var areaTypes = map[string]bool{
	"route": true, "intersection": true, "locality": true, "sublocality": true,
	"sublocality_level_1": true, "sublocality_level_2": true, "neighborhood": true,
	"postal_code": true, "administrative_area_level_1": true, "administrative_area_level_2": true,
	"administrative_area_level_3": true, "country": true, "political": true, "colloquial_area": true,
	"park": true, "natural_feature": true,
}
