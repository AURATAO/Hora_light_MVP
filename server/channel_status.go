package main

import (
	"context"
	"database/sql"
	"log"
	"time"

	"hora-auth/internal/channel"
	"hora-auth/internal/notify"
)

// chatStatusHook turns an app notification into a chat message for people
// who book by text (PLAN §7, M2). It runs on its own goroutine after the
// in-app row, email and push are already on their way (notify.Create).
//
// Only the task's requester is ever told: the chat is the requester's
// channel, and a notification addressed to the supporter of the same task
// (COMPLETED_SUPPORTER, a supporter-side TASK_REASSIGNED, …) must not reach
// a number that happens to be linked to that supporter's account.
func chatStatusHook(sqlDB *sql.DB, router *channel.Router) func(notify.CreateNotificationInput) {
	photosOf = func(ctx context.Context, taskID string) (completionPhotos, error) {
		var photo, receipt sql.NullString
		var cents sql.NullInt64
		err := sqlDB.QueryRowContext(ctx, `
			SELECT completion_photo_url, receipt_photo_url, receipt_amount_cents
			  FROM public.tasks WHERE id = $1::uuid`, taskID).Scan(&photo, &receipt, &cents)
		if err != nil {
			return completionPhotos{}, err
		}
		// Fail closed: only a signed link to one of our objects leaves the
		// platform. A photo that cannot be signed is left out — the text
		// still goes, and the email carries the photos (D-16 I-R).
		var p completionPhotos
		if photo.String != "" {
			if signed, reason := signedTaskPhotoURL(photo.String, taskPhotoLinkTTL); reason != "" {
				log.Printf("[channel] status COMPLETED task=%s: completion photo not sent: %s", taskID, reason)
			} else {
				p.Photo = signed
			}
		}
		if receipt.String != "" {
			if signed, reason := signedTaskPhotoURL(receipt.String, taskPhotoLinkTTL); reason != "" {
				log.Printf("[channel] status COMPLETED task=%s: receipt photo not sent: %s", taskID, reason)
			} else {
				p.Receipt = signed
			}
		}
		if cents.Int64 > 0 {
			p.ReceiptTotal = formatCentsUSD(int(cents.Int64))
		}
		return p, nil
	}
	return statusHook(func(ctx context.Context, taskID string) (string, error) {
		var requesterID string
		err := sqlDB.QueryRowContext(ctx,
			`SELECT requester_id::text FROM public.tasks WHERE id = $1::uuid`, taskID).Scan(&requesterID)
		return requesterID, err
	}, func(ctx context.Context, taskID, kind string) (channel.PendingApproval, error) {
		// The request the question describes, and no other: the app allows
		// exactly one pending request per (task, kind) — the partial unique
		// index in extension_requests — so this lookup is unambiguous. The
		// kind comes from the notification type (time vs budget). The
		// structured fields let the chat compose its own question.
		var a channel.PendingApproval
		var mins, cents sql.NullInt64
		var reason sql.NullString
		err := sqlDB.QueryRowContext(ctx, `
			SELECT id::text, kind, requested_minutes, requested_cents, reason
			  FROM public.extension_requests
			 WHERE task_id = $1::uuid AND kind = $2 AND status = $3`,
			taskID, kind, extensionStatusPending).Scan(&a.ID, &a.Kind, &mins, &cents, &reason)
		a.Minutes, a.Cents, a.Reason = int(mins.Int64), int(cents.Int64), reasonLabel(reason.String)
		return a, err
	}, router)
}

type statusNotifier interface {
	NotifyStatus(ctx context.Context, userID string, ev channel.StatusEvent) error
}

// chatEvent reports whether an app notification concerns the chat at all:
// one with text, or an extension resolution (which clears a pending question
// even when it says nothing).
func chatEvent(ev channel.StatusEvent) bool {
	if ev.TaskID == "" {
		return false
	}
	if ev.Type == "EXTENSION_RESOLVED" {
		return true
	}
	_, ok := channel.StatusText(ev)
	return ok
}

// approvalKind maps a request notification to the extension kind it is
// about; "" for any other type.
func approvalKind(notifType string) string {
	switch notifType {
	case "TIME_EXTENSION_REQUESTED":
		return extensionKindTime
	case "BUDGET_INCREASE_REQUESTED":
		return extensionKindBudget
	}
	return ""
}

// completionPhotos is what the chat attaches to a done message.
type completionPhotos struct {
	Photo        string // signed URL, or ""
	Receipt      string // signed URL, or ""
	ReceiptTotal string // "$12.40", or ""
}

var photosOf func(ctx context.Context, taskID string) (completionPhotos, error)

func statusHook(requesterOf func(context.Context, string) (string, error), pendingExtensionOf func(ctx context.Context, taskID, kind string) (channel.PendingApproval, error), n statusNotifier) func(notify.CreateNotificationInput) {
	return func(in notify.CreateNotificationInput) {
		ev := statusEvent(in)
		if in.Type == "TIME_EXTENSION_REQUESTED" || in.Type == "BUDGET_INCREASE_REQUESTED" {
			// Only now does StatusText have what it needs; the lookup is cheap
			// and happens only for these two types.
			ev.ApprovalMinutes = Billing.ApprovalTimeoutMinutes
		}
		if !chatEvent(ev) && ev.ExtensionID == "" && !(in.Type == "TIME_EXTENSION_REQUESTED" || in.Type == "BUDGET_INCREASE_REQUESTED") {
			return // not a chat event; skip the lookups
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		requesterID, err := requesterOf(ctx, in.TaskID)
		if err != nil {
			log.Printf("[channel] status %s task=%s: requester lookup: %v", in.Type, in.TaskID, err)
			return
		}
		if requesterID != in.UserID {
			return
		}
		if in.Type == "COMPLETED" && photosOf != nil {
			// M3.6: the completion photo and, with purchases, the receipt —
			// signed for the chat's own short TTL, never the email's.
			p, err := photosOf(ctx, in.TaskID)
			if err != nil {
				log.Printf("[channel] status COMPLETED task=%s: photo lookup: %v", in.TaskID, err)
			} else {
				ev.PhotoURL, ev.ReceiptPhotoURL, ev.ReceiptTotal = p.Photo, p.Receipt, p.ReceiptTotal
			}
		}
		if kind := approvalKind(in.Type); kind != "" {
			a, err := pendingExtensionOf(ctx, in.TaskID, kind)
			if err != nil {
				log.Printf("[channel] status %s task=%s: pending request lookup: %v", in.Type, in.TaskID, err)
				return
			}
			ev.ExtensionID = a.ID
			ev.Approval = &a
		}
		if err := n.NotifyStatus(ctx, in.UserID, ev); err != nil {
			log.Printf("[channel] status %s task=%s: %v", in.Type, in.TaskID, err)
		}
	}
}

func statusEvent(in notify.CreateNotificationInput) channel.StatusEvent {
	return channel.StatusEvent{
		Type:          in.Type,
		TaskID:        in.TaskID,
		TaskTitle:     in.TaskTitle,
		Title:         in.Title,
		Body:          in.Body,
		SupporterName: in.SupporterName,
		TotalLogged:   in.TotalLogged,
		FinalCost:     in.FinalCost,
		ExtensionID:   in.ExtensionID, // set on EXTENSION_RESOLVED: which request
	}
}
