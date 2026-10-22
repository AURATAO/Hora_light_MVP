package channel

import (
	"regexp"
	"strings"
	"unicode"
)

// Answers to an approval question (D-17 R-2, R-3, Arbiter 2026-10-22).
// These apply to the supporter's money/time requests only; the booking YES
// and every other question keep yesRE/noRE.
//
// Whole-message words, compared after normalise (lower case, curly quotes
// straightened, trailing punctuation and friendly emoji dropped, runs of a
// repeated letter collapsed — "Yesss", "Kk", "Okie", "sureee").

// plainYes: consent words. Not limited by what we sent last.
var plainYes = wordSet(
	"yes", "yeah", "yep", "yup", "yea", "ya", "y", "yessir", "hell yes", "yes please",
	"ok", "okay", "k", "kk", "oki", "okie",
	"sure", "alright", "all right", "why not", "of course", "absolutely", "definitely", "for sure", "100%",
	"let's do it", "lets do it", "go ahead", "go for it", "i'm down", "im down", "i'm in", "im in",
	"sounds good", "sounds great", "works for me", "do it", "confirm", "confirmed",
	"✅", "好", "好的", "可以", "是", "確認", "sí", "si",
)

// reactionYes: also reactions to news. Count as yes only when our last
// message in the thread IS the approval question (D-17 R-3).
var reactionYes = wordSet(
	"great", "perfect", "awesome", "amazing", "wonderful", "fine",
	"👍", "🙌", "👌",
)

// noWords: whole-message refusals (noRE's list).
var noWords = wordSet("no", "n", "nope", "nah", "no thanks", "no thank you", "cancel", "stop",
	"nevermind", "never mind", "don't", "dont", "不要", "取消", "不用")

// noWithReasonRE: a no-word, then punctuation, then a reason — "no, he
// needs to finish now", "nope, too much". The punctuation is required, so
// "no problem" / "no worries" never match (R-2).
var noWithReasonRE = regexp.MustCompile(`^(no|nope|nah|no thanks|不要|不用)\s*[,.!;:，。！]\s*\S`)

// conditionRE: what makes a "yes, …" not an approval (R-3): an amount, a
// number, a limit, a condition, a negation, or a question.
var conditionRE = regexp.MustCompile(`[$€£¥\d?？]|\b(max|maximum|min|minimum|only|if|unless|under|over|less|more than|up to|at most|at least|cheaper|instead|without|except|no|not|don't|dont|never|wait|hold on)\b|不超過|只要|如果|最多|最少|不要|不用|不`)

func wordSet(ws ...string) map[string]bool {
	m := make(map[string]bool, len(ws))
	for _, w := range ws {
		m[collapse(w)] = true
	}
	return m
}

// trailingNoise: what may follow an answer without changing it.
func trailingNoise(r rune) bool {
	switch r {
	case '!', '.', '~', '…', '！', '。', '🙏', '😊', '🙂', '😀', '😄', '❤', '️':
		return true
	}
	return unicode.IsSpace(r)
}

func normalise(text string) string {
	s := strings.ToLower(strings.TrimSpace(text))
	s = strings.NewReplacer("’", "'", "‘", "'").Replace(s)
	s = strings.TrimRightFunc(s, trailingNoise)
	s = strings.Join(strings.Fields(s), " ")
	return collapse(s)
}

// collapse runs of the same ASCII letter: "yesss" → "yes", "kk" → "k".
// Applied to both the word lists and the message, so they compare alike.
func collapse(s string) string {
	var b strings.Builder
	var prev rune
	for _, r := range s {
		if r == prev && r < unicode.MaxASCII && unicode.IsLetter(r) {
			continue
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}

// approvalYes: the whole message is a yes. reaction reports a reaction
// word (limited by what we sent last).
func approvalYes(text string) (yes, reaction bool) {
	s := normalise(text)
	if plainYes[s] {
		return true, false
	}
	if reactionYes[s] {
		return true, true
	}
	return false, false
}

// approvalNo: a refusal — a whole-message no-word, or a no-word with a
// reason after punctuation.
func approvalNo(text string) bool {
	s := normalise(text)
	return noWords[s] || noWithReasonRE.MatchString(s)
}

// yesWithMessage: a yes-word followed by more words ("yes but tell him to
// hurry", "ok, thanks Alex!"). rest is the part after the yes, as the
// person wrote it. conditional: the rest carries a condition, a number, a
// negation or a question — not an approval (R-3).
func yesWithMessage(text string) (rest string, reaction, conditional, ok bool) {
	orig := []rune(strings.TrimSpace(text))
	low := make([]rune, len(orig)) // same length as orig: indexes line up
	for i, r := range orig {
		switch r {
		case '’', '‘':
			r = '\''
		}
		low[i] = unicode.ToLower(r)
	}
	// Longest yes-word prefix that ends at a separator.
	best := -1
	for _, set := range []map[string]bool{plainYes, reactionYes} {
		for w := range set {
			if i := prefixEnd(low, []rune(w)); i > best {
				best = i
				_, reaction = approvalYes(string(low[:i]))
			}
		}
	}
	if best <= 0 {
		return "", false, false, false
	}
	tail := strings.TrimLeftFunc(string(orig[best:]), func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",.;:!-—–，。！、", r)
	})
	if tail == "" {
		return "", false, false, false // the whole message is the word: approvalYes's case
	}
	return tail, reaction, conditionRE.MatchString(strings.ToLower(tail)), true
}

// prefixEnd: if low starts with word (repeated letters allowed, as in
// collapse) and the word is followed by a separator, the rune index where
// the word ends; else -1.
func prefixEnd(lr, wr []rune) int {
	i, j := 0, 0
	var prev rune
	for j < len(wr) && i < len(lr) {
		switch {
		case lr[i] == wr[j]:
			prev = lr[i]
			i++
			j++
		case lr[i] == prev && prev < unicode.MaxASCII && unicode.IsLetter(prev):
			i++ // a repeated letter: "yesss"
		default:
			return -1
		}
	}
	if j < len(wr) {
		return -1
	}
	for i < len(lr) && lr[i] == prev && prev < unicode.MaxASCII && unicode.IsLetter(prev) {
		i++
	}
	if i >= len(lr) {
		return -1
	}
	if next := lr[i]; !(unicode.IsSpace(next) || strings.ContainsRune(",.;:!-—–，。！、", next)) {
		return -1 // "okay" is not "ok" + "ay"; "ok不要" has no separator
	}
	return i
}

// approvalAnswerish: anything the approval rules would read as an answer
// (for the I-P gate and the sent-before guards).
func approvalAnswerish(text string) bool {
	if y, _ := approvalYes(text); y {
		return true
	}
	if approvalNo(text) {
		return true
	}
	_, _, _, ok := yesWithMessage(text)
	return ok
}
