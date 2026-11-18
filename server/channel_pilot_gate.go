package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"hora-auth/internal/channel"
	"hora-auth/internal/notify"
)

// The pilot gate's daily digest (internal/channel/pilotgate.go): once a day
// ops get how many numbers texted while gated in the last 24 hours. A count
// only — no number, no hint, no message text (S-12). Sent to the same list
// as hand-off emails (CHANNEL_OPS_EMAILS, else the ops-admin allowlist).

// pilotDigestHour: when the digest goes out, New York time.
const pilotDigestHour = 9

// nextPilotDigest is the first pilotDigestHour:00 New York time after now.
func nextPilotDigest(now time.Time) time.Time {
	loc := nycLocation
	n := now.In(loc)
	at := time.Date(n.Year(), n.Month(), n.Day(), pilotDigestHour, 0, 0, 0, loc)
	if !at.After(n) {
		at = time.Date(n.Year(), n.Month(), n.Day()+1, pilotDigestHour, 0, 0, 0, loc)
	}
	return at
}

// pilotDigestEmail is the digest's subject and body for count senders in the
// 24 hours ending at end.
func pilotDigestEmail(count int, end time.Time) (subject, body string) {
	who := fmt.Sprintf("%d numbers", count)
	if count == 1 {
		who = "1 number"
	}
	subject = fmt.Sprintf("[HO:RA] Pilot gate — %s texted before launch (24h)", who)
	day := end.In(nycLocation).Format("Mon Jan 2, 3:04 PM MST")
	body = fmt.Sprintf(`<p>In the 24 hours to %s, <strong>%s</strong> not on the pilot allowlist texted the HO:RA line.</p>
<p>Each got the "we open in December" reply at most once a day, and nothing was booked or drafted. No phone numbers are in this email.</p>
<p>The gate is on while CHANNEL_PILOT_GATE=1.</p>`, day, who)
	return subject, body
}

// sendPilotDigest counts the 24 hours ending at end and emails ops.
func sendPilotDigest(ctx context.Context, store channel.PilotReplyStore, emails []string, end time.Time) error {
	n, err := store.GatedSenders(ctx, end.Add(-24*time.Hour), end)
	if err != nil {
		return err
	}
	subject, body := pilotDigestEmail(n, end)
	sent := 0
	for i, to := range emails {
		if err := sendOpsEmail(notify.EmailPayload{To: to, Subject: subject, Html: body}); err != nil {
			log.Printf("[channel] pilot digest email #%d failed: %v", i+1, err)
			continue
		}
		sent++
	}
	log.Printf("[channel] pilot digest: %d gated numbers, emailed %d of %d", n, sent, len(emails))
	return nil
}

// watchPilotGateDigest sends the digest every day at pilotDigestHour New
// York time until ctx ends. A restart waits for the next one (never twice).
func watchPilotGateDigest(ctx context.Context, store channel.PilotReplyStore, emails []string) {
	if len(emails) == 0 {
		log.Printf("[channel] WARNING: pilot gate on but no ops emails — no daily digest")
		return
	}
	for {
		at := nextPilotDigest(time.Now())
		t := time.NewTimer(time.Until(at))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, time.Minute)
		if err := sendPilotDigest(c, store, emails, at); err != nil {
			log.Printf("[channel] pilot digest: %v", err)
		}
		cancel()
	}
}
