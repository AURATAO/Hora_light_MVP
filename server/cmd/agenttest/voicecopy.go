package main

// -voice-copy: warmer English for the customer-facing Copy* templates in
// internal/channel/copy.go, proposed once by Sonnet (LLM_SMALL_MODEL) under
// voice.md, for a human to review (D-28). Nothing is applied to copy.go.
//
// Each template is masked as at runtime (MaskTemplateForVoice: format
// verbs, YES/STOP/HELP lines, prices, links, quoted titles …) and the
// proposal must pass the same rule check (CheckVoice). A proposal that
// fails it is reported as kept, with the reason. Excluded outright: the
// copies other code matches by exact text, opt-in / cancel-hint (TCPA and
// "cancel task"), supporter-facing lines, summary lines, and fragments
// that are glued into other sentences.
//
//	go run ./cmd/agenttest -voice-copy [-copy-out ../skills/decisions/D-28-copy-proposals.md]

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"hora-auth/internal/channel"
	"hora-auth/internal/channel/llm"
)

type copyConst struct {
	Name, Value string
	Line        int
}

// copyConsts reads every Copy* string constant declared in copy.go, with
// its value as the compiler sees it (concatenations resolved).
func copyConsts(dir string) ([]copyConst, error) {
	fs := token.NewFileSet()
	pkgs, err := parser.ParseDir(fs, dir, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for _, p := range pkgs {
		for _, f := range p.Files {
			files = append(files, f)
		}
	}
	conf := types.Config{Importer: importer.ForCompiler(fs, "source", nil), Error: func(error) {}}
	pkg, _ := conf.Check("channel", fs, files, nil)
	if pkg == nil {
		return nil, fmt.Errorf("type-check %s failed", dir)
	}
	var out []copyConst
	for _, n := range pkg.Scope().Names() {
		c, ok := pkg.Scope().Lookup(n).(*types.Const)
		if !ok || !strings.HasPrefix(n, "Copy") || c.Val().Kind() != constant.String {
			continue
		}
		pos := fs.Position(c.Pos())
		if filepath.Base(pos.Filename) != "copy.go" {
			continue
		}
		out = append(out, copyConst{Name: n, Value: constant.StringVal(c.Val()), Line: pos.Line})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out, nil
}

// copyExcluded: why a template is not proposed at all ("" = proposed).
func copyExcluded(c copyConst) string {
	switch c.Name {
	case "CopyParseFailed":
		return "matched by exact text (lastReplyWasParseFailure)"
	case "CopyOptInWelcome":
		return "opt-in reply (TCPA)"
	case "CopyCancelHint":
		return `"cancel task" wording`
	case "CopyAutoExtendOn", "CopyAutoExtendOff", "CopyNoteForSupporter", "CopyPhotoLine", "CopyPhotosLine":
		return "a line of the booking summary (word for word)"
	}
	if strings.HasPrefix(c.Name, "CopyToSupporter") {
		return "sent to the supporter, not the customer"
	}
	v := c.Value
	if strings.HasPrefix(v, " ") || strings.HasSuffix(v, " ") {
		return "a fragment glued into another sentence"
	}
	last := []rune(strings.TrimSpace(v))
	if len(last) > 0 && !strings.ContainsRune(".?!)”:", last[len(last)-1]) && !strings.HasSuffix(v, "%s") {
		return "a fragment glued into another sentence"
	}
	return ""
}

var copySituations = []struct{ name, re string }{
	{"Greeting and how HO:RA works", `Welcome|HelloAgain|About|^CopyAck`},
	{"Service area and addresses", `Area|Borough|Address|^CopyAskWhere|^CopyAskWhatWhere`},
	{"Waiting on the customer", `StillWaiting|StillPending|ConfirmReask|ApprovalFirst|ErrandAfterApproval|BackToDraft`},
	{"Supporter requests (approve / decline / items)", `Approv|Declin|Item`},
	{"Cancelling", `Cancel`},
	{"Where is my supporter", `^CopyWhere|OnMyWay`},
	{"Talking to a HO:RA teammate", `Handoff|3DS`},
	{"Passing messages to the supporter", `Relay|PassedOn|PassedThanks|CouldntPass|NotPassed|Hold|WhichTask|WhichSupporter|AskedToHurry|LetKnow|Ambiguous`},
	{"Drafting an errand (name, budget, extra time)", `AskName|Budget|ExtraTime|TooMany|Draft`},
	{"Booking and payment", `Book|OneStep|Setup|Card|Retry|Post|Summary|Price|TimePassed|Dropped`},
	{"Photos and receipts", `Photo|Receipt`},
	{"Other", `.`},
}

func copySituation(name string) string {
	for _, s := range copySituations {
		if regexp.MustCompile(s.re).MatchString(name) {
			return s.name
		}
	}
	return "Other"
}

type copyProposal struct {
	Name      string `json:"name"`
	Situation string `json:"situation"`
	Before    string `json:"before"`
	After     string `json:"after,omitempty"`
	// Status: proposed | unchanged | rejected | nothing_free | excluded.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// Rejected: a proposal the rule check refused (for the reviewer).
	Rejected string `json:"rejected,omitempty"`
}

// rerenderVoiceCopy rebuilds the review file from its .json (grouping or
// layout changes) without asking the model again.
func rerenderVoiceCopy(spec llm.Spec, outPath string) int {
	jsonPath := strings.TrimSuffix(outPath, filepath.Ext(outPath)) + ".json"
	b, err := os.ReadFile(jsonPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var props []copyProposal
	if err := json.Unmarshal(b, &props); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for i := range props {
		props[i].Situation = copySituation(props[i].Name)
	}
	if err := writeCopyReview(outPath, props, spec); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	b, _ = json.MarshalIndent(props, "", "  ")
	if err := os.WriteFile(jsonPath, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("re-rendered %s from %s\n", outPath, jsonPath)
	return 0
}

func runVoiceCopy(spec llm.Spec, providers llm.Providers, outPath string, parallel int) int {
	consts, err := copyConsts("internal/channel")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	model := &llm.Model{Primary: providers.Provider(spec)}
	props := make([]copyProposal, len(consts))
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(1, parallel))
	for i, c := range consts {
		p := copyProposal{Name: c.Name, Situation: copySituation(c.Name), Before: c.Value}
		if why := copyExcluded(c); why != "" {
			p.Status, p.Reason = "excluded", why
			props[i] = p
			continue
		}
		masked, blocks := channel.MaskTemplateForVoice(c.Value)
		if !freeWords(masked) {
			p.Status, p.Reason = "nothing_free", "every word is fixed (YES/STOP lines, prices, links, values)"
			props[i] = p
			continue
		}
		wg.Add(1)
		go func(i int, p copyProposal, masked string, blocks []string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var got llm.VoiceOutput
			var err error
			for try := 0; try < 2; try++ {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				got, err = model.Voice(ctx, llm.VoiceInput{Text: masked, Copy: true})
				cancel()
				if err == nil {
					break
				}
			}
			switch {
			case err != nil:
				p.Status, p.Reason = "rejected", "model error: "+err.Error()
			default:
				after, ok := channel.UnmaskVoice(got.Text, blocks)
				switch {
				case !ok:
					p.Status, p.Reason, p.Rejected = "rejected", "tokens", got.Text
				case after == p.Before:
					p.Status = "unchanged"
				default:
					if why := channel.CheckVoice(p.Before, after, blocks, channel.VoiceCheck{Wrote: got.Language}); why != "" {
						p.Status, p.Reason, p.Rejected = "rejected", why, after
					} else {
						p.Status, p.After = "proposed", after
					}
				}
			}
			props[i] = p
			fmt.Fprintf(os.Stderr, ".")
		}(i, p, masked, blocks)
	}
	wg.Wait()
	fmt.Fprintln(os.Stderr)
	if err := writeCopyReview(outPath, props, spec); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	jsonPath := strings.TrimSuffix(outPath, filepath.Ext(outPath)) + ".json"
	b, _ := json.MarshalIndent(props, "", "  ")
	if err := os.WriteFile(jsonPath, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	n := map[string]int{}
	for _, p := range props {
		n[p.Status]++
	}
	fmt.Printf("%d templates in copy.go: %d proposed, %d unchanged, %d rejected by the check, %d nothing to reword, %d excluded\nwrote %s and %s\n",
		len(props), n["proposed"], n["unchanged"], n["rejected"], n["nothing_free"], n["excluded"], outPath, jsonPath)
	return 0
}

func freeWords(masked string) bool {
	n := 0
	for _, c := range regexp.MustCompile(`⟦\d+⟧`).ReplaceAllString(masked, "") {
		if unicode.IsLetter(c) {
			n++
		}
	}
	return n >= 3
}

func writeCopyReview(path string, props []copyProposal, spec llm.Spec) error {
	var b strings.Builder
	n := map[string]int{}
	for _, p := range props {
		n[p.Status]++
	}
	fmt.Fprintf(&b, "# D-28: warmer English copy — proposals for review\n\n")
	fmt.Fprintf(&b, "Generated %s by `go run ./cmd/agenttest -voice-copy` (%s, voice.md %s). **Not applied to copy.go.**\n\n", time.Now().Format("2006-01-02"), spec, llm.VoicePromptVersion)
	fmt.Fprintf(&b, "Every proposal passed the runtime rule check (`CheckVoice`): placeholders, prices, times, YES/STOP/HELP lines, links, quoted titles and names unchanged; no new number, link, question or promise; at most 10%% longer. ")
	fmt.Fprintf(&b, "%d templates: **%d proposed**, %d the model kept as they are, %d proposals refused by the check (listed, with the refused text), %d with nothing to reword, %d excluded.\n\n", len(props), n["proposed"], n["unchanged"], n["rejected"], n["nothing_free"], n["excluded"])
	b.WriteString("Format: `CopyName`: \"before\" → \"after\". `\\n` is a line break. The machine-readable list is in the `.json` next to this file.\n")
	groups := map[string][]copyProposal{}
	for _, p := range props {
		if p.Status == "proposed" {
			groups[p.Situation] = append(groups[p.Situation], p)
		}
	}
	for _, s := range copySituations {
		ps := groups[s.name]
		if len(ps) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n\n", s.name)
		for _, p := range ps {
			fmt.Fprintf(&b, "- `%s`: %s → %s\n", p.Name, quote(p.Before), quote(p.After))
		}
	}
	b.WriteString("\n## Refused by the rule check (kept as they are)\n\n")
	for _, p := range props {
		if p.Status == "rejected" {
			fmt.Fprintf(&b, "- `%s` (%s): %s — refused: %s\n", p.Name, p.Reason, quote(p.Before), quote(p.Rejected))
		}
	}
	b.WriteString("\n## Kept by the model (already reads well)\n\n")
	for _, p := range props {
		if p.Status == "unchanged" {
			fmt.Fprintf(&b, "- `%s`: %s\n", p.Name, quote(p.Before))
		}
	}
	b.WriteString("\n## Not proposed\n\n")
	for _, p := range props {
		if p.Status == "excluded" || p.Status == "nothing_free" {
			why := p.Reason
			fmt.Fprintf(&b, "- `%s` — %s\n", p.Name, why)
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func quote(s string) string {
	s = strings.ReplaceAll(s, "\n", `\n`)
	return "\"" + s + "\""
}
