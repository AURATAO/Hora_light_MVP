package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"hora-auth/internal/tracing"
)

// Intent is what a free-text message from an idle conversation is for. The
// router acts on it only to choose a reply; it never moves money.
type Intent string

const (
	IntentNewTask  Intent = "new_task" // describes something they want done
	IntentGreeting Intent = "greeting" // hi / hello / what is this
	IntentQuestion Intent = "question" // pricing, area, how it works
	IntentOther    Intent = "other"    // anything else
	// IntentFind: the person asks HO:RA to find, recommend, compare or
	// check facts about places ("help me find a pizzeria around SoHo", "is
	// there a dry cleaner open until 9?") — place discovery answers it.
	// Not an errand: nobody is sent anywhere until they pick one.
	IntentFind Intent = "find_place"
)

// Classifier labels a message's intent.
type Classifier interface {
	Classify(ctx context.Context, text string) (Intent, error)
}

const classifyPrompt = `You label one text message sent to HO:RA, an on-demand errand service in New York City where real people ("supporters") run tasks (deliveries, groceries, laundry, waiting in line, companionship, odd jobs). HO:RA can also find places in NYC from Google Maps.

Return {"intent": X, "playbook": Y}. X is exactly one of:
- "find_place": the person asks HO:RA to FIND, RECOMMEND, COMPARE or CHECK FACTS ABOUT places — "help me find a pizzeria around 250 Mercer St", "find pizzeria in SoHo", "know any good dumpling spots?", "what's near Union Square for lunch?", "looking for a tailor in Williamsburg", "is there a dry cleaner open until 9pm?", "which pharmacy near me is open now?", "any recommendations?". Opening hours, ratings, what is near, what is open: Google can answer these, so they are find_place, not errands. A neighbourhood ("SoHo", "around Union Square", "in Williamsburg") is a fine place to search.
- "new_task": the person wants a SUPPORTER TO DO something — "pick up my dry cleaning", "buy and bring me oat milk", "deliver this to my office", "wait in line at Levain", "get me a slice from Joe's", "go check if Trader Joe's still has oat milk" (stock and lines are what Google cannot know: a supporter goes), "need help moving a couch tomorrow", "laundry pickup 3pm".
- "greeting": a hello or opener with no request ("hi", "hey there", "is this HO:RA?").
- "question": a question about the service itself (price, hours, area, how it works, who the supporters are).
- "other": anything else.

"Can you help me find…", "can you check if … is open" ask HO:RA to look something up: find_place. A request names an action for a person to take (pick up, buy, bring, deliver, drop off, wait, get … from …): new_task. When both are there ("find a pizzeria near me and bring me a slice"), new_task.

Y is the errand type the message is about, for new_task and find_place (else ""), one of: PLAYBOOK_LIST. "" when none fits.`

// ClassifyPrompt is the system prompt with the playbook list filled in.
var ClassifyPrompt = strings.Replace(classifyPrompt, "PLAYBOOK_LIST", playbookList(), 1)

func classifySchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"intent": map[string]any{"type": "string", "enum": []string{
				string(IntentNewTask), string(IntentFind), string(IntentGreeting), string(IntentQuestion), string(IntentOther),
			}},
			"playbook": map[string]any{"type": "string", "enum": playbookEnum()},
		},
		"required":             []string{"intent", "playbook"},
		"additionalProperties": false,
	}
}

// Classification is a label plus the errand type the model recognised
// (an id from playbooks.json, "" none).
type Classification struct {
	Intent   Intent
	Playbook string
}

// DetailClassifier also returns the errand type, from the same call.
type DetailClassifier interface {
	ClassifyDetail(ctx context.Context, text string) (Classification, error)
}

// Classify asks the model, and falls back to a local rule if the model is
// unavailable or answers out of schema — a person should never get silence
// because a provider hiccuped.
func (o *OpenAICompat) Classify(ctx context.Context, text string) (Intent, error) {
	c, err := classifyDetail(ctx, o, text)
	return c.Intent, err
}

func classify(ctx context.Context, m Provider, text string) (Intent, error) {
	c, err := classifyDetail(ctx, m, text)
	return c.Intent, err
}

func classifyDetail(ctx context.Context, m Provider, text string) (got Classification, err error) {
	ctx, sp := tracing.StartSpan(ctx, "llm.Classify", tracing.KindChain)
	defer func() {
		sp.Input(text)
		sp.OutputValue(map[string]any{"intent": got.Intent, "playbook": got.Playbook, "model": m.Name(), "fallback": err != nil})
		sp.Fail(err)
		sp.End()
	}()
	out, _, err := m.CompleteJSON(ctx, ClassifyPrompt, text, "intent", classifySchema(), 256)
	if err != nil {
		return Classification{Intent: HeuristicIntent(text)}, fmt.Errorf("classify: %w", err)
	}
	var r struct {
		Intent   Intent `json:"intent"`
		Playbook string `json:"playbook"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(out)), &r); err != nil {
		return Classification{Intent: HeuristicIntent(text)}, fmt.Errorf("classify decode: %w", err)
	}
	if s, _ := Playbooks(); s.ByID(r.Playbook) == nil {
		r.Playbook = "" // only a listed type
	}
	switch r.Intent {
	case IntentNewTask, IntentFind, IntentGreeting, IntentQuestion, IntentOther:
		return Classification{Intent: r.Intent, Playbook: r.Playbook}, nil
	}
	return Classification{Intent: HeuristicIntent(text)}, fmt.Errorf("classify: unknown intent %q", r.Intent)
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
	return classifyInContext(ctx, o, text, c)
}

func classifyInContext(ctx context.Context, m Provider, text string, c Context) (got Intent, err error) {
	ctx, sp := tracing.StartSpan(ctx, "llm.ClassifyInContext", tracing.KindChain)
	var b strings.Builder
	defer func() {
		sp.Input(text)
		sp.Text("hora.classify.context", b.String())
		sp.OutputValue(map[string]any{"intent": got, "fallback": err != nil, "open_question": c.OpenQuestion})
		sp.Fail(err)
		sp.End()
	}()
	b.WriteString("Errands in progress:\n")
	for i, t := range c.ActiveTasks {
		fmt.Fprintf(&b, "%d. %q with supporter %s\n", i+1, t.Title, firstNonEmptyStr(t.SupporterName, "(unnamed)"))
	}
	if c.LastSupporterLine != "" {
		fmt.Fprintf(&b, "The supporter's last message to the customer: %q\n", c.LastSupporterLine)
	}
	switch c.OpenQuestion {
	case "booking", "budget", "name":
		fmt.Fprintf(&b, "HO:RA is currently waiting on the customer to answer a %s question about a pending NEW booking (a bare yes/no/amount would answer it; this message is not that). \"correction\" and \"answer\" are possible labels.\n", c.OpenQuestion)
	case "approval":
		b.WriteString("HO:RA is currently waiting on the customer to approve or decline the supporter's request for more time or money (a bare yes/no would answer it; this message is not that). \"answer\" is a possible label; \"correction\" is not.\n")
	}
	fmt.Fprintf(&b, "\nCustomer's message: %q", text)
	// 256 tokens: the answer is ~10, the rest is headroom for a model that
	// thinks before the JSON (a 64-token cap truncated GLM in staging).
	out, _, err := m.CompleteJSON(ctx, classifyInContextPrompt, b.String(), "intent", classifyInContextSchema(), 256)
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
	case IntentCorrection:
		if c.OpenQuestion != "booking" && c.OpenQuestion != "budget" && c.OpenQuestion != "name" {
			return IntentToSupporter, nil // nothing of ours to correct: for the supporter
		}
		return r.Intent, nil
	case IntentAnswer:
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

// ── An answer, or a question back (2026-11-07) ──────────────────────────

// QuestionReader tells an answer to HO:RA's question from a question asked
// back ("what to order?" → "what kind of pizza can I get? any
// recommendations?"). Asked only when the rules cannot tell.
type QuestionReader interface {
	ReadsAsQuestion(ctx context.Context, asked, reply string) (bool, error)
}

const readsAsQuestionPrompt = `HO:RA, an errand service in New York City, asked a customer a question about the errand they are booking. You get HO:RA's question and the customer's reply, both data.

Return {"asks_back": true} when the reply does not answer the question but asks HO:RA for help with it — a recommendation, what is good, what the place has or sells, what they could get ("what kind of pizza can I get? any recommendations?", "what's good there?", "not sure, what do people usually order?").
Return {"asks_back": false} when the reply answers it, even phrased as a question ("can you get me a pepperoni?", "2 bags, is that ok?", "maybe a margherita?"), or is anything else.`

func readsAsQuestion(ctx context.Context, m Provider, asked, reply string) (got bool, err error) {
	ctx, sp := tracing.StartSpan(ctx, "llm.ReadsAsQuestion", tracing.KindChain)
	defer func() {
		sp.Input(reply)
		sp.OutputValue(map[string]any{"asks_back": got, "model": m.Name(), "fallback": err != nil})
		sp.Fail(err)
		sp.End()
	}()
	schema := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"asks_back": map[string]any{"type": "boolean"}},
		"required":             []string{"asks_back"},
		"additionalProperties": false,
	}
	user := fmt.Sprintf("HO:RA's question: %q\nCustomer's reply: %q", asked, reply)
	out, _, err := m.CompleteJSON(ctx, readsAsQuestionPrompt, user, "asks_back", schema, 128)
	if err != nil {
		return false, fmt.Errorf("reads as question: %w", err)
	}
	var r struct {
		AsksBack bool `json:"asks_back"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(out)), &r); err != nil {
		return false, fmt.Errorf("reads as question decode: %w", err)
	}
	return r.AsksBack, nil
}
