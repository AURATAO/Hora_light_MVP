package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"hora-auth/internal/channel/llm"
)

// State is where a conversation is (channel_conversations.state).
type State string

const (
	StateIdle            State = "idle"
	StateAwaitingName    State = "awaiting_name"
	StateAwaitingConfirm State = "awaiting_confirm"
	StateAwaitingSetup   State = "awaiting_setup"
	StateHandoff         State = "handoff"
	// StatePosting means a booking is in flight (stored as 'drafting'). Only
	// one caller can move a conversation into it — see ClaimForPosting.
	StatePosting State = "drafting"
)

// PendingDraft is a task waiting for the person's YES, with the text it was
// parsed from (so a follow-up like "make it 4pm" can be re-parsed with
// context) and the estimate they were shown.
type PendingDraft struct {
	Draft         llm.TaskDraft `json:"draft"`
	Source        string        `json:"source"`
	QuoteCents    int           `json:"quote_cents"`
	QuoteMinutes  int           `json:"quote_minutes"`
	SummaryShown  string        `json:"summary_shown"`
	CreatedAtUnix int64         `json:"created_at"`
}

// Conversation is one identity's chat state.
type Conversation struct {
	IdentityID      string
	State           State
	Pending         *PendingDraft
	PendingName     string
	TermsAcceptedAt *time.Time
}

// ConversationStore loads and saves conversation state.
type ConversationStore interface {
	GetConversation(ctx context.Context, identityID string) (Conversation, error)
	SaveConversation(ctx context.Context, c Conversation) error
	// ConversationsForUser returns the conversations of every number linked
	// to userID (normally one), for the setup page's "finish booking" step.
	ConversationsForUser(ctx context.Context, userID string) ([]Conversation, []Identity, error)
	// ClaimForPosting atomically moves a conversation from `from` to
	// StatePosting and returns its draft. ok=false when another message or
	// a second tap already claimed it (or there is nothing to post): the
	// caller must not post. This is the double-booking guard — two quick
	// "YES"es, or the page's finish step racing a YES, book once.
	ClaimForPosting(ctx context.Context, identityID string, from State) (*PendingDraft, bool, error)
	// AcceptTerms records the consent a YES carries and touches NOTHING
	// else. It must not be a full save: a YES that loaded the conversation
	// before a parallel YES finished booking would otherwise write the old
	// state and draft back over the finished one and book it twice.
	AcceptTerms(ctx context.Context, identityID string, at time.Time) error
}

func (s *PGStore) AcceptTerms(ctx context.Context, identityID string, at time.Time) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO channel_conversations (identity_id, terms_accepted_at, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (identity_id) DO UPDATE
		  SET terms_accepted_at = COALESCE(channel_conversations.terms_accepted_at, EXCLUDED.terms_accepted_at)
	`, identityID, at)
	if err != nil {
		return fmt.Errorf("accept terms: %w", err)
	}
	return nil
}

func (s *PGStore) ClaimForPosting(ctx context.Context, identityID string, from State) (*PendingDraft, bool, error) {
	var pending []byte
	err := s.DB.QueryRowContext(ctx, `
		UPDATE channel_conversations SET state = $3, updated_at = now()
		WHERE identity_id = $1 AND pending_draft IS NOT NULL
		  AND (state = $2
		       -- A claim left behind by a crash mid-post. Routing runs under
		       -- a 60 s timeout, so a live claim never gets this old.
		       OR (state = $3 AND updated_at < now() - interval '3 minutes'))
		RETURNING pending_draft
	`, identityID, string(from), string(StatePosting)).Scan(&pending)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("claim for posting: %w", err)
	}
	var p PendingDraft
	if err := json.Unmarshal(pending, &p); err != nil {
		return nil, false, fmt.Errorf("claim for posting: decode: %w", err)
	}
	return &p, true, nil
}

func (s *PGStore) GetConversation(ctx context.Context, identityID string) (Conversation, error) {
	c := Conversation{IdentityID: identityID, State: StateIdle}
	var state string
	var pending []byte
	var name sql.NullString
	var terms sql.NullTime
	err := s.DB.QueryRowContext(ctx, `
		SELECT state, pending_draft, pending_name, terms_accepted_at
		FROM channel_conversations WHERE identity_id = $1
	`, identityID).Scan(&state, &pending, &name, &terms)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("get conversation: %w", err)
	}
	c.State = State(state)
	c.PendingName = name.String
	if terms.Valid {
		t := terms.Time
		c.TermsAcceptedAt = &t
	}
	if len(pending) > 0 && string(pending) != "null" {
		var p PendingDraft
		if err := json.Unmarshal(pending, &p); err == nil {
			c.Pending = &p
		}
	}
	return c, nil
}

func (s *PGStore) SaveConversation(ctx context.Context, c Conversation) error {
	var pending any
	if c.Pending != nil {
		b, err := json.Marshal(c.Pending)
		if err != nil {
			return fmt.Errorf("save conversation: marshal: %w", err)
		}
		pending = string(b)
	}
	var terms any
	if c.TermsAcceptedAt != nil {
		terms = *c.TermsAcceptedAt
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO channel_conversations
		  (identity_id, state, pending_draft, pending_name, terms_accepted_at, updated_at)
		VALUES ($1, $2, $3::jsonb, NULLIF($4, ''), $5, now())
		ON CONFLICT (identity_id) DO UPDATE SET
		  state = EXCLUDED.state,
		  pending_draft = EXCLUDED.pending_draft,
		  pending_name = EXCLUDED.pending_name,
		  terms_accepted_at = COALESCE(channel_conversations.terms_accepted_at, EXCLUDED.terms_accepted_at),
		  updated_at = now()
	`, c.IdentityID, string(c.State), pending, c.PendingName, terms)
	if err != nil {
		return fmt.Errorf("save conversation: %w", err)
	}
	return nil
}

func (s *PGStore) ConversationsForUser(ctx context.Context, userID string) ([]Conversation, []Identity, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT i.id, i.channel, i.address FROM channel_identities i
		WHERE i.user_id = $1 ORDER BY i.created_at
	`, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("conversations for user: %w", err)
	}
	var ids []Identity
	for rows.Next() {
		var id Identity
		if err := rows.Scan(&id.ID, &id.Channel, &id.Address); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("conversations for user: scan: %w", err)
		}
		id.UserID = userID
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("conversations for user: %w", err)
	}
	convs := make([]Conversation, 0, len(ids))
	for _, id := range ids {
		c, err := s.GetConversation(ctx, id.ID)
		if err != nil {
			return nil, nil, err
		}
		convs = append(convs, c)
	}
	return convs, ids, nil
}

// ── Replies the router must read deterministically ────────────────────────
//
// Money moves only on an explicit YES (PLAN §6). These are matched by rule,
// never by the model: anything not clearly yes is not yes.

var (
	yesRE    = regexp.MustCompile(`(?i)^\s*(yes|y|yep|yeah|yup|yes please|ok|okay|confirm|confirmed|book it|book|do it|go ahead|sure|👍|✅|是|好|好的|確認|sí|si)[\s!.]*$`)
	noRE     = regexp.MustCompile(`(?i)^\s*(no|n|nope|nah|cancel|stop|never ?mind|don'?t|不要|取消|不用)[\s!.]*$`)
	nameBadR = regexp.MustCompile(`[0-9@/:]`)
)

// IsYes reports an explicit confirmation.
func IsYes(text string) bool { return yesRE.MatchString(text) }

// IsNo reports an explicit refusal or cancel.
func IsNo(text string) bool { return noRE.MatchString(text) }

// CleanName turns a reply to "what's your first name?" into a name, or ""
// when the reply does not look like one (a sentence, a number, a link).
func CleanName(text string) string {
	t := strings.TrimSpace(text)
	for _, p := range []string{"my name is ", "i'm ", "im ", "i am ", "it's ", "its ", "call me "} {
		if strings.HasPrefix(strings.ToLower(t), p) {
			t = strings.TrimSpace(t[len(p):])
			break
		}
	}
	t = strings.Trim(t, " .!,")
	if t == "" || len([]rune(t)) > 40 || nameBadR.MatchString(t) || len(strings.Fields(t)) > 3 {
		return ""
	}
	return t
}
