package main

import (
	"context"
	"log"
	"strings"

	"hora-auth/helpers"
)

// The DB half of naming people — see helpers/names.go for the rule.
//
// Every server-composed sentence that names a person goes through one of
// these: a push, an email, a task payload, a live-tracking marker. None of
// them read tasks.requester or tasks.assigned_to (emails) as a name any more.

// resolveDisplayName is the chain for one person, by email. A lookup failure
// degrades to the last resort rather than to an error: a notification that
// cannot be composed is worse than one that says "Jane Doe".
func resolveDisplayName(ctx context.Context, email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return ""
	}
	return displayNamesByEmail(ctx, []string{email})[strings.ToLower(email)]
}

// displayNamesByEmail resolves many at once — one query for a whole task
// list. The map is keyed by lower-cased email and has an entry for EVERY
// email passed in, so a caller can index it without a presence check.
func displayNamesByEmail(ctx context.Context, emails []string) map[string]string {
	out := make(map[string]string, len(emails))
	want := make([]string, 0, len(emails))
	for _, e := range emails {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		k := strings.ToLower(e)
		if _, seen := out[k]; seen {
			continue
		}
		out[k] = helpers.EmailPrefixName(e)
		want = append(want, k)
	}
	if len(want) == 0 || db == nil {
		return out
	}
	rows, err := db.Query(ctx, `
		select lower(email), coalesce(name, '')
		  from public.profiles
		 where lower(email) = any($1::text[])
	`, want)
	if err != nil {
		log.Printf("[names] lookup failed (%d emails): %v", len(want), err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var email, name string
		if err := rows.Scan(&email, &name); err != nil {
			continue
		}
		if helpers.HasDisplayName(name) {
			out[email] = strings.TrimSpace(name)
		}
	}
	return out
}

// attachTaskNames fills requester_name / assignee_name on a page of tasks
// from one lookup. The emails stay on the payload for the clients' identity
// checks (me.email === task.requester); these are what they print.
func attachTaskNames(ctx context.Context, tasks []Task) {
	emails := make([]string, 0, 2*len(tasks))
	for i := range tasks {
		emails = append(emails, tasks[i].Requester, tasks[i].AssignedTo)
	}
	names := displayNamesByEmail(ctx, emails)
	for i := range tasks {
		tasks[i].RequesterName = names[strings.ToLower(strings.TrimSpace(tasks[i].Requester))]
		tasks[i].AssigneeName = names[strings.ToLower(strings.TrimSpace(tasks[i].AssignedTo))]
	}
}
