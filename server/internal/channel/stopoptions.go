package channel

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"hora-auth/internal/channel/llm"
)

// Place search while drafting (pilot 2026-11-17). "Hi I need a coffee at
// blue bottle, is a one close to 54 w?", then "I want a coffee at Blue
// Bottle but I don't know the address", then "Where are Blue Bottle in
// Manhattan": three "I couldn't find … exactly" and a hand-off. When the
// map cannot place a stop and the person named a business with no address
// for it — a chain, a shop, a café — Google Places is searched near the
// area they gave and the closest few are offered with their addresses, as
// discovery does. A pick fills the stop (the address is Google's, already
// checked); another address re-drafts as before. A question about where a
// place is never counts as a failed address answer.

const (
	// MaxStopOptions: options shown for one stop.
	MaxStopOptions = 3
	// stopSearchTimeout bounds the Places search after the guess.
	stopSearchTimeout = 6 * time.Second
)

const (
	CopyStopOptionsLead    = "Here are the %s locations I found near %s:"
	CopyStopOptionsLeadNY  = "Here are the %s locations I found:"
	CopyStopOptionsPick    = "Which one should your supporter go to? Reply with the number, or send another address."
	CopyStopOptionsPickOne = "Is that the one? Reply YES or 1, or send another address."
	// CopyStopOptionsAgain: the options asked again with nothing to show
	// (names are not kept, Google's policy).
	CopyStopOptionsAgain = "Reply with the number of one of the places above, or send the address you want."
)

// placeQuestionRE: the person asks where a place is, or for it to be found,
// instead of giving an address ("where are Blue Bottle in Manhattan", "is
// there one close to 54th?", "I don't know the address", "can you search
// Blue Bottle for me?").
var placeQuestionRE = regexp.MustCompile(`(?i)\bwhere(?:'s|’s| is| are| r)\b|\b(?:can|could|would) you (?:please |pls )?(?:search|find|look up|look for|check)\b|\b(?:search|look up)\b.{0,40}\bfor me\b|\b(?:don'?t|do not|dont)\s+know\s+(?:the\s+|their\s+|its\s+|it'?s\s+|what\s+the\s+)?address\b|\bnot sure (?:of |about |what )?(?:the |their )?address\b|\bno idea (?:of |about |what )?(?:the |their )?address\b|\bis (?:there )?(?:a |an )?(?:one|location|branch|store|shop)\s+(?:close|near|nearby|around)\b|\b(?:closest|nearest)\b`)

// IsPlaceQuestion: the words ask where a place is.
func IsPlaceQuestion(text string) bool {
	return placeQuestionRE.MatchString(strings.NewReplacer("’", "'").Replace(text))
}

// stopProposal: what the model read for a stop the map could not place —
// the address to confirm, or the places found for the business named.
type stopProposal struct {
	address string
	options []llm.Place
	place   string
	near    string
}

// proposeStop asks the model for the stop the person meant: the business
// named with no address (searched on Google near the area they gave), else
// the most likely full address (checked by Google, confirmed with YES).
func (r *Router) proposeStop(ctx context.Context, id Identity, source, stop, area string, rejected []string, items []DraftItem) stopProposal {
	if r.Addresses == nil || r.Booker == nil || strings.TrimSpace(stop) == "" {
		return stopProposal{}
	}
	sg, ok := r.Addresses.(llm.StopGuesser)
	if !ok {
		return stopProposal{address: r.proposeAddress(ctx, id, source, stop, area, rejected)}
	}
	gctx, cancel := context.WithTimeout(ctx, addressGuessTimeout)
	g, err := sg.GuessStop(gctx, strings.Split(source, "\n"), stop, area)
	cancel()
	if err != nil {
		log.Printf("[channel] address guess identity=%s: %v", id.ID, err)
		return stopProposal{}
	}
	// A drop-off is where things are brought, never a business to look
	// for: the café named for the pickup must not come back as one
	// (address eval 2026-11-17: "Bond street 200 nyc" as the drop-off was
	// read as Blue Bottle near Bond St).
	if g.Place != "" && r.Places != nil && !isDropOffStop(items, stop) {
		near := firstNonEmpty(firstNonEmpty(g.Near, area), otherStop(items, stop))
		if near == "" {
			near = r.lastAddress(ctx, id)
		}
		if opts := r.searchStop(ctx, id, g.Place, near); len(opts) > 0 {
			log.Printf("[channel] stop options identity=%s: %d found", id.ID, len(opts))
			return stopProposal{options: opts, place: g.Place, near: near}
		}
	}
	return stopProposal{address: r.checkGuess(ctx, id, g.Address, stop, rejected)}
}

// otherStop: the draft's stop that is not the unclear one (the drop-off of
// a pickup), "" none — the area a place is looked for near when the person
// gave none.
func otherStop(items []DraftItem, unclear string) string {
	k := stopKey(unclear)
	for _, it := range items {
		for _, s := range []string{it.Draft.Location2, it.Draft.Location1} {
			if strings.TrimSpace(s) != "" && stopKey(s) != k && hasDigitRE.MatchString(s) {
				return s
			}
		}
	}
	return ""
}

// searchStop: up to MaxStopOptions places in NYC for the place named, near
// the area. One Places call, counted against the person's daily searches
// when discovery keeps the count.
func (r *Router) searchStop(ctx context.Context, id Identity, place, near string) []llm.Place {
	if r.DiscoveryStore != nil {
		day := r.now().In(r.loc()).Format("2006-01-02")
		ok, err := r.DiscoveryStore.TakePlacesCall(ctx, id.ID, day, r.limits().PerDay)
		if err != nil || !ok {
			log.Printf("[channel] stop search identity=%s: no search left (err=%v)", id.ID, err)
			return nil
		}
	}
	q := place
	if near != "" {
		q += " near " + near
	}
	if !strings.Contains(strings.ToLower(q), "new york") && !strings.Contains(strings.ToLower(q), "nyc") {
		q += ", New York, NY"
	}
	sctx, cancel := context.WithTimeout(ctx, stopSearchTimeout)
	defer cancel()
	// Near the area named: searched around its point, then ordered by
	// distance from it, closest first; farther than llm.NearMiles is
	// dropped unless nothing is closer (2026-11-17).
	lat, lng, located := 0.0, 0.0, false
	if near != "" && !llm.IsBorough(near) {
		lat, lng, located = llm.LocateArea(sctx, r.Places, near)
	}
	var found []llm.Place
	var err error
	if ns, ok := r.Places.(llm.NearSearcher); ok && located {
		found, err = ns.SearchPlacesNear(sctx, place, false, lat, lng, llm.NearMiles*1609.34)
	} else {
		found, err = r.Places.SearchPlaces(sctx, q, false)
	}
	if err != nil {
		log.Printf("[channel] stop search identity=%s: %v", id.ID, err)
		return nil
	}
	if located {
		found = llm.SortNear(found, lat, lng, llm.NearMiles)
	}
	var out []llm.Place
	for _, p := range found {
		if !p.InNYC || p.ID == "" || strings.TrimSpace(firstNonEmpty(p.FullAddress, p.Address)) == "" {
			continue
		}
		out = append(out, p)
		if len(out) == MaxStopOptions {
			break
		}
	}
	return out
}

// renderStopOptions: the places, from Google's data, and how to pick one.
func renderStopOptions(place, near string, opts []llm.Place) string {
	var b strings.Builder
	if near != "" {
		fmt.Fprintf(&b, CopyStopOptionsLead, place, near)
	} else {
		fmt.Fprintf(&b, CopyStopOptionsLeadNY, place)
	}
	b.WriteString("\n")
	for i, p := range opts {
		fmt.Fprintf(&b, "\n%d. %s\n   %s", i+1, p.Name, placeFacts(p))
	}
	b.WriteString("\n\n" + CopyDiscoveryFooter + "\n")
	if len(opts) == 1 {
		b.WriteString(CopyStopOptionsPickOne)
	} else {
		b.WriteString(CopyStopOptionsPick)
	}
	return b.String()
}

// pickStopOption reads an answer to the options: a number (or YES to a
// single one) fills the stop with that place's address. handled=false: not
// a pick — the words are read as before (an address re-drafts).
func (r *Router) pickStopOption(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) (bool, error) {
	p := conv.Pending
	n := len(p.StopOptions)
	k := pickNumber(text, n)
	if k == 0 && n == 1 && IsYes(text) {
		k = 1
	}
	if k == 0 {
		return false, nil
	}
	if r.Places == nil {
		return true, r.Reply(ctx, in, CopyPlaceLookupDown, "")
	}
	dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	pl, err := r.Places.PlaceDetails(dctx, p.StopOptions[k-1])
	cancel()
	if err != nil {
		log.Printf("[channel] stop option identity=%s: details: %v", id.ID, err)
		return true, r.Reply(ctx, in, CopyPlaceLookupDown, "")
	}
	addr := strings.TrimSpace(firstNonEmpty(pl.FullAddress, pl.Address))
	if !pl.InNYC || addr == "" {
		return true, r.Reply(ctx, in, CopyOutOfArea, "")
	}
	q := *p
	q.StopOptions, q.AddressGuess = nil, addr
	if len(q.Places) < MaxStopOptions {
		// The supporter's description and the summary name the place.
		q.Places = append(append([]ChosenPlace{}, q.Places...), ChosenPlace{ID: p.StopOptions[k-1], Name: pl.Name, Address: addr})
	}
	conv.Pending = &q
	log.Printf("[channel] stop option identity=%s: option %d of %d", id.ID, k, n)
	return true, r.acceptAddressGuess(ctx, in, id, conv)
}

// checkGuess: the model's full address, checked with Google ("" when it
// is none, the stop itself, outside, or one the person turned down).
func (r *Router) checkGuess(ctx context.Context, id Identity, g, stop string, rejected []string) string {
	if g == "" || stopKey(g) == stopKey(stop) {
		return ""
	}
	got := r.Booker.CheckArea(ctx, llm.TaskDraft{Location1: g})
	if got.Outcome != AreaInside || len(got.Resolved) != 1 || strings.TrimSpace(got.Resolved[0]) == "" {
		log.Printf("[channel] address guess identity=%s: not placed (%s)", id.ID, areaOutcomeName(got.Outcome))
		return ""
	}
	found := got.Resolved[0]
	for _, x := range rejected {
		if stopKey(x) == stopKey(found) || stopKey(x) == stopKey(g) {
			return ""
		}
	}
	log.Printf("[channel] address guess identity=%s: placed, asking to confirm", id.ID)
	return found
}

// isDropOffStop: stop is an errand's drop-off after a pickup.
func isDropOffStop(items []DraftItem, stop string) bool {
	k := stopKey(stop)
	for _, it := range items {
		if strings.TrimSpace(it.Draft.Location1) != "" && stopKey(it.Draft.Location2) == k {
			return true
		}
	}
	return false
}
