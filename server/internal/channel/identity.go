package channel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"
)

// Identity is one phone number on one channel, and the HO:RA user it belongs
// to once the person has completed setup from a link sent to that number.
type Identity struct {
	ID      string
	Channel Channel
	Address string // E.164
	// UserID is empty until setup links the number to an account.
	UserID string
}

// Linked reports whether the number belongs to an account yet.
func (i Identity) Linked() bool { return i.UserID != "" }

// IdentityStore is the identity and setup-link half of the channel tables.
type IdentityStore interface {
	// UpsertIdentity returns the identity for (channel, address), creating it
	// on first contact.
	UpsertIdentity(ctx context.Context, ch Channel, address string) (Identity, error)
	// AttachMessage records which identity sent an inbound message.
	AttachMessage(ctx context.Context, ch Channel, providerMessageID, identityID string) error
	// TouchConversation stamps the person's latest inbound message
	// (WhatsApp's 24-hour window is measured from it).
	TouchConversation(ctx context.Context, identityID string, at time.Time) error
	// SetupTokenIssuedSince reports whether a setup link was sent to this
	// identity after `since` — so a person texting three times in a row gets
	// one link, not three.
	SetupTokenIssuedSince(ctx context.Context, identityID string, since time.Time) (bool, error)
	// CreateSetupToken stores the hash of a new setup token.
	CreateSetupToken(ctx context.Context, identityID, tokenHash string, expiresAt time.Time) error
	// PeekSetupToken resolves a token without using it up, for the setup
	// page's first screen. ErrTokenInvalid when unknown, used or expired.
	PeekSetupToken(ctx context.Context, tokenHash string) (Identity, time.Time, error)
	// ConsumeSetupTokenAndLink uses the token once and links its identity to
	// userID, in one transaction. ErrTokenInvalid if the token is unknown,
	// used or expired; ErrIdentityTaken if the number already belongs to a
	// different account.
	ConsumeSetupTokenAndLink(ctx context.Context, tokenHash, userID string) (Identity, error)
}

var (
	ErrTokenInvalid  = errors.New("channel: setup link is invalid, used or expired")
	ErrIdentityTaken = errors.New("channel: this number is already linked to another account")
)

// SetupTokenTTL is how long a setup link works.
const SetupTokenTTL = 30 * time.Minute

// NewSetupToken returns a fresh token for the link and the hash to store.
// 32 random bytes: unguessable, so the link alone proves the person can read
// messages sent to this number.
func NewSetupToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("setup token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashSetupToken(token), nil
}

// HashSetupToken is what is stored and looked up; the raw token never is.
func HashSetupToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ── Postgres ───────────────────────────────────────────────────────────────

func (s *PGStore) UpsertIdentity(ctx context.Context, ch Channel, address string) (Identity, error) {
	if !ch.Valid() {
		return Identity{}, fmt.Errorf("upsert identity: invalid channel")
	}
	var id Identity
	var userID sql.NullString
	// DO UPDATE (a no-op write) rather than DO NOTHING so RETURNING yields
	// the existing row too.
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO channel_identities (channel, address)
		VALUES ($1, $2)
		ON CONFLICT (channel, address) DO UPDATE SET address = EXCLUDED.address
		RETURNING id, channel, address, user_id
	`, string(ch), address).Scan(&id.ID, &id.Channel, &id.Address, &userID)
	if err != nil {
		return Identity{}, fmt.Errorf("upsert identity: %w", err)
	}
	id.UserID = userID.String
	return id, nil
}

func (s *PGStore) AttachMessage(ctx context.Context, ch Channel, providerMessageID, identityID string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE channel_messages SET identity_id = $3
		WHERE channel = $1 AND provider_message_id = $2
	`, string(ch), providerMessageID, identityID)
	if err != nil {
		return fmt.Errorf("attach message: %w", err)
	}
	return nil
}

func (s *PGStore) TouchConversation(ctx context.Context, identityID string, at time.Time) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO channel_conversations (identity_id, last_inbound_at, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (identity_id) DO UPDATE
		  SET last_inbound_at = GREATEST(channel_conversations.last_inbound_at, EXCLUDED.last_inbound_at),
		      updated_at = now()
	`, identityID, at)
	if err != nil {
		return fmt.Errorf("touch conversation: %w", err)
	}
	return nil
}

func (s *PGStore) SetupTokenIssuedSince(ctx context.Context, identityID string, since time.Time) (bool, error) {
	var exists bool
	err := s.DB.QueryRowContext(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM channel_setup_tokens
		  WHERE identity_id = $1 AND created_at > $2 AND used_at IS NULL
		)
	`, identityID, since).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("setup token issued since: %w", err)
	}
	return exists, nil
}

func (s *PGStore) CreateSetupToken(ctx context.Context, identityID, tokenHash string, expiresAt time.Time) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO channel_setup_tokens (identity_id, token_hash, expires_at)
		VALUES ($1, $2, $3)
	`, identityID, tokenHash, expiresAt)
	if err != nil {
		return fmt.Errorf("create setup token: %w", err)
	}
	return nil
}

func (s *PGStore) PeekSetupToken(ctx context.Context, tokenHash string) (Identity, time.Time, error) {
	var id Identity
	var userID sql.NullString
	var expires time.Time
	err := s.DB.QueryRowContext(ctx, `
		SELECT i.id, i.channel, i.address, i.user_id, t.expires_at
		FROM channel_setup_tokens t
		JOIN channel_identities i ON i.id = t.identity_id
		WHERE t.token_hash = $1 AND t.used_at IS NULL AND t.expires_at > now()
	`, tokenHash).Scan(&id.ID, &id.Channel, &id.Address, &userID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, time.Time{}, ErrTokenInvalid
	}
	if err != nil {
		return Identity{}, time.Time{}, fmt.Errorf("peek setup token: %w", err)
	}
	id.UserID = userID.String
	return id, expires, nil
}

func (s *PGStore) ConsumeSetupTokenAndLink(ctx context.Context, tokenHash, userID string) (Identity, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, fmt.Errorf("link: begin: %w", err)
	}
	defer func() {
		// After Commit, Rollback returns sql.ErrTxDone; anything else is real.
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			log.Printf("[channel] link: rollback: %v", err)
		}
	}()

	// Use the token up first. The WHERE makes this the single gate: a second
	// tab, a replayed request or an expired link all find no row.
	var identityID string
	err = tx.QueryRowContext(ctx, `
		UPDATE channel_setup_tokens SET used_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING identity_id
	`, tokenHash).Scan(&identityID)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, ErrTokenInvalid
	}
	if err != nil {
		return Identity{}, fmt.Errorf("link: consume token: %w", err)
	}

	// Link, unless the number already belongs to someone else. Re-linking to
	// the same account is fine (person redoing setup).
	var id Identity
	var linked sql.NullString
	err = tx.QueryRowContext(ctx, `
		UPDATE channel_identities
		SET user_id = $2, verified_at = COALESCE(verified_at, now())
		WHERE id = $1 AND (user_id IS NULL OR user_id = $2)
		RETURNING id, channel, address, user_id
	`, identityID, userID).Scan(&id.ID, &id.Channel, &id.Address, &linked)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, ErrIdentityTaken
	}
	if err != nil {
		return Identity{}, fmt.Errorf("link: identity: %w", err)
	}
	id.UserID = linked.String

	// Older unused links for this number are dead now.
	if _, err := tx.ExecContext(ctx, `
		UPDATE channel_setup_tokens SET used_at = now()
		WHERE identity_id = $1 AND used_at IS NULL
	`, identityID); err != nil {
		return Identity{}, fmt.Errorf("link: retire tokens: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Identity{}, fmt.Errorf("link: commit: %w", err)
	}
	return id, nil
}

// IdentityByID loads one identity (ops routes). ErrIdentityNotFound when
// there is none.
func (s *PGStore) IdentityByID(ctx context.Context, identityID string) (Identity, error) {
	var id Identity
	var user sql.NullString
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, channel, address, user_id FROM channel_identities WHERE id = $1::uuid
	`, identityID).Scan(&id.ID, &id.Channel, &id.Address, &user)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, ErrIdentityNotFound
	}
	if err != nil {
		return Identity{}, fmt.Errorf("identity by id: %w", err)
	}
	id.UserID = user.String
	return id, nil
}

// HandedOff lists the conversations ops currently hold, oldest first.
func (s *PGStore) HandedOff(ctx context.Context) ([]HandoffRow, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT i.id, i.channel, i.address, COALESCE(i.user_id::text, ''), c.updated_at, c.last_inbound_at
		FROM channel_conversations c JOIN channel_identities i ON i.id = c.identity_id
		WHERE c.state = $1 ORDER BY c.updated_at ASC
	`, string(StateHandoff))
	if err != nil {
		return nil, fmt.Errorf("handed off: %w", err)
	}
	var out []HandoffRow
	for rows.Next() {
		var h HandoffRow
		var last sql.NullTime
		if err := rows.Scan(&h.IdentityID, &h.Channel, &h.address, &h.UserID, &h.Since, &last); err != nil {
			if cerr := rows.Close(); cerr != nil {
				log.Printf("[channel] handed off: close rows: %v", cerr)
			}
			return nil, fmt.Errorf("handed off: scan: %w", err)
		}
		h.PhoneHint = PhoneHint(h.address)
		if last.Valid {
			t := last.Time
			h.LastInboundAt = &t
		}
		out = append(out, h)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("handed off: close rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("handed off: %w", err)
	}
	return out, nil
}

// HandoffRow is one held conversation as ops see it: no full number (S-12).
type HandoffRow struct {
	IdentityID    string     `json:"identity_id"`
	Channel       Channel    `json:"channel"`
	PhoneHint     string     `json:"phone_hint"`
	UserID        string     `json:"user_id,omitempty"`
	Since         time.Time  `json:"since"`
	LastInboundAt *time.Time `json:"last_inbound_at,omitempty"`
	address       string
}

var ErrIdentityNotFound = errors.New("channel: identity not found")

// ConversationRow is one conversation as the ops Chats page lists it: who
// (by last four digits and, when linked, the account's display name), where
// the bot has it, and when either side last spoke. No full number, no
// message text (S-12) — the thread endpoint carries text, one chat at a time.
type ConversationRow struct {
	IdentityID        string     `json:"identity_id"`
	Channel           Channel    `json:"channel"`
	PhoneHint         string     `json:"phone_hint"`
	UserID            string     `json:"user_id,omitempty"`
	UserName          string     `json:"user_name,omitempty"`
	State             string     `json:"state"`
	UpdatedAt         time.Time  `json:"updated_at"`
	LastInboundAt     *time.Time `json:"last_inbound_at,omitempty"`
	LastOutboundAt    *time.Time `json:"last_outbound_at,omitempty"`
	HandoffActivityAt *time.Time `json:"handoff_activity_at,omitempty"`
}

// Conversations lists the most recently active conversations, newest first.
// Hand-offs are not pulled to the top here — HandedOff is the queue; this is
// the ledger — the page orders them.
func (s *PGStore) Conversations(ctx context.Context, limit int) ([]ConversationRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT i.id, i.channel, i.address, COALESCE(i.user_id::text, ''),
		       COALESCE(u.name, ''), c.state, c.updated_at,
		       c.last_inbound_at, c.last_outbound_at, c.handoff_activity_at
		FROM channel_conversations c
		JOIN channel_identities i ON i.id = c.identity_id
		LEFT JOIN users u ON u.id = i.user_id
		ORDER BY c.updated_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("conversations: %w", err)
	}
	var out []ConversationRow
	for rows.Next() {
		var r ConversationRow
		var address string
		var in, outAt, act sql.NullTime
		if err := rows.Scan(&r.IdentityID, &r.Channel, &address, &r.UserID, &r.UserName,
			&r.State, &r.UpdatedAt, &in, &outAt, &act); err != nil {
			if cerr := rows.Close(); cerr != nil {
				log.Printf("[channel] conversations: close rows: %v", cerr)
			}
			return nil, fmt.Errorf("conversations: scan: %w", err)
		}
		r.PhoneHint = PhoneHint(address)
		if in.Valid {
			t := in.Time
			r.LastInboundAt = &t
		}
		if outAt.Valid {
			t := outAt.Time
			r.LastOutboundAt = &t
		}
		if act.Valid {
			t := act.Time
			r.HandoffActivityAt = &t
		}
		out = append(out, r)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("conversations: close rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("conversations: %w", err)
	}
	return out, nil
}
