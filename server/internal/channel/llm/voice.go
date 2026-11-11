package llm

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"hora-auth/internal/tracing"
)

// The voice step (D-28, behind CHANNEL_VOICE): after the router has decided
// what to say, the small model (LLM_SMALL_MODEL, Sonnet) rewrites the free
// words of the reply in HO:RA's voice. It decides nothing. Fixed blocks
// never reach it: the router masks them as ⟦n⟧ tokens first, and checks
// everything it gets back by rule (channel/voice.go) before anything is sent.

//go:embed voice.md
var voicePrompt string

// VoicePromptVersion labels voice.md on the trace. Bump it with the file.
const VoicePromptVersion = "v0.6"

// VoicePrompt is the tone guide, the system prompt of every rewrite.
func VoicePrompt() string { return voicePrompt }

// VoiceInput is one reply to rewrite.
type VoiceInput struct {
	// Text is the reply with its fixed blocks masked as ⟦1⟧, ⟦2⟧, ….
	Text string
	// Customer is the person's latest message, for tone and language only
	// ("" when the reply answers nothing they just sent, e.g. a status
	// update).
	Customer string
	// Language is the customer's language as the router's rules read it:
	// "" English, "es", "zh-hant", "zh-hans", "zh" (script unclear), …,
	// "other". Stressed: open with one short acknowledgement. Formal: the
	// customer wrote 您.
	Language string
	Stressed bool
	Formal   bool
	// Copy: an English template improved offline for review
	// (agenttest -voice-copy), not a reply being sent.
	Copy bool
}

// voiceLanguageNames is how the prompt names the language to write.
var voiceLanguageNames = map[string]string{
	"":        "English",
	"es":      "Spanish",
	"fr":      "French",
	"it":      "Italian",
	"pt":      "Portuguese",
	"de":      "German",
	"zh-hant": "Chinese in Traditional characters (繁體中文), the script the customer used",
	"zh-hans": "Chinese in Simplified characters (简体中文), the script the customer used",
	"zh":      "Chinese, in exactly the script the customer used (Traditional or Simplified)",
	"ja":      "Japanese",
	"ko":      "Korean",
	"ru":      "Russian",
	"ar":      "Arabic",
	"he":      "Hebrew",
	"th":      "Thai",
	"hi":      "Hindi",
	"other":   "the language the customer wrote in",
}

// VoiceOutput is the model's rewrite, still masked.
type VoiceOutput struct {
	Text     string
	Language string // ISO 639-1, as the model reports it
	Usage    Usage
}

// Voicer rewrites a reply's free words. *Model implements it.
type Voicer interface {
	Voice(ctx context.Context, in VoiceInput) (VoiceOutput, error)
}

func voiceSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"language", "text"},
		"properties": map[string]any{
			"language": map[string]any{"type": "string"},
			"text":     map[string]any{"type": "string"},
		},
	}
}

// Voice runs on the step's primary model only. The router gives a rewrite a
// short deadline and sends the original on any failure, so the Baseten
// fallback could only add latency — and Sonnet writes the voice, by
// decision, not GLM.
func (m *Model) Voice(ctx context.Context, in VoiceInput) (VoiceOutput, error) {
	return voice(ctx, m.Primary, in)
}

func voice(ctx context.Context, p Provider, in VoiceInput) (VoiceOutput, error) {
	if p == nil {
		return VoiceOutput{}, errors.New("voice: no model")
	}
	var b strings.Builder
	if in.Copy {
		b.WriteString("Use: Copy — one of HO:RA's English message templates, for a human to review. Write in: English.\n\nTemplate to improve:\n" + in.Text)
		return voiceCall(ctx, p, b.String())
	}
	b.WriteString("Use: Reply\n")
	if c := strings.TrimSpace(in.Customer); c != "" {
		fmt.Fprintf(&b, "Customer's latest message (for tone and language only):\n%s\n\n", c)
	} else {
		b.WriteString("Customer's latest message: (none)\n\n")
	}
	lang, ok := voiceLanguageNames[in.Language]
	if !ok {
		lang = voiceLanguageNames["other"]
	}
	fmt.Fprintf(&b, "Write in: %s\n", lang)
	if strings.HasPrefix(in.Language, "zh") {
		if in.Formal {
			b.WriteString("Form of address: the customer used 您 — use 您.\n")
		} else {
			b.WriteString("Form of address: use 你, never 您.\n")
		}
	}
	if in.Stressed {
		b.WriteString("The customer sounds stressed or frustrated: open with one short acknowledgement (at most 25 characters), then the reply.\n")
	} else {
		b.WriteString("The customer is calm: no acknowledgement or apology beyond what the reply already says.\n")
	}
	fmt.Fprintf(&b, "\nHO:RA's reply to rewrite:\n%s", in.Text)
	return voiceCall(ctx, p, b.String())
}

func voiceCall(ctx context.Context, p Provider, user string) (VoiceOutput, error) {
	// The reply's own words are on channel.send already; the model's
	// output is either what is sent (also on channel.send) or rejected —
	// and a rejected rewrite is never kept (D-28).
	ctx = tracing.WithoutText(ctx)
	start := time.Now()
	out, u, err := p.CompleteJSON(ctx, voicePrompt, user, "voice", voiceSchema(), 600)
	if u.Latency == 0 {
		u.Latency = time.Since(start)
	}
	if err != nil {
		return VoiceOutput{Usage: u}, fmt.Errorf("voice: %w", err)
	}
	var r struct {
		Language string `json:"language"`
		Text     string `json:"text"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(out)), &r); err != nil {
		return VoiceOutput{Usage: u}, errors.New("voice: undecodable output")
	}
	return VoiceOutput{Text: r.Text, Language: strings.ToLower(strings.TrimSpace(r.Language)), Usage: u}, nil
}
