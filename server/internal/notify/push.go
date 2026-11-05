package notify

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

// Expo Push Service endpoint. Unauthenticated sends need no key; an optional
// EXPO_ACCESS_TOKEN (bearer) enables Expo's enhanced security if we set one.
const expoPushURL = "https://exp.host/--/api/v2/push/send"

type expoMessage struct {
	To    string            `json:"to"`
	Title string            `json:"title"`
	Body  string            `json:"body"`
	Sound string            `json:"sound,omitempty"`
	Data  map[string]string `json:"data,omitempty"`
	// iOS: "time-sensitive" breaks through Focus (the app needs the
	// com.apple.developer.usernotifications.time-sensitive entitlement, or
	// iOS shows it as an ordinary notification). Empty = Expo's default.
	InterruptionLevel string `json:"interruptionLevel,omitempty"`
	// "high" asks for immediate delivery; empty = Expo's default.
	Priority string `json:"priority,omitempty"`
}

// PushOptions: delivery hints for SendPushData. The zero value is today's push.
type PushOptions struct {
	Extra             map[string]string // data keys beside task_id and type
	InterruptionLevel string
	Priority          string
}

// One ticket per message we sent, returned in the same order.
type expoTicket struct {
	Status  string `json:"status"`
	ID      string `json:"id"`
	Message string `json:"message"`
	Details struct {
		Error string `json:"error"`
	} `json:"details"`
}

type expoResponse struct {
	Data []expoTicket `json:"data"`
}

// SendPush delivers a task-event notification to every registered device of the
// recipient via the Expo Push Service. It is a third channel alongside the
// in-app row and email — never a replacement — and is entirely best-effort:
// every failure is logged and swallowed. Callers fire it in a goroutine with a
// background context so a slow or failing Expo call never blocks or fails the
// request path (S-32). On a DeviceNotRegistered ticket the dead token is
// deleted so the registry self-heals.
func SendPush(ctx context.Context, db *sql.DB, userID, taskID, notifType, title, body string) {
	SendPushData(ctx, db, userID, taskID, notifType, title, body, PushOptions{})
}

// SendPushData is SendPush with options: extra data keys on each message,
// such as a "url" the app opens on tap, and delivery hints (D-24). task_id and
// type are always set and are not overridden by Extra.
func SendPushData(ctx context.Context, db *sql.DB, userID, taskID, notifType, title, body string, opts PushOptions) {
	if db == nil || userID == "" {
		return
	}

	tokens, err := pushTokensForUser(ctx, db, userID)
	if err != nil {
		log.Printf("[push] token lookup failed user=%s type=%s err=%v", userID, notifType, err)
		return
	}
	if len(tokens) == 0 {
		return
	}

	msgs := make([]expoMessage, 0, len(tokens))
	for _, tok := range tokens {
		msgs = append(msgs, expoMessage{
			To:    tok,
			Title: title,
			Body:  body,
			// Foreground presentation is governed by the mobile notification
			// handler; this drives sound/banner when the app is backgrounded
			// or the device is locked.
			Sound:             "default",
			Data:              pushData(taskID, notifType, opts.Extra),
			InterruptionLevel: opts.InterruptionLevel,
			Priority:          opts.Priority,
		})
	}

	payload, err := json.Marshal(msgs)
	if err != nil {
		log.Printf("[push] marshal failed user=%s err=%v", userID, err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, expoPushURL, bytes.NewReader(payload))
	if err != nil {
		log.Printf("[push] request build failed user=%s err=%v", userID, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if accessToken := os.Getenv("EXPO_ACCESS_TOKEN"); accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}

	log.Printf("[push] sending type=%s user=%s devices=%d", notifType, userID, len(msgs))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[push] request error user=%s type=%s err=%v", userID, notifType, err)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Printf("[push] expo error user=%s type=%s status=%d body=%s", userID, notifType, resp.StatusCode, string(respBody))
		return
	}

	var parsed expoResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		log.Printf("[push] response parse failed user=%s err=%v", userID, err)
		return
	}

	// Tickets align index-wise with the messages we sent — walk them to reap
	// tokens Expo reports as no longer belonging to a device.
	for i, ticket := range parsed.Data {
		if ticket.Status == "ok" || i >= len(tokens) {
			continue
		}
		log.Printf("[push] ticket error user=%s token=%s msg=%q detail=%s",
			userID, maskToken(tokens[i]), ticket.Message, ticket.Details.Error)
		if ticket.Details.Error == "DeviceNotRegistered" {
			if _, derr := db.ExecContext(ctx,
				`DELETE FROM public.device_push_tokens WHERE expo_push_token = $1`, tokens[i]); derr != nil {
				log.Printf("[push] failed to delete dead token: %v", derr)
			}
		}
	}
	log.Printf("[push] sent type=%s user=%s tickets=%d", notifType, userID, len(parsed.Data))
}

func pushTokensForUser(ctx context.Context, db *sql.DB, userID string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT expo_push_token FROM public.device_push_tokens WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("query push tokens: %w", err)
	}
	defer rows.Close()

	var tokens []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("scan push token: %w", err)
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// maskToken keeps a device credential out of logs in full while leaving enough
// to correlate a specific ticket.
func maskToken(t string) string {
	if len(t) <= 12 {
		return "***"
	}
	return t[:12] + "..." + t[len(t)-4:]
}

func pushData(taskID, notifType string, extra map[string]string) map[string]string {
	d := map[string]string{}
	for k, v := range extra {
		d[k] = v
	}
	d["task_id"] = taskID
	d["type"] = notifType
	return d
}
