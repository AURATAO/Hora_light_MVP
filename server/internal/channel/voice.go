package channel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"

	"hora-auth/internal/channel/llm"
	"hora-auth/internal/tracing"
)

// The voice layer (D-28, behind CHANNEL_VOICE=1). After the router has
// decided WHAT to say, Router.Voice (Sonnet, the small step) rewrites the
// free words of a customer-facing reply in HO:RA's voice
// (llm/voice.md). It decides nothing:
//
//  1. Whole messages that must go word for word are never sent: supporter
//     relays, ops' own words, a supporter's request (what a YES approves),
//     setup links, photos, the booking summary, STOP/START/HELP, scope
//     refusals, anything citing Google Maps or a web page, and the copies
//     other code reads back from the thread by their exact text.
//  2. In the rest, fixed spans are masked as ⟦n⟧ before the call, so the
//     model never sees them: the YES / reply instructions, "cancel task",
//     the Terms, links and emails, anything with a price, numbered lists,
//     the "Want …?" offers a YES answers, quoted titles, and every value
//     filled into a Copy* format (voiceFormats).
//  3. The rewrite is checked by rule (CheckVoice) against the original.
//     Any failure, a model error, or no answer within VoiceTimeout sends
//     the original text unchanged. The reason is logged and put on the
//     channel.voice span; no message text is.

// DefaultVoiceTimeout is how long a reply may wait for its rewrite: 2.5 s.
// Measured on Sonnet 5.5 (agenttest -voice, D-28, 2026-11-11): one run of
// 72 rewrites had a median of 1.34 s, a p95 of 1.90 s and a max of 2.48 s;
// an earlier run the same hour was slower (median 1.9 s, 24 of 70 over 2 s).
// At 2 s, a third of the replies would have gone out in the plain voice on
// a slow hour; at 2.5 s, 0 of 72 and 6 of 70. Any longer and a slow model is
// felt on every reply, which the voice cannot be worth. The cost of the
// limit is a few replies in the plain copy, never a wrong one.
// CHANNEL_VOICE_TIMEOUT overrides.
const DefaultVoiceTimeout = 2500 * time.Millisecond

// Voice outcomes on the channel.voice span and in the log.
const (
	VoiceRewritten = "rewritten"
	VoiceFallback  = "fallback"
	VoiceFixed     = "fixed" // never sent to the model
)

func (r *Router) voiceTimeout() time.Duration {
	if r.VoiceTimeout > 0 {
		return r.VoiceTimeout
	}
	return DefaultVoiceTimeout
}

// customerWordsKey carries the person's latest message to the voice layer
// (tone and language only).
type customerWordsKey struct{}

func withCustomerWords(ctx context.Context, text string) context.Context {
	return context.WithValue(ctx, customerWordsKey{}, text)
}

func customerWords(ctx context.Context) string {
	s, _ := ctx.Value(customerWordsKey{}).(string)
	return s
}

// VoiceResult is what the voice layer did with one reply.
type VoiceResult struct {
	Text     string // what to send
	Outcome  string // VoiceRewritten | VoiceFallback | VoiceFixed
	Reason   string // why fixed / why it fell back
	Language string
	Latency  time.Duration
	Blocks   int
	// Rejected is the model's rewrite that failed the check — for the eval
	// only; it is never logged, traced or sent.
	Rejected string
}

// voiceText is the text to send for out: the rewrite when the voice is on
// and every rule holds, else out.Text unchanged.
func (r *Router) voiceText(ctx context.Context, out Outbound) string {
	if r.Voice == nil {
		return out.Text
	}
	_, sp := tracing.StartSpan(ctx, "channel.voice", tracing.KindChain)
	res := r.VoiceRewrite(ctx, out)
	sp.Attr(
		attribute.String("hora.voice.outcome", res.Outcome),
		attribute.String("hora.voice.reason", res.Reason),
		attribute.Int64("hora.voice.latency_ms", res.Latency.Milliseconds()),
		attribute.String("hora.voice.language", res.Language),
		attribute.Int("hora.voice.blocks", res.Blocks),
		attribute.Int("hora.voice.chars_in", utf8.RuneCountInString(out.Text)),
		attribute.Int("hora.voice.chars_out", utf8.RuneCountInString(res.Text)),
		attribute.String("hora.voice.prompt_version", llm.VoicePromptVersion),
		attribute.String("hora.channel", string(out.Channel)),
	)
	sp.End()
	if res.Outcome == VoiceFallback {
		log.Printf("[channel] voice fallback channel=%s reason=%s latency_ms=%d", out.Channel, res.Reason, res.Latency.Milliseconds())
	}
	return res.Text
}

// VoiceRewrite runs the voice layer on one reply (exported for the eval).
func (r *Router) VoiceRewrite(ctx context.Context, out Outbound) VoiceResult {
	orig := out.Text
	if reason := r.voiceFixedReason(out); reason != "" {
		return VoiceResult{Text: orig, Outcome: VoiceFixed, Reason: reason}
	}
	masked, blocks := MaskForVoice(orig)
	if !hasFreeWords(masked) {
		return VoiceResult{Text: orig, Outcome: VoiceFixed, Reason: "nothing_free", Blocks: len(blocks)}
	}
	cctx, cancel := context.WithTimeout(ctx, r.voiceTimeout())
	defer cancel()
	start := time.Now()
	got, err := r.Voice.Voice(cctx, llm.VoiceInput{Text: masked, Customer: customerWords(ctx)})
	res := VoiceResult{Text: orig, Outcome: VoiceFallback, Latency: time.Since(start), Blocks: len(blocks), Language: got.Language}
	switch {
	case err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, llm.ErrTimeout) || cctx.Err() != nil):
		res.Reason = "timeout"
		return res
	case err != nil:
		res.Reason = "error"
		return res
	}
	restored, ok := unmaskVoice(got.Text, len(blocks), blocks)
	if !ok {
		res.Reason, res.Rejected = "tokens", got.Text
		return res
	}
	if reason := CheckVoice(orig, restored, blocks, got.Language); reason != "" {
		res.Reason, res.Rejected = reason, restored
		return res
	}
	res.Text, res.Outcome = restored, VoiceRewritten
	if restored == orig {
		res.Reason = "unchanged"
	}
	return res
}

// ── 1. Whole messages that never reach the model ───────────────────────────

// voiceReadBack: copies other code finds again in the thread by their exact
// text (lastReplyWasParseFailure, lastReply, the discovery offer). A
// rewrite would break the match.
var voiceReadBack = []string{CopyParseFailed, CopySupporterAlready, CopyLookingUp, CopyDiscoveryFooter}

// voiceTCPA: the carrier-keyword replies (opt-in, HELP) and the copies that
// stand for a person or a safety decision.
var voiceTCPA = []string{CopyHelpInfo, CopyOptInWelcome}

func (r *Router) voiceFixedReason(out Outbound) string {
	t := out.Text
	switch {
	case out.Relay:
		return "relay"
	case out.Verbatim:
		return "verbatim"
	case out.MediaURL != "":
		return "media"
	case out.StoredText != "":
		return "setup_link"
	case strings.TrimSpace(t) == "":
		return "empty"
	}
	for _, c := range voiceTCPA {
		if strings.Contains(t, c) {
			return "tcpa"
		}
	}
	if isScopeReply(t) {
		return "scope"
	}
	for _, c := range voiceReadBack {
		if strings.Contains(t, c) {
			return "read_back"
		}
	}
	switch {
	case strings.Contains(t, "Estimate:") || strings.Contains(t, "By booking you agree"):
		return "summary"
	case strings.Contains(t, "Google Maps") || voiceCiteRE.MatchString(t):
		return "cited"
	case strings.Contains(t, r.applyURL()):
		return "supporter_link"
	}
	return ""
}

// voiceCiteRE: a web answer's citation line ("From joespizzanyc.com/menu").
var voiceCiteRE = regexp.MustCompile(`(?m)^From [\w.-]+\.[a-z]{2,}\S*$`)

var (
	scopeRepliesOnce sync.Once
	scopeReplies     []string
)

// isScopeReply: a scope refusal's fixed reply, or the holding line sent
// when the scope check could not run.
func isScopeReply(t string) bool {
	scopeRepliesOnce.Do(func() {
		scopeReplies = []string{CopyScopeHolding}
		if s := scopeRules(); s != nil {
			for _, ref := range s.Refusals {
				if strings.TrimSpace(ref.Reply) != "" {
					scopeReplies = append(scopeReplies, ref.Reply)
				}
			}
		}
	})
	for _, c := range scopeReplies {
		if strings.Contains(t, c) {
			return true
		}
	}
	return false
}

// ── 2. Masking fixed spans ─────────────────────────────────────────────────

const (
	voiceTokOpen  = "⟦"
	voiceTokClose = "⟧"
)

func voiceToken(i int) string { return fmt.Sprintf("%s%d%s", voiceTokOpen, i, voiceTokClose) }

// A sentence is fixed when it carries any of these.
var (
	// The words a person answers with, and carrier keywords.
	voiceKeywordRE = regexp.MustCompile(`\b(?:YES|NO|STOP|START|HELP|INFO|SKIP|LAST)\b`)
	voiceFixedRE   = regexp.MustCompile(`(?i)cancel task|\bterms\b|\bestimate\b|\bcharg|\bfees?\b|\bpa(?:y|id)\b|\bholds?\b|\brefund|\bcard\b|google maps|\breply\b|\$|@|https?://|www\.|\b[\w-]+\.(?:com|co|org|net|app|io|nyc|us)\b`)
	voiceOfferRE   = regexp.MustCompile(`^\s*Want\b[^?]*\?`)
	// A line that is a list item, a summary line, or a parenthesised footer.
	voiceLineRE = regexp.MustCompile(`^\s*(?:\d+[.)]\s|\(|.*[·→])`)
	// Quoted titles and words: “…” and "…".
	voiceQuoteRE = regexp.MustCompile(`“[^”\n]*”|"[^"\n]*"`)
)

type span struct{ a, b int }

// MaskForVoice replaces the fixed spans of text with ⟦1⟧, ⟦2⟧, … and
// returns the masked text and the blocks, in order.
func MaskForVoice(text string) (string, []string) {
	var spans []span
	// Whole lines.
	off := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		body := strings.TrimRight(line, "\n")
		if voiceLineRE.MatchString(body) && strings.TrimSpace(body) != "" {
			spans = append(spans, span{off, off + len(body)})
		} else {
			for _, s := range sentences(body) {
				seg := body[s.a:s.b]
				if voiceKeywordRE.MatchString(seg) || voiceFixedRE.MatchString(seg) || voiceOfferRE.MatchString(seg) {
					spans = append(spans, span{off + s.a, off + s.b})
				}
			}
		}
		off += len(line)
	}
	for _, m := range voiceQuoteRE.FindAllStringIndex(text, -1) {
		spans = append(spans, span{m[0], m[1]})
	}
	spans = append(spans, formatValueSpans(text)...)
	spans = mergeSpans(spans)
	var b strings.Builder
	var blocks []string
	last := 0
	for _, s := range spans {
		b.WriteString(text[last:s.a])
		blocks = append(blocks, text[s.a:s.b])
		b.WriteString(voiceToken(len(blocks)))
		last = s.b
	}
	b.WriteString(text[last:])
	return b.String(), blocks
}

// sentences splits one line into sentence spans (trailing spaces belong to
// the sentence before). A period inside a word ("my-hora.com", "e.g.") is
// not an end unless a space follows.
func sentences(line string) []span {
	var out []span
	start := 0
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c != '.' && c != '!' && c != '?' {
			continue
		}
		j := i + 1
		for j < len(line) && (line[j] == '.' || line[j] == '!' || line[j] == '?' || line[j] == ')') {
			j++
		}
		if j < len(line) && line[j] != ' ' {
			continue
		}
		for j < len(line) && line[j] == ' ' {
			j++
		}
		trimEnd := j
		for trimEnd > start && line[trimEnd-1] == ' ' {
			trimEnd--
		}
		out = append(out, span{start, trimEnd})
		start = j
		i = j - 1
	}
	if start < len(line) {
		out = append(out, span{start, len(line)})
	}
	return out
}

func mergeSpans(s []span) []span {
	if len(s) == 0 {
		return nil
	}
	sort.Slice(s, func(i, j int) bool { return s[i].a < s[j].a || (s[i].a == s[j].a && s[i].b > s[j].b) })
	out := []span{s[0]}
	for _, x := range s[1:] {
		l := &out[len(out)-1]
		if x.a <= l.b {
			if x.b > l.b {
				l.b = x.b
			}
			continue
		}
		out = append(out, x)
	}
	return out
}

// formatValueSpans: the values filled into any Copy* format that appears
// in text — a draft's title, an address, a supporter's name, a count.
func formatValueSpans(text string) []span {
	var out []span
	for _, re := range voiceFormatREs() {
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			for g := 2; g+1 < len(m); g += 2 {
				if m[g] >= 0 && m[g+1] > m[g] {
					out = append(out, span{m[g], m[g+1]})
				}
			}
		}
	}
	return out
}

var (
	voiceFormatOnce sync.Once
	voiceFormatRx   []*regexp.Regexp
	fmtVerbRE       = regexp.MustCompile(`%(?:\[\d+\])?[-+# 0]*\d*(?:\.\d+)?[sdvfq]`)
)

// voiceFormatREs compiles each line of each format with a verb into a
// regexp whose groups are the values. A line with under six literal
// characters ("%s") would match anything and is skipped.
func voiceFormatREs() []*regexp.Regexp {
	voiceFormatOnce.Do(func() {
		for _, f := range voiceFormats {
			for _, piece := range strings.Split(f, "\n") {
				locs := fmtVerbRE.FindAllStringIndex(piece, -1)
				if len(locs) == 0 {
					continue
				}
				literal := len(fmtVerbRE.ReplaceAllString(piece, ""))
				if literal < 6 {
					continue
				}
				var b strings.Builder
				last := 0
				for i, l := range locs {
					b.WriteString(regexp.QuoteMeta(strings.ReplaceAll(piece[last:l[0]], "%%", "%")))
					if i == len(locs)-1 && l[1] == len(piece) {
						b.WriteString(`([^\n]+)`)
					} else {
						b.WriteString(`([^\n]+?)`)
					}
					last = l[1]
				}
				b.WriteString(regexp.QuoteMeta(strings.ReplaceAll(piece[last:], "%%", "%")))
				voiceFormatRx = append(voiceFormatRx, regexp.MustCompile(b.String()))
			}
		}
	})
	return voiceFormatRx
}

func hasFreeWords(masked string) bool {
	t := voiceTokRE.ReplaceAllString(masked, "")
	n := 0
	for _, c := range t {
		if unicode.IsLetter(c) {
			n++
		}
	}
	return n >= 3
}

var voiceTokRE = regexp.MustCompile(`⟦(\d+)⟧`)

// unmaskVoice puts the blocks back: each token exactly once, nothing else
// in brackets.
func unmaskVoice(text string, n int, blocks []string) (string, bool) {
	seen := make([]int, n+1)
	for _, m := range voiceTokRE.FindAllStringSubmatch(text, -1) {
		var i int
		if _, err := fmt.Sscan(m[1], &i); err != nil || i < 1 || i > n {
			return "", false
		}
		seen[i]++
	}
	for i := 1; i <= n; i++ {
		if seen[i] != 1 {
			return "", false
		}
	}
	if strings.Count(text, voiceTokOpen) != n || strings.Count(text, voiceTokClose) != n {
		return "", false
	}
	out := voiceTokRE.ReplaceAllStringFunc(text, func(tok string) string {
		var i int
		fmt.Sscan(strings.Trim(tok, voiceTokOpen+voiceTokClose), &i)
		return blocks[i-1]
	})
	return out, true
}

// ── 3. The rule check ──────────────────────────────────────────────────────

var (
	voiceNumberRE = regexp.MustCompile(`\d+(?:[.,:]\d+)*`)
	voiceTimeRE   = regexp.MustCompile(`\b\d{1,2}(?::\d{2})?\s?(?:AM|PM|am|pm|a\.m\.|p\.m\.)`)
	voiceURLRE    = regexp.MustCompile(`(?i)https?://\S+|www\.\S+|\b[\w.-]+@[\w-]+\.[\w.]+|\b[\w-]+(?:\.[\w-]+)*\.(?:com|co|org|net|app|io|nyc|us)\b\S*`)
	voicePhoneRE  = regexp.MustCompile(`\+?\d[\d\s().-]{8,}\d`)
	voiceNameRE   = regexp.MustCompile(`\b[A-Z][A-Za-z0-9'’&.:-]*[A-Za-z0-9]|\b[A-Z]\b`)
	voiceAIRE     = regexp.MustCompile(`(?i)\bas an ai\b|\blanguage model\b|\bi'?m (?:just )?(?:a|an) (?:bot|ai)\b`)
	// English words that would change what was decided or promised.
	voiceCommitRE  = regexp.MustCompile(`(?i)\b(?:book(?:ed|ing)?|confirm(?:ed)?|cancel(?:led|ed)?|refund(?:ed)?|free|guarantee[ds]?|promise[ds]?|charge[ds]?|discount|waive[ds]?|minutes?|hours?|asap|right away|immediately)\b`)
	voiceNumWordRE = regexp.MustCompile(`(?i)\b(?:one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|fifteen|twenty|thirty|forty|fifty|hundred|half|dozen|couple)\b`)
)

// CheckVoice is the rule check on a rewrite (restored: blocks back in).
// It returns "" when the rewrite may be sent, else the first reason it may
// not. lang is the language the model says it wrote.
func CheckVoice(original, rewritten string, blocks []string, lang string) string {
	if strings.TrimSpace(rewritten) == "" {
		return "empty"
	}
	// Every fixed block, unchanged, in order.
	pos := 0
	for _, b := range blocks {
		i := strings.Index(rewritten[pos:], b)
		if i < 0 {
			return "block_missing"
		}
		pos += i + len(b)
	}
	// Times verbatim, then every number kept and none added.
	for _, t := range voiceTimeRE.FindAllString(original, -1) {
		if strings.Count(rewritten, t) < strings.Count(original, t) {
			return "time_changed"
		}
	}
	on, rn := countAll(voiceNumberRE, original), countAll(voiceNumberRE, rewritten)
	for k, n := range on {
		if rn[k] < n {
			if strings.Contains(original, "$"+k) {
				return "price_changed"
			}
			return "number_dropped"
		}
	}
	for k, n := range rn {
		if on[k] < n {
			return "number_added"
		}
	}
	ou, ru := countAll(voiceURLRE, original), countAll(voiceURLRE, rewritten)
	for k := range ru {
		if ou[k] == 0 {
			return "url_added"
		}
	}
	for k := range ou {
		if ru[k] == 0 {
			return "url_dropped"
		}
	}
	if len(voicePhoneRE.FindAllString(rewritten, -1)) > len(voicePhoneRE.FindAllString(original, -1)) {
		return "phone_added"
	}
	if questions(rewritten) > questions(original) {
		return "question_added"
	}
	if len(voiceKeywordRE.FindAllString(rewritten, -1)) > len(voiceKeywordRE.FindAllString(original, -1)) {
		return "keyword_added"
	}
	if strings.Count(rewritten, "$") > strings.Count(original, "$") {
		return "price_added"
	}
	for name := range voiceNames(original) {
		if !containsWord(rewritten, name) {
			return "name_dropped"
		}
	}
	if voiceAIRE.MatchString(rewritten) && !voiceAIRE.MatchString(original) {
		return "as_an_ai"
	}
	if strings.Contains(rewritten, "!!") || strings.Count(rewritten, "!") > max(1, strings.Count(original, "!")) {
		return "exclamation"
	}
	oe, re := emojiCount(original), emojiCount(rewritten)
	if re > max(1, oe) {
		return "emoji"
	}
	english := lang == "" || lang == "en"
	if english {
		if len(voiceCommitRE.FindAllString(rewritten, -1)) > len(voiceCommitRE.FindAllString(original, -1)) {
			return "commitment_added"
		}
		if len(voiceNumWordRE.FindAllString(rewritten, -1)) > len(voiceNumWordRE.FindAllString(original, -1)) {
			return "number_added"
		}
	}
	// Same length or shorter; a 10% margin (at least 10 characters) for a
	// warmer word. Another language may run longer than English.
	ol, rl := utf8.RuneCountInString(original), utf8.RuneCountInString(rewritten)
	if english && rl > ol+max(10, ol/10) || !english && rl > ol*8/5+10 {
		return "too_long"
	}
	return ""
}

func countAll(re *regexp.Regexp, s string) map[string]int {
	m := map[string]int{}
	for _, x := range re.FindAllString(s, -1) {
		m[strings.TrimRight(x, ".,;:!?)")]++
	}
	return m
}

func questions(s string) int { return strings.Count(s, "?") + strings.Count(s, "？") }

func emojiCount(s string) int {
	n := 0
	for _, c := range s {
		if (c >= 0x1F300 && c <= 0x1FAFF) || (c >= 0x2600 && c <= 0x27BF) || (c >= 0x1F000 && c <= 0x1F2FF) {
			n++
		}
	}
	return n
}

func containsWord(s, w string) bool {
	re, err := regexp.Compile(`(?:^|[^\p{L}\p{N}])` + regexp.QuoteMeta(w) + `(?:$|[^\p{L}\p{N}])`)
	if err != nil {
		return strings.Contains(s, w)
	}
	return re.MatchString(s)
}

// voiceStarters: capitalised only because they start a sentence. Any other
// capitalised word is a name (a place, a supporter, a borough, HO:RA, a
// day) and must survive the rewrite.
var voiceStarters = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`A All Also An And Any Are As At Back Be Before Booked Booking But By Can Could Do Done Don't For From Get Give Go Going Got Great Have Here Here's Hi How I I'd I'll I'm I've If In Is It It's Its Just Let Let's Looks Need No Nothing Now OK Okay On Once One Or Our Please Passed Right Since So Something Sorry Still Sure Tell Thank Thanks That That's The Their There There's These They This To Unfortunately Until We We'll We're Welcome What What's When Where Which While Who Will With Would You You'll You're Your Yes Approved Declined Note Place Stops Receipt Purchases Time Total Photo Shopping Just Before Booked Got Hmm Absolutely Of Perfect Noted Sounds Happy Alright Understood Certainly Gotcha About Roughly Around Mind Happy Totally Afraid Quick Small`) {
		voiceStarters[w] = true
	}
}

// voiceNames are the capitalised words of the original that are names.
func voiceNames(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range voiceNameRE.FindAllString(s, -1) {
		w = strings.TrimRight(w, ".:")
		if voiceStarters[w] || w == "" {
			continue
		}
		if strings.HasPrefix(w, "I'") || strings.HasPrefix(w, "I’") {
			continue
		}
		out[w] = true
	}
	return out
}

// WithCustomerWordsForEval sets the person's latest message on ctx as
// HandleInbound does (cmd/agenttest -voice).
func WithCustomerWordsForEval(ctx context.Context, text string) context.Context {
	return withCustomerWords(ctx, text)
}
