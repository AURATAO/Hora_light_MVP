package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"hora-auth/internal/tracing"
)

// anthropicReply is a Messages API response with text and usage.
func anthropicReply(t *testing.T, text, stop string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-5-5",
		"content":     []any{map[string]any{"type": "text", "text": text}},
		"stop_reason": stop,
		"usage": map[string]any{
			"input_tokens": 101, "output_tokens": 19,
			"cache_read_input_tokens": 3266, "cache_creation_input_tokens": 0,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeAnthropic serves h at the Messages endpoint and records each body.
func fakeAnthropic(t *testing.T, model string, h func(w http.ResponseWriter, body map[string]any)) (*Anthropic, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// SDK v2 sends the API key as a bearer token.
		if r.URL.Path != "/v1/messages" || r.Header.Get("Authorization") != "Bearer key" {
			http.Error(w, `{"type":"error","error":{"type":"authentication_error","message":"bad"}}`, 401)
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		h(w, body)
	}))
	t.Cleanup(srv.Close)
	return &Anthropic{APIKey: "key", Model: model, BaseURL: srv.URL}, &calls
}

func write(t *testing.T, w http.ResponseWriter, b []byte) {
	if _, err := w.Write(b); err != nil {
		t.Errorf("test server write: %v", err) // handler goroutine: Errorf, not Fatal
	}
}

// The request is structured output with the schema's unsupported array
// bounds removed, no temperature (the API rejects it), thinking off on
// Sonnet 5.5 by its own switch, low effort and a cached system prompt.
func TestAnthropicRequestShape(t *testing.T) {
	var got map[string]any
	a, _ := fakeAnthropic(t, "claude-sonnet-5-5", func(w http.ResponseWriter, body map[string]any) {
		got = body
		write(t, w, anthropicReply(t, `{"errands":["pick up laundry at 2pm","buy milk"]}`, "end_turn"))
	})
	errands, u, err := (&Model{Primary: a}).SplitErrands(context.Background(), "pick up laundry at 2pm and buy milk")
	if err != nil || len(errands) != 2 {
		t.Fatalf("SplitErrands = %v, %v", errands, err)
	}
	if u.InputTokens != 101 || u.CacheReadTokens != 3266 || u.OutputTokens != 19 || u.PromptTokens() != 3367 || u.Model != "claude-sonnet-5-5" {
		t.Errorf("usage = %+v", u)
	}
	if _, ok := got["temperature"]; ok {
		t.Error("temperature sent: API version 2026-10-21 rejects it")
	}
	if got["model"] != "claude-sonnet-5-5" || got["max_tokens"] != float64(512) {
		t.Errorf("model / max_tokens: %v %v", got["model"], got["max_tokens"])
	}
	if th, _ := got["thinking"].(map[string]any); th["type"] != "between_tools" {
		t.Errorf("thinking = %v", got["thinking"])
	}
	oc, _ := got["output_config"].(map[string]any)
	f, _ := oc["format"].(map[string]any)
	if oc["effort"] != "low" || f["type"] != "json_schema" {
		t.Fatalf("output_config = %v", oc)
	}
	if s, _ := json.Marshal(f["schema"]); strings.Contains(string(s), "Items") || !strings.Contains(string(s), `"errands"`) {
		t.Errorf("schema = %s", s)
	}
	sys, _ := got["system"].([]any)
	if len(sys) != 1 || sys[0].(map[string]any)["cache_control"] == nil {
		t.Errorf("system = %v", got["system"])
	}
}

// Opus 5.5 cannot turn thinking off: no thinking field (adaptive), and a
// label's 256-token cap is raised so thinking cannot truncate the JSON.
func TestAnthropicOpusHasRoomToThink(t *testing.T) {
	var got map[string]any
	a, _ := fakeAnthropic(t, "claude-opus-5-5", func(w http.ResponseWriter, body map[string]any) {
		got = body
		write(t, w, anthropicReply(t, `{"purchase":true}`, "end_turn"))
	})
	yes, err := (&Model{Primary: a}).NeedsPurchase(context.Background(), "buy a cake")
	if err != nil || !yes {
		t.Fatalf("NeedsPurchase = %v, %v", yes, err)
	}
	if _, ok := got["thinking"]; ok {
		t.Errorf("thinking sent to Opus 5.5: %v", got["thinking"])
	}
	if got["max_tokens"].(float64) < minThinkingTokens {
		t.Errorf("max_tokens = %v", got["max_tokens"])
	}
}

// Errors carry the status or a fixed text — never the body, which can echo
// the person's words, nor the key (S-12).
func TestAnthropicErrorsCarryStatusOnly(t *testing.T) {
	for _, code := range []int{400, 429, 500, 529} {
		a, _ := fakeAnthropic(t, "claude-sonnet-5-5", func(w http.ResponseWriter, _ map[string]any) {
			w.WriteHeader(code)
			write(t, w, []byte(`{"type":"error","error":{"type":"x","message":"echo: secret-person-words"}}`))
		})
		_, err := (&Model{Primary: a}).Propose(context.Background(), "secret-person-words", AgentContext{})
		if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "key") ||
			!strings.Contains(err.Error(), fmt.Sprintf("anthropic: status %d", code)) {
			t.Errorf("%d: %v", code, err)
		}
	}
	for stop, want := range map[string]string{"refusal": "anthropic: refused", "max_tokens": "output truncated"} {
		a, _ := fakeAnthropic(t, "claude-sonnet-5-5", func(w http.ResponseWriter, _ map[string]any) {
			write(t, w, anthropicReply(t, `{"action":"secret-person-words`, stop))
		})
		_, err := (&Model{Primary: a}).Propose(context.Background(), "secret-person-words", AgentContext{})
		if err == nil || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", stop, err)
		}
	}
	a := &Anthropic{APIKey: "secret-key", Model: "claude-sonnet-5-5", BaseURL: "http://127.0.0.1:1"}
	if _, err := (&Model{Primary: a}).Propose(context.Background(), "secret-person-words", AgentContext{}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("unreachable: %v", err)
	}
}

// An Anthropic failure is retried once on Baseten: the person still gets an
// answer, and the trace says it was a fallback, why, and from which model.
func TestFallbackAnswersWhenAnthropicFails(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	stop := tracing.StartWithExporter(exp)
	defer func() { _ = stop(context.Background()) }()

	for name, fail := range map[string]func(http.ResponseWriter){
		"status":  func(w http.ResponseWriter) { w.WriteHeader(529) },
		"timeout": func(http.ResponseWriter) { time.Sleep(300 * time.Millisecond) },
	} {
		t.Run(name, func(t *testing.T) {
			exp.Reset()
			a, calls := fakeAnthropic(t, "claude-sonnet-5-5", func(w http.ResponseWriter, _ map[string]any) { fail(w) })
			a.HTTP = &http.Client{Timeout: 100 * time.Millisecond}
			m := &Model{Primary: a, Fallback: fakeModel(t, `{"action":"relay","task":"t1","escalation":""}`)}
			ctx := tracedMessage(context.Background())
			p, err := m.Propose(ctx, "thanks", agentCtx)
			tracing.EndMessage(ctx, err)
			if err != nil || p.Action != ActionRelay || p.Task != 0 || calls.Load() != 1 {
				t.Fatalf("Propose = %+v, %v (anthropic calls %d)", p, err, calls.Load())
			}
			spans := map[string]map[string]string{}
			for _, s := range exp.GetSpans() {
				a := map[string]string{}
				for _, kv := range s.Attributes {
					a[string(kv.Key)] = kv.Value.Emit()
				}
				spans[s.Name] = a
			}
			fb := spans["llm.fallback"]
			if fb["hora.llm.fallback"] != "true" || fb["hora.llm.fallback_from"] != "anthropic:claude-sonnet-5-5" ||
				!strings.HasSuffix(fb["hora.llm.fallback_to"], ":zai-org/GLM-5.3-Flash") || fb["hora.llm.fallback_reason"] != map[string]string{"status": "status_529", "timeout": "timeout"}[name] {
				t.Errorf("fallback span = %v", fb)
			}
			var meta map[string]any
			if err := json.Unmarshal([]byte(spans["agent.propose"]["metadata"]), &meta); err != nil || meta["fallback_from"] != "anthropic:claude-sonnet-5-5" {
				t.Errorf("agent metadata = %v (%v)", spans["agent.propose"]["metadata"], err)
			}
		})
	}
}

// Each LLM span carries provider, model, latency and tokens, cache included.
func TestAnthropicSpanCarriesUsage(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	stop := tracing.StartWithExporter(exp)
	defer func() { _ = stop(context.Background()) }()
	a, _ := fakeAnthropic(t, "claude-sonnet-5-5", func(w http.ResponseWriter, _ map[string]any) {
		write(t, w, anthropicReply(t, `{"action":"relay","task":"t1","escalation":""}`, "end_turn"))
	})
	ctx := tracedMessage(context.Background())
	_, err := (&Model{Primary: a}).Propose(ctx, "thanks", agentCtx)
	tracing.EndMessage(ctx, err)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range exp.GetSpans() {
		if s.Name != "llm.proposal" {
			continue
		}
		a := map[string]string{}
		for _, kv := range s.Attributes {
			a[string(kv.Key)] = kv.Value.Emit()
		}
		for k, want := range map[string]string{
			"llm.provider": "anthropic", "llm.model_name": "claude-sonnet-5-5",
			"llm.token_count.prompt": "3367", "llm.token_count.completion": "19",
			"llm.token_count.prompt_details.cache_read": "3266",
		} {
			if a[k] != want {
				t.Errorf("%s = %q, want %q", k, a[k], want)
			}
		}
		if _, ok := a["hora.llm.latency_ms"]; !ok {
			t.Error("no latency")
		}
		return
	}
	t.Fatal("no llm.proposal span")
}

// A caller that has given up is not retried into: the fallback would only
// fail the same way.
func TestNoFallbackAfterTheCallerGaveUp(t *testing.T) {
	a, _ := fakeAnthropic(t, "claude-sonnet-5-5", func(w http.ResponseWriter, _ map[string]any) { w.WriteHeader(500) })
	var backup atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { backup.Add(1) }))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := &Model{Primary: a, Fallback: &OpenAICompat{BaseURL: srv.URL, APIKey: "k", Model: "m"}}
	if _, err := m.Propose(ctx, "x", AgentContext{}); err == nil || backup.Load() != 0 {
		t.Fatalf("err=%v backup calls=%d", err, backup.Load())
	}
}

// The primary may not spend the time the fallback needs: with a deadline,
// it stops FallbackReserve early.
func TestPrimaryLeavesTheFallbackTime(t *testing.T) {
	a, _ := fakeAnthropic(t, "claude-sonnet-5-5", func(http.ResponseWriter, map[string]any) { time.Sleep(2 * time.Second) })
	m := &Model{Primary: a, Fallback: fakeModel(t, `{"intent":"greeting"}`), FallbackReserve: 300 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := m.Classify(ctx, "hi")
	if err != nil || got != IntentGreeting {
		t.Fatalf("Classify = %v, %v", got, err)
	}
}

func TestParseSpec(t *testing.T) {
	for in, want := range map[string]Spec{
		"anthropic:claude-sonnet-5-5":     {"anthropic", "claude-sonnet-5-5"},
		" Baseten:zai-org/GLM-5.3-Flash ": {"baseten", "zai-org/GLM-5.3-Flash"},
	} {
		if got, err := ParseSpec(in); err != nil || got != want {
			t.Errorf("%q = %+v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "claude-sonnet-5-5", "openai:gpt", "anthropic:"} {
		if _, err := ParseSpec(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestStepsFromEnv(t *testing.T) {
	for _, k := range []string{"LLM_AGENT_MODEL", "LLM_PARSE_MODEL", "LLM_SMALL_MODEL", "LLM_MODEL"} {
		t.Setenv(k, "")
	}
	var warned []string
	warn := func(s string) { warned = append(warned, s) }
	p := Providers{AnthropicKey: "a", BasetenKey: "b", BasetenModel: DefaultBasetenModel, BasetenBaseURL: DefaultBasetenBaseURL}
	s := StepsFromEnv(p, warn)
	if s.Agent.Name() != "anthropic:claude-sonnet-5-5 (fallback baseten:zai-org/GLM-5.3-Flash)" ||
		s.Parse.Name() != s.Agent.Name() || s.Small.Name() != "baseten:zai-org/GLM-5.3-Flash" || len(warned) != 0 {
		t.Fatalf("defaults: %s / %s / %s %v", s.Agent.Name(), s.Parse.Name(), s.Small.Name(), warned)
	}
	t.Setenv("LLM_AGENT_MODEL", "anthropic:claude-opus-5-5")
	t.Setenv("LLM_SMALL_MODEL", "openai:gpt") // malformed: default kept, said so
	s = StepsFromEnv(p, warn)
	if s.Agent.Name() != "anthropic:claude-opus-5-5 (fallback baseten:zai-org/GLM-5.3-Flash)" || s.Small.Name() != "baseten:zai-org/GLM-5.3-Flash" || len(warned) != 1 {
		t.Fatalf("override: %s / %s %v", s.Agent.Name(), s.Small.Name(), warned)
	}
	warned = nil
	p.AnthropicKey = "" // no Anthropic key: Baseten, said so
	s = StepsFromEnv(p, warn)
	if s.Parse.Name() != "baseten:zai-org/GLM-5.3-Flash" || len(warned) < 2 {
		t.Fatalf("no key: %s %v", s.Parse.Name(), warned)
	}
	for _, w := range warned {
		if strings.Contains(w, "=a") || strings.Contains(w, " b ") {
			t.Errorf("warning carries a key: %q", w)
		}
	}
}

// The parse prompt changes every call ("now" to the second): no cache
// marker, so no 1.25x write that is never read. The agent's is cached.
func TestParseIsNotPromptCached(t *testing.T) {
	var sys []any
	a, _ := fakeAnthropic(t, "claude-sonnet-5-5", func(w http.ResponseWriter, body map[string]any) {
		sys, _ = body["system"].([]any)
		write(t, w, anthropicReply(t, `{"title":"Pick up cake","category":"delivery","description":null,"location_1":"100 Washington St","location_2":null,"duration_minutes":null,"scheduled":false,"scheduled_time":null}`, "end_turn"))
	})
	if _, _, err := (&Model{Primary: a}).ParseTask(context.Background(), "now is 10:00:01", "pick up a cake"); err != nil {
		t.Fatal(err)
	}
	if len(sys) != 1 || sys[0].(map[string]any)["cache_control"] != nil {
		t.Fatalf("system = %v", sys)
	}
}
