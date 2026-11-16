package channel

import (
	"os"
	"testing"
)

// The suite describes companionship as it works when it is offered (the
// playbook, the scope evals, the routing paraphrases). The pause
// (llm/paused.go) has its own tests, which switch this off.
func TestMain(m *testing.M) {
	os.Setenv("COMPANIONSHIP_ENABLED", "1")
	os.Exit(m.Run())
}
