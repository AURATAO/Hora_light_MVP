package helpers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
)

// ErrTravelNotResolvable means the request was well-formed but Google could not
// resolve a driving route for the given origin/destination — an unmappable or
// bad address, or no route between the two points. This is a client-data
// condition (caller should surface a 4xx), not a server or upstream fault.
var ErrTravelNotResolvable = errors.New("could not resolve a travel route")

type distanceMatrixResponse struct {
	Status string `json:"status"`
	Rows   []struct {
		Elements []struct {
			Status   string `json:"status"`
			Duration struct {
				Value int `json:"value"` // seconds
			} `json:"duration"`
		} `json:"elements"`
	} `json:"rows"`
}

// GetTravelTime returns the estimated driving time in minutes from the origin
// coordinates to the destination address string, using the Google Distance Matrix API.
func GetTravelTime(originLat, originLng float64, destination string) (int, error) {
	apiKey := os.Getenv("GOOGLE_MAPS_API_KEY")
	if apiKey == "" {
		return 0, fmt.Errorf("GOOGLE_MAPS_API_KEY not set")
	}

	reqURL := fmt.Sprintf(
		"https://maps.googleapis.com/maps/api/distancematrix/json?origins=%f,%f&destinations=%s&mode=driving&key=%s",
		originLat, originLng,
		url.QueryEscape(destination),
		apiKey,
	)

	resp, err := http.Get(reqURL)
	if err != nil {
		return 0, fmt.Errorf("distance matrix request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read response: %w", err)
	}

	var result distanceMatrixResponse
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("[maps] parse error body=%s", string(body))
		return 0, fmt.Errorf("parse response: %w", err)
	}

	if result.Status != "OK" {
		log.Printf("[maps] bad status=%s body=%s", result.Status, string(body))
		// INVALID_REQUEST / NOT_FOUND mean the origin/destination we sent was
		// unusable — a caller-data problem (4xx). Other statuses (REQUEST_DENIED,
		// OVER_QUERY_LIMIT, UNKNOWN_ERROR) are server/upstream faults (5xx).
		if result.Status == "INVALID_REQUEST" || result.Status == "NOT_FOUND" {
			return 0, fmt.Errorf("distance matrix status %s: %w", result.Status, ErrTravelNotResolvable)
		}
		return 0, fmt.Errorf("distance matrix status: %s", result.Status)
	}

	if len(result.Rows) == 0 || len(result.Rows[0].Elements) == 0 {
		log.Printf("[maps] empty rows body=%s", string(body))
		return 0, fmt.Errorf("no route found: %w", ErrTravelNotResolvable)
	}

	el := result.Rows[0].Elements[0]
	if el.Status != "OK" {
		log.Printf("[maps] element status=%s body=%s", el.Status, string(body))
		// ZERO_RESULTS / NOT_FOUND: the address couldn't be geocoded or no route
		// exists — caller-data condition (4xx), not a server error.
		if el.Status == "ZERO_RESULTS" || el.Status == "NOT_FOUND" {
			return 0, fmt.Errorf("element status %s: %w", el.Status, ErrTravelNotResolvable)
		}
		return 0, fmt.Errorf("element status: %s", el.Status)
	}

	// Round up to nearest minute
	minutes := (el.Duration.Value + 59) / 60
	return minutes, nil
}
