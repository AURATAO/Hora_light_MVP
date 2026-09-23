package notify

import (
	"strings"
	"testing"
)

// The link builder is the whole of TASK 2's server side, so it is worth
// pinning: every user-facing email now routes through the redirect page, and a
// regression here silently sends TestFlight users back to a web session they
// are not signed in to.

func TestTaskLinkUsesRedirectPage(t *testing.T) {
	t.Setenv("APP_BASE_URL", "https://mvp.horaapp.co")

	got := TaskLink("abc-123")
	want := "https://mvp.horaapp.co/open/task/abc-123"
	if got != want {
		t.Fatalf("TaskLink = %q, want %q", got, want)
	}
}

func TestTaskReviewLinkPreservesReviewPath(t *testing.T) {
	t.Setenv("APP_BASE_URL", "https://mvp.horaapp.co")

	got := TaskReviewLink("abc-123")
	want := "https://mvp.horaapp.co/open/task/abc-123/review"
	if got != want {
		t.Fatalf("TaskReviewLink = %q, want %q", got, want)
	}
}

// A trailing slash on the env var must not produce "//open/task/…", which
// would 404 on Vercel's rewrite rules.
func TestTaskLinkTrimsTrailingSlash(t *testing.T) {
	t.Setenv("APP_BASE_URL", "https://mvp.horaapp.co/")

	if got := TaskLink("t1"); got != "https://mvp.horaapp.co/open/task/t1" {
		t.Fatalf("TaskLink with trailing slash = %q", got)
	}
}

func TestTaskLinkFallsBackToProductionBase(t *testing.T) {
	t.Setenv("APP_BASE_URL", "")

	if got := TaskLink("t1"); got != "https://horaapp.co/open/task/t1" {
		t.Fatalf("TaskLink with unset base = %q", got)
	}
}

// The ops team's new-task alert is the one email that deliberately did not
// move — admins read it beside the admin panel on a desktop.
func TestWebTaskURLStaysDirect(t *testing.T) {
	t.Setenv("APP_BASE_URL", "https://mvp.horaapp.co")

	if got := webTaskURL("t1"); got != "https://mvp.horaapp.co/tasks/t1" {
		t.Fatalf("webTaskURL = %q", got)
	}
}

// Every rendered template must carry the redirect link rather than /tasks/:id.
// buildEmail is given the link by Create, so this checks the templates use the
// argument they are handed — the failure mode being a template that rebuilt the
// URL itself.
func TestEveryTaskEmailLinksToRedirectPage(t *testing.T) {
	t.Setenv("APP_BASE_URL", "https://mvp.horaapp.co")

	types := []string{
		"ORDER_ACCEPTED", "CLOCK_IN", "CLOCK_OUT", "COMPLETED",
		"COMPLETED_SUPPORTER", "CANCELLED", "NEW_MESSAGE",
		"TASK_REASSIGNED", "TASK_REMOVED", // TASK_REMOVED renders via defaultEmail
	}
	for _, typ := range types {
		in := CreateNotificationInput{
			TaskID:    "task-9",
			Type:      typ,
			Title:     "Something happened",
			Body:      "Details here.",
			TaskTitle: "Pick up a parcel",
		}
		html := buildEmail(in, TaskLink(in.TaskID))

		if !strings.Contains(html, "https://mvp.horaapp.co/open/task/task-9") {
			t.Errorf("%s email has no redirect link", typ)
		}
		// The direct web path must not appear as an href. "/open/task/…" does
		// not contain "/tasks/", so any hit is a hardcoded webapp link.
		if strings.Contains(html, `href="https://mvp.horaapp.co/tasks/`) {
			t.Errorf("%s email still links straight to the webapp", typ)
		}
	}
}

// The completion email carries two buttons and they must not collapse to the
// same destination: "View task" opens the task, "Leave a review" keeps the
// review path for the web fallback.
func TestCompletedEmailKeepsBothDestinations(t *testing.T) {
	t.Setenv("APP_BASE_URL", "https://mvp.horaapp.co")

	in := CreateNotificationInput{TaskID: "task-9", Type: "COMPLETED", TaskTitle: "Pick up a parcel"}
	html := buildEmail(in, TaskLink(in.TaskID))

	if !strings.Contains(html, `href="https://mvp.horaapp.co/open/task/task-9"`) {
		t.Error("completion email lost its View task link")
	}
	if !strings.Contains(html, `href="https://mvp.horaapp.co/open/task/task-9/review"`) {
		t.Error("completion email lost its review link")
	}
}

// The receipt email renders the itemization it is given, escapes what the
// user typed, and keeps the redirect link like every other task email.
func TestReceiptEmailRendersTheItemization(t *testing.T) {
	t.Setenv("APP_BASE_URL", "https://mvp.horaapp.co")
	in := CreateNotificationInput{
		TaskID: "task-9", Type: "RECEIPT", Title: "Your HO:RA receipt — $14.50",
		TaskTitle: "Laundry <run>",
		Receipt: &Receipt{
			TaskTitle: "Laundry <run>",
			Lines: []ReceiptLine{
				{Label: "Base fee (first 15 min included)", Amount: "$12.00"},
				{Label: "25 billable min × $0.50", Amount: "$12.50"},
				{Label: "Promo (WELCOME10)", Amount: "−$10.00"},
			},
			Approvals: []string{"Approved 15 more minutes"},
			Charges:   []ReceiptLine{{Label: "Reserved amount · Visa ••4242", Amount: "$14.50"}},
			Total:     "$14.50",
			Released:  "$35.50",
		},
	}
	html := buildEmail(in, TaskLink(in.TaskID))
	for _, want := range []string{
		"Base fee (first 15 min included)", "$12.00", "25 billable min × $0.50", "Promo (WELCOME10)", "−$10.00",
		"Total charged", "$14.50", "Reserved amount · Visa ••4242", "Approved 15 more minutes",
		"$35.50 of the amount reserved", "Laundry &lt;run&gt;",
		`href="https://mvp.horaapp.co/open/task/task-9"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("receipt email lacks %q", want)
		}
	}
	if strings.Contains(html, "Laundry <run>") {
		t.Error("receipt email did not escape the task title")
	}
	// Without an itemization it still renders — as the default template —
	// rather than sending an empty page.
	in.Receipt = nil
	if html := buildEmail(in, TaskLink(in.TaskID)); !strings.Contains(html, "task-9") {
		t.Error("receipt email with no itemization rendered nothing")
	}
}
