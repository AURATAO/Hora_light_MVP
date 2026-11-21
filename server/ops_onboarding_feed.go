package main

// The onboarding feed: where each Supporter applicant stands in the app, for
// the n8n workflow that keeps the onboarding roster (Airtable) in step with
// HO:RA (Aura, 2026-11-21).
//
// The roster of applicants lives in Airtable, run by the founders: who was
// interviewed, by whom, what was decided. What only HO:RA knows is whether a
// person has applied in the app, been approved, set up payouts and done any
// work. Once a day the workflow reads this feed, matches people by email, and
// writes the app's side back onto the roster.
//
// READ ONLY, and deliberately narrow (CLAUDE.md rule 1: every read of a Hora
// table goes through this API; the automation never touches the database):
//
//   - One GET, no parameters, no writes anywhere.
//   - Only people who applied to be a Supporter or were approved. Customers
//     never appear.
//   - Per person: email (the join key with the roster), and the facts above.
//     No phone, no address, no names, no ids.
//   - Machine authentication: a bearer token from OPS_AUTOMATION_TOKEN,
//     compared in constant time. With the variable unset the route answers
//     404 and is indistinguishable from one that does not exist, so turning
//     the automation off is unsetting one variable.
//   - Nothing about the people is logged, only the count.

import (
	"crypto/subtle"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type onboardingFeedRow struct {
	Email string `json:"email"`
	// When they submitted the Supporter application in the app; null if they
	// were approved without one (early testers).
	AppliedAt *time.Time `json:"applied_at"`
	Approved  bool       `json:"approved"`
	// Set when the application was turned down in the ops panel.
	RejectedAt *time.Time `json:"rejected_at"`
	// Stripe reports their connected account can receive payouts. Without it
	// an approved Supporter cannot accept a task.
	PayoutsReady   bool       `json:"payouts_ready"`
	TasksCompleted int        `json:"tasks_completed"`
	LastTaskAt     *time.Time `json:"last_task_at"`
	// One word for the roster, from the facts above, furthest stage first:
	// active | payouts_ready | approved | rejected | applied.
	AppStatus string `json:"app_status"`
}

func onboardingAppStatus(r onboardingFeedRow) string {
	switch {
	case r.Approved && r.TasksCompleted > 0:
		return "active"
	case r.Approved && r.PayoutsReady:
		return "payouts_ready"
	case r.Approved:
		return "approved"
	case r.RejectedAt != nil:
		return "rejected"
	default:
		return "applied"
	}
}

// automationTokenOK is the whole of the feed's authentication. An unset
// token disables the route rather than opening it.
func automationTokenOK(c *gin.Context) bool {
	want := strings.TrimSpace(os.Getenv("OPS_AUTOMATION_TOKEN"))
	if want == "" {
		return false
	}
	got := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// GET /ops/automation/onboarding
func getOnboardingFeed(c *gin.Context) {
	if strings.TrimSpace(os.Getenv("OPS_AUTOMATION_TOKEN")) == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if !automationTokenOK(c) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	rows, err := db.Query(c.Request.Context(), `
		select lower(u.email),
		       p.supporter_applied_at,
		       coalesce(p.is_verified_supporter, false),
		       p.supporter_rejected_at,
		       coalesce(u.stripe_payouts_enabled, false),
		       (select count(*) from public.tasks t
		         where t.assigned_to_id = u.id and t.status = 'completed'),
		       (select max(t.completed_at) from public.tasks t
		         where t.assigned_to_id = u.id and t.status = 'completed')
		  from public.users u
		  join public.profiles p on p.id = u.id
		 where (p.supporter_applied_at is not null or coalesce(p.is_verified_supporter, false))
		   and coalesce(u.email, '') <> ''
		 order by p.supporter_applied_at nulls first, u.email
	`)
	if err != nil {
		log.Printf("[onboardingFeed][query] err=%v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	out := []onboardingFeedRow{}
	for rows.Next() {
		var r onboardingFeedRow
		if err := rows.Scan(&r.Email, &r.AppliedAt, &r.Approved, &r.RejectedAt,
			&r.PayoutsReady, &r.TasksCompleted, &r.LastTaskAt); err != nil {
			log.Printf("[onboardingFeed][scan] err=%v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		r.AppStatus = onboardingAppStatus(r)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[onboardingFeed][rows] err=%v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	log.Printf("[onboardingFeed] served %d applicants", len(out))
	c.JSON(http.StatusOK, gin.H{
		"generated_at": time.Now().UTC(),
		"applicants":   out,
	})
}
