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
	out, _, err := o.completeJSON(ctx, classifyPrompt, text, "intent", classifySchema(), 256)
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

// ── In context (D-17) ──────────────────────────────────────────────────────

// IntentToSupporter: the message is for the supporter on the job — an
// answer, an instruction, a preference, small talk. Only produced when the
// classifier is given a context with at least one active task.
const IntentToSupporter Intent = "to_supporter"

// IntentCorrection: the message changes the thing HO:RA is asking about
// (a time, a place, a detail of the pending booking) — only meaningful
// when a question of ours is open.
const IntentCorrection Intent = "correction"

// IntentAnswer: the message answers HO:RA's open question in words the
// rule does not match ("yes, approve it", "ok go ahead with the $10") —
// HO:RA re-asks for a plain YES/NO; it is not relayed.
const IntentAnswer Intent = "answer"

// Context is what the classifier knows about the thread (D-17 P2): the
// errands a supporter is working on, what the supporter last said, and
// which question of ours is open. Task titles and the supporter's line are
// shown to the model; nothing identifying the person is.
type Context struct {
	ActiveTasks []ContextTask
	// LastSupporterLine is the supporter's most recent relayed message
	// (within the recency window), "" when none.
	LastSupporterLine string
	// OpenQuestion names the question of ours the thread is waiting on:
	// "" | "booking" (a draft awaits YES) | "budget" | "approval" | "name".
	OpenQuestion string
}

type ContextTask struct {
	Title         string
	SupporterName string
}

// ContextClassifier labels a message given the thread's context.
type ContextClassifier interface {
	ClassifyInContext(ctx context.Context, text string, c Context) (Intent, error)
}

const classifyInContextPrompt = `You label one text message a customer sent to HO:RA, an on-demand errand service in New York City where real people ("supporters") run tasks. The customer has at least one errand in progress with a supporter, and talks to the supporter through this same thread.

Return {"intent": X} where X is exactly one of:
- "to_supporter": the message is for the supporter working on the errand(s) listed — an answer to what the supporter asked, an instruction or preference about that errand ("everything bagel", "leave it at the door", "no onions", "take your time", "where are you?"), a thank-you, or small talk.
- "new_task": the customer clearly describes a SEPARATE errand — a different place, time or purpose from the listed ones, or they say "new task", "another", "also", "can you also", "next week".
- "greeting": a hello with no content.
- "question": a question about HO:RA itself (pricing, hours, how it works) — not about the errand in progress.
- "correction": ONLY when HO:RA has an open question about a pending NEW booking (stated below) and the message changes that booking — a different time, place, duration or detail for it ("make it 4pm", "actually 2 bags", "the other address"). Never use this when no booking question is open.
- "answer": ONLY when HO:RA has an open question (stated below) and the message is the customer's answer to THAT question in words — approving or declining the supporter's request ("yes, approve it", "ok fine, go ahead with the extra"), confirming the booking ("book it please"), or a budget ("about twenty dollars"). Never use this when no question is open.
- "other": none of the above.

Default to "to_supporter": with a supporter on the job, short replies, preferences, details and answers are almost always for them. Choose "new_task" only when the message cannot be about the listed errand(s).`

func classifyInContextSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"intent": map[string]any{"type": "string", "enum": []string{
				string(IntentToSupporter), string(IntentNewTask), string(IntentCorrection), string(IntentAnswer), string(IntentGreeting), string(IntentQuestion), string(IntentOther),
			}},
		},
		"required":             []string{"intent"},
		"additionalProperties": false,
	}
}

// ClassifyInContext asks the model with the thread's context. On any
// failure it returns IntentToSupporter with the error (D-17 P7: fail
// toward the human), never a draft.
func (o *OpenAICompat) ClassifyInContext(ctx context.Context, text string, c Context) (Intent, error) {
	var b strings.Builder
	b.WriteString("Errands in progress:\n")
	for i, t := range c.ActiveTasks {
		fmt.Fprintf(&b, "%d. %q with supporter %s\n", i+1, t.Title, firstNonEmptyStr(t.SupporterName, "(unnamed)"))
	}
	if c.LastSupporterLine != "" {
		fmt.Fprintf(&b, "The supporter's last message to the customer: %q\n", c.LastSupporterLine)
	}
	if c.OpenQuestion != "" {
		fmt.Fprintf(&b, "HO:RA is currently waiting on the customer to answer a %s question about a NEW booking (a bare yes/no/amount would answer it; this message is not that).\n", c.OpenQuestion)
	}
	fmt.Fprintf(&b, "\nCustomer's message: %q", text)
	// 256 tokens: the answer is ~10, the rest is headroom for a model that
	// thinks before the JSON (a 64-token cap truncated GLM in staging).
	out, _, err := o.completeJSON(ctx, classifyInContextPrompt, b.String(), "intent", classifyInContextSchema(), 256)
	if err != nil {
		return IntentToSupporter, fmt.Errorf("classify in context: %w", err)
	}
	var r struct {
		Intent Intent `json:"intent"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(out)), &r); err != nil {
		return IntentToSupporter, fmt.Errorf("classify in context decode: %w", err)
	}
	switch r.Intent {
	case IntentCorrection, IntentAnswer:
		if c.OpenQuestion == "" {
			return IntentToSupporter, nil // nothing open: for the supporter
		}
		return r.Intent, nil
	case IntentToSupporter, IntentNewTask, IntentGreeting, IntentQuestion, IntentOther:
		return r.Intent, nil
	}
	return IntentToSupporter, fmt.Errorf("classify in context: unknown intent %q", r.Intent)
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
