package channel

import (
	"context"
	"errors"
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
	target := &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	ev := StatusEvent{Type: "COMPLETED", TaskID: "task-1", TaskTitle: "Bagel run", SupporterName: "Sam", TotalLogged: "32 min", FinalCost: "$27.40",
		PhotoURL:        "https://x.supabase.co/storage/v1/object/sign/task-completions/c/1.jpg?token=a",
		ReceiptPhotoURL: "https://x.supabase.co/storage/v1/object/sign/task-completions/r/1.jpg?token=b", ReceiptTotal: "$12.40"}
	g := newRig("")
	g.r.Status = target
	if err := g.r.NotifyStatus(context.Background(), "user-1", ev); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 2 {
		t.Fatalf("sent %d", len(g.ad.sent))
	}
	done, receipt := g.ad.sent[0], g.ad.sent[1]
	if !strings.HasPrefix(done.Text, "Done: “Bagel run”.") || !strings.Contains(done.Text, "Purchases: $12.40 (receipt photo follows)") || done.MediaURL != ev.PhotoURL {
		t.Fatalf("done: %+v", done)
	}
	if receipt.Text != "Receipt: $12.40." || receipt.MediaURL != ev.ReceiptPhotoURL {
		t.Fatalf("receipt: %+v", receipt)
	}
	if g.st.lastOutboundTask() != "task-1" {
		t.Fatal("receipt must be recorded against the task")
	}
	// The signed links never reach the message store (S-12).
	for _, body := range g.st.storedBodies() {
		if strings.Contains(body, "token=") || strings.Contains(body, "supabase.co") {
			t.Fatalf("signed link stored: %q", body)
		}
	}

	// No purchases: one message, with the photo; no photo: text only.
	g = newRig("")
	g.r.Status = target
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: "COMPLETED", TaskID: "task-1", TaskTitle: "Laundry", PhotoURL: "https://cdn/p.jpg"}); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 1 || g.ad.sent[0].MediaURL != "https://cdn/p.jpg" || strings.Contains(g.ad.sent[0].Text, "Purchases") {
		t.Fatalf("%+v", g.ad.sent)
	}
	g = newRig("")
	g.r.Status = target
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: "COMPLETED", TaskID: "task-1", TaskTitle: "Laundry"}); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 1 || g.ad.sent[0].MediaURL != "" {
		t.Fatalf("%+v", g.ad.sent)
	}

	// Purchases without a receipt photo: the amount is still stated, and
	// where the receipt is; no second message.
	g = newRig("")
	g.r.Status = target
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: "COMPLETED", TaskID: "task-1", TaskTitle: "Laundry", ReceiptTotal: "$3.00"}); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 1 || !strings.Contains(g.ad.sent[0].Text, "Purchases: $3.00") {
		t.Fatalf("%+v", g.ad.sent)
	}

	// A receipt photo without an amount: "see photo".
	g = newRig("")
	g.r.Status = target
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: "COMPLETED", TaskID: "task-1", TaskTitle: "Laundry", ReceiptPhotoURL: "https://cdn/r.jpg"}); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 2 || g.ad.sent[1].Text != CopyReceiptPhotoOnly || g.ad.sent[1].MediaURL != "https://cdn/r.jpg" {
		t.Fatalf("%+v", g.ad.sent)
	}

	// The provider refuses the media: the words still go, once, on their
	// own — and the receipt amount with where the photo is.
	g = newRig("")
	g.r.Status = target
	g.ad.mediaErr = &ProviderStatusError{Provider: "sendblue", Status: 400, Text: "media_url unreachable"}
	if err := g.r.NotifyStatus(context.Background(), "user-1", ev); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 2 || g.ad.sent[0].MediaURL != "" || !strings.HasPrefix(g.ad.sent[0].Text, "Done: “Bagel run”.") ||
		g.ad.sent[1].MediaURL != "" || g.ad.sent[1].Text != "Receipt: $12.40. (The photo couldn't be sent here.)" {
		t.Fatalf("%+v", g.ad.sent)
	}

	// The receipt photo refused with no amount known: where it is.
	g = newRig("")
	g.r.Status = target
	g.ad.mediaErr = &ProviderStatusError{Provider: "sendblue", Status: 400, Text: "media_url unreachable"}
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: "COMPLETED", TaskID: "task-1", TaskTitle: "Laundry", ReceiptPhotoURL: "https://cdn/r.jpg"}); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 2 || g.ad.sent[1].Text != CopyReceiptPhotoNotSent {
		t.Fatalf("%+v", g.ad.sent)
	}

	// A transport failure (the message may have gone through), an auth or
	// rate-limit answer, or a not-configured adapter: one call, no retry.
	for _, e := range []error{errors.New("dial tcp: i/o timeout"), ErrNotConfigured,
		&ProviderStatusError{Provider: "whatsapp", Status: 401, Text: "bad token"}, &ProviderStatusError{Provider: "sendblue", Status: 429, Text: "slow down"},
		&ProviderStatusError{Provider: "sendblue", Status: 500, Text: "oops"}} {
		g = newRig("")
		g.r.Status = target
		g.ad.err = e
		if err := g.r.NotifyStatus(context.Background(), "user-1", ev); !errors.Is(err, e) || g.ad.calls != 1 {
			t.Fatalf("%v: err %v calls %d", e, err, g.ad.calls)
		}
	}
	// A refusal of the request (4xx) is retried once as words; if that
	// fails too the error surfaces and nothing else is tried.
	g = newRig("")
	g.r.Status = target
	g.ad.err = &ProviderStatusError{Provider: "sendblue", Status: 400, Text: "bad"}
	if err := g.r.NotifyStatus(context.Background(), "user-1", ev); err == nil || g.ad.calls != 2 {
		t.Fatalf("err %v calls %d", err, g.ad.calls)
	}

	// A channel that cannot carry the media is asked first: the words never
	// promise a photo, and no photo-only message goes out.
	g = newRig("")
	g.r.Adapters[WhatsApp] = &pickyAdapter{fakeSendAdapter: g.ad}
	now := time.Now()
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: WhatsApp, Address: me}, LastInboundAt: &now}}
	heic := ev
	heic.PhotoURL = "https://x.supabase.co/storage/v1/object/sign/task-completions/c/1.heic?token=a"
	heic.ReceiptPhotoURL = "https://x.supabase.co/storage/v1/object/sign/task-completions/r/1.heic?token=b"
	if err := g.r.NotifyStatus(context.Background(), "user-1", heic); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 1 || g.ad.sent[0].MediaURL != "" || !strings.Contains(g.ad.sent[0].Text, "Purchases: $12.40\n") || strings.Contains(g.ad.sent[0].Text, "follows") || strings.Contains(g.ad.sent[0].Text, "receipt in") {
		t.Fatalf("%+v", g.ad.sent)
	}
	// Mixed: the completion photo carried, the receipt not; and a caption
	// over the cap drops the completion photo.
	g = newRig("")
	g.r.Adapters[WhatsApp] = &pickyAdapter{fakeSendAdapter: g.ad}
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: WhatsApp, Address: me}, LastInboundAt: &now}}
	mixed := ev
	mixed.ReceiptPhotoURL = heic.ReceiptPhotoURL
	if err := g.r.NotifyStatus(context.Background(), "user-1", mixed); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 1 || g.ad.sent[0].MediaURL != ev.PhotoURL || !g.ad.sent[0].MediaExpires || strings.Contains(g.ad.sent[0].Text, "follows") {
		t.Fatalf("%+v", g.ad.sent)
	}
	g = newRig("")
	g.r.Adapters[WhatsApp] = &pickyAdapter{fakeSendAdapter: g.ad}
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: WhatsApp, Address: me}, LastInboundAt: &now}}
	long := ev
	long.TaskTitle = strings.Repeat("x", 1100)
	if err := g.r.NotifyStatus(context.Background(), "user-1", long); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 2 || g.ad.sent[0].MediaURL != "" || g.ad.sent[1].MediaURL != ev.ReceiptPhotoURL {
		t.Fatalf("%+v", g.ad.sent)
	}
	// …and carries what it can.
	g = newRig("")
	g.r.Adapters[WhatsApp] = &pickyAdapter{fakeSendAdapter: g.ad}
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: WhatsApp, Address: me}, LastInboundAt: &now}}
	if err := g.r.NotifyStatus(context.Background(), "user-1", ev); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 2 || g.ad.sent[0].MediaURL != ev.PhotoURL || !strings.Contains(g.ad.sent[0].Text, "(receipt photo follows)") || g.ad.sent[1].MediaURL != ev.ReceiptPhotoURL {
		t.Fatalf("%+v", g.ad.sent)
	}

	// WhatsApp outside the 24h window: nothing is sent, photos included.
	old := time.Now().Add(-25 * time.Hour)
	g = newRig("")
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: WhatsApp, Address: me}, LastInboundAt: &old}}
	if err := g.r.NotifyStatus(context.Background(), "user-1", ev); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 0 {
		t.Fatalf("%+v", g.ad.sent)
	}
}

// pickyAdapter carries only .jpg links, like WhatsApp.
type pickyAdapter struct{ *fakeSendAdapter }

func (p *pickyAdapter) CanCarryMedia(u string, textLen int) bool {
	return looksLikeImageURL(u) && textLen <= 1024
}
