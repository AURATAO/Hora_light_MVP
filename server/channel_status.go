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
	return statusHook(func(ctx context.Context, taskID string) (string, error) {
		var requesterID string
		err := sqlDB.QueryRowContext(ctx,
			`SELECT requester_id::text FROM public.tasks WHERE id = $1::uuid`, taskID).Scan(&requesterID)
		return requesterID, err
	}, router)
}

type statusNotifier interface {
	NotifyStatus(ctx context.Context, userID string, ev channel.StatusEvent) error
}

func statusHook(requesterOf func(context.Context, string) (string, error), n statusNotifier) func(notify.CreateNotificationInput) {
	return func(in notify.CreateNotificationInput) {
		if _, ok := channel.StatusText(statusEvent(in)); !ok || in.TaskID == "" {
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
		if err := n.NotifyStatus(ctx, in.UserID, statusEvent(in)); err != nil {
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
	}
}
