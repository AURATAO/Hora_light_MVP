package main

import (
	"context"
	"testing"
	"time"

	"hora-auth/internal/channel"
)

// chatLive.LiveStatus is what answers "where is he?" in the chat
// (channel_live.go). It was shipped 2026-10-25 with unit coverage of the
// WORDS (internal/channel/where.go) but none of the query, and the query is
// where the two things a chat requester must never be told wrongly live: the
// boundary (whose task this is) and the difference between "no fix yet" and
// "hasn't set off". Declared as owed in D-17; this is it.
//
// Every case runs against a real database through the same seams the handler
// uses, so a column rename or a changed state rule fails here.

func liveStatusFor(t *testing.T, userID, taskID string) (channel.LiveStatus, error) {
	t.Helper()
	return (&chatLive{sqlDB: sqldb}).LiveStatus(context.Background(), userID, taskID)
}

func setTaskPlace(t *testing.T, taskID string, lat, lng float64) {
	t.Helper()
	if _, err := db.Exec(context.Background(),
		`update public.tasks set job_lat = $2, job_lng = $3 where id = $1::uuid`, taskID, lat, lng); err != nil {
		t.Fatalf("set task place: %v", err)
	}
}

// THE BOUNDARY. Only the requester of an open, assigned task gets an answer.
// Everyone else gets an error, and the chat says nothing rather than
// something — a supporter's live position is not a fact to hand out by text
// to whoever asked (S-12).
func TestChatLiveStatusAnswersTheRequesterOnly(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskPlace(t, w.taskID, 40.7580, -73.9855)
	seedPing(t, w.taskID, w.supporterID, 40.7590, -73.9860, 10*time.Second)

	if _, err := liveStatusFor(t, w.taskID, w.taskID); err == nil {
		t.Error("a non-requester id got an answer")
	}
	if _, err := liveStatusFor(t, w.supporterID, w.taskID); err == nil {
		t.Error("the SUPPORTER got the answer meant for the requester")
	}
	if _, err := liveStatusFor(t, w.requesterID, "00000000-0000-0000-0000-000000000009"); err == nil {
		t.Error("an unknown task id got an answer")
	}
	if _, err := liveStatusFor(t, w.requesterID, w.taskID); err != nil {
		t.Fatalf("the requester of an open assigned task should get an answer: %v", err)
	}

	// A task that is over is not a live task, whoever asks.
	if _, err := db.Exec(context.Background(),
		`update public.tasks set status='completed' where id=$1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := liveStatusFor(t, w.requesterID, w.taskID); err == nil {
		t.Error("a completed task still answered a live question")
	}
}

// Nobody has sent a position. The two silences are NOT the same, and this is
// the distinction the chat copy hangs on: "they haven't set off yet" versus
// "they set off but I can't see them".
func TestChatLiveStatusTellsNoFixFromNotSetOff(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskPlace(t, w.taskID, 40.7580, -73.9855)

	st, err := liveStatusFor(t, w.requesterID, w.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != channel.LiveState(liveStateUnavailable) {
		t.Fatalf("state = %q, want unavailable", st.State)
	}
	if !st.NotYetOnTheWay || st.SetOffNoFix {
		t.Errorf("no enroute_at, no ping, never clocked in → not on the way: %+v", st)
	}

	// "On my way" pressed, still no fix (the web app sends no GPS at all, so
	// this is the ordinary shape there, not an edge case).
	if _, err := db.Exec(context.Background(),
		`update public.tasks set enroute_at = now() where id=$1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	st, err = liveStatusFor(t, w.requesterID, w.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if !st.SetOffNoFix || st.NotYetOnTheWay {
		t.Errorf("enroute_at set, no ping → set off, no fix: %+v", st)
	}
}

// A closed session means they were there, so "hasn't set off" is false even
// with no ping and no enroute_at — the state the derivation got wrong in
// review (D-17, where-r1).
func TestChatLiveStatusAWorkedSessionIsNotNotSetOff(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskPlace(t, w.taskID, 40.7580, -73.9855)
	seedWorklog(t, w.taskID, 30, false) // closed session, no pings

	st, err := liveStatusFor(t, w.requesterID, w.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if st.NotYetOnTheWay {
		t.Errorf("somebody who already worked a session has set off: %+v", st)
	}
	if st.SetOffNoFix {
		t.Errorf("no enroute_at, so not the set-off-no-fix shape either: %+v", st)
	}
}

// Working: an open session wins over distance, and the minutes come from the
// same logged-minutes function the bill uses.
func TestChatLiveStatusWorkingCarriesTheClock(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskPlace(t, w.taskID, 40.7580, -73.9855)
	seedWorklog(t, w.taskID, 20, true) // open session, started 20 min ago
	seedPing(t, w.taskID, w.supporterID, 40.9000, -73.9000, 10*time.Second)

	st, err := liveStatusFor(t, w.requesterID, w.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != channel.LiveState(liveStateWorking) {
		t.Fatalf("state = %q, want working (an open session beats being far away)", st.State)
	}
	if st.WorkedMinutes < 19 || st.WorkedMinutes > 22 {
		t.Errorf("worked minutes = %d, want about 20", st.WorkedMinutes)
	}
}

// A fix older than livePingStaleAfter is not a position any more. Reporting a
// stale one as "3 blocks away" would be the chat inventing a fact.
func TestChatLiveStatusStalePingIsNotAPosition(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskPlace(t, w.taskID, 40.7580, -73.9855)
	seedPing(t, w.taskID, w.supporterID, 40.7590, -73.9860, livePingStaleAfter+time.Minute)

	st, err := liveStatusFor(t, w.requesterID, w.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != channel.LiveState(liveStateUnavailable) {
		t.Fatalf("state = %q, want unavailable for a stale fix", st.State)
	}
	if st.DistanceMeters != nil {
		t.Errorf("a stale fix must not carry a distance: %v", *st.DistanceMeters)
	}
	// There IS a ping, so neither "hasn't set off" nor "set off, no fix".
	if st.NotYetOnTheWay || st.SetOffNoFix {
		t.Errorf("a stale fix is still a fix: %+v", st)
	}
	if st.LastSeen.IsZero() {
		t.Error("last seen should still be reported, so the copy can say how old it is")
	}
}

// A fresh fix near the task: a distance the copy can turn into blocks.
func TestChatLiveStatusFreshPingCarriesDistance(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskPlace(t, w.taskID, 40.7580, -73.9855)
	seedPing(t, w.taskID, w.supporterID, 40.7595, -73.9855, 15*time.Second) // ~165 m north

	st, err := liveStatusFor(t, w.requesterID, w.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if st.DistanceMeters == nil {
		t.Fatalf("a fresh fix with task coordinates must carry a distance: %+v", st)
	}
	if *st.DistanceMeters < 100 || *st.DistanceMeters > 300 {
		t.Errorf("distance = %.0f m, want roughly 165", *st.DistanceMeters)
	}
	if st.State == channel.LiveState(liveStateUnavailable) {
		t.Error("a fresh fix is a position")
	}
}

// No coordinates on the task — a chat-booked task whose address could not be
// geocoded. There is a fix, so the person has set off, but no distance can be
// computed and none must be invented.
func TestChatLiveStatusWithoutTaskCoordinates(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedPing(t, w.taskID, w.supporterID, 40.7590, -73.9860, 10*time.Second)

	st, err := liveStatusFor(t, w.requesterID, w.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if st.DistanceMeters != nil {
		t.Errorf("no task coordinates, so no distance: %v", *st.DistanceMeters)
	}
	if st.NotYetOnTheWay {
		t.Errorf("there is a fix, so they have set off: %+v", st)
	}
}
