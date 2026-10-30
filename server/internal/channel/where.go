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

	"golang.org/x/text/unicode/norm"
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
	// and has never clocked in — nothing has happened yet.
	NotYetOnTheWay bool
	// SetOffNoFix: "On my way" tapped, no position received yet.
	SetOffNoFix bool
}

// LiveLookup reads the live status of an active task for its requester.
type LiveLookup interface {
	LiveStatus(ctx context.Context, userID, taskID string) (LiveStatus, error)
}

// whereRE: a question about where the supporter is or how far along —
// the whole message, give or take a greeting and a question mark, so a
// message that also carries an instruction ("I'll be late, where is he?",
// "tell Sam to ignore the ETA") is relayed as before (review r1 #2).
var whereRE = regexp.MustCompile(`(?i)^[\s,.!?¿¡]*(?:hi|hey|hello|ok|so|also|and|btw|sorry|hola|ciao|salut)?[\s,]*(?:` +
	`where (?:is|are|'s|s|r) (?:he|she|they|you|u|my supporter|the supporter|(?-i:[A-Z][a-z]+))(?: now| right now| at)?` +
	`|where'?s (?:he|she|my supporter|the supporter|(?-i:[A-Z][a-z]+))(?: now| at)?` +
	`|how far(?: away| along| out)?(?: is| are)?(?: (?:he|she|they|you|my supporter|(?-i:[A-Z][a-z]+)))?(?: now)?` +
	`|how close(?: is| are) (?:he|she|they|you|my supporter|(?-i:[A-Z][a-z]+))` +
	`|(?:is|are|has|have) (?:he|she|they|my supporter|(?-i:[A-Z][a-z]+)) (?:there|here|arrived|close|nearby|on (?:the|their|his|her) way)(?: yet)?` +
	// "are you there?" asks whether they are answering, not where they
	// are (review): the second person only with a whereabouts predicate.
	`|(?:are|have) you (?:arrived|close|nearby|on (?:the|your) way)(?: yet)?` +
	`|(?:what'?s the |what is the )?eta` +
	`|how (?:long|much longer) (?:until|till|before) (?:he|she|they|you|my supporter|(?-i:[A-Z][a-z]+)) (?:get|gets|arrive|arrives|is|are)(?: here| there)?` +
	`|any update on (?:where (?:he|she|they) (?:is|are)|his|her|their) (?:location|whereabouts|eta)?` +
	`|how much longer` +
	`|(?:他|她|你|supporter|(?-i:[A-Z][a-z]+))?(?:現在|现在)?(?:在哪|在哪裡|在哪里|到哪|到哪裡|到了嗎|到了没|到了沒|多遠|多远|還要多久|还要多久|快到了嗎|什麼時候到|什么时候到)` +
	`)[\s?？!！。~～.]*$`)

// whereAskedRE: Arbiter 2026-10-30 — other languages too (Spanish,
// French, Italian, Japanese, Korean). Each a whole-message whereabouts
// question about a PERSON, and only with a question mark (review r1, r2):
// without one, many are statements ("está cerca"); and a form without a
// person in it ("¿dónde está?", "어디예요?", "¿llegó?", "quanto manca?")
// can be about a thing — the parcel, the keys — or how long the task
// takes. So only second person ("estás", "tu", "sei", honorific Korean),
// a pronoun for a person (él/ella, lui/lei, 彼/彼女), a supporter's name,
// or a verb for an animate being (いる). Everything else goes to the
// model, which answers AND relays.
var whereAskedRE = regexp.MustCompile(`(?i)^[\s,.!¿¡]*(?:hola|ciao|salut|bonjour|buongiorno)?[\s,]*(?:` +
	`(?:d[oó]nde|a d[oó]nde) (?:est[aá]s|andas)(?: ahora)?` +
	`|(?:d[oó]nde|a d[oó]nde) (?:est[aá]|anda) (?:[eé]l|ella|mi supporter|(?-i:[A-Z][a-z]+))(?: ahora)?` +
	`|(?:ya )?llegaste` +
	`|cu[aá]nto te falta` +
	`|est[aá]s (?:cerca|lejos)` +
	`|o[uù] es[- ]tu(?: maintenant)?|tu es o[uù]` +
	`|o[uù] est (?:mon supporter|(?-i:[A-Z][a-z]+))(?: maintenant)?` +
	`|tu arrives quand` +
	`|dove sei(?: adesso| ora)?` +
	`|dov'[eè] (?:lui|lei|(?-i:[A-Z][a-z]+))(?: adesso| ora)?` +
	`|quanto ti manca` +
	`|sei (?:vicino|vicina|arrivato|arrivata)` +
	`|(?:彼|彼女)(?:は)?(?:今)?どこ(?:にいる|にいますか|ですか)?(?:の)?|(?:今)?どこにいる(?:の)?|(?:今)?どこにいますか` +
	`|(?:지금 )?어디(?:세요|쯤이에요|쯤이세요|\s?계세요)|도착하셨(?:어요|나요)` +
	`)\s*[?？][\s?？!！~～.]*$`)

// IsWhereQuestion reports a question about the supporter's whereabouts.
func IsWhereQuestion(text string) bool {
	t := whereNorm(text)
	return whereRE.MatchString(t) || whereAskedRE.MatchString(t)
}

// whereNorm: NFKC (fullwidth Latin, "Ｋｅｙｓ" → "Keys") and straight
// apostrophes (iMessage's curly one), trimmed. Every where check reads
// the text through it FIRST, so a name guard and the grammar see the same
// letters (review r2 #1).
func whereNorm(text string) string {
	t := strings.TrimSpace(norm.NFKC.String(text))
	return strings.NewReplacer("\u2019", "'", "\u2018", "'").Replace(t)
}

// whereNameRE: the capitalised name the question used, if any.
var whereNameRE = regexp.MustCompile(`(?:where (?:is|are|'s|s) |where'?s |how far(?: away| along| out)?(?: is| are)? |how close (?:is|are) |(?:is|are|has|have) |(?:until|till|before) )([A-Z][a-z]+)\b`)

// whereRelayCooldown: a question relayed while the supporter's phone is
// silent is not relayed again within this long (review r1 #7).
const whereRelayCooldown = 2 * time.Minute

// answerWhere: the live status in words. handled=false when it cannot be
// answered here (no lookup, no single task, a lookup failure) — the usual
// routing then decides; unavailable → relayed, and said so.
func (r *Router) answerWhere(ctx context.Context, in Inbound, id Identity, conv Conversation, text string, active []ActiveTask) (bool, error) {
	if r.Live == nil || len(active) == 0 {
		return false, nil
	}
	// The rule's name slot takes any capitalised word; only a supporter's
	// name may fill it — "Keys在哪" is a question about the keys (review
	// r5 #1). Every other capitalised word lowered, it must still be the
	// rule's question.
	text = whereNorm(text)
	if !IsWhereQuestion(lowerNonSupporterCaps(text, active)) {
		return false, nil
	}
	t, named := supporterNamed(active, text)
	if !named {
		if m := whereNameRE.FindStringSubmatch(text); m != nil && !strings.EqualFold(m[1], "he") {
			// A name that is no supporter's ("Where is Starbucks?"): not
			// about the supporter (review r1 #3).
			return false, nil
		}
		if len(active) != 1 {
			return r.answerWhereEach(ctx, in, id, active)
		}
		t = active[0]
	}
	return r.answerWhereFor(ctx, in, id, text, t)
}

// answerWhereIn answers for a scope already decided (the agent's path,
// whereScope): one task → its supporter, several → one line each. Names
// are not looked for again — whereScope's rules for names that are also
// words would be undone by supporterNamed's (review r3 #3).
func (r *Router) answerWhereIn(ctx context.Context, in Inbound, id Identity, text string, scope []ActiveTask) (bool, error) {
	if r.Live == nil || len(scope) == 0 {
		return false, nil
	}
	if len(scope) != 1 {
		return r.answerWhereEach(ctx, in, id, scope)
	}
	return r.answerWhereFor(ctx, in, id, text, scope[0])
}

// answerWhereFor: one supporter's status, or — phone silent — the question
// relayed to them, once per cooldown, saying so.
func (r *Router) answerWhereFor(ctx context.Context, in Inbound, id Identity, text string, t ActiveTask) (bool, error) {
	line, relay, err := r.whereLine(ctx, id, t)
	if err != nil {
		log.Printf("[channel] where identity=%s task=%s: %v", id.ID, t.ID, err)
		return false, nil
	}
	if !relay {
		return true, r.Reply(ctx, in, line, t.ID)
	}
	// The phone is silent (or they have not set off): the question goes to
	// the supporter — once per whereRelayCooldown — and the person is told.
	if since, ok := r.whereRelayedSince(id.ID, t.ID); ok && since < whereRelayCooldown {
		return true, r.Reply(ctx, in, line+fmt.Sprintf(CopyWhereAskedAlready, int(since.Minutes())), t.ID)
	}
	sent, err := r.relaySent(ctx, in, id, t.ID, text)
	if err != nil || !sent {
		return true, err // relaySent said what went wrong (review r1 #1)
	}
	r.noteWhereRelayed(id.ID, t.ID)
	return true, r.Reply(ctx, in, line+CopyWhereAsked, t.ID)
}

// answerWhereEach: several active tasks and no name — all of them are the
// person's own, so each supporter's status, one line each (no relay here).
func (r *Router) answerWhereEach(ctx context.Context, in Inbound, id Identity, active []ActiveTask) (bool, error) {
	var lines []string
	for _, t := range active {
		line, relay, err := r.whereLine(ctx, id, t)
		if err != nil {
			log.Printf("[channel] where identity=%s task=%s: %v", id.ID, t.ID, err)
			return false, nil
		}
		if relay {
			line += CopyWhereAskByName
		}
		lines = append(lines, line)
	}
	return true, r.Reply(ctx, in, strings.Join(lines, "\n"), "")
}

// whereLine: the status in words, and whether the question should be
// relayed instead (phone silent / not set off).
func (r *Router) whereLine(ctx context.Context, id Identity, t ActiveTask) (line string, relay bool, err error) {
	st, err := r.Live.LiveStatus(ctx, id.UserID, t.ID)
	if err != nil {
		return "", false, err
	}
	who := firstNonEmpty(DisplayName(FirstName(t.SupporterName)), "Your supporter")
	switch st.State {
	case LiveWorking:
		return fmt.Sprintf(CopyWhereWorking, who, t.Title, st.WorkedMinutes), false, nil
	case LiveAtDoor:
		return fmt.Sprintf(CopyWhereAtDoor, who), false, nil
	case LiveAlmostThere:
		return fmt.Sprintf(CopyWhereAlmost, who, distanceWords(st.DistanceMeters), agoWords(r.now(), st.LastSeen)), false, nil
	case LiveOnTheWay:
		return fmt.Sprintf(CopyWhereOnTheWay, who, distanceWords(st.DistanceMeters), agoWords(r.now(), st.LastSeen)), false, nil
	}
	if st.SetOffNoFix {
		// "On my way" tapped, no position yet (the first seconds): not a
		// silent phone — said, not relayed (review r1 #5).
		return fmt.Sprintf(CopyWhereSetOff, who), false, nil
	}
	if st.NotYetOnTheWay {
		return fmt.Sprintf(CopyWhereNotYetLine, who), true, nil
	}
	return fmt.Sprintf(CopyWhereUnavailableLine, who, agoWords(r.now(), st.LastSeen)), true, nil
}

// whereRelayedSince / noteWhereRelayed: the in-memory cooldown per
// (identity, task). Per instance — good enough for a courtesy.
func (r *Router) whereRelayedSince(identityID, taskID string) (time.Duration, bool) {
	r.whereMu.Lock()
	defer r.whereMu.Unlock()
	at, ok := r.whereRelayed[identityID+"|"+taskID]
	if !ok {
		return 0, false
	}
	return r.now().Sub(at), true
}

func (r *Router) noteWhereRelayed(identityID, taskID string) {
	r.whereMu.Lock()
	defer r.whereMu.Unlock()
	if r.whereRelayed == nil {
		r.whereRelayed = map[string]time.Time{}
	}
	r.whereRelayed[identityID+"|"+taskID] = r.now()
}

// distanceWords: " — about 0.8 mi away", " — about 2 blocks away", " — a
// block away" — or "" when unknown.
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

// agoWords: ", last seen just now" / "3 min ago" / "2 h ago" / "3 days
// ago" — or "".
func agoWords(now, at time.Time) string {
	if at.IsZero() {
		return ""
	}
	d := now.Sub(at)
	switch {
	case d < time.Minute:
		return ", last seen just now"
	case d < 90*time.Minute:
		return fmt.Sprintf(", last seen %d min ago", int(d.Minutes()))
	case d < 36*time.Hour:
		return fmt.Sprintf(", last seen %d h ago", int(d.Hours()+0.5))
	}
	return fmt.Sprintf(", last seen %d days ago", int(d.Hours()/24+0.5))
}

// lowerNonSupporterCaps lowers every capitalised ASCII word that is not
// exactly an active supporter's display name.
func lowerNonSupporterCaps(text string, active []ActiveTask) string {
	supporters := map[string]bool{}
	for _, a := range active {
		if n := DisplayName(FirstName(a.SupporterName)); n != "" {
			supporters[n] = true
		}
	}
	return whereCapRE.ReplaceAllStringFunc(text, func(w string) string {
		if supporters[w] {
			return w
		}
		return strings.ToLower(w)
	})
}
