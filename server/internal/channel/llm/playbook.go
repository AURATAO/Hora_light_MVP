package llm

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Errand playbooks (D-25): one config file, playbooks.json, with one entry
// per errand type — what it is recognised by, the questions customers ask,
// which sources an answer may use, what HO:RA asks before the booking
// summary, fixed notes for the supporter, the limits, and eval cases.
// Adding a type is a new entry plus its eval cases; no code per type.
//
// The model gets an entry's guidance and limits when it recognises the
// type (the discovery answer's "playbook" field, or the rule match on an
// errand's words). The asks, notes and summary lines are applied by rule.

//go:embed playbooks.json
var playbooksJSON []byte

// PlaybookAsk is one question asked before the booking summary.
type PlaybookAsk struct {
	Key      string `json:"key"`
	Question string `json:"question"`
	Optional bool   `json:"optional"`
	// Answer: "redraft" (errand words, re-parsed) or "note" (kept as a
	// supporter note, prefixed with Note).
	Answer string `json:"answer"`
	Note   string `json:"note"`
	SkipIf string `json:"skip_if"`
	skip   *regexp.Regexp
}

// PlaybookEval is one eval case (agenttest -playbooks, go test).
type PlaybookEval struct {
	Kind       string          `json:"kind"` // asks | discover | answer
	ID         string          `json:"id"`
	Msg        string          `json:"msg"`
	Playbook   string          `json:"playbook"`
	ExpectAsks []string        `json:"expect_asks"`
	HoraLine   string          `json:"hora_line"`
	Earlier    []string        `json:"earlier"`
	Place      string          `json:"place"`
	Place2     string          `json:"place2"`
	Question   string          `json:"question"`
	Topic      string          `json:"topic"`
	Web        []EvalWebSource `json:"web"`
	Expect     json.RawMessage `json:"expect"`
}

// EvalWebSource is a fixture web excerpt.
type EvalWebSource struct {
	Domain string `json:"domain"`
	Title  string `json:"title"`
	Text   string `json:"text"`
}

// Playbook is one errand type.
type Playbook struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Match            []string       `json:"match"`
	Unless           []string       `json:"unless"`
	TypicalQuestions []string       `json:"typical_questions"`
	Sources          []string       `json:"sources"`
	Guidance         string         `json:"guidance"`
	Asks             []PlaybookAsk  `json:"asks"`
	SupporterNotes   []string       `json:"supporter_notes"`
	SummaryLines     []string       `json:"summary_lines"`
	Limits           []string       `json:"limits"`
	Evals            []PlaybookEval `json:"evals"`
	match            *regexp.Regexp
	unless           *regexp.Regexp
}

// ScopeRules is the section a separate prompt fills (people, homes,
// refusals). Empty lists add nothing.
type ScopeRules struct {
	Status   string   `json:"_status"`
	People   []string `json:"people"`
	Homes    []string `json:"homes"`
	Refusals []string `json:"refusals"`
}

// EvalPlace is a fixture place for the answer evals (invented data).
type EvalPlace struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Address            string   `json:"address"`
	Rating             float64  `json:"rating"`
	Reviews            int      `json:"reviews"`
	PriceLevel         int      `json:"price_level"`
	PriceRange         string   `json:"price_range"`
	OpenNow            *bool    `json:"open_now"`
	Closes             string   `json:"closes"`
	Opens              string   `json:"opens"`
	WeekdayHours       []string `json:"weekday_hours"`
	ReviewSummary      string   `json:"review_summary"`
	GenerativeSummary  string   `json:"generative_summary"`
	Editorial          string   `json:"editorial"`
	Takeout            *bool    `json:"takeout"`
	Delivery           *bool    `json:"delivery"`
	DineIn             *bool    `json:"dine_in"`
	Vegetarian         *bool    `json:"vegetarian"`
	GoodForGroups      *bool    `json:"good_for_groups"`
	GoodForChildren    *bool    `json:"good_for_children"`
	WheelchairEntrance *bool    `json:"wheelchair_entrance"`
	Cards              *bool    `json:"cards"`
	NFC                *bool    `json:"nfc"`
	CashOnly           *bool    `json:"cash_only"`
	Website            string   `json:"website"`
	Lat                float64  `json:"lat"`
	Lng                float64  `json:"lng"`
}

// Facts turns a fixture into the facts production gets from Google.
func (e EvalPlace) Facts() PlaceFacts {
	f := PlaceFacts{
		Place: Place{ID: e.ID, Name: e.Name, Address: e.Address, FullAddress: e.Address, Rating: e.Rating, RatingCount: e.Reviews,
			PriceLevel: e.PriceLevel, OpenNow: e.OpenNow, ClosesAt: e.Closes, OpensAt: e.Opens, InNYC: true},
		PriceRange: e.PriceRange, WeekdayHours: e.WeekdayHours, Takeout: e.Takeout, Delivery: e.Delivery, DineIn: e.DineIn,
		Vegetarian: e.Vegetarian, GoodForGroups: e.GoodForGroups, GoodForChildren: e.GoodForChildren,
		WheelchairEntrance: e.WheelchairEntrance, AcceptsCards: e.Cards, AcceptsNFC: e.NFC, CashOnly: e.CashOnly,
		Website: e.Website, Lat: e.Lat, Lng: e.Lng,
	}
	if e.ReviewSummary != "" {
		f.ReviewSummary = &Summary{Text: e.ReviewSummary, Disclosure: "Summarized with Gemini", ReviewsURI: "https://maps.google.com/?cid=fixture"}
	}
	if e.GenerativeSummary != "" {
		f.GenerativeSummary = &Summary{Text: e.GenerativeSummary, Disclosure: "Summarized with Gemini"}
	}
	f.Editorial = e.Editorial
	return f
}

// PlaybookSet is the parsed file.
type PlaybookSet struct {
	Version    string     `json:"version"`
	ScopeRules ScopeRules `json:"scope_rules"`
	WebSources struct {
		WellKnown []string `json:"well_known"`
	} `json:"web_sources"`
	Playbooks  []Playbook  `json:"playbooks"`
	EvalPlaces []EvalPlace `json:"eval_places"`
}

var (
	defaultPlaybooks    *PlaybookSet
	defaultPlaybooksErr error
	playbooksOnce       sync.Once
)

// Playbooks is the embedded file, parsed and checked once.
func Playbooks() (*PlaybookSet, error) {
	playbooksOnce.Do(func() { defaultPlaybooks, defaultPlaybooksErr = ParsePlaybooks(playbooksJSON) })
	return defaultPlaybooks, defaultPlaybooksErr
}

// playbookSources: what an entry may list in "sources".
var playbookSources = map[string]bool{"places_facts": true, "places_summary": true, "web": true, "none": true}

// ParsePlaybooks parses and checks a playbook file: ids unique and plain,
// every regexp compiles, every ask has a question and a known answer kind,
// every entry has eval cases. An error names the entry.
func ParsePlaybooks(b []byte) (*PlaybookSet, error) {
	var s PlaybookSet
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("playbooks: %w", err)
	}
	if len(s.Playbooks) == 0 {
		return nil, fmt.Errorf("playbooks: no entries")
	}
	seen := map[string]bool{}
	for i := range s.Playbooks {
		p := &s.Playbooks[i]
		if !playbookIDRE.MatchString(p.ID) || seen[p.ID] {
			return nil, fmt.Errorf("playbooks: entry %d: bad or repeated id %q", i, p.ID)
		}
		seen[p.ID] = true
		if p.Name == "" || p.Guidance == "" || len(p.Evals) == 0 || len(p.Sources) == 0 {
			return nil, fmt.Errorf("playbooks: %s: name, guidance, sources and evals are required", p.ID)
		}
		for _, src := range p.Sources {
			if !playbookSources[src] {
				return nil, fmt.Errorf("playbooks: %s: unknown source %q", p.ID, src)
			}
		}
		if len(p.Match) > 0 {
			re, err := regexp.Compile(`(?i)\b(?:` + strings.Join(p.Match, "|") + `)\b`)
			if err != nil {
				return nil, fmt.Errorf("playbooks: %s: match: %w", p.ID, err)
			}
			p.match = re
		}
		if len(p.Unless) > 0 {
			re, err := regexp.Compile(`(?i)\b(?:` + strings.Join(p.Unless, "|") + `)\b`)
			if err != nil {
				return nil, fmt.Errorf("playbooks: %s: unless: %w", p.ID, err)
			}
			p.unless = re
		}
		keys := map[string]bool{}
		for j := range p.Asks {
			a := &p.Asks[j]
			if a.Key == "" || keys[a.Key] || a.Question == "" || (a.Answer != "redraft" && a.Answer != "note") {
				return nil, fmt.Errorf("playbooks: %s: ask %d: key, question and answer (redraft|note) are required", p.ID, j)
			}
			keys[a.Key] = true
			if a.Answer == "note" && a.Note == "" {
				return nil, fmt.Errorf("playbooks: %s: ask %s: a note answer needs its prefix", p.ID, a.Key)
			}
			if a.SkipIf != "" {
				re, err := regexp.Compile(a.SkipIf)
				if err != nil {
					return nil, fmt.Errorf("playbooks: %s: ask %s: skip_if: %w", p.ID, a.Key, err)
				}
				a.skip = re
			}
		}
		for j, e := range p.Evals {
			if e.Kind != "asks" && e.Kind != "discover" && e.Kind != "answer" {
				return nil, fmt.Errorf("playbooks: %s: eval %d: unknown kind %q", p.ID, j, e.Kind)
			}
		}
	}
	return &s, nil
}

var playbookIDRE = regexp.MustCompile(`^[a-z][a-z_]{1,30}$`)

// IDs: every entry's id, in file order (the discovery schema's enum).
func (s *PlaybookSet) IDs() []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s.Playbooks))
	for i, p := range s.Playbooks {
		out[i] = p.ID
	}
	return out
}

// ByID is the entry with that id, nil when none.
func (s *PlaybookSet) ByID(id string) *Playbook {
	if s == nil || id == "" {
		return nil
	}
	for i := range s.Playbooks {
		if s.Playbooks[i].ID == id {
			return &s.Playbooks[i]
		}
	}
	return nil
}

// Match is the first entry whose match words appear in the errand's words
// and whose unless words do not (file order decides between two), nil
// when none. Rule only.
func (s *PlaybookSet) Match(text string) *Playbook {
	if s == nil {
		return nil
	}
	for i := range s.Playbooks {
		if p := &s.Playbooks[i]; p.match != nil && p.match.MatchString(text) && (p.unless == nil || !p.unless.MatchString(text)) {
			return p
		}
	}
	return nil
}

// AsksFor: the entry's questions the person's words have not answered yet
// (skip_if does not match), in file order.
func (p *Playbook) AsksFor(words string) []PlaybookAsk {
	if p == nil {
		return nil
	}
	var out []PlaybookAsk
	for _, a := range p.Asks {
		if a.skip != nil && a.skip.MatchString(words) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// HasSource: the entry allows this source.
func (p *Playbook) HasSource(src string) bool {
	if p == nil {
		return src != "web" // no entry: Google's data only
	}
	for _, s := range p.Sources {
		if s == src {
			return true
		}
	}
	return false
}

// PromptBlock is what the model is told for a recognised entry: the scope
// rules first (when filled), then the entry's guidance and limits.
func (s *PlaybookSet) PromptBlock(p *Playbook) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	if s != nil {
		rules := append(append(append([]string{}, s.ScopeRules.People...), s.ScopeRules.Homes...), s.ScopeRules.Refusals...)
		if len(rules) > 0 {
			b.WriteString("Scope rules:\n")
			for _, r := range rules {
				b.WriteString("- " + r + "\n")
			}
		}
	}
	fmt.Fprintf(&b, "Errand type: %s.\n%s\n", p.Name, p.Guidance)
	if len(p.Limits) > 0 {
		b.WriteString("Limits:\n")
		for _, l := range p.Limits {
			b.WriteString("- " + l + "\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// EvalPlace is the fixture with that id, nil when none.
func (s *PlaybookSet) EvalPlace(id string) *EvalPlace {
	if s == nil {
		return nil
	}
	for i := range s.EvalPlaces {
		if s.EvalPlaces[i].ID == id {
			return &s.EvalPlaces[i]
		}
	}
	return nil
}
