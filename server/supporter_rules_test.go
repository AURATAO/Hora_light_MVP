package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSupporterRulesFileParsesAndIsServed(t *testing.T) {
	rules, err := supporterRules()
	if err != nil {
		t.Fatalf("supporter_rules.json: %v", err)
	}
	// The pilot's limits each have a rule (Aura 2026-11-16).
	all := ""
	for _, it := range rules.Items {
		all += it.Title + " " + it.Body + "\n"
	}
	for _, must := range []string{"say no", "minors", "ride", "alcohol", "tobacco", "cannabis", "prescription"} {
		if !strings.Contains(strings.ToLower(all), must) {
			t.Errorf("no rule mentions %q", must)
		}
	}
	// Companionship is paused: the rules do not describe it.
	if strings.Contains(strings.ToLower(all), "companion") {
		t.Error("a rule mentions companionship, which is not offered")
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/config", nil)
	getAppConfig(c)
	body := decodeFirstJSON(t, w)
	sr, _ := body["supporter_rules"].(map[string]any)
	if sr == nil || sr["version"] != rules.Version || len(sr["items"].([]any)) != len(rules.Items) {
		t.Errorf("GET /config supporter_rules = %v", body["supporter_rules"])
	}
	if _, ok := body["dispatch"]; !ok {
		t.Error("GET /config lost its dispatch flag")
	}
}

func TestParseSupporterRulesRefusesBrokenFiles(t *testing.T) {
	for name, body := range map[string]string{
		"no version": `{"items":[{"title":"t","body":"b"}]}`,
		"no rules":   `{"version":"1","items":[]}`,
		"no body":    `{"version":"1","items":[{"title":"t"}]}`,
	} {
		if _, err := parseSupporterRules([]byte(body)); err == nil {
			t.Errorf("%s: parsed, want an error", name)
		}
	}
}
