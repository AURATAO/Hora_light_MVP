package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Server-side TalkJS conversation membership.
//
// Nothing on the backend touched TalkJS conversations before this file, and
// that is not an oversight — it is the whole reason it now has to. Membership
// is established lazily by whichever client opens the chat: web
// (app/src/components/TaskChatBox.jsx) and mobile
// (mobile/src/app/task/[id]/chat.tsx) both call getOrCreateConversation
// ("task_<taskId>") and setParticipant for themselves and for the counterpart
// they read off the task. acceptTask does not participate at all.
//
// That design is self-correcting for assignment but not for un-assignment. After
// a reassignment the new supporter is added the moment either party opens the
// chat, because both clients derive the counterpart from tasks.assigned_to,
// which the swap has already updated. The *old* supporter, though, was written
// into the conversation when they opened it and no client ever removes a
// participant — so without this file they would keep reading (and sending) the
// requester's messages on a task that is no longer theirs.
//
// Hence: additions stay the clients' job, removals become ours.
//
// Everything here is best-effort. A TalkJS outage must not fail or roll back a
// reassignment that is already committed in Postgres; the cost of a failure is
// a stale participant, which is recoverable by hand, not a broken swap.

const (
	talkjsAPIBase  = "https://api.talkjs.com/v1"
	talkjsHTTPTime = 8 * time.Second
)

// talkjsConversationID mirrors the id both clients build. Keep in step.
func talkjsConversationID(taskID string) string {
	return "task_" + taskID
}

// talkjsConfigured reports whether the REST credentials are present. The app id
// is new to the backend (the signature handler only ever needed the secret), so
// a deploy that has not had TALKJS_APP_ID added yet degrades to "additions still
// work client-side, removals are logged and skipped" rather than erroring.
func talkjsConfigured() (appID, secret string, ok bool) {
	appID = os.Getenv("TALKJS_APP_ID")
	secret = os.Getenv("TALKJS_SECRET_KEY")
	return appID, secret, appID != "" && secret != ""
}

func talkjsRequest(ctx context.Context, method, path string, body any) (int, []byte, error) {
	appID, secret, ok := talkjsConfigured()
	if !ok {
		return 0, nil, fmt.Errorf("talkjs not configured")
	}

	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		payload = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, talkjsAPIBase+"/"+appID+path, payload)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: talkjsHTTPTime}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, nil
}

// talkjsConversationExists distinguishes "the chat was never opened" from a
// real failure. A conversation that does not exist needs no repair: whoever
// opens it next creates it from the task's current assignment, which is already
// the new supporter.
func talkjsConversationExists(ctx context.Context, taskID string) (bool, error) {
	status, _, err := talkjsRequest(ctx, http.MethodGet,
		"/conversations/"+url.PathEscape(talkjsConversationID(taskID)), nil)
	if err != nil {
		return false, err
	}
	switch {
	case status == http.StatusOK:
		return true, nil
	case status == http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("talkjs GET conversation returned %d", status)
	}
}

// talkjsSetParticipant adds (or re-adds) a user to the conversation. TalkJS
// user ids are emails throughout this codebase — the id both clients set on
// Talk.User and the value talkjsSignatureHandler signs.
func talkjsSetParticipant(ctx context.Context, taskID, userEmail string) error {
	status, raw, err := talkjsRequest(ctx, http.MethodPut,
		"/conversations/"+url.PathEscape(talkjsConversationID(taskID))+
			"/participants/"+url.PathEscape(userEmail),
		map[string]any{"access": "ReadWrite", "notify": true})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("talkjs PUT participant returned %d: %s", status, truncate(string(raw), 200))
	}
	return nil
}

// talkjsRemoveParticipant revokes access. A 404 is success: the user was not in
// the conversation, which is the state we wanted.
func talkjsRemoveParticipant(ctx context.Context, taskID, userEmail string) error {
	status, raw, err := talkjsRequest(ctx, http.MethodDelete,
		"/conversations/"+url.PathEscape(talkjsConversationID(taskID))+
			"/participants/"+url.PathEscape(userEmail), nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNotFound {
		return fmt.Errorf("talkjs DELETE participant returned %d: %s", status, truncate(string(raw), 200))
	}
	return nil
}

// talkjsHandoverChat moves the supporter seat in a task's conversation.
//
// Runs after the swap is committed, on a background context (the request's own
// context is cancelled the moment the handler returns), and never reports
// failure upward — see the file comment. Returns a short status string for the
// audit log so an operator can tell afterwards what happened.
func talkjsHandoverChat(taskID, oldEmail, newEmail string) string {
	if _, _, ok := talkjsConfigured(); !ok {
		log.Printf("[talkjs][reassign] task=%s skipped — TALKJS_APP_ID/TALKJS_SECRET_KEY not set", taskID)
		return "skipped_unconfigured"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*talkjsHTTPTime)
	defer cancel()

	exists, err := talkjsConversationExists(ctx, taskID)
	if err != nil {
		log.Printf("[talkjs][reassign] task=%s lookup failed: %v", taskID, err)
		return "lookup_failed"
	}
	if !exists {
		// Nobody has opened this chat. The clients will build it with the new
		// supporter already in it.
		log.Printf("[talkjs][reassign] task=%s no conversation yet — nothing to move", taskID)
		return "no_conversation"
	}

	// New supporter first, so the conversation is never left with the requester
	// alone if the second call fails.
	if newEmail != "" {
		if err := talkjsSetParticipant(ctx, taskID, newEmail); err != nil {
			log.Printf("[talkjs][reassign] task=%s add %s failed: %v", taskID, newEmail, err)
			return "add_failed"
		}
	}
	if oldEmail != "" && oldEmail != newEmail {
		if err := talkjsRemoveParticipant(ctx, taskID, oldEmail); err != nil {
			log.Printf("[talkjs][reassign] task=%s remove %s failed: %v", taskID, oldEmail, err)
			return "remove_failed"
		}
	}

	log.Printf("[talkjs][reassign] task=%s moved chat seat %q → %q", taskID, oldEmail, newEmail)
	return "ok"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
