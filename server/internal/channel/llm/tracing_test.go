package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"hora-auth/internal/tracing"
)

// fakeModel answers every completion with content, instantly.
func fakeModel(t *testing.T, content string) *OpenAICompat {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 900, "completion_tokens": 12},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write(body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	return &OpenAICompat{BaseURL: srv.URL + "/v1", APIKey: "key", Model: "zai-org/GLM-5.3-Flash"}
}

func tracedMessage(ctx context.Context) context.Context {
	ctx = tracing.StartMessage(ctx, "imessage", "msg-1", "ok go ahead, call 212-555-1234")
	tracing.WithIdentity(ctx, "user-1", "identity-1")
	return ctx
}

var agentCtx = AgentContext{
	Tasks:         []AgentTask{{Title: "Grocery run", Supporter: "Alex"}},
	SupporterLine: &AgentLine{Task: 0, Text: "text me at 917-555-0100", MinutesAgo: 3},
}

// A Phoenix that hangs or fails must not change Propose's answer or add
// latency: spans are exported off the call, with a bounded timeout, and the
// flush on shutdown is bounded by it too (D-22).
func TestPhoenixOutageDoesNotTouchPropose(t *testing.T) {
	const exportTimeout = 300 * time.Millisecond
	release := make(chan struct{})
	defer close(release)
	for name, phoenix := range map[string]http.HandlerFunc{
		"hanging": func(w http.ResponseWriter, r *http.Request) {
			// Read the body first: only then does the server notice the
			// exporter hanging up and cancel r.Context().
			_, _ = io.Copy(io.Discard, r.Body)
			select { // until the exporter gives up, or the test ends
			case <-r.Context().Done():
			case <-release:
			}
		},
		"failing": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		},
	} {
		t.Run(name, func(t *testing.T) {
			model := fakeModel(t, `{"action":"approve","task":"t1","escalation":""}`)
			want, wantErr := model.Propose(context.Background(), "ok go ahead", agentCtx) // untraced

			srv := httptest.NewServer(phoenix)
			defer func() { srv.CloseClientConnections(); srv.Close() }()
			shutdown, err := tracing.Start(tracing.Config{
				Endpoint: srv.URL, APIKey: "k",
				ExportTimeout: exportTimeout, BatchTimeout: 10 * time.Millisecond,
			})
			if err != nil {
				t.Fatal(err)
			}
			stopped := false
			defer func() {
				if !stopped {
					_ = shutdown(context.Background())
				}
			}()

			var slowest time.Duration
			for i := 0; i < 30; i++ {
				ctx := tracedMessage(context.Background())
				start := time.Now()
				got, err := model.Propose(ctx, "ok go ahead", agentCtx)
				if d := time.Since(start); d > slowest {
					slowest = d
				}
				tracing.EndMessage(ctx, err)
				if got != want || (err == nil) != (wantErr == nil) {
					t.Fatalf("traced Propose = %+v, %v; untraced %+v, %v", got, err, want, wantErr)
				}
				if i == 10 {
					time.Sleep(50 * time.Millisecond) // let an export start, and hang or fail
				}
			}
			// The model is local: any wait on Phoenix would show as an
			// export timeout's worth.
			if slowest >= exportTimeout/2 {
				t.Fatalf("slowest traced Propose took %s with Phoenix %s (export timeout %s)", slowest, name, exportTimeout)
			}

			// The flush fails; it must end by the shutdown deadline, as
			// startTracing's does (several hanging exports can run back to
			// back while the queue drains, so the deadline is the bound).
			const deadline = time.Second
			sctx, cancel := context.WithTimeout(context.Background(), deadline)
			start := time.Now()
			_ = shutdown(sctx)
			cancel()
			stopped = true
			if d := time.Since(start); d > deadline+exportTimeout {
				t.Fatalf("shutdown took %s with Phoenix %s (deadline %s)", d, name, deadline)
			}
		})
	}
}

func TestProposeSpanShowsWhatWasMadeSafe(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	stop := tracing.StartWithExporter(exp)
	defer func() { _ = stop(context.Background()) }()

	// "approve" with no question of ours open: DecodeProposal makes it a relay.
	model := fakeModel(t, `{"action":"approve","task":"t1","escalation":""}`)
	ctx := tracedMessage(context.Background())
	p, err := model.Propose(ctx, "ok go ahead, call 212-555-1234", agentCtx)
	tracing.EndMessage(ctx, err)
	if err != nil || p.Action != ActionRelay {
		t.Fatalf("Propose = %+v, %v", p, err)
	}

	var agent, call map[string]string
	for _, s := range exp.GetSpans() {
		a := map[string]string{}
		for _, kv := range s.Attributes {
			a[string(kv.Key)] = kv.Value.Emit()
			if v := kv.Value.Emit(); strings.Contains(v, "555-") || strings.Contains(v, "2125551234") {
				t.Errorf("span %s attribute %s carries a phone number: %q", s.Name, kv.Key, v)
			}
		}
		switch s.Name {
		case "agent.propose":
			agent = a
		case "llm.proposal":
			call = a
		}
	}
	if agent == nil || call == nil {
		t.Fatalf("spans: agent=%v llm=%v", agent != nil, call != nil)
	}
	checks := map[string]string{
		"openinference.span.kind":     "AGENT",
		"input.value":                 "ok go ahead, call [phone]",
		"output.value":                `{"action":"relay","task":"","escalation":""}`, // approve's label is dropped, then it is relayed
		"hora.agent.raw_output":       `{"action":"approve","task":"t1","escalation":""}`,
		"llm.prompt_template.version": AgentPromptVersion,
		"llm.model_name":              "zai-org/GLM-5.3-Flash",
		"session.id":                  "identity-1",
		"user.id":                     "user-1",
		"hora.inbound_message_id":     "msg-1",
	}
	for k, want := range checks {
		if agent[k] != want {
			t.Errorf("agent span %s = %q, want %q", k, agent[k], want)
		}
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(agent["metadata"]), &meta); err != nil || meta["made_safe"] != true || meta["raw_action"] != "approve" || meta["input_tokens"] != float64(900) {
		t.Errorf("agent metadata = %s (%v)", agent["metadata"], err)
	}
	if !strings.Contains(agent["hora.agent.context"], `"text": "text me at [phone]"`) {
		t.Errorf("context not recorded redacted: %s", agent["hora.agent.context"])
	}
	if call["openinference.span.kind"] != "LLM" || call["llm.token_count.prompt"] != "900" || call["llm.token_count.completion"] != "12" ||
		!strings.Contains(call["llm.input_messages.1.message.content"], "CUSTOMER'S MESSAGE") {
		t.Errorf("llm span = %v", call)
	}
}

// Off, the existing paths run as before and nothing is recorded.
func TestTracingOffRecordsNothing(t *testing.T) {
	if tracing.Enabled() {
		t.Fatal("tracing on with nothing started")
	}
	model := fakeModel(t, `{"action":"relay","task":"t1","escalation":""}`)
	ctx := tracing.StartMessage(context.Background(), "imessage", "m", "x")
	if ctx != context.Background() {
		t.Fatal("StartMessage changed the context with tracing off")
	}
	p, err := model.Propose(ctx, "thanks", agentCtx)
	if err != nil || p.Action != ActionRelay || p.Task != 0 {
		t.Fatalf("Propose = %+v, %v", p, err)
	}
}
