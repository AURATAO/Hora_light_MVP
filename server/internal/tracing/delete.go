package tracing

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Deleter removes a conversation's traces from Phoenix: DELETE
// /v1/sessions/{session_identifier}, which cascades through the session's
// traces and spans (Phoenix 13.13.0+). The identifier is our own session.id
// string — the conversation (channel identity) id.
type Deleter struct {
	Endpoint string // Phoenix base URL, as Config.Endpoint
	APIKey   string
	HTTP     *http.Client
}

// DeleterFromEnv is the deleter for the configured Phoenix; ok is false when
// tracing is not configured.
func DeleterFromEnv() (*Deleter, bool) {
	c, ok := ConfigFromEnv()
	if !ok {
		return nil, false
	}
	return &Deleter{Endpoint: c.Endpoint, APIKey: c.APIKey}, true
}

// StatusError is Phoenix's non-2xx answer. Its text is the status only: the
// body is never read into an error (S-12).
type StatusError struct{ Status int }

func (e *StatusError) Error() string { return fmt.Sprintf("phoenix: status %d", e.Status) }

// DeleteSession deletes every trace of the session. A session Phoenix does
// not have (never traced, or already deleted) is success: 404 is the answer
// both times, and deleting is idempotent.
func (d *Deleter) DeleteSession(ctx context.Context, sessionID string) error {
	if d == nil || d.Endpoint == "" || d.APIKey == "" {
		return fmt.Errorf("phoenix: not configured")
	}
	if sessionID == "" {
		return fmt.Errorf("phoenix: empty session id")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		d.Endpoint+"/v1/sessions/"+url.PathEscape(sessionID), nil)
	if err != nil {
		return fmt.Errorf("phoenix: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+d.APIKey)
	client := d.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		// A transport error names the URL, which holds no user data.
		return fmt.Errorf("phoenix: delete session: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) // reuse the connection
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode <= 299, resp.StatusCode == http.StatusNotFound:
		return nil
	}
	return &StatusError{Status: resp.StatusCode}
}
