package main

// Names: the chain, in one place.
//
// Build 11 device run: "taoaura.lavoro is on the way" on a requester's lock
// screen. The email prefix is the LAST resort, printed only for an account
// that has never been asked — and the thing that made it look chosen was that
// every profile was seeded with it. These pin the chain (helpers/names.go),
// the batch resolver a task list goes through (names.go), and the migration
// that blanks the seeds so the prompt actually fires.
//
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55440/horatest' \
//	  go test ./ -run Names -v

import (
	"context"
	"os"
	"testing"

	"hora-auth/helpers"
)

func TestNamesChainIsPureAndLastResortOnly(t *testing.T) {
	cases := []struct{ name, email, want string }{
		{"Rita", "rita.r@example.com", "Rita"},                 // chosen wins
		{"  Rita  ", "rita.r@example.com", "Rita"},             // trimmed
		{"", "taoaura.lavoro@example.com", "Taoaura Lavoro"},   // last resort, title-cased
		{"   ", "jane@example.com", "Jane"},                    // whitespace is not a name
		{"", "no-at-sign", "no-at-sign"},                       // nothing to split
		{"Taoaura Lavoro", "taoaura.lavoro@x.com", "Taoaura Lavoro"}, // a deliberate match is still theirs
	}
	for _, c := range cases {
		if got := helpers.DisplayName(c.name, c.email); got != c.want {
			t.Errorf("DisplayName(%q, %q) = %q, want %q", c.name, c.email, got, c.want)
		}
	}
	if helpers.HasDisplayName("  ") {
		t.Error("whitespace counts as a display name")
	}
}

func TestNamesResolveFromProfilesWithPrefixFallback(t *testing.T) {
	setupAdminOpsDB(t)
	ctx := context.Background()

	rita := seedUser(t, "rita.r@example.com", "Rita", false)
	// Finished onboarding before names were required: blank after the migration.
	nameless := seedUser(t, "Taoaura.Lavoro@Example.com", "", true)

	taskID := seedReassignTask(t, "open", rita, "rita.r@example.com", nameless, "taoaura.lavoro@example.com")

	// One person, by email — case does not matter (the task row carries the
	// address as it was typed at accept, the profile as it was at signup).
	if got := resolveDisplayName(ctx, "TAOAURA.LAVORO@example.com"); got != "Taoaura Lavoro" {
		t.Errorf("nameless supporter resolved to %q, want the email prefix", got)
	}
	if got := resolveDisplayName(ctx, "rita.r@example.com"); got != "Rita" {
		t.Errorf("rita resolved to %q", got)
	}
	// Nobody we know: still a name, never an error or a blank.
	if got := resolveDisplayName(ctx, "stranger.here@example.com"); got != "Stranger Here" {
		t.Errorf("unknown email resolved to %q", got)
	}
	if got := resolveDisplayName(ctx, ""); got != "" {
		t.Errorf("empty email resolved to %q, want empty", got)
	}

	// The batch path a task list takes.
	tasks := []Task{{ID: taskID, Requester: "rita.r@example.com", AssignedTo: "taoaura.lavoro@example.com"}, {Requester: "rita.r@example.com"}}
	attachTaskNames(ctx, tasks)
	if tasks[0].RequesterName != "Rita" || tasks[0].AssigneeName != "Taoaura Lavoro" {
		t.Errorf("task names = %q / %q", tasks[0].RequesterName, tasks[0].AssigneeName)
	}
	if tasks[1].AssigneeName != "" {
		t.Errorf("unassigned task carries assignee name %q", tasks[1].AssigneeName)
	}

	// loadAdminTask, which every server-composed notification reads from.
	at, err := loadAdminTask(ctx, taskID)
	if err != nil {
		t.Fatalf("loadAdminTask: %v", err)
	}
	if at.RequesterName != "Rita" || at.AssigneeName != "Taoaura Lavoro" {
		t.Errorf("adminTask names = %q / %q", at.RequesterName, at.AssigneeName)
	}
}

// The migration blanks exactly the seeds: the Go title-cased form, the
// trigger's raw local part, and a client echo with the dots kept — and leaves
// a chosen name alone even when it is short.
func TestNamesMigrationBlanksOnlySeededNames(t *testing.T) {
	setupAdminOpsDB(t)
	ctx := context.Background()

	seedUser(t, "taoaura.lavoro@example.com", "Taoaura Lavoro", false) // Go seed
	seedUser(t, "jane@example.com", "jane", false)                     // handle_new_user seed
	seedUser(t, "a.b.c@example.com", "a.b.c", false)                   // echoed raw
	seedUser(t, "rita.r@example.com", "Rita", false)                   // chosen
	seedUser(t, "sam@example.com", "Sam Smith", false)                 // chosen, shares a prefix

	// The fixture's profiles table is the reassign test's slice of the real
	// one and has no updated_at; the shipped table does, and the migration
	// touches it.
	if _, err := db.Exec(ctx, `alter table public.profiles add column if not exists updated_at timestamptz not null default now()`); err != nil {
		t.Fatalf("fixture updated_at: %v", err)
	}
	migration, err := os.ReadFile("../supabase/migrations/20260922075623_blank_seeded_profile_names.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}

	want := map[string]string{
		"taoaura.lavoro@example.com": "",
		"jane@example.com":           "",
		"a.b.c@example.com":          "",
		"rita.r@example.com":         "Rita",
		"sam@example.com":            "Sam Smith",
	}
	for email, wantName := range want {
		var got string
		if err := db.QueryRow(ctx, `select coalesce(name,'') from public.profiles where email = $1`, email).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", email, err)
		}
		if got != wantName {
			t.Errorf("%s: name = %q after migration, want %q", email, got, wantName)
		}
	}
}
