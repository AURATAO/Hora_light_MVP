package main

import (
	"context"
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
		err := relay.PostAsRequester(ctx, w.taskID, userID, "hi")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("user %q: got %v, want %q", userID, err, want)
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

	if err := relay.PostAsRequester(ctx, "00000000-0000-0000-0000-000000000009", w.requesterID, "hi"); err == nil {
		t.Fatal("unknown task must fail")
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
	var calls []string
	prev := chatRelayHook
	chatRelayHook = func(_ context.Context, requesterID, taskID, supporterName, taskTitle, text, mediaURL string) {
		calls = append(calls, requesterID+"|"+taskID+"|"+supporterName+"|"+taskTitle+"|"+text+"|"+mediaURL)
	}
	t.Cleanup(func() { chatRelayHook = prev })

	msg := talkjsMessage{Text: "On my way!", Attachment: &talkjsFile{URL: "https://cdn.example.test/p.jpg"}}
	relayChatMessageToRequester(sqldb, "task_"+w.taskID, w.taskID, w.supporterID, "Sam", msg)
	if len(calls) != 1 || calls[0] != w.requesterID+"|"+w.taskID+"|Sam|Grocery run|On my way!|https://cdn.example.test/p.jpg" {
		t.Fatalf("calls %v", calls)
	}
	relayChatMessageToRequester(sqldb, "task_"+w.taskID, w.taskID, w.requesterID, "Rita", msg) // own message
	relayChatMessageToRequester(sqldb, "task_other", w.taskID, w.supporterID, "Sam", msg)      // wrong room, custom.taskId forged
	relayChatMessageToRequester(sqldb, "task_"+w.taskID, "", w.supporterID, "Sam", msg)        // no task
	if len(calls) != 1 {
		t.Fatalf("calls %v", calls)
	}
	chatRelayHook = nil
	relayChatMessageToRequester(sqldb, "task_"+w.taskID, w.taskID, w.supporterID, "Sam", msg) // hook off: no panic
}

var _ channel.TaskChat = (*talkjsRelay)(nil)
