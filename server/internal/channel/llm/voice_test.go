package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeProvider struct {
	out       string
	err       error
	sys, user string
}

func (f *fakeProvider) Name() string { return "fake:voice" }
func (f *fakeProvider) CompleteJSON(_ context.Context, system, user, _ string, _ map[string]any, _ int) (string, Usage, error) {
	f.sys, f.user = system, user
	return f.out, Usage{}, f.err
}

func TestVoiceDecodes(t *testing.T) {
	p := &fakeProvider{out: `{"language":"ES","text":"¿En qué distrito está ⟦1⟧?"}`}
	got, err := voice(context.Background(), p, VoiceInput{Text: "Which borough is ⟦1⟧ in?", Customer: "necesito lavandería", Language: "es"})
	if err != nil || got.Text != "¿En qué distrito está ⟦1⟧?" || got.Language != "es" {
		t.Fatalf("%+v %v", got, err)
	}
	if p.sys != VoicePrompt() || !strings.Contains(p.user, "necesito lavandería") || !strings.Contains(p.user, "Which borough is ⟦1⟧ in?") {
		t.Fatalf("prompt: %q", p.user)
	}
	if !strings.Contains(p.user, "Write in: Spanish") || !strings.Contains(p.user, "calm") {
		t.Fatalf("instructions: %q", p.user)
	}
	_, _ = voice(context.Background(), p, VoiceInput{Text: "x", Customer: "還沒到嗎??", Language: "zh-hant", Stressed: true})
	if !strings.Contains(p.user, "Traditional") || !strings.Contains(p.user, "use 你, never 您") || !strings.Contains(p.user, "stressed") {
		t.Fatalf("zh-hant instructions: %q", p.user)
	}
	_, _ = voice(context.Background(), p, VoiceInput{Text: "Just your first name is fine.", Copy: true})
	if !strings.Contains(p.user, "Use: Copy") || strings.Contains(p.user, "Customer") {
		t.Fatalf("copy instructions: %q", p.user)
	}
	if _, err := voice(context.Background(), &fakeProvider{out: "not json"}, VoiceInput{Text: "x"}); err == nil {
		t.Fatal("undecodable output must fail")
	}
	if _, err := voice(context.Background(), &fakeProvider{err: errors.New("status 529")}, VoiceInput{Text: "x"}); err == nil {
		t.Fatal("a provider error must fail")
	}
}

// The tone guide keeps the rules the router's check enforces.
func TestVoicePromptRules(t *testing.T) {
	p := VoicePrompt()
	for _, w := range []string{"⟦1⟧", "Never add a question", `"!!"`, "emoji", "As an AI", "Traditional in → Traditional out", "您", "at most 25 characters", "as soon as"} {
		if !strings.Contains(p, w) {
			t.Errorf("voice.md lost %q", w)
		}
	}
}
