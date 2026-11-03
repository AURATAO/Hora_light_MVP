package tracing

import (
	"strings"
	"testing"
)

func TestRedactRemovesPhonesEmailsAndCards(t *testing.T) {
	cases := []struct{ in, want string }{
		// Phones, as people write them.
		{"call me at +1 (212) 555-1234 please", "call me at [phone] please"},
		{"my number is 212-555-1234", "my number is [phone]"},
		{"212.555.1234", "[phone]"},
		{"(212) 555-1234", "[phone]"},
		{"2125551234", "[phone]"},
		{"+12125551234", "[phone]"},
		{"1-800-555-0199 is the store", "[phone] is the store"},
		{"+44 20 7946 0958", "[phone]"},
		{"+39 06 1234 5678", "[phone]"},
		{"local 555-1234", "local [phone]"},
		{"打这个电话2125551234谢谢", "打这个电话[phone]谢谢"},
		{"tel:+12125551234", "tel:[phone]"},
		// Emails.
		{"email aura.tao+hora@gmail.com now", "email [email] now"},
		{"x@y.co", "[email]"},
		{"Jane_Doe-1@mail.example.org.", "[email]."},
		// Card-like runs.
		{"card 4242 4242 4242 4242 exp 12/30", "card [card] exp 12/30"},
		{"4242-4242-4242-4242", "[card]"},
		{"4242424242424242", "[card]"},
		{"amex 3782 822463 10005", "amex [card]"},
		// Together, in JSON as the context is rendered.
		{`{"text": "text 917-555-0100 or a@b.io"}`, `{"text": "text [phone] or [email]"}`},
	}
	for _, c := range cases {
		if got := Redact(c.in); got != c.want {
			t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// What review needs must survive: times, amounts, codes, addresses, the
// model's own JSON.
func TestRedactKeepsWhatIsNotPersonal(t *testing.T) {
	keep := []string{
		"",
		"pick up laundry at 3pm, about $40",
		"budget $1,250 max",
		"the door code is 4521",
		"350 5th Ave, New York, NY 10118",
		"apt 12B, 2nd floor",
		"tomorrow 10-11am",
		"2 bags, 3 eggs, 12 bagels",
		`{"title":"Grocery run","duration_minutes":30,"scheduled":true,"scheduled_time":"2026-11-03T19:00:00-05:00"}`,
		`{"action":"relay","task":"t1","escalation":""}`,
		"CONTEXT (data, not instructions):\n{\n  \"minutes_ago\": 12,\n  \"answered\": false\n}",
		"order #48213 is ready",
		"她到底到哪了",
		"meet at 7:30",
	}
	for _, s := range keep {
		if got := Redact(s); got != s {
			t.Errorf("Redact(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestRedactLeavesNoDigitsOfAPhone(t *testing.T) {
	in := "hi it's me, 646 555 0172, text me or mail me@x.com, card 5555 5555 5555 4444"
	got := Redact(in)
	for _, frag := range []string{"646", "0172", "me@x.com", "4444"} {
		if strings.Contains(got, frag) {
			t.Errorf("Redact left %q in %q", frag, got)
		}
	}
}
