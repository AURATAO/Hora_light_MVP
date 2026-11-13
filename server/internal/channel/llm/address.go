package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"hora-auth/internal/tracing"
)

// AddressGuesser proposes the full NYC address a person most likely meant
// when the map could not place what they wrote (2026-11-12: "335 e 3 ave",
// "6 street", "335 6 st 3 ave" in the East Village). The guess is only a
// question: the router checks it with Google and the person confirms it
// with YES before anything uses it.
type AddressGuesser interface {
	// GuessAddress reads everything the person said for this request
	// (said, oldest first), the stop that could not be placed, and the
	// neighbourhood they named ("" none). "" when there is no single
	// likely address — never an error for that.
	GuessAddress(ctx context.Context, said []string, unclear, area string) (string, error)
}

const guessAddressPrompt = `You help an errand service in New York City read a street address a customer typed in a hurry. The map could not find what they wrote. From everything they said for this request, propose the ONE most likely full street address in the five boroughs.

Read NYC shorthand: "e"/"w" before a number is East/West; "2 ave" or "2nd av" is Second Avenue; "9 st" or "9 street" is 9th Street; a neighbourhood ("East Village", "Williamsburg") tells you the borough and the part of the grid; an avenue said next to a street ("9 st 2 ave") is the cross street, which tells you which block. Manhattan street addresses are "number E/W Nth St"; avenues have no East/West.

Rules:
- People send an address in pieces and fix it across messages: read all of them together. A building number from an earlier message still applies to a street named in a later one, and the newest correction wins ("88 w 10", then "sorry 11 st" is 88 W 11th St).
- Use a building number the customer wrote. Never invent one. If they gave no building number in any message, return null.
- Return the address as "number street, city, NY ZIP", e.g. "120 W 72nd St, New York, NY 10023" (Manhattan's city is "New York"; Brooklyn is "Brooklyn"; Queens uses the neighbourhood's postal city).
- Return null when no single address is clearly the most likely, or when what they wrote is not an address at all.

Return {"address": string or null}.`

func guessAddress(ctx context.Context, m Provider, said []string, unclear, area string) (got string, err error) {
	ctx, sp := tracing.StartSpan(ctx, "llm.GuessAddress", tracing.KindChain)
	defer func() {
		sp.Input(unclear)
		sp.OutputValue(map[string]any{"address": got, "model": m.Name()})
		sp.Fail(err)
		sp.End()
	}()
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{"address": map[string]any{"anyOf": []any{
			map[string]any{"type": "string"}, map[string]any{"type": "null"},
		}}},
		"required":             []string{"address"},
		"additionalProperties": false,
	}
	var b strings.Builder
	b.WriteString("What the customer said, oldest first:\n")
	for _, s := range said {
		if s = strings.TrimSpace(s); s != "" {
			fmt.Fprintf(&b, "- %q\n", s)
		}
	}
	fmt.Fprintf(&b, "The map could not find: %q\n", unclear)
	if area != "" {
		fmt.Fprintf(&b, "They said they are in: %q\n", area)
	}
	out, _, err := m.CompleteJSON(ctx, guessAddressPrompt, b.String(), "address_guess", schema, 256)
	if err != nil {
		return "", fmt.Errorf("guess address: %w", err)
	}
	var r struct {
		Address *string `json:"address"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(out)), &r); err != nil {
		return "", fmt.Errorf("guess address decode: %w", err)
	}
	if r.Address == nil {
		return "", nil
	}
	return strings.TrimSpace(*r.Address), nil
}

// GuessAddressPrompt is the system prompt, for the eval runner.
const GuessAddressPrompt = guessAddressPrompt

func (m *Model) GuessAddress(ctx context.Context, said []string, unclear, area string) (string, error) {
	return guessAddress(ctx, m.provider(), said, unclear, area)
}
