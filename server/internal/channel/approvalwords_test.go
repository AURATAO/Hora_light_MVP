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
		{"Ok, thanks Alex!", "thanks alex", false, true, true},              // a name not known as the supporter: passed on (round 4)
		{"Yesss — and grab napkins", "and grab napkins", false, true, true}, // not a harmless shape (round 4)
		{"yes but max $5", "but max $5", false, true, true},
		{"yes if it's organic", "if it's organic", false, true, true},
		{"sure, why?", "why?", false, true, true},
		{"ok but not the money", "but not the money", false, true, true},
		{"yes, only 10 minutes", "only 10 minutes", false, true, true},
		{"好，不要太貴", "不要太貴", false, true, true},
		{"great, he's here", "he's here", true, true, true},                  // not a harmless tail (I-AE)
		{"okay so what's the item", "so what's the item", false, true, true}, // a question word: conditional
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

// Review round 1 (M3.2d): every way a tail could carry a condition, a
// refusal or a complaint is conditional — never an approval.
func TestApprovalTailsErrConditional(t *testing.T) {
	cond := []string{
		"yes, don’t go over", "yes, don't go over", "ok, can’t do more", "sure, won't need it",
		"yes, nothing more", "ok none of that", "yeah nah", "ok nvm", "ok nevermind", "sure nope",
		"yes cancel", "ok stop",
		"yes but five dollars", "ok make it ten", "好，十塊", "ok １０", "yes ＄５",
		"ok let me think", "sure let me check with my wife", "ok what for", "ok? wait",
		"yeah that's too much", "ok that's a lot", "ya know what forget it",
		"si, ma non più di 5", "sí, pero solo uno",
		"ok 👎", "sure 🙄", "yes 😡", "ok ❌",
	}
	for _, msg := range cond {
		// Never an approval: not a plain yes, and either not a yes-with-
		// message at all or a conditional one.
		_, _, c, ok := yesWithMessage(msg)
		if y, _ := approvalYes(msg); y || (ok && !c) {
			t.Errorf("%q: plain yes=%v, with message ok=%v conditional=%v", msg, y, ok, c)
		}
	}
	// Every no-word, straight or curly, as a tail.
	for w := range noWords {
		for _, msg := range []string{"yes " + w, "ok, " + w} {
			if _, _, c, ok := yesWithMessage(msg); ok && !c {
				t.Errorf("%q: a no-word tail must be conditional", msg)
			}
			if y, _ := approvalYes(msg); y {
				t.Errorf("%q: must not be a plain yes", msg)
			}
		}
	}
}

func TestApprovalWordsEdges(t *testing.T) {
	plain := []string{"yes no problem", "sure, no worries", "ok no rush", "yes 100%", "sure thing", "ok great", "book it", "👍🏽", "sí", "Ｙｅｓ"}
	for _, msg := range plain {
		if y, r := approvalYes(msg); !y {
			t.Errorf("%q: should be a yes (reaction=%v)", msg, r)
		}
	}
	if _, r := approvalYes("👍🏽"); !r {
		t.Error("👍🏽 is a reaction word")
	}
	for _, msg := range []string{"kkkkk", "ok-ish", "sure-ish", "yesterday", "okayyy so", "goood"} {
		if y, _ := approvalYes(msg); y {
			t.Errorf("%q: must not be a yes", msg)
		}
		if _, _, c, ok := yesWithMessage(msg); ok && !c {
			t.Errorf("%q: must not approve with a message", msg)
		}
	}
	if !approvalNo("no,") || !approvalNo("no.") || !approvalNo("NO!") {
		t.Error("a bare no with punctuation declines")
	}
	for _, msg := range []string{"no! go ahead", "no, it's fine", "no, yes please", "nope, sure", "no, ok do it", "no, why?"} {
		if approvalNo(msg) || !mixedNo(msg) {
			t.Errorf("%q: mixed, not a decline (no=%v mixed=%v)", msg, approvalNo(msg), mixedNo(msg))
		}
	}
	for _, msg := range []string{"no, he needs to finish now", "nope, too much", "no thanks", "不要，太貴"} {
		if !approvalNo(msg) || mixedNo(msg) {
			t.Errorf("%q: a decline (no=%v mixed=%v)", msg, approvalNo(msg), mixedNo(msg))
		}
	}
}

// Review round 2 (M3.2d): the Arbiter's R-3 markers, verbatim, each as a
// tail; spellings that collapsing repeated letters would have broken;
// hedges, conditional phrases, auxiliary questions, contractions; weak and
// laughter heads; the length cap.
func TestApprovalTailsRound2(t *testing.T) {
	ruled := []string{"$5", "5", "max", "min", "only", "if", "unless", "under", "over", "less", "more than", "up to", "at most", "不超過", "只要", "如果", "最多"}
	for _, m := range ruled {
		msg := "yes " + m + " x"
		if _, _, c, ok := yesWithMessage(msg); !ok || !c {
			t.Errorf("ruled marker %q: ok=%v conditional=%v", m, ok, c)
		}
	}
	notApproved := []string{
		"yes unless he's late", "ok, less please", "yes three dollars", "ok that's steep", "sì, più tardi",
		"ok maybe", "yes as long as he's quick", "sure we'll see", "ok did he finish", "ok but he didnt call",
		"好，我再想想", "yes, mañana", "ok idk", "yes provided it's fresh",
		"y tho", "si es barato", "ya veremos", "k whatever", "kkkkk, lol", "yyyy hmm",
		"yes and also could you grab some extra napkins and a lemon on the way back",
	}
	for _, msg := range notApproved {
		_, _, c, ok := yesWithMessage(msg)
		if y, _ := approvalYes(msg); y || (ok && !c) {
			t.Errorf("%q approves: plain=%v ok=%v conditional=%v", msg, y, ok, c)
		}
	}
	// Plain yeses that must keep working.
	for _, msg := range []string{"Yes👍", "👍👍", "yes 👍👍", "ok 😂", "yes 💪", "Yes, please", "yes thanks", "yes thank you so much", "sì", "ok ✅"} {
		if y, _ := approvalYes(msg); !y {
			t.Errorf("%q: should be a plain yes", msg)
		}
	}
	// A negated reason is a decline; an affirming or asking one is mixed.
	for _, msg := range []string{"no, I'm not fine with that", "no, not ok", "no, he can't wait"} {
		if !approvalNo(msg) || mixedNo(msg) {
			t.Errorf("%q: a decline (no=%v mixed=%v)", msg, approvalNo(msg), mixedNo(msg))
		}
	}
}
