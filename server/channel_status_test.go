package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"hora-auth/internal/channel"
	"hora-auth/internal/notify"
)

type recNotifier struct {
	got    []string
	events []channel.StatusEvent
}

func (r *recNotifier) NotifyStatus(_ context.Context, userID string, ev channel.StatusEvent) error {
	r.got = append(r.got, userID+":"+ev.Type)
	r.events = append(r.events, ev)
	return nil
}

// Only the task's requester is told in the chat. A notification addressed to
// anyone else about the same task (the supporter) never reaches a chat.
func TestChatStatusHookOnlyTellsTheRequester(t *testing.T) {
	rec := &recNotifier{}
	lookups := 0
	hook := statusHook(func(context.Context, string) (string, error) {
		lookups++
		return "requester-1", nil
	}, func(context.Context, string, string) (channel.PendingApproval, error) {
		return channel.PendingApproval{ID: "ext-1"}, nil
	}, rec)

	hook(notify.CreateNotificationInput{UserID: "requester-1", TaskID: "t1", Type: "ORDER_ACCEPTED"})
	hook(notify.CreateNotificationInput{UserID: "supporter-9", TaskID: "t1", Type: "CANCELLED", Body: "x"})
	hook(notify.CreateNotificationInput{UserID: "requester-1", TaskID: "t1", Type: "PAYOUT_SENT"})
	hook(notify.CreateNotificationInput{UserID: "requester-1", Type: "ORDER_ACCEPTED"}) // no task

	if len(rec.got) != 1 || rec.got[0] != "requester-1:ORDER_ACCEPTED" {
		t.Fatalf("got %v", rec.got)
	}
	if lookups != 2 {
		t.Fatalf("non-chat events must skip the DB lookup; lookups=%d", lookups)
	}
}

// Regression (S-12): the setup token must never be in the query string, which
// request logs record. It travels in the fragment.
func TestSetupLinkKeepsTokenOutOfTheQuery(t *testing.T) {
	t.Setenv("CHANNEL_SETUP_WEB_URL", "")
	got := setupLink("https://x.test", "tok/en+")
	if strings.Contains(got, "?") || !strings.Contains(got, "/channel/setup#t=") {
		t.Fatalf("setup link exposes the token to the server: %s", got)
	}
}

// A CHANNEL_SETUP_WEB_URL that cannot work is ignored, loudly, rather than
// producing unclickable links or a redirect that eats the token.
func TestSetupWebURLRejectsUnusableValues(t *testing.T) {
	for _, bad := range []string{"core.example.test/channel/setup", "/channel/setup", "https://core.example.test/channel/setup#t=x"} {
		t.Setenv("CHANNEL_SETUP_WEB_URL", bad)
		if got := channelSetupWebURL(); got != "" {
			t.Errorf("%q should be ignored, got %q", bad, got)
		}
		if got := setupLink("https://api.example.test", "abc"); got != "https://api.example.test/channel/setup#t=abc" {
			t.Errorf("%q should fall back to the built-in page, got %q", bad, got)
		}
	}
	t.Setenv("CHANNEL_SETUP_WEB_URL", "http://localhost:5173/channel/setup")
	if got := channelSetupWebURL(); got != "http://localhost:5173/channel/setup" {
		t.Errorf("a local dev URL must be usable: %q", got)
	}
}

// The texted link points at the web app's copy of the page when one is
// configured (D-16 R-4), and at this binary's own page when it is not. Either
// way the token stays in the fragment.
func TestSetupLinkPrefersTheWebApp(t *testing.T) {
	t.Setenv("CHANNEL_SETUP_WEB_URL", "")
	if got := setupLink("https://api.example.test", "abc"); got != "https://api.example.test/channel/setup#t=abc" {
		t.Fatalf("no web app configured: %q", got)
	}
	t.Setenv("CHANNEL_SETUP_WEB_URL", "https://core.example.test/channel/setup/")
	got := setupLink("https://api.example.test", "abc")
	if got != "https://core.example.test/channel/setup#t=abc" {
		t.Fatalf("with a web app: %q", got)
	}
	if strings.Contains(got, "?") {
		t.Fatal("the token must never reach a query string")
	}
}

// S-11 condition 5: the YES must answer the request the question described.
// Time and budget requests can be pending at once; the lookup is by the kind
// the notification is about, never "newest on the task".
func TestChatStatusHookArmsTheRequestOfTheRightKind(t *testing.T) {
	rec := &recNotifier{}
	var asked []string
	hook := statusHook(func(context.Context, string) (string, error) { return "requester-1", nil },
		func(_ context.Context, taskID, kind string) (channel.PendingApproval, error) {
			asked = append(asked, taskID+"/"+kind)
			return channel.PendingApproval{ID: "ext-" + kind, Kind: kind, Minutes: 15, Cents: 500}, nil
		}, rec)
	hook(notify.CreateNotificationInput{UserID: "requester-1", TaskID: "t1", Type: "BUDGET_INCREASE_REQUESTED", Body: "x"})
	hook(notify.CreateNotificationInput{UserID: "requester-1", TaskID: "t1", Type: "TIME_EXTENSION_REQUESTED", Body: "x"})
	if len(asked) != 2 || asked[0] != "t1/budget" || asked[1] != "t1/time" {
		t.Fatalf("lookups %v", asked)
	}
	if len(rec.events) != 2 || rec.events[0].ExtensionID != "ext-budget" || rec.events[1].ExtensionID != "ext-time" ||
		rec.events[0].Approval == nil || rec.events[0].Approval.Cents != 500 || rec.events[1].Approval == nil || rec.events[1].Approval.Minutes != 15 {
		t.Fatalf("armed %+v", rec.events)
	}
}

// M3.6: the hook attaches the task's photos to COMPLETED only, and a
// lookup failure degrades to the text message.
func TestChatStatusHookAttachesCompletionPhotos(t *testing.T) {
	rec := &recNotifier{}
	prev := photosOf
	t.Cleanup(func() { photosOf = prev })
	photosOf = func(_ context.Context, taskID string) (completionPhotos, error) {
		return completionPhotos{Photo: "https://s/p.jpg?t=1", Receipt: "https://s/r.jpg?t=2", ReceiptTotal: "$8.00"}, nil
	}
	hook := statusHook(func(context.Context, string) (string, error) { return "requester-1", nil },
		func(context.Context, string, string) (channel.PendingApproval, error) {
			return channel.PendingApproval{}, nil
		}, rec)
	hook(notify.CreateNotificationInput{UserID: "requester-1", TaskID: "t1", Type: "COMPLETED", TaskTitle: "Bagels"})
	hook(notify.CreateNotificationInput{UserID: "requester-1", TaskID: "t1", Type: "ORDER_ACCEPTED"})
	if len(rec.events) != 2 || rec.events[0].PhotoURL != "https://s/p.jpg?t=1" || rec.events[0].ReceiptTotal != "$8.00" || rec.events[1].PhotoURL != "" {
		t.Fatalf("%+v", rec.events)
	}
	photosOf = func(context.Context, string) (completionPhotos, error) { return completionPhotos{}, errors.New("db") }
	rec.events = nil
	hook(notify.CreateNotificationInput{UserID: "requester-1", TaskID: "t1", Type: "COMPLETED", TaskTitle: "Bagels"})
	if len(rec.events) != 1 || rec.events[0].PhotoURL != "" {
		t.Fatalf("a photo lookup failure must still send the text: %+v", rec.events)
	}
}

// M3.6 round 1: the real photo lookup fails closed. Without a signing
// client (this test binary), a stored reference — ours or not — never
// becomes a link; the amount still travels; a task with no purchases
// yields nothing.
func TestPhotosOfFailsClosed(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)
	ctx := context.Background()
	prev := photosOf
	t.Cleanup(func() { photosOf = prev })
	// No signer in this process: the "ours but unsigned" cases below depend
	// on it. The client is cached once per binary, so a run with storage
	// env set would sign for real — skip rather than call out.
	t.Setenv("SUPABASE_PROJECT_URL", "")
	t.Setenv("SUPABASE_SERVICE_ROLE_KEY", "")
	if taskPhotoStorageClient != nil {
		t.Skip("storage client already initialised in this binary")
	}
	t.Cleanup(func() { taskPhotoStorageOnce = sync.Once{}; taskPhotoStorageClient = nil }) // consumed with empty env here
	_ = chatStatusHook(sqldb, nil)                                                         // installs the DB-backed photosOf
	set := func(photo, receipt string, cents int) {
		t.Helper()
		if _, err := db.Exec(ctx, `update public.tasks set completion_photo_url = nullif($2,''), receipt_photo_url = nullif($3,''), receipt_amount_cents = $4 where id = $1::uuid`,
			w.taskID, photo, receipt, cents); err != nil {
			t.Fatal(err)
		}
	}
	// Ours, but no signer in this process: no link, amount still stated.
	set("https://x.supabase.co/storage/v1/object/public/task-completions/completions/"+w.taskID+"/1.jpg", "https://x.supabase.co/storage/v1/object/public/task-completions/completions/"+w.taskID+"/2.jpg", 1240)
	p, err := photosOf(ctx, w.taskID)
	if err != nil || p.Photo != "" || p.Receipt != "" || p.ReceiptTotal != "$12.40" {
		t.Fatalf("%+v %v", p, err)
	}
	// Not ours: never passed through.
	set("https://evil.example/home.jpg", "", 0)
	if p, err = photosOf(ctx, w.taskID); err != nil || p.Photo != "" || p.Receipt != "" || p.ReceiptTotal != "" {
		t.Fatalf("%+v %v", p, err)
	}
	// The task's own key, as the upload handler writes it, passes the
	// prefix check and stops only at the missing signer.
	if _, reason := signedTaskPhotoURL("https://x.supabase.co/storage/v1/object/public/task-completions/completions/"+w.taskID+"/1.jpg", w.taskID, taskPhotoLinkTTL); reason != "storage not configured" {
		t.Fatalf("own key: reason %q", reason)
	}
	// Ours, another task's key: stopped here (completeTask does not bind
	// the key to the task), whatever the signer would say.
	if _, reason := signedTaskPhotoURL("https://x.supabase.co/storage/v1/object/public/task-completions/completions/other-task/1.jpg", w.taskID, taskPhotoLinkTTL); reason != "not this task's photo" {
		t.Fatalf("reason %q", reason)
	}
	// Nothing stored: nothing.
	set("", "", 0)
	if p, err = photosOf(ctx, w.taskID); err != nil || p != (completionPhotos{}) {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestFormatCentsUSD(t *testing.T) {
	for cents, want := range map[int]string{0: "$0.00", 5: "$0.05", 99: "$0.99", 100: "$1.00", 123456: "$1234.56"} {
		if got := formatCentsUSD(cents); got != want {
			t.Errorf("%d: %q want %q", cents, got, want)
		}
	}
}

// M3.2c round 3: the requester's EXTENSION_RESOLVED names the request it
// is about, end to end — extensions.go sets it, the chat event carries it —
// so the chat clears the right armed question and no other.
func TestResolutionNamesItsRequest(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)
	var mu sync.Mutex
	var got []notify.CreateNotificationInput
	prev := notify.ChatHook
	notify.ChatHook = func(in notify.CreateNotificationInput) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, in)
	}
	t.Cleanup(func() { notify.ChatHook = prev })
	_, body := requestBudgetAs(t, w.taskID, w.supporterID, `{"requested_cents": 800, "fallback": "skip_item"}`)
	extID, _ := body["id"].(string)
	if code, body := resolveExtensionAs(t, approveExtension, w.taskID, extID, w.requesterID); code != http.StatusOK {
		t.Fatalf("approve: %d (%v)", code, body)
	}
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, in := range got {
			if in.Type == "EXTENSION_RESOLVED" && in.UserID == w.requesterID {
				return true
			}
		}
		return false
	}, "the requester's resolution reached the chat hook")
	mu.Lock()
	defer mu.Unlock()
	for _, in := range got {
		if in.Type != "EXTENSION_RESOLVED" {
			continue
		}
		if in.UserID == w.requesterID && in.ExtensionID != extID {
			t.Fatalf("requester copy must name the request: %+v", in)
		}
		if ev := statusEvent(in); in.UserID == w.requesterID && ev.ExtensionID != extID {
			t.Fatalf("statusEvent must carry it: %+v", ev)
		}
	}
}

// Staging 2026-10-25 21:11: a transient DB error on the requester lookup
// lost a time request (never asked on the phone; expired). The lookups
// retry before the event is dropped.
func TestStatusHookRetriesTransientLookupFailures(t *testing.T) {
	prev := lookupRetryWaits
	lookupRetryWaits = []time.Duration{0, time.Millisecond, time.Millisecond}
	defer func() { lookupRetryWaits = prev }()
	calls := 0
	requesterOf := func(context.Context, string) (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("failed to connect to `user=postgres`")
		}
		return "u1", nil
	}
	pendingOf := func(context.Context, string, string) (channel.PendingApproval, error) {
		return channel.PendingApproval{ID: "ext-1", Kind: "time", Minutes: 15}, nil
	}
	n := &recNotifier{}
	hook := statusHook(requesterOf, pendingOf, n)
	hook(notify.CreateNotificationInput{Type: "TIME_EXTENSION_REQUESTED", TaskID: "t1", UserID: "u1"})
	if calls != 3 || len(n.got) != 1 || n.got[0] != "u1:TIME_EXTENSION_REQUESTED" {
		t.Fatalf("calls %d got %v", calls, n.got)
	}
}
