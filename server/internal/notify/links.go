package notify

import (
	"fmt"
	"net/url"
	"strings"
)

// Where an email's "View task" button points.
//
// Every recipient of these emails is a TestFlight user reading them on the
// phone that has the app installed, so a link straight to the webapp
// (/tasks/:id) dropped them into a web session they were never signed in to.
// Linking to hora://task/:id instead would fix that for app users and break it
// for everyone else: a custom scheme is inert in a mail client on a device
// without the app, and Gmail/Outlook strip non-http hrefs outright.
//
// So emails link to a page on the webapp that immediately tries the scheme and
// falls back to the web task page (app/src/pages/OpenInApp.jsx). It is an
// ordinary https URL, which every mail client renders, and it needs no
// user-agent guessing: the device itself decides whether hora:// resolves.
//
// Universal Links would remove the intermediate page entirely, but they need
// an associated-domains entitlement and an apple-app-site-association file
// neither of which exists yet (mobile/app.json declares "scheme": "hora" and
// no associatedDomains). When that lands, only the two builders below change.

const (
	// Path prefix of the redirect page. Public — it must render for a
	// signed-out reader, since being signed out is the whole problem it solves.
	openPathPrefix = "/open/task/"
	// Where links point when APP_BASE_URL is unset.
	defaultWebBase = "https://horaapp.co"
)

// TaskLink is the smart link to a task: the app if it is installed, the web
// task page otherwise.
func TaskLink(taskID string) string {
	return openLink(taskID, "")
}

// TaskReviewLink is TaskLink for the post-completion review. The app has no
// review screen (reviews are web-only), so the redirect page opens the task in
// the app and keeps /tasks/:id/review as the web fallback — the review path is
// preserved for the reader who has no app.
func TaskReviewLink(taskID string) string {
	return openLink(taskID, "review")
}

func openLink(taskID, sub string) string {
	base := strings.TrimRight(getenv("APP_BASE_URL", defaultWebBase), "/")
	link := base + openPathPrefix + url.PathEscape(taskID)
	if sub != "" {
		link += "/" + sub
	}
	return link
}

// webTaskURL is the direct webapp link, kept for the recipients who are not on
// the app: the ops team's admin emails, which are read next to the ops panel.
func webTaskURL(taskID string) string {
	base := strings.TrimRight(getenv("APP_BASE_URL", defaultWebBase), "/")
	return fmt.Sprintf("%s/tasks/%s", base, url.PathEscape(taskID))
}
