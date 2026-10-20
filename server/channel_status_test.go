package main

import (
	"context"
	"strings"
	"testing"

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
	}, func(context.Context, string, string) (string, error) { return "ext-1", nil }, rec)

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
	got := setupLink("https://x.test", "tok/en+")
	if strings.Contains(got, "?") || !strings.Contains(got, "/channel/setup#t=") {
		t.Fatalf("setup link exposes the token to the server: %s", got)
	}
}

// S-11 condition 5: the YES must answer the request the question described.
// Time and budget requests can be pending at once; the lookup is by the kind
// the notification is about, never "newest on the task".
func TestChatStatusHookArmsTheRequestOfTheRightKind(t *testing.T) {
	rec := &recNotifier{}
	var asked []string
	hook := statusHook(func(context.Context, string) (string, error) { return "requester-1", nil },
		func(_ context.Context, taskID, kind string) (string, error) {
			asked = append(asked, taskID+"/"+kind)
			return "ext-" + kind, nil
		}, rec)
	hook(notify.CreateNotificationInput{UserID: "requester-1", TaskID: "t1", Type: "BUDGET_INCREASE_REQUESTED", Body: "x"})
	hook(notify.CreateNotificationInput{UserID: "requester-1", TaskID: "t1", Type: "TIME_EXTENSION_REQUESTED", Body: "x"})
	if len(asked) != 2 || asked[0] != "t1/budget" || asked[1] != "t1/time" {
		t.Fatalf("lookups %v", asked)
	}
	if len(rec.events) != 2 || rec.events[0].ExtensionID != "ext-budget" || rec.events[1].ExtensionID != "ext-time" {
		t.Fatalf("armed %+v", rec.events)
	}
}
