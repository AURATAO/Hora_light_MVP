// Package tracing sends traces of the messaging channel to our self-hosted
// Arize Phoenix (D-22), so a misread message or a slow reply can be looked at
// step by step: one trace per inbound message, a child span per model call,
// per service-area check and per Google lookup, and one per reply sent.
//
// OFF unless PHOENIX_COLLECTOR_ENDPOINT and PHOENIX_API_KEY are both set.
// Off, every function here is a no-op on the noop tracer: no span is
// recorded, no text is redacted, nothing leaves the process.
//
// NEVER ON THE CRITICAL PATH. Spans go to a batch processor that drops when
// its queue is full and exports on its own goroutine with a short timeout and
// no retries. A Phoenix outage costs us the traces, never a reply.
//
// TEXT (D-22, amending S-12 and D-18 A-7). Phoenix is the ONLY place message
// text goes, and only through Span's text methods, which run Redact first.
// Identity is the internal user id and the conversation (identity) id; never
// a phone number. A span carries text only inside a message trace whose
// conversation is known, so every trace with text can be deleted by session
// when the account is (DeleteSession).
//
// Attribute names are OpenInference's, so Phoenix renders LLM spans, sessions
// and users: https://github.com/Arize-ai/openinference (semconv/trace).
package tracing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// OpenInference attribute keys and span kinds.
const (
	attrKind        = "openinference.span.kind"
	attrInput       = "input.value"
	attrInputMime   = "input.mime_type"
	attrOutput      = "output.value"
	attrOutputMime  = "output.mime_type"
	attrSession     = "session.id"
	attrUser        = "user.id"
	attrMetadata    = "metadata"
	attrModel       = "llm.model_name"
	attrProvider    = "llm.provider"
	attrParams      = "llm.invocation_parameters"
	attrPromptVer   = "llm.prompt_template.version"
	attrTokPrompt   = "llm.token_count.prompt"
	attrTokComplete = "llm.token_count.completion"
	attrTokTotal    = "llm.token_count.total"
	attrInMsgs      = "llm.input_messages"
	attrOutMsgs     = "llm.output_messages"

	// Ours, to join a trace to channel_messages (channel, provider_message_id).
	attrMessageID = "hora.inbound_message_id"
	attrChannel   = "hora.channel"

	KindChain = "CHAIN"
	KindLLM   = "LLM"
	KindAgent = "AGENT"
	KindTool  = "TOOL"

	mimeText = "text/plain"
	mimeJSON = "application/json"
)

// Config is where traces go and how long an export may take.
type Config struct {
	// Endpoint is Phoenix's base URL (PHOENIX_COLLECTOR_ENDPOINT); spans are
	// posted to Endpoint + "/v1/traces".
	Endpoint string
	APIKey   string
	// Project is the Phoenix project (PHOENIX_PROJECT_NAME, default
	// "hora-channel").
	Project string
	// ExportTimeout bounds one export. Default 3s.
	ExportTimeout time.Duration
	// BatchTimeout is the longest a span waits in the queue. Default 5s.
	BatchTimeout time.Duration
}

// ConfigFromEnv reads the configuration; ok is false (tracing off) unless
// both the endpoint and the key are set.
func ConfigFromEnv() (Config, bool) {
	c := Config{
		Endpoint: BaseURL(os.Getenv("PHOENIX_COLLECTOR_ENDPOINT")),
		APIKey:   strings.TrimSpace(os.Getenv("PHOENIX_API_KEY")),
		Project:  strings.TrimSpace(os.Getenv("PHOENIX_PROJECT_NAME")),
	}
	return c, c.Endpoint != "" && c.APIKey != ""
}

// BaseURL normalises an endpoint as given: no trailing slash, and no
// "/v1/traces" if it was pasted with one.
func BaseURL(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	return strings.TrimRight(strings.TrimSuffix(s, "/v1/traces"), "/")
}

var provider atomic.Pointer[sdktrace.TracerProvider]

var noopTracer = noop.NewTracerProvider().Tracer("")

func tracer() trace.Tracer {
	if p := provider.Load(); p != nil {
		return p.Tracer("hora/channel")
	}
	return noopTracer
}

// Enabled reports whether spans are being exported.
func Enabled() bool { return provider.Load() != nil }

// Start turns tracing on with c and returns the function that flushes and
// stops it (call it on shutdown, with a deadline). Creating the exporter
// opens no connection: Start never waits on Phoenix.
func Start(c Config) (shutdown func(context.Context) error, err error) {
	if c.Endpoint == "" || c.APIKey == "" {
		return nil, errors.New("tracing: endpoint and API key are required")
	}
	if c.Project == "" {
		c.Project = "hora-channel"
	}
	if c.ExportTimeout <= 0 {
		c.ExportTimeout = 3 * time.Second
	}
	if c.BatchTimeout <= 0 {
		c.BatchTimeout = 5 * time.Second
	}
	exp, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpointURL(c.Endpoint+"/v1/traces"),
		otlptracehttp.WithHeaders(map[string]string{"Authorization": "Bearer " + c.APIKey}),
		otlptracehttp.WithTimeout(c.ExportTimeout),
		// A batch that fails is dropped, not retried: retries would hold the
		// export goroutine for up to a minute while the queue fills.
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}),
	)
	if err != nil {
		return nil, fmt.Errorf("tracing: exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", "hora-server"),
			attribute.String("openinference.project.name", c.Project),
		)),
		// Non-blocking: a full queue drops spans; End never waits.
		sdktrace.WithBatcher(exp,
			sdktrace.WithBatchTimeout(c.BatchTimeout),
			sdktrace.WithExportTimeout(c.ExportTimeout),
			sdktrace.WithMaxQueueSize(2048),
			sdktrace.WithMaxExportBatchSize(256),
		),
	)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(exportFailed))
	return install(tp), nil
}

// StartWithExporter turns tracing on with exp behind a synchronous processor.
// For tests only: they read what would have been exported.
func StartWithExporter(exp sdktrace.SpanExporter) (shutdown func(context.Context) error) {
	return install(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
}

func install(tp *sdktrace.TracerProvider) func(context.Context) error {
	provider.Store(tp)
	return func(ctx context.Context) error {
		provider.CompareAndSwap(tp, nil)
		return tp.Shutdown(ctx)
	}
}

// exportFailed logs that an export failed, at most once a minute, with a
// fixed text: the exporter's error can quote Phoenix's response body (S-12).
func exportFailed(error) {
	now := time.Now().Unix()
	last := lastExportLog.Load()
	if now-last < 60 || !lastExportLog.CompareAndSwap(last, now) {
		return
	}
	log.Printf("[tracing] export to Phoenix failed; spans dropped (replies unaffected)")
}

var lastExportLog atomic.Int64

// ── The message trace ──────────────────────────────────────────────────────

// message is one inbound message's trace state, shared by every span under
// it. Identity arrives after the root span starts (the router resolves it).
type message struct {
	mu        sync.Mutex
	root      trace.Span
	channel   string
	id        string
	userID    string
	sessionID string
	input     string
	replies   []string
}

type msgKey struct{}

func msgFrom(ctx context.Context) *message {
	m, _ := ctx.Value(msgKey{}).(*message)
	return m
}

// StartMessage opens the trace for one inbound message. End it with
// EndMessage. text is recorded (redacted) only once the conversation is
// known: a trace no session names could not be deleted with the account.
func StartMessage(ctx context.Context, channel, providerMessageID, text string) context.Context {
	if !Enabled() {
		return ctx
	}
	ctx, sp := tracer().Start(ctx, "channel.inbound", trace.WithAttributes(
		attribute.String(attrKind, KindChain),
		attribute.String(attrChannel, channel),
		attribute.String(attrMessageID, providerMessageID),
	))
	m := &message{root: sp, channel: channel, id: providerMessageID, input: text}
	return context.WithValue(ctx, msgKey{}, m)
}

// EndMessage closes the trace: the inbound text and every reply sent, and the
// routing error if there was one.
func EndMessage(ctx context.Context, err error) {
	m := msgFrom(ctx)
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	meta := map[string]any{"channel": m.channel, "inbound_message_id": m.id, "replies": len(m.replies)}
	if m.sessionID != "" {
		m.root.SetAttributes(
			attribute.String(attrInput, Redact(m.input)),
			attribute.String(attrInputMime, mimeText),
			attribute.String(attrOutput, Redact(strings.Join(m.replies, "\n---\n"))),
			attribute.String(attrOutputMime, mimeText),
		)
	} else {
		meta["identity_unknown"] = true
	}
	m.root.SetAttributes(attribute.String(attrMetadata, jsonString(meta)))
	fail(m.root, err)
	m.root.End()
}

// WithIdentity names whose message this is: userID is the internal users.id
// ("" for a number not linked to an account), sessionID the conversation
// (channel identity) id. Never a phone number.
func WithIdentity(ctx context.Context, userID, sessionID string) {
	m := msgFrom(ctx)
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.userID, m.sessionID = userID, sessionID
	m.root.SetAttributes(identityAttrs(m)...)
}

// Replied records a reply sent for the message being traced.
func Replied(ctx context.Context, text string) {
	if m := msgFrom(ctx); m != nil {
		m.mu.Lock()
		m.replies = append(m.replies, text)
		m.mu.Unlock()
	}
}

func identityAttrs(m *message) []attribute.KeyValue {
	kv := []attribute.KeyValue{
		attribute.String(attrMessageID, m.id),
		attribute.String(attrChannel, m.channel),
	}
	if m.sessionID != "" {
		kv = append(kv, attribute.String(attrSession, m.sessionID))
	}
	if m.userID != "" {
		kv = append(kv, attribute.String(attrUser, m.userID))
	}
	return kv
}

// ── Spans ──────────────────────────────────────────────────────────────────

// Span is a step under the message trace. Its text methods redact; all of
// them are no-ops when the span is not recording (tracing off, or no message
// trace with a known conversation above it).
type Span struct{ s trace.Span }

// StartSpan opens a child span of the message trace in ctx. Outside one — the
// app's own endpoints, a background job, a message whose conversation is not
// yet known — it records nothing.
func StartSpan(ctx context.Context, name, kind string) (context.Context, Span) {
	m := msgFrom(ctx)
	if m == nil {
		return ctx, Span{trace.SpanFromContext(context.Background())}
	}
	m.mu.Lock()
	known := m.sessionID != ""
	attrs := identityAttrs(m)
	m.mu.Unlock()
	if !known {
		return ctx, Span{trace.SpanFromContext(context.Background())}
	}
	ctx, sp := tracer().Start(ctx, name, trace.WithAttributes(append(attrs, attribute.String(attrKind, kind))...))
	return ctx, Span{sp}
}

// Recording reports whether anything set on s is kept; check it before
// building an expensive value.
func (s Span) Recording() bool { return s.s.IsRecording() }

// End closes the span.
func (s Span) End() { s.s.End() }

// Input is the step's input, plain text, redacted.
func (s Span) Input(text string) { s.text(attrInput, attrInputMime, mimeText, text) }

// InputJSON is the step's input as JSON, redacted.
func (s Span) InputJSON(v string) { s.text(attrInput, attrInputMime, mimeJSON, v) }

// Output is the step's result, plain text, redacted.
func (s Span) Output(text string) { s.text(attrOutput, attrOutputMime, mimeText, text) }

// OutputValue is the step's result marshalled to JSON, redacted.
func (s Span) OutputValue(v any) {
	if s.Recording() {
		s.text(attrOutput, attrOutputMime, mimeJSON, jsonString(v))
	}
}

func (s Span) text(key, mimeKey, mime, v string) {
	if s.Recording() {
		s.s.SetAttributes(attribute.String(key, Redact(v)), attribute.String(mimeKey, mime))
	}
}

// Text sets a string attribute that may hold personal text, redacted.
func (s Span) Text(key, v string) {
	if s.Recording() {
		s.s.SetAttributes(attribute.String(key, Redact(v)))
	}
}

// Attr sets attributes that hold no personal text (labels, counts, flags).
func (s Span) Attr(kv ...attribute.KeyValue) {
	if s.Recording() {
		s.s.SetAttributes(kv...)
	}
}

// Metadata sets the OpenInference metadata object (shown and filterable in
// Phoenix). Values must hold no personal text.
func (s Span) Metadata(m map[string]any) {
	if s.Recording() {
		s.s.SetAttributes(attribute.String(attrMetadata, jsonString(m)))
	}
}

// PromptVersion labels the prompt the call used.
func (s Span) PromptVersion(v string) { s.Attr(attribute.String(attrPromptVer, v)) }

// Model names the model.
func (s Span) Model(name string) { s.Attr(attribute.String(attrModel, name)) }

// Fail marks the span failed. err's text is redacted too, although by S-12
// errors on this path carry no message text.
func (s Span) Fail(err error) {
	if s.Recording() {
		fail(s.s, err)
	}
}

func fail(sp trace.Span, err error) {
	if err == nil {
		return
	}
	msg := Redact(err.Error())
	sp.SetStatus(codes.Error, msg)
	sp.AddEvent("exception", trace.WithAttributes(attribute.String("exception.message", msg)))
}

// LLMCall is one chat completion, as Phoenix's LLM view shows it.
type LLMCall struct {
	Model, Provider string
	System, User    string
	Params          map[string]any
}

// StartLLM opens an LLM span with the prompt recorded. The system prompt is
// our own text; the user turn is redacted.
func StartLLM(ctx context.Context, name string, c LLMCall) (context.Context, Span) {
	ctx, s := StartSpan(ctx, name, KindLLM)
	if !s.Recording() {
		return ctx, s
	}
	user := Redact(c.User)
	s.s.SetAttributes(
		attribute.String(attrModel, c.Model),
		attribute.String(attrProvider, c.Provider),
		attribute.String(attrParams, jsonString(c.Params)),
		attribute.String(attrInMsgs+".0.message.role", "system"),
		attribute.String(attrInMsgs+".0.message.content", c.System),
		attribute.String(attrInMsgs+".1.message.role", "user"),
		attribute.String(attrInMsgs+".1.message.content", user),
		attribute.String(attrInput, user),
		attribute.String(attrInputMime, mimeText),
	)
	return ctx, s
}

// LLMResult records the model's raw output (redacted) and token usage.
func (s Span) LLMResult(output string, promptTokens, completionTokens int) {
	if !s.Recording() {
		return
	}
	out := Redact(output)
	s.s.SetAttributes(
		attribute.String(attrOutMsgs+".0.message.role", "assistant"),
		attribute.String(attrOutMsgs+".0.message.content", out),
		attribute.String(attrOutput, out),
		attribute.String(attrOutputMime, mimeJSON),
		attribute.Int(attrTokPrompt, promptTokens),
		attribute.Int(attrTokComplete, completionTokens),
		attribute.Int(attrTokTotal, promptTokens+completionTokens),
	)
}

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// Configured reports whether Phoenix is configured in the environment, which
// is what decides whether an account deletion queues its traces' deletion —
// also in a process where Start was not (yet) called.
func Configured() bool {
	_, ok := ConfigFromEnv()
	return ok
}
