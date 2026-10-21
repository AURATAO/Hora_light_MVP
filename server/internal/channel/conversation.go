package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	// StateAwaitingApproval: the supporter asked for more time or budget and
	// the next YES/NO answers that (M3). A pending draft is kept underneath
	// and resumed afterwards.
	StateAwaitingApproval State = "awaiting_approval"
	// StateAwaitingBudget: an errand involves buying something and the chat
	// is asking how much to set aside (M3.5).
	StateAwaitingBudget State = "awaiting_budget"
	// StateAwaitingTaskChoice: several supporters could be meant; the next
	// message picks one by number (M3.2).
	StateAwaitingTaskChoice State = "awaiting_task_choice"
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
	// More holds the second and later errands of a message that asked for
	// several (M3). The first stays in Draft/QuoteCents/QuoteMinutes so older
	// stored rows still decode. All of them wait on the one YES.
	More []DraftItem `json:"more,omitempty"`
	// Unparsed is the part of the message no draft could be made from, kept
	// so the summary can say so instead of silently dropping it.
	Unparsed []string `json:"unparsed,omitempty"`
	// NoAutoExtend: the person turned off the up-to-15-minutes-over consent
	// the summary states (M3.5). Default (false) = consent, as the app's
	// checkbox; it is sent explicitly at posting either way.
	NoAutoExtend bool `json:"no_auto_extend,omitempty"`
	// BudgetAskIndex is the item the budget question is about (-1 = none).
	BudgetAskIndex int `json:"budget_ask_index,omitempty"`
	// NameAsked: the draft is waiting on the first name (awaiting_name), so
	// an interruption (an approval) can resume there.
	NameAsked bool `json:"name_asked,omitempty"`
	// WhileActive is the supporter's first name when the draft was started
	// while that supporter was on the job (D-17 P6): the summary says so
	// and "for <name>" relays the words instead.
	WhileActive      string `json:"while_active,omitempty"`
	WhileActiveShown bool   `json:"while_active_shown,omitempty"`
	// First-item budget fields; see DraftItem. Kept flat for stored rows.
	BudgetCents int  `json:"budget_cents,omitempty"`
	NeedsBudget bool `json:"needs_budget,omitempty"`
	BudgetAsked bool `json:"budget_asked,omitempty"`
}

// DraftItem is one errand with its estimate.
type DraftItem struct {
	Draft        llm.TaskDraft `json:"draft"`
	QuoteCents   int           `json:"quote_cents"`
	QuoteMinutes int           `json:"quote_minutes"`
	// BudgetCents is the shopping ceiling the person agreed to (0 = none);
	// NeedsBudget = the errand involves buying; BudgetAsked = the question
	// was answered (or the amount came with the message).
	BudgetCents int  `json:"budget_cents,omitempty"`
	NeedsBudget bool `json:"needs_budget,omitempty"`
	BudgetAsked bool `json:"budget_asked,omitempty"`
}

// Items is every errand in order, the first included.
func (p *PendingDraft) Items() []DraftItem {
	items := make([]DraftItem, 0, 1+len(p.More))
	items = append(items, DraftItem{Draft: p.Draft, QuoteCents: p.QuoteCents, QuoteMinutes: p.QuoteMinutes,
		BudgetCents: p.BudgetCents, NeedsBudget: p.NeedsBudget, BudgetAsked: p.BudgetAsked})
	return append(items, p.More...)
}

// SetItems stores items (at least one) in the same shape.
func (p *PendingDraft) SetItems(items []DraftItem) {
	p.Draft, p.QuoteCents, p.QuoteMinutes = items[0].Draft, items[0].QuoteCents, items[0].QuoteMinutes
	p.BudgetCents, p.NeedsBudget, p.BudgetAsked = items[0].BudgetCents, items[0].NeedsBudget, items[0].BudgetAsked
	p.More = append([]DraftItem(nil), items[1:]...)
}

// TotalCents is the sum of the estimates.
func (p *PendingDraft) TotalCents() int {
	total := 0
	for _, it := range p.Items() {
		total += it.QuoteCents
	}
	return total
}

// Headline names the booking in a few words: the title, or "3 errands".
func (p *PendingDraft) Headline() string {
	if n := 1 + len(p.More); n > 1 {
		return fmt.Sprintf("%d errands", n)
	}
	return p.Draft.Title
}

// Conversation is one identity's chat state.
type Conversation struct {
	IdentityID      string
	State           State
	Pending         *PendingDraft
	PendingName     string
	TermsAcceptedAt *time.Time
	// PendingExtensionID is the extension request the next YES/NO answers
	// (StateAwaitingApproval). ApprovalArmedAt is when that question was
	// asked: a message sent before it cannot be its answer (a second tap on
	// YES for the booking must not approve the supporter's request).
	PendingExtensionID string
	ApprovalArmedAt    *time.Time
	// LastInboundAt is the person's latest message (read-only here; stamped
	// by TouchConversation). WhatsApp's 24h window is measured from it.
	LastInboundAt *time.Time
	// ApprovalClaimedAt is when the armed question was claimed (written by
	// ClaimHold only — nothing else touches it, so an inbound message
	// cannot refresh it). A claim whose delivery was never confirmed is
	// re-sent only once it is older than UnconfirmedResend — a send in
	// flight must not be repeated by a resume point running beside it.
	ApprovalClaimedAt *time.Time
	// PendingTaskChoices are the task ids listed when asking which supporter
	// a message is for; PendingRelayText is that message, relayed on pick.
	PendingTaskChoices []string
	PendingRelayText   string
	// Deferred: approval questions held until the thread can take them
	// (D-17 I-O), oldest first. Read with the row; written ONLY through
	// HoldApproval/RemoveHold — a full-row save never touches them, so a booking's own
	// save cannot wipe a question that arrived while it was in flight.
	Deferred []DeferredApproval
}

// ResumeState is where a conversation goes once an approval is answered:
// back to the draft that was waiting, or idle.
func (c Conversation) ResumeState() State {
	if c.Pending != nil {
		switch {
		case c.Pending.NameAsked:
			return StateAwaitingName
		case c.Pending.BudgetAskIndex >= 0:
			return StateAwaitingBudget
		}
		return StateAwaitingConfirm
	}
	if len(c.PendingTaskChoices) > 0 {
		return StateAwaitingTaskChoice
	}
	return StateIdle
}

// DeferredApproval is an approval question that arrived while the thread
// could not take it. Router.resumeDeferred recomposes it from the
// structured request with the time still left, sends and arms it once the
// state allows — if the request is still the person's to answer.
type DeferredApproval struct {
	UserID        string          `json:"user_id"`
	TaskID        string          `json:"task_id"`
	TaskTitle     string          `json:"task_title"`
	SupporterName string          `json:"supporter_name"`
	ExtensionID   string          `json:"extension_id"`
	Type          string          `json:"type"` // the notification type (time / budget request)
	Approval      PendingApproval `json:"approval"`
	// TimeoutMinutes is the auto-decline window the app applies; AtUnix is
	// when the request was announced. What is left decides whether and how
	// the question is asked.
	TimeoutMinutes int   `json:"timeout_minutes"`
	AtUnix         int64 `json:"at_unix"`
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
	// HoldApproval appends a held approval question, and RemoveHold drops
	// one by request id — each narrowly and atomically in the store, like
	// AcceptTerms: nothing else on the row is written, and no caller holds
	// the list between a read and a write (D-17 I-O; a full save must never
	// wipe a hold made while a booking was in flight). Holding the same
	// request twice is a no-op.
	HoldApproval(ctx context.Context, identityID string, d DeferredApproval) error
	RemoveHold(ctx context.Context, identityID, extensionID string) error
	// ClaimHold arms a held question in one statement: the state becomes
	// awaiting_approval for that request with approval_armed_at set to
	// ArmedUnconfirmed — only if the thread is in a state the question may
	// interrupt (not already asking one) and the hold is still there. The
	// hold itself stays until ConfirmArmed, so a crash between the claim
	// and the send leaves a question that no message can answer and that
	// the next resume point re-sends. False when another caller got there
	// first or the state moved (D-17 I-O).
	ClaimHold(ctx context.Context, identityID, extensionID string, at time.Time) (bool, error)
	// ReclaimUnconfirmed renews the claim time on an unconfirmed question
	// — only if it is still unconfirmed and its claim time is what the
	// caller read — so of two repairs running at once exactly one re-sends.
	ReclaimUnconfirmed(ctx context.Context, identityID, extensionID string, seen *time.Time, at time.Time) (bool, error)
	// ConfirmArmed, after the question was delivered: stamps the real
	// armed-at time and drops the hold — if that request is still the
	// armed one (false otherwise: it ended while the send was in flight).
	ConfirmArmed(ctx context.Context, identityID, extensionID string, at time.Time) (bool, error)
	// ClearApproval ends the armed question for THAT request and no other
	// (compare-and-set on the request id): state becomes `resume`, request
	// and stamp cleared. False when it was not the armed one any more.
	ClearApproval(ctx context.Context, identityID, extensionID string, resume State) (bool, error)
}

// ArmedUnconfirmed marks a claimed question whose delivery has not been
// confirmed: nothing can be its answer (every message precedes it), and
// the hold is still there to re-send from.
var ArmedUnconfirmed = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// Unconfirmed reports whether the armed question has not been confirmed
// sent. In awaiting_approval a missing stamp counts as unconfirmed: a
// question with no known delivery time is never answerable.
func (c Conversation) Unconfirmed() bool {
	if c.State != StateAwaitingApproval {
		return false
	}
	return c.ApprovalArmedAt == nil || !c.ApprovalArmedAt.Before(ArmedUnconfirmed)
}

// UnconfirmedResend: how old a claim must be before an unconfirmed question
// is re-sent from its hold. Routing runs under a 60 s timeout, so a live
// send never gets this old.
const UnconfirmedResend = 3 * time.Minute

// armableStates: the states a supporter's question may interrupt — ours
// (a draft summary, budget, name, task choice) and idle. Not
// awaiting_approval: one question at a time. canArmApproval and the SQL
// literal both come from this one list.
var armableStates = []State{StateIdle, StateAwaitingConfirm, StateAwaitingTaskChoice, StateAwaitingBudget, StateAwaitingName}

func armableStatesLiteral() string {
	parts := make([]string, len(armableStates))
	for i, s := range armableStates {
		parts[i] = string(s)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func (s *PGStore) ClaimHold(ctx context.Context, identityID, extensionID string, at time.Time) (bool, error) {
	var one int
	err := s.DB.QueryRowContext(ctx, `
		UPDATE channel_conversations SET
		  state = $3, pending_extension_id = $2::uuid, approval_armed_at = $4, approval_claimed_at = $6, updated_at = now()
		WHERE identity_id = $1 AND state = ANY($5::text[])
		  AND coalesce(deferred_approval, '[]'::jsonb) @> jsonb_build_array(jsonb_build_object('extension_id', $2::text))
		RETURNING 1
	`, identityID, extensionID, string(StateAwaitingApproval), ArmedUnconfirmed, armableStatesLiteral(), at).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim hold: %w", err)
	}
	return true, nil
}

func (s *PGStore) ReclaimUnconfirmed(ctx context.Context, identityID, extensionID string, seen *time.Time, at time.Time) (bool, error) {
	var one int
	var seenV any
	if seen != nil {
		seenV = *seen
	}
	err := s.DB.QueryRowContext(ctx, `
		UPDATE channel_conversations SET approval_claimed_at = $4
		WHERE identity_id = $1 AND state = $5 AND pending_extension_id = $2::uuid
		  AND approval_armed_at >= $6 AND approval_claimed_at IS NOT DISTINCT FROM $3
		RETURNING 1
	`, identityID, extensionID, seenV, at, string(StateAwaitingApproval), ArmedUnconfirmed).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reclaim unconfirmed: %w", err)
	}
	return true, nil
}

func (s *PGStore) ConfirmArmed(ctx context.Context, identityID, extensionID string, at time.Time) (bool, error) {
	var one int
	err := s.DB.QueryRowContext(ctx, `
		UPDATE channel_conversations SET
		  approval_armed_at = $3,
		  deferred_approval = (SELECT jsonb_agg(e ORDER BY ord) FROM jsonb_array_elements(coalesce(deferred_approval, '[]'::jsonb)) WITH ORDINALITY AS t(e, ord)
		                        WHERE e->>'extension_id' <> $2::text)
		WHERE identity_id = $1 AND state = $4 AND pending_extension_id = $2::uuid
		RETURNING 1
	`, identityID, extensionID, at, string(StateAwaitingApproval)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("confirm armed: %w", err)
	}
	return true, nil
}

func (s *PGStore) ClearApproval(ctx context.Context, identityID, extensionID string, resume State) (bool, error) {
	var one int
	err := s.DB.QueryRowContext(ctx, `
		UPDATE channel_conversations SET
		  state = $3, pending_extension_id = NULL, approval_armed_at = NULL, approval_claimed_at = NULL, updated_at = now()
		WHERE identity_id = $1 AND state = $4 AND pending_extension_id = $2::uuid
		RETURNING 1
	`, identityID, extensionID, string(resume), string(StateAwaitingApproval)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("clear approval: %w", err)
	}
	return true, nil
}

func (s *PGStore) HoldApproval(ctx context.Context, identityID string, d DeferredApproval) error {
	b, err := json.Marshal([]DeferredApproval{d})
	if err != nil {
		return fmt.Errorf("hold approval: encode: %w", err)
	}
	_, err = s.DB.ExecContext(ctx, `
		INSERT INTO channel_conversations (identity_id, deferred_approval, updated_at)
		VALUES ($1, $2::jsonb, now())
		ON CONFLICT (identity_id) DO UPDATE SET
		  deferred_approval = CASE
		    WHEN coalesce(channel_conversations.deferred_approval, '[]'::jsonb) @> jsonb_build_array(jsonb_build_object('extension_id', $3::text))
		      OR (channel_conversations.state = 'awaiting_approval' AND channel_conversations.pending_extension_id::text = $3::text)
		      THEN channel_conversations.deferred_approval
		    ELSE coalesce(channel_conversations.deferred_approval, '[]'::jsonb) || $2::jsonb
		  END
	`, identityID, string(b), d.ExtensionID)
	if err != nil {
		return fmt.Errorf("hold approval: %w", err)
	}
	return nil
}

func (s *PGStore) RemoveHold(ctx context.Context, identityID, extensionID string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE channel_conversations SET
		  deferred_approval = (SELECT jsonb_agg(e ORDER BY ord) FROM jsonb_array_elements(coalesce(deferred_approval, '[]'::jsonb)) WITH ORDINALITY AS t(e, ord)
		                        WHERE e->>'extension_id' <> $2::text)
		WHERE identity_id = $1
		  AND NOT (state = $3 AND pending_extension_id::text = $2::text) -- the armed request's hold is its re-send source
	`, identityID, extensionID, string(StateAwaitingApproval))
	if err != nil {
		return fmt.Errorf("remove hold: %w", err)
	}
	return nil
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
	var name, ext, relay sql.NullString
	var terms, armed, lastIn, claimed sql.NullTime
	var choicesJSON string
	var deferred sql.NullString
	err := s.DB.QueryRowContext(ctx, `
		SELECT state, pending_draft, pending_name, terms_accepted_at, pending_extension_id::text,
		       COALESCE(array_to_json(pending_task_choices)::text, '[]'), pending_relay_text,
		       deferred_approval::text, approval_armed_at, last_inbound_at, approval_claimed_at
		FROM channel_conversations WHERE identity_id = $1
	`, identityID).Scan(&state, &pending, &name, &terms, &ext, &choicesJSON, &relay, &deferred, &armed, &lastIn, &claimed)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("get conversation: %w", err)
	}
	c.State = State(state)
	c.PendingName = name.String
	c.PendingExtensionID = ext.String
	c.PendingRelayText = relay.String
	if deferred.Valid && deferred.String != "" && deferred.String != "null" {
		if err := json.Unmarshal([]byte(deferred.String), &c.Deferred); err != nil {
			return c, fmt.Errorf("get conversation: decode deferred approvals: %w", err)
		}
	}
	if choicesJSON != "" && choicesJSON != "null" {
		if err := json.Unmarshal([]byte(choicesJSON), &c.PendingTaskChoices); err != nil {
			return c, fmt.Errorf("get conversation: decode task choices: %w", err)
		}
	}
	if terms.Valid {
		t := terms.Time
		c.TermsAcceptedAt = &t
	}
	if armed.Valid {
		t := armed.Time
		c.ApprovalArmedAt = &t
	}
	if lastIn.Valid {
		t := lastIn.Time
		c.LastInboundAt = &t
	}
	if claimed.Valid {
		t := claimed.Time
		c.ApprovalClaimedAt = &t
	}
	if len(pending) > 0 && string(pending) != "null" {
		var p PendingDraft
		if err := json.Unmarshal(pending, &p); err != nil {
			return c, fmt.Errorf("get conversation: decode pending draft: %w", err)
		}
		c.Pending = &p
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
	var terms, armed any
	if c.TermsAcceptedAt != nil {
		terms = *c.TermsAcceptedAt
	}
	if c.ApprovalArmedAt != nil {
		armed = *c.ApprovalArmedAt
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO channel_conversations
		  (identity_id, state, pending_draft, pending_name, terms_accepted_at, pending_extension_id,
		   pending_task_choices, pending_relay_text, approval_armed_at, updated_at)
		VALUES ($1, $2, $3::jsonb, NULLIF($4, ''), $5, NULLIF($6, '')::uuid,
		        NULLIF($7, '{}')::uuid[], NULLIF($8, ''), $9, now())
		ON CONFLICT (identity_id) DO UPDATE SET
		  -- An armed question (a claim that landed after this caller read
		  -- its snapshot) is kept unless the caller is ending it on purpose
		  -- (a handoff) — a stale full-row save must not silently disarm a
		  -- question the person is about to answer (S-11/5). The claim's
		  -- own fields travel with it; everything else is the caller's.
		  state = CASE WHEN channel_conversations.state = 'awaiting_approval' AND EXCLUDED.state <> 'handoff'
		               THEN channel_conversations.state ELSE EXCLUDED.state END,
		  pending_extension_id = CASE WHEN channel_conversations.state = 'awaiting_approval' AND EXCLUDED.state <> 'handoff'
		               THEN channel_conversations.pending_extension_id ELSE EXCLUDED.pending_extension_id END,
		  approval_armed_at = CASE WHEN channel_conversations.state = 'awaiting_approval' AND EXCLUDED.state <> 'handoff'
		               THEN channel_conversations.approval_armed_at ELSE EXCLUDED.approval_armed_at END,
		  approval_claimed_at = CASE WHEN channel_conversations.state = 'awaiting_approval' AND EXCLUDED.state <> 'handoff'
		               THEN channel_conversations.approval_claimed_at ELSE NULL END,
		  pending_draft = EXCLUDED.pending_draft,
		  pending_name = EXCLUDED.pending_name,
		  terms_accepted_at = COALESCE(channel_conversations.terms_accepted_at, EXCLUDED.terms_accepted_at),
		  pending_task_choices = EXCLUDED.pending_task_choices,
		  pending_relay_text = EXCLUDED.pending_relay_text,
		  updated_at = now()
	`, c.IdentityID, string(c.State), pending, c.PendingName, terms, c.PendingExtensionID,
		pgUUIDArray(c.PendingTaskChoices), c.PendingRelayText, armed)
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
			if cerr := rows.Close(); cerr != nil {
				log.Printf("[channel] conversations for user: close rows: %v", cerr)
			}
			return nil, nil, fmt.Errorf("conversations for user: scan: %w", err)
		}
		id.UserID = userID
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, fmt.Errorf("conversations for user: close rows: %w", err)
	}
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
	// strictYesRE / strictNoRE: the explicit words. With a supporter writing
	// in the last minutes only these answer a question of ours; the casual
	// affirmatives in yesRE go to the supporter (D-17 I-P, version B).
	strictYesRE = regexp.MustCompile(`(?i)^\s*(yes|y|是|確認|sí|si|confirm|confirmed)[\s!.]*$`)
	strictNoRE  = regexp.MustCompile(`(?i)^\s*(no|n|不要|取消|cancel|stop)[\s!.]*$`)
	yesRE       = regexp.MustCompile(`(?i)^\s*(yes|y|yep|yeah|yup|yes please|ok|okay|confirm|confirmed|book it|book|do it|go ahead|sure|👍|✅|是|好|好的|確認|sí|si)[\s!.]*$`)
	noRE        = regexp.MustCompile(`(?i)^\s*(no|n|nope|nah|cancel|stop|never ?mind|don'?t|不要|取消|不用)[\s!.]*$`)
	nameBadR    = regexp.MustCompile(`[0-9@/:]`)
)

// IsYes reports an explicit confirmation.
func IsYes(text string) bool { return yesRE.MatchString(text) }

// IsStrictYesNo: the explicit YES/NO words only (I-P version B).
func IsStrictYesNo(text string) bool {
	return strictYesRE.MatchString(text) || strictNoRE.MatchString(text)
}

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

// pgUUIDArray renders ids as a Postgres array literal ('{a,b}'; '{}' when
// empty, which the INSERT turns into NULL).
func pgUUIDArray(ids []string) string {
	return "{" + strings.Join(ids, ",") + "}"
}
