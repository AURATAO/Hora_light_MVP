package channel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// Status updates (PLAN §7, M2): what happens to a booked task — accepted,
// arrived, started, done, cancelled — is told in the thread the person booked
// from, in addition to the email and push the app already sends (business
// rules unchanged: the chat is one more channel, never a replacement).
//
// The source is the app's own notification call (notify.Create), so the chat
// says exactly what the email says, at the same moment, from the same event.

// StatusEvent is the part of an app notification the chat needs.
type StatusEvent struct {
	Type          string // notify type: ORDER_ACCEPTED, CLOCK_IN, …
	TaskID        string
	TaskTitle     string
	Title         string
	Body          string
	SupporterName string
	TotalLogged   string
	FinalCost     string
	// Approval requests (M3): the request the YES/NO will answer and the
	// auto-decline window the app applies. The question is composed here
	// from these structured fields — never from the app's notification
	// body, which carries the supporter's free-text reason unquoted.
	ExtensionID     string
	ApprovalMinutes int
	Approval        *PendingApproval
}

// PendingApproval is the structured request behind an approval question.
type PendingApproval struct {
	ID      string
	Kind    string // "time" | "budget"
	Minutes int    // kind=time
	Cents   int    // kind=budget
	Reason  string // the supporter's words, shown quoted and labelled as theirs
}

// StatusText is the chat message for an event, and false for events the chat
// does not carry. Left out on purpose, until the chat can act on them:
//   - anything that asks the person to do something in the app (approve more
//     time or budget, settle a balance) — M3 adds YES/NO for those by text;
//     until then email and push carry them, and saying "do it in the app" to
//     someone with no app would be a dead end;
//   - the receipt (email has the itemized one; COMPLETED states the total);
//   - supporter-side events.
func StatusText(ev StatusEvent) (string, bool) {
	task := quoted(ev.TaskTitle)
	who := firstNonEmpty(DisplayName(FirstName(ev.SupporterName)), "Your supporter")
	switch ev.Type {
	case "ORDER_ACCEPTED":
		return fmt.Sprintf("%s accepted %s. I'll text you when they arrive.", who, task), true
	case "ON_MY_WAY":
		// Chat-only. App users watch the live map from this moment; a chat
		// customer has no map, so this line is the substitute.
		return fmt.Sprintf(CopyOnMyWay, who, task), true
	case "SUPPORTER_ARRIVED":
		return fmt.Sprintf("%s has arrived for %s.", who, task), true
	case "CLOCK_IN":
		return fmt.Sprintf("%s started on %s. The clock is running.", who, task), true
	case "CLOCK_OUT":
		if ev.TotalLogged != "" {
			return fmt.Sprintf("%s clocked out on %s. Time so far: %s.", who, task, ev.TotalLogged), true
		}
		return fmt.Sprintf("%s clocked out on %s.", who, task), true
	case "COMPLETED":
		var b strings.Builder
		fmt.Fprintf(&b, "Done: %s.", task)
		if ev.TotalLogged != "" {
			fmt.Fprintf(&b, "\nTime: %s", ev.TotalLogged)
		}
		if ev.FinalCost != "" {
			fmt.Fprintf(&b, "\nTotal: %s", ev.FinalCost)
		}
		b.WriteString("\n\nYour receipt is on its way by email. Need something else? Just text me.")
		return b.String(), true
	case evTimeRequested, evBudgetRequested:
		return approvalText(ev)
	case evResolved:
		// The person's own answer was confirmed when they gave it (here or
		// in the app); only the timeout is news. Deterministic on the app's
		// own title.
		if strings.HasPrefix(ev.Title, "A request timed out") && strings.TrimSpace(ev.Body) != "" {
			return fmt.Sprintf(CopyApprovalTimedOut, strings.TrimSpace(ev.Body)), true
		}
		return "", false
	case "CANCELLED", "TASK_REASSIGNED", "TASK_REMOVED", "TIME_CAP_WARNING":
		// The app's own requester copy is already plain and self-contained.
		if strings.TrimSpace(ev.Body) == "" {
			return "", false
		}
		return strings.TrimSpace(ev.Body), true
	}
	return "", false
}

func quoted(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "your task"
	}
	return "“" + title + "”"
}

// StatusTarget is where a user's status updates go.
type StatusTarget struct {
	Identity      Identity
	LastInboundAt *time.Time
}

// StatusStore finds the thread for a user's task.
type StatusStore interface {
	// StatusTargetFor returns the number to tell about taskID: the one the
	// task was booked from when there is one, otherwise the number the
	// person wrote from most recently. ok=false when the user has no linked
	// number (an app-only user — nearly everyone today).
	StatusTargetFor(ctx context.Context, userID, taskID string) (StatusTarget, bool, error)
}

func (s *PGStore) StatusTargetFor(ctx context.Context, userID, taskID string) (StatusTarget, bool, error) {
	var t StatusTarget
	var last sql.NullTime
	err := s.DB.QueryRowContext(ctx, `
		SELECT i.id, i.channel, i.address, c.last_inbound_at
		FROM channel_identities i
		LEFT JOIN channel_conversations c ON c.identity_id = i.id
		WHERE i.user_id = $1::uuid
		ORDER BY EXISTS (
		           SELECT 1 FROM channel_messages m
		           WHERE m.channel = i.channel AND m.address = i.address
		             AND m.task_id = NULLIF($2, '')::uuid
		         ) DESC,
		         c.last_inbound_at DESC NULLS LAST
		LIMIT 1
	`, userID, taskID).Scan(&t.Identity.ID, &t.Identity.Channel, &t.Identity.Address, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return t, false, nil
	}
	if err != nil {
		return t, false, fmt.Errorf("status target: %w", err)
	}
	t.Identity.UserID = userID
	if last.Valid {
		lt := last.Time
		t.LastInboundAt = &lt
	}
	return t, true, nil
}

// WhatsAppWindow is how long after the person's last message WhatsApp allows
// a free-form message. Past it only approved templates may be sent (M4).
const WhatsAppWindow = 24 * time.Hour

// NotifyStatus tells userID about ev in their chat, if they have one. It is
// best-effort: the email and push for the same event have already gone, so a
// failure here is logged and returned, never retried into a duplicate.
func (r *Router) NotifyStatus(ctx context.Context, userID string, ev StatusEvent) error {
	if userID == "" || r.Status == nil {
		return nil
	}
	text, ok := StatusText(ev)
	// A resolution clears a pending question even when it sends no text.
	if !ok && ev.Type != evResolved {
		return nil
	}
	t, found, err := r.Status.StatusTargetFor(ctx, userID, ev.TaskID)
	if err != nil {
		return fmt.Errorf("NotifyStatus: %w", err)
	}
	if !found {
		return nil // an app-only user: nothing to send
	}
	if ev.Type == evResolved {
		if err := r.clearApproval(ctx, t.Identity.ID); err != nil {
			return fmt.Errorf("NotifyStatus: %w", err)
		}
		if !ok {
			return nil
		}
	}
	if t.Identity.Channel == WhatsApp {
		if t.LastInboundAt == nil || r.now().Sub(*t.LastInboundAt) > WhatsAppWindow {
			// Needs an approved template (M4). Email and push still carried it.
			log.Printf("[channel] status %s task=%s not sent: outside WhatsApp 24h window", ev.Type, ev.TaskID)
			return nil
		}
	}
	if err := r.Send(ctx, Outbound{Channel: t.Identity.Channel, To: t.Identity.Address, Text: text}, ev.TaskID); err != nil {
		return fmt.Errorf("NotifyStatus: %w", err)
	}
	if isApprovalRequest(ev.Type) && ev.ExtensionID != "" {
		// Armed only after the question was actually delivered.
		if err := r.askApproval(ctx, t.Identity.ID, ev.ExtensionID); err != nil {
			return fmt.Errorf("NotifyStatus: %w", err)
		}
	}
	return nil
}
