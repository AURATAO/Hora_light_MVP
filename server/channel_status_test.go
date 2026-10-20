package main

import (
	"context"
	"testing"

	"hora-auth/internal/channel"
	"hora-auth/internal/notify"
)

type recNotifier struct{ got []string }

func (r *recNotifier) NotifyStatus(_ context.Context, userID string, ev channel.StatusEvent) error {
	r.got = append(r.got, userID+":"+ev.Type)
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
