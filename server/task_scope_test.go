package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"hora-auth/internal/channel/llm"
)

// The scope rules on every task creation path (task_scope.go): POST /tasks
// from the web form and the app, an edit that changes the words, and the AI
// prefill in front of the frozen parser.

type stubScope struct {
	verdict string
	err     error
	calls   int
}

func (s *stubScope) CheckScope(context.Context, string) (string, error) {
	s.calls++
	return s.verdict, s.err
}

func withScopeModel(t *testing.T, m llm.ScopeChecker) {
	t.Helper()
	prev := taskScopeModel
	taskScopeModel = m
	t.Cleanup(func() { taskScopeModel = prev })
}

func refusalReply(t *testing.T, id string) string {
	t.Helper()
	s, _ := llm.Playbooks()
	r := s.ScopeRules.Refusal(id)
	if r == nil {
		t.Fatalf("no category %s", id)
	}
	return r.Reply
}

// A refused post is 422 with the category and its own reply as the message
// (what the web form shows), before anything is stored — no database needed.
func TestCreateTaskRefusesOutOfScope(t *testing.T) {
	withScopeModel(t, &stubScope{verdict: "allow"})
	for body, cat := range map[string]string{
		`{"title":"Keep me company","description":"come over, I'm home alone","category":"companionship","is_immediate":true}`:   "private_company",
		`{"title":"Massage","description":"a friendly massage after work","category":"anything_else","is_immediate":true}`:       "intimate",
		`{"title":"Ride to JFK","description":"drive me to JFK tomorrow at 6am","category":"anything_else","is_immediate":true}`: "driving",
		`{"title":"Walk my grandma home","category":"companionship","is_immediate":true}`:                                        "third_person",
	} {
		code, out := postTaskJSON(t, body)
		if code != http.StatusUnprocessableEntity || out["error"] != "out_of_scope" || out["category"] != cat || out["message"] != refusalReply(t, cat) {
			t.Errorf("%s: %d %v", body, code, out)
		}
	}
}

// Words the rules leave go to the model; its refusal is a 422 too, and its
// failure on sensitive words is a 503 — never a post (fail closed). Words
// with no sensitive word never ask it.
func TestCreateTaskScopeModel(t *testing.T) {
	m := &stubScope{verdict: "third_person"}
	withScopeModel(t, m)
	code, out := postTaskJSON(t, `{"title":"Walk","description":"my mom needs company on her walk, can someone go?","category":"companionship","is_immediate":true}`)
	if code != 422 || out["category"] != "third_person" || m.calls != 1 {
		t.Fatalf("model refusal: %d %v calls %d", code, out, m.calls)
	}
	m.err = errors.New("timeout")
	code, out = postTaskJSON(t, `{"title":"Walk","description":"my mom needs company on her walk, can someone go?","category":"companionship","is_immediate":true}`)
	if code != http.StatusServiceUnavailable || out["error"] != "scope_unchecked" || out["message"] != copyScopeUnchecked {
		t.Fatalf("model failure: %d %v", code, out)
	}
}

// Allowed posts go through and are stored (Postgres): public companionship,
// a home errand with access instructions, items with a friend, an item
// carried by car. A plain errand never asks the model, even a failing one.
func TestCreateTaskAllowsInScope(t *testing.T) {
	setupCreatedViaDB(t)
	m := &stubScope{verdict: "allow"}
	withScopeModel(t, m)
	for _, body := range []string{
		`{"title":"Walk in Washington Square Park","description":"keep me company for a walk in Washington Square Park","category":"companionship","estimated_minutes":60,"is_immediate":true}`,
		`{"title":"Laundry pickup","description":"the bag is with my doorman","category":"laundry","is_immediate":true}`,
		`{"title":"Pick up keys","description":"pick up the keys from my friend Sarah in Brooklyn","category":"delivery","is_immediate":true}`,
		`{"title":"Bring the boxes","description":"drive the boxes from my office to my apartment in your car","category":"delivery","is_immediate":true}`,
	} {
		if code, out := postTaskJSON(t, body); code != 201 {
			t.Errorf("%s: %d %v", body, code, out)
		}
	}
	m.err = errors.New("down")
	if code, out := postTaskJSON(t, `{"title":"Coffee run","category":"delivery","estimated_minutes":30,"is_immediate":true}`); code != 201 {
		t.Fatalf("plain errand with the model down: %d %v", code, out)
	}
}

// An edit that changes the words is checked; one that only changes the
// length is not (and a task posted before the rules stays editable).
func TestUpdateTaskChecksChangedWords(t *testing.T) {
	setupCreatedViaDB(t)
	m := &stubScope{verdict: "allow"}
	withScopeModel(t, m)
	code, out := postTaskJSON(t, `{"title":"Coffee run","description":"oat latte","category":"delivery","estimated_minutes":30,"is_immediate":true}`)
	if code != 201 {
		t.Fatalf("seed: %d %v", code, out)
	}
	id, _ := out["id"].(string)
	code, out = patchTaskJSON(t, id, `{"title":"Coffee run","description":"oat latte, then come over and keep me company at my place","category":"delivery","estimated_minutes":30,"is_immediate":true}`)
	if code != 422 || out["category"] != "private_company" || out["message"] != refusalReply(t, "private_company") {
		t.Fatalf("edit: %d %v", code, out)
	}
	var desc string
	if err := db.QueryRow(context.Background(), `select description from public.tasks where id=$1::uuid`, id).Scan(&desc); err != nil || desc != "oat latte" {
		t.Fatalf("stored %q %v: a refused edit must not land", desc, err)
	}
	m.err = errors.New("down") // unchanged words: never checked
	if code, out = patchTaskJSON(t, id, `{"title":"Coffee run","description":"oat latte","category":"delivery","estimated_minutes":45,"is_immediate":true}`); code != 200 {
		t.Fatalf("length-only edit: %d %v", code, out)
	}
}

// The chat's own post re-checks by rule only: the model already read the
// words in the chat's gate.
func TestChatPostScopeRulesOnly(t *testing.T) {
	m := &stubScope{err: errors.New("down")}
	withScopeModel(t, m)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/tasks", nil)
	c.Set(ctxScopeChecked, true)
	if refuseTaskScope(c, "test", "my mom needs company on her walk, can someone go?") || m.calls != 0 {
		t.Fatalf("chat post asked the model (calls %d) or failed closed: %s", m.calls, w.Body.String())
	}
	if !refuseTaskScope(c, "test", "be my date tonight") || w.Code != 422 {
		t.Fatal("a rule refusal still refuses a chat post")
	}
	// HO:RA's own fixed notes are not the person's words: a companionship
	// draft's description carries "…no alcohol…" and still posts.
	s, _ := llm.Playbooks()
	desc := "Walk in Washington Square Park.\n\n" + s.ByID("companionship").SupporterNotes[0] + "\n\n" + s.ScopeRules.HomeEntry.SupporterNote
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodPost, "/tasks", nil)
	c2.Set(ctxScopeChecked, true)
	if refuseTaskScope(c2, "test", taskScopeWords("Walk in Washington Square Park", desc)) {
		t.Fatalf("our own notes refused the post: %s", w2.Body.String())
	}
}

// The AI prefill: checked in front of the parser, which gets the body
// untouched; a refusal never reaches it.
func TestAIParseScopeGuard(t *testing.T) {
	withScopeModel(t, &stubScope{verdict: "allow"})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var reached string
	r.POST("/ai/parse-task", aiParseScopeGuard, func(c *gin.Context) {
		b, _ := io.ReadAll(c.Request.Body)
		reached = string(b)
		c.JSON(200, gin.H{"ok": true})
	})
	call := func(input string) (int, map[string]any) {
		body, _ := json.Marshal(map[string]string{"input": input})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/ai/parse-task", strings.NewReader(string(body))))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	reached = ""
	if code, out := call("can someone babysit tonight?"); code != 422 || out["category"] != "minors" || reached != "" {
		t.Fatalf("refused: %d %v reached %q", code, out, reached)
	}
	if code, _ := call("pick up my dry cleaning at 5th Ave Cleaners"); code != 200 || !strings.Contains(reached, "dry cleaning") {
		t.Fatalf("allowed: %d reached %q", code, reached)
	}
	withScopeModel(t, &stubScope{err: errors.New("down")})
	reached = ""
	if code, out := call("my mom needs company on her walk, can someone go?"); code != 503 || out["error"] != "scope_unchecked" || reached != "" {
		t.Fatalf("model down: %d %v", code, out)
	}
}

// Public companionship is quoted at the companionship base fee (the chat
// files a companionship playbook draft under that category).
func TestChatQuoteCompanionshipBase(t *testing.T) {
	b := &chatBooker{}
	comp, _, _ := b.Quote(context.Background(), llm.TaskDraft{Category: "companionship", DurationMinutes: 15}, 0)
	errand, _, _ := b.Quote(context.Background(), llm.TaskDraft{Category: "delivery", DurationMinutes: 15}, 0)
	if comp-errand != baseFeeCents("companionship")-baseFeeCents("delivery") || baseFeeCents("companionship") <= baseFeeCents("delivery") {
		t.Fatalf("companionship %d, errand %d", comp, errand)
	}
}
