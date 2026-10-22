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

// reactionYes: also reactions to news. Count as yes only when our last
// message in the thread IS the approval question (D-17 R-3).
var reactionYes = wordSet(
	"great", "perfect", "awesome", "amazing", "wonderful", "fine",
	"no problem", "no worries", // Arbiter 2026-10-22: approve — as reactions, since they also answer "running 5 min late"
	"👍", "🙌", "👌",
)

// noWords: whole-message refusals.
var noWords = wordSet("no", "n", "nope", "nah", "no thanks", "no thank you", "cancel", "stop",
	"nevermind", "never mind", "nvm", "don't", "dont", "不要", "取消", "不用", "不行", "別")

// yesLikeTails: a tail that only repeats the yes ("ok great", "sure, no
// problem") — the message is a plain yes, nothing to pass on.
var yesLikeTails = wordSet("no problem", "no worries", "no rush", "thing", "100%",
	// Arbiter 2026-10-22 16:51: "yes that's fine" approves directly.
	"that's fine", "thats fine", "it's fine", "its fine",
	"thanks", "thank you", "thanks so much", "thank you so much", "thx", "ty", "please", "pls", "plz")

// noWithReasonRE: a no-word, then punctuation, then a reason — "no, he
// needs to finish now", "nope, too much". The punctuation is required, so
// "no problem" / "no worries" never match (R-2).
var noWithReasonRE = regexp.MustCompile(`^(no|nope|nah|no thanks|nvm|(?:不要|不用|不行)[啊呀喔哦囉啦]*)\s*[,.!;:，。！]\s*(.+)$`)

// rulingMarkerRE: R-3's ruled markers — an amount, a digit, a cap or a
// floor. Checked on the raw tail BEFORE the supporter's name is put in, so
// a supporter named Max or Min never turns "thanks Max" into an approval
// (round 5). Every other tail is decided by harmlessTailRE alone.
var rulingMarkerRE = regexp.MustCompile(`[$€£¥\d]|\b(max|maximum|min|minimum|only|up to|at most|at least|under|over|less|more|budget|cap|limit)\b`)

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
		case r >= 0x1F3FB && r <= 0x1F3FF, r == 0xFE0F, r == 0xFE0E, r == 0x200D,
			r == 0x200B, r == 0x200C, r == 0x2060, r == 0x00AD, r == 0xFEFF:
			continue // skin tones, variation selectors, ZWJ, zero-width and soft hyphen
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
	return trimParticles(collapse(s))
}

// chineseParticles: sentence-final particles that change the tone, not the
// answer — "可以啊", "好呀", "不要啦" are 可以 / 好 / 不要 (Arbiter
// 2026-10-22 16:51). 吧 and 嘛 are left on: "好吧" / "可以吧" can be
// reluctant or a question, so they are asked again.
var chineseParticles = "啊呀喔哦囉啦"

// trimParticles strips trailing particles after a Chinese character, never
// down to nothing.
func trimParticles(s string) string {
	for {
		rs := []rune(s)
		if len(rs) < 2 || !strings.ContainsRune(chineseParticles, rs[len(rs)-1]) || !unicode.Is(unicode.Han, rs[len(rs)-2]) {
			return s
		}
		s = strings.TrimRightFunc(string(rs[:len(rs)-1]), unicode.IsSpace)
	}
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
	return noWithReasonRE.MatchString(s) && !mixedReason(reasonOf(text))
}

// reasonOf: the part after the first separator, from the cleaned but NOT
// collapsed text ("no, I approve" keeps "approve").
func reasonOf(text string) string {
	c := strings.TrimSpace(clean(text))
	if i := strings.IndexAny(c, ",.!;:，。！"); i >= 0 {
		return strings.TrimSpace(c[i+len(string([]rune(c[i:])[0])):])
	}
	return ""
}

// mixedNo: a no-word with a reason that contradicts it or asks — passed to
// the supporter, the request stays open (R-2: a no is a decline only when
// it is one).
func mixedNo(text string) bool {
	return noWithReasonRE.MatchString(normalise(text)) && mixedReason(reasonOf(text))
}

var affirmingRE = regexp.MustCompile(`\b(go ahead|it's fine|its fine|that's fine|thats fine|do it|fine|ok|okay|sure|yes|yeah|yep|approve|approved)\b|[?]|^(why|what|how)\b`)

// negationRE: a reason that negates is a decline reason ("no, I'm not fine
// with that"), not a contradiction — unless a strong yes sits beside it
// (strongAffirmRE: "no, don't worry, go ahead").
var negationRE = regexp.MustCompile(`\b(not|never|no longer|\w+n't|dont|cant|wont|isnt|arent|didnt|doesnt|shouldnt|couldnt|wouldnt)\b`)
var strongAffirmRE = regexp.MustCompile(`\b(i don'?t mind|go ahead|go for it|do it|approve|approved|yes|yeah|yep|sure|it'?s fine|that'?s fine|sounds good|fine)\b`)

// reassuranceRE: phrases that look negative but reassure — read as "fine".
var reassuranceRE = regexp.MustCompile(`\b(don'?t worry|no worries|not a problem|no problem|never ?mind|nvm)\b`)

// negatedBefore: a not / n't / never within the two words before index i,
// in the same clause (the caller passes only the clause — round 5).
var negatedBefore = regexp.MustCompile(`(\bnot|\bnever|\w+n't|\b(?:dont|cant|wont|isnt|arent|didnt|shouldnt|couldnt|wouldnt))\s+(\S+\s+)?$`)

// clauseStart: where the clause holding index i begins (after the last
// , . ; ! before it).
func clauseStart(r string, i int) int {
	if j := strings.LastIndexAny(r[:i], ",.;!"); j >= 0 {
		return j + 1
	}
	return 0
}

func mixedReason(reason string) bool {
	r := strings.Join(strings.Fields(clean(reason)), " ")
	if strings.ContainsAny(r, "?？") {
		return true // "no, why?" asks: passed on
	}
	r = reassuranceRE.ReplaceAllString(r, "fine")
	for _, m := range strongAffirmRE.FindAllStringIndex(r, -1) {
		if strings.HasPrefix(r[m[0]:m[1]], "i don") || !negatedBefore.MatchString(r[clauseStart(r, m[0]):m[0]]) {
			return true // "no, I don't mind", "no, don't worry, go ahead": contradicts the no
		}
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
func yesWithMessage(text string, supporter ...string) (rest string, reaction, conditional, ok bool) {
	c := clean(text)
	rest, reaction, cond, ok := splitYes(c, supporter...)
	if !ok || yesLikeTail(rest) {
		return "", false, false, false
	}
	return rest, reaction, cond, true
}

// splitYes finds the longest yes-word prefix followed by a separator in a
// cleaned string.
func splitYes(c string, supporter ...string) (rest string, reaction, conditional, ok bool) {
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
	return tail, reaction, tailConditional(tail, supporter...), true
}

// approvalYesWord: is this exact (cleaned) prefix a list word, and which
// kind — without the yes-tail rule (no recursion).
func approvalYesWord(s string) (yes, reaction bool) {
	n := normalise(s)
	return plainYes[n] || reactionYes[n], reactionYes[n] && !plainYes[n]
}

// harmlessTailRE: the ONLY tails that may ride along with an approving
// yes (R-3, I-AE as amended in round 4) — exact whole shapes, matched on
// the cleaned tail with the supporter's first name put in as NAME:
// thanks (to the supporter), tell / let him know, please, hurry. Anything
// else, however harmless it looks, is passed to the supporter with the
// request kept open: the set is closed, not a growing list of exceptions.
var harmlessTailRE = regexp.MustCompile(`^(?:(?:but|and|also|just|oh)\s+)?(?:` + strings.Join([]string{
	`(?:thanks|thank you|thx|ty|cheers)(?:\s+name)?`,
	`please`,
	`(?:please\s+)?hurry(?:\s+up)?`,
	`(?:please\s+)?tell (?:him|her|them|name) (?:to hurry(?: up)?|thanks|thank you)`,
	`(?:please\s+)?let (?:him|her|them|name) know`,
}, "|") + `)$`)

// tailConditional: true unless the whole tail is one of the harmless
// shapes. supporter is the request's supporter's first name, if known.
func tailConditional(tail string, supporter ...string) bool {
	t := normalise(tail)
	if yesLikeTails[t] {
		return false
	}
	c := strings.Join(strings.Fields(clean(tail)), " ")
	c = strings.TrimRightFunc(c, trailingNoise)
	c = strings.Map(func(r rune) rune {
		if strings.ContainsRune(",.;:!", r) {
			return ' '
		}
		return r
	}, c)
	c = strings.Join(strings.Fields(c), " ")
	if rulingMarkerRE.MatchString(c) {
		return true
	}
	if len(supporter) > 0 {
		// Whole-word on spaces, not \b (ASCII-only in RE2): "José", "Zoë"
		// and "王小明" substitute too (round 5).
		if n := strings.ToLower(strings.Join(strings.Fields(clean(supporter[0])), " ")); n != "" && n != "name" {
			w := strings.Fields(" " + c + " ")
			nw := strings.Fields(n)
			out := make([]string, 0, len(w))
			for i := 0; i < len(w); i++ {
				if i+len(nw) <= len(w) && strings.Join(w[i:i+len(nw)], " ") == n {
					out = append(out, "name")
					i += len(nw) - 1
					continue
				}
				out = append(out, w[i])
			}
			c = strings.Join(out, " ")
		}
	}
	return !harmlessTailRE.MatchString(c)
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

// approvalStrictAnswer is I-P's strict YES/NO for an approval question. A
// strict no-word with a particle ("不要啦", "取消啦") is a strict no too
// (Arbiter 2026-10-22 17:14: a decline, not words for the supporter). A
// yes-word with a particle ("是啊") is not made strict without a ruling.
func approvalStrictAnswer(text string) bool {
	// strictForm: particles and trailing ! . , ~ only — not letter
	// collapsing or emoji trimming, so "nooo 😂" is not made strict (r4, r6).
	return IsStrictYesNo(text) || strictNoRE.MatchString(strictForm(text))
}

// strictForm: NFKC and lower case (clean), trailing punctuation and spaces
// removed around the particles — "不要啦！", "不要啦～", "ＮＯ", "no," — but
// no letter collapsing and no emoji trimming ("nooo", "no 😂" stay
// casual). Review r5.
func strictForm(text string) string {
	// Only what normalise trims as trailing noise — never "?" (a question)
	// and never ":)" (review r6).
	punct := func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune("!.,~…！。，、～", r) }
	s := strings.TrimRightFunc(clean(text), punct)
	return strings.TrimRightFunc(trimParticles(s), punct)
}

// approvalCasual is I-P (version B) for an approval question: an answer
// word that is not a strict YES/NO goes to a supporter who wrote in the
// last minutes. The router's gate (HandleInbound) and DecideApproval both
// use it — one rule, one place (review r4).
func approvalCasual(text string) bool {
	return approvalAnswerish(text) && !approvalStrictAnswer(text)
}
