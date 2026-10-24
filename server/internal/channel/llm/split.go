package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Splitter separates one message into the errands it asks for (M3, Aura
// 2026-10-20: "three tasks at once shouldn't take three rounds"). It only
// splits text; each errand is then parsed and priced like any single request,
// and all of them wait on ONE rule-matched YES. Nothing here moves money.
type Splitter interface {
	// SplitErrands returns the errands in text, in the order given, each as
	// the person's own words for that errand (never paraphrased, never
	// invented). A message with one errand returns that one. Returns at most
	// MaxErrands.
	SplitErrands(ctx context.Context, text string) ([]string, Usage, error)
}

// MaxErrands bounds one message. More than this is a conversation with ops,
// not a chat booking.
const MaxErrands = 5

const splitPrompt = `You split one text message sent to HO:RA, an on-demand errand service in New York City, into the separate errands it asks for.

Rules:
- An errand is one job for one person to do: a pickup, a delivery, a grocery run, waiting in line, a return, and so on.
- Keep the person's own words for each errand. Copy the words that belong to that errand; do not paraphrase, do not add details, do not fill in anything that was not said.
- Shared details apply to every errand they belong to. "Tomorrow 10am pick up my laundry and get groceries" → both errands carry "tomorrow 10am". "At 2pm pick up laundry, then at 5pm return a package" → each carries its own time.
- A single errand with several steps ("pick up my laundry and drop it at my office") stays ONE errand.
- Most messages contain exactly one errand. Only split when the message clearly asks for more than one separate job.
- Return at most 5 errands. If there are more, return the first 5.

Return {"errands": ["...", "..."]}.`

func splitSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"errands": map[string]any{
				"type":     "array",
				"items":    map[string]any{"type": "string"},
				"minItems": 1,
				"maxItems": MaxErrands,
			},
		},
		"required":             []string{"errands"},
		"additionalProperties": false,
	}
}

// SplitErrands asks the model. If the model is unavailable or answers out of
// schema, the whole message is one errand — the person still gets a draft,
// just not a split one — and the error is returned for the log.
func (o *OpenAICompat) SplitErrands(ctx context.Context, text string) ([]string, Usage, error) {
	single := []string{strings.TrimSpace(text)}
	out, u, err := o.completeJSON(ctx, splitPrompt, text, "errands", splitSchema(), 512)
	if err != nil {
		return single, u, fmt.Errorf("split errands: %w", err)
	}
	var r struct {
		Errands []string `json:"errands"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		return single, u, fmt.Errorf("split errands: decode: %w", err)
	}
	errands := make([]string, 0, len(r.Errands))
	for _, e := range r.Errands {
		if e = strings.TrimSpace(e); e != "" {
			errands = append(errands, e)
		}
		if len(errands) == MaxErrands {
			break
		}
	}
	if len(errands) == 0 {
		return single, u, fmt.Errorf("split errands: model returned no errands")
	}
	return errands, u, nil
}

// NeedsPurchase is the tiebreak for the budget question (budget.go): does
// this errand involve the supporter paying for something? Used only when the
// rule saw no purchase word. The answer never sets an amount.
func (o *OpenAICompat) NeedsPurchase(ctx context.Context, text string) (bool, error) {
	// 256: the answer is a few tokens, but a model that thinks first ran past
	// 32 on staging (2026-10-24) and the check fell back to "no purchase" —
	// a bagel errand was drafted without the budget question.
	out, _, err := o.completeJSON(ctx, purchasePrompt, text, "purchase", purchaseSchema(), 256)
	if err != nil {
		return false, fmt.Errorf("needs purchase: %w", err)
	}
	var r struct {
		Purchase bool `json:"purchase"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		return false, fmt.Errorf("needs purchase: decode: %w", err)
	}
	return r.Purchase, nil
}

const purchasePrompt = `You read one errand a person asked HO:RA (an on-demand errand service in New York City) to do.

Answer whether the person doing the errand will have to PAY for something on the requester's behalf — buy, order, or pick up an item the requester has not already paid for (food, groceries, flowers, a prescription, supplies, a ticket).

Not a purchase: picking up something already paid for or belonging to the requester (their laundry, a package, dry cleaning, keys), waiting in line, accompanying someone, moving or assembling things, returning an item.

Return {"purchase": true} or {"purchase": false}. When unsure, answer false.`

func purchaseSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"purchase": map[string]any{"type": "boolean"}},
		"required":             []string{"purchase"},
		"additionalProperties": false,
	}
}
