package channel

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
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
		   reply_to_provider_id, relay, received_at, processed_at)
		VALUES ($1, $2, 'outbound', $3, $4, NULLIF($5, '')::uuid, NULLIF($6, ''), $7, now(), now())
		ON CONFLICT (channel, provider_message_id) DO NOTHING
	`, string(out.Channel), providerMessageID, out.To, out.RecordedText(), taskID, out.ReplyToProviderID, out.Relay)
	if err != nil {
		return fmt.Errorf("record outbound: %w", err)
	}
	return nil
}

// RecentMessages returns up to limit messages exchanged with the number,
// oldest first (ThreadReader, for ops handoffs).
func (s *PGStore) RecentMessages(ctx context.Context, ch Channel, address string, limit int) ([]ThreadMessage, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT direction, COALESCE(body, ''), received_at
		FROM channel_messages
		WHERE channel = $1 AND address = $2
		ORDER BY received_at DESC
		LIMIT $3
	`, string(ch), address, limit)
	if err != nil {
		return nil, fmt.Errorf("recent messages: %w", err)
	}
	var out []ThreadMessage
	for rows.Next() {
		var m ThreadMessage
		if err := rows.Scan(&m.Direction, &m.Text, &m.At); err != nil {
			if cerr := rows.Close(); cerr != nil {
				log.Printf("[channel] recent messages: close rows: %v", cerr)
			}
			return nil, fmt.Errorf("recent messages: scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("recent messages: close rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recent messages: %w", err)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// TaskForProviderMessage: the task one of our (or their) earlier messages
// was about, for inline replies. "" when unknown. (TaskLookup)
func (s *PGStore) TaskForProviderMessage(ctx context.Context, ch Channel, address, providerMessageID string) (string, bool, bool, error) {
	var taskID sql.NullString
	var relay bool
	// Scoped to the thread: a provider id from someone else's thread is
	// "unknown" here, whatever it names.
	err := s.DB.QueryRowContext(ctx, `
		SELECT task_id::text, relay FROM channel_messages
		WHERE channel = $1 AND address = $2 AND provider_message_id = $3
	`, string(ch), address, providerMessageID).Scan(&taskID, &relay)
	if err == sql.ErrNoRows {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, fmt.Errorf("task for message: %w", err)
	}
	return taskID.String, relay, true, nil
}

// SetMessageTask records which task an inbound message turned out to be
// about. (TaskLookup)
func (s *PGStore) SetMessageTask(ctx context.Context, ch Channel, providerMessageID, taskID string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE channel_messages SET task_id = $3::uuid
		WHERE channel = $1 AND provider_message_id = $2
	`, string(ch), providerMessageID, taskID)
	if err != nil {
		return fmt.Errorf("set message task: %w", err)
	}
	return nil
}

// LastRelayed: the most recent relayed supporter message on the thread
// since the given time. (TaskLookup)
func (s *PGStore) LastRelayed(ctx context.Context, ch Channel, address string, since time.Time) (string, string, time.Time, bool, error) {
	var body string
	var taskID sql.NullString
	var at time.Time
	err := s.DB.QueryRowContext(ctx, `
		SELECT body, task_id::text, received_at FROM channel_messages
		WHERE channel = $1 AND address = $2 AND direction = 'outbound' AND relay AND received_at >= $3
		ORDER BY received_at DESC LIMIT 1
	`, string(ch), address, since).Scan(&body, &taskID, &at)
	if err == sql.ErrNoRows {
		return "", "", time.Time{}, false, nil
	}
	if err != nil {
		return "", "", time.Time{}, false, fmt.Errorf("last relayed: %w", err)
	}
	return body, taskID.String, at, true, nil
}
