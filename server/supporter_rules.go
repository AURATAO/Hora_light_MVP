package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// The supporter rules the apps show (Aura 2026-11-16). They were a list
// inside the app, so a wording change after a legal review meant an App
// Review round. Now GET /config carries them and the app shows what it is
// sent; its built-in copy is only the fallback.

//go:embed supporter_rules.json
var supporterRulesJSON []byte

type supporterRule struct {
	Icon  string `json:"icon"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

type supporterRuleSet struct {
	Version string          `json:"version"`
	Items   []supporterRule `json:"items"`
}

var (
	supporterRulesOnce sync.Once
	supporterRulesVal  *supporterRuleSet
	supporterRulesErr  error
)

func parseSupporterRules(b []byte) (*supporterRuleSet, error) {
	var s supporterRuleSet
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s.Version) == "" || len(s.Items) == 0 {
		return nil, fmt.Errorf("supporter rules: a version and at least one rule are required")
	}
	for i, it := range s.Items {
		if strings.TrimSpace(it.Title) == "" || strings.TrimSpace(it.Body) == "" {
			return nil, fmt.Errorf("supporter rules: rule %d needs a title and a body", i)
		}
	}
	return &s, nil
}

func supporterRules() (*supporterRuleSet, error) {
	supporterRulesOnce.Do(func() { supporterRulesVal, supporterRulesErr = parseSupporterRules(supporterRulesJSON) })
	return supporterRulesVal, supporterRulesErr
}
