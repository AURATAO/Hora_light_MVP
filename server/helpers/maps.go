package helpers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
)

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
		return 0, fmt.Errorf("distance matrix status: %s", result.Status)
	}

	if len(result.Rows) == 0 || len(result.Rows[0].Elements) == 0 {
		log.Printf("[maps] empty rows body=%s", string(body))
		return 0, fmt.Errorf("no route found")
	}

	el := result.Rows[0].Elements[0]
	if el.Status != "OK" {
		log.Printf("[maps] element status=%s body=%s", el.Status, string(body))
		return 0, fmt.Errorf("element status: %s", el.Status)
	}

	// Round up to nearest minute
	minutes := (el.Duration.Value + 59) / 60
	return minutes, nil
}
