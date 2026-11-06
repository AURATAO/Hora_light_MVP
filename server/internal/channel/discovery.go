package channel

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"hora-auth/internal/channel/llm"
)

// Place discovery (D-23, phase 1). A customer asks HO:RA to find something
// in NYC — a place for an errand, what is open now, where to get an item, a
// gift, someone to go and check — and the answer leads to a normal booking.
//
// The model (llm.PlaceDiscovery: Sonnet with a Google Places tool) only
// proposes; the rules here decide:
//
//   - An "info" reply lists places from Google's data, rendered here — the
//     model never writes a name, an address, a rating or hours. Its one
//     line of lead text is checked (InfoLeadOK): no price, no question, no
//     time promise; otherwise fixed text is used. An info reply never sets
//     up a YES: it ends with "reply with a number".
//   - Choosing an option ("2", "the second one", "Joe's") starts a normal
//     draft through startDraftOpts: area check, summary, YES and the budget
//     rules unchanged. The chosen place is attached to the errand by rule
//     (applyPlaces), never through the parser's words, so a business's name
//     can never become a money input (ownwords.go).
//   - Only place ids are stored (Google's caching policy): a chosen place
//     is looked up again before the draft, and a pick by name is matched
//     against names looked up again by id.
//   - Anything that fails — Anthropic, Places, a cap — is fixed copy.

// ChosenPlace is a place picked from a discovery answer, as attached to the
// draft: Google's name and full address at the time of the choice.
type ChosenPlace struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// DiscoveryOffer is what the last discovery answer left open on a thread.
// Place ids only (Google's policy): names are never stored.
type DiscoveryOffer struct {
	Kind string `json:"kind"` // offerOptions | offerAskLocation | offerRouteAsk
	// Words: the person's own words for this search, oldest first.
	Words []string `json:"words"`
	// Options: the place ids of each option shown, in visiting order.
	Options [][]string `json:"options,omitempty"`
	Check   bool       `json:"check,omitempty"`
	AtUnix  int64      `json:"at"`
	// Suggest: the option an answer about the options proposed ("Want me
	// to set that up at Nolita Pizza?"): a YES starts its draft (a summary
	// and its own YES still come before any booking). SuggestWords go with
	// it to the parser ("Get their most popular pizza."). 0 none.
	Suggest      int    `json:"suggest,omitempty"`
	SuggestWords string `json:"suggest_words,omitempty"`
	// SuggestItems: what an answer from sources offered the supporter to
	// get ("Margherita", "spicy vodka slice") — the person's order once they
	// say YES; item names only, never Google's text (D-25). SuggestCheck /
	// SuggestAsk: the offer was to go and check, or ask there.
	SuggestItems []string `json:"suggest_items,omitempty"`
	SuggestCheck bool     `json:"suggest_check,omitempty"`
	SuggestAsk   string   `json:"suggest_ask,omitempty"`
	// Playbook: the errand type discovery recognised, "" none.
	Playbook string `json:"playbook,omitempty"`
}

const (
	offerOptions     = "options"
	offerAskLocation = "ask_location"
	// offerRouteAsk: several errands with no place were asked "find places
	// for these, or do you have the addresses?" (CopyRouteAsk). Words holds
	// the errands.
	offerRouteAsk = "route_ask"
	// How long an answer can still be picked from / a "Where should I
	// look?" still answered.
	offerOptionsTTL  = 2 * time.Hour
	offerLocationTTL = 30 * time.Minute
)

// DiscoveryStore keeps the open offer and the per-person daily count of
// Places calls (migration 20261104170000_channel_discovery.sql).
type DiscoveryStore interface {
	GetDiscovery(ctx context.Context, identityID string) (*DiscoveryOffer, error)
	// SaveDiscovery replaces the offer; nil clears it.
	SaveDiscovery(ctx context.Context, identityID string, o *DiscoveryOffer) error
	// TakePlacesCall counts one Places search for identityID on day (New
	// York date) and reports whether it was within perDay.
	TakePlacesCall(ctx context.Context, identityID, day string, perDay int) (bool, error)
	// PlacesCallsLeft reads the calls left on day without counting one.
	PlacesCallsLeft(ctx context.Context, identityID, day string, perDay int) (int, error)
}

// PastTask is one of the person's own earlier errands (the usual).
type PastTask struct {
	Title, Category, Description string
	Location1, Location2         string
	Minutes                      int
}

// TaskHistory reads a requester's own past errands, newest first.
type TaskHistory interface {
	PastTasks(ctx context.Context, userID string, n int) ([]PastTask, error)
}

// DiscoveryLimits caps the cost of discovery (env, DiscoveryLimitsFromEnv).
type DiscoveryLimits struct {
	PerMessage int           // Places searches per message
	PerDay     int           // Places searches per person per New York day
	Timeout    time.Duration // one discovery turn, inside the message's 60 s
}

// DiscoveryLimitsFromEnv:
//
//	CHANNEL_PLACES_PER_MESSAGE   default 3
//	CHANNEL_PLACES_PER_DAY       default 25
//	CHANNEL_DISCOVERY_TIMEOUT    default 30s (a Go duration)
func DiscoveryLimitsFromEnv() DiscoveryLimits {
	l := DiscoveryLimits{PerMessage: 3, PerDay: 25, Timeout: 30 * time.Second}
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CHANNEL_PLACES_PER_MESSAGE"))); err == nil && n > 0 && n <= 6 {
		l.PerMessage = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CHANNEL_PLACES_PER_DAY"))); err == nil && n > 0 {
		l.PerDay = n
	}
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv("CHANNEL_DISCOVERY_TIMEOUT"))); err == nil && d >= 5*time.Second && d <= 45*time.Second {
		l.Timeout = d
	}
	return l
}

func (r *Router) limits() DiscoveryLimits {
	l := r.DiscoveryLimits
	if l.PerMessage <= 0 {
		l.PerMessage = 3
	}
	if l.PerDay <= 0 {
		l.PerDay = 25
	}
	if l.Timeout <= 0 {
		l.Timeout = 30 * time.Second
	}
	return l
}

func (r *Router) discoveryOn() bool { return r.Discovery != nil && r.DiscoveryStore != nil }

// Copy: discovery. Every line here is fixed; only infoLead is the model's.
const (
	// The attribution Google's policy asks for where there is no map: the
	// text "Google Maps", unchanged, near the content and set apart from it.
	CopyDiscoveryFooter    = "Places, ratings and hours from Google Maps. Hours can change."
	CopyDiscoveryPick      = "Reply with a number and I'll set up the errand for a supporter."
	CopyDiscoveryPickOne   = "Reply 1 and I'll set up the errand for a supporter."
	CopyDiscoveryPickCheck = "Reply with a number and I'll send a supporter to check and text you what they find."
	CopyDiscoveryLead      = "Here's what I found:"
	CopyDiscoveryLeadCheck = "Here's where I can send someone to check:"
	CopyDiscoveryNothing   = "I couldn't find a good match for that. Tell me the store or address and I'll send someone."
	CopySearchDown         = "I can't search right now, but tell me the store or address and I'll send someone."
	CopySearchLimit        = "I've looked up a lot of places for you today, so I can't search more right now. Tell me the store or address and I'll send someone."
	CopyAskLocation        = "Where should I look? Send an address, cross streets or a neighborhood in NYC."
	CopyAskLocationLast    = "Where should I look? Send an address, cross streets or a neighborhood in NYC, or reply LAST to look near %s (your last errand)."
	CopyPlaceLookupDown    = "I can't look that place up right now. Send me its name and address and I'll set up the errand."
	CopyUsualNone          = "I don't see an earlier errand from you yet. Tell me what you need and I'll set it up."
	CopyUsualIntro         = "Same as last time, as a new booking:"
	CopyPlacesLineOne      = "Place: %s (from Google Maps)"
	CopyPlacesLineRoute    = "Stops, in order (from Google Maps): %s"
	CopyCheckLine          = "Your supporter goes, checks, and texts you what they find, with a photo."
	descriptionPlaceOne    = "Place (from Google Maps): %s, %s"
	descriptionPlaceRoute  = "Stops in order (from Google Maps):"
	descriptionCheck       = "Go and check, then text the requester what you find, with a photo. Buy only what they asked for, within the budget they set."
	CopyLookingUp          = "Looking that up…"
	// Answers about the options shown (llm.DiscoverAbout). Only Google's
	// data: no menu, no dish, no price is ever named.
	CopyAboutPick            = "Going by Google Maps ratings, I'd go with %s. I don't have their menus."
	CopyAboutMenuItem        = "I don't have %s's menu, but I can ask your supporter to get their most popular %s."
	CopyAboutMenu            = "I don't have %s's menu, but I can ask your supporter to find out what's most popular there and get that."
	CopyAboutDelivery        = "I can't tell from Google Maps whether %s delivers, but a supporter can pick it up and bring it to you."
	CopyAboutOther           = "I can't tell that from Google Maps, but your supporter can ask at %s and text you what they find."
	CopyAboutAsk             = "Want me to set that up at %s?"
	CopyAboutAskOr           = "Want me to set that up at %s? Or reply with another number."
	CopyAboutNoData          = "I don't have more on these than Google Maps shows, and I don't have their menus. Your supporter can ask there and text you. " + CopyDiscoveryPick
	CopyPickAsked            = "I don't have %s's menu, so I've added your question as a note for your supporter to ask there."
	suggestMenuItem          = "Get their most popular %s."
	suggestMenu              = "Ask what's most popular there and get that."
	CopyBookingStillWaiting  = "Your earlier booking is still waiting."
	CopyPickWhileDraft       = "Your earlier booking is still waiting. Reply YES to book it or NO to drop it, then send your pick again."
	CopyRouteAsk             = "Want me to find places for these on one route, or do you have the addresses?"
	routeFindText            = "Find a place for each of these errands, on one route."
	copyOutOfScopeGeneral    = "I'm HO:RA's errand concierge, so I can't help with that one. I can find places in NYC and send a real person to run the errand: laundry, pickups, returns, gifts, waiting in line and more."
	copyOutOfScopeMedLegal   = "I can't give medical or legal advice. If it's an emergency, call 911. I can send someone to a pharmacy for over-the-counter items or to drop off documents, though."
	copyOutOfScopeAlcohol    = "Supporters can't buy alcohol, tobacco, vapes or cannabis. I can send someone for groceries, snacks, a gift or a pickup instead."
	copyOutOfScopeRx         = "Supporters can't pick up prescriptions. I can send someone to a pharmacy for over-the-counter items, though."
	copyOutOfScopeAccounts   = "Supporters can't log into your accounts or make reservations for you. I can send someone to wait in line, pick something up or drop something off."
	copyOutOfScopePriceGuard = "I can't promise a store's prices. Your supporter pays what the receipt says, up to the shopping budget you set. Tell me what you need and I'll find a place."
)

func outOfScopeCopy(reason string) string {
	switch reason {
	case "medical_legal":
		return copyOutOfScopeMedLegal
	case "alcohol_tobacco":
		return copyOutOfScopeAlcohol
	case "prescription":
		return copyOutOfScopeRx
	case "accounts_reservations":
		return copyOutOfScopeAccounts
	case "price_guarantee":
		return copyOutOfScopePriceGuard
	}
	return copyOutOfScopeGeneral
}

// discoveryRE: words that ask HO:RA to find, plan or check — a strong
// reading by rule (a model or Places failure then gets CopySearchDown, not
// the generic answer). Weaker wordings reach discovery through the
// classifier's "question".
var discoveryRE = regexp.MustCompile(`(?i)\b(?:where (?:can|could|do|should|would) (?:i|we|you|someone|somebody)|where(?:'s| is) (?:the |a )?(?:nearest|closest|best|good)|find (?:me |us )?(?:a|an|some|the)|(?:any|a) good .{1,30} (?:near|around|in)|nearest|closest|near me|nearby|around here|open (?:now|late|till|until|at|tonight|24)|still open|24 ?(?:hours?|hr|/7)|recommend|suggest(?:ion)?|plan (?:my|a|the|some)|in one trip|on (?:the|one) route|best route|does .{1,40} (?:still )?(?:have|carry|sell)|how long is the line|how busy is|line at|(?:go |someone to |send someone to )check (?:if|whether|on|the)|drop (?:off )?(?:an?|my) (?:amazon |ups |fedex )?return)\b|附近|哪裡可以|哪里可以`)

// LooksLikeDiscovery: the words ask HO:RA to find, plan or check.
func LooksLikeDiscovery(text string) bool { return discoveryRE.MatchString(text) }

// usualRE: the person asks for a past errand again.
var usualRE = regexp.MustCompile(`(?i)\b(?:the usual|my usual|usual (?:errand|order|one|please)|same (?:thing |errand |order )?as (?:last time|before|usual|last week)|same again|same as always|(?:repeat|redo|rebook) (?:my |the )?last (?:one|errand|task|order|booking)|do (?:it|that) again)\b|老樣子|老样子|跟上次一樣|跟上次一样`)

// IsUsualRequest: "the usual", "same as last time".
func IsUsualRequest(text string) bool { return usualRE.MatchString(text) }

// routeDiscoveryFirst runs in idle before the supporter routing: "the
// usual", a pick from the options just shown, or the answer to "Where
// should I look?". With options open, anything else is read by discovery
// before the classifier — a question about the options ("which one should
// I pick?") is answered and the options stay open; a new errand comes back
// not_discovery and takes the usual routing (production 2026-11-06: "No I
// was asking which pizza should I choose" was drafted as an errand and the
// options were lost). handled=false: the usual routing; tried: discovery
// already read this message (it is not asked twice).
func (r *Router) routeDiscoveryFirst(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) (handled, tried bool, err error) {
	if !r.discoveryOn() {
		return false, false, nil
	}
	if IsUsualRequest(text) {
		return true, true, r.startUsual(ctx, in, id, conv, -1)
	}
	offer := r.freshOffer(ctx, id)
	if offer == nil {
		return false, false, nil
	}
	at := time.Unix(offer.AtUnix, 0)
	if r.supporterSpokeSince(ctx, in, id, &at) {
		// A supporter wrote after our answer: "2" may be theirs. The usual
		// routing (and the model, which sees both) decides.
		return false, false, nil
	}
	switch offer.Kind {
	case offerOptions:
		p := r.pickFromOffer(ctx, id, text, offer)
		switch {
		case p.k > 0 && p.question != "" && r.answersOn():
			// "Ok nolita pizza, what is the best flavor?": answered from
			// sources (D-25) — the model reads which question it is.
			if p.facts != nil {
				ctx = context.WithValue(ctx, factsPrefetchKey{}, p.facts)
			}
			handled, err := r.discover(ctx, in, id, conv, text, offer, false, false)
			if handled {
				return true, true, err
			}
			return true, true, r.chooseOptionWith(ctx, in, id, conv, offer, p.k, p.details, []string{p.question}, fmt.Sprintf(CopyPickAsked, p.name))
		case p.k > 0 && p.question != "":
			// "Ok nolita pizza, what is the best flavor?": that option,
			// with the question kept as a note for the supporter (no menu
			// here to answer it from).
			return true, true, r.chooseOptionWith(ctx, in, id, conv, offer, p.k, p.details, []string{p.question}, fmt.Sprintf(CopyPickAsked, p.name))
		case p.k > 0:
			return true, true, r.chooseOption(ctx, in, id, conv, offer, p.k, p.details)
		}
		if stripFiller(text) == "" {
			return false, false, nil
		}
		if p.looked != nil {
			// Looked up for the name match: an answer about the options
			// uses the same lookups, not a second set.
			ctx = context.WithValue(ctx, aboutPrefetchKey{}, p.looked)
		}
		if p.facts != nil {
			ctx = context.WithValue(ctx, factsPrefetchKey{}, p.facts)
		}
		handled, err := r.discover(ctx, in, id, conv, text, offer, false, false)
		return handled, true, err
	case offerAskLocation:
		if lastWordRE.MatchString(text) {
			if addr := r.lastAddress(ctx, id); addr != "" {
				text = "near " + addr
			}
		}
		handled, err := r.discover(ctx, in, id, conv, text, offer, true, false)
		return handled, true, err
	case offerRouteAsk:
		handled, err := r.answerRouteAsk(ctx, in, id, conv, text, offer)
		return handled, true, err
	}
	return false, false, nil
}

// offerPick: a pick among the open offer's options, read by rule. details:
// the person's other words for the errand ("" none); question: a question
// they asked with it ("what is the best flavor?"), "" none; name: the
// option's name as Google has it (a pick by name).
type offerPick struct {
	k                 int
	name              string
	details, question string
	// looked: the options looked up for a long message (AboutDetails), by
	// index — reused by an answer about them. facts: the same, with
	// answers on (Place Details Enterprise + Atmosphere).
	looked map[int]llm.Place
	facts  map[int]llm.PlaceFacts
}

// factsPrefetchKey carries pickFromOffer's facts to answerFromSources.
type factsPrefetchKey struct{}

// aboutPrefetchKey carries pickFromOffer's lookups to answerAbout within
// one message.
type aboutPrefetchKey struct{}

// pickFromOffer reads a pick among the open offer's options: a number or
// an ordinal by rule with no lookup; otherwise, when the words could carry
// a name, the options' names are looked up again by id (only ids are kept —
// Google's terms) and matched: the whole message ("Joe's", "the bakery"),
// or a name inside a message of any length ("Ok nolita pizza, what is the
// best flavor?"). k=0: not a pick.
func (r *Router) pickFromOffer(ctx context.Context, id Identity, text string, offer *DiscoveryOffer) offerPick {
	n := len(offer.Options)
	if k := pickNumber(text, n); k > 0 {
		return offerPick{k: k}
	}
	if !nameCandidate(text) && !longNameCandidate(text) {
		return offerPick{}
	}
	var names []string
	var looked map[int]llm.Place
	if nameCandidate(text) {
		names = r.offerNames(ctx, id, offer)
		if k := pickName(text, n, names); k > 0 {
			return offerPick{k: k, name: names[k-1]}
		}
	} else {
		// A longer message is as likely a question about the options as a
		// pick: one lookup per option with what an answer needs (names
		// included), shared with that answer — one set per message against
		// the daily cap.
		var facts map[int]llm.PlaceFacts
		if r.answersOn() {
			facts = r.factsFor(ctx, id, offer, singleOptions(offer))
			looked = map[int]llm.Place{}
			for i, f := range facts {
				looked[i] = f.Place
			}
		} else {
			looked = r.aboutDetails(ctx, id, offer, singleOptions(offer), nil)
		}
		names = make([]string, n)
		for i, pl := range looked {
			names[i] = pl.Name
		}
		k, rest := pickNameIn(text, n, names)
		if k == 0 {
			return offerPick{looked: looked, facts: facts}
		}
		p := offerPick{k: k, name: names[k-1], facts: facts}
		if len(pickWords(rest)) > 0 {
			p.details, p.question = splitQuestion(text)
		}
		return p
	}
	k, rest := pickNameIn(text, n, names)
	if k == 0 {
		return offerPick{looked: looked}
	}
	p := offerPick{k: k, name: names[k-1]}
	if len(pickWords(rest)) > 0 {
		p.details, p.question = splitQuestion(text)
	}
	return p
}

// offerNames looks up the name of each single-place option again (Place
// Details, the name only), in parallel; "" for a route or a failed lookup.
// Each lookup is a Places call, counted against the daily cap.
func (r *Router) offerNames(ctx context.Context, id Identity, offer *DiscoveryOffer) []string {
	if r.Places == nil {
		return nil
	}
	day := r.now().In(r.loc()).Format("2006-01-02")
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	names := make([]string, len(offer.Options))
	var wg sync.WaitGroup
	for i, opt := range offer.Options {
		if len(opt) != 1 {
			continue
		}
		ok, err := r.DiscoveryStore.TakePlacesCall(lctx, id.ID, day, r.limits().PerDay)
		if err != nil || !ok {
			log.Printf("[channel] offer names identity=%s: no lookup (cap or count failed: %v)", id.ID, err)
			continue
		}
		wg.Add(1)
		go func(i int, pid string) {
			defer wg.Done()
			name, err := r.Places.PlaceName(lctx, pid)
			if err != nil {
				log.Printf("[channel] offer names identity=%s option=%d: %v", id.ID, i+1, err)
				return
			}
			names[i] = name
		}(i, opt[0])
	}
	wg.Wait()
	return names
}

var lastWordRE = regexp.MustCompile(`(?i)^\W*last\W*$`)

// routeDiscovery runs in idle after the classifier: a find / plan / check
// request by rule, a question, or a follow-up to an answer still open.
func (r *Router) routeDiscovery(ctx context.Context, in Inbound, id Identity, conv Conversation, text string, intent llm.Intent) (bool, error) {
	if !r.discoveryOn() {
		return false, nil
	}
	strong := LooksLikeDiscovery(text)
	offer := r.freshOffer(ctx, id)
	if !strong && intent != llm.IntentQuestion && (offer == nil || intent != llm.IntentOther) {
		return false, nil
	}
	return r.discover(ctx, in, id, conv, text, offer, strong, false)
}

// routeDiscoveryWhileDraft runs with a booking summary open (onConfirm),
// before the message is read as a change to the booking. A clear search
// ("find me a…", "where can I get…", "any … near", "is there a … open")
// runs discovery and leaves the draft as it is; so does the answer to a
// "Where should I look?" asked after the summary. A pick from options shown
// after the summary is not booked over the open draft: the person is asked
// to settle that one first. Anything else ("make it 4pm") re-drafts as
// before.
func (r *Router) routeDiscoveryWhileDraft(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) (bool, error) {
	if !r.discoveryOn() || conv.Pending == nil {
		return false, nil
	}
	if offer := r.freshOffer(ctx, id); offer != nil && offer.AtUnix >= conv.Pending.SummaryShownAtUnix {
		at := time.Unix(offer.AtUnix, 0)
		if !r.supporterSpokeSince(ctx, in, id, &at) {
			switch offer.Kind {
			case offerOptions:
				if r.pickFromOffer(ctx, id, text, offer).k > 0 {
					return true, r.Reply(ctx, in, CopyPickWhileDraft, "")
				}
			case offerAskLocation:
				if handled, err := r.discover(ctx, in, id, conv, text, offer, false, true); handled {
					return true, err
				}
			}
		}
	}
	if !IsClearSearch(text) {
		return false, nil
	}
	return r.discover(ctx, in, id, conv, text, nil, true, true)
}

// clearSearchRE: words that can only be a search, even with a booking open.
// Narrower than discoveryRE: "make it 4pm", "near the door", "where do I
// meet him" never match.
var clearSearchRE = regexp.MustCompile(`(?i)\b(?:find (?:me |us )?(?:a|an|some|another|places?|somewhere)\b|where (?:can|could|should) (?:i|we) (?:get|buy|find|drop|mail|ship|print|fix|copy|return|take)\b|any (?:good |decent )?[\pL' &-]{2,30}? (?:near|around|nearby|open)\b|is there (?:a|an|any) [\pL' &-]{2,40}? (?:open|near|nearby|around)\b)`)

// IsClearSearch: the words only ask to find a place.
func IsClearSearch(text string) bool { return clearSearchRE.MatchString(text) }

// ── several errands, no places ──────────────────────────────────────────

// errandStartRE: an errand that needs a place, at the start of one item of
// a list ("pick up dry cleaning", "buy a birthday card", "mail a package").
var errandStartRE = regexp.MustCompile(`(?i)^(?:(?:and|also|then|plus|&)\s+)*(?:(?:i need to|i need you to|i have to|i gotta|need to|can you|could you|please|pls)\s+)*(?:pick up|pickup|grab|buy|get|mail|ship|send|post|drop off|drop|return|print|copy|fix|repair|hem|alter)\b`)

// placeGivenRE: something in the words that is (or names) a place: a
// number (a house number, a zip), or "at / from / on …".
var placeGivenRE = regexp.MustCompile(`(?i)\d|\b(?:at|from|on|in|near|around)\s+\S`)

var listSplitRE = regexp.MustCompile(`(?i)\s*(?:[,;\n•]|\band then\b|\bthen\b|\s&\s)\s*`)

// IsPlacelessErrandList: one message lists two or more errands that each
// need a place, and none says where. Rule only.
func IsPlacelessErrandList(text string) bool {
	if placeGivenRE.MatchString(text) || LooksLikeDiscovery(text) {
		return false
	}
	var items []string
	for _, part := range listSplitRE.Split(strings.TrimSpace(text), -1) {
		// "buy a card and mail a package": split on "and" only where an
		// errand starts after it.
		cur := ""
		for _, w := range strings.Split(part, " and ") {
			if cur != "" && errandStartRE.MatchString(strings.TrimSpace(w)) {
				items = append(items, cur)
				cur = ""
			}
			if cur != "" {
				cur += " and "
			}
			cur += w
		}
		if strings.TrimSpace(strings.Trim(cur, ".!")) != "" {
			items = append(items, cur)
		}
	}
	if len(items) < 2 || len(items) > llm.MaxErrands {
		return false
	}
	for _, it := range items {
		if !errandStartRE.MatchString(strings.TrimSpace(it)) {
			return false
		}
	}
	return true
}

// askRoute asks, by fixed copy, whether to find places for a list of
// errands with none (IsPlacelessErrandList), or whether the person has the
// addresses. Called in idle with no supporter routing taking the message.
func (r *Router) askRoute(ctx context.Context, in Inbound, id Identity, text string) (bool, error) {
	if !r.discoveryOn() || !IsPlacelessErrandList(text) {
		return false, nil
	}
	r.saveOffer(ctx, id, &DiscoveryOffer{Kind: offerRouteAsk, Words: []string{text}, AtUnix: r.now().Unix()})
	log.Printf("[channel] route ask identity=%s", id.ID)
	return true, r.Reply(ctx, in, CopyRouteAsk, "")
}

// findThemRE: "find them", "you find", "yes please find", "search", "one
// route" — the answer that asks HO:RA to find the places.
var findThemRE = regexp.MustCompile(`(?i)\b(?:find|search|look (?:them |it )?up|you pick|you choose|one route|route|plan)\b`)

// answerRouteAsk reads the answer to CopyRouteAsk: "find them" (or a plain
// yes) runs route planning; a decline drops the question; anything else is
// the addresses, which go with the errands down the normal draft path.
func (r *Router) answerRouteAsk(ctx context.Context, in Inbound, id Identity, conv Conversation, text string, offer *DiscoveryOffer) (bool, error) {
	switch {
	case findThemRE.MatchString(text) || IsYes(text) || IsAffirmation(stripFiller(text)):
		find := routeFindText
		if !IsYes(text) && !IsAffirmation(stripFiller(text)) {
			find += " " + text // "find them near Union Square": where to look
		}
		// The offer stays a route ask until discover answers (it saves its
		// own: options, or "Where should I look?").
		return r.discover(ctx, in, id, conv, find, offer, true, false)
	case IsNo(text) || IsDraftDecline(text):
		r.saveOffer(ctx, id, nil)
		return true, r.Reply(ctx, in, CopyAck, "")
	}
	r.saveOffer(ctx, id, nil)
	return true, r.startDraft(ctx, in, id, conv, strings.Join(offer.Words, "\n")+"\n"+text)
}

// freshOffer is the thread's open offer, nil when none or expired.
func (r *Router) freshOffer(ctx context.Context, id Identity) *DiscoveryOffer {
	o, err := r.DiscoveryStore.GetDiscovery(ctx, id.ID)
	if err != nil {
		log.Printf("[channel] discovery offer identity=%s: %v", id.ID, err)
		return nil
	}
	if o == nil {
		return nil
	}
	ttl := offerOptionsTTL
	if o.Kind == offerAskLocation || o.Kind == offerRouteAsk {
		ttl = offerLocationTTL
	}
	if r.now().Sub(time.Unix(o.AtUnix, 0)) > ttl {
		return nil
	}
	return o
}

func (r *Router) saveOffer(ctx context.Context, id Identity, o *DiscoveryOffer) {
	if err := r.DiscoveryStore.SaveDiscovery(ctx, id.ID, o); err != nil {
		// The answer still goes out; a pick from it then reads as new words.
		log.Printf("[channel] discovery save identity=%s: %v", id.ID, err)
	}
}

// discover runs one discovery turn. strong: the words asked for it by rule,
// so a failure is answered with CopySearchDown; otherwise a failure leaves
// the message to the usual routing. draftOpen: a booking summary is open
// (routeDiscoveryWhileDraft) — every answer ends by saying it still waits,
// and an answer that is not a search's (a pick, the usual, not discovery)
// is left to the draft.
func (r *Router) discover(ctx context.Context, in Inbound, id Identity, conv Conversation, text string, offer *DiscoveryOffer, strong, draftOpen bool) (bool, error) {
	reply := func(s string) error {
		if draftOpen {
			s += "\n\n" + CopyBookingStillWaiting
		}
		return r.Reply(ctx, in, s, "")
	}
	lim := r.limits()
	req := llm.DiscoveryRequest{Text: text, Now: r.now().In(r.loc()), MaxSearches: lim.PerMessage}
	if offer != nil {
		req.Earlier = offer.Words
		req.HoraLine = r.offerLine(ctx, id, offer)
	}
	past := r.pastTasks(ctx, id)
	for i, t := range past {
		if i == 5 {
			break
		}
		req.PastErrands = append(req.PastErrands, t.Title)
	}
	req.LastAddress = lastAddressOf(past)
	day := r.now().In(r.loc()).Format("2006-01-02")
	denied := false
	req.Allow = func(c context.Context) bool {
		ok, err := r.DiscoveryStore.TakePlacesCall(c, id.ID, day, lim.PerDay)
		if err != nil {
			log.Printf("[channel] places quota identity=%s: %v", id.ID, err)
			ok = false // cannot count: no search (the cost is the risk)
		}
		denied = denied || !ok
		return ok
	}
	// The answer takes seconds: one fixed line first, once. A search by
	// rule that says where to look gets it before the first model call
	// (production 2026-11-06: it went out 11 s in, after the classifier and
	// the model's first call) — unless the daily cap leaves no search.
	// Otherwise it goes only when the model is about to search. A failed
	// send is logged: the answer still follows.
	lookedUp := false
	lookingUp := func() {
		if lookedUp {
			return
		}
		lookedUp = true
		if err := r.Reply(ctx, in, CopyLookingUp, ""); err != nil {
			log.Printf("[channel] looking up identity=%s: %v", id.ID, err)
		}
	}
	if strong && saysWhere(text, offer) {
		if left, err := r.DiscoveryStore.PlacesCallsLeft(ctx, id.ID, day, lim.PerDay); err == nil && left > 0 {
			lookingUp()
		}
	}
	req.OnSearch = func(context.Context) { lookingUp() }
	dctx, cancel := context.WithTimeout(ctx, lim.Timeout)
	res, err := r.Discovery.Discover(dctx, req)
	cancel()
	log.Printf("[channel] discovery identity=%s action=%s searches=%d options=%d model=%s calls=%d latency=%s in=%d cached=%d out=%d err=%v",
		id.ID, res.Action, res.Searches, len(res.Options), res.Usage.Model, res.Calls, res.Usage.Latency.Round(time.Millisecond),
		res.Usage.InputTokens, res.Usage.CacheReadTokens, res.Usage.OutputTokens, err != nil)
	if err != nil {
		log.Printf("[channel] discovery identity=%s: %v", id.ID, err)
		if !strong {
			return false, nil
		}
		return true, reply(CopySearchDown)
	}
	words := append(append([]string{}, req.Earlier...), text)
	switch res.Action {
	case llm.DiscoverOutOfScope:
		r.saveOffer(ctx, id, nil)
		return true, reply(outOfScopeCopy(res.Reason))
	case llm.DiscoverAskLocation:
		r.saveOffer(ctx, id, &DiscoveryOffer{Kind: offerAskLocation, Words: lastWords(words), AtUnix: r.now().Unix()})
		if req.LastAddress != "" {
			return true, reply(fmt.Sprintf(CopyAskLocationLast, req.LastAddress))
		}
		return true, reply(CopyAskLocation)
	case llm.DiscoverUsual:
		if draftOpen {
			return false, nil
		}
		return true, r.startUsual(ctx, in, id, conv, res.Usual)
	case llm.DiscoverChoose:
		if !draftOpen && offer != nil && offer.Kind == offerOptions && res.Choice <= len(offer.Options) {
			return true, r.chooseOption(ctx, in, id, conv, offer, res.Choice, text)
		}
		return false, nil
	case llm.DiscoverAbout:
		if draftOpen || offer == nil || offer.Kind != offerOptions {
			return false, nil // nothing shown to ask about: the usual routing
		}
		if r.answersOn() {
			return true, r.answerFromSources(ctx, in, id, offer, res, text)
		}
		return true, r.answerAbout(ctx, in, id, offer, res, text)
	case llm.DiscoverInfo:
		if len(res.Options) == 0 {
			r.saveOffer(ctx, id, nil)
			if denied && res.Searches == 0 {
				return true, reply(CopySearchLimit)
			}
			return true, reply(CopyDiscoveryNothing)
		}
		o := &DiscoveryOffer{Kind: offerOptions, Words: lastWords(words), Check: res.Check, Playbook: res.Playbook, AtUnix: r.now().Unix()}
		for _, opt := range res.Options {
			var ids []string
			for _, p := range opt {
				ids = append(ids, p.ID)
			}
			o.Options = append(o.Options, ids)
		}
		r.saveOffer(ctx, id, o)
		return true, reply(RenderDiscovery(res))
	}
	return false, nil // not_discovery
}

// lastWords keeps the newest few lines of a search's words.
func lastWords(w []string) []string {
	if len(w) > 4 {
		return w[len(w)-4:]
	}
	return w
}

// RenderDiscovery is an info reply: the lead, each option from Google's
// data, the attribution and the way to pick. Never a price or a YES. The
// test runner (cmd/agenttest) grades this exact text.
func RenderDiscovery(res llm.DiscoveryResult) string {
	var b strings.Builder
	lead := res.Lead
	if !InfoLeadOK(lead) {
		if lead != "" {
			log.Printf("[channel] discovery lead refused by rule (%d chars) — fixed lead", len(lead))
		}
		lead = CopyDiscoveryLead
		if res.Check {
			lead = CopyDiscoveryLeadCheck
		}
	}
	b.WriteString(lead)
	b.WriteString("\n")
	for i, opt := range res.Options {
		b.WriteString("\n")
		if len(opt) == 1 {
			fmt.Fprintf(&b, "%d. %s\n   %s", i+1, opt[0].Name, placeFacts(opt[0]))
		} else {
			fmt.Fprintf(&b, "%d. %d stops, in this order:", i+1, len(opt))
			for j, p := range opt {
				fmt.Fprintf(&b, "\n   %c. %s · %s", 'a'+j, p.Name, placeFacts(p))
			}
		}
	}
	b.WriteString("\n\n" + CopyDiscoveryFooter + "\n")
	switch {
	case res.Check:
		b.WriteString(CopyDiscoveryPickCheck)
	case len(res.Options) == 1:
		b.WriteString(CopyDiscoveryPickOne)
	default:
		b.WriteString(CopyDiscoveryPick)
	}
	return b.String()
}

// placeFacts: address · rating · hours, each only when Google gave it.
func placeFacts(p llm.Place) string {
	parts := []string{p.Address}
	if p.Rating > 0 {
		r := fmt.Sprintf("★ %.1f", p.Rating)
		if p.RatingCount > 0 {
			r += fmt.Sprintf(" (%s)", thousands(p.RatingCount))
		}
		parts = append(parts, r)
	}
	if p.OpenNow != nil {
		switch {
		case *p.OpenNow && p.ClosesAt != "":
			parts = append(parts, "open now, closes "+p.ClosesAt)
		case *p.OpenNow:
			parts = append(parts, "open now")
		case p.OpensAt != "":
			parts = append(parts, "closed now, opens "+p.OpensAt)
		default:
			parts = append(parts, "closed now")
		}
	}
	return strings.Join(parts, " · ")
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// The model's lead may not carry a price, a question (a YES among them), a
// time promise, a rating, a link or a number that could be a phone.
var (
	leadMoneyRE   = regexp.MustCompile(`(?i)[$€£]|\b(?:dollars?|bucks|usd|price[sd]?|pricing|costs?|fees?|charge[sd]?|estimate|quote|cheap(?:er|est)?|free)\b`)
	leadAskRE     = regexp.MustCompile(`(?i)[?？]|\b(?:yes|y/n|confirm|book(?:ed|ing)?|reply|respond|text back)\b`)
	leadPromiseRE = regexp.MustCompile(`(?i)\b(?:in|within|under|by|before)\s+(?:\d+|an?|one|two|three|half an?)\s*(?:min|mins|minutes?|hours?|hrs?|am|pm|o'?clock)\b|\b(?:by|before)\s+\d{1,2}(?::\d\d)?\b|\b(?:asap|right away|immediately|guarantee[sd]?|promise[sd]?)\b`)
	leadFactRE    = regexp.MustCompile(`(?i)★|\b\d(?:\.\d)?\s*(?:stars?|/\s*5)\b|\brated\s+\d|\b\d\.\d\b|\b\d[\d,]*\s+reviews?\b|https?://|www\.|\d{7,}|\d{3}[-. ]\d{4}`)
)

// InfoLeadOK: the model's lead may be sent (the runner grades with it).
func InfoLeadOK(lead string) bool {
	lead = strings.TrimSpace(lead)
	if lead == "" || utf8.RuneCountInString(lead) > 240 || strings.Contains(lead, "\n") {
		return false
	}
	return !leadMoneyRE.MatchString(lead) && !leadAskRE.MatchString(lead) && !leadPromiseRE.MatchString(lead) && !leadFactRE.MatchString(lead)
}

// pickFillers are the words around a pick: "let's do the second one please".
var pickFillers = map[string]bool{
	"let's": true, "lets": true, "i'll": true, "ill": true, "i": true, "we'll": true, "go": true, "with": true, "take": true,
	"pick": true, "choose": true, "do": true, "want": true, "the": true, "option": true, "number": true, "no": true, "no.": true,
	"one": true, "place": true, "please": true, "pls": true, "thanks": true, "thank": true, "you": true, "ok": true, "okay": true,
	"that": true, "#": true, "is": true, "fine": true, "good": true, "sounds": true, "great": true,
}

var ordinals = map[string]int{"1": 1, "2": 2, "3": 3, "1st": 1, "2nd": 2, "3rd": 3, "first": 1, "second": 2, "third": 3, "#1": 1, "#2": 2, "#3": 3}

var pickPunctRE = regexp.MustCompile(`[!.,?;:()"“”]+`)

// pickOption reads a pick among n options by rule: a number or an ordinal
// ("2", "the second one", "#3 please", "last"), or a name among names
// (index i = option i+1; "" for an option with no single name). 0 = not a
// pick — the model then reads it, with the options in view.
func pickOption(text string, n int, names []string) int {
	if k := pickNumber(text, n); k > 0 {
		return k
	}
	return pickName(text, n, names)
}

// pickWords: the words of a pick, fillers dropped.
func pickWords(text string) []string {
	t := strings.ToLower(strings.NewReplacer("’", "'", "‘", "'").Replace(text))
	t = pickPunctRE.ReplaceAllString(t, " ")
	var rest []string
	for _, w := range strings.Fields(t) {
		if !pickFillers[w] {
			rest = append(rest, w)
		}
	}
	return rest
}

// pickNumber: a pick by number or ordinal ("2", "the second one", "last").
func pickNumber(text string, n int) int {
	rest := pickWords(text)
	if len(rest) == 1 {
		if k, ok := ordinals[rest[0]]; ok && k <= n {
			return k
		}
		if rest[0] == "last" && n > 0 {
			return n
		}
	}
	return 0
}

// nameCandidate: the words could be a place's name (one to four words left
// after the fillers, three letters or more) — worth looking the names up.
func nameCandidate(text string) bool {
	rest := pickWords(text)
	return len(rest) >= 1 && len(rest) <= 4 && utf8.RuneCountInString(nameKey(strings.Join(rest, " "))) >= 3
}

// pickName: a pick by name among names (index i = option i+1; "" for an
// option with no single name). Two names matching is no pick.
func pickName(text string, n int, names []string) int {
	rest := pickWords(text)
	if len(rest) == 0 {
		return 0
	}
	want := nameKey(strings.Join(rest, " "))
	if utf8.RuneCountInString(want) < 3 {
		return 0
	}
	hit := 0
	for i, name := range names {
		if i >= n || name == "" {
			continue
		}
		k := nameKey(name)
		if k == want || strings.HasPrefix(k, want+" ") || strings.Contains(" "+k+" ", " "+want+" ") {
			if hit != 0 {
				return 0 // two names match: not a pick by rule
			}
			hit = i + 1
		}
	}
	return hit
}

var nameKeyRE = regexp.MustCompile(`[^\p{L}\p{N} ]+`)

func nameKey(s string) string {
	s = strings.ToLower(strings.NewReplacer("’", "", "'", "", "&", " and ").Replace(s))
	return strings.Join(strings.Fields(nameKeyRE.ReplaceAllString(s, " ")), " ")
}

// chooseOption turns option k of the open offer into a normal draft. The
// places are looked up again by id (only ids are kept) and attached by
// rule; the words the parser reads are the person's own, plus the address.
func (r *Router) chooseOption(ctx context.Context, in Inbound, id Identity, conv Conversation, offer *DiscoveryOffer, k int, extra string) error {
	return r.chooseOptionWith(ctx, in, id, conv, offer, k, extra, nil, "")
}

// chooseOptionWith is chooseOption with notes for the supporter (the
// person's question, kept on the draft and sent with the booking) and an
// intro said once before the draft's first question.
func (r *Router) chooseOptionWith(ctx context.Context, in Inbound, id Identity, conv Conversation, offer *DiscoveryOffer, k int, extra string, notes []string, intro string) error {
	if r.Places == nil {
		return r.Reply(ctx, in, CopyPlaceLookupDown, "")
	}
	var chosen []ChosenPlace
	// All the lookups share one bound: the draft after them (parse, area
	// check) needs the rest of the message's 60 s.
	dctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	for _, pid := range offer.Options[k-1] {
		p, err := r.Places.PlaceDetails(dctx, pid)
		if err != nil {
			log.Printf("[channel] discovery choice identity=%s: details: %v", id.ID, err)
			return r.Reply(ctx, in, CopyPlaceLookupDown, "")
		}
		if !p.InNYC {
			r.saveOffer(ctx, id, nil)
			return r.Reply(ctx, in, CopyOutOfArea, "")
		}
		chosen = append(chosen, ChosenPlace{Name: p.Name, Address: firstNonEmpty(p.FullAddress, p.Address)})
	}
	if len(chosen) == 0 {
		return r.Reply(ctx, in, CopyPlaceLookupDown, "")
	}
	r.saveOffer(ctx, id, nil)
	lines := append([]string{}, offer.Words...)
	if strings.TrimSpace(extra) != "" {
		lines = append(lines, strings.TrimSpace(extra))
	}
	if len(chosen) == 1 {
		lines = append(lines, "At: "+chosen[0].Address)
	} else {
		var stops []string
		for _, c := range chosen {
			stops = append(stops, c.Address)
		}
		lines = append(lines, "Stops in order: "+strings.Join(stops, "; "))
	}
	log.Printf("[channel] discovery choice identity=%s option=%d stops=%d check=%t", id.ID, k, len(chosen), offer.Check)
	return r.startDraftOpts(ctx, in, id, conv, strings.Join(lines, "\n"), draftOpts{places: chosen, check: offer.Check, notes: notes, intro: intro, playbook: offer.Playbook})
}

// applyPlaces attaches the chosen place(s) to a parsed errand by rule: the
// stops are Google's addresses (the person's own other stop kept), the
// supporter's description names the places, and a check errand gets its
// instruction and, when no length was named, 15 minutes (the base fee's
// included time).
func applyPlaces(d *llm.TaskDraft, places []ChosenPlace, check, durationNamed bool) {
	if len(places) == 0 {
		return
	}
	first := places[0].Address
	if len(places) == 1 {
		if !isPlaceStop(d.Location1, places) && !isPlaceStop(d.Location2, places) {
			switch {
			case d.Location1 != "" && d.Location2 == "" && hasDigitRE.MatchString(d.Location1):
				d.Location2 = first // the person's own address stays the first stop
			default:
				d.Location1 = first
			}
		}
	} else {
		own := ""
		for _, s := range []string{d.Location1, d.Location2} {
			if s != "" && !isPlaceStop(s, places) {
				own = s
			}
		}
		d.Location1, d.Location2 = first, firstNonEmpty(own, places[len(places)-1].Address)
	}
	if d.Location2 == d.Location1 {
		d.Location2 = ""
	}
	var desc []string
	if strings.TrimSpace(d.Description) != "" {
		desc = append(desc, strings.TrimSpace(d.Description))
	}
	if len(places) == 1 {
		desc = append(desc, fmt.Sprintf(descriptionPlaceOne, places[0].Name, places[0].Address))
	} else {
		l := descriptionPlaceRoute
		for i, p := range places {
			l += fmt.Sprintf("\n%d. %s, %s", i+1, p.Name, p.Address)
		}
		desc = append(desc, l)
	}
	if check {
		desc = append(desc, descriptionCheck)
		if !durationNamed && d.DurationMinutes == 0 {
			d.DurationMinutes = 15
		}
	}
	d.Description = strings.Join(desc, "\n\n")
}

var hasDigitRE = regexp.MustCompile(`\d`)

// isPlaceStop: the stop is one of the places' addresses (the parser copied
// it, perhaps shortened to the street).
func isPlaceStop(stop string, places []ChosenPlace) bool {
	s := stopKey(stop)
	if s == "" {
		return false
	}
	for _, p := range places {
		a := stopKey(p.Address)
		street, _, _ := strings.Cut(a, ",")
		if s == a || strings.HasPrefix(a, s) || strings.HasPrefix(s, street) {
			return true
		}
	}
	return false
}

// placesLines: the summary's lines for a discovery draft ("" for others).
func placesLines(p *PendingDraft) string {
	if p == nil || len(p.Places) == 0 {
		return ""
	}
	var b strings.Builder
	if len(p.Places) == 1 {
		b.WriteString("\n" + fmt.Sprintf(CopyPlacesLineOne, p.Places[0].Name))
	} else {
		var names []string
		for i, c := range p.Places {
			names = append(names, fmt.Sprintf("%d. %s", i+1, c.Name))
		}
		b.WriteString("\n" + fmt.Sprintf(CopyPlacesLineRoute, strings.Join(names, ", ")))
	}
	if p.Check {
		b.WriteString("\n" + CopyCheckLine)
	}
	return b.String()
}

// ── the usual ────────────────────────────────────────────────────────────

func (r *Router) pastTasks(ctx context.Context, id Identity) []PastTask {
	if r.History == nil || !id.Linked() {
		return nil
	}
	ts, err := r.History.PastTasks(ctx, id.UserID, 20)
	if err != nil {
		log.Printf("[channel] past tasks identity=%s: %v", id.ID, err)
		return nil
	}
	return ts
}

func (r *Router) lastAddress(ctx context.Context, id Identity) string {
	return lastAddressOf(r.pastTasks(ctx, id))
}

// lastAddressOf: where the newest errand ended (its drop-off, else its
// only stop) — the person's own address, offered as "LAST".
func lastAddressOf(ts []PastTask) string {
	if len(ts) == 0 {
		return ""
	}
	return firstNonEmpty(ts[0].Location2, ts[0].Location1)
}

// usualTask: the errand that repeats most among the recent ones (newest
// wins a tie), else the newest.
func usualTask(ts []PastTask) PastTask {
	count := map[string]int{}
	for _, t := range ts {
		count[nameKey(t.Title)]++
	}
	best := ts[0]
	for _, t := range ts {
		if count[nameKey(t.Title)] > count[nameKey(best.Title)] {
			best = t
		}
	}
	return best
}

// startUsual offers the person's own past errand (index into their newest
// tasks, or -1 for the usual one) as a new draft: area check, price,
// budget question and summary as for any other. Only their own tasks.
func (r *Router) startUsual(ctx context.Context, in Inbound, id Identity, conv Conversation, idx int) error {
	ts := r.pastTasks(ctx, id)
	if len(ts) == 0 {
		return r.Reply(ctx, in, CopyUsualNone, "")
	}
	t := usualTask(ts)
	if idx >= 0 && idx < len(ts) {
		t = ts[idx]
	}
	d := llm.TaskDraft{Title: t.Title, Category: t.Category, Description: stripAmounts(t.Description),
		Location1: t.Location1, Location2: t.Location2, DurationMinutes: t.Minutes}
	if err := d.Validate(); err != nil {
		return r.Reply(ctx, in, CopyUsualNone, "")
	}
	r.saveOffer(ctx, id, nil)
	// The words a correction re-drafts from: the errand as the person had
	// it, with no amount in them — a past budget is asked again, never
	// carried (ownwords.go).
	source := stripAmounts(d.Title)
	if d.Description != "" {
		source += ". " + d.Description
	}
	if d.Location1 != "" {
		source += "\nAt: " + d.Location1
	}
	if d.Location2 != "" {
		source += "\nTo: " + d.Location2
	}
	log.Printf("[channel] usual identity=%s from=%d tasks", id.ID, len(ts))
	return r.startDraftOpts(ctx, in, id, conv, source, draftOpts{prebuilt: &d, intro: CopyUsualIntro})
}

// stripAmounts removes dollar figures from a past errand's words.
func stripAmounts(s string) string {
	return strings.Join(strings.Fields(saidAmountRE.ReplaceAllString(s, "")), " ")
}

// ── picks by name in longer messages, and questions about the options ───

// longNameCandidate: a message long enough that a name may sit inside it
// with other words ("Ok nolita pizza, what is the best flavor?").
func longNameCandidate(text string) bool { return len(pickWords(text)) > 4 }

// nameGenericWords: words a place name shares with many others. A pick by
// one word inside a longer message never rests on one of these alone.
var nameGenericWords = map[string]bool{
	"the": true, "and": true, "of": true, "a": true, "at": true, "on": true, "new": true, "york": true, "nyc": true, "ny": true,
	"pizza": true, "pizzeria": true, "cafe": true, "café": true, "coffee": true, "bakery": true, "laundromat": true, "laundry": true,
	"cleaners": true, "pharmacy": true, "market": true, "store": true, "shop": true, "deli": true, "grocery": true, "restaurant": true,
	"bar": true, "kitchen": true, "street": true, "st": true, "avenue": true, "ave": true, "brooklyn": true, "manhattan": true,
	"queens": true, "bronx": true, "wash": true, "fold": true, "co": true, "inc": true, "best": true, "famous": true, "original": true,
}

// pickNotRE: words that turn a name in a message into something else than
// a pick of it ("not Nolita Pizza", "cheaper than Joe's", "Joe's or
// Suds?"). The model reads those.
var pickNotRE = regexp.MustCompile(`(?i)\b(?:not|no|don'?t|isn'?t|instead|other|another|rather|than|except|besides|cheaper|closer|better|worse|vs|versus|or|compare)\b`)

// nameCuts: where a place name's core ends ("Joe's Pizza - Broadway").
var nameCutRE = regexp.MustCompile(`\s+[-–—|:]\s+|\s*[(,]`)

// pickNameIn finds one option's name inside a message of any length: the
// name, its core before " - …" / "(…)", or the core's first words (two or
// more, not all generic; one word only when it is distinctive and five
// letters or longer). Two options matching, or words that turn it into a
// comparison or a refusal, are no pick. rest: the message without the
// name.
func pickNameIn(text string, n int, names []string) (int, string) {
	if pickNotRE.MatchString(strings.NewReplacer("’", "'").Replace(text)) {
		return 0, ""
	}
	tk := " " + nameKey(text) + " "
	hit, hitKey := 0, ""
	for i, name := range names {
		if i >= n || name == "" {
			continue
		}
		var cands []string
		full := nameKey(name)
		core := nameKey(nameCutRE.Split(name, 2)[0])
		cands = append(cands, full, core)
		w := strings.Fields(core)
		for j := len(w) - 1; j >= 1; j-- {
			pre := w[:j]
			distinct := false
			for _, x := range pre {
				distinct = distinct || !nameGenericWords[x]
			}
			if !distinct || (j == 1 && utf8.RuneCountInString(pre[0]) < 5) {
				continue
			}
			cands = append(cands, strings.Join(pre, " "))
		}
		for _, c := range cands {
			if c == "" || !strings.Contains(tk, " "+c+" ") {
				continue
			}
			if hit != 0 && hit != i+1 {
				return 0, "" // two options named
			}
			if hit == 0 || len(c) > len(hitKey) {
				hit, hitKey = i+1, c
			}
		}
	}
	if hit == 0 {
		return 0, ""
	}
	return hit, strings.TrimSpace(strings.Replace(tk, " "+hitKey+" ", " ", 1))
}

// questionPartRE: a part of a message that asks something.
var questionPartRE = regexp.MustCompile(`(?i)^\W*(?:and |so |also |but |ok |okay )*(?:what|what's|whats|which|how|do|does|did|is|are|can|could|would|will|should|any|where|when|who|why)\b`)

var partSplitRE = regexp.MustCompile(`[.!?;,\n]+|\s+-\s+`)

// splitQuestion splits a message into the words for the errand and the
// question asked with them: "Ok nolita pizza, what is the best flavor?" →
// "Ok nolita pizza" and "what is the best flavor?".
func splitQuestion(text string) (details, question string) {
	var d, q []string
	idx := partSplitRE.FindAllStringIndex(text, -1)
	start := 0
	for i := 0; i <= len(idx); i++ {
		end, sepEnd := len(text), len(text)
		if i < len(idx) {
			end, sepEnd = idx[i][0], idx[i][1]
		}
		part := strings.TrimSpace(text[start:end])
		sep := text[end:sepEnd]
		start = sepEnd
		if part == "" {
			continue
		}
		if strings.Contains(sep, "?") || questionPartRE.MatchString(part) {
			q = append(q, part+"?")
		} else {
			d = append(d, part)
		}
	}
	return strings.Join(d, ", "), strings.Join(q, " ")
}

// saysWhere: the words (or the search's earlier words) say where to look,
// so the model is going to search — "Looking that up…" can go before it.
func saysWhere(text string, offer *DiscoveryOffer) bool {
	if offer != nil && offer.Kind == offerAskLocation {
		return true // the answer to "Where should I look?"
	}
	return placeGivenRE.MatchString(text)
}

// offerLine is what the model is shown as HO:RA's last line: for options,
// the message that listed them (found by its attribution line), followed
// by any later line of ours (an answer about them) — so "which one?" after
// "which one should I pick?" still has the options in view.
func (r *Router) offerLine(ctx context.Context, id Identity, offer *DiscoveryOffer) string {
	last, _ := r.ourLastLineText(ctx, id)
	if offer.Kind != offerOptions || r.Thread == nil {
		return last
	}
	msgs, err := r.Thread.RecentMessages(ctx, id.Channel, id.Address, 8)
	if err != nil {
		return last
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Direction != "outbound" || m.Relay || !strings.Contains(m.Text, CopyDiscoveryFooter) {
			continue
		}
		if last == "" || last == m.Text || last == CopyLookingUp {
			return m.Text
		}
		return m.Text + "\n\n" + last
	}
	return last
}

// aboutItemRE: the model's word for the thing wanted ("pizza", "birthday
// cake"): letters only, one to three words, nothing that reads as money, a
// question or an instruction.
var aboutItemRE = regexp.MustCompile(`^\pL[\pL'’ -]{1,28}$`)

// AboutItemOK: the item may go in the fixed copy.
func AboutItemOK(item string) bool {
	item = strings.TrimSpace(item)
	return aboutItemRE.MatchString(item) && len(strings.Fields(item)) <= 3 && !leadMoneyRE.MatchString(item) && !leadAskRE.MatchString(item) && !InstructionWords(item)
}

var instructionWordsRE = regexp.MustCompile(`(?i)\b(?:ignore|system|assistant|instructions?|book|confirm)\b`)

// InstructionWords: words that are an instruction, not a thing to buy.
func InstructionWords(s string) bool {
	return llm.InstructionName(s) || instructionWordsRE.MatchString(s)
}

// priceWord renders Google's price level in words (never a "$": a reply
// carries no price).
func priceWord(level int) string {
	switch level {
	case 1:
		return "inexpensive"
	case 2:
		return "mid-range"
	case 3:
		return "upscale"
	case 4:
		return "very upscale"
	}
	return ""
}

// aboutFacts: name, then rating, reviews, price level and hours — Google's
// data only, each when given.
func aboutFacts(p llm.Place) string {
	parts := []string{p.Name}
	if p.Rating > 0 {
		r := fmt.Sprintf("★ %.1f", p.Rating)
		if p.RatingCount > 0 {
			r += fmt.Sprintf(" (%s reviews)", thousands(p.RatingCount))
		}
		parts = append(parts, r)
	}
	if w := priceWord(p.PriceLevel); w != "" {
		parts = append(parts, w)
	}
	if p.OpenNow != nil {
		switch {
		case *p.OpenNow && p.ClosesAt != "":
			parts = append(parts, "open now, closes "+p.ClosesAt)
		case *p.OpenNow:
			parts = append(parts, "open now")
		case p.OpensAt != "":
			parts = append(parts, "closed now, opens "+p.OpensAt)
		default:
			parts = append(parts, "closed now")
		}
	}
	return strings.Join(parts, " · ")
}

// bestRated: the option to suggest by rule — the highest rating, then the
// most reviews. -1 when none has a rating.
func bestRated(ps map[int]llm.Place) int {
	best := -1
	for i, p := range ps {
		if p.Rating <= 0 {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		b := ps[best]
		if p.Rating > b.Rating || (p.Rating == b.Rating && (p.RatingCount > b.RatingCount || (p.RatingCount == b.RatingCount && i < best))) {
			best = i
		}
	}
	return best
}

// answerAbout answers a question about the options shown ("which one
// should I pick?", "what's good there?", "do they do delivery?") from
// Google's data, looked up again by id, and fixed copy: HO:RA has no menus,
// so it never names a dish — it offers to have the supporter ask. The
// options stay open; the suggested one is what a YES sets up.
func (r *Router) answerAbout(ctx context.Context, in Inbound, id Identity, offer *DiscoveryOffer, res llm.DiscoveryResult, text string) error {
	want := singleOptions(offer)
	if res.Choice > 0 && res.Choice <= len(offer.Options) && len(offer.Options[res.Choice-1]) == 1 {
		want = []int{res.Choice - 1}
	}
	pre, _ := ctx.Value(aboutPrefetchKey{}).(map[int]llm.Place)
	got := r.aboutDetails(ctx, id, offer, want, pre)
	k := -1
	if res.Choice > 0 {
		if _, ok := got[res.Choice-1]; ok {
			k = res.Choice - 1
		}
	}
	if k < 0 {
		k = bestRated(got)
	}
	if k < 0 {
		for i := range got { // no ratings at all: the first one looked up
			if k < 0 || i < k {
				k = i
			}
		}
	}
	log.Printf("[channel] discovery about identity=%s topic=%s choice=%d looked_up=%d suggest=%d", id.ID, res.Topic, res.Choice, len(got), k+1)
	if k < 0 {
		o := *offer
		o.Suggest, o.SuggestWords = 0, ""
		r.saveOffer(ctx, id, &o)
		return r.Reply(ctx, in, CopyAboutNoData, "")
	}
	p := got[k]
	var lead, words string
	switch res.Topic {
	case "pick":
		lead = fmt.Sprintf(CopyAboutPick, p.Name)
	case "menu":
		if AboutItemOK(res.Item) {
			item := strings.ToLower(strings.TrimSpace(res.Item))
			lead, words = fmt.Sprintf(CopyAboutMenuItem, p.Name, item), fmt.Sprintf(suggestMenuItem, item)
		} else {
			lead, words = fmt.Sprintf(CopyAboutMenu, p.Name), suggestMenu
		}
	case "delivery":
		lead = fmt.Sprintf(CopyAboutDelivery, p.Name)
	default:
		lead, words = fmt.Sprintf(CopyAboutOther, p.Name), strings.TrimSpace(text)
	}
	var b strings.Builder
	b.WriteString(lead + "\n\n")
	if res.Topic == "pick" && len(got) > 1 {
		for _, i := range want {
			if q, ok := got[i]; ok {
				fmt.Fprintf(&b, "%d. %s\n", i+1, aboutFacts(q))
			}
		}
	} else {
		fmt.Fprintf(&b, "%d. %s\n", k+1, aboutFacts(p))
	}
	b.WriteString("\n" + CopyDiscoveryFooter + "\n")
	if len(offer.Options) > 1 {
		fmt.Fprintf(&b, CopyAboutAskOr, p.Name)
	} else {
		fmt.Fprintf(&b, CopyAboutAsk, p.Name)
	}
	o := *offer
	o.Suggest, o.SuggestWords, o.AtUnix = k+1, words, r.now().Unix()
	r.saveOffer(ctx, id, &o)
	return r.Reply(ctx, in, b.String(), "")
}

// singleOptions: the indexes of the offer's single-place options.
func singleOptions(offer *DiscoveryOffer) []int {
	var out []int
	for i, opt := range offer.Options {
		if len(opt) == 1 {
			out = append(out, i)
		}
	}
	return out
}

// aboutDetails looks the wanted options up again by id (rating, reviews,
// price level, hours), in parallel, each counted against the daily cap.
// Index → place; a failed or refused lookup is left out. pre: lookups made
// earlier in this message, used as they are (a failed one is not retried).
func (r *Router) aboutDetails(ctx context.Context, id Identity, offer *DiscoveryOffer, want []int, pre map[int]llm.Place) map[int]llm.Place {
	out := map[int]llm.Place{}
	if pre != nil {
		for _, i := range want {
			if p, ok := pre[i]; ok {
				out[i] = p
			}
		}
		return out
	}
	if r.Places == nil {
		return out
	}
	day := r.now().In(r.loc()).Format("2006-01-02")
	lctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, i := range want {
		ok, err := r.DiscoveryStore.TakePlacesCall(lctx, id.ID, day, r.limits().PerDay)
		if err != nil || !ok {
			log.Printf("[channel] about details identity=%s: no lookup (cap or count failed: %v)", id.ID, err)
			continue
		}
		wg.Add(1)
		go func(i int, pid string) {
			defer wg.Done()
			p, err := r.Places.AboutDetails(lctx, pid)
			if err != nil || !p.InNYC || p.Name == "" || llm.InstructionName(p.Name) {
				log.Printf("[channel] about details identity=%s option=%d: %v", id.ID, i+1, err)
				return
			}
			mu.Lock()
			out[i] = p
			mu.Unlock()
		}(i, offer.Options[i][0])
	}
	wg.Wait()
	return out
}
