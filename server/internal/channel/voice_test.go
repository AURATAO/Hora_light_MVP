package channel

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"hora-auth/internal/channel/llm"
	"hora-auth/internal/tracing"
)

// fakeVoice rewrites with f; it records what it was given.
type fakeVoice struct {
	mu    sync.Mutex
	f     func(in llm.VoiceInput) (llm.VoiceOutput, error)
	delay time.Duration
	got   []llm.VoiceInput
}

func (v *fakeVoice) Voice(ctx context.Context, in llm.VoiceInput) (llm.VoiceOutput, error) {
	v.mu.Lock()
	v.got = append(v.got, in)
	v.mu.Unlock()
	if v.delay > 0 {
		select {
		case <-time.After(v.delay):
		case <-ctx.Done():
			return llm.VoiceOutput{}, fmt.Errorf("voice: %w", llm.ErrTimeout)
		}
	}
	return v.f(in)
}

func (v *fakeVoice) calls() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.got)
}

func say(text string) func(llm.VoiceInput) (llm.VoiceOutput, error) {
	return func(llm.VoiceInput) (llm.VoiceOutput, error) { return llm.VoiceOutput{Text: text, Language: "en"}, nil }
}

// ── The rule check ─────────────────────────────────────────────────────────

func TestCheckVoice(t *testing.T) {
	ack := "Bea's phone hasn't reported a location for a bit — I've asked them where they are."
	summaryish := "Laundry pickup\nTomorrow, 3:00 PM · about 30 min\nEstimate: $42 (you pay for the time actually used)\n\nReply YES to book, or tell me what to change."
	_, sumBlocks := MaskForVoice(summaryish)
	cases := []struct {
		name, orig, rewrite string
		blocks              []string
		lang, want          string
		o                   *VoiceCheck
	}{
		{"warmer, same facts", "Got it. What would you like to tell them?", "Sure thing. What would you like me to tell them?", nil, "en", "", nil},
		{"dropped price", "Cancelling now costs $12, the base fee.", "Cancelling now costs the base fee.", nil, "en", "price_changed", nil},
		{"changed price", "Cancelling now costs $12.", "Cancelling now costs $15.", nil, "en", "price_changed", nil},
		{"changed time", "Alex will be there at 3:00 PM.", "Alex will be there at 4:00 PM.", nil, "en", "time_changed", nil},
		{"time reformatted", "Alex will be there at 3:00 PM.", "Alex will be there at 3pm.", nil, "en", "time_changed", nil},
		{"dropped number", "Up to 10 extra minutes are fine.", "A few extra minutes are fine.", nil, "en", "number_dropped", nil},
		{"added number", "I'll text you when they arrive.", "I'll text you in 5 minutes.", nil, "en", "number_added", nil},
		{"number in words", "I'll text you when they arrive.", "I'll text you in five.", nil, "en", "number_added", nil},
		{"added URL", "Tell me what you need and when.", "Tell me what you need and when, or see https://my-hora.com.", nil, "en", "url_added", nil},
		{"added bare domain", "Tell me what you need.", "Tell me what you need, or visit my-hora.com.", nil, "en", "url_added", nil},
		{"dropped email", "Please email info@my-hora.com and we'll follow up.", "Please write to us and we'll follow up.", nil, "en", "url_dropped", nil},
		{"added question", "Got it. I'm here whenever you need a hand.", "Got it. Anything else I can help with?", nil, "en", "question_added", nil},
		{"missing YES line", summaryish, strings.Replace(summaryish, "\n\nReply YES to book, or tell me what to change.", "\n\nShall I book it?", 1), sumBlocks, "en", "block_missing", nil},
		{"YES line reworded", summaryish, strings.Replace(summaryish, "Reply YES to book", "Say YES to book", 1), sumBlocks, "en", "block_missing", nil},
		{"blocks out of order", "A ⟦1⟧ B ⟦2⟧", "x second y first", []string{"first", "second"}, "en", "block_missing", nil},
		{"added YES", "Tell me what to change.", "Tell me what to change, or send YES.", nil, "en", "keyword_added", nil},
		{"dropped name", "Alex is on the way to Hudson Cleaners.", "Your supporter is on the way.", nil, "en", "name_dropped", nil},
		{"dropped borough", "We run errands in Manhattan and Brooklyn.", "We run errands in Manhattan.", nil, "en", "name_dropped", nil},
		{"too long", "Got it.", "Got it — thank you so much for letting me know, I really appreciate it.", nil, "en", "too_long", nil},
		{"two emoji", "Got it.", "Got it 👍🙂", nil, "en", "emoji", nil},
		{"one emoji", "Got it, I'm here whenever you need a hand.", "Got it 👍 I'm here whenever you need a hand.", nil, "en", "", nil},
		{"exclamation chain", "Got it.", "Got it!!", nil, "en", "exclamation", nil},
		{"as an AI", "I can only read text for now.", "As an AI, I can only read text for now.", nil, "en", "as_an_ai", nil},
		{"new promise", "I'm finding a supporter now.", "I'm finding a supporter now, guaranteed.", nil, "en", "commitment_added", nil},
		{"new price word", "I won't book that.", "I won't book that, and it's free.", nil, "en", "commitment_added", nil},
		{"empty", "Got it.", "  ", nil, "en", "empty", nil},
		{"spanish rewrite keeps names", "Which borough is Broadway in?", "¿En qué distrito está Broadway?", nil, "es", "", nil},
		{"spanish rewrite drops name", "Which borough is Broadway in?", "¿En qué distrito está?", nil, "es", "name_dropped", nil},
		{"phone added", "Tell me what you need.", "Tell me what you need, or call 212-555-0100.", nil, "en", "number_added", nil},
		{"possessive becomes 'de Bea'", "Bea's phone hasn't reported a location.", "El teléfono de Bea no ha reportado su ubicación.", nil, "es", "", nil},
		{"name joined to Chinese", "Which borough is 350 Broadway in?", "350 Broadway在哪个区？", nil, "", "", &VoiceCheck{Lang: LangZhHans, Wrote: "zh"}},
		{"name inside a longer word is not the name", "Alex is on the way.", "Alexander is on the way.", nil, "en", "name_dropped", nil},
		{"garbled", "Which borough is 350 Broadway in?", "ÂżEn quĂ© distrito estĂĄ 350 Broadway?", nil, "es", "garbled", nil},
		{"half translated", "Bea is on the way for “Recoger ropa”.", "Bea va en camino for “Recoger ropa”.", []string{"“Recoger ropa”"}, "es", "untranslated", nil},
		{"English inside a block is fine", "Will you buy something? About how much, e.g. $20?", "¿Vas a comprar algo? About how much, e.g. $20?", []string{"About how much, e.g. $20?"}, "es", "", nil},
		{"new place", "Which borough is 350 Broadway in?", "¿En qué distrito de Nueva York está 350 Broadway?", nil, "es", "name_added", nil},
		{"guessed gender, Chinese", "Alex accepted it. I'll text you when they arrive.", "Alex 已接下。他到了我會傳訊息給你。", nil, "", "gendered", &VoiceCheck{Lang: LangZhHant, Wrote: "zh"}},
		{"neutral plural is fine", "I've asked them where they are.", "我已經問過他們人在哪裡。", nil, "", "", &VoiceCheck{Lang: LangZhHant, Wrote: "zh"}},
		{"guessed gender, Spanish", "Bea is on the way. I'll text you when they arrive.", "Bea va en camino. Te escribo cuando ella llegue.", nil, "es", "gendered", nil},
		{"guessed gender, English", "Bea is on the way.", "Bea is on the way, she's close.", nil, "en", "gendered", nil},
		{"as soon as", "I'll text you when they arrive.", "I'll text you as soon as they arrive.", nil, "en", "commitment_added", nil},
		{"right away", "I'm finding a supporter now.", "I'm finding a supporter right away.", nil, "en", "commitment_added", nil},
		{"immediately", "Please try again in a moment.", "Please try again immediately.", nil, "en", "commitment_added", nil},
		{"stressed: one acknowledgement", ack, "Sorry for the wait — " + ack, nil, "", "", &VoiceCheck{Wrote: "en", Stressed: true}},
		{"calm: no room for it", ack, "Sorry for the wait — " + ack, nil, "", "too_long", &VoiceCheck{Wrote: "en"}},
		{"stressed: acknowledgement too long", ack, "I am so very sorry about all of this — " + ack, nil, "", "too_long", &VoiceCheck{Wrote: "en", Stressed: true}},
		{"stressed: acknowledgement plus padding", ack, "Sorry for the wait — " + ack + " Thanks so much for bearing with us today.", nil, "", "too_long", &VoiceCheck{Wrote: "en", Stressed: true}},
		{"stressed: acknowledgement with a promise", ack, "On it right away — " + ack, nil, "", "commitment_added", &VoiceCheck{Wrote: "en", Stressed: true}},
		{"two apologies", "I couldn't cancel it just now.", "Sorry, I couldn't cancel it just now, sorry.", nil, "", "over_apology", &VoiceCheck{Wrote: "en"}},
		{"English customer, Spanish reply", "Got it.", "Entendido.", nil, "", "language_mismatch", &VoiceCheck{Wrote: "es"}},
		{"Spanish customer, English reply", "Got it.", "Got it.", nil, "", "language_mismatch", &VoiceCheck{Lang: "es", Wrote: "en"}},
		{"Traditional in, Simplified out", "Got it.", "好的，这个收到了。", nil, "", "script_mismatch", &VoiceCheck{Lang: LangZhHant, Wrote: "zh"}},
		{"Simplified in, Traditional out", "Got it.", "好的，這個收到了。", nil, "", "script_mismatch", &VoiceCheck{Lang: LangZhHans, Wrote: "zh"}},
		{"Traditional in, Traditional out", "Got it.", "好的，這個收到了。", nil, "", "", &VoiceCheck{Lang: LangZhHant, Wrote: "zh"}},
		{"您 unasked", "Got it.", "好的，您的请求收到了。", nil, "", "formality", &VoiceCheck{Lang: LangZhHans, Wrote: "zh"}},
		{"您 when they used it", "Got it.", "好的，您的请求收到了。", nil, "", "", &VoiceCheck{Lang: LangZhHans, Wrote: "zh", Formal: true}},
	}
	for _, c := range cases {
		o := VoiceCheck{Wrote: c.lang}
		if c.lang != "en" {
			o.Lang = c.lang
		}
		if c.o != nil {
			o = *c.o
		}
		if got := CheckVoice(c.orig, c.rewrite, c.blocks, o); got != c.want {
			t.Errorf("%s: CheckVoice = %q, want %q", c.name, got, c.want)
		}
	}
}

// The masker keeps every fixed block away from the model.
func TestMaskForVoice(t *testing.T) {
	cases := []struct {
		text  string
		fixed []string // must each be inside one block
		free  []string // must stay visible to the model
	}{
		{CopyBooked, []string{CopyCancelHint}, []string{"I'm finding a supporter now"}},
		{fmt.Sprintf(CopyAskName, "Pick up dry cleaning"), []string{"Pick up dry cleaning"}, []string{"what's your first name?"}},
		{fmt.Sprintf(CopyCancelConfirmFee, "Laundry pickup", "", "$12", "$10"), []string{"“Laundry pickup”", "$12 (the $10 base fee", "Reply YES to cancel, or NO to keep it."}, nil},
		{fmt.Sprintf(CopyCancelledFree, "Laundry pickup"), []string{"“Laundry pickup”", "No charge."}, []string{"No worries at all"}},
		{CopyHandoffTimedOut, []string{"info@my-hora.com", "“real person”"}, []string{"I'm back to help"}},
		{fmt.Sprintf(CopyOfferItems, "a Margherita", "Nolita Pizza"), []string{"Want your supporter to get a Margherita at Nolita Pizza?"}, nil},
		{"1. Joe's Pizza\n2. Prince Street Pizza", []string{"1. Joe's Pizza", "2. Prince Street Pizza"}, nil},
		{CopyDraftStillPending, []string{CopyDraftStillPending}, nil},
		{CopyAskBudget, []string{`Reply "nothing" if there's nothing to buy.`, "$20"}, []string{"Will your supporter be buying something"}},
		{fmt.Sprintf(CopyWhichBorough, "Broadway"), []string{"Broadway"}, []string{"Which borough is"}},
		// A copy glued on after a trailing value is not part of the value.
		{fmt.Sprintf(CopyWhereUnavailableLine, "Bea", "") + CopyWhereAsked, []string{"Bea"}, []string{"I've asked them where they are.", "phone hasn't reported"}},
		// The format that was sent wins over a shorter one that also matches.
		{"Bea is on the way for “Recoger ropa”. I'll text you when they arrive.", []string{"Bea", "“Recoger ropa”"}, []string{" is on the way for ", "I'll text you"}},
		// "e.g." does not end the sentence: the price sentence is one block.
		{CopyAskBudget, []string{"About how much should I set aside, e.g. $20?"}, nil},
	}
	for _, c := range cases {
		masked, blocks := MaskForVoice(c.text)
		for _, f := range c.fixed {
			if strings.Contains(masked, f) {
				t.Errorf("%q: %q reaches the model: %q", c.text, f, masked)
			}
			in := false
			for _, b := range blocks {
				in = in || strings.Contains(b, f)
			}
			if !in {
				t.Errorf("%q: %q is in no block %q", c.text, f, blocks)
			}
		}
		for _, f := range c.free {
			if !strings.Contains(masked, f) {
				t.Errorf("%q: %q was masked: %q", c.text, f, masked)
			}
		}
		if back, ok := unmaskVoice(masked, len(blocks), blocks); !ok || back != c.text {
			t.Errorf("%q: unmask(mask) = %q, %t", c.text, back, ok)
		}
	}
}

// Whole messages that never reach the model.
func TestVoiceFixedMessages(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	v := &fakeVoice{f: say("rewritten")}
	g.r.Voice = v
	refusal := ""
	if s := scopeRules(); s != nil && len(s.Refusals) > 0 {
		refusal = s.Refusals[0].Reply
	}
	fixed := map[string]Outbound{
		"relay":          {Text: "Alex · Laundry: on my way", Relay: true},
		"verbatim":       {Text: "Hi, this is Sam from HO:RA, checking in.", Verbatim: true},
		"media":          {Text: "Done.", MediaURL: "https://x/y.jpg"},
		"setup_link":     {Text: fmt.Sprintf(CopyOneStepToBook, "https://x/s/tok"), StoredText: "[setup link]"},
		"tcpa":           {Text: CopyHelpInfo},
		"tcpa opt-in":    {Text: CopyOptInWelcome},
		"scope":          {Text: refusal + "\n\n" + CopyBookingStillWaiting},
		"scope holding":  {Text: CopyScopeHolding},
		"read_back":      {Text: CopyParseFailed},
		"supporter":      {Text: CopySupporterAlready},
		"summary":        {Text: "Laundry pickup\nAs soon as possible\nEstimate: $42 (you pay for the time actually used)\n\nReply YES to book, or tell me what to change."},
		"cited":          {Text: "Reviews mention their Margherita.\n\nWant your supporter to get a Margherita at Nolita Pizza?\nFrom Google Maps."},
		"cited web":      {Text: "Their menu lists a vodka slice.\nFrom joespizzanyc.com/menu"},
		"supporter link": {Text: fmt.Sprintf(CopySupporterApply, DefaultSupporterApplyURL)},
		"all protected":  {Text: CopyConfirmReask},
	}
	if refusal == "" {
		delete(fixed, "scope")
	}
	ctx := withCustomerWords(context.Background(), "hola, necesito ayuda con mi ropa por favor")
	for name, out := range fixed {
		res := g.r.VoiceRewrite(ctx, out)
		if res.Outcome != VoiceFixed || res.Text != out.Text {
			t.Errorf("%s: outcome %s (%s), text %q", name, res.Outcome, res.Reason, res.Text)
		}
	}
	if v.calls() != 0 {
		t.Fatalf("the model was asked %d times for fixed messages", v.calls())
	}
}

// Every way a rewrite can fail sends the original, with the reason.
func TestVoiceFallbacks(t *testing.T) {
	orig := "Something went wrong on our side while booking. Reply YES to try again."
	cases := []struct {
		name   string
		v      *fakeVoice
		reason string
	}{
		{"model error", &fakeVoice{f: func(llm.VoiceInput) (llm.VoiceOutput, error) {
			return llm.VoiceOutput{}, errors.New("voice: status 529")
		}}, "error"},
		{"timeout", &fakeVoice{f: say("Sorry, something went wrong on our end. ⟦1⟧"), delay: time.Second}, "timeout"},
		{"token dropped", &fakeVoice{f: say("Sorry, something went wrong on our end. Try again?")}, "tokens"},
		{"token doubled", &fakeVoice{f: say("Sorry — ⟦1⟧ ⟦1⟧")}, "tokens"},
		{"token invented", &fakeVoice{f: say("Sorry — ⟦1⟧ ⟦2⟧")}, "tokens"},
		{"question added", &fakeVoice{f: say("Sorry, that didn't go through — want to try again? ⟦1⟧")}, "question_added"},
	}
	for _, c := range cases {
		g := newRig(llm.IntentNewTask)
		g.r.Voice, g.r.VoiceTimeout = c.v, 50*time.Millisecond
		res := g.r.VoiceRewrite(stressedCtx(), Outbound{Channel: IMessage, Text: orig})
		if res.Outcome != VoiceFallback || res.Reason != c.reason || res.Text != orig {
			t.Errorf("%s: %s/%s %q, want fallback/%s and the original", c.name, res.Outcome, res.Reason, res.Text, c.reason)
		}
	}
	// And a good one goes through, with the block put back.
	g := newRig(llm.IntentNewTask)
	g.r.Voice = &fakeVoice{f: say("Sorry, something went wrong on our end. ⟦1⟧")}
	res := g.r.VoiceRewrite(stressedCtx(), Outbound{Channel: IMessage, Text: orig})
	if res.Outcome != VoiceRewritten || res.Text != "Sorry, something went wrong on our end. Reply YES to try again." {
		t.Fatalf("good rewrite: %s/%s %q", res.Outcome, res.Reason, res.Text)
	}
}

// Off by default: no Voice, no change, no span.
func TestVoiceOffSendsCopy(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	if err := g.r.Reply(context.Background(), Inbound{Channel: IMessage, From: me}, CopyAck, ""); err != nil {
		t.Fatal(err)
	}
	if g.ad.last() != CopyAck {
		t.Fatalf("sent %q", g.ad.last())
	}
}

// Through the real send path: the rewrite is what is sent and recorded,
// the model sees the person's words, and the channel.voice span carries
// the outcome and latency but no text.
func TestVoiceThroughSend(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	stop := tracing.StartWithExporter(exp)
	defer func() { _ = stop(context.Background()) }()

	g := newRig(llm.IntentOther)
	v := &fakeVoice{f: func(in llm.VoiceInput) (llm.VoiceOutput, error) {
		return llm.VoiceOutput{Text: welcomeES, Language: "es"}, nil
	}}
	g.r.Voice = v
	h := newHandler(g.ad, g.st, g.r)
	h.route(Inbound{Channel: IMessage, ProviderMessageID: "pm-v1", From: me, Text: "hola, necesito ayuda por favor", ReceivedAt: time.Now()})
	if v.calls() == 0 {
		t.Fatalf("voice not asked; sent %q", g.ad.last())
	}
	if in := v.got[0]; in.Customer != "hola, necesito ayuda por favor" || in.Language != "es" || in.Stressed {
		t.Errorf("voice input %+v", in)
	}
	if want := welcomeES; g.ad.last() != want {
		t.Fatalf("sent %q", g.ad.last())
	}
	found := false
	for _, s := range exp.GetSpans() {
		if s.Name != "channel.voice" {
			continue
		}
		found = true
		a := map[string]string{}
		for _, kv := range s.Attributes {
			a[string(kv.Key)] = kv.Value.Emit()
		}
		if a["hora.voice.outcome"] != VoiceRewritten || a["hora.voice.latency_ms"] == "" || a["hora.voice.trigger"] != VoiceTriggerLanguage || a["hora.voice.language"] != "es" {
			t.Errorf("span attrs %v", a)
		}
		for k, val := range a {
			if strings.Contains(val, "concierge") || strings.Contains(val, "necesito") || k == "input.value" || k == "output.value" {
				t.Errorf("channel.voice carries text: %s=%q", k, val)
			}
		}
	}
	if !found {
		t.Fatal("no channel.voice span")
	}
}

// A fallback is logged by reason and sends the copy as written.
func TestVoiceFallbackThroughSend(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.r.Voice = &fakeVoice{f: say("Booked! I'm finding a supporter in 5 minutes. ⟦1⟧")}
	if err := g.r.Reply(stressedCtx(), Inbound{Channel: IMessage, From: me}, CopyBooked, ""); err != nil {
		t.Fatal(err)
	}
	if g.ad.last() != CopyBooked {
		t.Fatalf("sent %q, want the copy", g.ad.last())
	}
}

// Ops' own words and supporter requests are never reworded.
func TestVoiceNeverRewordsOpsOrRequests(t *testing.T) {
	g, _ := handoffRig(t, llm.IntentOther)
	g.r.Go = func(f func()) { f() }
	ctx := stressedCtx()
	id := Identity{ID: "id-" + me, Channel: IMessage, Address: me, UserID: "user-1"}
	g.say(t, "are you a bot?") // hands off
	v := &fakeVoice{f: say("reworded")}
	g.r.Voice = v
	if err := g.r.OpsSend(ctx, id, "Hi, Sam from HO:RA here. I'll sort out the refund today."); err != nil {
		t.Fatal(err)
	}
	if g.ad.last() != "Hi, Sam from HO:RA here. I'll sort out the refund today." || v.calls() != 0 {
		t.Fatalf("ops text: sent %q, model calls %d", g.ad.last(), v.calls())
	}
	ev := StatusEvent{Type: evTimeRequested, TaskTitle: "Laundry pickup", SupporterName: "Alex", ExtensionID: "ext-1",
		Approval: &PendingApproval{ID: "ext-1", Kind: "time", Minutes: 15, Reason: "long line"}}
	text, ok := StatusText(ev)
	if !ok {
		t.Fatal("no approval text")
	}
	out := Outbound{Channel: IMessage, To: me, Text: text, Verbatim: true}
	if res := g.r.VoiceRewrite(ctx, out); res.Outcome != VoiceFixed {
		t.Fatalf("approval: %s/%s", res.Outcome, res.Reason)
	}
}

// Every Copy* format with a value is listed in voiceFormats, so its values
// are masked.
func TestVoiceFormatsComplete(t *testing.T) {
	fs := token.NewFileSet()
	pkgs, err := parser.ParseDir(fs, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	var missing []string
	for _, p := range pkgs {
		for _, f := range p.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				vs, ok := n.(*ast.ValueSpec)
				if !ok {
					return true
				}
				for i, name := range vs.Names {
					if name.Name == "voiceFormats" {
						for _, e := range vs.Values[0].(*ast.CompositeLit).Elts {
							listed[e.(*ast.Ident).Name] = true
						}
					}
					if !strings.HasPrefix(name.Name, "Copy") || i >= len(vs.Values) {
						continue
					}
					if s, ok := stringValue(vs.Values[i]); ok && fmtVerbRE.MatchString(s) {
						missing = append(missing, name.Name)
					}
				}
				return true
			})
		}
	}
	var out []string
	for _, n := range missing {
		if !listed[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	if len(out) > 0 {
		t.Fatalf("Copy formats not in voiceFormats (voice_formats.go): %s", strings.Join(out, ", "))
	}
	if len(listed) < 100 {
		t.Fatalf("voiceFormats has %d entries; the parse found the list?", len(listed))
	}
}

// stringValue reads a string literal or a + of literals and Copy idents
// (an ident's own literal is checked on its own spec).
func stringValue(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(x.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		a, ok1 := stringValue(x.X)
		b, ok2 := stringValue(x.Y)
		return a + b, ok1 || ok2
	}
	return "", false
}

func stressedCtx() context.Context {
	return withCustomerWords(context.Background(), "this is ridiculous, I've been waiting forever")
}

// English and calm: sent as written, no model call, no added latency.
func TestVoiceSkipsCalmEnglish(t *testing.T) {
	g := newRig(llm.IntentOther)
	v := &fakeVoice{f: say("reworded")}
	g.r.Voice = v
	h := newHandler(g.ad, g.st, g.r)
	h.route(Inbound{Channel: IMessage, ProviderMessageID: "pm-calm", From: me, Text: "hi there, what is this?", ReceivedAt: time.Now()})
	if v.calls() != 0 || g.ad.last() != CopyWelcome {
		t.Fatalf("calm English: model calls %d, sent %q", v.calls(), g.ad.last())
	}
	for _, words := range []string{"", "YES", "2", "pick up my laundry at 12 Bedford Ave tomorrow at 3pm", "thanks!", "no hurry, whenever works"} {
		res := g.r.VoiceRewrite(withCustomerWords(context.Background(), words), Outbound{Text: CopyAck})
		if res.Outcome != VoiceSkipped || res.Trigger != "" {
			t.Errorf("%q: %s trigger=%q", words, res.Outcome, res.Trigger)
		}
	}
	if res := g.r.VoiceRewrite(context.Background(), Outbound{Text: CopyAck}); res.Outcome != VoiceSkipped {
		t.Errorf("no words: %s", res.Outcome)
	}
	if v.calls() != 0 {
		t.Fatalf("model asked %d times", v.calls())
	}
}

func TestVoiceTriggers(t *testing.T) {
	cases := []struct {
		words, trigger, lang string
	}{
		{"hola, necesito que alguien recoja mi ropa", VoiceTriggerLanguage, "es"},
		{"gracias", VoiceTriggerLanguage, "es"},
		{"你好，有人能帮我取快递吗", VoiceTriggerLanguage, LangZhHans},
		{"你好，有人可以幫我拿包裹嗎", VoiceTriggerLanguage, LangZhHant},
		{"你好", VoiceTriggerLanguage, LangZh},
		{"where is my supporter??", VoiceTriggerStressed, ""},
		{"I've been waiting forever", VoiceTriggerStressed, ""},
		{"I was charged twice", VoiceTriggerStressed, ""},
		{"WHERE IS MY ORDER", VoiceTriggerStressed, ""},
		{"我的跑腿員怎麼還沒到？？", VoiceTriggerLanguage + "+" + VoiceTriggerStressed, LangZhHant},
		{"¿dónde está? todavía no llega", VoiceTriggerLanguage + "+" + VoiceTriggerStressed, "es"},
	}
	for _, c := range cases {
		tr := voiceTriggerFor(withCustomerWords(context.Background(), c.words))
		if tr.Reason != c.trigger || tr.Lang != c.lang {
			t.Errorf("%q: trigger %q lang %q, want %q %q", c.words, tr.Reason, tr.Lang, c.trigger, c.lang)
		}
	}
	// The agent's own reading of this message, when it was asked anyway.
	ctx := withCustomerWords(context.Background(), "the receipt says $40 but I set aside $20")
	if tr := voiceTriggerFor(ctx); tr.Reason != "" {
		t.Fatalf("calm words triggered: %+v", tr)
	}
	markStressed(ctx, "agent:"+string(llm.EscalateDispute))
	if tr := voiceTriggerFor(ctx); tr.Reason != VoiceTriggerStressed || tr.StressBy != "agent:dispute" {
		t.Fatalf("agent dispute: %+v", tr)
	}
	// A status update takes the thread's language, never its mood.
	if tr := voiceTriggerFor(withThreadWords(context.Background(), "where is she?? this is ridiculous")); tr.Reason != "" {
		t.Fatalf("thread mood triggered: %+v", tr)
	}
	if tr := voiceTriggerFor(withThreadWords(context.Background(), "gracias, ¿cuándo llega?")); tr.Reason != VoiceTriggerLanguage || tr.StressBy != "" {
		t.Fatalf("thread language: %+v", tr)
	}
	if tr := voiceTriggerFor(withCustomerWords(context.Background(), "您好，請問包裹到了嗎")); !tr.Formal {
		t.Fatal("您 not seen")
	}
}

func TestDetectLanguage(t *testing.T) {
	cases := map[string]string{
		"pick up my laundry from 12 Bedford Ave": "",
		"YES":                                    "",
		"ok":                                     "",
		"350 Broadway":                           "",
		"grab coffee at La Colombe on Lafayette": "",
		"can you pick up something from Del Frisco's":  "",
		"recoger un paquete en 350 broadway":           "es",
		"merci beaucoup, je voudrais une livraison":    "fr",
		"ciao, ho bisogno di ritirare un pacco domani": "it",
		"olá, preciso buscar um pacote amanhã":         "pt",
		"hallo, ich brauche Hilfe mit einem Paket":     "de",
		"荷物を受け取ってください":                                 "ja",
		"택배 좀 받아주세요":                                   "ko",
		"Здравствуйте, нужна помощь":                   "ru",
		"這個可以幫我送嗎":                                     LangZhHant,
		"这个可以帮我送吗":                                     LangZhHans,
	}
	for in, want := range cases {
		if got := DetectLanguage(in); got != want {
			t.Errorf("DetectLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStressedBy(t *testing.T) {
	stressed := []string{"hello??", "where is she?!", "still waiting on my groceries", "this is ridiculous", "I'm going to be late",
		"no one is answering", "you guys are useless", "I NEED THIS NOW", "where is the DRIVER HELLO", "怎麼還沒到", "this is taking forever"}
	calm := []string{"hi", "where is she?", "is she close?", "can you hurry a bit if possible", "no hurry", "pick up from CVS on 14th",
		"YES", "pick me up a late lunch", "how long does laundry usually take?", "thanks so much!", "NYC ok"}
	for _, s := range stressed {
		if StressedBy(s) == "" {
			t.Errorf("%q should read as stressed", s)
		}
	}
	for _, s := range calm {
		if by := StressedBy(s); by != "" {
			t.Errorf("%q read as stressed (%s)", s, by)
		}
	}
}

// A status update answers no message: it takes the thread's language (the
// newest inbound that says anything about it), never its mood.
func TestVoiceStatusTakesThreadLanguage(t *testing.T) {
	ev := StatusEvent{Type: "ORDER_ACCEPTED", TaskID: "task-1", SupporterName: "Maya", TaskTitle: "Laundry"}
	run := func(thread fixedThread) *fakeVoice {
		g := newRig("")
		g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{Channel: IMessage, Address: me}}}
		g.r.Thread = thread
		v := &fakeVoice{f: func(in llm.VoiceInput) (llm.VoiceOutput, error) {
			return llm.VoiceOutput{Text: in.Text, Language: "es"}, nil
		}}
		g.r.Voice = v
		if err := g.r.NotifyStatus(context.Background(), "user-1", ev); err != nil {
			t.Fatal(err)
		}
		return v
	}
	now := time.Now()
	v := run(fixedThread{
		{Direction: "inbound", Text: "hola, necesito recoger mi ropa mañana", At: now.Add(-3 * time.Minute)},
		{Direction: "outbound", Text: "…", At: now.Add(-2 * time.Minute)},
		{Direction: "inbound", Text: "YES", At: now.Add(-time.Minute)},
	})
	if v.calls() != 1 || v.got[0].Language != "es" || v.got[0].Stressed {
		t.Fatalf("Spanish thread: calls %d %+v", v.calls(), v.got)
	}
	if v := run(fixedThread{{Direction: "inbound", Text: "where is she?? this is ridiculous", At: now}}); v.calls() != 0 {
		t.Fatalf("an English thread's mood triggered the voice on a status update")
	}
}

const welcomeES = "¡Hola! Soy tu conserje de HO:RA. Te conecto con personas reales que hacen tus recados.\n\nSolo operamos en los cinco distritos de New York City: Manhattan, Brooklyn, Queens, el Bronx y Staten Island.\n\n¿En qué te puedo ayudar hoy?"
