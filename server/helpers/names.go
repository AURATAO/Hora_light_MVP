package helpers

import (
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// How a person is named, everywhere HO:RA says their name.
//
// The chain is: the display name they chose (profiles.name) → otherwise the
// local part of their email, title-cased. The second half exists ONLY because
// something has to be printed for an account that has never been asked; it is
// the last resort, and it is not a name. Before build 12 every profile was
// SEEDED with it on creation, which is how "taoaura.lavoro is on the way"
// reached a requester's lock screen — the seed read as a chosen name to every
// surface that checked for one.
//
// The DB half (looking the profile up) lives in the main package as
// resolveDisplayName; this file is the pure half, in helpers so the ops
// package can apply the same rule to a row it already holds. Clients never
// derive a name from an email themselves (app/src/lib/displayName.test.mjs
// guards both trees for it).

// EmailPrefixName is the last resort: "jane.doe@x.com" → "Jane Doe".
func EmailPrefixName(email string) string {
	email = strings.TrimSpace(email)
	if i := strings.IndexByte(email, '@'); i > 0 {
		return cases.Title(language.Und).String(strings.ReplaceAll(email[:i], ".", " "))
	}
	return email
}

// HasDisplayName reports whether a stored profile name is one the person
// chose. Empty is the only "no" — the seeded prefixes are blanked by migration
// 20260922120000, so this does not have to guess whether "Jane Doe" was typed
// or derived, and a person who deliberately types the same name their email
// carries gets to keep it.
func HasDisplayName(name string) bool {
	return strings.TrimSpace(name) != ""
}

// DisplayName applies the chain to values already in hand.
func DisplayName(profileName, email string) string {
	if HasDisplayName(profileName) {
		return strings.TrimSpace(profileName)
	}
	return EmailPrefixName(email)
}
