package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"hora-auth/internal/channel"
)

// Chat relay, server side (PLAN §6 steps 5–7; M3.2). The channel package
// decides *whether* a requester's text is for a supporter (relay.go); this
// file is the TalkJS end of it, both directions:
//
//   - requester → supporter: talkjsRelay posts into the task's conversation
//     as the requester (TalkJS REST, server-signed — the requester has no
//     client session). The conversation is created on the way if nobody has
//     opened it yet, with the same id, subject and custom.taskId both
//     clients build (TaskChatBox.jsx, mobile chat.tsx), so a supporter who
//     opens the chat afterwards lands in the same room.
//   - supporter → requester: the TalkJS webhook (talkjs_webhook.go) hands
//     every message.sent to chatRelayHook, which sends it to the requester's
//     number when they book by text (Router.RelayToChat). Nil in production
//     until the messaging channel is enabled: the webhook then does what it
//     did before.
//
// Safety: a blocked pair relays nothing either way (user_blocks, the same
// check the pushes use). Relaying moves no money and changes no task state.

// chatRelayHook is set by RegisterChannelWebhooks (channel_webhooks.go).
// Called from the TalkJS webhook off the request path.
var chatRelayHook func(ctx context.Context, requesterID, taskID, supporterName, taskTitle, text, mediaURL string)

// talkjsRelay implements channel.TaskChat.
type talkjsRelay struct {
	sqlDB *sql.DB
}

// ActiveTasks: the requester's open tasks that have a supporter, newest
// first — the ones a message could be for.
func (t *talkjsRelay) ActiveTasks(ctx context.Context, userID string) ([]channel.ActiveTask, error) {
	rows, err := t.sqlDB.QueryContext(ctx, `
		SELECT t.id::text, COALESCE(t.title, ''), COALESCE(p.name, '')
		  FROM public.tasks t
		  LEFT JOIN public.profiles p ON p.id = t.assigned_to_id
		 WHERE t.requester_id = $1::uuid
		   AND t.status = 'open'
		   AND t.assigned_to_id IS NOT NULL
		 ORDER BY t.created_at DESC
		 LIMIT 9`, userID)
	if err != nil {
		return nil, fmt.Errorf("active tasks: %w", err)
	}
	defer rows.Close()
	var out []channel.ActiveTask
	for rows.Next() {
		var a channel.ActiveTask
		if err := rows.Scan(&a.ID, &a.Title, &a.SupporterName); err != nil {
			return nil, fmt.Errorf("active tasks: %w", err)
		}
		a.SupporterName = firstNameOf(a.SupporterName)
		out = append(out, a)
	}
	return out, rows.Err()
}

// PostAsRequester puts text into the task's chat as the requester.
func (t *talkjsRelay) PostAsRequester(ctx context.Context, taskID, userID, text string) error {
	if _, _, ok := talkjsConfigured(); !ok {
		return fmt.Errorf("post as requester: talkjs not configured")
	}
	var requesterID, supporterID sql.NullString
	var requesterEmail, supporterEmail sql.NullString
	var title sql.NullString
	err := t.sqlDB.QueryRowContext(ctx, `
		SELECT t.requester_id::text, t.assigned_to_id::text, t.title,
		       ru.email, su.email
		  FROM public.tasks t
		  LEFT JOIN public.users ru ON ru.id = t.requester_id
		  LEFT JOIN public.users su ON su.id = t.assigned_to_id
		 WHERE t.id = $1::uuid`, taskID).Scan(&requesterID, &supporterID, &title, &requesterEmail, &supporterEmail)
	if err != nil {
		return fmt.Errorf("post as requester: %w", err)
	}
	// The task must be the caller's: the id came from ActiveTasks or from a
	// channel_messages row, but the sender is re-checked here, not trusted.
	if requesterID.String != userID {
		return fmt.Errorf("post as requester: task %s is not the sender's", taskID)
	}
	if supporterID.String == "" || supporterEmail.String == "" || requesterEmail.String == "" {
		return fmt.Errorf("post as requester: task %s has no chat pair", taskID)
	}
	if blocked, err := pairBlocked(ctx, t.sqlDB, requesterID.String, supporterID.String); err != nil {
		return fmt.Errorf("post as requester: block check: %w", err)
	} else if blocked {
		return fmt.Errorf("post as requester: pair blocked")
	}

	convPath := "/conversations/" + url.PathEscape(talkjsConversationID(taskID))
	// PUT is create-or-update; participants given here are added with
	// ReadWrite. Subject/custom mirror what the clients set.
	status, raw, err := talkjsRequest(ctx, http.MethodPut, convPath, map[string]any{
		"participants": []string{requesterEmail.String, supporterEmail.String},
		"subject":      relayOr(strings.TrimSpace(title.String), "Task"),
		"custom":       map[string]string{"taskId": taskID},
	})
	if err != nil {
		return fmt.Errorf("post as requester: ensure conversation: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("post as requester: talkjs PUT conversation returned %d: %s", status, truncate(string(raw), 200))
	}
	status, raw, err = talkjsRequest(ctx, http.MethodPost, convPath+"/messages", []map[string]any{{
		"text":   text,
		"sender": requesterEmail.String,
		"type":   "UserMessage",
	}})
	if err != nil {
		return fmt.Errorf("post as requester: send: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("post as requester: talkjs POST message returned %d: %s", status, truncate(string(raw), 200))
	}
	return nil
}

// relayChatMessageToRequester is the webhook's side: a message.sent from the
// supporter goes to the requester's number, when they book by text. The
// requester's own messages (posted by PostAsRequester, or from the app)
// are not echoed back. Runs on a background context — never on the webhook
// request path (S-32).
func relayChatMessageToRequester(db *sql.DB, taskID, senderUID, supporterName string, msg talkjsMessage) {
	hook := chatRelayHook
	if hook == nil || db == nil || taskID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var requesterID, supporterID, title sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT requester_id::text, assigned_to_id::text, title
		  FROM public.tasks WHERE id = $1::uuid`, taskID).Scan(&requesterID, &supporterID, &title)
	if err != nil {
		log.Printf("[talkjs][relay] task=%s lookup: %v", taskID, err)
		return
	}
	// Only the assigned supporter's words reach the requester's phone: not
	// the requester's own, not a former supporter who still has read access.
	if senderUID == "" || senderUID != supporterID.String || senderUID == requesterID.String {
		return
	}
	if blocked, err := pairBlocked(ctx, db, senderUID, requesterID.String); err != nil || blocked {
		if err != nil {
			log.Printf("[talkjs][relay] task=%s block check: %v", taskID, err)
		}
		return
	}
	mediaURL := ""
	if msg.Attachment != nil {
		mediaURL = msg.Attachment.URL
	}
	hook(ctx, requesterID.String, taskID, supporterName, title.String, msg.Text, mediaURL)
}

func relayOr(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func firstNameOf(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.IndexByte(name, ' '); i > 0 {
		return name[:i]
	}
	return name
}
