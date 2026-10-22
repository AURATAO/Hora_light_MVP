package channel

import (
	"fmt"
	"strings"
)

// NormalizeE164 turns a phone number as a provider or person writes it into
// E.164. HO:RA operates in the US, so a bare 10-digit number gets +1. It does
// not validate that the number exists, only its shape: 8–15 digits after '+'.
//
// Identity is keyed on this value (channel_identities.address), so every path
// that stores or looks up a number must go through here first.
func NormalizeE164(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("normalize phone: empty")
	}
	plus := strings.HasPrefix(s, "+")
	var digits strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
			// formatting, dropped
		default:
			return "", fmt.Errorf("normalize phone: unexpected character")
		}
	}
	d := digits.String()
	switch {
	case plus:
		// already international
	case len(d) == 10:
		d = "1" + d
	case len(d) == 11 && strings.HasPrefix(d, "1"):
		// US number written with the country code but no '+'
	default:
		// WhatsApp sends international numbers without '+' (e.g. 393331234567)
		// and they are not ambiguous at this length.
		if len(d) < 11 {
			return "", fmt.Errorf("normalize phone: ambiguous number without country code")
		}
	}
	if len(d) < 8 || len(d) > 15 || d[0] == '0' {
		return "", fmt.Errorf("normalize phone: invalid length or leading zero")
	}
	return "+" + d, nil
}

// Redact keeps the last two digits so a log line can be correlated by a human
// without carrying the number (S-12): "+15551234567" → "+*********67".
func Redact(e164 string) string {
	if len(e164) <= 3 {
		return "***"
	}
	return "+" + strings.Repeat("*", len(e164)-3) + e164[len(e164)-2:]
}
