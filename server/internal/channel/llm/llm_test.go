package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDecodeDraftToleratesFencesAndProse(t *testing.T) {
	text := "Sure! Here it is:\n```json\n{\"title\":\"Pick up laundry {wash & fold}\",\"category\":\"laundry\",\"description\":null,\"location_1\":\"12 Bedford Ave\",\"location_2\":null,\"duration_minutes\":30,\"scheduled\":false,\"scheduled_time\":null}\n```"
	d, err := decodeDraft(text)
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "Pick up laundry {wash & fold}" || d.Category != "laundry" || d.DurationMinutes != 30 {
		t.Fatalf("%+v", d)
	}
}

func TestValidateNeverInvents(t *testing.T) {
	d := TaskDraft{Title: " x ", Category: "spaceship", DurationMinutes: 99999, Scheduled: true, ScheduledTime: "tomorrow"}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if d.Title != "x" || d.Category != "anything_else" || d.DurationMinutes != 0 || d.Scheduled || d.ScheduledTime != "" {
		t.Fatalf("%+v", d)
	}
	if err := (&TaskDraft{}).Validate(); err == nil {
		t.Fatal("empty title must fail")
	}
}

func TestOpenAICompatParseTask(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer key" {
			http.Error(w, "bad", 401)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"title\":\"Grocery run\",\"category\":\"grocery\",\"description\":\"Peanut allergy: check labels.\",\"location_1\":\"Whole Foods on 3rd\",\"location_2\":null,\"duration_minutes\":60,\"scheduled\":false,\"scheduled_time\":null}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1200,"completion_tokens":80}}`))
	}))
	defer srv.Close()

	p := &OpenAICompat{BaseURL: srv.URL + "/v1", APIKey: "key", Model: "zai-org/GLM-5.3-Flash",
		ExtraBody: map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}}}
	d, u, err := p.ParseTask(context.Background(), "system", "groceries pls")
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "Grocery run" || d.Location1 != "Whole Foods on 3rd" || u.InputTokens != 1200 || u.OutputTokens != 80 {
		t.Fatalf("%+v %+v", d, u)
	}
	if got["model"] != "zai-org/GLM-5.3-Flash" || got["temperature"].(float64) != 0 {
		t.Fatalf("request: %+v", got)
	}
	if _, ok := got["chat_template_kwargs"]; !ok {
		t.Fatal("ExtraBody not merged")
	}
	rf, _ := got["response_format"].(map[string]any)
	if rf["type"] != "json_schema" {
		t.Fatalf("response_format: %+v", rf)
	}
}

func TestOpenAICompatTruncationIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"title\":"},"finish_reason":"length"}]}`))
	}))
	defer srv.Close()
	p := &OpenAICompat{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	if _, _, err := p.ParseTask(context.Background(), "s", "t"); err == nil {
		t.Fatal("truncated output must be an error")
	}
}

func TestOpenAICompatUnconfigured(t *testing.T) {
	if _, _, err := (&OpenAICompat{}).ParseTask(context.Background(), "s", "t"); err == nil {
		t.Fatal("unconfigured provider must error")
	}
}
