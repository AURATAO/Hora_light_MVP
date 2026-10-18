package channel

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"time"
)

// Router decides what an inbound message means and acts on it (PLAN §6).
// M0 ships LogRouter only; the real router lands in M1–M3.
type Router interface {
	HandleInbound(ctx context.Context, in Inbound) error
}

// LogRouter records that a message arrived, without its content or number.
type LogRouter struct{}

func (LogRouter) HandleInbound(_ context.Context, in Inbound) error {
	log.Printf("[channel] inbound channel=%s id=%s from=%s service=%s reply_to=%t len=%d",
		in.Channel, in.ProviderMessageID, Redact(in.From), in.Service, in.ReplyToProviderID != "", len(in.Text))
	return nil
}

const maxWebhookBody = 1 << 20 // 1 MiB; both providers send a few KB

// WebhookHandler is the POST endpoint for one adapter. Order matters:
//
//  1. read the raw body (signatures are over the exact bytes)
//  2. verify → 401 and stop; nothing unauthenticated is parsed or stored
//  3. parse → messages a person sent
//  4. record each one; a duplicate is ACKed and dropped here
//  5. answer 200, then route new messages off the request path
//
// A store failure answers 500 so the provider retries; the retry is safe
// because step 4 dedupes. Routing runs after the response because it calls an
// LLM (seconds) and providers time out; a crash between 4 and 5 leaves the row
// with processed_at NULL for a recovery sweep.
type WebhookHandler struct {
	Adapter Adapter
	Store   Store
	Router  Router
	// RouteTimeout bounds one message's routing. Default 60s.
	RouteTimeout time.Duration
	// async runs routing; tests replace it to run inline.
	async func(func())
}

func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ch := h.Adapter.Channel()
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := h.Adapter.VerifyRequest(r, body); err != nil {
		if errors.Is(err, ErrNotConfigured) {
			log.Printf("[channel] %s webhook rejected: adapter not configured", ch)
		} else {
			log.Printf("[channel] %s webhook rejected: verification failed", ch)
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	msgs, err := h.Adapter.ParseInbound(body)
	if err != nil {
		// Authenticated but unreadable: a payload shape we don't know. 200 so
		// the provider doesn't retry the same bytes; the log has no PII.
		log.Printf("[channel] %s webhook parse error: %v", ch, err)
		w.WriteHeader(http.StatusOK)
		return
	}
	var fresh []Inbound
	for _, m := range msgs {
		isNew, err := h.Store.RecordInbound(r.Context(), m)
		if err != nil {
			log.Printf("[channel] %s record failed id=%s: %v", ch, m.ProviderMessageID, err)
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		if isNew {
			fresh = append(fresh, m)
		}
	}
	w.WriteHeader(http.StatusOK)

	run := h.async
	if run == nil {
		run = func(f func()) { go f() }
	}
	for _, m := range fresh {
		m := m
		run(func() { h.route(m) })
	}
}

func (h *WebhookHandler) route(m Inbound) {
	timeout := h.RouteTimeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := h.Router.HandleInbound(ctx, m); err != nil {
		log.Printf("[channel] %s route failed id=%s: %v", m.Channel, m.ProviderMessageID, err)
		return // processed_at stays NULL → recovery sweep
	}
	if err := h.Store.MarkProcessed(ctx, m.Channel, m.ProviderMessageID); err != nil {
		log.Printf("[channel] %s mark processed failed id=%s: %v", m.Channel, m.ProviderMessageID, err)
	}
}
