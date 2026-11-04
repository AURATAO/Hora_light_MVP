package channel

import (
	"regexp"
	"strconv"
	"strings"
)

// The person's own words are the only source of a money input (gap C,
// 2026-11-04). With several errands in one message, each errand's text is
// the splitter's copy of it, and the parser reads the duration from that
// copy: a model that invents or mistypes a figure ("$400" for "$40", "2
// hours" for "20 minutes") would otherwise set the shopping budget, and so
// the hold, with only the summary standing between it and the card. So an
// amount or a duration read from the model's text is used only when the same
// figure appears in what the person wrote, in any format ("$40", "40
// dollars", "40 usd", "forty"). Otherwise it is dropped: the budget question
// is asked, and the duration falls back to the default estimate.

// saidAmountRE: a dollar figure in the person's words. $40, 40$, 40 dollars,
// 40 bucks, 40 usd, budget 40, up to 40, spend 40.
var saidAmountRE = regexp.MustCompile(`(?i)\$\s?(\d{1,3}(?:,\d{3})+|\d+)(?:\.\d{1,2})?|\b(\d{1,3}(?:,\d{3})+|\d+)(?:\.\d{1,2})?\s?(?:\$|dollars?\b|bucks?\b|usd\b)|\b(?:budget|up to|max|maximum|limit|spend|spending|under|at most|no more than|less than)(?: of| is|:)?\s?(\d{1,3}(?:,\d{3})+|\d+)\b`)

// saidAmount reports whether the person's words name dollars.
func saidAmount(source string, dollars int) bool {
	if dollars <= 0 {
		return false
	}
	for _, m := range saidAmountRE.FindAllStringSubmatchIndex(source, -1) {
		for g := 1; g <= 3; g++ {
			if m[2*g] < 0 {
				continue
			}
			if g == 3 && timeUnitAfterRE.MatchString(source[m[1]:]) {
				break // "up to 15 mins" is a time
			}
			if n, err := strconv.Atoi(strings.ReplaceAll(source[m[2*g]:m[2*g+1]], ",", "")); err == nil && n == dollars {
				return true
			}
		}
	}
	for _, w := range wordNumbersIn(source) {
		if w.value == dollars && !clockOrTimeAfterRE.MatchString(source[w.end:]) {
			return true
		}
	}
	return false
}

// saidNoBudget: a "nothing to buy" in the person's words, on any line.
func saidNoBudget(source string) bool {
	for _, l := range strings.Split(source, "\n") {
		if IsNoBudget(l) {
			return true
		}
	}
	return false
}

// clockOrTimeAfterRE: what makes a number a time rather than dollars.
var clockOrTimeAfterRE = regexp.MustCompile(`(?i)^\s*(?:h|hr|hrs|hours?|m|mins?|minutes?|seconds?|am|pm|a\.m\.|p\.m\.|o'?clock)\b|^\s*(?:分|小時|個小時|鐘頭)`)

// Durations in the person's words. Each pattern's figure is converted to
// minutes; an hours figure followed by a minutes one ("1 hour 30 min",
// "1h30") also counts as their sum.
var (
	saidHoursRE   = regexp.MustCompile(`(?i)\b(\d+(?:\.\d+)?)\s*(?:h|hr|hrs|hours?)\b`)
	saidMinutesRE = regexp.MustCompile(`(?i)\b(\d+)\s*(?:m|mins?|minutes?)\b`)
	saidHnMRE     = regexp.MustCompile(`(?i)\b(\d+)\s*(?:h|hr|hrs|hours?)\s*(?:and\s*)?(\d{1,2})\s*(?:m|mins?|minutes?)?\b`)
	saidCJKMinRE  = regexp.MustCompile(`([\d一二三四五六七八九十兩]+)\s*分鐘?`)
	saidCJKHourRE = regexp.MustCompile(`([\d一二三四五六七八九十兩]+)\s*個?(?:小時|鐘頭)`)
	// Phrases with a fixed length.
	saidPhrases = []struct {
		re      *regexp.Regexp
		minutes int
	}{
		{regexp.MustCompile(`(?i)\b(?:an?|one)\s+hour\s+and\s+a\s+half\b|\b(?:one\s+and\s+a\s+half|1\s+and\s+a\s+half)\s+hours?\b`), 90},
		{regexp.MustCompile(`(?i)\bhalf\s+(?:an?\s+)?hour\b|半小時|半個小時`), 30},
		{regexp.MustCompile(`(?i)\b(?:an?|one)\s+hour\b|一個?小時`), 60},
		{regexp.MustCompile(`(?i)\ba\s+couple\s+(?:of\s+)?hours\b`), 120},
		{regexp.MustCompile(`(?i)\bquarter\s+(?:of\s+an\s+)?hour\b`), 15},
		{regexp.MustCompile(`(?i)\bhalf\s+a\s+day\b|\bhalf-day\b`), 240},
	}
	wordUnitAfterRE = regexp.MustCompile(`(?i)^[\s-]*(?:(h|hr|hrs|hours?)|(m|mins?|minutes?))\b`)
)

// saidMinutesIn reports whether the person's words give a length of minutes.
func saidMinutesIn(source string, minutes int) bool {
	if minutes <= 0 {
		return false
	}
	for _, v := range durationsIn(source) {
		if v == minutes {
			return true
		}
	}
	return false
}

// durationsIn lists every length, in minutes, the text names.
func durationsIn(text string) []int {
	var out []int
	for _, m := range saidHoursRE.FindAllStringSubmatch(text, -1) {
		if f, err := strconv.ParseFloat(m[1], 64); err == nil {
			out = append(out, int(f*60+0.5))
		}
	}
	for _, m := range saidMinutesRE.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			out = append(out, n)
		}
	}
	for _, m := range saidHnMRE.FindAllStringSubmatch(text, -1) {
		h, _ := strconv.Atoi(m[1])
		mm, _ := strconv.Atoi(m[2])
		out = append(out, h*60+mm)
	}
	for _, m := range saidCJKMinRE.FindAllStringSubmatch(text, -1) {
		if n, ok := cjkNumber(m[1]); ok {
			out = append(out, n)
		}
	}
	for _, m := range saidCJKHourRE.FindAllStringSubmatch(text, -1) {
		if n, ok := cjkNumber(m[1]); ok {
			out = append(out, n*60)
		}
	}
	for _, p := range saidPhrases {
		if p.re.MatchString(text) {
			out = append(out, p.minutes)
		}
	}
	for _, w := range wordNumbersIn(text) {
		u := wordUnitAfterRE.FindStringSubmatch(text[w.end:])
		switch {
		case u == nil:
		case u[1] != "":
			out = append(out, w.value*60)
		default:
			out = append(out, w.value)
		}
	}
	return out
}

// wordNumber is a number written in words, and where it ends in the text.
type wordNumber struct {
	value int
	end   int
}

var numberWordValues = map[string]int{
	"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7,
	"eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13,
	"fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17, "eighteen": 18,
	"nineteen": 19, "twenty": 20, "thirty": 30, "forty": 40, "fourty": 40, "fifty": 50,
	"sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90,
}

var wordTokenRE = regexp.MustCompile(`[A-Za-z]+`)

// wordNumbersIn reads numbers written in words: "forty", "forty-five",
// "twenty five", "a hundred", "one hundred and twenty".
func wordNumbersIn(text string) []wordNumber {
	var out []wordNumber
	toks := wordTokenRE.FindAllStringIndex(text, -1)
	total, cur, seen, end := 0, 0, false, 0
	flush := func() {
		if seen {
			out = append(out, wordNumber{value: total + cur, end: end})
		}
		total, cur, seen = 0, 0, false
	}
	for i, t := range toks {
		w := strings.ToLower(text[t[0]:t[1]])
		// Only spaces or a hyphen may sit between the words of one number.
		if seen && strings.Trim(text[end:t[0]], " -") != "" {
			flush()
		}
		next := ""
		if i+1 < len(toks) {
			next = strings.ToLower(text[toks[i+1][0]:toks[i+1][1]])
		}
		switch v, ok := numberWordValues[w]; {
		case ok:
			if seen && cur%10 == 0 && cur%100 != 0 && v < 10 {
				cur += v // "forty five"
			} else if seen && cur != 0 && cur%100 != 0 {
				flush() // "five ten": two numbers
				cur, seen = v, true
			} else {
				cur += v
				seen = true
			}
			end = t[1]
		case w == "hundred":
			if !seen {
				cur = 1
			}
			cur *= 100
			seen, end = true, t[1]
		case w == "thousand":
			if !seen {
				cur = 1
			}
			total += cur * 1000
			cur, seen, end = 0, true, t[1]
		case w == "a" && !seen && (next == "hundred" || next == "thousand"):
			// "a hundred": the next word sets it
		case w == "and" && seen && numberWordValues[next] > 0:
			end = t[1] // "one hundred and twenty"
		default:
			flush()
		}
	}
	flush()
	return out
}

// cjkNumber reads 15, 十五, 二十, 兩.
func cjkNumber(s string) (int, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, true
	}
	digits := map[rune]int{'一': 1, '二': 2, '兩': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	total, cur := 0, 0
	for _, r := range s {
		switch {
		case r == '十':
			if cur == 0 {
				cur = 1
			}
			total += cur * 10
			cur = 0
		case digits[r] > 0:
			cur = digits[r]
		default:
			return 0, false
		}
	}
	total += cur
	return total, total > 0
}
