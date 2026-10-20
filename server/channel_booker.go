package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"hora-auth/internal/channel"
	"hora-auth/internal/channel/llm"
)

// chatBooker is channel.Booker: the messaging channel's only door into task
// pricing and posting. It adds no rules of its own.
//
//   - Quote is quoteTask — the function behind POST /tasks/estimate (S-05).
//   - Post runs the real createTask handler in-process, with the linked
//     user's identity set exactly as dualAuth would set it (S-11: the user id
//     comes from the linked identity row, never from message text). Every
//     gate — outstanding balance, no card, decline, 3DS, PAYMENTS_ENFORCED —
//     is therefore the production code path, not a copy of it.
type chatBooker struct {
	sqlDB *sql.DB
}

// defaultChatMinutes is what createTask itself falls back to when no
// estimate is given; the quote uses the same number so they agree.
const defaultChatMinutes = 30

func draftMinutes(d llm.TaskDraft) int {
	if d.DurationMinutes > 0 {
		return d.DurationMinutes
	}
	return defaultChatMinutes
}

func (b *chatBooker) Quote(_ context.Context, d llm.TaskDraft) (int, int, error) {
	minutes := draftMinutes(d)
	q := quoteTask(d.Category, minutes, 0,
		resolveRateCentsPerMin(estimateStartAt(!d.Scheduled, d.ScheduledTime)))
	return q.TotalCents, minutes, nil
}

// draftToCreateInput maps a chat draft onto the POST /tasks body the app sends.
func draftToCreateInput(d llm.TaskDraft) createTaskInput {
	loc := strings.TrimSpace(d.Location1)
	if l2 := strings.TrimSpace(d.Location2); l2 != "" {
		// The same " | " encoding both clients use for a two-stop task.
		loc = loc + " | " + l2
	}
	in := createTaskInput{
		Title:            d.Title,
		Description:      d.Description,
		Category:         d.Category,
		LocationText:     loc,
		EstimatedMinutes: draftMinutes(d),
		IsImmediate:      !d.Scheduled,
		// Closest existing attribution value. A dedicated "message" value
		// needs a CHECK change on tasks, which waits for the review freeze to
		// end; channel_messages.task_id already identifies these tasks.
		CreatedVia: "ai_parse",
	}
	if d.Scheduled {
		in.ScheduledAt = d.ScheduledTime
	}
	return in
}

func (b *chatBooker) Post(ctx context.Context, userID string, d llm.TaskDraft) channel.PostResult {
	var email string
	if err := b.sqlDB.QueryRowContext(ctx,
		`select coalesce(email,'') from public.users where id = $1::uuid`, userID).Scan(&email); err != nil {
		log.Printf("[channel-booker] post uid=%s: %v", userID, fmt.Errorf("load user: %w", err))
		return channel.PostResult{Outcome: channel.PostFailed}
	}
	if email == "" {
		log.Printf("[channel-booker] post uid=%s: user has no email", userID)
		return channel.PostResult{Outcome: channel.PostFailed}
	}
	body, err := json.Marshal(draftToCreateInput(d))
	if err != nil {
		log.Printf("[channel-booker] post uid=%s: %v", userID, fmt.Errorf("encode task: %w", err))
		return channel.PostResult{Outcome: channel.PostFailed}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/tasks", bytes.NewReader(body)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("uid", userID)
	c.Set("email", email)
	createTask(c)

	return postResultFrom(w.Code, w.Body.Bytes())
}

// postResultFrom reads createTask's response. Exported shape, so it is tested
// directly against the bodies createTask writes.
func postResultFrom(status int, raw []byte) channel.PostResult {
	var r struct {
		ID      string `json:"id"`
		Error   string `json:"error"`
		Message string `json:"message"`
		TaskID  string `json:"task_id"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		// createTask always answers JSON; anything else is a server fault.
		log.Printf("[channel-booker] createTask status=%d: %v", status, fmt.Errorf("decode response: %w", err))
		return channel.PostResult{Outcome: channel.PostFailed}
	}
	switch {
	case status == http.StatusCreated && r.ID != "":
		return channel.PostResult{Outcome: channel.PostBooked, TaskID: r.ID}
	case status == http.StatusPaymentRequired && r.Error == "payment_method_required":
		return channel.PostResult{Outcome: channel.PostNeedsCard, Message: r.Message}
	case status == http.StatusPaymentRequired && r.Error == "payment_authentication_required":
		return channel.PostResult{Outcome: channel.PostNeeds3DS, TaskID: r.TaskID, Message: r.Message}
	case status == http.StatusPaymentRequired:
		return channel.PostResult{Outcome: channel.PostDeclined, Message: r.Message}
	case status == http.StatusForbidden && r.Error == "outstanding_balance":
		return channel.PostResult{Outcome: channel.PostOwesBalance, Message: r.Message}
	default:
		return channel.PostResult{Outcome: channel.PostFailed, Message: ""}
	}
}

// ApplyChatProfile fills the profile from what the person said in the chat:
// the first name, and the time they replied YES to a summary stating the
// Terms. Blanks only — an existing name or consent is never overwritten.
func (b *chatBooker) ApplyChatProfile(ctx context.Context, userID, name string, termsAt *time.Time) error {
	var email string
	if err := b.sqlDB.QueryRowContext(ctx,
		`select coalesce(email,'') from public.users where id = $1::uuid`, userID).Scan(&email); err != nil {
		return fmt.Errorf("apply chat profile: user: %w", err)
	}
	if email == "" {
		return errors.New("apply chat profile: user has no email")
	}
	var terms any
	if termsAt != nil {
		terms = *termsAt
	}
	_, err := b.sqlDB.ExecContext(ctx, `
		INSERT INTO public.profiles (id, email, name, phone, city, avatar_url, bio, terms_accepted_at, created_at, updated_at)
		VALUES ($4::uuid, $1, $2, '', '', '', '', $3, now(), now())
		ON CONFLICT (email) DO UPDATE SET
		  name = CASE WHEN coalesce(profiles.name, '') = '' THEN EXCLUDED.name ELSE profiles.name END,
		  terms_accepted_at = COALESCE(profiles.terms_accepted_at, EXCLUDED.terms_accepted_at),
		  updated_at = now()
	`, email, strings.TrimSpace(name), terms, userID)
	if err != nil {
		return fmt.Errorf("apply chat profile: %w", err)
	}
	return nil
}
