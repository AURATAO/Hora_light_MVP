//go:build liveeval

package main

// Live eval of the chat's understanding steps other than the agent
// (cmd/agenttest grades that one): ParseTask with the production prompt,
// SplitErrands, NeedsPurchase and Classify, through the same llm code the
// router calls. It calls a real model and costs money, so it only builds
// with the tag:
//
//	set -a; source .env; set +a
//	EVAL_MODEL=anthropic:claude-sonnet-5-5 go test -tags liveeval -run TestChatParseEval -v .
//	EVAL_MODEL=baseten:zai-org/GLM-5.3-Flash EVAL_OUT=/tmp/glm.json go test -tags liveeval -run TestChatParseEval -v .
//
// EVAL_RUNS (default 1) repeats every case. No fallback: the model named is
// the model graded. Failing cases are reported, not fatal — the numbers are
// the output; EVAL_OUT writes them as JSON.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

type parseEvalCase struct {
	N        int    `json:"n"`
	Step     string `json:"step"`
	Note     string `json:"note"`
	Input    string `json:"input"`
	Category []string
	Loc1     *string  `json:"loc1"`
	Loc2     *string  `json:"loc2"`
	Sched    *bool    `json:"scheduled"`
	Desc     []string `json:"description"`
	TitleNot []string `json:"title_not"`
	Errands  int      `json:"errands"`
	Purchase *bool    `json:"purchase"`
	Intent   string   `json:"intent"`
}

type parseEvalResult struct {
	Case          int    `json:"case"`
	Run           int    `json:"run"`
	Step          string `json:"step"`
	Grade         string `json:"grade"` // PASS | FAIL | UNSAFE | ERROR
	Why           string `json:"why,omitempty"`
	LatencyMS     int64  `json:"latency_ms"`
	InTok         int    `json:"in_tokens"`
	CacheReadTok  int    `json:"cache_read_tokens"`
	CacheWriteTok int    `json:"cache_write_tokens"`
	OutTok        int    `json:"out_tokens"`
}

// usageRecorder is a Provider that keeps the last call's usage: Classify
// and NeedsPurchase do not return it.
type usageRecorder struct {
	llm.Provider
	mu   sync.Mutex
	last llm.Usage
}

func (r *usageRecorder) CompleteJSON(ctx context.Context, system, user, schemaName string, schema map[string]any, maxTokens int) (string, llm.Usage, error) {
	out, u, err := r.Provider.CompleteJSON(ctx, system, user, schemaName, schema, maxTokens)
	r.mu.Lock()
	r.last = u
	r.mu.Unlock()
	return out, u, err
}

func TestChatParseEval(t *testing.T) {
	spec, err := llm.ParseSpec(os.Getenv("EVAL_MODEL"))
	if err != nil {
		t.Fatalf("EVAL_MODEL: %v", err)
	}
	b, err := os.ReadFile("testdata/chat_parse_eval.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []json.RawMessage `json:"cases"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	var cases []parseEvalCase
	for _, raw := range f.Cases {
		var c parseEvalCase
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		var cat struct {
			Category []string `json:"category"`
		}
		if err := json.Unmarshal(raw, &cat); err != nil {
			t.Fatal(err)
		}
		c.Category = cat.Category
		cases = append(cases, c)
	}
	runs := 1
	if v, err := strconv.Atoi(os.Getenv("EVAL_RUNS")); err == nil && v > 0 {
		runs = v
	}
	p := llm.ProvidersFromEnv()
	rec := &usageRecorder{Provider: p.Provider(spec)}
	model := &llm.Model{Primary: rec}

	var results []parseEvalResult
	for run := 1; run <= runs; run++ {
		for _, c := range cases {
			r := runParseCase(model, rec, c)
			r.Run = run
			if r.Grade != "PASS" {
				t.Logf("%-6s #%d %s %q: %s", r.Grade, c.N, c.Step, firstLine(c.Input), r.Why)
			}
			results = append(results, r)
		}
	}
	t.Logf("model %s", spec)
	for _, line := range parseEvalSummary(results) {
		t.Log(line)
	}
	if out := os.Getenv("EVAL_OUT"); out != "" {
		j, err := json.MarshalIndent(map[string]any{"model": spec.String(), "at": time.Now().UTC().Format(time.RFC3339), "results": results}, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, j, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func runParseCase(model *llm.Model, rec *usageRecorder, c parseEvalCase) parseEvalResult {
	r := parseEvalResult{Case: c.N, Step: c.Step}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	start := time.Now()
	fail := func(grade, why string) { r.Grade, r.Why = grade, why }
	r.Grade = "PASS"
	switch c.Step {
	case "parse":
		d, _, err := model.ParseTask(ctx, aiTaskParserSystemPrompt(time.Now().In(nycLocation)), c.Input)
		if err != nil {
			fail("ERROR", err.Error())
			break
		}
		gradeDraft(d, c, fail)
	case "split":
		e, _, err := model.SplitErrands(ctx, c.Input)
		switch {
		case err != nil:
			fail("ERROR", err.Error())
		case len(e) != c.Errands:
			fail("FAIL", fmt.Sprintf("%d errands, want %d", len(e), c.Errands))
		}
	case "purchase":
		yes, err := model.NeedsPurchase(ctx, c.Input)
		switch {
		case err != nil:
			fail("ERROR", err.Error())
		case yes != *c.Purchase:
			fail("FAIL", fmt.Sprintf("purchase %v, want %v", yes, *c.Purchase))
		}
	case "classify":
		got, err := model.Classify(ctx, c.Input)
		switch {
		case err != nil:
			fail("ERROR", err.Error())
		case string(got) != c.Intent:
			fail("FAIL", fmt.Sprintf("intent %s, want %s", got, c.Intent))
		}
	default:
		fail("ERROR", "unknown step "+c.Step)
	}
	rec.mu.Lock()
	u := rec.last
	rec.mu.Unlock()
	r.LatencyMS = u.Latency.Milliseconds()
	if r.LatencyMS == 0 {
		r.LatencyMS = time.Since(start).Milliseconds()
	}
	r.InTok, r.CacheReadTok, r.CacheWriteTok, r.OutTok = u.InputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.OutputTokens
	return r
}

// gradeDraft: UNSAFE first (time, invented stop, status title), then FAIL.
func gradeDraft(d llm.TaskDraft, c parseEvalCase, fail func(string, string)) {
	low := strings.ToLower
	if c.Sched != nil && d.Scheduled != *c.Sched {
		fail("UNSAFE", fmt.Sprintf("scheduled %v (%s), want %v", d.Scheduled, d.ScheduledTime, *c.Sched))
		return
	}
	for _, w := range c.TitleNot {
		if strings.Contains(low(d.Title), w) {
			fail("UNSAFE", fmt.Sprintf("title %q has %q", d.Title, w))
			return
		}
	}
	for _, s := range []struct {
		want *string
		got  string
		name string
	}{{c.Loc1, d.Location1, "location_1"}, {c.Loc2, d.Location2, "location_2"}} {
		switch {
		case s.want == nil:
		case *s.want == "" && strings.TrimSpace(s.got) != "":
			fail("UNSAFE", fmt.Sprintf("%s %q invented", s.name, s.got))
			return
		case *s.want != "" && !strings.Contains(low(s.got), low(*s.want)):
			fail("FAIL", fmt.Sprintf("%s %q, want ~%q", s.name, s.got, *s.want))
			return
		}
	}
	if len(c.Category) > 0 {
		ok := false
		for _, k := range c.Category {
			ok = ok || d.Category == k
		}
		if !ok {
			fail("FAIL", fmt.Sprintf("category %s, want %v", d.Category, c.Category))
			return
		}
	}
	for _, w := range c.Desc {
		if !strings.Contains(low(d.Description), low(w)) {
			fail("FAIL", fmt.Sprintf("description %q lacks %q", d.Description, w))
			return
		}
	}
}

func parseEvalSummary(results []parseEvalResult) []string {
	steps := map[string][]parseEvalResult{}
	for _, r := range results {
		steps[r.Step] = append(steps[r.Step], r)
	}
	var names []string
	for s := range steps {
		names = append(names, s)
	}
	sort.Strings(names)
	var out []string
	for _, s := range names {
		rs := steps[s]
		count := map[string]int{}
		var lat []int64
		var sum int64
		in, cr, cw, o := 0, 0, 0, 0
		for _, r := range rs {
			count[r.Grade]++
			if r.Grade != "ERROR" {
				lat = append(lat, r.LatencyMS)
				sum += r.LatencyMS
			}
			in, cr, cw, o = in+r.InTok, cr+r.CacheReadTok, cw+r.CacheWriteTok, o+r.OutTok
		}
		sort.Slice(lat, func(i, k int) bool { return lat[i] < lat[k] })
		avg, p95 := int64(0), int64(0)
		if len(lat) > 0 {
			avg, p95 = sum/int64(len(lat)), lat[(len(lat)*95+99)/100-1]
		}
		out = append(out, fmt.Sprintf("%-8s %d: %d PASS, %d FAIL, %d UNSAFE, %d ERROR; latency avg %dms p95 %dms; tokens in %d cache_read %d cache_write %d out %d",
			s, len(rs), count["PASS"], count["FAIL"], count["UNSAFE"], count["ERROR"], avg, p95, in, cr, cw, o))
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}
