package helpers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const factsBody = `{"id":"gN","displayName":{"text":"Nolita Pizza"},"shortFormattedAddress":"68 Kenmare St, New York","formattedAddress":"68 Kenmare St, New York, NY 10012, USA",
 "addressComponents":[{"longText":"New York County","shortText":"New York County","types":["administrative_area_level_2"]},{"longText":"New York","shortText":"NY","types":["administrative_area_level_1"]},{"longText":"United States","shortText":"US","types":["country"]}],
 "rating":4.5,"userRatingCount":1840,"priceLevel":"PRICE_LEVEL_INEXPENSIVE",
 "priceRange":{"startPrice":{"currencyCode":"USD","units":"10"},"endPrice":{"currencyCode":"USD","units":"20"}},
 "location":{"latitude":40.7215,"longitude":-73.9962},"websiteUri":"https://www.nolitapizza.example/menu",
 "regularOpeningHours":{"weekdayDescriptions":["Monday: 11:00 AM – 11:00 PM","Sunday: 12:00 – 10:00 PM"]},
 "reviewSummary":{"text":{"text":"People love the spicy vodka slice.","languageCode":"en-US"},"disclosureText":{"text":"Summarized with Gemini"},"reviewsUri":"https://maps.google.com/?cid=1&reviews"},
 "generativeSummary":{"overview":{"text":"Counter-service pizzeria."},"disclosureText":{"text":"Summarized with Gemini"}},
 "takeout":true,"delivery":false,"servesVegetarianFood":true,
 "accessibilityOptions":{"wheelchairAccessibleEntrance":true},
 "paymentOptions":{"acceptsCreditCards":false,"acceptsDebitCards":true,"acceptsCashOnly":false,"acceptsNfc":true}}`

// The answer lookup asks for the Atmosphere fields it uses and nothing
// that costs more or must not be shown (reviews, photos, phone numbers),
// and reads Google's nested shapes.
func TestPlaceFactsLookup(t *testing.T) {
	var mask string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mask = r.Header.Get("X-Goog-FieldMask")
		_, _ = w.Write([]byte(factsBody))
	}))
	defer srv.Close()
	prev := PlacesDetailsURL
	PlacesDetailsURL = srv.URL + "/"
	t.Cleanup(func() { PlacesDetailsURL = prev })
	t.Setenv("GOOGLE_MAPS_API_KEY", "k")
	nyc, _ := time.LoadLocation("America/New_York")
	f, err := PlaceFactsLookup(context.Background(), "gN", nyc)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"reviewSummary.text", "reviewSummary.reviewsUri", "generativeSummary.overview", "servesVegetarianFood", "paymentOptions", "regularOpeningHours", "location"} {
		if !strings.Contains(mask, want) {
			t.Errorf("mask lacks %s", want)
		}
	}
	for _, bad := range []string{"Phone", "photos", ",reviews,", "reviews,"} {
		if strings.Contains(","+mask+",", bad) && bad != "reviews," {
			t.Errorf("mask asks for %s", bad)
		}
	}
	if strings.Contains(","+mask+",", ",reviews,") {
		t.Error("mask asks for reviews")
	}
	if f.Name != "Nolita Pizza" || f.PriceRange != "$10–20" || f.PriceLevel != 1 || f.Lat == 0 || f.Website == "" {
		t.Fatalf("facts %+v", f)
	}
	if f.ReviewSummary == nil || f.ReviewSummary.Disclosure != "Summarized with Gemini" || f.ReviewSummary.ReviewsURI == "" || f.GenerativeSummary == nil {
		t.Fatalf("summaries %+v %+v", f.ReviewSummary, f.GenerativeSummary)
	}
	if f.Takeout == nil || !*f.Takeout || f.Delivery == nil || *f.Delivery || f.GoodForGroups != nil {
		t.Fatal("booleans")
	}
	if f.AcceptsCards == nil || !*f.AcceptsCards || f.AcceptsNFC == nil || !*f.AcceptsNFC || f.WheelchairEntrance == nil {
		t.Fatal("payment / access")
	}
	if len(f.WeekdayHours) != 2 || f.Area != AreaInside {
		t.Fatalf("hours %v area %v", f.WeekdayHours, f.Area)
	}
}
