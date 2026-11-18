package channel

// Pilot gate (2026-11-18). The iMessage and WhatsApp numbers go on the
// website before the service opens; until launch a stranger who texts must
// not be able to book. CHANNEL_PILOT_GATE=1 turns the gate on — unset,
// nothing here runs (Router.Pilot is nil).
//
// With the gate on, these numbers are routed exactly as before:
//   - CHANNEL_PILOT_ALLOWLIST (E.164, comma-separated): the pilot's people;
//   - CHANNEL_OPS_NUMBERS: ops' own phones (also texted on a hand-off);
//   - a number whose account is an approved supporter.
//
// Anyone else gets CopyPilotClosed, at most once per PilotReplyEvery, and
// nothing else: no draft, no booking, no place search, no hand-off, no
// model reads the words, and photos are not taken. The "once" is read from
// the messages we already keep (channel_messages, the outbound row with
// this exact body), so it holds across restarts with no new table — and no
// list of gated senders is kept anywhere. Nobody is texted later: a gated
// sender has no draft, task or offer for anything to follow up on.
//
// What still runs first, for everyone (HandleInbound): STOP / START and a
// stopped number (optout.go, TCPA). A bare HELP or INFO gets what it gets
// today (help.go): the info line on iMessage, or ops when the number has a
// task under way. Where HELP would be read as words (WhatsApp, nothing
// under way) a gated sender gets the closed line instead.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// CopyPilotClosed is the one reply a gated sender gets.
const CopyPilotClosed = "Thanks for reaching out! HO:RA opens in New York City in January. We can't take requests yet, but we'd love to help once we're open."

// PilotReplyEvery: a gated sender gets CopyPilotClosed at most once in this
// long.
const PilotReplyEvery = 24 * time.Hour

// PilotReplyStore reads the closed replies already sent (channel_messages).
type PilotReplyStore interface {
	// PilotRepliedSince reports whether CopyPilotClosed went to (ch,
	// address) after since.
	PilotRepliedSince(ctx context.Context, ch Channel, address string, since time.Time) (bool, error)
	// GatedSenders counts the numbers that texted in [from, to) while
	// gated — numbers only, for the ops digest.
	GatedSenders(ctx context.Context, from, to time.Time) (int, error)
}

// PilotGate holds who passes the gate. Router.Pilot nil = no gate.
type PilotGate struct {
	// allow: CHANNEL_PILOT_ALLOWLIST and CHANNEL_OPS_NUMBERS, E.164.
	allow   map[string]bool
	Replies PilotReplyStore
}

// NewPilotGate builds a gate that lets the given numbers through. Entries
// that are not phone numbers are skipped (counted in the log, never shown).
func NewPilotGate(replies PilotReplyStore, numbers ...string) *PilotGate {
	g := &PilotGate{allow: map[string]bool{}, Replies: replies}
	for _, raw := range numbers {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if e164, err := NormalizeE164(raw); err == nil {
			g.allow[e164] = true
		}
	}
	return g
}

// Allowed reports whether address is on the allowlist or an ops number.
func (g *PilotGate) Allowed(address string) bool {
	if e164, err := NormalizeE164(address); err == nil {
		return g.allow[e164]
	}
	return g.allow[address]
}

// PilotGateOn reports CHANNEL_PILOT_GATE=1.
func PilotGateOn() bool { return env("CHANNEL_PILOT_GATE") == "1" }

// PilotGateFromEnv is the gate when CHANNEL_PILOT_GATE=1, else nil (off).
// CHANNEL_PILOT_ALLOWLIST and CHANNEL_OPS_NUMBERS are read here; the log
// line gives counts, never numbers.
func PilotGateFromEnv(replies PilotReplyStore) *PilotGate {
	if !PilotGateOn() {
		return nil
	}
	allow, badA := splitE164(env("CHANNEL_PILOT_ALLOWLIST"))
	ops, badO := splitE164(env("CHANNEL_OPS_NUMBERS"))
	g := NewPilotGate(replies, append(allow, ops...)...)
	log.Printf("[channel] pilot gate ON: %d allowlisted, %d ops numbers, approved supporters pass", len(allow), len(ops))
	if badA+badO > 0 {
		log.Printf("[channel] WARNING: pilot gate: %d allowlist/ops entries are not phone numbers, skipped", badA+badO)
	}
	return g
}

func splitE164(v string) (ok []string, bad int) {
	for _, raw := range strings.Split(v, ",") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if e164, err := NormalizeE164(raw); err == nil {
			ok = append(ok, e164)
		} else {
			bad++
		}
	}
	return ok, bad
}

// pilotGate runs right after the opt-out rules (HandleInbound). gated: the
// message is not routed; the closed line went out, or had within
// PilotReplyEvery. A bare HELP / INFO is answered as today when help.go
// answers it.
func (r *Router) pilotGate(ctx context.Context, in Inbound, id Identity) (gated bool, err error) {
	g := r.Pilot
	if g == nil || g.Allowed(in.From) {
		return false, nil
	}
	// Unlinked numbers are not looked up (approved returns at once). A
	// failed lookup reads as "not approved": gated, fail closed.
	if r.approved(ctx, id) {
		return false, nil
	}
	hint := PhoneHint(in.From)
	text := strings.TrimSpace(in.Text)
	if IsHelpKeyword(text) || IsInfoKeyword(text) {
		conv, err := r.Conversations.GetConversation(ctx, id.ID)
		if err != nil {
			return true, fmt.Errorf("pilot gate: %w", err)
		}
		if handled, err := r.routeHelp(ctx, in, id, conv, text); handled {
			log.Printf("[channel] pilot gate channel=%s from=%s: HELP/INFO answered as usual", in.Channel, hint)
			return true, err
		}
	}
	if g.Replies == nil {
		return true, fmt.Errorf("pilot gate: no reply store")
	}
	sent, err := g.Replies.PilotRepliedSince(ctx, in.Channel, in.From, r.now().Add(-PilotReplyEvery))
	if err != nil {
		// Unknown: no reply rather than a second one. The error leaves the
		// message for the recovery sweep, which asks again.
		return true, fmt.Errorf("pilot gate: %w", err)
	}
	if sent {
		log.Printf("[channel] pilot gate channel=%s from=%s: gated, already told within %s", in.Channel, hint, PilotReplyEvery)
		return true, nil
	}
	log.Printf("[channel] pilot gate channel=%s from=%s: gated, closed line sent", in.Channel, hint)
	return true, r.Reply(ctx, in, CopyPilotClosed, "")
}

// PilotRepliedSince: PilotReplyStore over channel_messages.
func (s *PGStore) PilotRepliedSince(ctx context.Context, ch Channel, address string, since time.Time) (bool, error) {
	var ok bool
	err := s.DB.QueryRowContext(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM channel_messages
		  WHERE channel = $1 AND address = $2 AND direction = 'outbound'
		    AND body = $3 AND received_at > $4)
	`, string(ch), address, CopyPilotClosed, since).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("pilot replied since: %w", err)
	}
	return ok, nil
}

// GatedSenders counts the distinct numbers with a message in [from, to)
// that had the closed line within PilotReplyEvery before it (or as its
// answer, a moment after). A count only; no number leaves the query.
func (s *PGStore) GatedSenders(ctx context.Context, from, to time.Time) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `
		SELECT count(DISTINCT i.channel::text || ' ' || i.address)
		FROM channel_messages i
		WHERE i.direction = 'inbound' AND i.received_at >= $1 AND i.received_at < $2
		  AND EXISTS (
		    SELECT 1 FROM channel_messages o
		    WHERE o.channel = i.channel AND o.address = i.address
		      AND o.direction = 'outbound' AND o.body = $3
		      AND o.received_at > i.received_at - interval '24 hours'
		      AND o.received_at < i.received_at + interval '10 minutes')
	`, from, to, CopyPilotClosed).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("gated senders: %w", err)
	}
	return n, nil
}
