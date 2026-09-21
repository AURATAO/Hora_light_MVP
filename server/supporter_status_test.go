package main

// supporter_status, and the one place it is derived.
//
// WHY THIS HAS A TEST OF ITS OWN. Every supporter surface in both clients is
// now gated on supporter_status == "approved" — the Earn tab, the
// Posted/Working segment, the available-tasks feed, the earnings row, the
// dashboard stat pills. That makes the derivation load-bearing in a way it was
// not when it only decided which banner to draw: getting it wrong now either
// hides a supporter's own work from them or shows a requester a tab full of
// other people's jobs.
//
// Pure arithmetic over four fields, so no DB.

import (
	"testing"
	"time"
)

// A timestamp's VALUE never matters to the derivation — only whether it is
// there — so one fixed instant serves every case and nothing here can depend
// on the clock.
func fixedTime() time.Time {
	return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
}

func TestSupporterStatusDerivation(t *testing.T) {
	cases := []struct {
		name       string
		verified   bool
		appliedAt  bool
		rejectedAt bool
		want       string
	}{
		// Nobody has done anything. The default, and the state every
		// requester-only account sits in.
		{"never applied", false, false, false, "none"},

		{"applied, awaiting review", false, true, false, "applied"},

		// VERIFICATION OUTRANKS EVERYTHING. An approved supporter keeps their
		// application timestamp forever, so reading `applied` off that column
		// would take the Earn tab away from every supporter on the platform.
		{"approved", true, true, false, "approved"},
		{"approved with no application on file", true, false, false, "approved"},

		// REJECTION OUTRANKS THE APPLICATION, and re-applying clears the
		// rejection — so a second-chance applicant reads "applied", which is
		// what lets them back into the form.
		{"rejected", false, true, true, "rejected"},
		{"rejected with no application on file", false, false, true, "rejected"},

		// An approved supporter who was once rejected is approved. Ops
		// approving somebody they previously turned down must not leave them
		// locked out of the surfaces they were just granted.
		{"approved after an earlier rejection", true, true, true, "approved"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Profile{IsVerifiedSupporter: tc.verified}
			if tc.appliedAt {
				now := fixedTime()
				p.SupporterAppliedAt = &now
			}
			if tc.rejectedAt {
				now := fixedTime()
				p.SupporterRejectedAt = &now
			}
			p.deriveSupporterStatus()
			if p.SupporterStatus != tc.want {
				t.Errorf("supporter_status = %q, want %q", p.SupporterStatus, tc.want)
			}

			// THE GATE ITSELF. The clients compare against this exact string;
			// a typo here is a tab that never appears for anybody.
			gated := p.SupporterStatus == "approved"
			if gated != tc.verified {
				t.Errorf("gated = %v but is_verified_supporter = %v — the surfaces and the flag disagree",
					gated, tc.verified)
			}
		})
	}
}
