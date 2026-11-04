package channel

import (
	"regexp"
	"strconv"
	"strings"
)

// Extra-time acknowledgements (production 2026-11-03 15:56 NY). The summary
// says "up to 15 extra minutes are OK without asking"; the person answered
// "15 mins is fine" and DurationOnly read it as a new length: the errand
// went from about 30 to about 15 minutes and $19.50 to $12. A message that
// only acknowledges that line changes nothing: the draft is kept and the
// plain YES asked for (CopyExtraTimeAck). The length changes only when the
// words clearly give one for the errand ("make it 15 minutes", "it only
// takes 15 min") — those carry a length word, so they are never this.

// extraTimeAckWordRE: the agreeing part.
var extraTimeAckWordRE = regexp.MustCompile(`(?i)^(?:fine|ok|okay|k|good|great|works|perfect|sure|alright|cool|no problem|np|all good|sounds good|👍|👌)$`)

// extraTimeLengthRE: words that make it a length for the errand, not an
// answer to the extra-time line.
var extraTimeLengthRE = regexp.MustCompile(`(?i)\b(?:make it|takes?|took|taking|only|just|should|will|it'?ll|need|needs|change|set|instead|long|total|max|maximum|tops|at most|about|around|approx(?:imately)?|roughly|for)\b`)

// extraTimeFillerRE: what may sit around the acknowledgement.
var extraTimeFillerRE = regexp.MustCompile(`(?i)\b(?:the|a|is|are|be|it'?s|its|that'?s|thats|that|those|these|yes|yeah|yep|up|to|by|me|with|thanks|thank|you|thx|ty|of|over|also|and|too|totally|completely|absolutely|really)\b`)

var extraTimeNumberRE = regexp.MustCompile(`(?i)\b(\d{1,3})\s*(?:m|mins?|minutes?)?\b|\b(fifteen|ten|twenty|thirty)\b`)

var extraTimeUnitRE = regexp.MustCompile(`(?i)\b(?:m|mins?|minutes?)\b`)

var extraTimeExtraRE = regexp.MustCompile(`(?i)\b(?:extra|more|additional|overtime|over-?run|over time)\b`)

var wordNumbers = map[string]int{"ten": 10, "fifteen": 15, "twenty": 20, "thirty": 30}

// IsExtraTimeAck reports whether text only agrees to the summary's
// extra-time line of extraMinutes: an agreeing word, the line's own figure
// or the word "extra", and nothing else ("15 mins is fine", "extra time is
// ok", "the 15 extra minutes are fine 👍"). "15 mins" alone, "20 mins is
// fine", "make it 15 minutes" and "it only takes 15 min" are not.
func IsExtraTimeAck(text string, extraMinutes int) bool {
	if extraMinutes <= 0 {
		return false
	}
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" || extraTimeLengthRE.MatchString(t) {
		return false
	}
	named := extraTimeExtraRE.MatchString(t)
	for _, m := range extraTimeNumberRE.FindAllStringSubmatch(t, -1) {
		n := wordNumbers[m[2]]
		if m[1] != "" {
			n, _ = strconv.Atoi(m[1])
		}
		if n != extraMinutes {
			return false // another figure: maybe a length; not this
		}
		named = true
	}
	if !named {
		return false
	}
	rest := extraTimeNumberRE.ReplaceAllString(t, " ")
	rest = extraTimeExtraRE.ReplaceAllString(rest, " ")
	rest = extraTimeUnitRE.ReplaceAllString(rest, " ")
	rest = strings.NewReplacer("time", " ", "no problem", "np", "all good", "fine", "sounds good", "fine").Replace(rest)
	rest = extraTimeFillerRE.ReplaceAllString(rest, " ")
	rest = strings.Map(func(r rune) rune {
		if strings.ContainsRune(".,!?;:~-()'\"", r) {
			return ' '
		}
		return r
	}, rest)
	agreed := false
	for _, w := range strings.Fields(rest) {
		if !extraTimeAckWordRE.MatchString(w) {
			return false
		}
		agreed = true
	}
	return agreed
}
