package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"hora-auth/internal/channel"
	"hora-auth/internal/notify"
)

// SUPPORTER_APPLY_URL (supporter interest, internal/channel/supporter.go):
// set → used; unset or not https → the default form.
func TestSupporterApplyURLFromEnv(t *testing.T) {
	for env, want := range map[string]string{
		"":                            channel.DefaultSupporterApplyURL,
		"https://horaapp.co/join":     "https://horaapp.co/join",
		"  https://horaapp.co/join  ": "https://horaapp.co/join",
		"http://horaapp.co/join":      channel.DefaultSupporterApplyURL,
		"javascript:alert(1)":         channel.DefaultSupporterApplyURL,
	} {
		t.Setenv("SUPPORTER_APPLY_URL", env)
		if got := supporterApplyURLFromEnv(); got != want {
			t.Errorf("%q: %q, want %q", env, got, want)
		}
	}
}

// The supporter-interest notice: email only (non-urgent), the last four
// digits, no message text, says the bot answered.
func TestOpsNoticeSupporterInterest(t *testing.T) {
	prev := sendOpsEmail
	defer func() { sendOpsEmail = prev }()
	var subject, body string
	sendOpsEmail = func(p notify.EmailPayload) error { subject, body = p.Subject, p.Html; return nil }
	t.Setenv("CHANNEL_OPS_EMAILS", "ops@example.com")
	t.Setenv("CHANNEL_OPS_NUMBERS", "+1 (555) 010-0199")
	sms := &stubAdapter{}
	n := newOpsNotifier(nil, sms, &stubToucher{})
	n.lookup = func(context.Context, string) (string, error) { return "+15550100777", nil }
	if err := n.NotifyOps(context.Background(), channel.HandoffNotice{IdentityID: "i", Channel: channel.IMessage, PhoneHint: "•••• 0777",
		Reason: channel.HandoffSupporterInterest, Thread: []channel.ThreadMessage{}}); err != nil {
		t.Fatal(err)
	}
	if subject != "[HO:RA] Someone asked about becoming a supporter — imessage •••• 0777" || len(sms.sent) != 0 {
		t.Fatalf("subject %q texts %d", subject, len(sms.sent))
	}
	if !strings.Contains(body, "application link") || strings.Contains(body, "URGENT") || strings.Contains(body, "+1555") {
		t.Fatalf("body %s", body)
	}
}

// The status lookup reads profiles as GET /profile derives it (Postgres).
func TestChatSupportersStatus(t *testing.T) {
	setupDispatchDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `ALTER TABLE public.profiles ADD COLUMN IF NOT EXISTS supporter_applied_at timestamptz`); err != nil {
		t.Fatal(err)
	}
	mig, err := os.ReadFile("../supabase/migrations/20260722103000_profiles_supporter_rejected_at.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, string(mig)); err != nil {
		t.Fatalf("migration: %v", err)
	}
	ids := map[string]string{
		"approved": "11111111-1111-1111-1111-111111111111",
		"applied":  "22222222-2222-2222-2222-222222222222",
		"rejected": "33333333-3333-3333-3333-333333333333",
		"none":     "44444444-4444-4444-4444-444444444444",
	}
	for st, id := range ids {
		if _, err := db.Exec(ctx, `insert into public.profiles (id, email, name, is_verified_supporter, supporter_applied_at, supporter_rejected_at)
			values ($1::uuid, $2, 'x', $3, case when $4 then now() end, case when $5 then now() end)`,
			id, st+"@example.test", st == "approved", st == "applied" || st == "rejected", st == "rejected"); err != nil {
			t.Fatalf("seed %s: %v", st, err)
		}
	}
	s := &chatSupporters{sqlDB: sqldb}
	for want, id := range ids {
		if got, err := s.SupporterStatus(ctx, id); err != nil || got != want {
			t.Errorf("%s: %q %v", want, got, err)
		}
	}
	if got, err := s.SupporterStatus(ctx, "55555555-5555-5555-5555-555555555555"); err != nil || got != "none" {
		t.Errorf("no profile: %q %v", got, err)
	}
}
