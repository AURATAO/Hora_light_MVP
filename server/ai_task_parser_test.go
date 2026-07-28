package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testNow(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, time.July, 27, 15, 4, 0, 0, nycLocation)
}

func TestExtractJSONObject(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare object", `{"title":"a"}`, `{"title":"a"}`},
		{"leading and trailing space", "  \n{\"title\":\"a\"}\n ", `{"title":"a"}`},
		{"json fence", "```json\n{\"title\":\"a\"}\n```", `{"title":"a"}`},
		{"bare fence", "```\n{\"title\":\"a\"}\n```", `{"title":"a"}`},
		{"single line fence", "```json {\"title\":\"a\"} ```", `{"title":"a"}`},
		{"prose preamble", "Here is the JSON:\n{\"title\":\"a\"}", `{"title":"a"}`},
		{"trailing prose", "{\"title\":\"a\"}\nHope that helps!", `{"title":"a"}`},
		{"nested object", `{"a":{"b":1},"c":2}`, `{"a":{"b":1},"c":2}`},
		{"brace inside string", `{"title":"use {this} brace"}`, `{"title":"use {this} brace"}`},
		{"escaped quote before brace", `{"title":"say \"hi\" }now"}`, `{"title":"say \"hi\" }now"}`},
		{"no object", "I could not parse that.", ""},
		{"unterminated object", `{"title":"a"`, ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractJSONObject(tc.in); got != tc.want {
				t.Errorf("extractJSONObject(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDecodeParsedTaskHappyPath(t *testing.T) {
	now := testNow(t)
	raw := "```json\n" + `{
	  "title": "Pick up laundry",
	  "category": "laundry",
	  "description": "Blue bag, not the red one.",
	  "location_1": "A Cleaners, 200 W 79th St",
	  "location_2": "350 5th Ave",
	  "duration_minutes": 60,
	  "scheduled": true,
	  "scheduled_time": "2026-07-28T14:00:00-04:00"
	}` + "\n```"

	got, ok := decodeParsedTask(raw, now)
	if !ok {
		t.Fatal("decodeParsedTask returned ok=false for a well-formed response")
	}
	if got.Title != "Pick up laundry" || got.Category != "laundry" {
		t.Errorf("title/category = %q/%q", got.Title, got.Category)
	}
	if got.Location1 != "A Cleaners, 200 W 79th St" || got.Location2 != "350 5th Ave" {
		t.Errorf("locations = %q / %q", got.Location1, got.Location2)
	}
	if got.DurationMinutes != 60 {
		t.Errorf("duration = %d, want 60", got.DurationMinutes)
	}
	if !got.Scheduled || got.ScheduledTime != "2026-07-28T14:00:00-04:00" {
		t.Errorf("schedule = %v / %q", got.Scheduled, got.ScheduledTime)
	}
}

// Absent fields come back as JSON null. Go decodes null into the zero value, so
// the clients keep seeing "" / 0 / false and their contract is unchanged.
func TestDecodeParsedTaskNullsBecomeZeroValues(t *testing.T) {
	now := testNow(t)
	raw := `{"title":"Help around the apartment","category":"anything_else","description":null,
	         "location_1":null,"location_2":null,"duration_minutes":null,
	         "scheduled":false,"scheduled_time":null}`

	got, ok := decodeParsedTask(raw, now)
	if !ok {
		t.Fatal("decodeParsedTask returned ok=false for an all-null response")
	}
	if got.Description != "" || got.Location1 != "" || got.Location2 != "" {
		t.Errorf("nullable strings did not zero out: %+v", got)
	}
	if got.DurationMinutes != 0 || got.Scheduled || got.ScheduledTime != "" {
		t.Errorf("nullable non-strings did not zero out: %+v", got)
	}

	// The JSON the clients receive must still carry every key as a plain value.
	out, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		`"description":""`, `"location_1":""`, `"location_2":""`,
		`"duration_minutes":0`, `"scheduled":false`, `"scheduled_time":""`,
	} {
		if !strings.Contains(string(out), key) {
			t.Errorf("response JSON missing %s: %s", key, out)
		}
	}
}

func TestDecodeParsedTaskRejectsUnusable(t *testing.T) {
	now := testNow(t)
	cases := []struct {
		name string
		in   string
	}{
		{"no json at all", "I'm sorry, I can't help with that."},
		{"empty response", ""},
		{"truncated json", `{"title":"Pick up la`},
		{"empty title", `{"title":"","category":"delivery"}`},
		{"whitespace title", `{"title":"   ","category":"delivery"}`},
		{"empty object", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := decodeParsedTask(tc.in, now); ok {
				t.Errorf("decodeParsedTask(%q) returned ok=true; a 422 was expected", tc.in)
			}
		})
	}
}

func TestNormalizeParsedTaskCategory(t *testing.T) {
	now := testNow(t)
	for _, in := range []string{"", "shopping", "DELIVERY", "anything else"} {
		p := aiParsedTask{Title: "t", Category: in}
		if !normalizeParsedTask(&p, now) {
			t.Fatalf("normalize returned false for category %q", in)
		}
		if !aiParserCategories[p.Category] {
			t.Errorf("category %q normalized to %q, which the form does not accept", in, p.Category)
		}
	}

	p := aiParsedTask{Title: "t", Category: "grocery"}
	normalizeParsedTask(&p, now)
	if p.Category != "grocery" {
		t.Errorf("valid category was rewritten to %q", p.Category)
	}
}

func TestNormalizeParsedTaskLocations(t *testing.T) {
	now := testNow(t)

	dup := aiParsedTask{Title: "t", Location1: "350 5th Ave", Location2: "350 5th ave"}
	normalizeParsedTask(&dup, now)
	if dup.Location2 != "" {
		t.Errorf("duplicate location_2 survived: %q", dup.Location2)
	}

	dropOffOnly := aiParsedTask{Title: "t", Location2: "200 W 79th St"}
	normalizeParsedTask(&dropOffOnly, now)
	if dropOffOnly.Location1 != "200 W 79th St" || dropOffOnly.Location2 != "" {
		t.Errorf("lone drop-off not promoted to the first row: %q / %q",
			dropOffOnly.Location1, dropOffOnly.Location2)
	}

	both := aiParsedTask{Title: "t", Location1: "  A  ", Location2: "  B  "}
	normalizeParsedTask(&both, now)
	if both.Location1 != "A" || both.Location2 != "B" {
		t.Errorf("distinct locations were altered: %q / %q", both.Location1, both.Location2)
	}
}

func TestNormalizeParsedTaskDuration(t *testing.T) {
	now := testNow(t)
	cases := map[int]int{0: 0, 15: 15, 120: 120, 1440: 1440, -30: 0, 100000: 0}
	for in, want := range cases {
		p := aiParsedTask{Title: "t", DurationMinutes: in}
		normalizeParsedTask(&p, now)
		if p.DurationMinutes != want {
			t.Errorf("duration %d normalized to %d, want %d", in, p.DurationMinutes, want)
		}
	}
}

func TestNormalizeSchedule(t *testing.T) {
	now := testNow(t)
	cases := []struct {
		name          string
		in            aiParsedTask
		wantScheduled bool
		wantTime      string
	}{
		{
			name:          "future time sets scheduled",
			in:            aiParsedTask{Title: "t", Scheduled: false, ScheduledTime: "2026-07-28T14:00:00-04:00"},
			wantScheduled: true,
			wantTime:      "2026-07-28T14:00:00-04:00",
		},
		{
			name:          "utc offset is renormalized to New York",
			in:            aiParsedTask{Title: "t", Scheduled: true, ScheduledTime: "2026-07-28T18:00:00Z"},
			wantScheduled: true,
			wantTime:      "2026-07-28T14:00:00-04:00",
		},
		{
			name:          "scheduled true without a time falls back to immediate",
			in:            aiParsedTask{Title: "t", Scheduled: true},
			wantScheduled: false,
		},
		{
			name:          "past time falls back to immediate",
			in:            aiParsedTask{Title: "t", Scheduled: true, ScheduledTime: "2026-07-26T14:00:00-04:00"},
			wantScheduled: false,
		},
		{
			name:          "unparseable time falls back to immediate",
			in:            aiParsedTask{Title: "t", Scheduled: true, ScheduledTime: "tomorrow afternoon"},
			wantScheduled: false,
		},
		{
			name:          "date-only string falls back to immediate",
			in:            aiParsedTask{Title: "t", Scheduled: true, ScheduledTime: "2026-07-28"},
			wantScheduled: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.in
			normalizeParsedTask(&p, now)
			if p.Scheduled != tc.wantScheduled {
				t.Errorf("scheduled = %v, want %v", p.Scheduled, tc.wantScheduled)
			}
			if p.Scheduled && p.ScheduledTime != tc.wantTime {
				t.Errorf("scheduled_time = %q, want %q", p.ScheduledTime, tc.wantTime)
			}
			if !p.Scheduled && p.ScheduledTime != "" {
				t.Errorf("scheduled_time = %q, want cleared", p.ScheduledTime)
			}
		})
	}
}

func TestAIResponseTextSkipsNonTextBlocks(t *testing.T) {
	// The regression this endpoint actually hit: a leading thinking block with
	// empty text made content[0].Text "", so every parse 422'd.
	blocks := []anthropicContentBlock{
		{Type: "thinking", Text: ""},
		{Type: "text", Text: `{"title":"a"}`},
	}
	if got := aiResponseText(blocks); got != `{"title":"a"}` {
		t.Errorf("aiResponseText = %q, want the text block's contents", got)
	}
	if got := aiResponseText(nil); got != "" {
		t.Errorf("aiResponseText(nil) = %q, want empty", got)
	}
}

func TestSystemPromptInjectsCurrentTimeAndTimezone(t *testing.T) {
	now := testNow(t)
	prompt := aiTaskParserSystemPrompt(now)

	for _, want := range []string{
		"2026-07-27T15:04:00-04:00",     // now, RFC 3339 with the NY offset
		"Monday, 27 July 2026, 3:04 PM", // now, human readable
		aiParserTimezone,                // the zone itself
		"2026-07-28T14:00:00-04:00",     // tomorrow afternoon, used by two examples
		"2026-07-28T09:00:00-04:00",     // tomorrow morning
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "%!") {
		t.Errorf("system prompt has a formatting error: %s", prompt)
	}
}

// Example timestamps are derived from "now", so they must never point backwards.
func TestSystemPromptExampleTimesAreInTheFuture(t *testing.T) {
	// 23:30 local is the interesting case: naive "today at 14:00" arithmetic
	// would produce a past example.
	now := time.Date(2026, time.December, 31, 23, 30, 0, 0, nycLocation)
	prompt := aiTaskParserSystemPrompt(now)

	for _, stamp := range []string{"2027-01-01T09:00:00-05:00", "2027-01-01T14:00:00-05:00"} {
		if !strings.Contains(prompt, stamp) {
			t.Errorf("system prompt missing next-day example %q", stamp)
		}
		at, err := time.Parse(time.RFC3339, stamp)
		if err != nil {
			t.Fatalf("parse %q: %v", stamp, err)
		}
		if !at.After(now) {
			t.Errorf("example timestamp %q is not after now", stamp)
		}
	}
}

func TestAIParseResponseSchemaShape(t *testing.T) {
	raw, err := json.Marshal(aiParseResponseSchema())
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var schema struct {
		Type   string `json:"type"`
		Schema struct {
			Type                 string          `json:"type"`
			Properties           map[string]any  `json:"properties"`
			Required             []string        `json:"required"`
			AdditionalProperties json.RawMessage `json:"additionalProperties"`
		} `json:"schema"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if schema.Type != "json_schema" || schema.Schema.Type != "object" {
		t.Fatalf("unexpected schema envelope: %s", raw)
	}
	if string(schema.Schema.AdditionalProperties) != "false" {
		t.Errorf("additionalProperties = %s, want false (structured outputs requires it)",
			schema.Schema.AdditionalProperties)
	}

	// Every field the clients read must be declared and required.
	fields := []string{
		"title", "category", "description", "location_1",
		"location_2", "duration_minutes", "scheduled", "scheduled_time",
	}
	for _, f := range fields {
		if _, ok := schema.Schema.Properties[f]; !ok {
			t.Errorf("schema is missing property %q", f)
		}
	}
	if len(schema.Schema.Required) != len(fields) {
		t.Errorf("required has %d entries, want %d", len(schema.Schema.Required), len(fields))
	}
}
