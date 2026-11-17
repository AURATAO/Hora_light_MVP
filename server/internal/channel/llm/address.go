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
- The address is for the stop the map could not find, never another stop they gave: a drop-off address ("bring it to 120 E 7th St") is not where the place they named is.

Also read whether they named a business or place to go to — a shop, café, restaurant, chain or store ("Blue Bottle", "a Duane Reade", "the Apple Store") — without giving its street address:
- "place": its name as people search for it on a map ("Blue Bottle Coffee", "Duane Reade", "Apple Store"); null when they named none, or when they gave its street address.
- "near": where they said to look for it, as a map search area ("W 54th St, Manhattan", "Manhattan", "5th Ave and 57th St", "near 2 Bond St"); null when they said nowhere. A street said without a building number ("54 w", "close to 54th") is a search area here, never an address.
A question about where the place is ("where are Blue Bottle in Manhattan", "is there one close to 54th?", "I don't know the address") names the place and the area: fill both.

Return {"address": string or null, "place": string or null, "near": string or null}.`

// StopGuess is the guess: the full address, and the business named with no
// address for it (Place, searched near Near). Any may be "".
type StopGuess struct {
	Address, Place, Near string
}

// StopGuesser is AddressGuesser with the named place (pilot 2026-11-17: "I
// need a coffee at blue bottle, is one close to 54 w?" had no address to
// guess, only a café to find).
type StopGuesser interface {
	GuessStop(ctx context.Context, said []string, unclear, area string) (StopGuess, error)
}

func guessAddress(ctx context.Context, m Provider, said []string, unclear, area string) (got StopGuess, err error) {
	ctx, sp := tracing.StartSpan(ctx, "llm.GuessAddress", tracing.KindChain)
	defer func() {
		sp.Input(unclear)
		sp.OutputValue(map[string]any{"address": got.Address, "place": got.Place, "near": got.Near, "model": m.Name()})
		sp.Fail(err)
		sp.End()
	}()
	nullable := map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}}
	schema := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"address": nullable, "place": nullable, "near": nullable},
		"required":             []string{"address", "place", "near"},
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
		return StopGuess{}, fmt.Errorf("guess address: %w", err)
	}
	var r struct {
		Address, Place, Near *string
	}
	if err := json.Unmarshal([]byte(extractJSONObject(out)), &r); err != nil {
		return StopGuess{}, fmt.Errorf("guess address decode: %w", err)
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return strings.TrimSpace(*p)
	}
	return StopGuess{Address: str(r.Address), Place: str(r.Place), Near: str(r.Near)}, nil
}

// GuessAddressPrompt is the system prompt, for the eval runner.
const GuessAddressPrompt = guessAddressPrompt

func (m *Model) GuessAddress(ctx context.Context, said []string, unclear, area string) (string, error) {
	g, err := guessAddress(ctx, m.provider(), said, unclear, area)
	return g.Address, err
}

// GuessStop is GuessAddress with the place named and where to look for it.
func (m *Model) GuessStop(ctx context.Context, said []string, unclear, area string) (StopGuess, error) {
	return guessAddress(ctx, m.provider(), said, unclear, area)
}
