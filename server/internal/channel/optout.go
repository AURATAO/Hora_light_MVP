package channel

// Text opt-out (2026-11-03). STOP, QUIT, END, REVOKE, OPT OUT, CANCEL and
// UNSUBSCRIBE are carrier opt-out keywords (the FCC's list, and Sendblue's).
// Sendblue honours them itself: a number that sent one is refused on every
// later send (400, OPTED_OUT) until it texts START. Production 2026-11-03: a
// test number texted "Cancel" — our own copy told people to — and every
// reply after it failed while HO:RA carried on as if it were heard.
//
// So, on the iMessage line:
//   - a message that is only an opt-out keyword is an opt-out, ahead of every
//     other rule. Nothing else reads it: a bare "stop" no longer declines a
//     supporter's request, a bare "cancel" no longer starts a cancel whose
//     questions could never arrive;
//   - a send Sendblue refuses with OPTED_OUT marks the number the same way
//     (the person may have opted out in a way we did not see);
//   - while marked, nothing is sent to the number and its messages are not
//     acted on (no one would hear the answer); ops are told once, with the
//     person's open tasks, and the app tells the person and the supporter;
//   - START clears the mark and gets one short welcome-back line.
//
// Opting out is a different thing from cancelling a task. Our copy teaches
// "cancel task" (two words, never a keyword); IsCancelRequest accepts any
// sentence that mentions cancelling.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"hora-auth/internal/tracing"
)

// OptOutStore keeps the opt-out mark on a channel identity
// (channel_identities.opted_out_at).
type OptOutStore interface {
	// MarkOptedOut sets the mark on (ch, address), creating the identity if
	// we have never seen it. newly is false when it was already set.
	MarkOptedOut(ctx context.Context, ch Channel, address string) (id Identity, newly bool, err error)
	// ClearOptedOut removes the mark. cleared is false when none was set.
	ClearOptedOut(ctx context.Context, ch Channel, address string) (cleared bool, err error)
	// OptedOut reports whether (ch, address) is marked. An unknown number is
	// not.
	OptedOut(ctx context.Context, ch Channel, address string) (bool, error)
}

// OpenOrPendingTasks is an optional Booker capability: the requester's tasks
// still under way — posted (open, assigned or not) or awaiting a payment
// step. Ops are told about them when the person stops texts. Without it the
// router falls back to OpenTasks.
type OpenOrPendingTasks interface {
	OpenOrPendingTasks(ctx context.Context, userID string) ([]TaskRef, error)
}

// HandoffOptedOut: not a hand-off — the person stopped texts. Sent on the
// hand-off alert path so ops hear about it the same way.
const HandoffOptedOut HandoffReason = "opted_out"

// optOutWords are the opt-out keywords, normalized (optOutNorm).
var optOutWords = map[string]bool{
	"stop": true, "quit": true, "end": true, "revoke": true, "opt out": true,
	"cancel": true, "unsubscribe": true,
}

var (
	optOutTrimRE  = regexp.MustCompile(`^[\s.!?,;:"'“”‘’()*~-]+|[\s.!?,;:"'“”‘’()*~-]+$`)
	optOutSpaceRE = regexp.MustCompile(`[\s_-]+`)
)

// optOutNorm lower-cases, trims surrounding punctuation and joins "opt-out"
// / "optout" / "OPT  OUT" into "opt out".
func optOutNorm(text string) string {
	t := strings.ToLower(optOutTrimRE.ReplaceAllString(text, ""))
	t = optOutSpaceRE.ReplaceAllString(t, " ")
	if t == "optout" {
		t = "opt out"
	}
	return t
}

// IsOptOutKeyword reports a message that is only an opt-out keyword
// ("STOP", "Cancel.", "opt out"). A keyword inside a sentence ("cancel
// task", "please stop by the store") is not one.
func IsOptOutKeyword(text string) bool { return optOutWords[optOutNorm(text)] }

// IsOptInKeyword reports a message that is only START.
func IsOptInKeyword(text string) bool { return optOutNorm(text) == "start" }

// optOutApplies: the channel whose provider enforces carrier keywords.
// WhatsApp has no such keywords; "cancel" there is still a cancel request.
func optOutApplies(ch Channel) bool { return ch == IMessage }

// handleOptOut runs first for every inbound message (HandleInbound).
// handled: nothing else may read the message.
func (r *Router) handleOptOut(ctx context.Context, in Inbound, id Identity) (handled bool, err error) {
	if r.OptOuts == nil || !optOutApplies(in.Channel) {
		return false, nil
	}
	if IsOptInKeyword(in.Text) {
		cleared, err := r.OptOuts.ClearOptedOut(ctx, in.Channel, in.From)
		if err != nil {
			return true, fmt.Errorf("opt in: %w", err)
		}
		if !cleared {
			return false, nil // was never stopped: an ordinary message
		}
		log.Printf("[channel] opt-in identity=%s: texts back on", id.ID)
		return true, r.Reply(ctx, in, CopyOptInWelcome, "")
	}
	if IsOptOutKeyword(in.Text) {
		// The provider has stopped the number already: no reply (it would be
		// refused), no other reading of the word.
		r.recordOptOut(ctx, in.Channel, in.From, "keyword")
		return true, nil
	}
	opted, err := r.OptOuts.OptedOut(ctx, in.Channel, in.From)
	if err != nil {
		return true, fmt.Errorf("opt-out check: %w", err)
	}
	if opted {
		// Stopped: no answer could reach them, so nothing is done on their
		// word either. The message is recorded; ops were told at the opt-out.
		log.Printf("[channel] inbound id=%s identity=%s: number has stopped texts, not acted on (START resumes)", in.ProviderMessageID, id.ID)
		return true, nil
	}
	return false, nil
}

// recordOptOut marks the number and, the first time, records an error span
// and alerts ops — with the person's open tasks, since a supporter may be
// texting into the void. Best effort: logged, never fails the caller.
func (r *Router) recordOptOut(ctx context.Context, ch Channel, address, source string) {
	if r.OptOuts == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	id, newly, err := r.OptOuts.MarkOptedOut(ctx, ch, address)
	if err != nil {
		log.Printf("[channel] opt-out to=%s source=%s: NOT recorded: %v", Redact(address), source, err)
		return
	}
	if !newly {
		return
	}
	open := handoffOpen{optedOut: true}
	if id.UserID != "" && r.Booker != nil {
		var tasks []TaskRef
		if b, ok := r.Booker.(OpenOrPendingTasks); ok {
			tasks, err = b.OpenOrPendingTasks(ctx, id.UserID)
		} else {
			tasks, err = r.Booker.OpenTasks(ctx, id.UserID)
		}
		if err != nil {
			log.Printf("[channel] opt-out identity=%s: open tasks: %v", id.ID, err)
			open.openTasksUnknown = true
		}
		open.openTasks = tasks
	}
	tracing.ErrorEvent(ctx, "channel.opted_out", string(ch), id.UserID, id.ID, ErrRecipientOptedOut,
		map[string]any{"source": source, "open_tasks": len(open.openTasks), "open_tasks_unknown": open.openTasksUnknown})
	log.Printf("[channel] opt-out identity=%s source=%s open_tasks=%d: no more texts until START", id.ID, source, len(open.openTasks))
	if err := r.notifyOps(ctx, id, HandoffOptedOut, false, open); err != nil {
		log.Printf("[channel] opt-out identity=%s: ops NOT told: %v", id.ID, err)
	}
}

// ── Postgres ───────────────────────────────────────────────────────────────

func (s *PGStore) MarkOptedOut(ctx context.Context, ch Channel, address string) (Identity, bool, error) {
	if !ch.Valid() {
		return Identity{}, false, fmt.Errorf("mark opted out: invalid channel")
	}
	scan := func(row *sql.Row) (Identity, error) {
		var id Identity
		var userID sql.NullString
		err := row.Scan(&id.ID, &id.Channel, &id.Address, &userID)
		id.UserID = userID.String
		return id, err
	}
	// "newly" must be exact: it decides whether ops are alerted. A
	// concurrent second UPDATE waits on the row lock, re-reads the WHERE on
	// the committed row and matches nothing.
	id, err := scan(s.DB.QueryRowContext(ctx, `
		UPDATE channel_identities SET opted_out_at = now()
		WHERE channel = $1 AND address = $2 AND opted_out_at IS NULL
		RETURNING id, channel, address, user_id
	`, string(ch), address))
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Identity{}, false, fmt.Errorf("mark opted out: %w", err)
	}
	// Already marked, or a number we have never seen.
	id, err = scan(s.DB.QueryRowContext(ctx, `
		INSERT INTO channel_identities (channel, address, opted_out_at)
		VALUES ($1, $2, now())
		ON CONFLICT (channel, address) DO NOTHING
		RETURNING id, channel, address, user_id
	`, string(ch), address))
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Identity{}, false, fmt.Errorf("mark opted out: insert: %w", err)
	}
	id, err = scan(s.DB.QueryRowContext(ctx, `
		SELECT id, channel, address, user_id FROM channel_identities
		WHERE channel = $1 AND address = $2
	`, string(ch), address))
	if err != nil {
		return Identity{}, false, fmt.Errorf("mark opted out: read: %w", err)
	}
	return id, false, nil
}

func (s *PGStore) ClearOptedOut(ctx context.Context, ch Channel, address string) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `
		UPDATE channel_identities SET opted_out_at = NULL
		WHERE channel = $1 AND address = $2 AND opted_out_at IS NOT NULL
	`, string(ch), address)
	if err != nil {
		return false, fmt.Errorf("clear opted out: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("clear opted out: %w", err)
	}
	return n > 0, nil
}

func (s *PGStore) OptedOut(ctx context.Context, ch Channel, address string) (bool, error) {
	var at sql.NullTime
	err := s.DB.QueryRowContext(ctx, `
		SELECT opted_out_at FROM channel_identities WHERE channel = $1 AND address = $2
	`, string(ch), address).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("opted out: %w", err)
	}
	return at.Valid, nil
}

// UserTextsStopped: whether a number linked to userID on the iMessage line
// has stopped texts, and since when — for the app (GET /profile, and the
// supporter's view of the task).
func (s *PGStore) UserTextsStopped(ctx context.Context, userID string) (bool, time.Time, error) {
	var at sql.NullTime
	err := s.DB.QueryRowContext(ctx, `
		SELECT max(opted_out_at) FROM channel_identities
		WHERE user_id = $1::uuid AND channel = $2 AND opted_out_at IS NOT NULL
	`, userID, string(IMessage)).Scan(&at)
	if err != nil {
		return false, time.Time{}, fmt.Errorf("user texts stopped: %w", err)
	}
	return at.Valid, at.Time, nil
}
