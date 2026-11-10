package main

// Scope rules on every task creation path (D-26, 2026-11-10): the same check
// the text channel runs (internal/channel/llm/scope.go) guards POST /tasks
// (web and app), an edit that changes a task's title or description, and
// the AI prefill (POST /ai/parse-task — in front of the frozen parser,
// ai_task_parser.go, never inside it).
//
// Rules first; the small model (LLM_SMALL_MODEL) for words the rules leave.
// Fail closed like the chat: sensitive words the model could not read are
// not posted (503 scope_unchecked, try again). A refusal is 422 with the
// category's own reply as the message, which the web form shows.
//
// A chat booking was checked word by word as the person typed (with the
// model), so its post re-checks by rule only: no second model call after
// the YES.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"hora-auth/internal/channel/llm"
)

// taskScopeModel reads what the rules leave; main sets it from
// LLM_SMALL_MODEL. Nil (tests): the rules alone.
var taskScopeModel llm.ScopeChecker

// taskScopeTimeout bounds the model's reading on a post.
var taskScopeTimeout = 8 * time.Second

// ctxScopeChecked: set by the chat booker — the words were checked by the
// chat's gate already, with the model.
const ctxScopeChecked = "scope_checked_by_chat"

const copyScopeUnchecked = "We couldn't check this request just now. Please try again in a moment, or contact info@my-hora.com."

// taskScopeWords: what the check reads — the requester's own words. ". "
// between them, so a title and the start of its description are not read as
// one phrase ("Walk" + "my mom needs…" is not "walk my mom").
func taskScopeWords(title, description string) string {
	t, d := strings.TrimSpace(title), strings.TrimSpace(description)
	if t == "" || d == "" {
		return t + d
	}
	return t + ". " + d
}

// refuseTaskScope checks the words and, when they are refused (or could
// not be checked), answers the request and returns true. route names the
// path in the log; the words never go to the log.
func refuseTaskScope(c *gin.Context, route, words string) bool {
	s, err := llm.Playbooks()
	if err != nil || s == nil {
		log.Printf("[scope] %s: scope rules unavailable: %v", route, err)
		return false
	}
	sr := &s.ScopeRules
	words = s.StripFixedNotes(words)
	var model llm.ScopeChecker = taskScopeModel
	if c.GetBool(ctxScopeChecked) {
		model = nil // the chat's gate asked the model; rules only here
	}
	var d llm.ScopeDecision
	if model == nil {
		d = sr.Rule(words)
	} else {
		ctx, cancel := context.WithTimeout(c.Request.Context(), taskScopeTimeout)
		d, err = sr.Decide(ctx, model, words)
		cancel()
		if err != nil {
			log.Printf("[scope] %s: check FAILED kind=%s: not posted", route, llm.ErrorKind(err))
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "scope_unchecked", "message": copyScopeUnchecked})
			return true
		}
	}
	if d.Allowed() {
		return false
	}
	log.Printf("[scope] %s: refused category=%s by=%s alert=%t", route, d.Refusal.ID, d.By, d.Refusal.Alert)
	c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "out_of_scope", "category": d.Refusal.ID, "message": d.Refusal.Reply})
	return true
}

// aiParseScopeGuard runs in front of parseTaskWithAI: it reads the input,
// checks it, and puts the body back for the parser untouched.
func aiParseScopeGuard(c *gin.Context) {
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	var body struct {
		Input string `json:"input"`
	}
	if json.Unmarshal(raw, &body) == nil && strings.TrimSpace(body.Input) != "" {
		if refuseTaskScope(c, "ai-parse", body.Input) {
			c.Abort()
			return
		}
	}
	c.Next()
}
