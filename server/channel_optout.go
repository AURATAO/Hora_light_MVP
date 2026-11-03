package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"hora-auth/internal/channel"
)

// Text opt-out, as the app shows it (channel/optout.go). A number that sent
// STOP, CANCEL, … is texted nothing until it sends START. The person sees
// that on their profile with the way back; the supporter on one of their
// tasks sees that the requester can't be reached by text.
//
// Read with the pgx pool from channel_identities (Go only, S-10). A database
// without the opted_out_at column (migration not applied) answers an error,
// which leaves the field out — the screens look as they did.

// TextsStopped is GET /profile's texts_stopped.
type TextsStopped struct {
	StoppedAt time.Time `json:"stopped_at"`
	// ResumeNumber is HO:RA's iMessage line (SENDBLUE_FROM_NUMBER), as
	// configured; "" when not configured.
	ResumeNumber string `json:"resume_number,omitempty"`
	// Message is the sentence to show, number included.
	Message string `json:"message"`
}

// userTextsStoppedFn is the lookup; a variable so tests can stand in.
var userTextsStoppedFn = func(ctx context.Context, userID string) (bool, time.Time, error) {
	if db == nil || userID == "" {
		return false, time.Time{}, nil
	}
	var at *time.Time
	err := db.QueryRow(ctx, `
		select max(opted_out_at) from public.channel_identities
		 where user_id = $1::uuid and channel = $2 and opted_out_at is not null
	`, userID, string(channel.IMessage)).Scan(&at)
	if err != nil {
		return false, time.Time{}, fmt.Errorf("texts stopped: %w", err)
	}
	if at == nil {
		return false, time.Time{}, nil
	}
	return true, *at, nil
}

// textsStoppedFor is the profile's view; nil when texts are on (or unknown).
func textsStoppedFor(ctx context.Context, userID string) *TextsStopped {
	stopped, at, err := userTextsStoppedFn(ctx, userID)
	if err != nil {
		log.Printf("[profile] texts stopped uid=%s: %v", userID, err)
		return nil
	}
	if !stopped {
		return nil
	}
	num := strings.TrimSpace(os.Getenv("SENDBLUE_FROM_NUMBER"))
	if n, err := channel.NormalizeE164(num); err == nil {
		num = n
	} else {
		num = ""
	}
	return &TextsStopped{StoppedAt: at, ResumeNumber: num, Message: textsStoppedMessage(num)}
}

// textsStoppedMessage is the line the app shows.
func textsStoppedMessage(e164 string) string {
	to := "HO:RA's number"
	if e164 != "" {
		to = displayPhone(e164)
	}
	return fmt.Sprintf("You've stopped texts from HO:RA. Text START to %s to turn them back on.", to)
}

// displayPhone renders a US number as (212) 555-0100; anything else as given.
func displayPhone(e164 string) string {
	if len(e164) == 12 && strings.HasPrefix(e164, "+1") {
		d := e164[2:]
		return fmt.Sprintf("(%s) %s-%s", d[:3], d[3:6], d[6:])
	}
	return e164
}

// requesterTextsStopped: whether the supporter (or ops) should be told the
// requester can't be reached by text. Unknown reads as false.
func requesterTextsStopped(ctx context.Context, requesterID string) bool {
	stopped, _, err := userTextsStoppedFn(ctx, requesterID)
	if err != nil {
		log.Printf("[getTask] requester texts stopped uid=%s: %v", requesterID, err)
		return false
	}
	return stopped
}
