// agenttest grades the requester agent (D-18) against cases.json.
//
// It sends every case through the PRODUCTION agent code — the same system
// prompt (llm.AgentPrompt), json_schema (llm.AgentSchema), context
// rendering (llm.AgentUserMessage) and decoding (llm.DecodeProposal) the
// router uses — and shows, for each case with an approval question open,
// what the backend's RULES do with the message (channel.DecideApproval, the
// function the router acts on). In production the model is asked only when
// the rules leave the message to it ("agent"), and a model approve/decline
// there only gets the plain YES/NO question put again.
//
// Usage (from server/):
//
//	export LLM_API_KEY=...   # never commit it, never paste it in chat
//	export LLM_MODEL=...     # default zai-org/GLM-5.3-Flash
//	go run ./cmd/agenttest                     # all cases once
//	go run ./cmd/agenttest -runs 5             # money cases must pass every run
//	go run ./cmd/agenttest -case 19 -v         # one case, with the raw output
//	go run ./cmd/agenttest -group "C "         # one group (prefix)
//	go run ./cmd/agenttest -json cmd/agenttest/report.json   # machine-readable report (git-ignored there)
//
// Exit codes: 0 all pass; 1 failures (safe); 2 the model proposed approve or
// decline where forbidden (the prompt is wrong — production re-asks); 3 a
// case's "server" outcome disagrees with the rules (the backend or the case
// is wrong). `go run` reports any non-zero code as 1; build it
// (`go build -o agenttest ./cmd/agenttest`) to see the real code.
//
// Offline self-test (no model, no key): go test ./cmd/agenttest
//
// Optional: LLM_BASE_URL (default https://inference.baseten.co/v1),
// LLM_DISABLE_THINKING=1, LLM_TIMEOUT (default 60s), -parallel (default 1),
// -delay between requests (default 1s), -agent-only (ask the model only
// where production would: the rules leave the message to the agent).
// A 429 / 5xx is retried with backoff (up to 6 tries).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"hora-auth/internal/channel"
	"hora-auth/internal/channel/llm"
)

// ---------- case file ----------

type Line struct {
	Task       string `json:"task"`
	Text       string `json:"text"`
	MinutesAgo int    `json:"minutes_ago"`
	Answered   bool   `json:"answered"`
}

type Ctx struct {
	Tasks        []string `json:"tasks"`
	Open         string   `json:"open"`          // a request key: the approval question is armed
	OpenQuestion string   `json:"open_question"` // booking | budget | name (approval comes from Open)
	// QuestionNotLast: we sent something after the approval question (R-3).
	QuestionNotLast bool  `json:"question_not_last"`
	SupporterLine   *Line `json:"supporter_line"`
}

type Expect struct {
	Action    string   `json:"action"`
	ActionAny []string `json:"action_any"`
	Task      string   `json:"task"`
}

type Case struct {
	N      int      `json:"n"`
	Group  string   `json:"group"`
	Money  bool     `json:"money"`
	Ctx    Ctx      `json:"ctx"`
	Msg    string   `json:"msg"`
	Expect Expect   `json:"expect"`
	Forbid []string `json:"forbid"`
	// Server: what the backend's rules do with the message when an approval
	// question is open (see verdictName). Required on every case with Open.
	Server string `json:"server"`
	Note   string `json:"note"`
}

type File struct {
	Readme []string `json:"_readme"`
	Tasks  map[string]struct {
		Title     string `json:"title"`
		Supporter string `json:"supporter"`
	} `json:"tasks"`
	Requests map[string]struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	} `json:"requests"`
	Cases []Case `json:"cases"`
}

func load(path string) (File, error) {
	var f File
	b, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// ---------- context and rules ----------

const supporterWindowMinutes = 10 // D-17 I-P

// build renders a case's context with the router's own assembler
// (channel.AgentContextFor) — the model is tested on what it sees.
func build(f File, c Case) (llm.AgentContext, error) {
	keys := c.Ctx.Tasks
	if keys == nil {
		keys = []string{"T42"}
	}
	var active []channel.ActiveTask
	for i, k := range keys {
		t, ok := f.Tasks[k]
		if !ok {
			return llm.AgentContext{}, fmt.Errorf("case %d: unknown task %q", c.N, k)
		}
		active = append(active, channel.ActiveTask{ID: fmt.Sprintf("t%d", i+1), Title: t.Title, SupporterName: t.Supporter})
	}
	open := c.Ctx.OpenQuestion
	if c.Ctx.Open != "" {
		if _, ok := f.Requests[c.Ctx.Open]; !ok {
			return llm.AgentContext{}, fmt.Errorf("case %d: unknown request %q", c.N, c.Ctx.Open)
		}
		open = "approval"
	}
	var line channel.SupporterLine
	if l := c.Ctx.SupporterLine; l != nil {
		line = channel.SupporterLine{Text: l.Text, TaskID: l.Task, MinutesAgo: l.MinutesAgo, Answered: l.Answered}
	}
	return channel.AgentContextFor(active, open, line), nil
}

func labelIndex(label string, n int) int {
	var i int
	if _, err := fmt.Sscanf(label, "t%d", &i); err == nil && fmt.Sprintf("t%d", i) == label && i >= 1 && i <= n {
		return i - 1
	}
	return -1
}

// verdict is the backend's rule decision for a case with an approval open.
// The request belongs to the first errand.
func verdict(ac llm.AgentContext, c Case) channel.ApprovalVerdict {
	f := channel.ApprovalFacts{
		Text:           c.Msg,
		QuestionIsLast: !c.Ctx.QuestionNotLast,
	}
	if len(ac.Tasks) > 0 {
		name := ac.Tasks[0].Supporter
		f.Supporter = func() string { return name }
	}
	// I-P counts a line on an active task within the window, answered or
	// not (D-17 I-P as ruled) — read from the case, not from the model's view.
	if l := c.Ctx.SupporterLine; l != nil && labelIndex(l.Task, len(ac.Tasks)) >= 0 && l.MinutesAgo <= supporterWindowMinutes {
		f.SupporterLive = true
	}
	return channel.DecideApproval(f)
}

var verdictNames = map[channel.ApprovalVerdict]string{
	channel.VerdictNotAnAnswer:   "agent",
	channel.VerdictUnconfirmed:   "unconfirmed",
	channel.VerdictSentBefore:    "sent_before",
	channel.VerdictReask:         "reask",
	channel.VerdictApprove:       "approve",
	channel.VerdictApprovePassOn: "approve+passon",
	channel.VerdictDecline:       "decline",
	channel.VerdictPassOnKeep:    "passon",
	channel.VerdictToSupporter:   "to_supporter",
}

func verdictName(v channel.ApprovalVerdict) string {
	if n, ok := verdictNames[v]; ok {
		return n
	}
	return fmt.Sprintf("verdict(%d)", v)
}

// ---------- grading ----------

type Result struct {
	Case      int    `json:"case"`
	Run       int    `json:"run"`
	Group     string `json:"group"`
	Money     bool   `json:"money"`
	Msg       string `json:"msg"`
	Action    string `json:"action"` // as the model said it
	Task      string `json:"task"`
	Grade     string `json:"grade"` // PASS | SAME | FAIL | UNSAFE | ERROR
	Why       string `json:"why,omitempty"`
	Server    string `json:"server,omitempty"` // the rules' verdict (approval open)
	Outcome   string `json:"outcome"`          // what production would do
	LatencyMS int64  `json:"latency_ms"`       // the attempt that answered (what production would wait)
	TotalMS   int64  `json:"total_ms"`         // all attempts and backoff
	Retries   int    `json:"retries"`          // rate-limit / 5xx retries (production does not retry)
	Raw       string `json:"raw,omitempty"`
}

type rawProposal struct {
	Action string `json:"action"`
	Task   string `json:"task"`
}

// sameRoute: with exactly one errand active, the router sends relay, reply
// and ask to the same place — that errand's supporter (relay.go target
// chain; ToIntent maps all three to "for the supporter").
var sameRoute = map[string]bool{"relay": true, "reply": true, "ask": true}

// grade: PASS (an expected action), SAME (a different action with the same
// production outcome — one errand, relay/reply/ask), FAIL, UNSAFE.
func grade(c Case, raw string, nTasks int) (rawProposal, string, string) {
	var p rawProposal
	s := strings.TrimSpace(raw)
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		s = s[i : j+1]
	}
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return p, "FAIL", "unparseable output"
	}
	for _, f := range c.Forbid {
		if p.Action == f {
			if f == "approve" || f == "decline" {
				return p, "UNSAFE", "proposed " + f + " (forbidden here)"
			}
			return p, "FAIL", "proposed " + f + " (forbidden here)"
		}
	}
	want := c.Expect.ActionAny
	if c.Expect.Action != "" {
		want = append([]string{c.Expect.Action}, want...)
	}
	ok := len(want) == 0
	for _, w := range want {
		if p.Action == w {
			ok = true
		}
	}
	if !ok {
		if nTasks == 1 && sameRoute[p.Action] {
			for _, w := range want {
				if sameRoute[w] {
					return p, "SAME", fmt.Sprintf("action %q, want %s — same production outcome (one errand)", p.Action, strings.Join(want, "|"))
				}
			}
		}
		return p, "FAIL", fmt.Sprintf("action %q, want %s", p.Action, strings.Join(want, "|"))
	}
	if c.Expect.Task != "" && p.Action == "relay" && p.Task != c.Expect.Task {
		return p, "FAIL", fmt.Sprintf("task %q, want %q", p.Task, c.Expect.Task)
	}
	return p, "PASS", ""
}

// outcome is what production does: the rules' verdict when they decide;
// otherwise the agent's (normalised) proposal, where approve/decline only
// re-ask.
func outcome(server string, p llm.Proposal) string {
	if server != "" && server != "agent" {
		return "rules:" + server
	}
	switch p.Action {
	case llm.ActionApprove, llm.ActionDecline:
		return "reask (agent said " + string(p.Action) + ")"
	case llm.ActionRelay, llm.ActionReply:
		if p.Task >= 0 {
			return fmt.Sprintf("relay:t%d", p.Task+1)
		}
		return "relay:by-rule"
	}
	return string(p.Action)
}

// ---------- main ----------

func main() {
	var (
		casesPath = flag.String("cases", "cmd/agenttest/cases.json", "case file")
		runs      = flag.Int("runs", 1, "runs per case")
		only      = flag.Int("case", 0, "run one case")
		group     = flag.String("group", "", "run one group (prefix)")
		verbose   = flag.Bool("v", false, "show raw output")
		jsonOut   = flag.String("json", "", "write a JSON report here")
		parallel  = flag.Int("parallel", 1, "concurrent requests (raise only if your plan's rate limit allows)")
		delay     = flag.Duration("delay", time.Second, "pause between requests, per worker")
		agentOnly = flag.Bool("agent-only", false, "skip cases the rules decide (the model is not asked there in production)")
	)
	flag.Parse()
	f, err := load(*casesPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if bad := checkServer(f); len(bad) > 0 {
		for _, b := range bad {
			fmt.Fprintln(os.Stderr, "RULES:", b)
		}
		os.Exit(3)
	}
	key := firstEnv("LLM_API_KEY", "BASETEN_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "set LLM_API_KEY (never commit it, never paste it in chat)")
		os.Exit(1)
	}
	timeout := 60 * time.Second
	if v := os.Getenv("LLM_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			timeout = d
		}
	}
	model := &llm.OpenAICompat{
		BaseURL: orDefault(os.Getenv("LLM_BASE_URL"), "https://inference.baseten.co/v1"),
		Model:   orDefault(os.Getenv("LLM_MODEL"), "zai-org/GLM-5.3-Flash"),
		APIKey:  key,
	}
	if os.Getenv("LLM_DISABLE_THINKING") == "1" {
		model.ExtraBody = map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}}
	}

	var todo []Case
	for _, c := range f.Cases {
		if *agentOnly && c.Server != "" && c.Server != "agent" {
			continue
		}
		if (*only == 0 || c.N == *only) && strings.HasPrefix(c.Group, *group) {
			todo = append(todo, c)
		}
	}
	type job struct {
		c   Case
		run int
	}
	total := len(todo) * *runs
	jobs := make(chan job)
	var mu sync.Mutex
	var results []Result
	var wg sync.WaitGroup
	for w := 0; w < max(1, *parallel); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				r := runOne(model, f, j.c, j.run, timeout)
				time.Sleep(*delay)
				mu.Lock()
				results = append(results, r)
				fmt.Fprintf(os.Stderr, "\r%d/%d done", len(results), total)
				mu.Unlock()
			}
		}()
	}
	for run := 1; run <= *runs; run++ {
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
	code := report(results, *verbose)
	if *jsonOut != "" {
		for i := range results {
			if !*verbose {
				results[i].Raw = ""
			}
		}
		b, err := json.MarshalIndent(map[string]any{"model": model.Model, "prompt": "v0.3.2", "at": time.Now().UTC().Format(time.RFC3339), "results": results}, "", " ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "report:", err)
			os.Exit(max(code, 1))
		}
		if err := os.WriteFile(*jsonOut, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(max(code, 1))
		}
	}
	os.Exit(code)
}

func runOne(model *llm.OpenAICompat, f File, c Case, run int, timeout time.Duration) Result {
	r := Result{Case: c.N, Run: run, Group: c.Group, Money: c.Money, Msg: c.Msg}
	ac, err := build(f, c)
	if err != nil {
		r.Grade, r.Why = "ERROR", err.Error()
		return r
	}
	if c.Ctx.Open != "" {
		r.Server = verdictName(verdict(ac, c))
	}
	var raw string
	backoff := 2 * time.Second
	start := time.Now()
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		t0 := time.Now()
		raw, _, err = model.ProposeRaw(ctx, c.Msg, ac)
		r.LatencyMS = time.Since(t0).Milliseconds()
		cancel()
		if err == nil || attempt == 6 || !retryable(err) {
			break
		}
		r.Retries++
		time.Sleep(backoff + time.Duration(rand.Int63n(int64(time.Second))))
		backoff *= 2
	}
	r.TotalMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Grade, r.Why = "ERROR", err.Error()
		r.Outcome = outcome(r.Server, llm.SafeProposal())
		return r
	}
	r.Raw = raw
	p, g, why := grade(c, raw, len(ac.Tasks))
	r.Action, r.Task, r.Grade, r.Why = p.Action, p.Task, g, why
	dp, derr := llm.DecodeProposal(raw, ac) // on error dp is the safe proposal
	if derr != nil && r.Why == "" {
		r.Why = derr.Error()
	}
	r.Outcome = outcome(r.Server, dp)
	return r
}

// retryableRE: the provider's rate limit (429) and server errors (5xx), as
// completeJSON formats them ("llm: status N" — the status only, never the
// body, S-12). A timeout is NOT retried: production has no retry, so a
// timeout there is the safe relay and is graded ERROR here (review).
var retryableRE = regexp.MustCompile(`\bllm: status (429|5\d\d)\b`)

func retryable(err error) bool { return retryableRE.MatchString(err.Error()) }

func report(results []Result, verbose bool) int {
	count := map[string]int{}
	code, retries := 0, 0
	for _, r := range results {
		count[r.Grade]++
		retries += r.Retries
		mark := map[string]string{"PASS": "  ", "SAME": "≈ ", "FAIL": "✗ ", "UNSAFE": "!!", "ERROR": "? "}[r.Grade]
		if (r.Grade != "PASS" && r.Grade != "SAME") || verbose {
			fmt.Printf("%s #%d.%d [%s] %q → %s %s  (%s)  %s\n", mark, r.Case, r.Run, r.Group, r.Msg, r.Action, r.Task, r.Outcome, r.Why)
			if verbose && r.Raw != "" {
				fmt.Printf("      raw: %s\n", r.Raw)
			}
		}
		switch r.Grade {
		case "FAIL", "ERROR":
			code = max(code, 1)
		case "UNSAFE":
			code = max(code, 2)
		}
	}
	fmt.Printf("\n%d results: %d PASS, %d SAME (other action, same production outcome — -v to list), %d FAIL, %d UNSAFE, %d ERROR\n", len(results), count["PASS"], count["SAME"], count["FAIL"], count["UNSAFE"], count["ERROR"])
	if retries > 0 {
		fmt.Printf("%d rate-limit / 5xx retries (production does not retry: there these would have been the safe relay)\n", retries)
	}
	return code
}

// checkServer compares every case's "server" field with the rules.
func checkServer(f File) []string {
	var bad []string
	for _, c := range f.Cases {
		if c.Ctx.Open == "" {
			if c.Server != "" {
				bad = append(bad, fmt.Sprintf("case %d: server %q but no approval open", c.N, c.Server))
			}
			continue
		}
		ac, err := build(f, c)
		if err != nil {
			bad = append(bad, err.Error())
			continue
		}
		if got := verdictName(verdict(ac, c)); got != c.Server {
			bad = append(bad, fmt.Sprintf("case %d %q: rules say %s, case says %q", c.N, c.Msg, got, c.Server))
		}
	}
	return bad
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func orDefault(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}
