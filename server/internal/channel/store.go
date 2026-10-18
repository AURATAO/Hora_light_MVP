package channel

import (
	"context"
	"database/sql"
	"fmt"
)

// Store persists every message in channel_messages. Its first job is dedupe:
// both providers retry webhooks (Sendblue up to 3× on 5xx or a 45s timeout;
// Meta on any non-2xx), so the same message can arrive more than once and
// must be acted on exactly once.
type Store interface {
	// RecordInbound inserts the message keyed on (channel, provider_message_id).
	// isNew is false when that key already exists: a retry, to be ACKed and
	// otherwise ignored.
	RecordInbound(ctx context.Context, in Inbound) (isNew bool, err error)
	// MarkProcessed stamps processed_at once the router has handled it. Rows
	// left unstamped after a crash are what a recovery sweep re-routes.
	MarkProcessed(ctx context.Context, ch Channel, providerMessageID string) error
	// RecordOutbound stores a sent message with the task it belongs to, so a
	// later inline reply to it resolves to that task (router step 5).
	RecordOutbound(ctx context.Context, out Outbound, providerMessageID, taskID string) error
}

// PGStore is the Postgres Store. The Go backend is the only writer (CLAUDE.md
// Rule 1); the table is RLS deny-all with no client grants.
type PGStore struct{ DB *sql.DB }

func (s *PGStore) RecordInbound(ctx context.Context, in Inbound) (bool, error) {
	if !in.Channel.Valid() || in.ProviderMessageID == "" {
		return false, fmt.Errorf("record inbound: invalid channel or empty provider id")
	}
	var id string
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO channel_messages
		  (channel, provider_message_id, direction, address, body, service,
		   reply_to_provider_id, received_at)
		VALUES ($1, $2, 'inbound', $3, $4, $5, NULLIF($6, ''), $7)
		ON CONFLICT (channel, provider_message_id) DO NOTHING
		RETURNING id
	`, string(in.Channel), in.ProviderMessageID, in.From, in.Text, in.Service,
		in.ReplyToProviderID, in.ReceivedAt).Scan(&id)
	if err == sql.ErrNoRows {
		return false, nil // duplicate delivery
	}
	if err != nil {
		return false, fmt.Errorf("record inbound: %w", err)
	}
	return true, nil
}

func (s *PGStore) MarkProcessed(ctx context.Context, ch Channel, providerMessageID string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE channel_messages SET processed_at = now()
		WHERE channel = $1 AND provider_message_id = $2 AND processed_at IS NULL
	`, string(ch), providerMessageID)
	if err != nil {
		return fmt.Errorf("mark processed: %w", err)
	}
	return nil
}

func (s *PGStore) RecordOutbound(ctx context.Context, out Outbound, providerMessageID, taskID string) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO channel_messages
		  (channel, provider_message_id, direction, address, body, task_id,
		   reply_to_provider_id, received_at, processed_at)
		VALUES ($1, $2, 'outbound', $3, $4, NULLIF($5, '')::uuid, NULLIF($6, ''), now(), now())
		ON CONFLICT (channel, provider_message_id) DO NOTHING
	`, string(out.Channel), providerMessageID, out.To, out.Text, taskID, out.ReplyToProviderID)
	if err != nil {
		return fmt.Errorf("record outbound: %w", err)
	}
	return nil
}
