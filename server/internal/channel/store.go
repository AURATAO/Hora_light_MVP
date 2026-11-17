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
	out, err := s.queryThread(ctx, "recent messages", `
		SELECT id::text, direction, COALESCE(body, ''), received_at, COALESCE(relay, false), provider_message_id, created_at
		FROM channel_messages
		WHERE channel = $1 AND address = $2
		ORDER BY received_at DESC
		LIMIT $3
	`, string(ch), address, limit)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// MessagesSince returns up to limit messages for the number written
// (created_at) after `after`, oldest written first — the incremental fetch
// behind Ops Chats' fast poll. The caller merges by ID; rows from
// concurrent transactions can commit slightly out of created_at order, so
// callers pass a cursor with some overlap rather than the exact last one.
func (s *PGStore) MessagesSince(ctx context.Context, ch Channel, address string, after time.Time, limit int) ([]ThreadMessage, error) {
	return s.queryThread(ctx, "messages since", `
		SELECT id::text, direction, COALESCE(body, ''), received_at, COALESCE(relay, false), provider_message_id, created_at
		FROM channel_messages
		WHERE channel = $1 AND address = $2 AND created_at > $3
		ORDER BY created_at ASC
		LIMIT $4
	`, string(ch), address, after, limit)
}

func (s *PGStore) queryThread(ctx context.Context, what, q string, args ...any) ([]ThreadMessage, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	var out []ThreadMessage
	for rows.Next() {
		var m ThreadMessage
		if err := rows.Scan(&m.ID, &m.Direction, &m.Text, &m.At, &m.Relay, &m.ProviderID, &m.Created); err != nil {
			if cerr := rows.Close(); cerr != nil {
				log.Printf("[channel] %s: close rows: %v", what, cerr)
			}
			return nil, fmt.Errorf("%s: scan: %w", what, err)
		}
		out = append(out, m)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("%s: close rows: %w", what, err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
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
func (s *PGStore) LastRelayed(ctx context.Context, ch Channel, address string, since time.Time, excludeProviderID string) (Relayed, error) {
	var rl Relayed
	var taskID sql.NullString
	err := s.DB.QueryRowContext(ctx, `
		SELECT m.body, m.task_id::text, m.received_at,
		       EXISTS (SELECT 1 FROM channel_messages i
		                WHERE i.channel = m.channel AND i.address = m.address
		                  AND i.direction = 'inbound' AND i.received_at > m.received_at
		                  AND i.provider_message_id <> $4) AS answered
		  FROM channel_messages m
		 WHERE m.channel = $1 AND m.address = $2 AND m.direction = 'outbound' AND m.relay AND m.received_at >= $3
		 ORDER BY m.received_at DESC LIMIT 1
	`, string(ch), address, since, excludeProviderID).Scan(&rl.Text, &taskID, &rl.At, &rl.Answered)
	if err == sql.ErrNoRows {
		return Relayed{}, nil
	}
	if err != nil {
		return Relayed{}, fmt.Errorf("last relayed: %w", err)
	}
	rl.TaskID, rl.OK = taskID.String, true
	return rl, nil
}
