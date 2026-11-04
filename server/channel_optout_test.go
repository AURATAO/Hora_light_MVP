package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"hora-auth/internal/channel"
	"hora-auth/internal/notify"
)

// The ops alert when a number stops texts: on the hand-off alert path
// (email + ops text), last four digits only, and it names the open tasks.
func TestOptOutOpsAlert(t *testing.T) {
	prev := sendOpsEmail
	defer func() { sendOpsEmail = prev }()
	var mu sync.Mutex
	var mails []notify.EmailPayload
	sendOpsEmail = func(p notify.EmailPayload) error {
		mu.Lock()
		mails = append(mails, p)
		mu.Unlock()
		return nil
	}
	t.Setenv("CHANNEL_OPS_EMAILS", "ops@example.com")
	t.Setenv("CHANNEL_OPS_NUMBERS", "+1 (555) 010-0199")
	sms := &stubAdapter{}
	n := newOpsNotifier(nil, sms, &stubToucher{})
	n.lookup = func(context.Context, string) (string, error) { return "+15551234567", nil }
	notice := channel.HandoffNotice{IdentityID: "ident-1", Channel: channel.IMessage, PhoneHint: channel.PhoneHint("+15551234567"),
		Reason: channel.HandoffOptedOut, OptedOut: true,
		OpenTasks: []channel.TaskRef{{ID: "task-9", Title: "Laundry <pickup>"}}}
	if err := n.NotifyOps(context.Background(), notice); err != nil {
		t.Fatal(err)
	}
	if len(mails) != 1 || len(sms.sent) != 1 {
		t.Fatalf("mails %d texts %d", len(mails), len(sms.sent))
	}
	m := mails[0]
	for _, want := range []string{"Texts stopped", "•••• 4567", "HAS 1 OPEN TASK"} {
		if !strings.Contains(m.Subject, want) {
			t.Errorf("subject %q missing %q", m.Subject, want)
		}
	}
	for _, want := range []string{"They have open tasks", "Laundry &lt;pickup&gt;", "task-9", "START", "not handed off"} {
		if !strings.Contains(m.Html, want) {
			t.Errorf("email missing %q", want)
		}
	}
	if !strings.Contains(sms.sent[0].Text, "4567") || !strings.Contains(sms.sent[0].Text, "HAS 1 OPEN TASK") {
		t.Errorf("ops text %q", sms.sent[0].Text)
	}
	for _, s := range []string{m.Subject, m.Html, sms.sent[0].Text} {
		if strings.Contains(s, "5551234567") || strings.Contains(s, "555123") {
			t.Fatalf("full number leaked: %q", s)
		}
	}
	if strings.Contains(m.Html, "/release") {
		t.Error("an opt-out is not a hand-off: no release instructions")
	}

	// Open tasks unreadable: the alert says so rather than "none".
	mails = nil
	notice.OpenTasks, notice.OpenTasksUnknown = nil, true
	if err := n.NotifyOps(context.Background(), notice); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mails[0].Subject, "UNKNOWN") || !strings.Contains(mails[0].Html, "could not be read") {
		t.Fatalf("unknown open tasks: %q", mails[0].Subject)
	}
}

func TestTextsStoppedMessage(t *testing.T) {
	prev := userTextsStoppedFn
	defer func() { userTextsStoppedFn = prev }()
	at := time.Date(2026, 11, 3, 15, 0, 0, 0, time.UTC)
	userTextsStoppedFn = func(context.Context, string) (bool, time.Time, error) { return true, at, nil }
	t.Setenv("SENDBLUE_FROM_NUMBER", "+12125550100")
	got := textsStoppedFor(context.Background(), "u1")
	want := "You've stopped texts from HO:RA. Text START to (212) 555-0100 to turn them back on."
	if got == nil || got.Message != want || got.ResumeNumber != "+12125550100" || !got.StoppedAt.Equal(at) {
		t.Fatalf("got %+v", got)
	}
	t.Setenv("SENDBLUE_FROM_NUMBER", "")
	if got := textsStoppedFor(context.Background(), "u1"); got == nil || !strings.Contains(got.Message, "Text START to HO:RA's number") {
		t.Fatalf("no number configured: %+v", got)
	}
	userTextsStoppedFn = func(context.Context, string) (bool, time.Time, error) { return false, time.Time{}, nil }
	if textsStoppedFor(context.Background(), "u1") != nil {
		t.Fatal("texts on: no field")
	}
	// Migration not applied: the column is missing, the field is left out.
	userTextsStoppedFn = func(context.Context, string) (bool, time.Time, error) {
		return false, time.Time{}, errors.New(`column "opted_out_at" does not exist`)
	}
	if textsStoppedFor(context.Background(), "u1") != nil || requesterTextsStopped(context.Background(), "u1") {
		t.Fatal("an unreadable flag must read as texts on")
	}
}

// The supporter on an open task sees that the requester can't be reached by
// text; the requester and anyone else do not get the field.
func TestGetTaskSaysRequesterTextsStopped(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	prev := userTextsStoppedFn
	defer func() { userTextsStoppedFn = prev }()
	userTextsStoppedFn = func(_ context.Context, uid string) (bool, time.Time, error) {
		return uid == w.requesterID, time.Now(), nil
	}
	read := func(uid, email string) map[string]any {
		code, rec := callTaskHandler(t, getTask, http.MethodGet, "/tasks/"+w.taskID, w.taskID, uid, email, "", nil)
		if code != http.StatusOK {
			t.Fatalf("getTask as %s: %d %s", email, code, rec.Body.String())
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if v := read(w.supporterID, oldSupporterEmail)["requester_texts_stopped"]; v != true {
		t.Fatalf("supporter view: requester_texts_stopped = %v", v)
	}
	if _, ok := read(w.requesterID, requesterEmail)["requester_texts_stopped"]; ok {
		t.Fatal("the requester's own view carries the supporter's flag")
	}
	userTextsStoppedFn = func(context.Context, string) (bool, time.Time, error) { return false, time.Time{}, nil }
	if _, ok := read(w.supporterID, oldSupporterEmail)["requester_texts_stopped"]; ok {
		t.Fatal("texts on: field present")
	}
}

// Ops are told about tasks still under way: open (assigned or not) and
// parked on a payment step — not finished ones, not someone else's.
func TestOpenOrPendingTasks(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	ctx := context.Background()
	b := &chatBooker{sqlDB: sqldb}
	got, err := b.OpenOrPendingTasks(ctx, w.requesterID)
	if err != nil || len(got) != 1 || got[0].ID != w.taskID {
		t.Fatalf("open: %v %v", got, err)
	}
	if _, err := db.Exec(ctx, `update public.tasks set status = 'pending_payment' where id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	if got, err := b.OpenOrPendingTasks(ctx, w.requesterID); err != nil || len(got) != 1 {
		t.Fatalf("pending_payment: %v %v", got, err)
	}
	if _, err := db.Exec(ctx, `update public.tasks set status = 'completed' where id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	if got, err := b.OpenOrPendingTasks(ctx, w.requesterID); err != nil || len(got) != 0 {
		t.Fatalf("completed: %v %v", got, err)
	}
	if got, _ := b.OpenOrPendingTasks(ctx, w.supporterID); len(got) != 0 {
		t.Fatalf("supporter's own: %v", got)
	}
}

// A message from a stopped number (channel/optout_forward.go): email and ops
// text, last four digits only, the words (escaped), open tasks, and never a
// reply path by text. Urgent ones say so in the subject and the text.
func TestOptedOutMessageOpsAlert(t *testing.T) {
	prev := sendOpsEmail
	defer func() { sendOpsEmail = prev }()
	var mails []notify.EmailPayload
	var mu sync.Mutex
	sendOpsEmail = func(p notify.EmailPayload) error {
		mu.Lock()
		mails = append(mails, p)
		mu.Unlock()
		return nil
	}
	t.Setenv("CHANNEL_OPS_EMAILS", "ops@example.com")
	t.Setenv("CHANNEL_OPS_NUMBERS", "+1 (555) 010-0199, +15551234567")
	sms := &stubAdapter{}
	n := newOpsNotifier(nil, sms, &stubToucher{})
	n.lookup = func(context.Context, string) (string, error) { return "+15551234567", nil }
	at := time.Date(2026, 11, 4, 16, 8, 0, 0, time.UTC)
	notice := channel.HandoffNotice{IdentityID: "ident-1", Channel: channel.IMessage, PhoneHint: channel.PhoneHint("+15551234567"),
		Reason: channel.HandoffOptedOutMessage, OptedOut: true,
		OpenTasks: []channel.TaskRef{{ID: "task-9", Title: "Laundry pickup"}},
		Thread: []channel.ThreadMessage{{Direction: "inbound", Text: "is <she> close?", At: at},
			{Direction: "inbound", Text: "the door code is 4521", At: at.Add(time.Minute)}}}
	if err := n.NotifyOps(context.Background(), notice); err != nil {
		t.Fatal(err)
	}
	// The person's own number is in CHANNEL_OPS_NUMBERS: never texted.
	if len(mails) != 1 || len(sms.sent) != 1 || sms.sent[0].To == "+15551234567" {
		t.Fatalf("mails %d texts %+v", len(mails), sms.sent)
	}
	m := mails[0]
	if !strings.Contains(m.Subject, "Message from a number that stopped texts") || strings.Contains(m.Subject, "URGENT") ||
		!strings.Contains(m.Subject, "•••• 4567") || !strings.Contains(m.Subject, "HAS 1 OPEN TASK") {
		t.Errorf("subject %q", m.Subject)
	}
	for _, want := range []string{"Do not reply by text", "is &lt;she&gt; close?", "the door code is 4521", "task-9", "not linked to an account", "10m0s"} {
		if !strings.Contains(m.Html, want) {
			t.Errorf("email missing %q", want)
		}
	}
	if strings.Contains(m.Html, "/send") || strings.Contains(m.Html, "/release") {
		t.Error("no reply-by-text instructions for a stopped number")
	}
	txt := sms.sent[0].Text
	if !strings.Contains(txt, "the door code is 4521") || !strings.Contains(txt, "2 messages") || !strings.Contains(txt, "Do NOT text them") {
		t.Errorf("ops text %q", txt)
	}
	for _, s := range []string{m.Subject, m.Html, txt} {
		if strings.Contains(s, "5551234567") || strings.Contains(s, "555123") {
			t.Fatalf("full number leaked: %q", s)
		}
	}

	mails, sms.sent = nil, nil
	notice.Urgent, notice.UrgentReason = true, "safety"
	notice.Thread = []channel.ThreadMessage{{Direction: "inbound", Text: strings.Repeat("help ", 60), At: at}}
	if err := n.NotifyOps(context.Background(), notice); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(mails[0].Subject, "[HO:RA] URGENT (safety)") || !strings.Contains(mails[0].Html, "URGENT (safety)") {
		t.Errorf("urgent subject %q", mails[0].Subject)
	}
	if txt := sms.sent[0].Text; !strings.HasPrefix(txt, "[HO:RA] URGENT (safety):") || !strings.Contains(txt, "…") || len([]rune(txt)) > 320 {
		t.Errorf("urgent ops text %q", txt)
	}
}

// Linked: the account's email is in the alert.
func TestOptedOutMessageOpsAlertNamesTheAccount(t *testing.T) {
	setupCreatedViaDB(t)
	var uid string
	if err := db.QueryRow(context.Background(), `INSERT INTO public.users (email, name) VALUES ('rita@example.test', 'Rita') RETURNING id::text`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	prev := sendOpsEmail
	defer func() { sendOpsEmail = prev }()
	var mails []notify.EmailPayload
	sendOpsEmail = func(p notify.EmailPayload) error { mails = append(mails, p); return nil }
	t.Setenv("CHANNEL_OPS_EMAILS", "ops@example.com")
	t.Setenv("CHANNEL_OPS_NUMBERS", "")
	n := newOpsNotifier(sqldb, nil, nil)
	err := n.NotifyOps(context.Background(), channel.HandoffNotice{IdentityID: "ident-1", Channel: channel.IMessage, PhoneHint: "•••• 4567",
		UserID: uid, Reason: channel.HandoffOptedOutMessage, OptedOut: true,
		Thread: []channel.ThreadMessage{{Direction: "inbound", Text: "hello", At: time.Now()}}})
	if err != nil || len(mails) != 1 || !strings.Contains(mails[0].Html, "rita@example.test") || !strings.Contains(mails[0].Html, "No open tasks") {
		t.Fatalf("err %v mails %+v", err, mails)
	}
}
