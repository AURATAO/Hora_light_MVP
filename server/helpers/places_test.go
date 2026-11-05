package helpers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const placesBody = `{"places":[
 {"id":"gA","displayName":{"text":"Joe's\nLaundromat​"},"shortFormattedAddress":"12 E 14th St, New York","formattedAddress":"12 E 14th St, New York, NY 10003, USA",
  "addressComponents":[{"longText":"New York County","shortText":"New York County","types":["administrative_area_level_2","political"]},{"longText":"New York","shortText":"NY","types":["administrative_area_level_1"]},{"longText":"United States","shortText":"US","types":["country"]}],
  "businessStatus":"OPERATIONAL","rating":4.6,"userRatingCount":230,
  "currentOpeningHours":{"openNow":true,"nextCloseTime":"2026-11-05T04:00:00Z"}},
 {"id":"gB","displayName":{"text":"Hoboken Wash"},"formattedAddress":"1 Washington St, Hoboken, NJ 07030, USA",
  "addressComponents":[{"longText":"Hudson County","shortText":"Hudson County","types":["administrative_area_level_2"]},{"longText":"New Jersey","shortText":"NJ","types":["administrative_area_level_1"]},{"longText":"United States","shortText":"US","types":["country"]}],
  "businessStatus":"OPERATIONAL"},
 {"id":"gC","displayName":{"text":"Gone Cleaners"},"businessStatus":"CLOSED_PERMANENTLY"}
]}`

// One Text Search: the key and the field mask in headers (never the URL),
// the query in the body; closed places dropped; the area judged by the
// county rule; names made one clean line; times in New York.
func TestPlacesSearch(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Goog-Api-Key") != "k" || !strings.Contains(r.Header.Get("X-Goog-FieldMask"), "places.currentOpeningHours") || r.URL.RawQuery != "" {
			http.Error(w, "bad", 400)
			return
		}
		if strings.Contains(r.Header.Get("X-Goog-FieldMask"), "Phone") || strings.Contains(r.Header.Get("X-Goog-FieldMask"), "reviews") {
			http.Error(w, "phone or reviews requested", 400)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(placesBody))
	}))
	defer srv.Close()
	prev := PlacesSearchURL
	PlacesSearchURL = srv.URL
	t.Cleanup(func() { PlacesSearchURL = prev })
	t.Setenv("GOOGLE_MAPS_API_KEY", "k")
	nyc, _ := time.LoadLocation("America/New_York")

	ps, err := PlacesSearch(context.Background(), "laundromat near Union Square, Manhattan", true, 5, nyc)
	if err != nil {
		t.Fatal(err)
	}
	if got["textQuery"] != "laundromat near Union Square, Manhattan" || got["openNow"] != true {
		t.Fatalf("body %v", got)
	}
	if len(ps) != 2 {
		t.Fatalf("got %d places", len(ps))
	}
	a, b := ps[0], ps[1]
	if a.Name != "Joe's Laundromat" || a.Area != AreaInside || a.ClosesAt != "11 PM" || a.OpenNow == nil || !*a.OpenNow || a.FullAddress != "12 E 14th St, New York, NY 10003" {
		t.Fatalf("a = %+v", a)
	}
	if b.Area != AreaOutside || b.Address == "" {
		t.Fatalf("b = %+v", b)
	}
}

func TestPlacesNoKey(t *testing.T) {
	t.Setenv("GOOGLE_MAPS_API_KEY", "")
	if _, err := PlacesSearch(context.Background(), "x", false, 5, nil); err != ErrPlacesNoKey {
		t.Fatalf("err %v", err)
	}
	if _, err := PlaceDetails(context.Background(), "../etc", nil); err == nil {
		t.Fatal("bad id accepted")
	}
}

func TestCleanPlaceText(t *testing.T) {
	if got := CleanPlaceText("  A\tB‮\nC  ", 80); got != "A B C" {
		t.Fatalf("%q", got)
	}
	if got := CleanPlaceText("abcdef", 3); got != "abc" {
		t.Fatalf("%q", got)
	}
}

// A name lookup (a pick by name): the place id in the path, the name and
// nothing else in the field mask, the name made one clean line.
func TestPlaceName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Goog-Api-Key") != "k" || r.Header.Get("X-Goog-FieldMask") != "displayName" || r.URL.Path != "/gA" || r.Method != http.MethodGet {
			http.Error(w, "bad", 400)
			return
		}
		_, _ = w.Write([]byte(`{"displayName":{"text":"Joe's\nLaundromat"}}`))
	}))
	defer srv.Close()
	prev := PlacesDetailsURL
	PlacesDetailsURL = srv.URL + "/"
	t.Cleanup(func() { PlacesDetailsURL = prev })
	t.Setenv("GOOGLE_MAPS_API_KEY", "k")
	name, err := PlaceName(context.Background(), "gA")
	if err != nil || name != "Joe's Laundromat" {
		t.Fatalf("name %q err %v", name, err)
	}
	if _, err := PlaceName(context.Background(), "../etc"); err == nil {
		t.Fatal("bad id accepted")
	}
}
