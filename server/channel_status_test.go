package main

import (
	"context"
	"errors"
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
