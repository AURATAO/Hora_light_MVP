package channel

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Answers to an approval question (D-17 R-2, R-3, Arbiter 2026-10-22).
// These apply to the supporter's money/time requests only (I-S); the
// booking YES and every other question keep yesRE/noRE.
//
// Whole-message words, compared after normalise: NFKC (fullwidth forms,
// composed accents), lower case, curly quotes straightened, emoji skin
// tones and variation selectors dropped, trailing punctuation and friendly
// emoji dropped, and runs of a repeated letter collapsed ("Yesss", "Kk",
// "Okie", "sureee") — except a message that is one letter repeated three
// or more times ("kkkkk" is laughter, not consent).

// plainYes: consent words. Not limited by what we sent last.
var plainYes = wordSet(
	"yes", "yeah", "yep", "yup", "yea", "ya", "y", "yessir", "hell yes", "yes please",
	"ok", "okay", "k", "kk", "oki", "okie",
	"sure", "alright", "all right", "why not", "of course", "absolutely", "definitely", "for sure", "100%",
	"let's do it", "lets do it", "go ahead", "go for it", "i'm down", "im down", "i'm in", "im in",
	"sounds good", "sounds great", "works for me", "do it", "book it", "book", "confirm", "confirmed",
	"✅", "好", "好的", "可以", "是", "確認", "sí", "sì", "si",
)

// weakHeads: whole-message yes, but too ambiguous to start a "yes + a
// message" — "y tho", "si es barato" (Spanish "if"), "ya veremos", "k
// whatever", "book a cab too". As a head they make the message a non-answer.
var weakHeads = wordSet("y", "ya", "yea", "si", "k", "kk", "book", "book it", "confirm", "confirmed", "do it", "是")

// maxTailWords: a "yes + a message" tail longer than this is not read as
// an instruction — conditional (R-3 backstop: no list of conditions is
// ever complete; a long tail is a conversation, not a footnote).
const maxTailWords = 8

// reactionYes: also reactions to news. Count as yes only when our last
// message in the thread IS the approval question (D-17 R-3).
var reactionYes = wordSet(
	"great", "perfect", "awesome", "amazing", "wonderful", "fine",
	"👍", "🙌", "👌",
)

// noWords: whole-message refusals.
var noWords = wordSet("no", "n", "nope", "nah", "no thanks", "no thank you", "cancel", "stop",
	"nevermind", "never mind", "nvm", "don't", "dont", "不要", "取消", "不用", "不行", "別")

// yesLikeTails: a tail that only repeats the yes ("ok great", "sure, no
// problem") — the message is a plain yes, nothing to pass on.
var yesLikeTails = wordSet("no problem", "no worries", "no rush", "thing", "100%",
	"thanks", "thank you", "thanks so much", "thank you so much", "thx", "ty", "please", "pls", "plz")

// noWithReasonRE: a no-word, then punctuation, then a reason — "no, he
// needs to finish now", "nope, too much". The punctuation is required, so
// "no problem" / "no worries" never match (R-2).
var noWithReasonRE = regexp.MustCompile(`^(no|nope|nah|no thanks|nvm|不要|不用|不行)\s*[,.!;:，。！]\s*(.+)$`)

// conditionRE: what makes a "yes, …" tail not an approval (R-3, I-U). It
// errs toward conditional: a wrong "conditional" costs one more message, a
// wrong "unconditional" approves money. Read on a normalised tail.
var conditionRE = regexp.MustCompile(strings.Join([]string{
	`[$€£¥\d?]`, // amounts, digits (NFKC folds fullwidth), questions
	`\b(max|maximum|min|minimum|only|just|if|unless|under|over|less|more|than|up to|at most|at least|cheaper|instead|without|except|limit|budget|cap)\b`,
	`\b(as long as|so long as|provided|providing|assuming|depending|depends|otherwise|until|in case|as soon as)\b`,
	`\b(maybe|perhaps|probably|possibly|i guess|i think|guess|whatever|idk|dunno|hmm+|we'll see|well see|let's see|lets see|let's talk|lets talk|not sure|unsure)\b`,
	`\b(n|no|not|nope|nah|nvm|never|nevermind|none|nothing|non|cancel|stop)\b`,
	`\b(don'?t|can'?t|won'?t|shouldn'?t|couldn'?t|wouldn'?t|isn'?t|aren'?t|wasn'?t|weren'?t|didn'?t|doesn'?t|haven'?t|hasn'?t|hadn'?t|mustn'?t|needn'?t|ain'?t)\b`,
	`\b(wait|hold on|hang on|let me|lemme|think|check|ask|later|first|before|after|so|tomorrow|tonight|next)\b`,
	`\b(what|why|how|who|when|where|which|can|could|should|would|is|are|do|does|did|will|has|have|was|were|may|might|need)\b`,
	`\b(too|much|expensive|pricey|a lot|steep|crazy|ridiculous|forget|right)\b`,
	`\b(one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|fifteen|twenty|thirty|forty|fifty|hundred|half|dollars?|bucks?|cents?|minutes?|mins?|hours?|hrs?)\b`,
	`\b(pero|ma|solo|mas|meno|menos|sin|senza|luego|domani)\b|più|però|mañana|sólo|más`, // Spanish / Italian (non-ASCII words outside \b: RE2's \b is ASCII)
	`不超過|只要|如果|最多|最少|不要|不用|取消|除非|只能|只有|別|不|想|考慮|看看|等|先|再|問|晚點|待會|明天|[一二三四五六七八九十百千兩半塊元分]`,
}, "|"))

// friendlyEmoji may close a message without changing it.
var friendlyEmoji = map[rune]bool{'🙏': true, '😊': true, '🙂': true, '😀': true, '😄': true, '😁': true, '😃': true,
	'😂': true, '🤣': true, '😎': true, '💪': true, '🎉': true, '🥳': true, '💯': true, '❤': true, '♥': true}

// answerEmoji may close a word-answer too ("Yes👍", "ok ✅") — but alone
// they ARE the answer, so they are never trimmed to nothing.
var answerEmoji = map[rune]bool{'👍': true, '✅': true, '🙌': true, '👌': true}

func wordSet(ws ...string) map[string]bool {
	m := make(map[string]bool, len(ws))
	for _, w := range ws {
		m[normalise(w)] = true
	}
	return m
}

func trailingNoise(r rune) bool {
	switch r {
	case '!', '.', ',', '~', '…', '！', '。', '，', '、':
		return true
	}
	return unicode.IsSpace(r) || friendlyEmoji[r] || answerEmoji[r]
}

// trimTrailing drops trailing noise but never down to nothing: "👍👍" is
// the answer itself.
func trimTrailing(s string) string {
	t := strings.TrimRightFunc(s, trailingNoise)
	if t != "" {
		return t
	}
	return strings.TrimRightFunc(s, func(r rune) bool { return r != '👍' && r != '✅' && r != '🙌' && r != '👌' && trailingNoise(r) })
}

// clean: NFKC, lower case, straight quotes, no skin tones or variation
// selectors. Rune-for-rune stable after this step is not guaranteed, so
// callers that need positions work on its output only.
func clean(text string) string {
	s := norm.NFKC.String(strings.TrimSpace(text))
	var b strings.Builder
	var prev rune
	for _, r := range s {
		switch {
		case r == '’' || r == '‘' || r == '`':
			r = '\''
		case r >= 0x1F3FB && r <= 0x1F3FF, r == 0xFE0F, r == 0xFE0E, r == 0x200D:
			continue // skin tones, variation selectors, ZWJ
		case r == prev && r > 0x2000 && !unicode.IsLetter(r) && !unicode.IsNumber(r):
			continue // "👍👍" → "👍"
		}
		b.WriteRune(unicode.ToLower(r))
		prev = r
	}
	return b.String()
}

func normalise(text string) string {
	s := trimTrailing(clean(text))
	s = strings.Join(strings.Fields(s), " ")
	if oneLetterRun(s) {
		return s // "kkkkk": laughter — left as is, matches nothing
	}
	return collapse(s)
}

func oneLetterRun(s string) bool {
	rs := []rune(s)
	if len(rs) < 3 {
		return false
	}
	for _, r := range rs {
		if r != rs[0] {
			return false
		}
	}
	return true
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
	// "ok great", "sure, no problem": a yes followed only by more yes.
	if rest, r, _, ok := splitYes(clean(text)); ok && yesLikeTail(rest) {
		_, tailReaction := approvalYes(rest)
		return true, r && (tailReaction || !plainYes[normalise(rest)])
	}
	return false, false
}

func yesLikeTail(rest string) bool {
	n := normalise(rest)
	return plainYes[n] || reactionYes[n] || yesLikeTails[n]
}

// approvalNo: a refusal — a whole-message no-word, or a no-word, then
// punctuation, then a reason that is not itself a yes or a question ("no!
// go ahead", "no, it's fine", "no, why?" are not refusals: mixedNo).
func approvalNo(text string) bool {
	s := normalise(text)
	if noWords[s] {
		return true
	}
	m := noWithReasonRE.FindStringSubmatch(s)
	return m != nil && !mixedReason(m[2])
}

// mixedNo: a no-word with a reason that contradicts it or asks — passed to
// the supporter, the request stays open (R-2: a no is a decline only when
// it is one).
func mixedNo(text string) bool {
	m := noWithReasonRE.FindStringSubmatch(normalise(text))
	return m != nil && mixedReason(m[2])
}

var affirmingRE = regexp.MustCompile(`\b(go ahead|it's fine|its fine|that's fine|thats fine|do it|fine|ok|okay|sure|yes|yeah|yep|approve|approved)\b|[?]|^(why|what|how)\b`)

// negationRE: a reason that negates is a decline reason ("no, I'm not fine
// with that"), not a contradiction.
var negationRE = regexp.MustCompile(`\b(not|never|don'?t|can'?t|won'?t|isn'?t|aren'?t|no longer)\b`)

func mixedReason(reason string) bool {
	r := strings.Join(strings.Fields(clean(reason)), " ")
	if strings.ContainsAny(r, "?？") {
		return true // "no, why?" asks: passed on
	}
	if negationRE.MatchString(r) {
		return false // "no, I'm not fine with that": a decline
	}
	if y, _ := approvalYes(reason); y {
		return true
	}
	if _, _, _, ok := splitYes(clean(reason)); ok {
		return true
	}
	return affirmingRE.MatchString(r)
}

// yesWithMessage: a yes-word followed by more words ("yes but tell him to
// hurry", "ok, thanks Alex!"). rest is the part after the yes, as written
// (cleaned). conditional: the rest carries a condition, a number, a
// negation, a deferral, a question or a complaint — not an approval (R-3).
// A rest that only repeats the yes is approvalYes's case, not this one.
func yesWithMessage(text string) (rest string, reaction, conditional, ok bool) {
	c := clean(text)
	rest, reaction, cond, ok := splitYes(c)
	if !ok || yesLikeTail(rest) {
		return "", false, false, false
	}
	return rest, reaction, cond, true
}

// splitYes finds the longest yes-word prefix followed by a separator in a
// cleaned string.
func splitYes(c string) (rest string, reaction, conditional, ok bool) {
	lr := []rune(c)
	best := -1
	for _, set := range []map[string]bool{plainYes, reactionYes} {
		for w := range set {
			if i := prefixEnd(lr, []rune(w)); i > best {
				head := string(lr[:i])
				ok, r := approvalYesWord(head)
				if !ok {
					continue // "kkkkk, lol": laughter is not a yes head
				}
				best = i
				reaction = r
			}
		}
	}
	if best <= 0 {
		return "", false, false, false
	}
	if weakHeads[normalise(string(lr[:best]))] {
		// "y tho", "si es barato": an ambiguous head makes the whole
		// message a non-answer (asked again), not a yes.
		return "", false, false, false
	}
	tail := strings.TrimLeftFunc(string(lr[best:]), func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",.;:!—–，。！、", r)
	})
	tail = strings.TrimSpace(trimTrailing(tail))
	if tail == "" {
		return "", false, false, false // the whole message is the word
	}
	return tail, reaction, tailConditional(tail), true
}

// approvalYesWord: is this exact (cleaned) prefix a list word, and which
// kind — without the yes-tail rule (no recursion).
func approvalYesWord(s string) (yes, reaction bool) {
	n := normalise(s)
	return plainYes[n] || reactionYes[n], reactionYes[n] && !plainYes[n]
}

func tailConditional(tail string) bool {
	t := normalise(tail)
	if yesLikeTails[t] {
		return false
	}
	// The markers are written as spelled ("unless", "steep", "three"), so
	// they are matched on the cleaned tail before collapsing repeated
	// letters — and on the collapsed one too ("waaait" → "wait").
	c := strings.Join(strings.Fields(clean(tail)), " ")
	if conditionRE.MatchString(c) || conditionRE.MatchString(t) {
		return true
	}
	if len(strings.Fields(c)) > maxTailWords {
		return true
	}
	// An emoji the friendly set does not cover (👎 🙄 😡 ❌): read as a
	// reaction against.
	for _, r := range t {
		if r > 0x2000 && !friendlyEmoji[r] && !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsPunct(r) && !unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// prefixEnd: if lr starts with word (repeated letters allowed, as in
// collapse) and the word is followed by a separator, the rune index where
// the word ends; else -1. A hyphen is not a separator ("ok-ish").
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
	if next := lr[i]; !(unicode.IsSpace(next) || strings.ContainsRune(",.;:!—–，。！、", next)) {
		return -1 // "okay" is not "ok" + "ay"; "ok不要" has no separator
	}
	return i
}

// approvalAnswerish: anything the approval rules would read as an answer
// or a refusal (for the I-P gate and the sent-before / unconfirmed guards).
func approvalAnswerish(text string) bool {
	if y, _ := approvalYes(text); y {
		return true
	}
	if approvalNo(text) || mixedNo(text) {
		return true
	}
	_, _, _, ok := yesWithMessage(text)
	return ok
}
