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
