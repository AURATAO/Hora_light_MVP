//go:build liveeval

package channel

// The production transcripts of 2026-11-13/14 through the real router on
// real models (scope and classifier on LLM_SMALL_MODEL, parse on
// LLM_PARSE_MODEL), on WhatsApp and iMessage, an unlinked number, the
// thread recorded as channel_messages records it. Prints every reply. It
// costs money; it only builds with the tag:
//
//	set -a; source .env; set +a
//	CHANNEL_PARSE_PROMPT_FILE=/path/to/prompt.txt \
//	  go test -tags liveeval -run TestLiveScopeTranscripts -v ./internal/channel

import (
	"os"
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

func TestLiveScopeTranscripts(t *testing.T) {
	prov := llm.ProvidersFromEnv()
	if prov.AnthropicKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	steps := llm.StepsFromEnv(prov, func(s string) { t.Log("warn:", s) })
	t.Logf("small=%s parse=%s", steps.Small.Name(), steps.Parse.Name())
	prompt := "Extract the errand as JSON for the task form."
	if f := os.Getenv("CHANNEL_PARSE_PROMPT_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		prompt = string(b)
	}
	sr := scopeSet(t)
	for _, ch := range bothChannels {
		for _, transcript := range [][]string{{heyTea1, heyTea2}, {perfect}} {
			c := chatRigOn(t, ch, nil, nil)
			c.r.Go = nil
			c.r.Scope, c.r.Classifier, c.r.Parser, c.r.Splitter, c.r.QuestionReader = steps.Small, steps.Small, steps.Parse, steps.Parse, steps.Small
			c.r.Prompt = func(time.Time) string { return prompt }
			for _, msg := range transcript {
				start := time.Now()
				replies := c.send(t, msg)
				t.Logf("[%s] customer: %s   (%dms)", ch, msg, time.Since(start).Milliseconds())
				for _, r := range replies {
					t.Logf("[%s] HO:RA:    %s", ch, strings.ReplaceAll(r, "\n", " / "))
				}
				noWelcome(t, ch, msg, replies)
				for _, r := range replies {
					ref := sr.RefusalByReply(r)
					switch {
					case msg == perfect && (ref == nil || ref.ID != "dating"):
						t.Errorf("[%s] %q: want the dating decline, got %q", ch, msg, r)
					case msg != perfect && ref != nil:
						t.Errorf("[%s] %q: refused as %s", ch, msg, ref.ID)
					}
				}
			}
		}
	}
}
