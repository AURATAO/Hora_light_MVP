// AI task parser: proxies a free-text task description to the Claude API
// and returns structured fields used to prefill the "Post a Task" form.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	// Embeds the IANA time zone database in the binary so LoadLocation works on
	// minimal container images that ship no /usr/share/zoneinfo.
	_ "time/tzdata"

	"github.com/gin-gonic/gin"
)

const (
	aiParserModel = "claude-sonnet-5"
	// Effort trades latency against extraction quality on messy, multilingual
	// input. Thinking is disabled (see buildAIParseRequest), so this is the only
	// depth control; "low" is noticeably faster if parses stay accurate.
	aiParserEffort = "medium"
	// The NYC beta runs on one wall clock. Relative times ("tomorrow afternoon")
	// are resolved against this zone.
	aiParserTimezone = "America/New_York"
	// Long inputs are the point of this endpoint, but an unbounded body is not.
	aiParseMaxInputChars = 4000
	aiParseMaxDuration   = 24 * 60
)

// aiParserCategories is the closed set the "Post a Task" form accepts. Enforced
// in the response schema and re-checked server-side.
var aiParserCategories = map[string]bool{
	"delivery":      true,
	"grocery":       true,
	"laundry":       true,
	"companionship": true,
	"queue":         true,
	"anything_else": true,
}

var nycLocation = loadNYCLocation()

func loadNYCLocation() *time.Location {
	loc, err := time.LoadLocation(aiParserTimezone)
	if err != nil {
		// Unreachable with time/tzdata embedded; UTC keeps the handler serving.
		return time.UTC
	}
	return loc
}

// aiTaskParserPromptTemplate is filled in per request so that "now" and the
// example timestamps always agree with each other. Verb order:
// now (human), now (RFC 3339), timezone, tomorrow 14:00, tomorrow 09:00,
// tomorrow 14:00.
const aiTaskParserPromptTemplate = `You extract structured task details from free-form text for HO:RA, an on-demand task marketplace operating in New York City. A requester describes what they need in their own words; you turn that into the fields that prefill the "Post a Task" form. The requester reviews and edits every field before the task is posted, so a partial, imperfect extraction is always more useful to them than a failure.

CURRENT DATE AND TIME
It is now %s, which is %s in timezone %s.
Resolve every relative time against that instant. "tomorrow afternoon", "tonight", "in 2 hours", "next Monday morning", "明天下午", "mañana por la tarde" must all become a concrete RFC 3339 timestamp carrying the New York UTC offset, for example %s.
Time-of-day defaults: morning is 09:00, afternoon is 14:00, evening is 18:00, night is 20:00, noon is 12:00.
Never emit a timestamp in the past. If a bare weekday or time of day has already passed today, use the next occurrence.

INPUT LANGUAGE
Input arrives in ANY language. English is the most common, but never assume it. Chinese, Spanish, Korean, and mixed-language input are all normal and must parse just as well.

OUTPUT LANGUAGE
"title" and "description" are ALWAYS written in clear, natural English, whatever language the input used, because Supporters across NYC have to be able to read every task.
Preserve proper nouns exactly as the requester wrote them, and never translate or transliterate them: business names ("Trader Joe's", "Levain Bakery"), street addresses ("167 W 74th St"), and people's names ("Aunt Mei"). Addresses in "location_1" and "location_2" are copied verbatim in the requester's original script.

FIELD MAPPING PRIORITY
First pull out everything that maps onto a form field:
- "title": a short imperative summary in English, at most 60 characters. Always produce one.
- "category": exactly one of delivery, grocery, laundry, companionship, queue, anything_else. Use anything_else when nothing else fits.
- "location_1": the pickup, starting, or only address mentioned. null if the requester named no place.
- "location_2": the drop-off or destination address. A phrase like "from X to Y" or "取了送到" fills BOTH location_1 and location_2. null when only one place is mentioned. Never repeat location_1 here.
- "duration_minutes": how long the task will take, as an integer. Prefer 15, 30, 60, or 120. null when the input gives no signal at all.
- "scheduled": true only when the requester asked for a specific later time. false for now, ASAP, or an unspecified time.
- "scheduled_time": the resolved RFC 3339 timestamp when "scheduled" is true, otherwise null. These two fields must always agree.

CATCH-ALL RULE, NEVER LOSE INFORMATION
Every detail in the input that does NOT map onto a field above belongs in "description": preferences and allergies, access and delivery instructions, apartment or buzzer numbers, budgets and payment notes, item counts, sizes, colors, brand choices, contact details, deadlines, who the task is really for, and anything else the Supporter would need in order to do the job right.
Write "description" as clean, scannable instructions for the Supporter, not as a transcript of what the requester typed. Drop filler ("hey", "can you please", "thanks so much") but keep every fact. Use null only when there genuinely is nothing beyond the mapped fields.

PARTIAL EXTRACTION BEATS FAILURE
Never refuse, never ask a clarifying question, never return an empty object. When a field has no signal in the input, set it to null (or false for "scheduled"). When the input is shapeless, put your best short summary in "title", put everything else in "description", and leave the rest null. Any signal at all is worth returning.

EXAMPLES

Input: grab me a coffee from the Starbucks on 8th ave
Output: {"title":"Pick up coffee from Starbucks","category":"delivery","description":null,"location_1":"the Starbucks on 8th Ave","location_2":null,"duration_minutes":15,"scheduled":false,"scheduled_time":null}

Input: need someone to take a package from my office at 350 5th Ave to my apartment at 200 W 79th St, it's a bit heavy so maybe bring a cart
Output: {"title":"Deliver package from office to apartment","category":"delivery","description":"The package is heavy, so bring a cart or dolly if possible.","location_1":"350 5th Ave","location_2":"200 W 79th St","duration_minutes":60,"scheduled":false,"scheduled_time":null}

Input: can someone walk my mom to her doctor appointment tomorrow morning? she's at 145 E 32nd St and she uses a cane
Output: {"title":"Walk my mom to her doctor appointment","category":"companionship","description":"The task is for the requester's mother, who uses a cane, so walk at her pace. Pick her up at 145 E 32nd St and accompany her to the appointment.","location_1":"145 E 32nd St","location_2":null,"duration_minutes":120,"scheduled":true,"scheduled_time":"%s"}

Input: hey so I'm having people over and I still need groceries, Whole Foods on 3rd is fine, I need like 2 bags of stuff, the usual salad things plus a rotisserie chicken, BUT one of my friends is super allergic to peanuts so please please check labels, budget is around 60 bucks, and when you get here ring the doorbell twice because the buzzer is broken, apt 4R, thanks so much
Output: {"title":"Grocery run for dinner party","category":"grocery","description":"About two bags of groceries: salad ingredients plus a rotisserie chicken. IMPORTANT: a guest has a severe peanut allergy, so check labels and avoid anything with peanuts or peanut traces. Budget is around 60 dollars. On delivery, go to apt 4R and ring the doorbell twice, as the buzzer is broken.","location_1":"Whole Foods on 3rd","location_2":null,"duration_minutes":60,"scheduled":false,"scheduled_time":null}

Input: ugh I have so much to do today and I just can't get to any of it, mostly the stuff piling up around the apartment
Output: {"title":"Help with tasks piling up around the apartment","category":"anything_else","description":"The requester is behind on a backlog of household tasks around their apartment and has not specified which ones yet. Confirm the exact list with them before starting.","location_1":null,"location_2":null,"duration_minutes":null,"scheduled":false,"scheduled_time":null}

Input: 幫我去 Levain Bakery 排隊買餅乾，地址是 167 W 74th St，明天下午，買六個巧克力口味的就好
Output: {"title":"Wait in line at Levain Bakery for cookies","category":"queue","description":"Wait in line at Levain Bakery and buy six chocolate chip cookies.","location_1":"167 W 74th St","location_2":null,"duration_minutes":60,"scheduled":true,"scheduled_time":"%s"}

Return the JSON object and nothing else.`

func aiTaskParserSystemPrompt(now time.Time) string {
	nowHuman := now.Format("Monday, 2 January 2006, 3:04 PM")
	nowRFC := now.Format(time.RFC3339)
	morning := time.Date(now.Year(), now.Month(), now.Day()+1, 9, 0, 0, 0, now.Location()).Format(time.RFC3339)
	afternoon := time.Date(now.Year(), now.Month(), now.Day()+1, 14, 0, 0, 0, now.Location()).Format(time.RFC3339)
	return fmt.Sprintf(aiTaskParserPromptTemplate,
		nowHuman, nowRFC, aiParserTimezone, afternoon, morning, afternoon)
}

// aiParseResponseSchema constrains the model's output. Absent fields come back
// as JSON null, which unmarshals to the Go zero value, so the response shape the
// clients see ("" / 0 / false) is unchanged.
func aiParseResponseSchema() map[string]any {
	nullableString := map[string]any{
		"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "null"},
		},
	}
	return map[string]any{
		"type": "json_schema",
		"schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{"type": "string"},
				"category": map[string]any{
					"type": "string",
					"enum": []string{"delivery", "grocery", "laundry", "companionship", "queue", "anything_else"},
				},
				"description": nullableString,
				"location_1":  nullableString,
				"location_2":  nullableString,
				"duration_minutes": map[string]any{
					"anyOf": []any{
						map[string]any{"type": "integer"},
						map[string]any{"type": "null"},
					},
				},
				"scheduled":      map[string]any{"type": "boolean"},
				"scheduled_time": nullableString,
			},
			"required": []string{
				"title", "category", "description", "location_1",
				"location_2", "duration_minutes", "scheduled", "scheduled_time",
			},
			"additionalProperties": false,
		},
	}
}

type aiParseRequest struct {
	Input string `json:"input"`
}

// aiParsedTask is the wire shape the mobile and web clients already consume.
// The clients split location_1 / location_2 back into the " | " encoded
// location_text on submit, so these stay separate strings here.
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
	input := strings.TrimSpace(body.Input)
	if len([]rune(input)) > aiParseMaxInputChars {
		c.JSON(http.StatusBadRequest, gin.H{"error": "description is too long — please shorten it"})
		return
	}

	apiKey := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if apiKey == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI parsing unavailable"})
		return
	}

	now := time.Now().In(nycLocation)

	reqBody, err := json.Marshal(map[string]any{
		"model":      aiParserModel,
		"max_tokens": 2000,
		"system":     aiTaskParserSystemPrompt(now),
		// Extraction against a strict schema is not a reasoning task, and the
		// client is blocking on this call. Leaving thinking on (the default for
		// this model) also puts a content-free thinking block first in the
		// response.
		"thinking": map[string]any{"type": "disabled"},
		"output_config": map[string]any{
			"effort": aiParserEffort,
			"format": aiParseResponseSchema(),
		},
		"messages": []map[string]string{
			{"role": "user", "content": input},
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
		// Distinguishes a 20s timeout from a network failure in the logs.
		log.Printf("[ai-parse] upstream call failed: %v", err)
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
		// The upstream body carries the actual reason (a rejected schema, a bad
		// model id, a rate limit). Swallowing it made every failure look alike.
		log.Printf("[ai-parse] upstream %d: %s", resp.StatusCode, truncateForLog(respBody, 500))
		c.JSON(http.StatusBadGateway, gin.H{"error": "AI request failed"})
		return
	}

	var anthropicResp struct {
		Content    []anthropicContentBlock `json:"content"`
		StopReason string                  `json:"stop_reason"`
	}
	if err := json.Unmarshal(respBody, &anthropicResp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "unexpected AI response"})
		return
	}
	if anthropicResp.StopReason == "refusal" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": aiParseFailureMessage})
		return
	}

	text := aiResponseText(anthropicResp.Content)
	parsed, ok := decodeParsedTask(text, now)
	if !ok {
		log.Printf("[ai-parse] unusable model output (stop_reason=%s): %s",
			anthropicResp.StopReason, truncateForLog([]byte(text), 500))
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": aiParseFailureMessage})
		return
	}

	c.JSON(http.StatusOK, parsed)
}

// aiParseFailureMessage reaches the user verbatim: ApiError surfaces the body's
// "error" string as the thrown Error's message.
const aiParseFailureMessage = "Couldn't read that description — please fill the form in manually."

func truncateForLog(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…(truncated)"
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// aiResponseText concatenates every text block. Indexing content[0] is not safe:
// non-text blocks (thinking, for one) can precede the answer.
func aiResponseText(content []anthropicContentBlock) string {
	var b strings.Builder
	for _, block := range content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

// decodeParsedTask turns raw model text into a task, or reports failure so the
// caller can send a 422 rather than an empty form.
func decodeParsedTask(text string, now time.Time) (aiParsedTask, bool) {
	obj := extractJSONObject(text)
	if obj == "" {
		return aiParsedTask{}, false
	}
	var parsed aiParsedTask
	if err := json.Unmarshal([]byte(obj), &parsed); err != nil {
		return aiParsedTask{}, false
	}
	if !normalizeParsedTask(&parsed, now) {
		return aiParsedTask{}, false
	}
	return parsed, true
}

// extractJSONObject pulls the first complete JSON object out of a model
// response. The response schema should make this a no-op, but a markdown fence
// or a sentence of preamble is not worth failing the whole parse over.
func extractJSONObject(s string) string {
	s = strings.TrimSpace(s)
	if unfenced := stripCodeFence(s); unfenced != "" {
		s = unfenced
	}

	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		switch {
		case escaped:
			escaped = false
		case inString && ch == '\\':
			escaped = true
		case ch == '"':
			inString = !inString
		case inString:
			// Braces inside string literals are not structural.
		case ch == '{':
			depth++
		case ch == '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// stripCodeFence removes a leading ```lang fence and its closing fence. Returns
// "" when s is not fenced, leaving the caller's original string in play.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return ""
	}
	rest := s[len("```"):]
	nl := strings.IndexByte(rest, '\n')
	if nl < 0 {
		// Single-line fence; the brace scan handles it.
		return ""
	}
	rest = rest[nl+1:]
	if end := strings.LastIndex(rest, "```"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

// normalizeParsedTask enforces the invariants the clients rely on and reports
// whether anything usable survived. It is deliberately forgiving: a bad value in
// one field clears that field rather than failing the whole parse.
func normalizeParsedTask(p *aiParsedTask, now time.Time) bool {
	p.Title = strings.TrimSpace(p.Title)
	p.Category = strings.TrimSpace(p.Category)
	p.Description = strings.TrimSpace(p.Description)
	p.Location1 = strings.TrimSpace(p.Location1)
	p.Location2 = strings.TrimSpace(p.Location2)
	p.ScheduledTime = strings.TrimSpace(p.ScheduledTime)

	// No title means nothing usable came back; a 422 beats a blank form.
	if p.Title == "" {
		return false
	}

	if !aiParserCategories[p.Category] {
		p.Category = "anything_else"
	}

	// The clients render location_1 and location_2 as separate rows, so a
	// duplicated address would show up twice.
	if p.Location2 != "" && strings.EqualFold(p.Location1, p.Location2) {
		p.Location2 = ""
	}
	// A drop-off with no pickup belongs in the first row.
	if p.Location1 == "" && p.Location2 != "" {
		p.Location1, p.Location2 = p.Location2, ""
	}

	if p.DurationMinutes < 0 || p.DurationMinutes > aiParseMaxDuration {
		p.DurationMinutes = 0
	}

	p.normalizeSchedule(now)
	return true
}

// normalizeSchedule keeps scheduled and scheduled_time in agreement and drops
// times the client would reject anyway. Falling back to "immediate" is safe:
// the requester picks a time on the review screen, and the original wording is
// preserved in description.
func (p *aiParsedTask) normalizeSchedule(now time.Time) {
	if p.ScheduledTime == "" {
		p.Scheduled = false
		return
	}
	at, err := time.Parse(time.RFC3339, p.ScheduledTime)
	if err != nil || !at.After(now) {
		p.Scheduled = false
		p.ScheduledTime = ""
		return
	}
	p.Scheduled = true
	p.ScheduledTime = at.In(nycLocation).Format(time.RFC3339)
}
