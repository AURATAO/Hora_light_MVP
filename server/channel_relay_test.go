package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"hora-auth/internal/channel"
)

// Chat relay authorization (channel_relay.go). DB-backed; skipped without
// TEST_DATABASE_URL. Every check here runs BEFORE any TalkJS call, so the
// tests need no TalkJS at all: the config check is deliberately last.

func blockPair(t *testing.T, a, b string) {
	t.Helper()
	if _, err := sqldb.ExecContext(context.Background(),
		`insert into public.user_blocks (blocker_id, blocked_id) values ($1::uuid, $2::uuid)`, a, b); err != nil {
		t.Fatalf("block: %v", err)
	}
}

func TestActiveTasksListsOnlyOpenAssignedUnblocked(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)
	ctx := context.Background()
	relay := &talkjsRelay{sqlDB: sqldb}

	got, err := relay.ActiveTasks(ctx, w.requesterID)
	if err != nil || len(got) != 1 || got[0].ID != w.taskID || got[0].Title != "Grocery run" || got[0].SupporterName != "Sam" {
		t.Fatalf("%+v %v", got, err)
	}
	// The supporter's own list is empty: the relay is the requester's.
	if got, _ := relay.ActiveTasks(ctx, w.supporterID); len(got) != 0 {
		t.Fatalf("supporter must not see tasks as a requester: %+v", got)
	}
	// Unassigned and closed tasks are not on the list.
	if _, err := sqldb.ExecContext(ctx, `UPDATE public.tasks SET assigned_to_id = NULL, assigned_to = '' WHERE id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	if got, _ := relay.ActiveTasks(ctx, w.requesterID); len(got) != 0 {
		t.Fatalf("unassigned task listed: %+v", got)
	}
	if _, err := sqldb.ExecContext(ctx, `UPDATE public.tasks SET assigned_to_id = $2::uuid, assigned_to = $3, status = 'completed' WHERE id = $1::uuid`, w.taskID, w.supporterID, w.supporterEmail); err != nil {
		t.Fatal(err)
	}
	if got, _ := relay.ActiveTasks(ctx, w.requesterID); len(got) != 0 {
		t.Fatalf("completed task listed: %+v", got)
	}
	// A blocked pair is not offered at all (no "try again" dead end).
	if _, err := sqldb.ExecContext(ctx, `UPDATE public.tasks SET status = 'open' WHERE id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	blockPair(t, w.requesterID, w.supporterID)
	if got, _ := relay.ActiveTasks(ctx, w.requesterID); len(got) != 0 {
		t.Fatalf("blocked pair listed: %+v", got)
	}
	// And the other direction: the supporter blocking the requester.
	if _, err := sqldb.ExecContext(ctx, `DELETE FROM public.user_blocks`); err != nil {
		t.Fatal(err)
	}
	blockPair(t, w.supporterID, w.requesterID)
	if got, _ := relay.ActiveTasks(ctx, w.requesterID); len(got) != 0 {
		t.Fatalf("supporter-initiated block listed: %+v", got)
	}
}

func TestScrubURLErrDropsTheEmail(t *testing.T) {
	err := &url.Error{Op: "Put", URL: "https://api.talkjs.com/v1/app/users/aura@example.test", Err: errors.New("dial tcp: timeout")}
	got := scrubURLErr(fmt.Errorf("wrap: %w", err))
	if strings.Contains(got.Error(), "aura@") || !strings.Contains(got.Error(), "timeout") {
		t.Fatalf("%v", got)
	}
}

func TestPostAsRequesterRefusesBeforeTalkJS(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)
	ctx := context.Background()
	relay := &talkjsRelay{sqlDB: sqldb}
	// No TalkJS credentials in tests: a request that passes every check
	// fails on configuration, which proves the checks ran first.
	t.Setenv("TALKJS_APP_ID", "")
	t.Setenv("TALKJS_SECRET_KEY", "")

	wantErr := func(userID, want string) {
		t.Helper()
		err := relay.PostAsRequester(ctx, w.taskID, userID, "hi", "k1")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("user %q: got %v, want %q", userID, err, want)
		}
		// Every refusal except "not configured" is a not-active, so the
		// router tells the person instead of "try again".
		if isActive := !errors.Is(err, channel.ErrTaskNotActive); isActive != (want == "talkjs not configured") {
			t.Fatalf("user %q: ErrTaskNotActive wrapping wrong for %v", userID, err)
		}
	}
	wantErr(w.supporterID, "not the sender's") // the supporter cannot speak as the requester
	wantErr("", "not the sender's")
	wantErr("00000000-0000-0000-0000-000000000001", "not the sender's")
	wantErr(w.requesterID, "talkjs not configured") // owner: every check passed

	if _, err := sqldb.ExecContext(ctx, `UPDATE public.tasks SET status = 'completed' WHERE id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	wantErr(w.requesterID, "no open chat pair")
	if _, err := sqldb.ExecContext(ctx, `UPDATE public.tasks SET status = 'open', assigned_to_id = NULL, assigned_to = '' WHERE id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	wantErr(w.requesterID, "no open chat pair")
	if _, err := sqldb.ExecContext(ctx, `UPDATE public.tasks SET assigned_to_id = $2::uuid WHERE id = $1::uuid`, w.taskID, w.supporterID); err != nil {
		t.Fatal(err)
	}
	blockPair(t, w.supporterID, w.requesterID)
	wantErr(w.requesterID, "pair blocked")

	if err := relay.PostAsRequester(ctx, "00000000-0000-0000-0000-000000000009", w.requesterID, "hi", "k2"); !errors.Is(err, channel.ErrTaskNotActive) {
		t.Fatalf("unknown task must fail as not-active: %v", err)
	}
}

// Supporter → requester: only the assigned supporter, on an open task, in
// the task's own room, with no block. Everything else is dropped.
func TestRelayToRequesterGate(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)
	ctx := context.Background()

	if p, ok := relayAllowedToRequester(ctx, sqldb, w.taskID, w.supporterID); !ok || p.requesterID != w.requesterID || p.title != "Grocery run" {
		t.Fatalf("assigned supporter must pass: %+v %v", p, ok)
	}
	for name, sender := range map[string]string{
		"requester themself": w.requesterID,
		"empty sender":       "",
		"stranger":           "00000000-0000-0000-0000-000000000002",
	} {
		if _, ok := relayAllowedToRequester(ctx, sqldb, w.taskID, sender); ok {
			t.Fatalf("%s must not reach the requester's phone", name)
		}
	}
	if _, ok := relayAllowedToRequester(ctx, sqldb, "00000000-0000-0000-0000-000000000009", w.supporterID); ok {
		t.Fatal("unknown task must fail closed")
	}
	if _, err := sqldb.ExecContext(ctx, `UPDATE public.tasks SET status = 'cancelled' WHERE id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	if _, ok := relayAllowedToRequester(ctx, sqldb, w.taskID, w.supporterID); ok {
		t.Fatal("a closed task relays nothing")
	}
	if _, err := sqldb.ExecContext(ctx, `UPDATE public.tasks SET status = 'open' WHERE id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	blockPair(t, w.requesterID, w.supporterID)
	if _, ok := relayAllowedToRequester(ctx, sqldb, w.taskID, w.supporterID); ok {
		t.Fatal("a blocked pair relays nothing")
	}
}

// The webhook entry point: the hook fires only for the task's own room and
// only for the assigned supporter; the requester's own message never echoes.
func TestRelayChatMessageToRequesterUsesRoomAndSender(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)
	// The hook is passed in, as the webhook passes it; this test touches no
	// package state (the race that cost a red -race run on 2026-10-27 was a
	// test assigning chatRelayHook while an earlier test's goroutine read it).
	var calls []string
	hook := chatRelayFn(func(_ context.Context, requesterID, taskID, supporterName, taskTitle, text, mediaURL string) {
		calls = append(calls, requesterID+"|"+taskID+"|"+supporterName+"|"+taskTitle+"|"+text+"|"+mediaURL)
	})

	msg := talkjsMessage{Text: "On my way!", Attachment: &talkjsFile{URL: "https://cdn.example.test/p.jpg"}}
	relayChatMessageToRequester(hook, sqldb, "task_"+w.taskID, w.taskID, w.supporterID, msg)
	// The name is the profile's first name ("Sam Supporter"), never the TalkJS payload's.
	if len(calls) != 1 || calls[0] != w.requesterID+"|"+w.taskID+"|Sam|Grocery run|On my way!|https://cdn.example.test/p.jpg" {
		t.Fatalf("calls %v", calls)
	}
	relayChatMessageToRequester(hook, sqldb, "task_"+w.taskID, w.taskID, w.requesterID, msg) // own message
	relayChatMessageToRequester(hook, sqldb, "task_other", w.taskID, w.supporterID, msg)     // wrong room, custom.taskId forged
	relayChatMessageToRequester(hook, sqldb, "task_"+w.taskID, "", w.supporterID, msg)       // no task
	if len(calls) != 1 {
		t.Fatalf("calls %v", calls)
	}
	relayChatMessageToRequester(nil, sqldb, "task_"+w.taskID, w.taskID, w.supporterID, msg) // hook off: no panic
}

var _ channel.TaskChat = (*talkjsRelay)(nil)

// M3.2c: a held approval is only resumed while the request is still the
// person's to answer — pending, inside the app's auto-decline window (expiry
// is lazy, so the clock is checked here), on an open task that still has
// that supporter.
func TestExtensionPendingChecksAgeTaskAndSupporter(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)
	ctx := context.Background()
	b := &chatBooker{sqlDB: sqldb}
	seed := func(kind, age string) string { // one pending per kind per task
		var id string
		if err := db.QueryRow(ctx, `
			insert into public.extension_requests (task_id, supporter_id, kind, requested_minutes, requested_cents, fallback, status, created_at)
			values ($1::uuid, $2::uuid, $3,
			        case when $3 = 'time' then 15 end, case when $3 = 'budget' then 800 end, case when $3 = 'budget' then 'skip_item' end,
			        'pending', now() - $4::interval) returning id::text`,
			w.taskID, w.supporterID, kind, age).Scan(&id); err != nil {
			t.Fatalf("seed: %v", err)
		}
		return id
	}
	fresh, stale := seed("time", "1 minute"), seed("budget", "6 minutes")
	check := func(ext string, want bool, why string) {
		t.Helper()
		got, err := b.ExtensionPending(ctx, w.requesterID, ext)
		if err != nil || got != want {
			t.Fatalf("%s: got %v want %v (%v)", why, got, want, err)
		}
	}
	check(fresh, true, "fresh pending request")
	check(stale, false, "older than the auto-decline window, still 'pending' in the row")
	if _, err := b.ExtensionPending(ctx, w.supporterID, fresh); err != nil {
		t.Fatal(err)
	} else if got, _ := b.ExtensionPending(ctx, w.supporterID, fresh); got {
		t.Fatal("not the supporter's to answer")
	}
	if _, err := db.Exec(ctx, `update public.extension_requests set status = 'approved', resolved_at = now() where id = $1::uuid`, fresh); err != nil {
		t.Fatal(err)
	}
	check(fresh, false, "resolved in the app")
	other := seed("time", "1 minute")
	if _, err := db.Exec(ctx, `update public.tasks set assigned_to_id = null, assigned_to = '' where id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	check(other, false, "supporter no longer on the task")
	if _, err := db.Exec(ctx, `update public.tasks set assigned_to_id = $2::uuid, assigned_to = $3 where id = $1::uuid`, w.taskID, w.supporterID, w.supporterEmail); err != nil {
		t.Fatal(err)
	}
	check(other, true, "supporter back on the task")
	blockPair(t, w.requesterID, w.supporterID)
	check(other, false, "a blocked pair, like the relay")
	if _, err := sqldb.ExecContext(ctx, `DELETE FROM public.user_blocks`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `update public.tasks set assigned_to_id = $2::uuid, assigned_to = $3, status = 'cancelled' where id = $1::uuid`, w.taskID, w.supporterID, w.supporterEmail); err != nil {
		t.Fatal(err)
	}
	check(other, false, "task closed")
}
