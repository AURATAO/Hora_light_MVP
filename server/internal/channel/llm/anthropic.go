package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"go.opentelemetry.io/otel/attribute"

	"hora-auth/internal/tracing"
)

// Anthropic calls the Claude Messages API through the official Go SDK. It is
// a Provider like OpenAICompat: the same prompts, schemas and decoding, with
// the schema enforced by structured outputs (output_config.format).
//
// No temperature: the API version this SDK sends (2026-10-21) rejects
// temperature on every model. Output is pinned by the schema instead, and
// thinking is kept off where the model allows it (Sonnet 5.5:
// "between_tools") or at low effort where it is always on (Opus 5.5).
type Anthropic struct {
	APIKey string // ANTHROPIC_API_KEY
	Model  string // e.g. claude-sonnet-5-5
	// Effort is output_config.effort; "" = low. A classification needs no
	// more, and every level up costs latency on each message.
	Effort string
	// BaseURL overrides the API host (tests); "" = the SDK's default.
	BaseURL string
	// HTTP carries the timeout; nil = 45s, the same as OpenAICompat.
	HTTP *http.Client

	once   sync.Once
	client *anthropic.Client
}

func (a *Anthropic) Name() string { return "anthropic:" + a.Model }

func (a *Anthropic) sdk() *anthropic.Client {
	a.once.Do(func() {
		hc := a.HTTP
		if hc == nil {
			hc = &http.Client{Timeout: 45 * time.Second}
		}
		opts := []option.RequestOption{
			option.WithAPIKey(a.APIKey),
			option.WithHTTPClient(hc),
			// No SDK retries: a failed call goes to the fallback provider
			// (Model.Fallback) instead, inside the message's own deadline.
			option.WithMaxRetries(0),
		}
		if a.BaseURL != "" {
			opts = append(opts, option.WithBaseURL(a.BaseURL))
		}
		c := anthropic.NewClient(opts...)
		a.client = &c
	})
	return a.client
}

// thinksAlways: models on which thinking cannot be turned off (a
// {type:"disabled"} request is a 400); they get a larger max_tokens.
func thinksAlways(model string) bool {
	for _, p := range []string{"claude-opus-5-5", "claude-fable-", "claude-mythos-"} {
		if strings.HasPrefix(model, p) {
			return true
		}
	}
	return false
}

// thinkingFor turns up-front thinking off where the model allows it.
func thinkingFor(model string) anthropic.ThinkingConfigParamUnion {
	switch {
	case strings.HasPrefix(model, "claude-sonnet-5-5"):
		// Sonnet 5.5 rejects "disabled"; "between_tools" is its off switch
		// (accepted at effort high or below).
		return anthropic.ThinkingConfigParamUnion{OfBetweenTools: &anthropic.ThinkingConfigBetweenToolsParam{}}
	case thinksAlways(model):
		return anthropic.ThinkingConfigParamUnion{} // omitted: adaptive, steered by effort
	}
	return anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}}
}

type noCacheKey struct{}

// withoutPromptCache marks a call whose system prompt changes every call:
// a cache write (1.25x input) that is never read is pure cost. The parse
// prompt carries "now" to the second (aiTaskParserSystemPrompt).
func withoutPromptCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, noCacheKey{}, true)
}

func promptCacheOff(ctx context.Context) bool {
	off, _ := ctx.Value(noCacheKey{}).(bool)
	return off
}

// minThinkingTokens: room for adaptive thinking before the JSON on a model
// that always thinks; the channel's own caps (256 for a label) assume none.
const minThinkingTokens = 4096

// CompleteJSON runs one schema-constrained completion.
func (a *Anthropic) CompleteJSON(ctx context.Context, system, user, schemaName string, schema map[string]any, maxTokens int) (string, Usage, error) {
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	if thinksAlways(a.Model) && maxTokens < minThinkingTokens {
		maxTokens = minThinkingTokens
	}
	effort := a.Effort
	if effort == "" {
		effort = "low"
	}
	sys := anthropic.TextBlockParam{Text: system}
	if !promptCacheOff(ctx) {
		// The agent's and the splitter's prompts are fixed text over the
		// 512-token minimum on current models (the agent's is ~3K): cached,
		// they are read at 5% of the input price.
		sys.CacheControl = anthropic.NewCacheControlEphemeralParam()
	}
	p := anthropic.MessageNewParams{
		MaxTokens: int64(maxTokens),
		System:    []anthropic.TextBlockParam{sys},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(user))},
		Thinking:  thinkingFor(a.Model),
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffort(effort),
			Format: anthropic.JSONOutputFormatParam{Schema: structuredSchema(schema)},
		},
	}
	msg, u, err := a.Messages(ctx, schemaName, p)
	if err != nil {
		return "", u, err
	}
	return messageText(msg), u, nil
}

// Messages is one Messages API call with the channel's guarantees: one LLM
// span (provider, model, latency, tokens incl. cache), errors with a status
// or a fixed text only (S-12), and refusal / truncation as errors. Any
// MessageNewParams may be passed — tools included — so a tool-using step
// (place discovery) is built on this, not beside it. Model defaults to
// a.Model.
func (a *Anthropic) Messages(ctx context.Context, name string, p anthropic.MessageNewParams) (msg *anthropic.Message, u Usage, err error) {
	if p.Model == "" {
		p.Model = anthropic.Model(a.Model)
	}
	u = Usage{Model: string(p.Model)}
	var out string
	_, sp := tracing.StartLLM(ctx, "llm."+name, tracing.LLMCall{
		Model: string(p.Model), Provider: "anthropic", System: systemText(p), User: lastUserText(p),
		Params: map[string]any{
			"max_tokens": p.MaxTokens, "effort": p.OutputConfig.Effort, "thinking": thinkingLabel(p.Thinking),
			"response_format": name, "tools": len(p.Tools),
		},
	})
	defer func() {
		sp.LLMResult(out, u.PromptTokens(), u.OutputTokens)
		sp.Attr(
			attribute.Int64("hora.llm.latency_ms", u.Latency.Milliseconds()),
			attribute.Int("llm.token_count.prompt_details.cache_read", u.CacheReadTokens),
			attribute.Int("llm.token_count.prompt_details.cache_write", u.CacheWriteTokens),
		)
		sp.Fail(err)
		sp.End()
	}()
	if a.APIKey == "" || a.Model == "" {
		return nil, u, fmt.Errorf("provider not configured")
	}
	start := time.Now()
	msg, err = a.sdk().Messages.New(ctx, p)
	u.Latency = time.Since(start)
	if err != nil {
		return nil, u, anthropicErr(ctx, err)
	}
	u.InputTokens = int(msg.Usage.InputTokens)
	u.OutputTokens = int(msg.Usage.OutputTokens)
	u.CacheReadTokens = int(msg.Usage.CacheReadInputTokens)
	u.CacheWriteTokens = int(msg.Usage.CacheCreationInputTokens)
	out = messageText(msg)
	sp.Attr(attribute.String("hora.llm.finish_reason", string(msg.StopReason)))
	switch msg.StopReason {
	case anthropic.StopReasonRefusal:
		// Fixed text: the category is a label, the explanation is not ours.
		return nil, u, fmt.Errorf("anthropic: refused (%s)", msg.StopDetails.Category)
	case anthropic.StopReasonMaxTokens:
		sp.Text("hora.llm.truncated_output", out)
		out = ""
		return nil, u, fmt.Errorf("output truncated (max_tokens %d)", p.MaxTokens)
	}
	return msg, u, nil
}

// anthropicErr keeps the status and drops the body: an API error's text
// includes the response, which can echo the prompt (S-12).
func anthropicErr(ctx context.Context, err error) error {
	var apiErr *anthropic.Error
	var ne net.Error
	switch {
	case errors.As(err, &apiErr):
		return &StatusError{Provider: "anthropic", Status: apiErr.StatusCode}
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return fmt.Errorf("anthropic: %w", ErrTimeout)
	case ctx.Err() != nil:
		return fmt.Errorf("anthropic: %w", ctx.Err())
	}
	return errors.New("anthropic: request failed")
}

// messageText is the response's text blocks, in order (thinking skipped).
func messageText(msg *anthropic.Message) string {
	if msg == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range msg.Content {
		if t, ok := c.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

func systemText(p anthropic.MessageNewParams) string {
	var parts []string
	for _, s := range p.System {
		parts = append(parts, s.Text)
	}
	return strings.Join(parts, "\n\n")
}

// lastUserText is the newest user turn's text, for the trace (redacted
// there).
func lastUserText(p anthropic.MessageNewParams) string {
	for i := len(p.Messages) - 1; i >= 0; i-- {
		m := p.Messages[i].OfUser
		if m == nil {
			continue
		}
		var parts []string
		for _, c := range m.Content {
			if c.OfText != nil {
				parts = append(parts, c.OfText.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func thinkingLabel(t anthropic.ThinkingConfigParamUnion) string {
	switch {
	case t.OfBetweenTools != nil:
		return "between_tools"
	case t.OfDisabled != nil:
		return "disabled"
	case t.OfAdaptive != nil:
		return "adaptive"
	case t.OfEnabled != nil:
		return "enabled"
	}
	return "default"
}

// structuredSchema drops the constraints structured outputs do not accept
// (array sizes, numeric and string bounds) from a copy of s. The decoders
// enforce them after the call (SplitErrands caps at MaxErrands and
// rejects an empty list), so nothing is lost.
func structuredSchema(s map[string]any) map[string]any {
	out, _ := stripUnsupported(s).(map[string]any)
	return out
}

var unsupportedKeys = map[string]bool{
	"minItems": true, "maxItems": true, "uniqueItems": true,
	"minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true, "multipleOf": true,
	"minLength": true, "maxLength": true,
}

func stripUnsupported(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			if !unsupportedKeys[k] {
				m[k] = stripUnsupported(x)
			}
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = stripUnsupported(x)
		}
		return out
	}
	return v
}
