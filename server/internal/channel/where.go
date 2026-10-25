package channel

// "Where is he?" — answered from the live tracking the app already has
// (D-12: GPS pings while the supporter is on the way, the clock once they
// work), not passed to the supporter (Arbiter 2026-10-25 22:27). Only when
// the phone has stopped reporting is the question relayed — and the person
// is told it was.

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
)

// LiveState is the app's own closed set (live_tracking.go).
type LiveState string

const (
	LiveOnTheWay    LiveState = "on_the_way"
	LiveAlmostThere LiveState = "almost_there"
	LiveAtDoor      LiveState = "at_door"
	LiveWorking     LiveState = "working"
	LiveUnavailable LiveState = "unavailable"
)

// LiveStatus is where a supporter is, as the app's map shows it.
type LiveStatus struct {
	State LiveState
	// DistanceMeters to the task's place; nil when unknown or stale.
	DistanceMeters *float64
	// LastSeen: the latest position's time; zero when none.
	LastSeen time.Time
	// WorkedMinutes: minutes clocked so far (the running session included).
	WorkedMinutes int
	// NotYetOnTheWay: assigned, but the supporter has not tapped "On my way"
	// and sends no position yet.
	NotYetOnTheWay bool
}

// LiveLookup reads the live status of an active task for its requester.
type LiveLookup interface {
	LiveStatus(ctx context.Context, userID, taskID string) (LiveStatus, error)
}

// whereRE: a question about where the supporter is or how far along.
var whereRE = regexp.MustCompile(`(?i)\b(where (is|are|'s|s) (he|she|they|it|my supporter|the supporter|(?-i:[A-Z][a-z]+))\b|where'?s (he|she|(?-i:[A-Z][a-z]+))\b|how far( away| along)?( is| are)? (he|she|they|(?-i:[A-Z][a-z]+))\b|how far( away| along)?\s*\??$|how close( is| are)? (he|she|they|(?-i:[A-Z][a-z]+))?\b|(is|are|has|have) (he|she|they|(?-i:[A-Z][a-z]+)) (there|here|arrived|close|near|on (the|their|his|her) way)\b|\beta\b|how (long|much longer)( until| till| before)?\b|any update on (where|him|her|them)\b)|(他|她|supporter)?(在哪|到哪|到了嗎|到了没|多遠|多远|還要多久|还要多久|快到了嗎|什麼時候到)`)

// IsWhereQuestion reports a question about the supporter's whereabouts.
func IsWhereQuestion(text string) bool { return whereRE.MatchString(text) }

// answerWhere: the live status in words. handled=false when it cannot be
// answered here (no lookup, no single task, a lookup failure) — the usual
// routing then decides; unavailable → relayed, and said so.
func (r *Router) answerWhere(ctx context.Context, in Inbound, id Identity, conv Conversation, text string, active []ActiveTask) (bool, error) {
	if r.Live == nil || len(active) == 0 {
		return false, nil
	}
	t, ok := supporterNamed(active, text)
	if !ok {
		if len(active) != 1 {
			return false, nil // several and none named: the list, as usual
		}
		t = active[0]
	}
	st, err := r.Live.LiveStatus(ctx, id.UserID, t.ID)
	if err != nil {
		log.Printf("[channel] where identity=%s task=%s: %v", id.ID, t.ID, err)
		return false, nil
	}
	who := firstNonEmpty(DisplayName(FirstName(t.SupporterName)), "Your supporter")
	switch st.State {
	case LiveWorking:
		return true, r.Reply(ctx, in, fmt.Sprintf(CopyWhereWorking, who, t.Title, st.WorkedMinutes), t.ID)
	case LiveAtDoor:
		return true, r.Reply(ctx, in, fmt.Sprintf(CopyWhereAtDoor, who), t.ID)
	case LiveAlmostThere:
		return true, r.Reply(ctx, in, fmt.Sprintf(CopyWhereAlmost, who, distanceWords(st.DistanceMeters), agoWords(r.now(), st.LastSeen)), t.ID)
	case LiveOnTheWay:
		return true, r.Reply(ctx, in, fmt.Sprintf(CopyWhereOnTheWay, who, distanceWords(st.DistanceMeters), agoWords(r.now(), st.LastSeen)), t.ID)
	}
	if st.NotYetOnTheWay {
		// Nothing to show yet: the question goes to the supporter, and the
		// person is told so.
		sent, err := r.relaySent(ctx, in, id, t.ID, text)
		if err != nil {
			return true, err
		}
		if !sent {
			return true, r.Reply(ctx, in, fmt.Sprintf(CopyWhereNotYet, who), t.ID)
		}
		return true, r.Reply(ctx, in, fmt.Sprintf(CopyWhereNotYetAsked, who), t.ID)
	}
	// Unavailable: the phone stopped reporting — asked, and said so.
	sent, err := r.relaySent(ctx, in, id, t.ID, text)
	if err != nil {
		return true, err
	}
	if !sent {
		return true, r.Reply(ctx, in, fmt.Sprintf(CopyWhereUnavailable, who), t.ID)
	}
	return true, r.Reply(ctx, in, fmt.Sprintf(CopyWhereUnavailableAsked, who, agoWords(r.now(), st.LastSeen)), t.ID)
}

// distanceWords: "about 0.8 mi away", "about 2 blocks away", "a block away"
// — or "" when unknown.
func distanceWords(m *float64) string {
	if m == nil {
		return ""
	}
	meters := *m
	switch {
	case meters < 120:
		return " — a block away"
	case meters < 500:
		return fmt.Sprintf(" — about %d blocks away", int(meters/80+0.5))
	}
	miles := meters / 1609.344
	if miles < 10 {
		return fmt.Sprintf(" — about %.1f mi away", miles)
	}
	return fmt.Sprintf(" — about %d mi away", int(miles+0.5))
}

// agoWords: ", last seen just now" / ", last seen 3 min ago" — or "".
func agoWords(now, at time.Time) string {
	if at.IsZero() {
		return ""
	}
	m := int(now.Sub(at).Minutes())
	if m < 1 {
		return ", last seen just now"
	}
	return fmt.Sprintf(", last seen %d min ago", m)
}

var _ = strings.TrimSpace
