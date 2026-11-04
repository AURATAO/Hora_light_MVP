package channel

// Messages from a stopped number reach ops (2026-11-04). Before, anything an
// opted-out number wrote other than START was recorded and seen by no one.
// We still send nothing back — the person opted out (TCPA), and Sendblue
// refuses every send — but ops can read it and reach them another way: a
// phone call, an email, the web.
//
//   - Every message other than START (and another opt-out keyword) is
//     forwarded on the hand-off alert path, email and ops text: the number's
//     last four digits, the words, the account's email when linked, and
//     whether they have tasks under way.
//   - Urgent first: a request for a person, or anything that would make the
//     agent escalate (feeling unsafe, harassment, injury, a theft or damage
//     claim, a charge dispute, legal or medical) — matched by rule, never a
//     model — goes at once, marked urgent.
//   - Anything else is batched: at most one alert per number per
//     OptedOutForwardWindow; messages in between wait for the next alert or
//     for the hand-off sweep (FlushOptedOut), which sends them once the
//     window has passed.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"regexp"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"hora-auth/internal/tracing"
)

// HandoffOptedOutMessage: a number that stopped texts wrote to us. Not a
// hand-off: the thread cannot be written to.
const HandoffOptedOutMessage HandoffReason = "opted_out_message"

// OptedOutForwardWindow: at most one ordinary alert per number per window.
const OptedOutForwardWindow = 10 * time.Minute

// OptedOutForwards keeps the batch and the rate limit of forwards to ops. An
// optional OptOutStore capability; without it every message is forwarded on
// its own.
type OptedOutForwards interface {
	// TakeOptedOutBatch decides whether a forward is due for the identity —
	// urgent, or no forward within window — and if so takes it: the batch is
	// every inbound message since the last forward (opt-out keywords left
	// out), and the forward is recorded. Not due: the identity is marked as
	// having messages waiting. Due is false, too, when nothing is left to
	// forward or the number is no longer opted out.
	TakeOptedOutBatch(ctx context.Context, identityID string, urgent bool, window time.Duration) (OptedOutBatch, error)
	// RequeueOptedOutBatch puts back a forward that reached no one: the
	// messages wait again, and the sweep retries them — an urgent batch at
	// its next pass, an ordinary one once the window has passed.
	RequeueOptedOutBatch(ctx context.Context, b OptedOutBatch) error
	// WaitingOptedOutForwards lists identities whose waiting messages are
	// due: the window has passed since their last forward.
	WaitingOptedOutForwards(ctx context.Context, window time.Duration) ([]string, error)
}

// OptedOutBatch is one forward to ops.
type OptedOutBatch struct {
	IdentityID string
	Due        bool
	Messages   []ThreadMessage // oldest first
	// Urgent: any of the messages is (set by the router before a requeue).
	Urgent bool
	// The forward as recorded, and what it replaced (to put it back).
	ForwardedAt     time.Time
	Through         time.Time
	PrevForwardedAt *time.Time
	PrevThrough     *time.Time
}

// optedOutUrgentRE: the escalation reasons, by rule (llm/agent.go's
// "escalate" list). Erring toward urgent: a false alarm costs ops a look.
var optedOutUrgentRE = []struct {
	reason string
	re     *regexp.Regexp
}{
	{"safety", regexp.MustCompile(`(?i)\b(unsafe|not safe|danger(ous)?|scared|afraid|frightened|threat(en(ed|ing)?|s)?|harass(ed|ing|ment)?|stalk(ed|ing|er)?|follow(ed|ing) me|assault(ed)?|attack(ed)?|hit me|grabbed me|touched me|abus(e|ed|ive)|hurt|injur(y|ed|ies)|bleeding|hospital|ambulance|emergency|911|police|cops?|help me|creep(y)?|inappropriate)\b|不安全|騷擾|受傷|報警|救命`)},
	{"theft or damage", regexp.MustCompile(`(?i)\b(stole|stolen|steal(ing)?|theft|thief|robbed|rob(bery)?|missing|took my|never (came|arrived|delivered)|damaged?|broke(n)?|ruined)\b|偷|被搶|弄壞`)},
	{"charge dispute", regexp.MustCompile(`(?i)\b(charged (me )?(twice|double|wrong(ly)?|too much|more)|double[- ]charged?|overcharg(e|ed|ing)|wrong(ly)? charged|refund|dispute|chargeback|fraud(ulent)?|scam(med)?|unauthori[sz]ed|didn'?t authori[sz]e|my bank)\b|退款|多收`)},
	{"legal or medical", regexp.MustCompile(`(?i)\b(lawyer|attorney|sue|suing|lawsuit|legal action|medical|doctor|allergic|allergy)\b`)},
}

// optedOutHumanRE: asking to be reached, beyond IsHumanRequest's wording.
var optedOutHumanRE = regexp.MustCompile(`(?i)\b(call me|phone me|email me|e-mail me|contact me|reach me|get back to me|someone (call|contact|email)|need (a |to talk to (a )?)?(human|person)|need help)\b|^\s*help[\s!.?]*$|打給我|聯絡我`)

// OptedOutUrgency reports whether a message from a stopped number is urgent,
// and why ("asked for a person", "safety", …).
func OptedOutUrgency(text string) (bool, string) {
	for _, u := range optedOutUrgentRE {
		if u.re.MatchString(text) {
			return true, u.reason
		}
	}
	if IsHumanRequest(text) || optedOutHumanRE.MatchString(text) {
		return true, "asked for a person"
	}
	return false, ""
}

// forwardOptedOut is every message from a stopped number other than START
// or an opt-out keyword (handleOptOut). Best effort and silent: nothing is
// ever sent to the number; a failure is logged and retried by the sweep.
func (r *Router) forwardOptedOut(ctx context.Context, in Inbound, id Identity) {
	urgent, _ := OptedOutUrgency(in.Text)
	at := in.ReceivedAt
	if at.IsZero() {
		at = r.now()
	}
	one := []ThreadMessage{{Direction: "inbound", Text: in.Text, At: at}}
	if err := r.sendOptedOutForward(context.WithoutCancel(ctx), id, urgent, one); err != nil {
		log.Printf("[channel] opted-out forward identity=%s: ops NOT told (retried by the sweep): %v", id.ID, err)
	}
}

// FlushOptedOut sends a stopped number's waiting messages once the window
// has passed (the hand-off sweep). Nothing due: nothing sent.
func (r *Router) FlushOptedOut(ctx context.Context, id Identity) error {
	return r.sendOptedOutForward(ctx, id, false, nil)
}

func (r *Router) sendOptedOutForward(ctx context.Context, id Identity, urgentNow bool, single []ThreadMessage) (err error) {
	// Under the message's trace, or its own root from the sweep.
	ctx, sp := tracing.StartRootSpan(ctx, "channel.opted_out_forward", tracing.KindChain, string(id.Channel), id.UserID, id.ID)
	outcome, reason, urgent, n, openTasks := "", "", false, 0, 0
	defer func() {
		// Counts and labels only: never the number, never the words (D-22).
		sp.Attr(attribute.String("hora.forward.outcome", outcome), attribute.Bool("hora.forward.urgent", urgent),
			attribute.String("hora.forward.reason", reason), attribute.Int("hora.forward.messages", n),
			attribute.Int("hora.forward.open_tasks", openTasks), attribute.Bool("hora.forward.sweep", single == nil))
		sp.Metadata(map[string]any{"outcome": outcome, "urgent": urgent, "reason": reason, "messages": n})
		sp.Fail(err)
		sp.End()
	}()
	store, batched := r.OptOuts.(OptedOutForwards)
	var b OptedOutBatch
	switch {
	case batched:
		b, err = store.TakeOptedOutBatch(ctx, id.ID, urgentNow, OptedOutForwardWindow)
		if err != nil {
			outcome = "error"
			return fmt.Errorf("opted-out forward: %w", err)
		}
		if !b.Due {
			outcome = "batched"
			return nil
		}
	case single == nil:
		outcome = "nothing_waiting"
		return nil
	default:
		b = OptedOutBatch{IdentityID: id.ID, Due: true, Messages: single}
	}
	n = len(b.Messages)
	for _, m := range b.Messages {
		if u, why := OptedOutUrgency(m.Text); u && !urgent {
			urgent, reason = true, why
		}
	}
	open := r.openTasksFor(ctx, id)
	openTasks = len(open.openTasks)
	open.optedOut, open.thread, open.urgent, open.urgentReason = true, b.Messages, urgent, reason
	b.Urgent = urgent
	if err = r.notifyOps(ctx, id, HandoffOptedOutMessage, false, open); err != nil {
		outcome = "failed"
		if batched {
			if rerr := store.RequeueOptedOutBatch(ctx, b); rerr != nil {
				log.Printf("[channel] opted-out forward identity=%s: requeue: %v", id.ID, rerr)
			}
		}
		return err
	}
	outcome = "forwarded"
	log.Printf("[channel] opted-out forward identity=%s: %d message(s) to ops urgent=%t reason=%q open_tasks=%d", id.ID, n, urgent, reason, openTasks)
	return nil
}

// openTasksFor: the person's tasks still under way, for an ops notice.
func (r *Router) openTasksFor(ctx context.Context, id Identity) handoffOpen {
	var open handoffOpen
	if id.UserID == "" || r.Booker == nil {
		return open
	}
	var err error
	if b, ok := r.Booker.(OpenOrPendingTasks); ok {
		open.openTasks, err = b.OpenOrPendingTasks(ctx, id.UserID)
	} else {
		open.openTasks, err = r.Booker.OpenTasks(ctx, id.UserID)
	}
	if err != nil {
		log.Printf("[channel] identity=%s: open tasks: %v", id.ID, err)
		open.openTasksUnknown = true
	}
	return open
}

// ── Postgres ───────────────────────────────────────────────────────────────

func (s *PGStore) TakeOptedOutBatch(ctx context.Context, identityID string, urgent bool, window time.Duration) (OptedOutBatch, error) {
	b := OptedOutBatch{IdentityID: identityID}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return b, fmt.Errorf("take opted-out batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var ch, addr string
	var optedOut, prevF, prevT sql.NullTime
	var now time.Time
	// The row lock serialises two messages arriving together: one takes the
	// forward, the other finds it taken and waits for the next.
	err = tx.QueryRowContext(ctx, `
		SELECT channel, address, opted_out_at, opted_out_forwarded_at, opted_out_forwarded_through, now()
		FROM channel_identities WHERE id = $1::uuid FOR UPDATE
	`, identityID).Scan(&ch, &addr, &optedOut, &prevF, &prevT, &now)
	if err != nil {
		return b, fmt.Errorf("take opted-out batch: %w", err)
	}
	if !optedOut.Valid {
		return b, nil // texted START meanwhile
	}
	if !urgent && prevF.Valid && now.Sub(prevF.Time) < window {
		if _, err := tx.ExecContext(ctx, `
			UPDATE channel_identities SET opted_out_pending_since = COALESCE(opted_out_pending_since, now())
			WHERE id = $1::uuid`, identityID); err != nil {
			return b, fmt.Errorf("take opted-out batch: wait: %w", err)
		}
		return b, tx.Commit()
	}
	since := optedOut.Time
	if prevT.Valid {
		since = prevT.Time
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT COALESCE(body, ''), received_at, created_at FROM channel_messages
		WHERE channel = $1 AND address = $2 AND direction = 'inbound' AND created_at > $3
		ORDER BY created_at
		LIMIT 50
	`, ch, addr, since)
	if err != nil {
		return b, fmt.Errorf("take opted-out batch: messages: %w", err)
	}
	var through time.Time
	read := 0
	for rows.Next() {
		read++
		var m ThreadMessage
		var created time.Time
		if err := rows.Scan(&m.Text, &m.At, &created); err != nil {
			_ = rows.Close()
			return b, fmt.Errorf("take opted-out batch: scan: %w", err)
		}
		through = created
		if IsOptOutKeyword(m.Text) || IsOptInKeyword(m.Text) {
			continue // the keyword itself is not news for ops
		}
		m.Direction = "inbound"
		b.Messages = append(b.Messages, m)
	}
	if err := rows.Close(); err != nil {
		return b, fmt.Errorf("take opted-out batch: %w", err)
	}
	if err := rows.Err(); err != nil {
		return b, fmt.Errorf("take opted-out batch: %w", err)
	}
	if len(b.Messages) == 0 {
		// Nothing to tell: the waiting mark goes, the window is not used.
		if _, err := tx.ExecContext(ctx, `
			UPDATE channel_identities SET opted_out_pending_since = NULL,
			       opted_out_forwarded_through = COALESCE($2, opted_out_forwarded_through)
			WHERE id = $1::uuid`, identityID, nullTime(through)); err != nil {
			return b, fmt.Errorf("take opted-out batch: clear: %w", err)
		}
		b.Messages = nil
		return b, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE channel_identities SET opted_out_forwarded_at = $2, opted_out_forwarded_through = $3,
		       opted_out_pending_since = CASE WHEN $4 THEN now() END
		WHERE id = $1::uuid`, identityID, now, through, read == 50); err != nil { // 50: more wait
		return b, fmt.Errorf("take opted-out batch: record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return b, fmt.Errorf("take opted-out batch: %w", err)
	}
	b.Due, b.ForwardedAt, b.Through = true, now, through
	if prevF.Valid {
		b.PrevForwardedAt = &prevF.Time
	}
	if prevT.Valid {
		b.PrevThrough = &prevT.Time
	}
	return b, nil
}

func (s *PGStore) RequeueOptedOutBatch(ctx context.Context, b OptedOutBatch) error {
	var prevF, prevT any
	if b.PrevForwardedAt != nil && !b.Urgent { // urgent: no window, retried at once
		prevF = *b.PrevForwardedAt
	}
	if b.PrevThrough != nil {
		prevT = *b.PrevThrough
	}
	// Only if no later forward was taken meanwhile.
	_, err := s.DB.ExecContext(ctx, `
		UPDATE channel_identities SET opted_out_forwarded_at = $3, opted_out_forwarded_through = $4,
		       opted_out_pending_since = COALESCE(opted_out_pending_since, now())
		WHERE id = $1::uuid AND opted_out_forwarded_at = $2`, b.IdentityID, b.ForwardedAt, prevF, prevT)
	if err != nil {
		return fmt.Errorf("requeue opted-out batch: %w", err)
	}
	return nil
}

func (s *PGStore) WaitingOptedOutForwards(ctx context.Context, window time.Duration) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id::text FROM channel_identities
		WHERE opted_out_pending_since IS NOT NULL AND opted_out_at IS NOT NULL
		  AND (opted_out_forwarded_at IS NULL OR opted_out_forwarded_at <= now() - $1::interval)
		ORDER BY opted_out_pending_since
		LIMIT 100
	`, fmt.Sprintf("%d seconds", int(window.Seconds())))
	if err != nil {
		return nil, fmt.Errorf("waiting opted-out forwards: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("waiting opted-out forwards: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("waiting opted-out forwards: %w", err)
	}
	return ids, nil
}
