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
	model *llm.OpenAICompat
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

func (b *chatBooker) Quote(_ context.Context, d llm.TaskDraft, budgetCents int) (int, int, error) {
	minutes := draftMinutes(d)
	// Same quote as /tasks/estimate: base + time + the shopping budget the
	// hold must also cover.
	q := quoteTask(d.Category, minutes, budgetCents,
		resolveRateCentsPerMin(estimateStartAt(!d.Scheduled, d.ScheduledTime)))
	return q.TotalCents, minutes, nil
}

// draftToCreateInput maps a chat draft onto the POST /tasks body the app sends.
// The two consents the app collects on its form — the shopping budget and
// the auto-extend checkbox — are sent explicitly from what the chat gathered,
// never left to the server default (M3.5).
func draftToCreateInput(d llm.TaskDraft, opts channel.PostOptions) createTaskInput {
	autoExtend := opts.AutoExtend
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
		CreatedVia:        "ai_parse",
		PrepayAmountCents: opts.BudgetCents,
		AutoExtendConsent: &autoExtend,
	}
	if d.Scheduled {
		in.ScheduledAt = d.ScheduledTime
	}
	return in
}

func (b *chatBooker) Post(ctx context.Context, userID string, d llm.TaskDraft, opts channel.PostOptions) channel.PostResult {
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
	body, err := json.Marshal(draftToCreateInput(d, opts))
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

// ResolveExtension answers a supporter's request as the chat's requester
// (M3). The decision runs through decideExtension — the same code the app's
// Approve/Decline buttons call — so the requester-only check, expiry-first,
// double-answer safety, ceiling move, audit row and announcements are
// production's. The chat only carries the YES/NO.
func (b *chatBooker) ResolveExtension(ctx context.Context, userID, extensionID string, approve bool) channel.ExtensionResult {
	var taskID string
	err := b.sqlDB.QueryRowContext(ctx,
		`select task_id::text from public.extension_requests where id = $1::uuid`, extensionID).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return channel.ExtensionResult{Outcome: channel.ExtNotFound}
	}
	if err != nil {
		log.Printf("[channel-booker] resolve ext=%s: %v", extensionID, fmt.Errorf("load request: %w", err))
		return channel.ExtensionResult{Outcome: channel.ExtFailed}
	}
	decision := extensionStatusDenied
	if approve {
		decision = extensionStatusApproved
	}
	res, err := decideExtension(ctx, taskID, extensionID, userID, decision)
	switch {
	case errors.Is(err, errExtensionTaskNotFound), errors.Is(err, errExtensionNotFound), errors.Is(err, errExtensionNotRequester):
		// Not this person's to answer, or gone. The chat gets one neutral
		// line; the reason stays in the log (S-33).
		log.Printf("[channel-booker] resolve ext=%s uid=%s: %v", extensionID, userID, err)
		return channel.ExtensionResult{Outcome: channel.ExtNotFound, TaskID: taskID}
	case err != nil:
		log.Printf("[channel-booker] resolve ext=%s uid=%s: %v", extensionID, userID, err)
		return channel.ExtensionResult{Outcome: channel.ExtFailed, TaskID: taskID}
	}
	if res.Already != nil {
		return channel.ExtensionResult{Outcome: channel.ExtAlready, Status: res.Already.Status, Ask: b.extensionAsk(ctx, *res.Already), TaskID: taskID}
	}
	return channel.ExtensionResult{Outcome: channel.ExtResolved, Status: res.Resolved.Status, Ask: b.extensionAsk(ctx, res.Resolved), TaskID: taskID}
}

// extensionAsk is the request in words, the same way the app's
// announcements phrase it: "15 more minutes on “Laundry pickup”".
func (b *chatBooker) extensionAsk(ctx context.Context, e ExtensionRequest) string {
	ask := fmt.Sprintf("%d more minutes", derefInt(e.RequestedMinutes))
	if e.Kind == extensionKindBudget {
		ask = formatCentsUSD(derefInt(e.RequestedCents)) + " more budget"
	}
	var title string
	if err := b.sqlDB.QueryRowContext(ctx,
		`select coalesce(title,'') from public.tasks where id = $1::uuid`, e.TaskID).Scan(&title); err != nil {
		log.Printf("[channel-booker] ask ext=%s: %v", e.ID, fmt.Errorf("load title: %w", err))
		return ask
	}
	if title == "" {
		return ask
	}
	return ask + " on \u201c" + title + "\u201d"
}

// NeedsPurchase is the model tiebreak for budget.go: asked only when the
// rule saw no purchase word. The model's answer never sets an amount.
func (b *chatBooker) NeedsPurchase(ctx context.Context, text string) (bool, error) {
	return b.model.NeedsPurchase(ctx, text)
}

// ExtensionPending: the request still awaits the requester's answer, and
// is theirs to answer.
func (b *chatBooker) ExtensionPending(ctx context.Context, userID, extensionID string) (bool, error) {
	var pending bool
	// Expiry is lazy in this codebase (a request reads 'pending' until the
	// supporter's screen or the sweep expires it), so the clock is checked
	// here too; and the task must still be open with that supporter.
	err := b.sqlDB.QueryRowContext(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM public.extension_requests e
		    JOIN public.tasks t ON t.id = e.task_id
		   WHERE e.id = $1::uuid AND e.status = $2 AND t.requester_id = $3::uuid
		     AND t.status = 'open' AND t.assigned_to_id = e.supporter_id
		     AND e.created_at > now() - make_interval(mins => $4)
		     AND NOT EXISTS (SELECT 1 FROM public.user_blocks b
		                      WHERE (b.blocker_id = t.requester_id AND b.blocked_id = e.supporter_id)
		                         OR (b.blocker_id = e.supporter_id AND b.blocked_id = t.requester_id)))`,
		extensionID, extensionStatusPending, userID, Billing.ApprovalTimeoutMinutes).Scan(&pending)
	if err != nil {
		return false, fmt.Errorf("extension pending: %w", err)
	}
	return pending, nil
}

// OpenTasks lists the requester's posted tasks that can still be cancelled
// (status open, newest first) — the same status cancelTask accepts.
func (b *chatBooker) OpenTasks(ctx context.Context, userID string) ([]channel.TaskRef, error) {
	rows, err := b.sqlDB.QueryContext(ctx, `
		SELECT id::text, COALESCE(title, '')
		  FROM public.tasks
		 WHERE requester_id = $1::uuid AND status = 'open'
		 ORDER BY created_at DESC
		 LIMIT 9`, userID)
	if err != nil {
		return nil, fmt.Errorf("open tasks: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			log.Printf("[channel-booker] open tasks: close rows: %v", cerr)
		}
	}()
	var out []channel.TaskRef
	for rows.Next() {
		var t channel.TaskRef
		if err := rows.Scan(&t.ID, &t.Title); err != nil {
			return nil, fmt.Errorf("open tasks: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CancelQuote prices cancelling now with the app's own preview
// (cancellationPreview — parity-tested against the cancel handler).
func (b *chatBooker) CancelQuote(ctx context.Context, userID, taskID string) (channel.CancelQuote, error) {
	var title, status string
	var assigned *string
	err := b.sqlDB.QueryRowContext(ctx, `
		SELECT COALESCE(title, ''), status, assigned_to_id::text
		  FROM public.tasks WHERE id = $1::uuid AND requester_id = $2::uuid`, taskID, userID).Scan(&title, &status, &assigned)
	if err != nil {
		return channel.CancelQuote{}, fmt.Errorf("cancel quote: %w", err)
	}
	if status != "open" {
		return channel.CancelQuote{}, fmt.Errorf("cancel quote: task is %s", status)
	}
	// cancellationPreview reads a failed minutes query as zero minutes; a
	// quote the person says YES to must not rest on that (review 10/24 #9).
	if _, err := totalClosedMinutes(ctx, taskID); err != nil {
		return channel.CancelQuote{}, fmt.Errorf("cancel quote: minutes: %w", err)
	}
	var sessionOpen bool
	if err := b.sqlDB.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM public.worklogs WHERE task_id = $1::uuid AND end_at IS NULL)`, taskID).Scan(&sessionOpen); err != nil {
		return channel.CancelQuote{}, fmt.Errorf("cancel quote: session: %w", err)
	}
	p := cancellationPreview(ctx, taskID, assigned, 0)
	if p == nil {
		return channel.CancelQuote{}, fmt.Errorf("cancel quote: no preview")
	}
	// Moving: the charge can change before the cancel lands — a session is
	// running, or the free window closes within cancelQuoteMargin.
	closing := p.WithinGrace && p.GraceEndsAt != nil && time.Until(*p.GraceEndsAt) < cancelQuoteMargin
	return channel.CancelQuote{Title: title, ChargeCents: p.ChargeCents, BaseFeeCents: p.BaseFeeCents,
		Committed: p.Committed, WithinGrace: p.WithinGrace, Moving: sessionOpen || closing}, nil
}

// Cancel runs the app's own cancel handler as the requester. The reason code
// is "other", which relays nothing to the supporter (cancel_reasons.go).
func (b *chatBooker) Cancel(ctx context.Context, userID, taskID string) channel.CancelResult {
	body, err := json.Marshal(map[string]string{"reason": "Cancelled by text message", "reason_code": "other"})
	if err != nil {
		log.Printf("[channel-booker] cancel task=%s: %v", taskID, err)
		return channel.CancelResult{}
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/cancel", bytes.NewReader(body)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: taskID}}
	c.Set("uid", userID)
	cancelTask(c)
	return cancelResultFrom(w.Code, w.Body.Bytes())
}

// cancelQuoteMargin: a free window closing sooner than this is a moving
// charge — the YES could arrive after it closed.
const cancelQuoteMargin = 2 * time.Minute

// cancelResultFrom reads cancelTask's response. Gone only for the two
// answers that mean the task is past cancelling (already closed, removed) or
// not found; any other refusal is a failure, and a 200 whose bill cannot be
// read is a cancel with an unknown charge — never "no charge".
func cancelResultFrom(status int, raw []byte) channel.CancelResult {
	var r struct {
		BillCents *int   `json:"bill_cents"`
		Error     string `json:"error"`
	}
	decodeErr := json.Unmarshal(raw, &r)
	switch {
	case status == http.StatusOK:
		if decodeErr != nil || r.BillCents == nil {
			log.Printf("[channel-booker] cancel status=200: bill not readable")
			return channel.CancelResult{OK: true, BillUnknown: true}
		}
		return channel.CancelResult{OK: true, BillCents: *r.BillCents}
	case status == http.StatusNotFound,
		status == http.StatusBadRequest && (r.Error == "task already closed" || r.Error == "task_removed"):
		return channel.CancelResult{Gone: true}
	}
	log.Printf("[channel-booker] cancel failed status=%d", status)
	return channel.CancelResult{}
}
