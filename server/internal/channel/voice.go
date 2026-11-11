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

// The voice layer (D-28, behind CHANNEL_VOICE=1). Warm English lives in the
// copy itself (copy.go); at runtime Router.Voice (Sonnet, the small step)
// rewrites a reply only when the customer needs something the copy cannot
// give (voiceTrigger):
//
//   - language: their latest message is not in English (rules:
//     DetectLanguage) — the reply is written in it, Chinese in the script
//     they used;
//   - stressed: they sound stressed or frustrated (rules: StressedBy; or
//     the agent's own escalation of this message, when it was asked
//     anyway) — the reply may open with one short acknowledgement.
//
// Every other reply is sent as written: no model call, no added latency.
// When it does run, it decides nothing:
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
//     the original text unchanged. The trigger and the reason are logged
//     and put on the channel.voice span; no message text is.

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
	VoiceFixed     = "fixed"   // never sent to the model
	VoiceSkipped   = "skipped" // no trigger: English, calm — sent as written
)

// Voice triggers.
const (
	VoiceTriggerLanguage = "language"
	VoiceTriggerStressed = "stressed"
)

func (r *Router) voiceTimeout() time.Duration {
	if r.VoiceTimeout > 0 {
		return r.VoiceTimeout
	}
	return DefaultVoiceTimeout
}

// voiceSignal is what the voice layer knows about the customer for this
// reply: their latest words, and whether anything already asked about the
// message (the agent) found them upset. Shared by pointer down the ctx so
// a later step can mark it.
type voiceSignal struct {
	mu    sync.Mutex
	words string
	// fromThread: the words are the thread's last inbound, not a message
	// this reply answers (a status update) — good for the language only.
	fromThread bool
	stressBy   string // set by a model step that was asked anyway
}

type voiceSignalKey struct{}

func withCustomerWords(ctx context.Context, text string) context.Context {
	return context.WithValue(ctx, voiceSignalKey{}, &voiceSignal{words: text})
}

// withThreadWords: the language of the thread, for a reply that answers
// no message of theirs.
func withThreadWords(ctx context.Context, text string) context.Context {
	return context.WithValue(ctx, voiceSignalKey{}, &voiceSignal{words: text, fromThread: true})
}

func signalFrom(ctx context.Context) *voiceSignal {
	s, _ := ctx.Value(voiceSignalKey{}).(*voiceSignal)
	return s
}

// withSignalOf carries ctx's signal onto a fresh context (dispatch).
func withSignalOf(dst, src context.Context) context.Context {
	if s := signalFrom(src); s != nil {
		return context.WithValue(dst, voiceSignalKey{}, s)
	}
	return dst
}

func customerWords(ctx context.Context) string {
	if s := signalFrom(ctx); s != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.words
	}
	return ""
}

// markStressed records that a model step already run for this message
// read the customer as upset (by: "agent:dispute").
func markStressed(ctx context.Context, by string) {
	if s := signalFrom(ctx); s != nil {
		s.mu.Lock()
		if s.stressBy == "" {
			s.stressBy = by
		}
		s.mu.Unlock()
	}
}

// voiceTrigger says whether this reply goes to the model, and why.
type voiceTrigger struct {
	Reason   string // "" none | language | stressed | language+stressed
	Lang     string // DetectLanguage: "" English
	StressBy string // the rule or step that found stress
	Words    string
	Formal   bool // Chinese: the customer used 您
}

func voiceTriggerFor(ctx context.Context) voiceTrigger {
	s := signalFrom(ctx)
	if s == nil {
		return voiceTrigger{}
	}
	s.mu.Lock()
	words, fromThread, by := s.words, s.fromThread, s.stressBy
	s.mu.Unlock()
	t := voiceTrigger{Words: words, Lang: DetectLanguage(words), Formal: strings.Contains(words, "您")}
	if !fromThread {
		if rule := StressedBy(words); rule != "" {
			by = "rule:" + rule
		}
		t.StressBy = by
	}
	switch {
	case t.Lang != "" && t.StressBy != "":
		t.Reason = VoiceTriggerLanguage + "+" + VoiceTriggerStressed
	case t.Lang != "":
		t.Reason = VoiceTriggerLanguage
	case t.StressBy != "":
		t.Reason = VoiceTriggerStressed
	}
	return t
}

// VoiceResult is what the voice layer did with one reply.
type VoiceResult struct {
	Text     string // what to send
	Outcome  string // VoiceRewritten | VoiceFallback | VoiceFixed | VoiceSkipped
	Reason   string // why fixed / why it fell back
	Trigger  string // why it ran: language | stressed | language+stressed
	StressBy string
	Language string // what the rules detected ("" English)
	Latency  time.Duration
	Blocks   int
	// Rejected is the model's rewrite that failed the check — for the eval
	// only; it is never logged, traced or sent.
	Rejected string
}

// voiceText is the text to send for out: the rewrite when the voice is on,
// triggered, and every rule holds; else out.Text unchanged.
func (r *Router) voiceText(ctx context.Context, out Outbound) string {
	if r.Voice == nil {
		return out.Text
	}
	_, sp := tracing.StartSpan(ctx, "channel.voice", tracing.KindChain)
	res := r.VoiceRewrite(ctx, out)
	sp.Attr(
		attribute.String("hora.voice.outcome", res.Outcome),
		attribute.String("hora.voice.reason", res.Reason),
		attribute.String("hora.voice.trigger", res.Trigger),
		attribute.String("hora.voice.stress_by", res.StressBy),
		attribute.Int64("hora.voice.latency_ms", res.Latency.Milliseconds()),
		attribute.String("hora.voice.language", res.Language),
		attribute.Int("hora.voice.blocks", res.Blocks),
		attribute.Int("hora.voice.chars_in", utf8.RuneCountInString(out.Text)),
		attribute.Int("hora.voice.chars_out", utf8.RuneCountInString(res.Text)),
		attribute.String("hora.voice.prompt_version", llm.VoicePromptVersion),
		attribute.String("hora.channel", string(out.Channel)),
	)
	sp.End()
	if res.Trigger != "" {
		log.Printf("[channel] voice channel=%s trigger=%s stress_by=%s lang=%s outcome=%s reason=%s latency_ms=%d",
			out.Channel, res.Trigger, res.StressBy, res.Language, res.Outcome, res.Reason, res.Latency.Milliseconds())
	}
	return res.Text
}

// VoiceRewrite runs the voice layer on one reply (exported for the eval).
func (r *Router) VoiceRewrite(ctx context.Context, out Outbound) VoiceResult {
	orig := out.Text
	tr := voiceTriggerFor(ctx)
	base := VoiceResult{Text: orig, Trigger: tr.Reason, StressBy: tr.StressBy, Language: tr.Lang}
	if tr.Reason == "" {
		base.Outcome, base.Reason = VoiceSkipped, "english_calm"
		return base
	}
	if reason := r.voiceFixedReason(out); reason != "" {
		base.Outcome, base.Reason = VoiceFixed, reason
		return base
	}
	masked, blocks := MaskForVoice(orig)
	base.Blocks = len(blocks)
	if !hasFreeWords(masked) {
		base.Outcome, base.Reason = VoiceFixed, "nothing_free"
		return base
	}
	cctx, cancel := context.WithTimeout(ctx, r.voiceTimeout())
	defer cancel()
	start := time.Now()
	got, err := r.Voice.Voice(cctx, llm.VoiceInput{
		Text: masked, Customer: tr.Words, Language: tr.Lang, Stressed: tr.StressBy != "", Formal: tr.Formal,
	})
	res := base
	res.Outcome, res.Latency = VoiceFallback, time.Since(start)
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
	opts := VoiceCheck{Lang: tr.Lang, Wrote: got.Language, Stressed: tr.StressBy != "", Formal: tr.Formal}
	if reason := CheckVoice(orig, restored, blocks, opts); reason != "" {
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
	return maskVoice(text, nil)
}

// MaskTemplateForVoice is MaskForVoice for a Copy* template: its format
// verbs (%s, %[2]s, %d …) are fixed too (agenttest -voice-copy).
func MaskTemplateForVoice(tmpl string) (string, []string) {
	var verbs []span
	for _, m := range fmtVerbRE.FindAllStringIndex(tmpl, -1) {
		verbs = append(verbs, span{m[0], m[1]})
	}
	return maskVoice(tmpl, verbs)
}

// UnmaskVoice puts n blocks back into a masked text (false: a token is
// missing, repeated or invented).
func UnmaskVoice(text string, blocks []string) (string, bool) {
	return unmaskVoice(text, len(blocks), blocks)
}

func maskVoice(text string, extra []span) (string, []string) {
	spans := append([]span(nil), extra...)
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
		if c == '.' && (strings.HasSuffix(line[:i+1], "e.g.") || strings.HasSuffix(line[:i+1], "i.e.")) {
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
// Where two formats match the same words, the one with more literal text
// is the one that was sent ("%s is on the way for %s. I'll text you …"
// over "%s is on the way%s%s."); the other is ignored.
func formatValueSpans(text string) []span {
	var out, taken []span
	for _, f := range voiceFormatREs() {
		for _, m := range f.re.FindAllStringSubmatchIndex(text, -1) {
			whole := span{m[0], m[1]}
			clash := false
			for _, t := range taken {
				if whole.a < t.b && t.a < whole.b {
					clash = true
					break
				}
			}
			if clash {
				continue
			}
			taken = append(taken, whole)
			for g := 2; g+1 < len(m); g += 2 {
				if m[g] >= 0 && m[g+1] > m[g] {
					out = append(out, span{m[g], m[g+1]})
				}
			}
		}
	}
	return out
}

type voiceFormatRE struct {
	re      *regexp.Regexp
	literal int
}

var (
	voiceFormatOnce sync.Once
	voiceFormatRx   []voiceFormatRE
	fmtVerbRE       = regexp.MustCompile(`%(?:\[\d+\])?[-+# 0]*\d*(?:\.\d+)?[sdvfq]`)
)

// voiceFormatREs compiles each line of each format with a verb into a
// regexp whose groups are the values, longest literal first. A line with
// under six literal characters ("%s") would match anything and is skipped.
func voiceFormatREs() []voiceFormatRE {
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
				// A value may be empty (an optional " — 2 blocks away"); one
				// at the end of the line stops at the end of its sentence, so
				// a copy glued on after it ("… — I've asked them") is not
				// taken for the value.
				var b strings.Builder
				b.WriteString(`(?m)`)
				last := 0
				for i, l := range locs {
					b.WriteString(regexp.QuoteMeta(strings.ReplaceAll(piece[last:l[0]], "%%", "%")))
					b.WriteString(`([^\n]*?)`)
					if i == len(locs)-1 && l[1] == len(piece) {
						b.WriteString(`(?:[.!?](?:\s|$)|\s—|$)`)
					}
					last = l[1]
				}
				b.WriteString(regexp.QuoteMeta(strings.ReplaceAll(piece[last:], "%%", "%")))
				voiceFormatRx = append(voiceFormatRx, voiceFormatRE{regexp.MustCompile(b.String()), literal})
			}
		}
		sort.SliceStable(voiceFormatRx, func(i, j int) bool { return voiceFormatRx[i].literal > voiceFormatRx[j].literal })
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
	voiceCommitRE  = regexp.MustCompile(`(?i)\b(?:book(?:ed|ing)?|confirm(?:ed)?|cancel(?:led|ed)?|refund(?:ed)?|free|guarantee[ds]?|promise[ds]?|charge[ds]?|discount|waive[ds]?|minutes?|hours?|asap|as soon as|right away|immediately)\b`)
	voiceNumWordRE = regexp.MustCompile(`(?i)\b(?:one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|fifteen|twenty|thirty|forty|fifty|hundred|half|dozen|couple)\b`)
)

// VoiceCheck is what the check knows besides the two texts.
type VoiceCheck struct {
	// Lang is what the rules detected in the customer's words ("" English);
	// Wrote, what the model says it wrote (ISO 639-1).
	Lang, Wrote string
	// Stressed: one opening acknowledgement of up to voiceAckChars more is
	// allowed.
	Stressed bool
	// Formal: the customer wrote 您 (Chinese); otherwise the reply uses 你.
	Formal bool
}

// voiceAckChars: the room an opening acknowledgement may take for a
// stressed customer ("Sorry for the wait — ", "Totally understand — ").
const voiceAckChars = 25

// CheckVoice is the rule check on a rewrite (restored: blocks back in).
// It returns "" when the rewrite may be sent, else the first reason it may
// not.
func CheckVoice(original, rewritten string, blocks []string, o VoiceCheck) string {
	if strings.TrimSpace(rewritten) == "" {
		return "empty"
	}
	if reason := checkLanguage(rewritten, o); reason != "" {
		return reason
	}
	if voiceGarbledRE.MatchString(rewritten) && !voiceGarbledRE.MatchString(original) {
		return "garbled"
	}
	if o.Lang != "" && untranslated(rewritten, blocks) {
		return "untranslated"
	}
	if gendered(original, rewritten) {
		return "gendered"
	}
	if !glossaryKept(original, rewritten) {
		return "glossary"
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
	if newName(original, rewritten) {
		return "name_added"
	}
	if strings.Contains(rewritten, "!!") || strings.Count(rewritten, "!") > max(1, strings.Count(original, "!")) {
		return "exclamation"
	}
	oe, re := emojiCount(original), emojiCount(rewritten)
	if re > max(1, oe) {
		return "emoji"
	}
	english := o.Lang == ""
	if english {
		if len(voiceCommitRE.FindAllString(rewritten, -1)) > len(voiceCommitRE.FindAllString(original, -1)) {
			return "commitment_added"
		}
		if len(voiceNumWordRE.FindAllString(rewritten, -1)) > len(voiceNumWordRE.FindAllString(original, -1)) {
			return "number_added"
		}
		if n := len(voiceSorryRE.FindAllString(rewritten, -1)); n > max(1, len(voiceSorryRE.FindAllString(original, -1))) {
			return "over_apology"
		}
	}
	// Same length or shorter; a 10% margin (at least 10 characters) for a
	// warmer word. Another language may run longer than English. A
	// stressed customer's reply may also open with one acknowledgement.
	ol := utf8.RuneCountInString(original)
	limit := ol + max(10, ol/10)
	if !english {
		limit = ol*8/5 + 10
	}
	if utf8.RuneCountInString(rewritten) > limit {
		if !o.Stressed {
			return "too_long"
		}
		ack, rest, ok := openingAck(rewritten)
		if !ok || utf8.RuneCountInString(ack) > voiceAckChars || utf8.RuneCountInString(rest) > limit {
			return "too_long"
		}
		if questions(ack) > 0 || voiceNumberRE.MatchString(ack) || english && voiceCommitRE.MatchString(ack) {
			return "ack_not_plain"
		}
	}
	return ""
}

// voiceGarbledRE: text decoded in the wrong charset ("ÂżEn quĂ©",
// "â€™"), or the replacement character.
var voiceGarbledRE = regexp.MustCompile(`[ÃÂĂĹ][\x{80}-\x{24F}]|â€|\x{FFFD}`)

// voiceEnglishRE: English words that no other language writes — left in a
// translated reply outside the fixed blocks, they are untranslated copy.
var voiceEnglishRE = regexp.MustCompile(`(?i)\b(?:the|and|for|you|your|is|are|with|they|them|this|that|what|when|where|how|i'?ll|i'?m|won'?t|can'?t|couldn'?t|just|please|here)\b`)

func untranslated(rewritten string, blocks []string) bool {
	free := rewritten
	for _, b := range blocks {
		free = strings.Replace(free, b, " ", 1)
	}
	return voiceEnglishRE.MatchString(free)
}

// The glossary (voice.md): "supporter" and "HO:RA" are never translated.
var (
	voiceSupporterRE  = regexp.MustCompile(`(?i)\bsupporters?\b`)
	voiceTranslatedRE = regexp.MustCompile(`跑腿|配送員|配送员|(?i)\b(?:repartidor|ayudante|asistente|recadero|mensajero|livreur|fattorino|entregador|helfer)\w*`)
)

// GlossaryKept: every "supporter" and "HO:RA" of the original is still
// there, untranslated (exported for the eval's own check).
func GlossaryKept(original, rewritten string) bool { return glossaryKept(original, rewritten) }

func glossaryKept(original, rewritten string) bool {
	if len(voiceSupporterRE.FindAllString(rewritten, -1)) < len(voiceSupporterRE.FindAllString(original, -1)) {
		return false
	}
	if strings.Count(rewritten, "HO:RA") < strings.Count(original, "HO:RA") {
		return false
	}
	return !voiceTranslatedRE.MatchString(rewritten) || voiceTranslatedRE.MatchString(original)
}

// gendered: a supporter's gender is never known; the copy says "they".
// A reply that adds "he" or "she" (他 / 她 / él / ella / il / elle …)
// guesses it.
var voiceGenderRE = regexp.MustCompile(`(?i)\b(?:he|she|him|his|her|hers|él|ella|il|elle|lui|lei|ele|ela|er|sie)\b|他(?:[^们們]|$)|她`)

func gendered(original, rewritten string) bool {
	return len(voiceGenderRE.FindAllString(rewritten, -1)) > len(voiceGenderRE.FindAllString(original, -1))
}

// newName: a capitalised Latin word in the middle of a sentence that the
// original does not have ("de Nueva York") — a fact the router did not
// give.
func newName(original, rewritten string) bool {
	for _, m := range voiceMidCapRE.FindAllStringSubmatch(rewritten, -1) {
		w := strings.TrimSuffix(strings.TrimSuffix(m[1], "'s"), "’s")
		if w == "I" || strings.HasPrefix(w, "I'") || strings.HasPrefix(w, "I’") || voiceKeywordRE.MatchString(w) {
			continue
		}
		if !containsWord(original, w) {
			return true
		}
	}
	return false
}

// voiceMidCapRE: a capitalised word after a lower-case word or a comma —
// not at the start of a sentence, a line or a quote.
var voiceMidCapRE = regexp.MustCompile(`[\p{Ll},]\s+([A-Z][A-Za-z0-9'’&-]*)`)

var voiceSorryRE = regexp.MustCompile(`(?i)\bsorry\b|\bapologi[sz]e`)

// openingAck splits a reply into its opening clause and the rest: the
// clause ends at the first dash, comma, period, "!" or colon (or their
// full-width forms) within the first voiceAckChars+5 characters.
func openingAck(s string) (ack, rest string, ok bool) {
	n := 0
	for i, c := range s {
		if n > voiceAckChars+5 {
			return "", "", false
		}
		n++
		switch c {
		case '—', '–', ',', '.', '!', ':', '，', '。', '！', '：', '、':
			j := i + utf8.RuneLen(c)
			return s[:j], strings.TrimLeft(s[j:], " "), true
		case '-':
			if i > 0 && s[i-1] == ' ' {
				j := i + 1
				return s[:j], strings.TrimLeft(s[j:], " "), true
			}
		}
	}
	return "", "", false
}

// checkLanguage: the reply is in the customer's language, Chinese in their
// script and their form of "you".
func checkLanguage(rewritten string, o VoiceCheck) string {
	wrote := strings.ToLower(o.Wrote)
	want := strings.ToLower(o.Lang)
	switch {
	case want == "" && wrote != "" && wrote != "en":
		return "language_mismatch"
	case want == "other" && wrote == "en":
		return "language_mismatch"
	case want != "" && want != "other" && !strings.HasPrefix(wrote, want[:2]):
		return "language_mismatch"
	}
	if strings.HasPrefix(want, "zh") {
		trad, simp := chineseScript(rewritten)
		switch {
		case want == LangZhHant && simp > 0:
			return "script_mismatch"
		case want == LangZhHans && trad > 0:
			return "script_mismatch"
		}
		if !o.Formal && strings.Contains(rewritten, "您") {
			return "formality"
		}
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
	// A Latin-letter boundary only: Chinese writes "Bea已经到了" with no
	// space, and the name is still there.
	re, err := regexp.Compile(`(?:^|[^A-Za-z0-9])` + regexp.QuoteMeta(w) + `(?:$|[^A-Za-z0-9])`)
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
		w = strings.TrimSuffix(strings.TrimSuffix(w, "'s"), "’s")
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

// WithThreadWordsForEval: a status update, whose language is the thread's.
func WithThreadWordsForEval(ctx context.Context, text string) context.Context {
	return withThreadWords(ctx, text)
}
