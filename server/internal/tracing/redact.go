package tracing

import (
	"regexp"
	"strings"
)

// Redact removes what must never reach Phoenix from text a person wrote, the
// context rendered for the model and the model's output (D-22): email
// addresses → [email], card-like digit runs (13+ digits) → [card], phone
// numbers → [phone].
//
// Phone and card numbers are found as runs of digits joined by at most two
// of " .-()" in a row — "+1 (212) 555-1234", "4242 4242 4242 4242" — and
// judged by their digit count, so what review needs stays readable: a time
// ("3pm", "2026-11-03T15:00:00-05:00"), an amount ("$40", "$1,250"), a door
// code ("4521"), a street number ("350 5th Ave"), a zip ("10001"). Erring
// is toward redacting: a list of ten single digits reads as a phone.
func Redact(s string) string {
	if s == "" {
		return s
	}
	s = emailRE.ReplaceAllString(s, "[email]")
	return redactNumbers(s)
}

var emailRE = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`)

const numberSeps = " .-()"

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// redactNumbers scans bytes: a digit is always one ASCII byte, and no byte of
// a multi-byte UTF-8 character is one, so text in any language passes
// through untouched around the numbers.
func redactNumbers(s string) string {
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); {
		end, label := numberRun(s, i)
		if end == i {
			i++
			continue
		}
		if label != "" {
			b.WriteString(s[last:i])
			b.WriteString(label)
			last = end
		}
		i = end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// numberRun returns where the run of digits starting at i ends (i when none
// starts there) and its label, "" to keep it as written.
func numberRun(s string, i int) (int, string) {
	j := i
	plus := false
	for j < len(s) && (s[j] == '+' || s[j] == '(') {
		plus = plus || s[j] == '+'
		j++
	}
	if j-i > 2 || j >= len(s) || !isDigit(s[j]) {
		return i, ""
	}
	digits := 0
	var groups []int
	var seps []byte
	for {
		n := 0
		for j < len(s) && isDigit(s[j]) {
			j, n = j+1, n+1
		}
		digits += n
		groups = append(groups, n)
		// Up to two separators, then a digit (or "(" then a digit), continue.
		k := j
		for k < len(s) && k-j < 2 && strings.IndexByte(numberSeps, s[k]) >= 0 {
			k++
		}
		if k == j || k >= len(s) || !isDigit(s[k]) {
			break
		}
		seps = append(seps, s[j:k]...)
		j = k
	}
	end := j
	switch {
	case digits >= 13:
		return end, "[card]"
	case digits >= 10, plus && digits >= 7:
		return end, "[phone]"
	case digits == 7 && len(groups) == 2 && groups[0] == 3 && len(seps) == 1 && (seps[0] == '-' || seps[0] == '.'):
		return end, "[phone]" // 555-1234
	}
	return end, ""
}
