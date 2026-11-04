package helpers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"go.opentelemetry.io/otel/attribute"

	"hora-auth/internal/tracing"
)

// Places API (New) for the chat's place discovery (D-23). Server-side only,
// with the same key as geocoding (GOOGLE_MAPS_API_KEY — the key must have
// "Places API (New)" enabled).
//
// Billing (Google Maps Platform price list, checked 2026-11-04): a request
// is billed at the highest SKU any field in its mask belongs to. rating,
// userRatingCount and currentOpeningHours make every search Text Search
// Enterprise ($35 / 1,000, 1,000 free a month); a details lookup with
// displayName is Place Details Pro ($17 / 1,000, 5,000 free). No phone
// number, website, review or photo is ever requested: they cost more and
// the chat shows none of them (reviews and photos would also need their
// authors credited).
//
// Caching (Places API policies): only place ids may be stored. The chat
// keeps the ids of what it showed, never names or addresses, and looks a
// chosen place up again (PlaceDetails).

// PlacesSearchURL / PlacesDetailsURL are variables so tests can point them
// at a local server. Never set outside tests.
var (
	PlacesSearchURL  = "https://places.googleapis.com/v1/places:searchText"
	PlacesDetailsURL = "https://places.googleapis.com/v1/places/"
)

// ErrPlacesNoKey: GOOGLE_MAPS_API_KEY is not set on this service.
var ErrPlacesNoKey = errors.New("GOOGLE_MAPS_API_KEY not set")

// Place is one result as the chat may show it. Every field is Google's.
type Place struct {
	ID          string
	Name        string
	Address     string // shortFormattedAddress: "123 Main St, Brooklyn"
	FullAddress string // formattedAddress, without ", USA": what a task is booked at
	Rating      float64
	RatingCount int
	// OpenNow: nil when Google gives no hours.
	OpenNow  *bool
	ClosesAt string // local time the place next closes ("11 PM"), "" unknown
	OpensAt  string // local time it next opens, "" unknown
	// Area is the service-area verdict on the place's own address parts, by
	// the rule a task's address is held to (AreaOfParts).
	Area AreaVerdict
}

const (
	searchFields  = "places.id,places.displayName,places.shortFormattedAddress,places.formattedAddress,places.addressComponents,places.businessStatus,places.rating,places.userRatingCount,places.currentOpeningHours"
	detailsFields = "id,displayName,shortFormattedAddress,formattedAddress,addressComponents"
)

// nycRect is the five boroughs' bounding box (a filter on categorical
// searches, a bias on the rest). It takes in parts of New Jersey and Nassau
// County: each result is still judged by AreaOfParts.
var nycRect = map[string]any{"rectangle": map[string]any{
	"low":  map[string]float64{"latitude": 40.4774, "longitude": -74.2591},
	"high": map[string]float64{"latitude": 40.9176, "longitude": -73.7004},
}}

type placesWire struct {
	ID                    string                `json:"id"`
	DisplayName           struct{ Text string } `json:"displayName"`
	ShortFormattedAddress string                `json:"shortFormattedAddress"`
	FormattedAddress      string                `json:"formattedAddress"`
	AddressComponents     []struct {
		LongText  string   `json:"longText"`
		ShortText string   `json:"shortText"`
		Types     []string `json:"types"`
	} `json:"addressComponents"`
	BusinessStatus      string  `json:"businessStatus"`
	Rating              float64 `json:"rating"`
	UserRatingCount     int     `json:"userRatingCount"`
	CurrentOpeningHours *struct {
		OpenNow       *bool  `json:"openNow"`
		NextCloseTime string `json:"nextCloseTime"`
		NextOpenTime  string `json:"nextOpenTime"`
	} `json:"currentOpeningHours"`
}

// PlacesSearch is one Text Search (New) call: up to max results (1–10) for
// query, only places open now when openNow. Places that closed for good or
// for now are dropped; the rest come back in Google's order. The query is
// redacted in the trace like any text; the key never leaves this function.
func PlacesSearch(ctx context.Context, query string, openNow bool, max int, loc *time.Location) (out []Place, err error) {
	_, sp := tracing.StartSpan(ctx, "places.search", tracing.KindTool)
	start, httpStatus := time.Now(), 0
	defer func() {
		sp.Input(query)
		sp.Attr(
			attribute.Bool("hora.places.open_now", openNow),
			attribute.Int("hora.places.results", len(out)),
			attribute.Int64("hora.places.latency_ms", time.Since(start).Milliseconds()),
			attribute.Int("http.status_code", httpStatus),
		)
		sp.Fail(err)
		sp.End()
	}()
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("places: empty query")
	}
	if max < 1 || max > 10 {
		max = 5
	}
	body := map[string]any{
		"textQuery": query, "pageSize": max, "languageCode": "en", "regionCode": "us",
		"locationBias": nycRect,
	}
	if openNow {
		body["openNow"] = true
	}
	var r struct {
		Places []placesWire `json:"places"`
	}
	if httpStatus, err = placesCall(ctx, http.MethodPost, PlacesSearchURL, searchFields, body, &r); err != nil {
		return nil, err
	}
	for _, w := range r.Places {
		if w.BusinessStatus == "CLOSED_PERMANENTLY" || w.BusinessStatus == "CLOSED_TEMPORARILY" || w.ID == "" {
			continue
		}
		out = append(out, w.place(loc))
	}
	return out, nil
}

// PlaceDetails looks one place up again by id (Place Details Pro): name,
// address and the service-area verdict — no hours, no rating.
func PlaceDetails(ctx context.Context, id string, loc *time.Location) (p Place, err error) {
	_, sp := tracing.StartSpan(ctx, "places.details", tracing.KindTool)
	start, httpStatus := time.Now(), 0
	defer func() {
		sp.Attr(
			attribute.Int64("hora.places.latency_ms", time.Since(start).Milliseconds()),
			attribute.Int("http.status_code", httpStatus),
		)
		sp.Fail(err)
		sp.End()
	}()
	if !placeIDOK(id) {
		return Place{}, errors.New("places: bad place id")
	}
	var w placesWire
	if httpStatus, err = placesCall(ctx, http.MethodGet, PlacesDetailsURL+id, detailsFields, nil, &w); err != nil {
		return Place{}, err
	}
	if w.ID == "" {
		return Place{}, errors.New("places: no such place")
	}
	return w.place(loc), nil
}

// placeIDOK: a place id is letters, digits, '-' and '_' (it goes into a
// URL path).
func placeIDOK(id string) bool {
	if id == "" || len(id) > 300 {
		return false
	}
	for _, c := range id {
		if !(c == '-' || c == '_' || c < unicode.MaxASCII && (unicode.IsLetter(c) || unicode.IsDigit(c))) {
			return false
		}
	}
	return true
}

// placesCall sends one request. Errors carry the HTTP status only — never
// the key, the body or the query (S-12).
func placesCall(ctx context.Context, method, u, fields string, body any, out any) (int, error) {
	key := strings.TrimSpace(os.Getenv("GOOGLE_MAPS_API_KEY"))
	if key == "" {
		return 0, ErrPlacesNoKey
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, errors.New("places: build request")
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, errors.New("places: build request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", key)
	req.Header.Set("X-Goog-FieldMask", fields)
	resp, err := placesHTTP.Do(req)
	if err != nil {
		return 0, errors.New("places: request failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, errors.New("places: read response")
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("places: http %d", resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return resp.StatusCode, errors.New("places: bad response")
	}
	return resp.StatusCode, nil
}

var placesHTTP = &http.Client{Timeout: 10 * time.Second}

func (w placesWire) place(loc *time.Location) Place {
	p := Place{
		ID:          w.ID,
		Name:        CleanPlaceText(w.DisplayName.Text, 80),
		Address:     CleanPlaceText(w.ShortFormattedAddress, 120),
		FullAddress: CleanPlaceText(strings.TrimSuffix(strings.TrimSpace(w.FormattedAddress), ", USA"), 160),
		Rating:      w.Rating,
		RatingCount: w.UserRatingCount,
	}
	if p.Address == "" {
		p.Address = p.FullAddress
	}
	var country, state, county string
	for _, c := range w.AddressComponents {
		for _, ty := range c.Types {
			switch ty {
			case "country":
				country = c.ShortText
			case "administrative_area_level_1":
				state = c.ShortText
			case "administrative_area_level_2":
				county = c.LongText
			}
		}
	}
	p.Area = AreaOfParts(country, state, county)
	if h := w.CurrentOpeningHours; h != nil {
		p.OpenNow = h.OpenNow
		p.ClosesAt = clockAt(h.NextCloseTime, loc)
		p.OpensAt = clockAt(h.NextOpenTime, loc)
	}
	return p
}

// clockAt renders an RFC 3339 instant as a local clock time ("11 PM",
// "9:30 AM"); "" when there is none.
func clockAt(rfc string, loc *time.Location) string {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return ""
	}
	if loc != nil {
		t = t.In(loc)
	}
	if t.Minute() == 0 {
		return t.Format("3 PM")
	}
	return t.Format("3:04 PM")
}

// CleanPlaceText makes Google's text safe to put in a text message: one
// line, no control or format characters (a name is written by the business,
// not by Google), at most max runes.
func CleanPlaceText(s string, max int) string {
	var b strings.Builder
	n := 0
	space := false
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if n >= max {
			break
		}
		if space {
			b.WriteByte(' ')
			n++
			space = false
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
