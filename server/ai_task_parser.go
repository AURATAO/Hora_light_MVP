// AI task parser: proxies a free-text task description to the Claude API
// and returns structured fields used to prefill the "Post a Task" form.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const aiTaskParserSystemPrompt = `You are a task parsing assistant for HO:RA, an on-demand task platform in NYC.
Parse the user's request and return ONLY a valid JSON object with no markdown, no explanation, no backticks:
{
  "title": "short task title max 60 chars",
  "category": "one of: delivery, grocery, laundry, companionship, queue, anything_else",
  "description": "any additional details from user input",
  "location_1": "pickup or starting address, empty string if not mentioned",
  "location_2": "drop-off address, empty string if not mentioned",
  "duration_minutes": 15 or 30 or 60 or 120,
  "scheduled": false,
  "scheduled_time": "ISO datetime string if user mentioned specific time, otherwise empty string"
}
If unsure about a field, leave it as empty string or use the closest valid value.`

type aiParseRequest struct {
	Input string `json:"input"`
}

type aiParsedTask struct {
	Title           string `json:"title"`
	Category        string `json:"category"`
	Description     string `json:"description"`
	Location1       string `json:"location_1"`
	Location2       string `json:"location_2"`
	DurationMinutes int    `json:"duration_minutes"`
	Scheduled       bool   `json:"scheduled"`
	ScheduledTime   string `json:"scheduled_time"`
}

func parseTaskWithAI(c *gin.Context) {
	var body aiParseRequest
	if err := c.BindJSON(&body); err != nil || strings.TrimSpace(body.Input) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "input required"})
		return
	}

	apiKey := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if apiKey == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI parsing unavailable"})
		return
	}

	reqBody, err := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 1000,
		"system":     aiTaskParserSystemPrompt,
		"messages": []map[string]string{
			{"role": "user", "content": body.Input},
		},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "request build failed"})
		return
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "request build failed"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "AI request failed"})
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read response failed"})
		return
	}
	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": "AI request failed"})
		return
	}

	var anthropicResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &anthropicResp); err != nil || len(anthropicResp.Content) == 0 {
		c.JSON(http.StatusBadGateway, gin.H{"error": "unexpected AI response"})
		return
	}

	text := strings.TrimSpace(anthropicResp.Content[0].Text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var parsed aiParsedTask
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "could not parse task"})
		return
	}

	c.JSON(http.StatusOK, parsed)
}
