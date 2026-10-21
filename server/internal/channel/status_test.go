package channel

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestStatusTextCoversTheRequesterJourney(t *testing.T) {
	cases := []struct {
		ev   StatusEvent
		want string
	}{
		{StatusEvent{Type: "ORDER_ACCEPTED", SupporterName: "Maya", TaskTitle: "Laundry pickup"}, "Maya accepted “Laundry pickup”."},
		{StatusEvent{Type: "SUPPORTER_ARRIVED", TaskTitle: "Laundry pickup"}, "Your supporter has arrived"},
		{StatusEvent{Type: "CLOCK_IN", SupporterName: "Maya", TaskTitle: "x"}, "The clock is running."},
		{StatusEvent{Type: "CLOCK_OUT", SupporterName: "Maya", TaskTitle: "x", TotalLogged: "42 min"}, "Time so far: 42 min."},
		{StatusEvent{Type: "COMPLETED", TaskTitle: "x", TotalLogged: "42 min", FinalCost: "$31.50"}, "Total: $31.50"},
		{StatusEvent{Type: "CANCELLED", Body: "Your task has been cancelled. Reason: changed plans"}, "Reason: changed plans"},
	}
	for _, c := range cases {
		got, ok := StatusText(c.ev)
		if !ok || !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q (ok=%v), want it to contain %q", c.ev.Type, got, ok, c.want)
		}
	}
}

// Events that ask for an action in the app (or belong to the supporter) are
// not sent until the chat can act on them — "approve it in the app" is a dead
// end for someone with no app.
func TestStatusTextSkipsWhatTheChatCannotActOn(t *testing.T) {
	for _, typ := range []string{
		"TIME_EXTENSION_REQUESTED", "BUDGET_INCREASE_REQUESTED", "EXTENSION_RESOLVED",
		"TIME_CAP_REACHED", "BALANCE_DUE", "RECEIPT",
		"COMPLETED_SUPPORTER", "PAYOUT_SENT", "PAYOUT_DEPOSITED", "NEW_MESSAGE",
	} {
		if got, ok := StatusText(StatusEvent{Type: typ, Body: "Approve it in the app"}); ok {
			t.Errorf("%s should not reach the chat, got %q", typ, got)
		}
	}
	if _, ok := StatusText(StatusEvent{Type: "CANCELLED"}); ok {
		t.Error("an empty body must not send an empty message")
	}
}

type memStatus struct {
	target StatusTarget
	found  bool
}

func (m *memStatus) StatusTargetFor(context.Context, string, string) (StatusTarget, bool, error) {
	return m.target, m.found, nil
}

func TestNotifyStatusSendsToTheBookingThread(t *testing.T) {
	g := newRig("")
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{Channel: IMessage, Address: me}}}
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: "ORDER_ACCEPTED", TaskID: "task-1", SupporterName: "Maya", TaskTitle: "Laundry"}); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 1 || g.ad.sent[0].To != me || !strings.HasPrefix(g.ad.sent[0].Text, "Maya accepted") {
		t.Fatalf("sent %+v", g.ad.sent)
	}
	if g.st.lastOutboundTask() != "task-1" {
		t.Fatal("status message must be recorded against its task, so a reply can find it")
	}
}

func TestNotifyStatusQuietWhenNoThreadOrNotAChatEvent(t *testing.T) {
	g := newRig("")
	g.r.Status = &memStatus{found: false}
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: "ORDER_ACCEPTED", TaskID: "t"}); err != nil {
		t.Fatal(err)
	}
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{Channel: IMessage, Address: me}}}
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: "PAYOUT_SENT", TaskID: "t"}); err != nil {
		t.Fatal(err)
	}
	g.r.Status = nil
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: "ORDER_ACCEPTED", TaskID: "t"}); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 0 {
		t.Fatalf("nothing should be sent, got %+v", g.ad.sent)
	}
}

func TestNotifyStatusRespectsWhatsAppWindow(t *testing.T) {
	g := newRig("")
	wa := &fakeSendAdapter{}
	g.r.Adapters[WhatsApp] = wa
	now := time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)
	g.r.Now = func() time.Time { return now }

	stale := now.Add(-25 * time.Hour)
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{Channel: WhatsApp, Address: me}, LastInboundAt: &stale}}
	if err := g.r.NotifyStatus(context.Background(), "u", StatusEvent{Type: "CLOCK_IN", TaskID: "t"}); err != nil {
		t.Fatal(err)
	}
	if len(wa.sent) != 0 {
		t.Fatal("outside the 24h window a free-form WhatsApp message must not be sent")
	}

	fresh := now.Add(-2 * time.Hour)
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{Channel: WhatsApp, Address: me}, LastInboundAt: &fresh}}
	if err := g.r.NotifyStatus(context.Background(), "u", StatusEvent{Type: "CLOCK_IN", TaskID: "t"}); err != nil {
		t.Fatal(err)
	}
	if len(wa.sent) != 1 {
		t.Fatal("inside the window it should send")
	}
}

func TestStatusTextOnMyWayIsChatOnlyCopy(t *testing.T) {
	got, ok := StatusText(StatusEvent{Type: "ON_MY_WAY", SupporterName: "Maya", TaskTitle: "Laundry"})
	if !ok || !strings.HasPrefix(got, "Maya is on the way for “Laundry”") {
		t.Fatalf("%q %v", got, ok)
	}
}

// M3.6: the done message carries the completion photo; purchases add a
// second message with the receipt photo and the amount. Both are sent to
// the booking thread, tagged with the task.
func TestCompletedCarriesPhotoAndReceipt(t *testing.T) {
	g := newRig("")
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	ev := StatusEvent{Type: "COMPLETED", TaskID: "task-1", TaskTitle: "Bagel run", SupporterName: "Sam", TotalLogged: "32 min", FinalCost: "$27.40",
		PhotoURL:        "https://x.supabase.co/storage/v1/object/sign/task-completions/c/1.jpg?token=a",
		ReceiptPhotoURL: "https://x.supabase.co/storage/v1/object/sign/task-completions/r/1.jpg?token=b", ReceiptTotal: "$12.40"}
	if err := g.r.NotifyStatus(context.Background(), "user-1", ev); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 2 {
		t.Fatalf("sent %d", len(g.ad.sent))
	}
	done, receipt := g.ad.sent[0], g.ad.sent[1]
	if !strings.HasPrefix(done.Text, "Done: “Bagel run”.") || !strings.Contains(done.Text, "Purchases: $12.40 (receipt below)") || done.MediaURL != ev.PhotoURL {
		t.Fatalf("done: %+v", done)
	}
	if receipt.Text != "Receipt for the purchases: $12.40." || receipt.MediaURL != ev.ReceiptPhotoURL {
		t.Fatalf("receipt: %+v", receipt)
	}
	if g.st.lastOutboundTask() != "task-1" {
		t.Fatal("receipt must be recorded against the task")
	}
	// No purchases: one message, with the photo; no photo: text only.
	g = newRig("")
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: "COMPLETED", TaskID: "task-1", TaskTitle: "Laundry", PhotoURL: "https://cdn/p.jpg"}); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 1 || g.ad.sent[0].MediaURL != "https://cdn/p.jpg" || strings.Contains(g.ad.sent[0].Text, "Purchases") {
		t.Fatalf("%+v", g.ad.sent)
	}
	g = newRig("")
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: "COMPLETED", TaskID: "task-1", TaskTitle: "Laundry"}); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 1 || g.ad.sent[0].MediaURL != "" {
		t.Fatalf("%+v", g.ad.sent)
	}
}
