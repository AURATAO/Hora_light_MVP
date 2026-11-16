package channel

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"hora-auth/internal/channel/llm"
)

// Answers from real sources (D-25). A question about the options shown
// ("Ok nolita pizza, what is the best flavor?", "are they open Sunday?") is
// answered with real data only, then turned into an errand:
//
//	ask → answer from sources → "Want your supporter to get …?" → draft → summary + YES
//
// Sources, in order: Google's fields (hours, takeout, payment… rendered here
// by rule), Google's summaries (review, generative, editorial — the model
// writes one or two sentences from them), then, when the errand type allows
// it and Google had nothing relevant, Anthropic web search on the place's
// own site and well-known sources. Nothing found is said honestly, with the
// supporter-asks-on-site errand offered. What nobody can know from here
// ("is there a line right now?") is a check errand.
//
// The rule check (AnswerOK) holds every name, number, price, rating and item
// in the model's answer to the sources; anything else is fixed wording.
// Nothing is stored: summaries and excerpts are fetched again for each
// question (Google's terms). The offer keeps the items the person can say
// YES to — their order, not Google's text.
//
// Safety is unchanged: an answer never books, never states HO:RA's price,
// never promises a time. A YES to the offer starts a normal draft, whose
// summary asks its own YES.

// WebLimits caps web search (env, WebLimitsFromEnv).
type WebLimits struct {
	PerQuestion int // searches one question may make; 0 = web search off
	PerDay      int // per person per New York day
}

// WebLimitsFromEnv:
//
//	CHANNEL_WEB_SEARCH_PER_QUESTION   default 1 (0 turns web search off)
//	CHANNEL_WEB_SEARCH_PER_DAY        default 5
func WebLimitsFromEnv() WebLimits {
	l := WebLimits{PerQuestion: 1, PerDay: 5}
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CHANNEL_WEB_SEARCH_PER_QUESTION"))); err == nil && n >= 0 && n <= 3 {
		l.PerQuestion = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CHANNEL_WEB_SEARCH_PER_DAY"))); err == nil && n >= 0 {
		l.PerDay = n
	}
	return l
}

// WebQuota is the per-person daily count of web searches (PGStore).
type WebQuota interface {
	TakeWebSearch(ctx context.Context, identityID, day string, perDay int) (bool, error)
}

// answersOn: questions about the options are answered from sources.
func (r *Router) answersOn() bool {
	return r.discoveryOn() && r.Playbooks != nil && r.Facts != nil
}

// Copy: answers. Every line is fixed; only AnswerReply's model sentence is
// not, and it passes AnswerOK first.
const (
	CopyOfferItems     = "Want your supporter to get %s at %s?"
	CopyOfferCheck     = "Want me to send a supporter to %s to check and text you what they find?"
	CopyOfferAsk       = "Want me to send a supporter to %s to ask and text you what they find?"
	CopyLiveUnknown    = "I can't see that from here: Google Maps doesn't show it live."
	CopyNothingFound   = "I couldn't find that for %s on Google Maps or their website."
	CopyAllergy        = "For allergies, your supporter can ask staff, but I can't guarantee it."
	CopyNoMenuDraft    = "I don't have %s's menu. You can ask your supporter to get their most popular %s."
	CopyNoMenu         = "I don't have their menu, but your supporter can get their most popular %s. Want me to set that up at %s?"
	CopyFromGoogle     = "From Google Maps."
	CopyFromSite       = "From %s" // the cited page, shortest form ("joespizzanyc.com/menu"); no period, so the phone's link ends at the URL
	CopyHoursChange    = "Hours can change."
	CopyHoursNone      = "Google Maps doesn't list %s's hours."
	CopyPriceLevel     = "Google Maps lists %s as %s."
	CopyPriceRange     = "Google Maps lists %s as %s, about %s a person."
	CopyPriceNone      = "Google Maps doesn't list prices for %s."
	CopyClosest        = "From %s, %s is closest (about %s)."
	CopyClosestDown    = "I can't measure distances right now. Here are the addresses:"
	CopyFactUnknown    = "Google Maps doesn't say %s."
	CopyCompareFacts   = "Here's how they compare on Google Maps."
	descriptionGetLine = "Get: %s"
	noteAskThere       = "Ask there: %s"
)

// AnswerQuestion is one question about the options, as the rule and the
// model read it.
type AnswerQuestion struct {
	Text     string
	Topic    string
	Item     string
	Origin   string
	Playbook *llm.Playbook
	// Facts: the options looked up again, by option index; Asked: the one
	// asked about (-1: all of them); Compare: the ones to compare.
	Facts   map[int]llm.PlaceFacts
	Asked   int
	Compare []int
	Now     time.Time
	// NoOffer: no "Want me to set that up…?" — a draft is already open and
	// its own question follows (a question asked back, 2026-11-07).
	NoOffer bool
}

// AnswerDeps are what an answer may call.
type AnswerDeps struct {
	Answerer llm.Answerer
	Web      llm.WebSearcher
	// AllowWeb counts one web search against the daily cap (nil: none).
	AllowWeb       func(ctx context.Context) bool
	WebPerQuestion int    // > 0 turns the web step on (one search of the place's own site)
	ScopeAndRules  string // the playbook's prompt block
	Geocode        func(ctx context.Context, addr string) (lat, lng float64, err error)
}

// AnswerReply is the reply and what a YES to it sets up.
type AnswerReply struct {
	Text    string
	Suggest int      // option index (0-based) a YES sets up; -1 none
	Items   []string // what the supporter is to get (from sources)
	Check   bool     // a YES sends someone to check / ask
	Ask     string   // the question the supporter asks there
	// For reports and traces.
	Source string // places_facts | places_summary (editorial) | web | none | live
	// OfferIn: the offer is already in the answer's own copy (CopyNoMenu).
	OfferIn     bool
	ModelCalls  int
	WebSearches int
	Refused     bool // the model's answer failed AnswerOK: fixed wording sent
	// RefusedAnswer: the text that failed (eval reports only; never sent).
	RefusedAnswer string
	Usage         llm.Usage
	WebUsage      llm.Usage
}

var vegRE = regexp.MustCompile(`(?i)\bveg(?:etarian|an|gie|gies)?\b|\bplant[- ]based\b|\bmeat\b`)

var allergyRE = regexp.MustCompile(`(?i)\b(?:allerg\w*|anaphyla\w*|epipen|celiac|coeliac|gluten|nuts?|peanuts?|tree nuts?|dairy|lactose|shellfish|sesame|soy|eggs? allergy|nut[- ]free|gluten[- ]free)\b`)

// BuildAnswer makes the reply for one question. The router and the eval
// runner call this same function.
func BuildAnswer(ctx context.Context, d AnswerDeps, q AnswerQuestion) AnswerReply {
	rep := AnswerReply{Suggest: -1}
	idx := answerOrder(q)
	if len(idx) == 0 {
		rep.Text, rep.Source = CopyAboutNoData, "none"
		return rep
	}
	k := idx[0]
	if q.Asked < 0 && (q.Topic == "pick" || q.Topic == "menu" || q.Topic == "other") {
		ps := map[int]llm.Place{}
		for i, f := range q.Facts {
			ps[i] = f.Place
		}
		if b := bestRated(ps); b >= 0 {
			k = b
		}
	}
	p := q.Facts[k]
	rep.Suggest = k
	var lines []string
	var cited []string // web: the cited pages, shortest form
	allergy := allergyRE.MatchString(q.Text)
	usedGoogleData := false

	switch q.Topic {
	case "live":
		rep.Source, rep.Check, rep.Ask = "live", true, strings.TrimSpace(q.Text)
		lines = append(lines, CopyLiveUnknown)
	case "hours":
		rep.Source, usedGoogleData = "places_facts", true
		lines = append(lines, hoursLine(p, q.Text, q.Now))
	case "price":
		rep.Source, usedGoogleData = "places_facts", true
		lines = append(lines, priceLine(p))
	case "dietary":
		rep.Source, usedGoogleData = "places_facts", true
		if !allergy || vegRE.MatchString(q.Text) {
			lines = append(lines, flagLine(p.Vegetarian, p.Name+" serves vegetarian food", p.Name+" doesn't list vegetarian food", "whether "+p.Name+" serves vegetarian food"))
		}
	case "takeout_delivery":
		rep.Source, usedGoogleData = "places_facts", true
		lines = append(lines, flagLine(p.Takeout, p.Name+" does takeout", p.Name+" doesn't do takeout", "whether "+p.Name+" does takeout"),
			flagLine(p.Delivery, "it delivers", "it doesn't deliver", "whether it delivers")+" A supporter can pick it up and bring it to you.")
	case "groups_kids":
		rep.Source, usedGoogleData = "places_facts", true
		lines = append(lines, flagLine(p.GoodForGroups, p.Name+" is good for groups", p.Name+" isn't marked good for groups", "whether "+p.Name+" is good for groups"),
			flagLine(p.GoodForChildren, "it's good for children", "it isn't marked good for children", "whether it's good for children"))
	case "accessibility":
		rep.Source, usedGoogleData = "places_facts", true
		lines = append(lines, flagLine(p.WheelchairEntrance, p.Name+" has a wheelchair-accessible entrance", p.Name+" doesn't list a wheelchair-accessible entrance", "whether "+p.Name+" has a wheelchair-accessible entrance"))
	case "payment":
		rep.Source, usedGoogleData = "places_facts", true
		lines = append(lines, paymentLine(p))
	case "closest":
		rep.Source, usedGoogleData = "places_facts", true
		line, best := closestLine(ctx, d, q)
		lines = append(lines, line)
		if best >= 0 {
			rep.Suggest, k, p = best, best, q.Facts[best]
		}
	default: // pick, menu, compare, other: summaries, then the web
		rep.Source = "places_summary"
		places := []llm.PlaceFacts{p}
		if q.Topic == "compare" || (q.Topic == "pick" && q.Asked < 0) {
			places = nil
			for _, i := range idx {
				places = append(places, q.Facts[i])
			}
		}
		var sources []llm.Source
		for _, f := range places {
			sources = append(sources, cleanSources(llm.SummarySources(f, len(sources)))...)
		}
		// What to get: Google's editorial summary only when it names
		// something to get; else one search of the place's own website.
		namesItems := func(res llm.AnswerResult, ok bool) bool {
			return ok && (q.Topic != "menu" || len(places) > 1 || len(res.Items) > 0)
		}
		var res llm.AnswerResult
		ok := false
		if len(sources) > 0 {
			res, ok = modelAnswer(ctx, d, q, places, sources, &rep)
			ok = namesItems(res, ok)
		}
		own := llm.HostOfURL(p.Website)
		if !ok && len(places) == 1 && own != "" && q.Playbook.HasSource("web") && d.Web != nil && d.WebPerQuestion > 0 && d.AllowWeb != nil && q.Topic != "pick" && d.AllowWeb(ctx) {
			web, st, err := d.Web.SearchWeb(ctx, llm.WebRequest{Place: p.Name, Address: firstNonEmpty(p.FullAddress, p.Address), Question: q.Text,
				OwnDomain: own, MaxUses: 1})
			rep.WebSearches += st.Searches
			rep.WebUsage = st.Usage
			if err != nil {
				log.Printf("[channel] answer web search: %v", err)
			}
			web = cleanSources(web)
			for i := range web {
				web[i].Ref = fmt.Sprintf("s%d", len(sources)+i+1)
			}
			if len(web) > 0 {
				sources = web
				if res, ok = modelAnswer(ctx, d, q, places, sources, &rep); namesItems(res, ok) {
					rep.Source = "web"
				} else {
					ok = false
				}
			}
		}
		switch {
		case ok:
			lines = append(lines, res.Answer)
			if rep.Source == "web" {
				cited = citedPages(res.Sources, sources)
			}
			if len(places) == 1 {
				rep.Items = completeItems(res.Items, sources) // an order is for one place only
			}
			usedGoogleData = rep.Source != "web"
		case q.Topic == "compare" && len(places) > 1:
			rep.Source, usedGoogleData = "places_facts", true
			lines = append(lines, CopyCompareFacts)
		case q.Topic == "pick" && len(places) > 1:
			rep.Source, usedGoogleData = "places_facts", true
			lines = append(lines, fmt.Sprintf(CopyAboutPick, p.Name))
		case q.Topic == "menu" && !allergy && q.NoOffer:
			rep.Source = "none"
			lines = append(lines, fmt.Sprintf(CopyNoMenuDraft, p.Name, popularWord(q.Item)))
		case q.Topic == "menu" && !allergy:
			rep.Source = "none"
			item := popularWord(q.Item)
			lines = append(lines, fmt.Sprintf(CopyNoMenu, item, p.Name))
			rep.Check, rep.Ask, rep.OfferIn = true, fmt.Sprintf("what their most popular %s is, and get that", item), true
		default:
			rep.Source = "none"
			lines = append(lines, fmt.Sprintf(CopyNothingFound, p.Name))
			rep.Check, rep.Ask = true, strings.TrimSpace(q.Text)
		}
	}
	if allergy {
		lines = append(lines, CopyAllergy)
		// No order is proposed on an allergy question: the offer is to
		// have the supporter ask staff.
		rep.Items, rep.Check, rep.Ask = nil, true, strings.TrimSpace(q.Text)
	}

	// The message: one or two sentences, the option lines only where they
	// add something (several places, or hours and distance), the offer,
	// then one short attribution line when Google's data or the place's
	// website is shown. No links.
	var b strings.Builder
	b.WriteString(strings.Join(lines, " "))
	var shown []int
	switch {
	case q.Topic == "compare" || (q.Topic == "pick" && q.Asked < 0):
		shown = idx
	case q.Topic == "hours" || q.Topic == "live" || q.Topic == "closest":
		shown = []int{k}
	}
	if len(shown) > 0 {
		b.WriteString("\n")
	}
	for _, i := range shown {
		fmt.Fprintf(&b, "\n%d. %s", i+1, aboutFacts(q.Facts[i].Place))
	}
	if q.NoOffer {
		rep.Suggest, rep.Items, rep.Check, rep.Ask = -1, nil, false, ""
	} else if !rep.OfferIn {
		b.WriteString("\n\n")
		switch {
		case len(rep.Items) > 0:
			fmt.Fprintf(&b, CopyOfferItems, joinItems(rep.Items), p.Name)
		case rep.Check && rep.Source == "live":
			fmt.Fprintf(&b, CopyOfferCheck, p.Name)
		case rep.Check:
			fmt.Fprintf(&b, CopyOfferAsk, p.Name)
		case len(q.Facts) > 1:
			fmt.Fprintf(&b, CopyAboutAskOr, p.Name)
		default:
			fmt.Fprintf(&b, CopyAboutAsk, p.Name)
		}
	}
	if a := attributionLine(cited, usedGoogleData || len(shown) > 0, len(shown) > 0 || q.Topic == "hours"); a != "" {
		b.WriteString("\n" + a)
	}
	rep.Text = b.String()
	return rep
}

// answerOrder: the options to answer about — the one asked, the ones to
// compare, else every option looked up, in order.
func answerOrder(q AnswerQuestion) []int {
	if q.Asked >= 0 {
		if _, ok := q.Facts[q.Asked]; ok && q.Topic != "compare" {
			return []int{q.Asked}
		}
	}
	var out []int
	if q.Topic == "compare" && len(q.Compare) > 0 {
		for _, c := range q.Compare {
			if _, ok := q.Facts[c-1]; ok {
				out = append(out, c-1)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	for i := 0; i < llm.MaxOptions; i++ {
		if _, ok := q.Facts[i]; ok {
			out = append(out, i)
		}
	}
	return out
}

// modelAnswer asks the model and checks its answer by rule.
func modelAnswer(ctx context.Context, d AnswerDeps, q AnswerQuestion, places []llm.PlaceFacts, sources []llm.Source, rep *AnswerReply) (llm.AnswerResult, bool) {
	if d.Answerer == nil || len(sources) == 0 {
		return llm.AnswerResult{}, false
	}
	var names []string
	for _, f := range places {
		names = append(names, f.Name)
	}
	actx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := d.Answerer.Answer(actx, llm.AnswerRequest{Question: q.Text, Topic: q.Topic, Item: q.Item, Places: names, Sources: sources, Playbook: d.ScopeAndRules})
	rep.ModelCalls++
	rep.Usage = addUsage(rep.Usage, res.Usage)
	if err != nil {
		log.Printf("[channel] answer model: %v", err)
		return res, false
	}
	if !res.Found {
		return res, false
	}
	if !AnswerOK(res, sources, places, q.Text) {
		rep.Refused, rep.RefusedAnswer = true, res.Answer
		log.Printf("[channel] answer refused by rule (%d chars, %d items) — fixed wording", len(res.Answer), len(res.Items))
		return res, false
	}
	return res, true
}

func addUsage(a, b llm.Usage) llm.Usage {
	a.Model = firstNonEmpty(b.Model, a.Model)
	a.InputTokens += b.InputTokens
	a.OutputTokens += b.OutputTokens
	a.CacheReadTokens += b.CacheReadTokens
	a.CacheWriteTokens += b.CacheWriteTokens
	a.Latency += b.Latency
	return a
}

// injectionRE: source text written as an instruction to a model or to the
// customer about HO:RA. Such a source is dropped before the model sees it.
var injectionRE = regexp.MustCompile(`(?i)\b(?:system|assistant|developer)\s*:|\bignore (?:all |any |the |your |previous |prior )+(?:instructions|rules)\b|\breply\s+(?:with\s+)?"?yes\b|\btell the (?:customer|user)\b|\bho:?ra\b|\b(?:you are|act as) (?:an? )?(?:ai|assistant|model)\b`)

// cleanSources drops sources that read as instructions.
func cleanSources(ss []llm.Source) []llm.Source {
	var out []llm.Source
	for _, s := range ss {
		if injectionRE.MatchString(s.Text) {
			log.Printf("[channel] answer source dropped: reads as an instruction (%s)", s.Kind)
			continue
		}
		out = append(out, s)
	}
	return out
}

// Rules for the model's answer.
var (
	answerAskRE     = regexp.MustCompile(`(?i)[?？]|\b(?:yes|y/n|confirm|book(?:ed|ing)?|reply|respond|text back|reserve|reservation)\b`)
	answerBanRE     = regexp.MustCompile(`(?i)\b(?:ho:?ra|fees?|free|guarantee[sd]?|safe(?:ly)?|allergen|nut[- ]free|gluten[- ]free|in stock|sold out|no line|short line|no wait)\b|https?://|www\.|\d{3}[-. ]\d{4}|\d{7,}`)
	answerMoneyRE   = regexp.MustCompile(`[$€£]\s?\d+(?:[.,]\d{1,2})?`)
	answerNumberRE  = regexp.MustCompile(`\d+(?:[.,]\d+)?`)
	answerWordRE    = regexp.MustCompile(`[\pL][\pL'’-]*`)
	answerSentStart = regexp.MustCompile(`(?:^|[.!:;]\s+)([\pL][\pL'’-]*)`)
	answerItemRE    = regexp.MustCompile(`^[\pL\d][\pL\d'’ &-]{1,40}$`)
)

// answerCommon: capitalised words an answer may use that no source has to.
var answerCommon = map[string]bool{"i": true, "google": true, "maps": true, "gemini": true, "reviews": true, "reviewers": true, "their": true, "they": true, "the": true,
	"people": true, "it": true, "its": true, "a": true, "an": true, "menu": true, "website": true, "nyc": true, "new": true, "york": true}

// AnswerOK: the model's answer may be sent. Every price, number and
// capitalised name in it is in a source, a place's name or the question;
// every item is in a source; no question, YES, booking, HO:RA price, time
// promise, stock or allergy claim, link or phone number.
func AnswerOK(res llm.AnswerResult, sources []llm.Source, places []llm.PlaceFacts, question string) bool {
	a := strings.TrimSpace(res.Answer)
	if a == "" || utf8.RuneCountInString(a) > 320 || strings.Contains(a, "\n") {
		return false
	}
	if answerAskRE.MatchString(a) || answerBanRE.MatchString(a) || leadPromiseRE.MatchString(a) || InstructionWords(a) {
		return false
	}
	var corpus strings.Builder
	for _, s := range sources {
		corpus.WriteString(" " + s.Text)
	}
	var names strings.Builder
	for _, p := range places {
		names.WriteString(" " + p.Name + " " + p.Address)
	}
	src := normText(corpus.String())
	known := src + " " + normText(names.String()) + " " + normText(question)
	for _, m := range answerMoneyRE.FindAllString(a, -1) {
		if !strings.Contains(strings.ReplaceAll(corpus.String(), " ", ""), strings.ReplaceAll(m, " ", "")) {
			return false
		}
	}
	ratings := map[string]bool{}
	for _, p := range places {
		if p.Rating > 0 {
			ratings[fmt.Sprintf("%.1f", p.Rating)] = true
		}
	}
	rawKnown := corpus.String() + " " + names.String() + " " + question
	for _, n := range answerNumberRE.FindAllString(a, -1) {
		if !ratings[n] && !strings.Contains(rawKnown, n) {
			return false
		}
	}
	// Capitalised words that do not start a sentence are names: a dish, a
	// place, a brand. Each must be known.
	starts := map[int]bool{}
	for _, m := range answerSentStart.FindAllStringSubmatchIndex(a, -1) {
		starts[m[2]] = true
	}
	for _, m := range answerWordRE.FindAllStringIndex(a, -1) {
		w := a[m[0]:m[1]]
		r, _ := utf8.DecodeRuneInString(w)
		if !unicode.IsUpper(r) || starts[m[0]] || answerCommon[strings.ToLower(w)] {
			continue
		}
		base := strings.TrimSuffix(strings.TrimSuffix(w, "'s"), "’s") // "Levain Bakery's"
		if !strings.Contains(" "+known+" ", " "+normText(base)+" ") {
			return false
		}
	}
	for _, it := range res.Items {
		if !answerItemRE.MatchString(it) || InstructionWords(it) || leadMoneyRE.MatchString(it) || !strings.Contains(" "+src+" ", " "+normText(it)+" ") {
			return false
		}
	}
	refs := map[string]bool{}
	for _, s := range sources {
		refs[s.Ref] = true
	}
	for _, r := range res.Sources {
		if !refs[r] {
			return false
		}
	}
	return true
}

// normText: lower case, words only, single spaces.
func normText(s string) string {
	s = strings.ToLower(strings.NewReplacer("’", "'", "–", " ", "—", " ").Replace(s))
	return strings.Join(strings.Fields(nameKeyRE.ReplaceAllString(strings.ReplaceAll(s, "'", ""), " ")), " ")
}

// attributionLine: "From joespizzanyc.com/menu" for an answer from the
// place's own site (Anthropic's web search terms: the cited source shown
// and clickable — a phone links a texted URL), else "From Google Maps."
// when Google's data is shown ("Hours can change." after hours), else "".
func attributionLine(cited []string, google, hours bool) string {
	var parts []string
	switch {
	case len(cited) > 0:
		parts = append(parts, fmt.Sprintf(CopyFromSite, joinAnd(cited)))
	case google:
		parts = append(parts, CopyFromGoogle)
	}
	if hours {
		parts = append(parts, CopyHoursChange)
	}
	return strings.Join(parts, " ")
}

// citedPages: the web pages the answer used, each in its shortest form —
// the domain, or domain/path for a page below the home page. Two pages
// of one site are that site's domain.
func citedPages(used []string, sources []llm.Source) []string {
	want := map[string]bool{}
	for _, r := range used {
		want[r] = true
	}
	var hosts []string
	pages := map[string][]string{}
	for _, s := range sources {
		if s.Kind != llm.SourceWeb || !want[s.Ref] {
			continue
		}
		host, page := shortURL(s.URL, s.Label)
		if host == "" {
			continue
		}
		if _, ok := pages[host]; !ok {
			hosts = append(hosts, host)
		}
		if !slices.Contains(pages[host], page) {
			pages[host] = append(pages[host], page)
		}
	}
	var out []string
	for _, h := range hosts {
		if len(pages[h]) == 1 {
			out = append(out, pages[h][0])
		} else {
			out = append(out, h)
		}
	}
	return out
}

// sitePathRE: a path short and plain enough to text ("/menu", "/menu/lunch").
var sitePathRE = regexp.MustCompile(`^(?:/[A-Za-z0-9._~-]+){1,3}$`)

// shortURL: a cited URL as host ("joespizzanyc.com", no scheme or www.)
// and page ("joespizzanyc.com/menu"; the host for the home page, or for a
// path too long or odd to text). No query or fragment.
func shortURL(raw, label string) (host, page string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		if h := strings.ToLower(strings.TrimPrefix(label, "www.")); hostRE.MatchString(h) {
			return h, h
		}
		return "", ""
	}
	host = strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if !hostRE.MatchString(host) {
		return "", ""
	}
	p := strings.TrimSuffix(u.EscapedPath(), "/")
	if p == "" || len(p) > 30 || !sitePathRE.MatchString(p) {
		return host, host
	}
	return host, host + p
}

var hostRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+$`)

// popularWord: what "their most popular …" is — the customer's own word
// for it, a slice for pizza, else "item".
func popularWord(item string) string {
	w := strings.ToLower(strings.TrimSpace(item))
	switch {
	case w == "pizza" || w == "pizzas" || w == "slices":
		return "slice"
	case !AboutItemOK(w):
		return "item"
	}
	return w
}

// joinAnd: "a", "a and b", "a, b and c".
func joinAnd(w []string) string {
	switch len(w) {
	case 0:
		return ""
	case 1:
		return w[0]
	}
	return strings.Join(w[:len(w)-1], ", ") + " and " + w[len(w)-1]
}

// itemKinds: the words that say what kind of thing an item is, plural to
// singular — "vodka" is a flavor, "vodka slice" is what a supporter asks for.
var itemKinds = map[string]string{
	"slice": "slice", "slices": "slice", "pie": "pie", "pies": "pie", "square": "square", "squares": "square",
	"pizza": "pizza", "pizzas": "pizza", "roll": "roll", "rolls": "roll", "bagel": "bagel", "bagels": "bagel",
	"sandwich": "sandwich", "sandwiches": "sandwich", "taco": "taco", "tacos": "taco", "burrito": "burrito", "burritos": "burrito",
	"dumpling": "dumpling", "dumplings": "dumpling", "bun": "bun", "buns": "bun", "cookie": "cookie", "cookies": "cookie",
	"croissant": "croissant", "croissants": "croissant", "donut": "donut", "donuts": "donut", "doughnut": "doughnut", "doughnuts": "doughnut",
	"muffin": "muffin", "muffins": "muffin", "scone": "scone", "scones": "scone", "cupcake": "cupcake", "cupcakes": "cupcake",
	"cake": "cake", "cakes": "cake", "bowl": "bowl", "bowls": "bowl", "burger": "burger", "burgers": "burger", "wrap": "wrap", "wraps": "wrap",
	"latte": "latte", "lattes": "latte", "smoothie": "smoothie", "smoothies": "smoothie",
}

var itemKindAlt = func() string {
	var w []string
	for k := range itemKinds {
		w = append(w, k)
	}
	sort.Slice(w, func(i, j int) bool { return len(w[i]) > len(w[j]) })
	return strings.Join(w, "|")
}()

// completeItems gives a bare item the kind word its source uses for it:
// "plain, spinach and vodka slices" → "plain slice", "spinach slice",
// "vodka slice". Only through a list ("x, y and z slices"), never across
// a sentence; an item that already names a kind, or ends in s ("garlic
// knots"), is kept as it is.
func completeItems(items []string, sources []llm.Source) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, completeItem(it, sources))
	}
	return out
}

func completeItem(it string, sources []llm.Source) string {
	f := strings.Fields(strings.ToLower(it))
	if len(f) == 0 || len(f) > 3 {
		return it
	}
	last := f[len(f)-1]
	if _, ok := itemKinds[last]; ok || strings.HasSuffix(last, "s") {
		return it
	}
	word := `[\pL'’-]+`
	sep := `(?:\s*,\s*(?:(?:and|or)\s+)?|\s+(?:and|or|&)\s+)`
	re, err := regexp.Compile(`(?i)(?:^|[^\pL])` + regexp.QuoteMeta(strings.Join(f, " ")) + `(?:` + sep + word + `(?:\s+` + word + `)?)*?\s+(` + itemKindAlt + `)(?:[^\pL]|$)`)
	if err != nil {
		return it
	}
	for _, s := range sources {
		text := strings.Join(strings.Fields(s.Text), " ")
		if m := re.FindStringSubmatch(text); m != nil {
			return strings.TrimSpace(it) + " " + itemKinds[strings.ToLower(m[1])]
		}
	}
	return it
}

// joinItems: "a Margherita and a spicy vodka slice".
func joinItems(items []string) string {
	var w []string
	for _, it := range items {
		w = append(w, withArticle(it))
	}
	return joinAnd(w)
}

func withArticle(it string) string {
	low := strings.ToLower(it)
	if strings.HasSuffix(low, "s") || strings.HasPrefix(low, "the ") || strings.HasPrefix(low, "a ") || strings.HasPrefix(low, "an ") || unicode.IsDigit([]rune(low)[0]) {
		return it
	}
	if strings.ContainsRune("aeiou", rune(low[0])) {
		return "an " + it
	}
	return "a " + it
}

// ItemsNote is the supporter note for items said YES to: "Get: 1
// Margherita, 1 spicy vodka slice".
func ItemsNote(items []string) string {
	var w []string
	for _, it := range items {
		if unicode.IsDigit([]rune(it)[0]) {
			w = append(w, it)
		} else {
			w = append(w, "1 "+it)
		}
	}
	return fmt.Sprintf(descriptionGetLine, strings.Join(w, ", "))
}

// flagLine: one of Google's yes/no fields in words.
func flagLine(v *bool, yes, no, unknown string) string {
	switch {
	case v == nil:
		return fmt.Sprintf(CopyFactUnknown, unknown)
	case *v:
		return "Google Maps says " + yes + "."
	}
	return "Google Maps says " + no + "."
}

func paymentLine(p llm.PlaceFacts) string {
	switch {
	case p.CashOnly != nil && *p.CashOnly:
		return "Google Maps says " + p.Name + " is cash only."
	case p.AcceptsCards != nil && *p.AcceptsCards && p.AcceptsNFC != nil && *p.AcceptsNFC:
		return "Google Maps says " + p.Name + " takes cards and tap to pay."
	case p.AcceptsCards != nil && *p.AcceptsCards:
		return "Google Maps says " + p.Name + " takes cards."
	case p.AcceptsCards != nil:
		return "Google Maps says " + p.Name + " doesn't take cards."
	}
	return fmt.Sprintf(CopyFactUnknown, "how "+p.Name+" takes payment")
}

func priceLine(p llm.PlaceFacts) string {
	w := priceWord(p.PriceLevel)
	switch {
	case w != "" && p.PriceRange != "":
		return fmt.Sprintf(CopyPriceRange, p.Name, w, p.PriceRange)
	case w != "":
		return fmt.Sprintf(CopyPriceLevel, p.Name, w)
	case p.PriceRange != "":
		return fmt.Sprintf(CopyPriceRange, p.Name, "priced", p.PriceRange)
	}
	return fmt.Sprintf(CopyPriceNone, p.Name)
}

var weekdays = []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

var dayWordRE = regexp.MustCompile(`(?i)\b(sun|mon|tues?|wed(?:nes)?|thu(?:rs)?|fri|sat(?:ur)?)(?:day)?s?\b|\b(today|tonight|tomorrow)\b`)

// hoursLine: the day asked about ("open Sunday?"), else today, from
// Google's weekly hours ("Sunday: 12:00 – 10:00 PM").
func hoursLine(p llm.PlaceFacts, text string, now time.Time) string {
	day := now.Weekday()
	named := false
	if m := dayWordRE.FindStringSubmatch(text); m != nil {
		named = true
		switch strings.ToLower(m[2]) {
		case "tomorrow":
			day = (day + 1) % 7
		case "today", "tonight":
			named = false
		default:
			pre := strings.ToLower(m[1])[:2]
			for i, w := range weekdays {
				if strings.HasPrefix(w, pre) {
					day = time.Weekday(i)
				}
			}
		}
	}
	dayName := strings.ToUpper(weekdays[day][:1]) + weekdays[day][1:]
	for _, h := range p.WeekdayHours {
		d, hours, ok := strings.Cut(h, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(d), weekdays[day]) {
			continue
		}
		hours = strings.TrimSpace(hours)
		if strings.EqualFold(hours, "closed") {
			return fmt.Sprintf("Google Maps lists %s as closed on %s.", p.Name, dayName)
		}
		return fmt.Sprintf("Google Maps lists %s's %s hours as %s.", p.Name, dayName, hours)
	}
	if !named && p.OpenNow != nil {
		return fmt.Sprintf(CopyHoursNone, p.Name) + " Its status now is below."
	}
	return fmt.Sprintf(CopyHoursNone, p.Name)
}

// closestLine: the option nearest the customer's own address, by straight
// line from Google's coordinates. The origin must be the customer's words.
func closestLine(ctx context.Context, d AnswerDeps, q AnswerQuestion) (string, int) {
	origin := strings.TrimSpace(q.Origin)
	if origin == "" || d.Geocode == nil || !strings.Contains(normText(q.Text), normText(origin)) {
		return closestDown(q), -1
	}
	gctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	lat, lng, err := d.Geocode(gctx, origin+", New York, NY")
	if err != nil {
		log.Printf("[channel] closest: geocode: %v", err)
		return closestDown(q), -1
	}
	best, bestD := -1, 0.0
	for _, i := range answerOrder(AnswerQuestion{Facts: q.Facts, Asked: -1}) {
		f := q.Facts[i]
		if f.Lat == 0 && f.Lng == 0 {
			continue
		}
		dist := miles(lat, lng, f.Lat, f.Lng)
		if best < 0 || dist < bestD {
			best, bestD = i, dist
		}
	}
	if best < 0 {
		return closestDown(q), -1
	}
	return fmt.Sprintf(CopyClosest, origin, q.Facts[best].Name, milesText(bestD)), best
}

func closestDown(q AnswerQuestion) string {
	var b strings.Builder
	b.WriteString(CopyClosestDown)
	for _, i := range answerOrder(AnswerQuestion{Facts: q.Facts, Asked: -1}) {
		fmt.Fprintf(&b, " %d. %s, %s.", i+1, q.Facts[i].Name, q.Facts[i].Address)
	}
	return b.String()
}

func miles(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 3958.8
	rad := math.Pi / 180
	dlat, dlng := (lat2-lat1)*rad, (lng2-lng1)*rad
	a := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dlng/2)*math.Sin(dlng/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

func milesText(m float64) string {
	if m < 0.1 {
		return "a block away"
	}
	return fmt.Sprintf("%.1f miles in a straight line", m)
}

// ── the router's side ────────────────────────────────────────────────────

// factsFor looks the wanted options up again (Place Details Enterprise +
// Atmosphere), in parallel, each counted against the daily Places cap.
func (r *Router) factsFor(ctx context.Context, id Identity, offer *DiscoveryOffer, want []int) map[int]llm.PlaceFacts {
	out := map[int]llm.PlaceFacts{}
	if pre, ok := ctx.Value(factsPrefetchKey{}).(map[int]llm.PlaceFacts); ok {
		for _, i := range want {
			if f, ok := pre[i]; ok {
				out[i] = f
			}
		}
		return out // looked up earlier in this message: not again
	}
	day := r.now().In(r.loc()).Format("2006-01-02")
	lctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, i := range want {
		ok, err := r.DiscoveryStore.TakePlacesCall(lctx, id.ID, day, r.limits().PerDay)
		if err != nil || !ok {
			log.Printf("[channel] answer facts identity=%s: no lookup (cap or count failed: %v)", id.ID, err)
			continue
		}
		wg.Add(1)
		go func(i int, pid string) {
			defer wg.Done()
			f, err := r.Facts.PlaceFacts(lctx, pid)
			if err != nil || !f.InNYC || f.Name == "" || llm.InstructionName(f.Name) {
				log.Printf("[channel] answer facts identity=%s option=%d: %v", id.ID, i+1, err)
				return
			}
			mu.Lock()
			out[i] = f
			mu.Unlock()
		}(i, offer.Options[i][0])
	}
	wg.Wait()
	return out
}

// answerFromSources answers a question about the options (llm.DiscoverAbout
// with answers on). The options stay open; a YES sets up what the reply
// offers.
func (r *Router) answerFromSources(ctx context.Context, in Inbound, id Identity, offer *DiscoveryOffer, res llm.DiscoveryResult, text string) error {
	want := singleOptions(offer)
	asked := -1
	if res.Choice > 0 && res.Choice <= len(offer.Options) && len(offer.Options[res.Choice-1]) == 1 {
		asked = res.Choice - 1
		if res.Topic != "compare" && res.Topic != "closest" {
			want = []int{asked}
		}
	}
	facts := r.factsFor(ctx, id, offer, want)
	pb := r.Playbooks.ByID(firstNonEmpty(res.Playbook, offer.Playbook))
	day := r.now().In(r.loc()).Format("2006-01-02")
	deps := AnswerDeps{Answerer: r.Answerer, Web: r.Web, WebPerQuestion: r.WebLimits.PerQuestion,
		ScopeAndRules: r.Playbooks.PromptBlock(pb), Geocode: r.Geocode}
	if q, ok := r.DiscoveryStore.(WebQuota); ok && r.WebLimits.PerDay > 0 {
		deps.AllowWeb = func(c context.Context) bool {
			ok, err := q.TakeWebSearch(c, id.ID, day, r.WebLimits.PerDay)
			if err != nil {
				log.Printf("[channel] web quota identity=%s: %v", id.ID, err)
			}
			return err == nil && ok
		}
	}
	rep := BuildAnswer(ctx, deps, AnswerQuestion{Text: text, Topic: res.Topic, Item: res.Item, Origin: res.Origin, Playbook: pb,
		Facts: facts, Asked: asked, Compare: res.Compare, Now: r.now().In(r.loc())})
	log.Printf("[channel] answer identity=%s topic=%s playbook=%s looked_up=%d source=%s model_calls=%d web_searches=%d refused=%t suggest=%d items=%d check=%t",
		id.ID, res.Topic, firstNonEmpty(res.Playbook, offer.Playbook), len(facts), rep.Source, rep.ModelCalls, rep.WebSearches, rep.Refused, rep.Suggest+1, len(rep.Items), rep.Check)
	o := *offer
	o.Suggest, o.SuggestWords, o.SuggestItems, o.SuggestCheck, o.SuggestAsk = 0, "", nil, false, ""
	if rep.Suggest >= 0 {
		o.Suggest, o.SuggestItems, o.SuggestCheck, o.SuggestAsk = rep.Suggest+1, rep.Items, rep.Check, rep.Ask
	}
	if pb != nil {
		o.Playbook = pb.ID
	}
	o.AtUnix = r.now().Unix()
	r.saveOffer(ctx, id, &o)
	return r.Reply(ctx, in, rep.Text, "")
}

// chooseSuggested starts the draft a YES to an answer's offer sets up: the
// items as a supporter note ("Get: 1 Margherita, 1 spicy vodka slice"), or
// a check / ask-there errand with the question as its note.
func (r *Router) chooseSuggested(ctx context.Context, in Inbound, id Identity, conv Conversation, o *DiscoveryOffer) error {
	var notes []string
	extra := o.SuggestWords
	if len(o.SuggestItems) > 0 {
		notes = append(notes, ItemsNote(o.SuggestItems))
		extra = ItemsNote(o.SuggestItems)
	}
	oc := *o
	if o.SuggestCheck {
		oc.Check = true
		if o.SuggestAsk != "" {
			notes = append(notes, fmt.Sprintf(noteAskThere, o.SuggestAsk))
		}
	}
	return r.chooseOptionWith(ctx, in, id, conv, &oc, o.Suggest, extra, notes, "")
}

// ── playbooks on drafts: asks, supporter notes, summary lines ────────────

// draftPlaybook: the errand type of a draft — the one discovery recognised,
// a repeat or a route, the one an earlier draft had, else the rule match on
// the words.
func (r *Router) draftPlaybook(o draftOpts, prev *PendingDraft, source string) *llm.Playbook {
	if r.Playbooks == nil {
		return nil
	}
	switch {
	case o.playbook != "":
		return r.Playbooks.ByID(o.playbook)
	case o.prebuilt != nil || len(o.places) > 1:
		return r.Playbooks.ByID("repeat_multi")
	case prev != nil && prev.Playbook != "":
		return r.Playbooks.ByID(prev.Playbook)
	}
	if pb := r.Playbooks.Match(source); pb != nil {
		return pb // the rule, when it matches
	}
	return r.Playbooks.ByID(o.playbookHint) // else the classifier's reading
}

// applyPlaybookNotes adds the type's fixed supporter notes to the task the
// supporter reads (once each).
func applyPlaybookNotes(d *llm.TaskDraft, pb *llm.Playbook) {
	if pb == nil {
		return
	}
	for _, n := range pb.SupporterNotes {
		if !strings.Contains(d.Description, n) {
			d.Description = strings.TrimSpace(strings.TrimSpace(d.Description) + "\n\n" + n)
		}
	}
}

// playbookLines: the summary's fixed lines for the draft's type.
func (r *Router) playbookLines(p *PendingDraft) string {
	if r.Playbooks == nil || p == nil {
		return ""
	}
	var b strings.Builder
	if pb := r.Playbooks.ByID(p.Playbook); pb != nil {
		for _, l := range pb.SummaryLines {
			b.WriteString("\n" + l)
		}
	}
	if l := r.homeEntryLine(p); l != "" {
		b.WriteString("\n" + l)
	}
	return b.String()
}

// CopyPlaybookAskTail ends the questions before the summary.
const CopyPlaybookAskTail = "Or reply SKIP."

// playbookAsksText: the questions, in one message.
func playbookAsksText(asks []llm.PlaybookAsk) string {
	var q []string
	for _, a := range asks {
		q = append(q, a.Question)
	}
	s := strings.Join(q, " ")
	if !strings.Contains(s, "SKIP") {
		s += " " + CopyPlaybookAskTail
	}
	return s
}

// skipRE: the answer to the questions is "nothing to add".
var skipRE = regexp.MustCompile(`(?i)^\W*(?:skip|no|nope|nah|none|nothing|no thanks|no thank you|that'?s it|that'?s all|all good|it'?s fine|fine|n/?a|not really|go ahead|continue)\W*$`)

// answerPlaybookAsk reads the answer to the questions asked before the
// summary: skip → the summary; note-only questions → a supporter note;
// otherwise the words re-draft the errand (a time or an address lands in
// the booking, never only in a note).
func (r *Router) answerPlaybookAsk(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	p := conv.Pending
	asked := p.PlaybookAsk
	askText := playbookAsksText(r.pendingAsks(p))
	p.PlaybookAsk = nil
	pb := r.Playbooks.ByID(p.Playbook)
	switch {
	case IsDraftDecline(text):
		return r.dropDraft(ctx, in, conv)
	case IsCancelRequest(text) || IsWithdrawal(text):
		// "cancel the draft": dropped by rule, never read as the answer.
		return r.dropDraftWith(ctx, in, conv, CopyDroppedCancelHint)
	case (skipRE.MatchString(text) || len(r.Playbooks.AsksOf(pb)) == 0) && p.DetailsAsked:
		// Asked before where ("first"): skipping it leaves the place
		// still to ask — the request goes on to the details question.
		return r.startDraft(ctx, in, id, conv, p.Source)
	case skipRE.MatchString(text) || len(r.Playbooks.AsksOf(pb)) == 0:
		return r.afterAsks(ctx, in, id, conv)
	case IsBookingYes(text) || IsAffirmation(text):
		// Nothing to say yes to yet: the questions again (state unchanged).
		p.PlaybookAsk = asked
		return r.Reply(ctx, in, askText, "")
	}
	// Options shown for a question asked back (below): a pick makes the
	// draft at that place, with the type's questions asked afresh.
	if o := r.optionsSince(ctx, in, id, p); o != nil {
		if k := r.pickFromOffer(ctx, id, text, o).k; k > 0 {
			conv.Pending = nil
			return r.chooseOption(ctx, in, id, conv, o, k, "")
		}
	}
	// "What kind of pizza can I get? Any recommendations?" to "what should
	// they order?" asks for help with the question; it is not the answer
	// (production 2026-11-07: it was drafted as the order). Answered from
	// the draft's place, and the question is put again.
	if r.asksBack(ctx, askText, text) {
		p.PlaybookAsk = asked
		return r.answerAskedBack(ctx, in, id, conv, text, askText)
	}
	noteOnly := true
	var prefixes []string
	for _, a := range r.Playbooks.AsksOf(pb) {
		for _, k := range asked {
			if a.Key == k {
				noteOnly = noteOnly && a.Answer == "note"
				if a.Note != "" {
					prefixes = append(prefixes, strings.TrimSuffix(strings.TrimSpace(a.Note), ":"))
				}
			}
		}
	}
	// One answer to several note questions ("who's home?" and "anything to
	// avoid?") carries all their labels, so the supporter knows what it
	// answers.
	prefix := ""
	if len(prefixes) > 0 {
		prefix = strings.Join(prefixes, " / ") + ": "
	}
	if noteOnly && !noteCarriesChange(text) {
		note := prefix + strings.TrimSpace(text)
		if utf8.RuneCountInString(note) > maxNoteRunes {
			note = string([]rune(note)[:maxNoteRunes]) + "…"
		}
		p.Notes = append(p.Notes, note)
		if len(p.Notes) > MaxNotes {
			p.Notes = p.Notes[len(p.Notes)-MaxNotes:]
		}
		return r.afterAsks(ctx, in, id, conv)
	}
	return r.startDraft(ctx, in, id, conv, p.Source+"\n"+text)
}

// pendingAsks: the draft's questions still waiting on an answer.
func (r *Router) pendingAsks(p *PendingDraft) []llm.PlaybookAsk {
	if r.Playbooks == nil || p == nil || len(p.PlaybookAsk) == 0 {
		return nil
	}
	var asks []llm.PlaybookAsk
	for _, a := range r.Playbooks.AsksOf(r.Playbooks.ByID(p.Playbook)) {
		for _, k := range p.PlaybookAsk {
			if a.Key == k {
				asks = append(asks, a)
			}
		}
	}
	return asks
}

// afterAsks goes on from the type's questions as a draft does: the first
// name when there is none yet, then the budget question or the summary.
func (r *Router) afterAsks(ctx context.Context, in Inbound, id Identity, conv Conversation) error {
	if !id.Linked() && conv.PendingName == "" {
		conv.State = StateAwaitingName
		conv.Pending.NameAsked = true
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("afterAsks: %w", err)
		}
		return r.Reply(ctx, in, fmt.Sprintf(CopyAskName, conv.Pending.Headline()), "")
	}
	return r.continueDraft(ctx, in, id, conv)
}

// ── a question asked back (2026-11-07) ──────────────────────────────────

// orderQuestionRE: a reply that asks for help choosing what to get, by
// rule ("any recommendations?", "what kind of pizza can I get?", "what's
// good there?"). Anything else with a "?" is the model's to read.
var orderQuestionRE = regexp.MustCompile(`(?i)\b(?:recommend\w*|suggest\w*|what(?:'s| is| are) (?:good|best|popular|the best|on the menu)|what (?:kind|kinds|type|types|sort) of|what (?:do|would|should) (?:you|i|we|people) (?:get|order|recommend|suggest|usually)|what can (?:i|we|they) (?:get|order)|what do they (?:have|sell|serve|make)|(?:any|some) (?:ideas|picks|favou?rites)|(?:see|send|show) (?:me )?the menu|which (?:one|pizza|flavou?r|dish|item)s? (?:is|are|should|do))\b`)

// OrderQuestion: the rule alone (the eval runner reads it too).
func OrderQuestion(text string) bool { return orderQuestionRE.MatchString(text) }

// asksBack: the reply asks HO:RA for help with its question. The rule
// first; with a "?" the rule does not know, the configured model reads it.
func (r *Router) asksBack(ctx context.Context, asked, text string) bool {
	if OrderQuestion(text) {
		return true
	}
	if !strings.Contains(text, "?") || r.QuestionReader == nil {
		return false
	}
	yes, err := r.QuestionReader.ReadsAsQuestion(ctx, asked, text)
	if err != nil {
		log.Printf("[channel] reads as question: %v", err)
		return false // read as the answer, as before
	}
	return yes
}

// answerAskedBack answers a question asked back while the draft's own
// questions wait: from the draft's place when it has one (its Google data,
// with no offer of a new booking), else a search near the draft's words.
// The draft's question follows either way; nothing is booked.
func (r *Router) answerAskedBack(ctx context.Context, in Inbound, id Identity, conv Conversation, text, askText string) error {
	p := conv.Pending
	if len(p.Places) == 1 && p.Places[0].ID != "" && r.answersOn() {
		offer := &DiscoveryOffer{Kind: offerOptions, Options: [][]string{{p.Places[0].ID}}, Words: []string{p.Source}, Playbook: p.Playbook}
		pb := r.Playbooks.ByID(p.Playbook)
		facts := r.factsFor(ctx, id, offer, []int{0})
		deps := AnswerDeps{Answerer: r.Answerer, Web: r.Web, WebPerQuestion: r.WebLimits.PerQuestion,
			ScopeAndRules: r.Playbooks.PromptBlock(pb), Geocode: r.Geocode}
		day := r.now().In(r.loc()).Format("2006-01-02")
		if q, ok := r.DiscoveryStore.(WebQuota); ok && r.WebLimits.PerDay > 0 {
			deps.AllowWeb = func(c context.Context) bool {
				ok, err := q.TakeWebSearch(c, id.ID, day, r.WebLimits.PerDay)
				return err == nil && ok
			}
		}
		rep := BuildAnswer(ctx, deps, AnswerQuestion{Text: text, Topic: "menu", Playbook: pb, Facts: facts, Asked: 0, Now: r.now().In(r.loc()), NoOffer: true})
		log.Printf("[channel] asked back identity=%s place=draft source=%s model_calls=%d", id.ID, rep.Source, rep.ModelCalls)
		return r.Reply(ctx, in, rep.Text+"\n\n"+askText, "")
	}
	if r.discoveryOn() {
		// No place yet: a search near what the draft says, its words as the
		// earlier words. Options shown can be picked (optionsSince).
		log.Printf("[channel] asked back identity=%s place=search", id.ID)
		if handled, err := r.discoverTail(ctx, in, id, conv, text, &DiscoveryOffer{Words: []string{p.Source}}, true, true, askText); handled {
			return err
		}
	}
	return r.Reply(ctx, in, CopyAskedBackNoAnswer+"\n\n"+askText, "")
}

// CopyAskedBackNoAnswer: a question asked back that cannot be looked up.
const CopyAskedBackNoAnswer = "I can't look that up for this errand right now."

// optionsSince: options shown after the draft was made (a question asked
// back), still fresh and with no supporter writing since; nil none.
func (r *Router) optionsSince(ctx context.Context, in Inbound, id Identity, p *PendingDraft) *DiscoveryOffer {
	if !r.discoveryOn() {
		return nil
	}
	o := r.freshOffer(ctx, id)
	if o == nil || o.Kind != offerOptions || o.AtUnix < p.CreatedAtUnix {
		return nil
	}
	at := time.Unix(o.AtUnix, 0)
	if r.supporterSpokeSince(ctx, in, id, &at) {
		return nil
	}
	return o
}
