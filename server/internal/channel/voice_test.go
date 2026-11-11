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
	summaryish := "Laundry pickup\nTomorrow, 3:00 PM · about 30 min\nEstimate: $42 (you pay for the time actually used)\n\nReply YES to book, or tell me what to change."
	_, sumBlocks := MaskForVoice(summaryish)
	cases := []struct {
		name, orig, rewrite string
		blocks              []string
		lang, want          string
	}{
		{"warmer, same facts", "Got it. What would you like to tell them?", "Sure thing. What would you like me to tell them?", nil, "en", ""},
		{"dropped price", "Cancelling now costs $12, the base fee.", "Cancelling now costs the base fee.", nil, "en", "price_changed"},
		{"changed price", "Cancelling now costs $12.", "Cancelling now costs $15.", nil, "en", "price_changed"},
		{"changed time", "Alex will be there at 3:00 PM.", "Alex will be there at 4:00 PM.", nil, "en", "time_changed"},
		{"time reformatted", "Alex will be there at 3:00 PM.", "Alex will be there at 3pm.", nil, "en", "time_changed"},
		{"dropped number", "Up to 10 extra minutes are fine.", "A few extra minutes are fine.", nil, "en", "number_dropped"},
		{"added number", "I'll text you when they arrive.", "I'll text you in 5 minutes.", nil, "en", "number_added"},
		{"number in words", "I'll text you when they arrive.", "I'll text you in five.", nil, "en", "number_added"},
		{"added URL", "Tell me what you need and when.", "Tell me what you need and when, or see https://my-hora.com.", nil, "en", "url_added"},
		{"added bare domain", "Tell me what you need.", "Tell me what you need, or visit my-hora.com.", nil, "en", "url_added"},
		{"dropped email", "Please email info@my-hora.com and we'll follow up.", "Please write to us and we'll follow up.", nil, "en", "url_dropped"},
		{"added question", "Got it. I'm here whenever you need a hand.", "Got it. Anything else I can help with?", nil, "en", "question_added"},
		{"missing YES line", summaryish, strings.Replace(summaryish, "\n\nReply YES to book, or tell me what to change.", "\n\nShall I book it?", 1), sumBlocks, "en", "block_missing"},
		{"YES line reworded", summaryish, strings.Replace(summaryish, "Reply YES to book", "Say YES to book", 1), sumBlocks, "en", "block_missing"},
		{"blocks out of order", "A ⟦1⟧ B ⟦2⟧", "x second y first", []string{"first", "second"}, "en", "block_missing"},
		{"added YES", "Tell me what to change.", "Tell me what to change, or send YES.", nil, "en", "keyword_added"},
		{"dropped name", "Alex is on the way to Hudson Cleaners.", "Your supporter is on the way.", nil, "en", "name_dropped"},
		{"dropped borough", "We run errands in Manhattan and Brooklyn.", "We run errands in Manhattan.", nil, "en", "name_dropped"},
		{"too long", "Got it.", "Got it — thank you so much for letting me know, I really appreciate it.", nil, "en", "too_long"},
		{"two emoji", "Got it.", "Got it 👍🙂", nil, "en", "emoji"},
		{"one emoji", "Got it, I'm here whenever you need a hand.", "Got it 👍 I'm here whenever you need a hand.", nil, "en", ""},
		{"exclamation chain", "Got it.", "Got it!!", nil, "en", "exclamation"},
		{"as an AI", "I can only read text for now.", "As an AI, I can only read text for now.", nil, "en", "as_an_ai"},
		{"new promise", "I'm finding a supporter now.", "I'm finding a supporter now, guaranteed.", nil, "en", "commitment_added"},
		{"new price word", "I won't book that.", "I won't book that, and it's free.", nil, "en", "commitment_added"},
		{"empty", "Got it.", "  ", nil, "en", "empty"},
		{"spanish rewrite keeps names", "Which borough is Broadway in?", "¿En qué distrito está Broadway?", nil, "es", ""},
		{"spanish rewrite drops name", "Which borough is Broadway in?", "¿En qué distrito está?", nil, "es", "name_dropped"},
		{"phone added", "Tell me what you need.", "Tell me what you need, or call 212-555-0100.", nil, "en", "number_added"},
	}
	for _, c := range cases {
		if got := CheckVoice(c.orig, c.rewrite, c.blocks, c.lang); got != c.want {
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
	for name, out := range fixed {
		res := g.r.VoiceRewrite(context.Background(), out)
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
		res := g.r.VoiceRewrite(context.Background(), Outbound{Channel: IMessage, Text: orig})
		if res.Outcome != VoiceFallback || res.Reason != c.reason || res.Text != orig {
			t.Errorf("%s: %s/%s %q, want fallback/%s and the original", c.name, res.Outcome, res.Reason, res.Text, c.reason)
		}
	}
	// And a good one goes through, with the block put back.
	g := newRig(llm.IntentNewTask)
	g.r.Voice = &fakeVoice{f: say("Sorry, something went wrong on our end. ⟦1⟧")}
	res := g.r.VoiceRewrite(context.Background(), Outbound{Channel: IMessage, Text: orig})
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
		return llm.VoiceOutput{Text: strings.Replace(in.Text, "Hi! I'm your", "Hi, I'm your", 1), Language: "en"}, nil
	}}
	g.r.Voice = v
	h := newHandler(g.ad, g.st, g.r)
	h.route(Inbound{Channel: IMessage, ProviderMessageID: "pm-v1", From: me, Text: "thanks so much", ReceivedAt: time.Now()})
	if v.calls() == 0 {
		t.Fatalf("voice not asked; sent %q", g.ad.last())
	}
	if got := v.got[0].Customer; got != "thanks so much" {
		t.Errorf("customer words %q", got)
	}
	if want := strings.Replace(CopyWelcome, "Hi! I'm your", "Hi, I'm your", 1); g.ad.last() != want {
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
		if a["hora.voice.outcome"] != VoiceRewritten || a["hora.voice.latency_ms"] == "" {
			t.Errorf("span attrs %v", a)
		}
		for k, val := range a {
			if strings.Contains(val, "concierge") || strings.Contains(val, "thanks") || k == "input.value" || k == "output.value" {
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
	if err := g.r.Reply(context.Background(), Inbound{Channel: IMessage, From: me}, CopyBooked, ""); err != nil {
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
	ctx := context.Background()
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
