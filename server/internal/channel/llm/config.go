package llm

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Steps is the model behind each channel step, chosen by configuration
// (PLAN §9: by the eval, never by feel).
//
//	LLM_AGENT_MODEL  Propose (the requester agent)          default anthropic:claude-sonnet-5-5
//	LLM_PARSE_MODEL  ParseTask and SplitErrands             default anthropic:claude-sonnet-5-5
//	LLM_SMALL_MODEL  NeedsPurchase, Classify, ClassifyInContext  default baseten:zai-org/GLM-5.3-Flash
//
// Each value is "anthropic:<model>" or "baseten:<model>". An Anthropic step
// falls back, call by call, to the Baseten model (LLM_MODEL, default
// zai-org/GLM-5.3-Flash) when Anthropic errors or times out.
type Steps struct {
	Agent, Parse, Small *Model
}

const (
	DefaultAnthropicModel = "claude-sonnet-5-5"
	DefaultBasetenModel   = "zai-org/GLM-5.3-Flash"
	DefaultBasetenBaseURL = "https://inference.baseten.co/v1"
)

// Providers holds what is needed to build any Provider. Keys never leave
// it: not in Name, not in logs.
type Providers struct {
	AnthropicKey    string
	AnthropicEffort string // "" = low
	BasetenBaseURL  string
	BasetenKey      string
	BasetenModel    string // the fallback model
	BasetenExtra    map[string]any
	Timeout         time.Duration // per call; 0 = 45s
}

// ProvidersFromEnv reads the keys and Baseten settings:
//
//	ANTHROPIC_API_KEY
//	LLM_ANTHROPIC_EFFORT    low (default) | medium | high
//	LLM_BASE_URL            default https://inference.baseten.co/v1
//	LLM_API_KEY             falls back to BASETEN_API_KEY
//	LLM_MODEL               the Baseten fallback model, default zai-org/GLM-5.3-Flash
//	LLM_DISABLE_THINKING=1  sends chat_template_kwargs.enable_thinking=false to Baseten
func ProvidersFromEnv() Providers {
	p := Providers{
		AnthropicKey:    env("ANTHROPIC_API_KEY", ""),
		AnthropicEffort: env("LLM_ANTHROPIC_EFFORT", ""),
		BasetenBaseURL:  env("LLM_BASE_URL", DefaultBasetenBaseURL),
		BasetenKey:      env("LLM_API_KEY", env("BASETEN_API_KEY", "")),
		BasetenModel:    env("LLM_MODEL", DefaultBasetenModel),
	}
	if os.Getenv("LLM_DISABLE_THINKING") == "1" {
		p.BasetenExtra = map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}}
	}
	return p
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// Provider builds one provider for s.
func (p Providers) Provider(s Spec) Provider {
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 45 * time.Second
	}
	if s.Provider == "anthropic" {
		return &Anthropic{APIKey: p.AnthropicKey, Model: s.Model, Effort: p.AnthropicEffort, HTTP: &http.Client{Timeout: timeout}}
	}
	return &OpenAICompat{BaseURL: p.BasetenBaseURL, APIKey: p.BasetenKey, Model: s.Model, ExtraBody: p.BasetenExtra, HTTP: &http.Client{Timeout: timeout}}
}

// Step builds a step's Model from its spec. An Anthropic step gets the
// Baseten fallback when Baseten has a key; an Anthropic step with no
// Anthropic key runs on Baseten (warn is told why).
func (p Providers) Step(spec Spec, warn func(string)) *Model {
	fb := Spec{Provider: "baseten", Model: firstNonEmptyStr(p.BasetenModel, DefaultBasetenModel)}
	if spec.Provider == "anthropic" && p.AnthropicKey == "" {
		warn(fmt.Sprintf("%s: ANTHROPIC_API_KEY not set — using %s", spec, fb))
		spec = fb
	}
	m := &Model{Primary: p.Provider(spec)}
	if spec.Provider == "anthropic" {
		if p.BasetenKey != "" {
			m.Fallback = p.Provider(fb)
		} else {
			warn(fmt.Sprintf("%s: no Baseten key — no fallback", spec))
		}
	}
	return m
}

// StepsFromEnv builds the three steps. A malformed spec is reported
// through warn and that step keeps its default — the chat keeps working, and
// the startup log says which model each step really got.
func StepsFromEnv(p Providers, warn func(string)) Steps {
	get := func(k, def string) Spec {
		s, err := ParseSpec(env(k, def))
		if err != nil {
			warn(fmt.Sprintf("%s: %v — using %s", k, err, def))
			s, _ = ParseSpec(def)
		}
		return s
	}
	return Steps{
		Agent: p.Step(get("LLM_AGENT_MODEL", "anthropic:"+DefaultAnthropicModel), warn),
		Parse: p.Step(get("LLM_PARSE_MODEL", "anthropic:"+DefaultAnthropicModel), warn),
		Small: p.Step(get("LLM_SMALL_MODEL", "baseten:"+DefaultBasetenModel), warn),
	}
}
