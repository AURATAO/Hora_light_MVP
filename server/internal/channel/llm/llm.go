// Package llm is the language-model boundary for the messaging channel.
//
// The model translates free text into structured data and never decides
// anything: money moves only through the router's state machine on an
// explicit YES (PLAN §6). Models are swappable behind Parser and chosen by
// configuration after the eval run (PLAN §9), never by feel.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TaskDraft mirrors the wire shape of the existing POST /tasks/ai-parse
// response (server/ai_task_parser.go aiParsedTask), so a draft from a text
// message and a draft from the app fill the same task fields.
type TaskDraft struct {
	Title           string `json:"title"`
	Category        string `json:"category"`
	Description     string `json:"description"`
	Location1       string `json:"location_1"`
	Location2       string `json:"location_2"`
	DurationMinutes int    `json:"duration_minutes"`
	Scheduled       bool   `json:"scheduled"`
	ScheduledTime   string `json:"scheduled_time"`
}

// Categories is the closed set the task form accepts; re-checked after every
// parse because a schema-constrained model can still be served by a provider
// that ignores the schema.
var Categories = map[string]bool{
	"delivery": true, "grocery": true, "laundry": true,
	"companionship": true, "queue": true, "anything_else": true,
}

// Validate normalizes a draft in place and rejects one that cannot become a
// task form. It never invents data: an unknown category becomes
// anything_else, a missing title is an error.
func (d *TaskDraft) Validate() error {
	d.Title = strings.TrimSpace(d.Title)
	if d.Title == "" {
		return errors.New("draft: empty title")
	}
	if !Categories[d.Category] {
		d.Category = "anything_else"
	}
	if d.DurationMinutes < 0 || d.DurationMinutes > 24*60 {
		d.DurationMinutes = 0
	}
	if d.Scheduled {
		if _, err := time.Parse(time.RFC3339, d.ScheduledTime); err != nil {
			d.Scheduled, d.ScheduledTime = false, ""
		}
	} else {
		d.ScheduledTime = ""
	}
	return nil
}

// Parser turns a requester's message into a task draft.
type Parser interface {
	// ParseTask returns a validated draft. systemPrompt carries "now" and the
	// extraction rules; it is built by the caller so the app endpoint and the
	// messaging channel share one prompt.
	ParseTask(ctx context.Context, systemPrompt, text string) (TaskDraft, Usage, error)
}

// Usage is what one call cost, for the eval and for cost tracking.
type Usage struct {
	Model        string
	InputTokens  int
	OutputTokens int
	Latency      time.Duration
}

// TaskDraftSchema is the JSON schema the model's output must match. Kept in
// step with aiParseResponseSchema in server/ai_task_parser.go.
func TaskDraftSchema() map[string]any {
	ns := map[string]any{"anyOf": []any{
		map[string]any{"type": "string"}, map[string]any{"type": "null"},
	}}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{"type": "string"},
			"category": map[string]any{
				"type": "string",
				"enum": []string{"delivery", "grocery", "laundry", "companionship", "queue", "anything_else"},
			},
			"description": ns,
			"location_1":  ns,
			"location_2":  ns,
			"duration_minutes": map[string]any{"anyOf": []any{
				map[string]any{"type": "integer"}, map[string]any{"type": "null"},
			}},
			"scheduled":      map[string]any{"type": "boolean"},
			"scheduled_time": ns,
		},
		"required": []string{
			"title", "category", "description", "location_1",
			"location_2", "duration_minutes", "scheduled", "scheduled_time",
		},
		"additionalProperties": false,
	}
}

// decodeDraft reads the model's text as a TaskDraft, tolerating code fences
// and leading prose from providers that don't enforce the schema.
func decodeDraft(text string) (TaskDraft, error) {
	obj := extractJSONObject(text)
	if obj == "" {
		return TaskDraft{}, fmt.Errorf("decode draft: no JSON object in model output")
	}
	var d TaskDraft
	if err := json.Unmarshal([]byte(obj), &d); err != nil {
		return TaskDraft{}, fmt.Errorf("decode draft: %w", err)
	}
	if err := d.Validate(); err != nil {
		return TaskDraft{}, fmt.Errorf("decodeDraft: %w", err)
	}
	return d, nil
}

// extractJSONObject returns the first balanced {...} in s, respecting strings.
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// wrapErr adds context to err and keeps nil as nil (S-31).
func wrapErr(where string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", where, err)
}
