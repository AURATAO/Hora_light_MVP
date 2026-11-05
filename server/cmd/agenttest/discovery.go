package main

// Place discovery eval (D-23): `go run ./cmd/agenttest -discovery`.
//
// Every case goes through the PRODUCTION discovery code — llm.PlaceDiscovery
// (the same prompt, tool, schema and decoding) and channel.RenderDiscovery
// (the reply the person would get). Places come from the fixtures in
// discovery.json by default, so the cases are repeatable and cost no
// Google calls; -live-places sends the model's searches to Google instead
// (GOOGLE_MAPS_API_KEY with Places API (New); billed at Text Search
// Enterprise).
//
// Grades: PASS | FAIL (wrong action or missing check/reason/choice/options)
// | UNSAFE (the reply the person would get carries a YES question or a
// price, or shows a place the case forbids — exit 2) | ERROR (no answer:
// production sends "I can't search right now"). Reported apart: raw leads
// the rule refused (InfoLeadOK) — the reply then uses fixed text, so these
// are prompt quality, not exposure.

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"hora-auth/helpers"
	"hora-auth/internal/channel"
	"hora-auth/internal/channel/llm"
)

type fixturePlace struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Address  string   `json:"address"`
	Full     string   `json:"full"`
	Rating   float64  `json:"rating"`
	Reviews  int      `json:"reviews"`
	OpenNow  *bool    `json:"open_now"`
	Closes   string   `json:"closes"`
	Opens    string   `json:"opens"`
	InNYC    bool     `json:"in_nyc"`
	Keywords []string `json:"keywords"`
}

type dCase struct {
	N           int      `json:"n"`
	Group       string   `json:"group"`
	Msg         string   `json:"msg"`
	Earlier     []string `json:"earlier"`
	HoraLine    string   `json:"hora_line"`
	LastAddress string   `json:"last_address"`
	Past        []string `json:"past"`
	Expect      struct {
		Action     []string `json:"action"`
		Check      *bool    `json:"check"`
		Reason     []string `json:"reason"`
		Choice     int      `json:"choice"`
		Usual      []string `json:"usual"`
		MinOptions int      `json:"min_options"`
		MinStops   int      `json:"min_stops"`
	} `json:"expect"`
	ForbidPlaces []string `json:"forbid_places"`
	Note         string   `json:"note"`
}

type dFile struct {
	Readme []string       `json:"_readme"`
	Now    string         `json:"now"`
	Places []fixturePlace `json:"places"`
	Cases  []dCase        `json:"cases"`
}

type dResult struct {
	Case      int      `json:"case"`
	Run       int      `json:"run"`
	Group     string   `json:"group"`
	Msg       string   `json:"msg"`
	Grade     string   `json:"grade"`
	Why       string   `json:"why,omitempty"`
	Action    string   `json:"action"`
	Options   int      `json:"options"`
	MaxStops  int      `json:"max_stops"`
	Searches  int      `json:"searches"`
	Queries   []string `json:"queries,omitempty"`
	Lead      string   `json:"lead,omitempty"`
	LeadOK    bool     `json:"lead_ok"`
	Reply     string   `json:"reply,omitempty"`
	LatencyMS int64    `json:"latency_ms"`
	In        int      `json:"in"`
	CacheRead int      `json:"cache_read"`
	CacheW    int      `json:"cache_write"`
	Out       int      `json:"out"`
	CostUSD   float64  `json:"cost_usd"`
}

// fixtureSearch answers a search with the fixtures whose keywords appear
// in the query (Google's order: the file's), at most 5.
type fixtureSearch struct {
	places []fixturePlace
	mu     sync.Mutex
	asked  []string
}

func (f *fixtureSearch) SearchPlaces(_ context.Context, q string, openNow bool) ([]llm.Place, error) {
	f.mu.Lock()
	f.asked = append(f.asked, q)
	f.mu.Unlock()
	low := strings.ToLower(q)
	var out []llm.Place
	for _, p := range f.places {
		hit := false
		for _, k := range p.Keywords {
			hit = hit || strings.Contains(low, strings.ToLower(k))
		}
		if !hit || (openNow && p.OpenNow != nil && !*p.OpenNow) {
			continue
		}
		out = append(out, llm.Place{ID: p.ID, Name: p.Name, Address: p.Address, FullAddress: p.Full, Rating: p.Rating, RatingCount: p.Reviews,
			OpenNow: p.OpenNow, ClosesAt: p.Closes, OpensAt: p.Opens, InNYC: p.InNYC})
		if len(out) == 5 {
			break
		}
	}
	return out, nil
}

func (f *fixtureSearch) PlaceDetails(context.Context, string) (llm.Place, error) {
	return llm.Place{}, fmt.Errorf("not used by the eval")
}
func (f *fixtureSearch) PlaceName(context.Context, string) (string, error) {
	return "", fmt.Errorf("not used by the eval")
}

// liveSearch is Google, as production calls it.
type liveSearch struct {
	loc   *time.Location
	mu    sync.Mutex
	asked []string
}

func (l *liveSearch) SearchPlaces(ctx context.Context, q string, openNow bool) ([]llm.Place, error) {
	l.mu.Lock()
	l.asked = append(l.asked, q)
	l.mu.Unlock()
	ps, err := helpers.PlacesSearch(ctx, q, openNow, 5, l.loc)
	if err != nil {
		return nil, err
	}
	out := make([]llm.Place, len(ps))
	for i, p := range ps {
		out[i] = llm.Place{ID: p.ID, Name: p.Name, Address: p.Address, FullAddress: p.FullAddress, Rating: p.Rating, RatingCount: p.RatingCount,
			OpenNow: p.OpenNow, ClosesAt: p.ClosesAt, OpensAt: p.OpensAt, InNYC: p.Area == helpers.AreaInside}
	}
	return out, nil
}
func (l *liveSearch) PlaceDetails(context.Context, string) (llm.Place, error) {
	return llm.Place{}, fmt.Errorf("not used by the eval")
}
func (l *liveSearch) PlaceName(context.Context, string) (string, error) {
	return "", fmt.Errorf("not used by the eval")
}

// Prices for the cost line (USD). Sonnet 5.5: $2 / $10 per million tokens,
// cache reads $0.20, 5-minute cache writes 1.25 × input. Google: Text Search
// Enterprise $35 / 1,000 (rating + hours fields), Place Details Pro
// $17 / 1,000 — list prices at the lowest volume tier, before the monthly
// free calls.
const (
	usdIn, usdOut, usdCacheRead, usdCacheWrite = 2.0 / 1e6, 10.0 / 1e6, 0.20 / 1e6, 2.50 / 1e6
	usdSearch, usdDetails                      = 0.035, 0.017
)

// replyBadRE: what the reply the person gets must never carry — a YES, a
// question, a price. Checked with the places' own names taken out (those are
// Google's, shown as Google has them).
var replyBadRE = regexp.MustCompile(`(?i)\byes\b|[?？]|[$€£]|\b(?:dollars?|usd|price|estimate)\b`)

func runDiscovery(path string, spec llm.Spec, providers llm.Providers, runs, only int, group string, verbose bool, jsonOut string, parallel int, delay, timeout time.Duration, live bool) int {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var f dFile
	if err := json.Unmarshal(b, &f); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		return 1
	}
	a, ok := providers.Provider(spec).(*llm.Anthropic)
	if !ok || providers.AnthropicKey == "" {
		fmt.Fprintln(os.Stderr, "discovery runs on Anthropic only: -model anthropic:<model> and ANTHROPIC_API_KEY")
		return 1
	}
	nyc, _ := time.LoadLocation("America/New_York")
	now := time.Now().In(nyc)
	if f.Now != "" {
		if t, err := time.ParseInLocation("2006-01-02T15:04", f.Now, nyc); err == nil {
			now = t
		}
	}
	var todo []dCase
	for _, c := range f.Cases {
		if (only == 0 || c.N == only) && strings.HasPrefix(c.Group, group) {
			todo = append(todo, c)
		}
	}
	type job struct {
		c   dCase
		run int
	}
	jobs := make(chan job)
	var mu sync.Mutex
	var results []dResult
	var wg sync.WaitGroup
	total := len(todo) * runs
	for w := 0; w < max(1, parallel); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				var ps llm.PlaceSearcher
				var asked func() []string
				if live {
					l := &liveSearch{loc: nyc}
					ps, asked = l, func() []string { return l.asked }
				} else {
					fx := &fixtureSearch{places: f.Places}
					ps, asked = fx, func() []string { return fx.asked }
				}
				r := runDiscoveryOne(&llm.PlaceDiscovery{Model: a, Places: ps}, j.c, j.run, now, timeout)
				r.Queries = asked()
				time.Sleep(delay)
				mu.Lock()
				results = append(results, r)
				fmt.Fprintf(os.Stderr, "\r%d/%d done", len(results), total)
				mu.Unlock()
			}
		}()
	}
	for run := 1; run <= runs; run++ {
		for _, c := range todo {
			jobs <- job{c, run}
		}
	}
	close(jobs)
	wg.Wait()
	fmt.Fprintln(os.Stderr)
	sort.Slice(results, func(i, k int) bool {
		if results[i].Case != results[k].Case {
			return results[i].Case < results[k].Case
		}
		return results[i].Run < results[k].Run
	})
	code := discoveryReport(results, verbose)
	if jsonOut != "" {
		out, err := json.MarshalIndent(map[string]any{"model": spec.String(), "prompt": llm.DiscoveryPromptVersion, "live_places": live,
			"at": time.Now().UTC().Format(time.RFC3339), "results": results}, "", " ")
		if err == nil {
			err = os.WriteFile(jsonOut, out, 0o644)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "report:", err)
			return max(code, 1)
		}
	}
	return code
}

func runDiscoveryOne(d *llm.PlaceDiscovery, c dCase, run int, now time.Time, timeout time.Duration) dResult {
	r := dResult{Case: c.N, Run: run, Group: c.Group, Msg: c.Msg, LeadOK: true}
	req := llm.DiscoveryRequest{Text: c.Msg, Now: now, Earlier: c.Earlier, HoraLine: c.HoraLine, LastAddress: c.LastAddress,
		PastErrands: c.Past, MaxSearches: 3}
	var res llm.DiscoveryResult
	var err error
	backoff := 2 * time.Second
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		t0 := time.Now()
		res, err = d.Discover(ctx, req)
		r.LatencyMS = time.Since(t0).Milliseconds()
		cancel()
		if err == nil || attempt == 6 || !retryable(err) {
			break
		}
		time.Sleep(backoff + time.Duration(rand.Int63n(int64(time.Second))))
		backoff *= 2
	}
	u := res.Usage
	r.In, r.CacheRead, r.CacheW, r.Out, r.Searches = u.InputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.OutputTokens, res.Searches
	r.CostUSD = float64(u.InputTokens)*usdIn + float64(u.OutputTokens)*usdOut + float64(u.CacheReadTokens)*usdCacheRead +
		float64(u.CacheWriteTokens)*usdCacheWrite + float64(res.Searches)*usdSearch
	if err != nil {
		r.Grade, r.Why = "ERROR", err.Error()
		return r
	}
	r.Action, r.Options, r.Lead = string(res.Action), len(res.Options), res.Lead
	r.LeadOK = res.Lead == "" || channel.InfoLeadOK(res.Lead)
	for _, o := range res.Options {
		r.MaxStops = max(r.MaxStops, len(o))
	}
	if res.Action == llm.DiscoverInfo && len(res.Options) > 0 {
		r.Reply = channel.RenderDiscovery(res)
		bare := r.Reply
		for _, o := range res.Options {
			for _, p := range o {
				bare = strings.ReplaceAll(bare, p.Name, "")
				for _, id := range c.ForbidPlaces {
					if p.ID == id {
						r.Grade, r.Why = "UNSAFE", "shows forbidden place "+id
						return r
					}
				}
			}
		}
		if m := replyBadRE.FindString(bare); m != "" {
			r.Grade, r.Why = "UNSAFE", fmt.Sprintf("reply carries %q", m)
			return r
		}
	}
	r.Grade, r.Why = "PASS", ""
	fail := func(why string) {
		if r.Grade == "PASS" {
			r.Grade, r.Why = "FAIL", why
		}
	}
	if !contains(c.Expect.Action, string(res.Action)) {
		fail(fmt.Sprintf("action %s, want %v", res.Action, c.Expect.Action))
	}
	if res.Action == llm.DiscoverInfo {
		if c.Expect.Check != nil && res.Check != *c.Expect.Check {
			fail(fmt.Sprintf("check %t, want %t", res.Check, *c.Expect.Check))
		}
		if len(res.Options) < c.Expect.MinOptions {
			fail(fmt.Sprintf("%d options, want ≥ %d", len(res.Options), c.Expect.MinOptions))
		}
		if r.MaxStops < c.Expect.MinStops {
			fail(fmt.Sprintf("longest option %d stops, want ≥ %d", r.MaxStops, c.Expect.MinStops))
		}
	}
	if res.Action == llm.DiscoverOutOfScope && len(c.Expect.Reason) > 0 && !contains(c.Expect.Reason, res.Reason) {
		fail(fmt.Sprintf("reason %s, want %v", res.Reason, c.Expect.Reason))
	}
	if res.Action == llm.DiscoverChoose && c.Expect.Choice > 0 && res.Choice != c.Expect.Choice {
		fail(fmt.Sprintf("choice %d, want %d", res.Choice, c.Expect.Choice))
	}
	if res.Action == llm.DiscoverUsual && len(c.Expect.Usual) > 0 && !contains(c.Expect.Usual, fmt.Sprintf("u%d", res.Usual+1)) {
		fail(fmt.Sprintf("usual u%d, want %v", res.Usual+1, c.Expect.Usual))
	}
	return r
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func discoveryReport(results []dResult, verbose bool) int {
	count := map[string]int{}
	code, badLeads := 0, 0
	var cost, infoCost float64
	var infos, searches int
	var lat []int64
	for _, r := range results {
		count[r.Grade]++
		mark := map[string]string{"PASS": "  ", "FAIL": "✗ ", "UNSAFE": "!!", "ERROR": "? "}[r.Grade]
		if r.Grade != "PASS" || verbose {
			fmt.Printf("%s #%d.%d [%s] %q → %s opts=%d stops=%d searches=%d  %s\n", mark, r.Case, r.Run, r.Group, r.Msg, r.Action, r.Options, r.MaxStops, r.Searches, r.Why)
			if verbose {
				fmt.Printf("      queries: %q\n", r.Queries)
				if r.Lead != "" {
					fmt.Printf("      lead (ok=%t): %s\n", r.LeadOK, r.Lead)
				}
			}
		}
		if !r.LeadOK {
			badLeads++
			if !verbose {
				fmt.Printf("   #%d.%d lead refused by rule (fixed text sent): %q\n", r.Case, r.Run, r.Lead)
			}
		}
		switch r.Grade {
		case "FAIL", "ERROR":
			code = max(code, 1)
		case "UNSAFE":
			code = max(code, 2)
		}
		if r.Grade != "ERROR" {
			lat = append(lat, r.LatencyMS)
		}
		cost += r.CostUSD
		searches += r.Searches
		if r.Action == string(llm.DiscoverInfo) && r.Options > 0 {
			infos++
			infoCost += r.CostUSD
		}
	}
	fmt.Printf("\n%d results: %d PASS, %d FAIL, %d UNSAFE, %d ERROR; %d raw leads refused by rule\n", len(results), count["PASS"], count["FAIL"], count["UNSAFE"], count["ERROR"], badLeads)
	if len(lat) > 0 {
		sort.Slice(lat, func(i, k int) bool { return lat[i] < lat[k] })
		var sum int64
		for _, l := range lat {
			sum += l
		}
		fmt.Printf("latency: avg %dms, p95 %dms, max %dms\n", sum/int64(len(lat)), lat[(len(lat)*95+99)/100-1], lat[len(lat)-1])
	}
	if len(results) > 0 {
		fmt.Printf("cost: $%.4f over %d results ($%.4f each, %d Places searches)", cost, len(results), cost/float64(len(results)), searches)
		if infos > 0 {
			fmt.Printf("; an answer with options $%.4f on average (Sonnet + Places)", infoCost/float64(infos))
		}
		fmt.Println()
	}
	return code
}
