package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAICompat calls any OpenAI-compatible chat-completions endpoint. First
// user: Baseten Model APIs (base https://inference.baseten.co/v1, model
// zai-org/GLM-5.3-Flash), which support tool calling and structured output
// (https://docs.baseten.co/inference/model-apis/overview).
type OpenAICompat struct {
	BaseURL string // e.g. https://inference.baseten.co/v1
	APIKey  string // BASETEN_API_KEY
	Model   string // e.g. zai-org/GLM-5.3-Flash
	// MaxTokens caps the reply. A draft is ~150 tokens; reasoning models
	// spend more before answering, hence the headroom.
	MaxTokens int
	// ExtraBody is merged into the request, for provider switches such as
	// turning reasoning off ({"chat_template_kwargs":{"enable_thinking":false}})
	// when the eval shows it costs latency without buying accuracy.
	ExtraBody map[string]any
	HTTP      *http.Client
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (o *OpenAICompat) ParseTask(ctx context.Context, systemPrompt, text string) (TaskDraft, Usage, error) {
	u := Usage{Model: o.Model}
	if o.BaseURL == "" || o.APIKey == "" || o.Model == "" {
		return TaskDraft{}, u, fmt.Errorf("llm parse: provider not configured")
	}
	maxTokens := o.MaxTokens
	if maxTokens == 0 {
		maxTokens = 2048
	}
	req := map[string]any{
		"model": o.Model,
		"messages": []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: text},
		},
		"max_tokens":  maxTokens,
		"temperature": 0,
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "task_draft",
				"strict": true,
				"schema": TaskDraftSchema(),
			},
		},
	}
	for k, v := range o.ExtraBody {
		req[k] = v
	}
	body, err := json.Marshal(req)
	if err != nil {
		return TaskDraft{}, u, fmt.Errorf("llm parse marshal: %w", err)
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(o.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return TaskDraft{}, u, fmt.Errorf("llm parse request: %w", err)
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
		return TaskDraft{}, u, fmt.Errorf("llm parse: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := string(respBody)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return TaskDraft{}, u, fmt.Errorf("llm parse: status %d: %s", resp.StatusCode, msg)
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
		return TaskDraft{}, u, fmt.Errorf("llm parse decode: %w", err)
	}
	u.InputTokens, u.OutputTokens = r.Usage.PromptTokens, r.Usage.CompletionTokens
	if len(r.Choices) == 0 {
		return TaskDraft{}, u, fmt.Errorf("llm parse: no choices")
	}
	if r.Choices[0].FinishReason == "length" {
		return TaskDraft{}, u, fmt.Errorf("llm parse: output truncated (max_tokens %d)", maxTokens)
	}
	d, err := decodeDraft(r.Choices[0].Message.Content)
	return d, u, err
}
