package channel

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// InboundRouter decides what an inbound message means and acts on it
// (PLAN §6). *Router is the real one; LogRouter only records arrival.
type InboundRouter interface {
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
	Router  InboundRouter
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

	for _, m := range fresh {
		m := m
		if h.async != nil {
			h.async(func() { h.route(m) })
			continue
		}
		// One person's messages are routed one at a time, in arrival
		// order (production 2026-10-30: "15 mins" and "No extra" sent
		// five seconds apart were routed in parallel; the slower re-draft
		// saved over the opt-out and the summary briefly offered the
		// auto-extension again). Different people still run in parallel.
		inboundQueue.do(string(m.Channel)+"|"+m.From, func() { h.route(m) })
	}
}

// inboundQueue serializes routing per sender within this process. One
// Render instance serves the channel; during a deploy's overlap two may
// briefly run, which this does not cover (declared residual).
var inboundQueue = &keyedSerial{queues: map[string][]func(){}}

// keyedSerial runs the functions given for one key one after another, in
// the order given, on one goroutine per busy key.
type keyedSerial struct {
	mu     sync.Mutex
	queues map[string][]func()
}

func (k *keyedSerial) do(key string, f func()) {
	k.mu.Lock()
	if q, busy := k.queues[key]; busy {
		k.queues[key] = append(q, f)
		k.mu.Unlock()
		return
	}
	k.queues[key] = []func(){}
	k.mu.Unlock()
	go func() {
		for next := f; next != nil; {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[channel] route panic: %v", r)
					}
				}()
				next()
			}()
			k.mu.Lock()
			if q := k.queues[key]; len(q) > 0 {
				next, k.queues[key] = q[0], q[1:]
			} else {
				delete(k.queues, key)
				next = nil
			}
			k.mu.Unlock()
		}
	}()
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
