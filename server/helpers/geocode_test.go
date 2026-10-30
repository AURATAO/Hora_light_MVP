package helpers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func geocodeServer(t *testing.T, body string, status int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("address") == "" || r.URL.Query().Get("key") == "" {
			t.Errorf("request lacks address or key: %s", r.URL.RawQuery)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	prev := GeocodeURL
	GeocodeURL = srv.URL
	t.Cleanup(func() { GeocodeURL = prev })
	t.Setenv("GOOGLE_MAPS_API_KEY", "test-key-not-real")
}

func TestGeocodeAddress(t *testing.T) {
	geocodeServer(t, `{"status":"OK","results":[{"geometry":{"location":{"lat":40.7128,"lng":-74.006},"location_type":"ROOFTOP"}}]}`, 200)
	lat, lng, err := GeocodeAddress(context.Background(), "12 Bedford Ave, Brooklyn")
	if err != nil || lat != 40.7128 || lng != -74.006 {
		t.Fatalf("got %v,%v err %v", lat, lng, err)
	}
}

func TestGeocodeAddressNoResult(t *testing.T) {
	for _, body := range []string{`{"status":"ZERO_RESULTS","results":[]}`, `{"status":"OK","results":[]}`} {
		geocodeServer(t, body, 200)
		if _, _, err := GeocodeAddress(context.Background(), "nowhere"); !errors.Is(err, ErrNotGeocodable) {
			t.Errorf("%s: err %v, want ErrNotGeocodable", body, err)
		}
	}
}

// A refusal (the Geocoding API not enabled for the key) is an error that
// names the status — and never the key or the address.
func TestGeocodeAddressErrorsCarryNoSecrets(t *testing.T) {
	geocodeServer(t, `{"status":"REQUEST_DENIED","error_message":"This API project is not authorized"}`, 200)
	_, _, err := GeocodeAddress(context.Background(), "12 Bedford Ave")
	if err == nil || !strings.Contains(err.Error(), "REQUEST_DENIED") {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "test-key-not-real") || strings.Contains(err.Error(), "Bedford") {
		t.Fatalf("error leaks the key or the address: %v", err)
	}
	geocodeServer(t, `oops`, 500)
	if _, _, err := GeocodeAddress(context.Background(), "12 Bedford Ave"); err == nil || strings.Contains(err.Error(), "Bedford") {
		t.Fatalf("http 500: %v", err)
	}
}

func TestGeocodeAddressWithoutAKey(t *testing.T) {
	t.Setenv("GOOGLE_MAPS_API_KEY", "")
	if _, _, err := GeocodeAddress(context.Background(), "12 Bedford Ave"); !errors.Is(err, ErrGeocodeNoKey) {
		t.Fatalf("err %v", err)
	}
}

// Review: a centroid or a guessed area is not a place to measure against.
func TestGeocodeAddressRefusesApproximateResults(t *testing.T) {
	for _, body := range []string{
		`{"status":"OK","results":[{"geometry":{"location":{"lat":40.65,"lng":-73.95},"location_type":"APPROXIMATE"}}]}`,
		`{"status":"OK","results":[{"partial_match":true,"geometry":{"location":{"lat":40.65,"lng":-73.95},"location_type":"GEOMETRIC_CENTER"}}]}`,
	} {
		geocodeServer(t, body, 200)
		if _, _, err := GeocodeAddress(context.Background(), "Brooklyn"); !errors.Is(err, ErrNotGeocodable) {
			t.Errorf("%s: err %v", body, err)
		}
	}
	// Review r2: a bare street is a route at its midpoint, not a door.
	geocodeServer(t, `{"status":"OK","results":[{"types":["route"],"geometry":{"location":{"lat":40.75,"lng":-73.98},"location_type":"GEOMETRIC_CENTER"}}]}`, 200)
	if _, _, err := GeocodeAddress(context.Background(), "Broadway"); !errors.Is(err, ErrNotGeocodable) {
		t.Errorf("a bare street: err %v", err)
	}
	geocodeServer(t, `{"status":"OK","results":[{"types":["neighborhood","political"],"geometry":{"location":{"lat":40.72,"lng":-73.95},"location_type":"ROOFTOP"}}]}`, 200)
	if _, _, err := GeocodeAddress(context.Background(), "Williamsburg"); !errors.Is(err, ErrNotGeocodable) {
		t.Errorf("a neighbourhood: err %v", err)
	}
	// A typo in a real address still matches a building.
	geocodeServer(t, `{"status":"OK","results":[{"types":["street_address"],"partial_match":true,"geometry":{"location":{"lat":40.71,"lng":-73.96},"location_type":"RANGE_INTERPOLATED"}}]}`, 200)
	if _, _, err := GeocodeAddress(context.Background(), "12 Bedfrod Ave"); err != nil {
		t.Errorf("a partial street match: %v", err)
	}
}
