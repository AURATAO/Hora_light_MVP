package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"hora-auth/internal/tracing"
)

// OpenAICompat calls any OpenAI-compatible chat-completions endpoint. First
// user: Baseten Model APIs (base https://inference.baseten.co/v1, model
// zai-org/GLM-5.3-Flash), which support tool calling and structured output
// (https://docs.baseten.co/inference/model-apis/overview).
type OpenAICompat struct {
	BaseURL string // e.g. https://inference.baseten.co/v1
	APIKey  string // BASETEN_API_KEY
	Model   string // e.g. zai-org/GLM-5.3-Flash
	// MaxTokens caps a parse reply. A draft is ~150 tokens; reasoning models
	// spend more before answering, hence the headroom.
	MaxTokens int
	// ExtraBody is merged into every request, for provider switches such as
	// turning reasoning off ({"chat_template_kwargs":{"enable_thinking":false}})
	// when the eval shows it costs latency without buying accuracy.
	ExtraBody map[string]any
	HTTP      *http.Client
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Name is "baseten:<model>" on Baseten, else "<host>:<model>".
func (o *OpenAICompat) Name() string {
	host := providerHost(o.BaseURL)
	if host == "inference.baseten.co" {
		host = "baseten"
	}
	return host + ":" + o.Model
}

// CompleteJSON is completeJSON as a Provider.
func (o *OpenAICompat) CompleteJSON(ctx context.Context, system, user, schemaName string, schema map[string]any, maxTokens int) (string, Usage, error) {
	return o.completeJSON(ctx, system, user, schemaName, schema, maxTokens)
}

func (o *OpenAICompat) ParseTask(ctx context.Context, systemPrompt, text string) (TaskDraft, Usage, error) {
	return parseTask(ctx, o, systemPrompt, text)
}

func parseTask(ctx context.Context, m Provider, systemPrompt, text string) (d TaskDraft, u Usage, err error) {
	ctx, sp := tracing.StartSpan(ctx, "llm.ParseTask", tracing.KindChain)
	defer func() {
		sp.Input(text)
		sp.OutputValue(d) // the validated draft
		sp.Fail(err)
		sp.End()
	}()
	maxTokens := 2048
	if o, ok := m.(*OpenAICompat); ok && o.MaxTokens != 0 {
		maxTokens = o.MaxTokens
	}
	out, u, err := m.CompleteJSON(withoutPromptCache(ctx), systemPrompt, text, "task_draft", TaskDraftSchema(), maxTokens)
	if err != nil {
		return TaskDraft{}, u, fmt.Errorf("llm parse: %w", err)
	}
	d, err = decodeDraft(out)
	return d, u, wrapErr("ParseTask", err)
}

// completeJSON runs one schema-constrained completion and returns the raw
// message content.
func (o *OpenAICompat) completeJSON(ctx context.Context, system, user, schemaName string, schema map[string]any, maxTokens int) (out string, u Usage, err error) {
	u = Usage{Model: o.Model}
	// One LLM span per call (D-22): the prompt, the raw output and the
	// tokens, redacted. A no-op unless a message is being traced.
	_, sp := tracing.StartLLM(ctx, "llm."+schemaName, tracing.LLMCall{
		Model: o.Model, Provider: providerHost(o.BaseURL), System: system, User: user,
		Params: map[string]any{"max_tokens": maxTokens, "temperature": 0, "response_format": schemaName, "extra_body": o.ExtraBody},
	})
	defer func() {
		sp.LLMResult(out, u.InputTokens, u.OutputTokens)
		sp.Attr(attribute.Int64("hora.llm.latency_ms", u.Latency.Milliseconds()))
		sp.Fail(err)
		sp.End()
	}()
	if o.BaseURL == "" || o.APIKey == "" || o.Model == "" {
		return "", u, fmt.Errorf("provider not configured")
	}
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	req := map[string]any{
		"model": o.Model,
		"messages": []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		"max_tokens":  maxTokens,
		"temperature": 0,
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   schemaName,
				"strict": true,
				"schema": schema,
			},
		},
	}
	for k, v := range o.ExtraBody {
		req[k] = v
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", u, fmt.Errorf("marshal: %w", err)
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(o.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", u, fmt.Errorf("request: %w", err)
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+o.APIKey)

	client := o.HTTP
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	start := time.Now()
	resp, err := client.Do(hr)
	u.Latency = time.Since(start)
	if err != nil {
		return "", u, fmt.Errorf("llm request: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("[llm] close response body: %v", cerr)
		}
	}()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", u, fmt.Errorf("llm read response: status %d: %w", resp.StatusCode, err)
	}
	sp.Attr(attribute.Int("http.status_code", resp.StatusCode))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Status only: an error body can echo the prompt, i.e. the person's
		// message (S-12).
		return "", u, fmt.Errorf("llm: status %d", resp.StatusCode)
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &r); err != nil {
		// Fixed text: a JSON syntax error quotes a character of the body;
		// keep provider output out of logged errors entirely (S-12).
		return "", u, fmt.Errorf("decode: provider response is not JSON")
	}
	u.InputTokens, u.OutputTokens = r.Usage.PromptTokens, r.Usage.CompletionTokens
	if len(r.Choices) == 0 {
		return "", u, fmt.Errorf("no choices")
	}
	sp.Attr(attribute.String("hora.llm.finish_reason", r.Choices[0].FinishReason))
	if r.Choices[0].FinishReason == "length" {
		sp.Text("hora.llm.truncated_output", r.Choices[0].Message.Content)
		return "", u, fmt.Errorf("output truncated (max_tokens %d)", maxTokens)
	}
	return r.Choices[0].Message.Content, u, nil
}

// providerHost is the provider's host for the trace ("inference.baseten.co").
func providerHost(base string) string {
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host
	}
	return ""
}
