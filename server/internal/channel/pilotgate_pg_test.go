package channel

import (
	"context"
	"testing"
	"time"
)

// The gate's two reads over the real channel_messages table.
func TestPGPilotReplies(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	const a, b, c = "+15550000001", "+15550000002", "+15550000003"
	now := time.Now()

	in := func(addr, pid string, at time.Time) {
		t.Helper()
		if _, err := s.RecordInbound(ctx, Inbound{Channel: IMessage, ProviderMessageID: pid, From: addr, Text: "hi", ReceivedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	out := func(addr, pid, body string) {
		t.Helper()
		if err := s.RecordOutbound(ctx, Outbound{Channel: IMessage, To: addr, Text: body}, pid, ""); err != nil {
			t.Fatal(err)
		}
	}

	// a: gated, told, texted twice more. b: an ordinary reply only. c:
	// told on WhatsApp, not iMessage.
	in(a, "a1", now.Add(-time.Minute))
	out(a, "a1-out", CopyPilotClosed)
	in(a, "a2", now)
	in(a, "a3", now)
	in(b, "b1", now)
	out(b, "b1-out", "Got it")
	if _, err := s.RecordInbound(ctx, Inbound{Channel: WhatsApp, ProviderMessageID: "c1", From: c, Text: "hi", ReceivedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordOutbound(ctx, Outbound{Channel: WhatsApp, To: c, Text: CopyPilotClosed}, "c1-out", ""); err != nil {
		t.Fatal(err)
	}

	since := now.Add(-PilotReplyEvery)
	for _, tc := range []struct {
		ch   Channel
		addr string
		want bool
	}{{IMessage, a, true}, {IMessage, b, false}, {IMessage, c, false}, {WhatsApp, c, true}} {
		got, err := s.PilotRepliedSince(ctx, tc.ch, tc.addr, since)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("%s %s: replied=%t, want %t", tc.ch, tc.addr, got, tc.want)
		}
	}
	// A line older than the window does not count.
	if got, err := s.PilotRepliedSince(ctx, IMessage, a, now.Add(time.Hour)); err != nil || got {
		t.Fatalf("future since: %t %v", got, err)
	}

	// Two gated numbers (a three times, c once); b is not one.
	n, err := s.GatedSenders(ctx, now.Add(-24*time.Hour), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("gated senders = %d, want 2", n)
	}
	if n, err := s.GatedSenders(ctx, now.Add(-48*time.Hour), now.Add(-24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("earlier day: %d %v", n, err)
	}
}
