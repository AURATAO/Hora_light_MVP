package channel

import "testing"

// D-17 R-2 / R-3 word rules, as ruled 2026-10-22.
func TestApprovalWords(t *testing.T) {
	for _, tc := range []struct {
		msg               string
		yes, reaction, no bool
	}{
		{"yes", true, false, false}, {"Yesss!!", true, false, false}, {"Kk", true, false, false},
		{"Okie", true, false, false}, {"Hell yes", true, false, false}, {"100%", true, false, false},
		{"Sounds good!", true, false, false}, {"I’m down", true, false, false}, {"let's do it", true, false, false},
		{"Of course", true, false, false}, {"可以", true, false, false}, {"✅", true, false, false},
		{"Great", true, true, false}, {"Perfect!", true, true, false}, {"👍", true, true, false}, {"Fine.", true, true, false},
		{"no", false, false, true}, {"no thanks", false, false, true}, {"No, he needs to finish now", false, false, true},
		{"nope, too much", false, false, true}, {"不要", false, false, true},
		{"no problem", false, false, false}, {"no worries", false, false, false}, {"not ok", false, false, false},
		{"okay so", false, false, false}, {"ok不要", false, false, false}, {"yeah right, $10 for milk?", false, false, false},
	} {
		y, r := approvalYes(tc.msg)
		if y != tc.yes || r != tc.reaction || approvalNo(tc.msg) != tc.no {
			t.Errorf("%q: yes=%v reaction=%v no=%v", tc.msg, y, r, approvalNo(tc.msg))
		}
	}
	for _, tc := range []struct {
		msg, rest      string
		reaction, cond bool
		ok             bool
	}{
		{"yes but tell him to hurry", "but tell him to hurry", false, false, true},
		{"Ok, thanks Alex!", "thanks Alex!", false, false, true},
		{"Yesss — and grab napkins", "and grab napkins", false, false, true},
		{"yes but max $5", "but max $5", false, true, true},
		{"yes if it's organic", "if it's organic", false, true, true},
		{"sure, why?", "why?", false, true, true},
		{"ok but not the money", "but not the money", false, true, true},
		{"yes, only 10 minutes", "only 10 minutes", false, true, true},
		{"好，不要太貴", "不要太貴", false, true, true},
		{"great, he's here", "he's here", true, false, true},
		{"okay so what's the item", "so what's the item", false, false, true},
		{"yes", "", false, false, false},
		{"ok不要", "", false, false, false},
		{"no problem, go ahead", "", false, false, false},
		{"yesterday was fine", "", false, false, false},
	} {
		rest, r, c, ok := yesWithMessage(tc.msg)
		if ok != tc.ok || rest != tc.rest || r != tc.reaction || c != tc.cond {
			t.Errorf("%q: rest=%q reaction=%v cond=%v ok=%v", tc.msg, rest, r, c, ok)
		}
	}
}
