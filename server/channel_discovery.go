package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"

	"hora-auth/helpers"
	"hora-auth/internal/channel"
	"hora-auth/internal/channel/llm"
)

// Place discovery for the text channel (D-23, internal/channel/discovery.go).
// Off unless CHANNEL_DISCOVERY=1. The model is the agent step's
// (LLM_AGENT_MODEL) and must be Anthropic: GLM cannot run the tool loop, so
// with a Baseten agent step discovery questions get fixed copy instead.
//
//	CHANNEL_DISCOVERY=1             turn it on
//	GOOGLE_MAPS_API_KEY             the existing key; needs "Places API (New)" enabled
//	CHANNEL_PLACES_PER_MESSAGE      Places searches per message (default 3)
//	CHANNEL_PLACES_PER_DAY          Places searches per person per NY day (default 25)
//	CHANNEL_DISCOVERY_TIMEOUT       one discovery turn (default 30s; the message has 60s)
func configureDiscovery(router *channel.Router, sqlDB *sql.DB, store *channel.PGStore) {
	if os.Getenv("CHANNEL_DISCOVERY") != "1" {
		return
	}
	spec, err := llm.ParseSpec(firstNonEmptyEnv("LLM_AGENT_MODEL", "anthropic:"+llm.DefaultAnthropicModel))
	if err != nil {
		log.Printf("[channel] WARNING: discovery: %v", err)
	}
	places := placesSearcher{}
	router.Places = places
	router.DiscoveryStore = store
	router.History = &chatHistory{sqlDB: sqlDB}
	router.DiscoveryLimits = channel.DiscoveryLimitsFromEnv()
	d, err := llm.ProvidersFromEnv().Discovery(spec, places)
	if err != nil {
		// On, but it cannot run: every discovery question gets CopySearchDown.
		log.Printf("[channel] WARNING: discovery on but unavailable (%s): %v — fixed copy only", spec, err)
		router.Discovery = unavailableDiscovery{}
		return
	}
	router.Discovery = d
	l := router.DiscoveryLimits
	log.Printf("[channel] place discovery ON (D-23): model=%s places/message=%d places/day=%d timeout=%s", d.Model.Name(), l.PerMessage, l.PerDay, l.Timeout)
}

func firstNonEmptyEnv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// unavailableDiscovery fails every turn: the router answers a discovery
// question with CopySearchDown ("tell me the store or address").
type unavailableDiscovery struct{}

func (unavailableDiscovery) Discover(context.Context, llm.DiscoveryRequest) (llm.DiscoveryResult, error) {
	return llm.DiscoveryResult{Usual: -1}, llm.ErrDiscoveryUnavailable
}

// placesSearcher is Google Places (New) behind the server key.
type placesSearcher struct{}

func (placesSearcher) SearchPlaces(ctx context.Context, query string, openNow bool) ([]llm.Place, error) {
	ps, err := helpers.PlacesSearch(ctx, query, openNow, 5, nycLocation)
	if err != nil {
		return nil, err
	}
	out := make([]llm.Place, len(ps))
	for i, p := range ps {
		out[i] = llmPlace(p)
	}
	return out, nil
}

func (placesSearcher) PlaceDetails(ctx context.Context, id string) (llm.Place, error) {
	p, err := helpers.PlaceDetails(ctx, id, nycLocation)
	if err != nil {
		return llm.Place{}, err
	}
	return llmPlace(p), nil
}

func llmPlace(p helpers.Place) llm.Place {
	return llm.Place{ID: p.ID, Name: p.Name, Address: p.Address, FullAddress: p.FullAddress, Rating: p.Rating,
		RatingCount: p.RatingCount, OpenNow: p.OpenNow, ClosesAt: p.ClosesAt, OpensAt: p.OpensAt,
		InNYC: p.Area == helpers.AreaInside}
}

// chatHistory reads a requester's own past errands for "the usual": posted
// or completed tasks only (never cancelled or removed), newest first.
type chatHistory struct{ sqlDB *sql.DB }

func (h *chatHistory) PastTasks(ctx context.Context, userID string, n int) ([]channel.PastTask, error) {
	rows, err := h.sqlDB.QueryContext(ctx, `
		SELECT COALESCE(title, ''), COALESCE(category, ''), COALESCE(description, ''),
		       COALESCE(location_text, ''), COALESCE(estimated_minutes, 0)
		  FROM public.tasks
		 WHERE requester_id = $1::uuid AND status IN ('open', 'completed')
		 ORDER BY created_at DESC
		 LIMIT $2`, userID, n)
	if err != nil {
		return nil, fmt.Errorf("past tasks: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			log.Printf("[channel] past tasks: close rows: %v", cerr)
		}
	}()
	var out []channel.PastTask
	for rows.Next() {
		var t channel.PastTask
		var loc string
		if err := rows.Scan(&t.Title, &t.Category, &t.Description, &loc, &t.Minutes); err != nil {
			return nil, fmt.Errorf("past tasks: %w", err)
		}
		if stops := taskStops(loc); len(stops) > 0 {
			t.Location1 = stops[0]
			if len(stops) > 1 {
				t.Location2 = stops[len(stops)-1]
			}
		}
		if t.Title != "" {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}
