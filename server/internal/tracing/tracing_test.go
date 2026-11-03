package tracing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func attrs(s tracetest.SpanStub) map[string]string {
	m := map[string]string{}
	for _, kv := range s.Attributes {
		m[string(kv.Key)] = kv.Value.Emit()
	}
	return m
}

func byName(spans tracetest.SpanStubs, name string) (tracetest.SpanStub, bool) {
	for _, s := range spans {
		if s.Name == name {
			return s, true
		}
	}
	return tracetest.SpanStub{}, false
}

func TestOffByDefault(t *testing.T) {
	t.Setenv("PHOENIX_COLLECTOR_ENDPOINT", "")
	t.Setenv("PHOENIX_API_KEY", "")
	if _, ok := ConfigFromEnv(); ok || Configured() {
		t.Fatal("tracing configured with no environment")
	}
	t.Setenv("PHOENIX_COLLECTOR_ENDPOINT", "https://phoenix.example")
	if _, ok := ConfigFromEnv(); ok {
		t.Fatal("tracing configured with an endpoint but no key")
	}
	t.Setenv("PHOENIX_COLLECTOR_ENDPOINT", "")
	t.Setenv("PHOENIX_API_KEY", "k")
	if _, ok := ConfigFromEnv(); ok {
		t.Fatal("tracing configured with a key but no endpoint")
	}
	if Enabled() {
		t.Fatal("tracing enabled without Start")
	}
	ctx := context.Background()
	if got := StartMessage(ctx, "imessage", "m1", "hi"); got != ctx {
		t.Fatal("StartMessage changed the context with tracing off")
	}
	_, sp := StartSpan(ctx, "x", KindTool)
	if sp.Recording() {
		t.Fatal("a span records with tracing off")
	}
	sp.Input("text") // no-ops, no panic
	sp.Fail(errors.New("e"))
	sp.End()
	EndMessage(ctx, nil)
}

func TestConfigFromEnvNormalisesTheEndpoint(t *testing.T) {
	t.Setenv("PHOENIX_API_KEY", "k")
	for in, want := range map[string]string{
		"https://phoenix.example/":          "https://phoenix.example",
		"https://phoenix.example/v1/traces": "https://phoenix.example",
		" http://localhost:6006 ":           "http://localhost:6006",
	} {
		t.Setenv("PHOENIX_COLLECTOR_ENDPOINT", in)
		c, ok := ConfigFromEnv()
		if !ok || c.Endpoint != want {
			t.Errorf("endpoint %q → %q (ok=%v), want %q", in, c.Endpoint, ok, want)
		}
	}
}

func TestMessageTraceCarriesIdentityAndRedactedText(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	stop := StartWithExporter(exp)
	defer func() { _ = stop(context.Background()) }()

	ctx := StartMessage(context.Background(), "imessage", "msg-123", "call me 212-555-1234")
	// Before the conversation is known nothing is a child span…
	_, early := StartSpan(ctx, "too.early", KindTool)
	if early.Recording() {
		t.Fatal("a span recorded before the conversation was known")
	}
	early.End()
	WithIdentity(ctx, "user-uuid", "identity-uuid")
	cctx, sp := StartLLM(ctx, "llm.proposal", LLMCall{Model: "m", Provider: "p", System: "sys", User: "mail a@b.co"})
	_ = cctx
	sp.LLMResult(`{"action":"relay"}`, 10, 2)
	sp.End()
	Replied(ctx, "Got it, texting 917-555-0100")
	EndMessage(ctx, nil)

	spans := exp.GetSpans()
	root, ok := byName(spans, "channel.inbound")
	if !ok {
		t.Fatalf("no root span in %d spans", len(spans))
	}
	llm, ok := byName(spans, "llm.proposal")
	if !ok {
		t.Fatal("no llm span")
	}
	if llm.Parent.SpanID() != root.SpanContext.SpanID() || llm.SpanContext.TraceID() != root.SpanContext.TraceID() {
		t.Fatal("llm span is not a child of the message trace")
	}
	ra, la := attrs(root), attrs(llm)
	for _, a := range []map[string]string{ra, la} {
		if a["session.id"] != "identity-uuid" || a["user.id"] != "user-uuid" || a["hora.inbound_message_id"] != "msg-123" {
			t.Errorf("identity attributes = %v", a)
		}
	}
	if ra["input.value"] != "call me [phone]" || ra["output.value"] != "Got it, texting [phone]" {
		t.Errorf("root input/output = %q / %q", ra["input.value"], ra["output.value"])
	}
	if la["llm.input_messages.1.message.content"] != "mail [email]" || la["openinference.span.kind"] != "LLM" ||
		la["llm.token_count.total"] != "12" || la["llm.model_name"] != "m" {
		t.Errorf("llm attributes = %v", la)
	}
	for _, s := range spans {
		for _, kv := range s.Attributes {
			if v := kv.Value.Emit(); strings.Contains(v, "555") || strings.Contains(v, "a@b.co") {
				t.Errorf("span %s attribute %s leaks %q", s.Name, kv.Key, v)
			}
		}
	}
}

// A message whose conversation never became known (the identity upsert
// failed) is exported without its text: no session could delete it.
func TestNoTextWithoutAConversation(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	stop := StartWithExporter(exp)
	defer func() { _ = stop(context.Background()) }()

	ctx := StartMessage(context.Background(), "whatsapp", "wamid.1", "my secret errand")
	EndMessage(ctx, errors.New("HandleInbound: db down"))
	root, ok := byName(exp.GetSpans(), "channel.inbound")
	if !ok {
		t.Fatal("no root span")
	}
	a := attrs(root)
	if _, has := a["input.value"]; has {
		t.Fatalf("text exported with no conversation: %v", a)
	}
	if !strings.Contains(a["metadata"], "identity_unknown") || root.Status.Code.String() != "Error" {
		t.Fatalf("root = %v status %v", a, root.Status)
	}
}

func TestSpanOutsideAMessageRecordsNothing(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	stop := StartWithExporter(exp)
	defer func() { _ = stop(context.Background()) }()
	_, sp := StartSpan(context.Background(), "geocode.google", KindTool) // the app's own request
	sp.Input("350 5th Ave")
	sp.Attr(attribute.Int("n", 1))
	sp.End()
	if n := len(exp.GetSpans()); n != 0 {
		t.Fatalf("%d spans exported outside a message trace", n)
	}
}

func TestDeleteSession(t *testing.T) {
	var gotPath, gotAuth string
	status := http.StatusNoContent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method %s", r.Method)
		}
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(status)
		_, _ = w.Write([]byte("session 0f3c… for +12125551234 not found")) // never read into an error
	}))
	defer srv.Close()
	d := &Deleter{Endpoint: srv.URL, APIKey: "secret"}
	ctx := context.Background()

	if err := d.DeleteSession(ctx, "4b0e6f8a-0000-4000-8000-000000000001"); err != nil {
		t.Fatalf("204: %v", err)
	}
	if gotPath != "/v1/sessions/4b0e6f8a-0000-4000-8000-000000000001" || gotAuth != "Bearer secret" {
		t.Fatalf("request %s auth %q", gotPath, gotAuth)
	}
	status = http.StatusNotFound
	if err := d.DeleteSession(ctx, "gone"); err != nil {
		t.Fatalf("404 must count as deleted: %v", err)
	}
	status = http.StatusBadGateway
	err := d.DeleteSession(ctx, "x")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 502 {
		t.Fatalf("502: %v", err)
	}
	if strings.Contains(err.Error(), "+1212") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error carries the body or the key: %v", err)
	}
	if err := (&Deleter{}).DeleteSession(ctx, "x"); err == nil {
		t.Fatal("unconfigured deleter succeeded")
	}
}

// ErrorEvent: a root span outside a message trace (a send from an app
// notification), a child inside one; never without a session.
func TestErrorEvent(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	stop := StartWithExporter(exp)
	defer func() { _ = stop(context.Background()) }()

	ErrorEvent(context.Background(), "channel.opted_out", "imessage", "user-1", "conv-1",
		errors.New("send to +1*******67: opted out"), map[string]any{"source": "provider"})
	ErrorEvent(context.Background(), "channel.no_session", "imessage", "", "", errors.New("x"), nil)

	ctx := StartMessage(context.Background(), "imessage", "m1", "STOP")
	WithIdentity(ctx, "user-1", "conv-1")
	ErrorEvent(ctx, "channel.opted_out_child", "imessage", "user-1", "conv-1", errors.New("opted out"), nil)
	EndMessage(ctx, nil)

	spans := exp.GetSpans()
	root, ok := byName(spans, "channel.opted_out")
	if !ok {
		t.Fatal("no span")
	}
	a := attrs(root)
	if a["session.id"] != "conv-1" || a["user.id"] != "user-1" || a["hora.channel"] != "imessage" || !strings.Contains(a["metadata"], "provider") {
		t.Fatalf("attrs %v", a)
	}
	if root.Status.Code.String() != "Error" || root.Parent.IsValid() {
		t.Fatalf("status %v parent %v", root.Status, root.Parent)
	}
	if _, ok := byName(spans, "channel.no_session"); ok {
		t.Fatal("a span with no session was recorded")
	}
	child, ok := byName(spans, "channel.opted_out_child")
	inbound, _ := byName(spans, "channel.inbound")
	if !ok || child.Parent.SpanID() != inbound.SpanContext.SpanID() {
		t.Fatal("inside a message trace the error span is its child")
	}
}
