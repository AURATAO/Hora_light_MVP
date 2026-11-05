package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// PGStore's DiscoveryStore (channel_discovery, one row per identity).

func (s *PGStore) GetDiscovery(ctx context.Context, identityID string) (*DiscoveryOffer, error) {
	var raw []byte
	err := s.DB.QueryRowContext(ctx, `SELECT offer FROM channel_discovery WHERE identity_id = $1`, identityID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && raw == nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get discovery: %w", err)
	}
	var o DiscoveryOffer
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("get discovery: decode: %w", err)
	}
	return &o, nil
}

func (s *PGStore) SaveDiscovery(ctx context.Context, identityID string, o *DiscoveryOffer) error {
	var offer any
	if o != nil {
		b, err := json.Marshal(o)
		if err != nil {
			return fmt.Errorf("save discovery: marshal: %w", err)
		}
		offer = string(b)
	}
	if _, err := s.DB.ExecContext(ctx, `
		INSERT INTO channel_discovery (identity_id, offer, updated_at) VALUES ($1, $2::jsonb, now())
		ON CONFLICT (identity_id) DO UPDATE SET offer = EXCLUDED.offer, updated_at = now()`, identityID, offer); err != nil {
		return fmt.Errorf("save discovery: %w", err)
	}
	return nil
}

// TakePlacesCall counts one call atomically: a new day starts at 1, and a
// call over perDay is not counted (nor made).
func (s *PGStore) TakePlacesCall(ctx context.Context, identityID, day string, perDay int) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO channel_discovery (identity_id, places_day, places_calls, updated_at) VALUES ($1, $2::date, 1, now())
		ON CONFLICT (identity_id) DO UPDATE SET
		  places_calls = CASE WHEN channel_discovery.places_day IS DISTINCT FROM EXCLUDED.places_day THEN 1
		                      ELSE channel_discovery.places_calls + 1 END,
		  places_day = EXCLUDED.places_day, updated_at = now()
		WHERE channel_discovery.places_day IS DISTINCT FROM EXCLUDED.places_day OR channel_discovery.places_calls < $3
		RETURNING places_calls`, identityID, day, perDay).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // the WHERE refused it: at the cap
	}
	if err != nil {
		return false, fmt.Errorf("take places call: %w", err)
	}
	return n <= perDay, nil
}
