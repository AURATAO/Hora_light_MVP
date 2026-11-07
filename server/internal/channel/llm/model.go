package llm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"hora-auth/internal/tracing"
)

// Provider is one model behind one API: a single schema-constrained
// completion. *OpenAICompat (Baseten) and *Anthropic implement it; every
// channel step (Propose, ParseTask, …) is written once over it, so the
// prompts, schemas and decoding are the same whichever model answers.
type Provider interface {
	// CompleteJSON returns the model's raw JSON text. Errors carry a status
	// or a fixed text, never model or message text (S-12).
	CompleteJSON(ctx context.Context, system, user, schemaName string, schema map[string]any, maxTokens int) (string, Usage, error)
	// Name is "provider:model" for logs and traces. Never a key.
	Name() string
}

// Spec is a step's model as configured: "anthropic:claude-sonnet-5-5" or
// "baseten:zai-org/GLM-5.3-Flash".
type Spec struct {
	Provider string // "anthropic" | "baseten"
	Model    string
}

func (s Spec) String() string { return s.Provider + ":" + s.Model }

// ParseSpec reads "provider:model". The model may itself contain ":" or
// "/" (Baseten slugs do); only the first ":" separates.
func ParseSpec(s string) (Spec, error) {
	p, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	p, m = strings.ToLower(strings.TrimSpace(p)), strings.TrimSpace(m)
	if !ok || m == "" {
		return Spec{}, fmt.Errorf("model spec %q: want provider:model", s)
	}
	switch p {
	case "anthropic", "baseten":
		return Spec{Provider: p, Model: m}, nil
	}
	return Spec{}, fmt.Errorf("model spec %q: unknown provider %q (anthropic or baseten)", s, p)
}

// Model runs the channel's steps on one provider, with an optional
// fallback: if the primary errors or times out, that one call is made again
// on Fallback, so a person's message is never lost to a provider outage.
type Model struct {
	Primary  Provider
	Fallback Provider // nil: none
	// FallbackReserve is how much of the caller's deadline the primary may
	// not use, so the fallback still has time to answer (default 15s). The
	// router gives a whole message 60s, and the providers' own timeout is
	// 45s: without a reserve a hung primary would leave the fallback nothing.
	FallbackReserve time.Duration
}

// Name is the primary's "provider:model", plus the fallback's.
func (m *Model) Name() string {
	if m.Fallback == nil {
		return m.Primary.Name()
	}
	return m.Primary.Name() + " (fallback " + m.Fallback.Name() + ")"
}

func (m *Model) provider() Provider {
	if m.Fallback == nil {
		return m.Primary
	}
	return fallbackProvider{primary: m.Primary, backup: m.Fallback, reserve: m.FallbackReserve}
}

func (m *Model) ParseTask(ctx context.Context, systemPrompt, text string) (TaskDraft, Usage, error) {
	return parseTask(ctx, m.provider(), systemPrompt, text)
}

func (m *Model) Propose(ctx context.Context, text string, c AgentContext) (Proposal, error) {
	return propose(ctx, m.provider(), text, c)
}

func (m *Model) ProposeRaw(ctx context.Context, text string, c AgentContext) (string, Usage, error) {
	return proposeRaw(ctx, m.provider(), text, c)
}

func (m *Model) Classify(ctx context.Context, text string) (Intent, error) {
	return classify(ctx, m.provider(), text)
}

func (m *Model) ClassifyDetail(ctx context.Context, text string) (Classification, error) {
	return classifyDetail(ctx, m.provider(), text)
}

func (m *Model) ReadsAsQuestion(ctx context.Context, asked, reply string) (bool, error) {
	return readsAsQuestion(ctx, m.provider(), asked, reply)
}

func (m *Model) ClassifyInContext(ctx context.Context, text string, c Context) (Intent, error) {
	return classifyInContext(ctx, m.provider(), text, c)
}

func (m *Model) SplitErrands(ctx context.Context, text string) ([]string, Usage, error) {
	return splitErrands(ctx, m.provider(), text)
}

func (m *Model) NeedsPurchase(ctx context.Context, text string) (bool, error) {
	return needsPurchase(ctx, m.provider(), text)
}

// fallbackProvider retries one failed call on the backup.
type fallbackProvider struct {
	primary, backup Provider
	reserve         time.Duration
}

func (f fallbackProvider) Name() string { return f.primary.Name() }

const defaultFallbackReserve = 15 * time.Second

func (f fallbackProvider) CompleteJSON(ctx context.Context, system, user, schemaName string, schema map[string]any, maxTokens int) (string, Usage, error) {
	pctx := ctx
	reserve := f.reserve
	if reserve == 0 {
		reserve = defaultFallbackReserve
	}
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) > 2*reserve {
		var cancel context.CancelFunc
		pctx, cancel = context.WithDeadline(ctx, dl.Add(-reserve))
		defer cancel()
	}
	out, u, err := f.primary.CompleteJSON(pctx, system, user, schemaName, schema, maxTokens)
	if err == nil {
		return out, u, nil
	}
	if ctx.Err() != nil {
		// The caller gave up (the message's own deadline): nothing to retry into.
		return out, u, err
	}
	// The error is a status or a fixed text by the Provider contract (S-12).
	log.Printf("[llm] %s failed on %s after %s, retrying on %s: %v", schemaName, f.primary.Name(), u.Latency.Round(time.Millisecond), f.backup.Name(), err)
	bctx, sp := tracing.StartSpan(ctx, "llm.fallback", tracing.KindChain)
	sp.Attr(
		attribute.Bool("hora.llm.fallback", true),
		attribute.String("hora.llm.fallback_from", f.primary.Name()),
		attribute.String("hora.llm.fallback_to", f.backup.Name()),
		attribute.String("hora.llm.fallback_reason", fallbackReason(err)),
	)
	out, bu, berr := f.backup.CompleteJSON(bctx, system, user, schemaName, schema, maxTokens)
	sp.Fail(berr)
	sp.End()
	bu.FallbackFrom = f.primary.Name()
	if berr != nil {
		return out, bu, fmt.Errorf("%w; fallback %s: %w", err, f.backup.Name(), berr)
	}
	return out, bu, nil
}

// fallbackReason is a label for the trace: timeout, status, or other.
func fallbackReason(err error) string {
	var se *StatusError
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTimeout):
		return "timeout"
	case errors.As(err, &se):
		return fmt.Sprintf("status_%d", se.Status)
	}
	return "error"
}

// ErrTimeout: the provider did not answer in time.
var ErrTimeout = errors.New("timeout")

// StatusError is a provider's non-2xx answer: the status only, never the
// body, which can echo the prompt (S-12).
type StatusError struct {
	Provider string
	Status   int
}

func (e *StatusError) Error() string { return fmt.Sprintf("%s: status %d", e.Provider, e.Status) }
