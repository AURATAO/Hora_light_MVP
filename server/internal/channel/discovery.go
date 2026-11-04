package channel

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
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
//     is looked up again before the draft.
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
	Kind string `json:"kind"` // offerOptions | offerAskLocation
	// Words: the person's own words for this search, oldest first.
	Words []string `json:"words"`
	// Options: the place ids of each option shown, in visiting order.
	Options [][]string `json:"options,omitempty"`
	Check   bool       `json:"check,omitempty"`
	AtUnix  int64      `json:"at"`
}

const (
	offerOptions     = "options"
	offerAskLocation = "ask_location"
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
	CopyDiscoveryFooter      = "Places, ratings and hours from Google Maps. Hours can change."
	CopyDiscoveryPick        = "Reply with a number and I'll set up the errand for a supporter."
	CopyDiscoveryPickOne     = "Reply 1 and I'll set up the errand for a supporter."
	CopyDiscoveryPickCheck   = "Reply with a number and I'll send a supporter to check and text you what they find."
	CopyDiscoveryLead        = "Here's what I found:"
	CopyDiscoveryLeadCheck   = "Here's where I can send someone to check:"
	CopyDiscoveryNothing     = "I couldn't find a good match for that. Tell me the store or address and I'll send someone."
	CopySearchDown           = "I can't search right now, but tell me the store or address and I'll send someone."
	CopySearchLimit          = "I've looked up a lot of places for you today, so I can't search more right now. Tell me the store or address and I'll send someone."
	CopyAskLocation          = "Where should I look? Send an address, cross streets or a neighborhood in NYC."
	CopyAskLocationLast      = "Where should I look? Send an address, cross streets or a neighborhood in NYC, or reply LAST to look near %s (your last errand)."
	CopyPlaceLookupDown      = "I can't look that place up right now. Send me its name and address and I'll set up the errand."
	CopyUsualNone            = "I don't see an earlier errand from you yet. Tell me what you need and I'll set it up."
	CopyUsualIntro           = "Same as last time, as a new booking:"
	CopyPlacesLineOne        = "Place: %s (from Google Maps)"
	CopyPlacesLineRoute      = "Stops, in order (from Google Maps): %s"
	CopyCheckLine            = "Your supporter goes, checks, and texts you what they find, with a photo."
	descriptionPlaceOne      = "Place (from Google Maps): %s, %s"
	descriptionPlaceRoute    = "Stops in order (from Google Maps):"
	descriptionCheck         = "Go and check, then text the requester what you find, with a photo. Buy only what they asked for, within the budget they set."
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
// should I look?". handled=false: the usual routing.
func (r *Router) routeDiscoveryFirst(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) (bool, error) {
	if !r.discoveryOn() {
		return false, nil
	}
	if IsUsualRequest(text) {
		return true, r.startUsual(ctx, in, id, conv, -1)
	}
	offer := r.freshOffer(ctx, id)
	if offer == nil {
		return false, nil
	}
	at := time.Unix(offer.AtUnix, 0)
	if r.supporterSpokeSince(ctx, in, id, &at) {
		// A supporter wrote after our answer: "2" may be theirs. The usual
		// routing (and the model, which sees both) decides.
		return false, nil
	}
	switch offer.Kind {
	case offerOptions:
		if k := pickOption(text, len(offer.Options), r.offerNames(ctx, id)); k > 0 {
			return true, r.chooseOption(ctx, in, id, conv, offer, k, "")
		}
	case offerAskLocation:
		if lastWordRE.MatchString(text) {
			if addr := r.lastAddress(ctx, id); addr != "" {
				text = "near " + addr
			}
		}
		return r.discover(ctx, in, id, conv, text, offer, true)
	}
	return false, nil
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
	return r.discover(ctx, in, id, conv, text, offer, strong)
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
	if o.Kind == offerAskLocation {
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
// the message to the usual routing.
func (r *Router) discover(ctx context.Context, in Inbound, id Identity, conv Conversation, text string, offer *DiscoveryOffer, strong bool) (bool, error) {
	lim := r.limits()
	req := llm.DiscoveryRequest{Text: text, Now: r.now().In(r.loc()), MaxSearches: lim.PerMessage}
	if offer != nil {
		req.Earlier = offer.Words
		req.HoraLine, _ = r.ourLastLineText(ctx, id)
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
	dctx, cancel := context.WithTimeout(ctx, lim.Timeout)
	res, err := r.Discovery.Discover(dctx, req)
	cancel()
	log.Printf("[channel] discovery identity=%s action=%s searches=%d options=%d model=%s latency=%s in=%d cached=%d out=%d err=%v",
		id.ID, res.Action, res.Searches, len(res.Options), res.Usage.Model, res.Usage.Latency.Round(time.Millisecond),
		res.Usage.InputTokens, res.Usage.CacheReadTokens, res.Usage.OutputTokens, err != nil)
	if err != nil {
		log.Printf("[channel] discovery identity=%s: %v", id.ID, err)
		if !strong {
			return false, nil
		}
		return true, r.Reply(ctx, in, CopySearchDown, "")
	}
	words := append(append([]string{}, req.Earlier...), text)
	switch res.Action {
	case llm.DiscoverOutOfScope:
		r.saveOffer(ctx, id, nil)
		return true, r.Reply(ctx, in, outOfScopeCopy(res.Reason), "")
	case llm.DiscoverAskLocation:
		r.saveOffer(ctx, id, &DiscoveryOffer{Kind: offerAskLocation, Words: lastWords(words), AtUnix: r.now().Unix()})
		if req.LastAddress != "" {
			return true, r.Reply(ctx, in, fmt.Sprintf(CopyAskLocationLast, req.LastAddress), "")
		}
		return true, r.Reply(ctx, in, CopyAskLocation, "")
	case llm.DiscoverUsual:
		return true, r.startUsual(ctx, in, id, conv, res.Usual)
	case llm.DiscoverChoose:
		if offer != nil && offer.Kind == offerOptions && res.Choice <= len(offer.Options) {
			return true, r.chooseOption(ctx, in, id, conv, offer, res.Choice, text)
		}
		return false, nil
	case llm.DiscoverInfo:
		if len(res.Options) == 0 {
			r.saveOffer(ctx, id, nil)
			if denied && res.Searches == 0 {
				return true, r.Reply(ctx, in, CopySearchLimit, "")
			}
			return true, r.Reply(ctx, in, CopyDiscoveryNothing, "")
		}
		o := &DiscoveryOffer{Kind: offerOptions, Words: lastWords(words), Check: res.Check, AtUnix: r.now().Unix()}
		for _, opt := range res.Options {
			var ids []string
			for _, p := range opt {
				ids = append(ids, p.ID)
			}
			o.Options = append(o.Options, ids)
		}
		r.saveOffer(ctx, id, o)
		return true, r.Reply(ctx, in, RenderDiscovery(res), "")
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
	t := strings.ToLower(strings.NewReplacer("’", "'", "‘", "'").Replace(text))
	t = pickPunctRE.ReplaceAllString(t, " ")
	var rest []string
	for _, w := range strings.Fields(t) {
		if !pickFillers[w] {
			rest = append(rest, w)
		}
	}
	if len(rest) == 1 {
		if k, ok := ordinals[rest[0]]; ok && k <= n {
			return k
		}
		if rest[0] == "last" && n > 0 {
			return n
		}
	}
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

// offerLineRE: an option line of a rendered answer — "2. Joe's Laundromat".
var offerLineRE = regexp.MustCompile(`^(\d)\. (.+)$`)

// offerNames reads the single-place options' names back from the answer
// we sent (the thread's record of it), so names are never stored apart
// from the message itself.
func (r *Router) offerNames(ctx context.Context, id Identity) []string {
	if r.Thread == nil {
		return nil
	}
	msgs, err := r.Thread.RecentMessages(ctx, id.Channel, id.Address, 8)
	if err != nil {
		log.Printf("[channel] offer names identity=%s: %v", id.ID, err)
		return nil
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Direction != "outbound" || !strings.Contains(m.Text, CopyDiscoveryFooter) {
			continue
		}
		var names []string
		for _, line := range strings.Split(m.Text, "\n") {
			mm := offerLineRE.FindStringSubmatch(line)
			if mm == nil {
				continue
			}
			k, _ := strconv.Atoi(mm[1])
			for len(names) < k-1 {
				names = append(names, "")
			}
			if len(names) == k-1 {
				name := mm[2]
				if strings.Contains(name, " stops, in this order:") {
					name = ""
				}
				names = append(names, name)
			}
		}
		return names
	}
	return nil
}

// chooseOption turns option k of the open offer into a normal draft. The
// places are looked up again by id (only ids are kept) and attached by
// rule; the words the parser reads are the person's own, plus the address.
func (r *Router) chooseOption(ctx context.Context, in Inbound, id Identity, conv Conversation, offer *DiscoveryOffer, k int, extra string) error {
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
	return r.startDraftOpts(ctx, in, id, conv, strings.Join(lines, "\n"), draftOpts{places: chosen, check: offer.Check})
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
