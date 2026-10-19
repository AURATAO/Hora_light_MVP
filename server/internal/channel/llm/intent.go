package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Intent is what a free-text message from an idle conversation is for. The
// router acts on it only to choose a reply; it never moves money.
type Intent string

const (
	IntentNewTask  Intent = "new_task" // describes something they want done
	IntentGreeting Intent = "greeting" // hi / hello / what is this
	IntentQuestion Intent = "question" // pricing, area, how it works
	IntentOther    Intent = "other"    // anything else
)

// Classifier labels a message's intent.
type Classifier interface {
	Classify(ctx context.Context, text string) (Intent, error)
}

const classifyPrompt = `You label one text message sent to HO:RA, an on-demand errand service in New York City where real people run tasks (deliveries, groceries, laundry, waiting in line, companionship, odd jobs).

Return {"intent": X} where X is exactly one of:
- "new_task": the person describes something they want done, even vaguely or partly ("can someone grab my dry cleaning", "need help moving a couch tomorrow", "laundry pickup 3pm").
- "greeting": a hello or opener with no request ("hi", "hey there", "is this HO:RA?").
- "question": a question about the service itself (price, hours, area, how it works, who the supporters are).
- "other": anything else.

When in doubt between new_task and anything else, choose new_task.`

func classifySchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"intent": map[string]any{"type": "string", "enum": []string{
				string(IntentNewTask), string(IntentGreeting), string(IntentQuestion), string(IntentOther),
			}},
		},
		"required":             []string{"intent"},
		"additionalProperties": false,
	}
}

// Classify asks the model, and falls back to a local rule if the model is
// unavailable or answers out of schema — a person should never get silence
// because a provider hiccuped.
func (o *OpenAICompat) Classify(ctx context.Context, text string) (Intent, error) {
	out, _, err := o.completeJSON(ctx, classifyPrompt, text, "intent", classifySchema(), 64)
	if err != nil {
		return HeuristicIntent(text), fmt.Errorf("classify: %w", err)
	}
	var r struct {
		Intent Intent `json:"intent"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(out)), &r); err != nil {
		return HeuristicIntent(text), fmt.Errorf("classify decode: %w", err)
	}
	switch r.Intent {
	case IntentNewTask, IntentGreeting, IntentQuestion, IntentOther:
		return r.Intent, nil
	}
	return HeuristicIntent(text), fmt.Errorf("classify: unknown intent %q", r.Intent)
}

var greetingRE = regexp.MustCompile(`(?i)^\s*(hi+|hey+|hello+|yo|hiya|good (morning|afternoon|evening)|sup|what'?s up|start|help|你好|嗨|hola|ciao)[\s!.?]*$`)

// HeuristicIntent is the no-model fallback: short greetings are greetings,
// everything else is treated as a request (the cheaper mistake — the person
// sees a draft they can decline).
func HeuristicIntent(text string) Intent {
	t := strings.TrimSpace(text)
	if t == "" || greetingRE.MatchString(t) {
		return IntentGreeting
	}
	return IntentNewTask
}
