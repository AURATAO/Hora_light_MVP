package llm

import "testing"

func TestCompanionshipIsPausedUnlessEnabled(t *testing.T) {
	s, err := Playbooks()
	if err != nil {
		t.Fatal(err)
	}
	asks := []string{
		"can someone keep me company for a walk in the park?",
		"I'd like someone to grab a coffee with me",
		"walk with me to Union Square",
	}
	errands := []string{
		"pick up my dry cleaning",
		"grab my coffee and a bagel and bring it to my office",
		"wait in line for me at the Apple Store",
		"buy Advil at CVS",
	}

	t.Setenv("COMPANIONSHIP_ENABLED", "")
	for _, msg := range asks {
		ref := s.PausedRefusal(msg, "")
		if ref == nil || ref.ID != CompanionshipPausedID || ref.Reply == "" || ref.Alert {
			t.Errorf("paused, %q: %+v, want the paused refusal (never an alert)", msg, ref)
		}
	}
	for _, msg := range errands {
		if ref := s.PausedRefusal(msg, "delivery"); ref != nil {
			t.Errorf("paused, %q: refused; an errand is not companionship", msg)
		}
	}
	// The category alone is enough, in either spelling, with no words and
	// no playbooks at hand.
	for _, cat := range []string{"companionship", "companion"} {
		if (*PlaybookSet)(nil).PausedRefusal("", cat) == nil {
			t.Errorf("paused, category %q: not refused", cat)
		}
	}

	// Back on: one variable, nothing else.
	t.Setenv("COMPANIONSHIP_ENABLED", "1")
	for _, msg := range asks {
		if ref := s.PausedRefusal(msg, "companionship"); ref != nil {
			t.Errorf("enabled, %q: still refused", msg)
		}
	}
}
