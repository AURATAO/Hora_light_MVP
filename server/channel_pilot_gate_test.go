package main

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel"
	"hora-auth/internal/notify"
)

type stubPilotReplies struct {
	n        int
	from, to time.Time
}

func (s *stubPilotReplies) PilotRepliedSince(context.Context, channel.Channel, string, time.Time) (bool, error) {
	return false, nil
}

func (s *stubPilotReplies) GatedSenders(_ context.Context, from, to time.Time) (int, error) {
	s.from, s.to = from, to
	return s.n, nil
}

// The digest: the previous 24 hours, a count, no phone number anywhere.
func TestPilotDigest(t *testing.T) {
	prev := sendOpsEmail
	defer func() { sendOpsEmail = prev }()
	var mails []notify.EmailPayload
	sendOpsEmail = func(p notify.EmailPayload) error { mails = append(mails, p); return nil }

	end := time.Date(2026, 11, 19, 9, 0, 0, 0, nycLocation)
	st := &stubPilotReplies{n: 7}
	if err := sendPilotDigest(context.Background(), st, []string{"ops@example.com", "aura@example.com"}, end); err != nil {
		t.Fatal(err)
	}
	if !st.to.Equal(end) || !st.from.Equal(end.Add(-24*time.Hour)) {
		t.Fatalf("window %s – %s", st.from, st.to)
	}
	if len(mails) != 2 {
		t.Fatalf("%d emails", len(mails))
	}
	m := mails[0]
	if !strings.Contains(m.Subject, "7 numbers") || !strings.Contains(m.Html, "<strong>7 numbers</strong>") {
		t.Fatalf("subject %q body %q", m.Subject, m.Html)
	}
	if regexp.MustCompile(`\d{4}`).MatchString(strings.ReplaceAll(m.Subject+m.Html, "2026", "")) {
		t.Fatalf("a digit run that could be a number: %q / %q", m.Subject, m.Html)
	}
	if s, _ := pilotDigestEmail(1, end); !strings.Contains(s, "1 number texted") {
		t.Fatalf("singular: %q", s)
	}
}

func TestNextPilotDigest(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 18, h, m, 0, 0, nycLocation) }
	if got := nextPilotDigest(at(8, 59)); !got.Equal(at(9, 0)) {
		t.Fatalf("before 9: %s", got)
	}
	if got := nextPilotDigest(at(9, 0)); !got.Equal(at(9, 0).AddDate(0, 0, 1)) {
		t.Fatalf("at 9: %s", got)
	}
	if got := nextPilotDigest(at(22, 0)); !got.Equal(at(9, 0).AddDate(0, 0, 1)) {
		t.Fatalf("evening: %s", got)
	}
}
