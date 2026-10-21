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
		   AND NOT `+usersBlockedSQL("t.requester_id", "t.assigned_to_id")+`
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
	pair, err := relayPair(ctx, t.sqlDB, taskID)
	if err != nil {
		return fmt.Errorf("post as requester: %w", err)
	}
	// The task must be the caller's and still open with a supporter: the id
	// came from ActiveTasks or from a channel_messages row, but the sender
	// is re-checked here, not trusted.
	if pair.requesterID == "" || pair.requesterID != userID {
		return fmt.Errorf("post as requester: task %s is not the sender's", taskID)
	}
	if pair.status != "open" || pair.supporterID == "" || pair.supporterEmail == "" || pair.requesterEmail == "" {
		return fmt.Errorf("post as requester: task %s has no open chat pair", taskID)
	}
	if blocked, err := pairBlocked(ctx, t.sqlDB, pair.requesterID, pair.supporterID); err != nil {
		return fmt.Errorf("post as requester: block check: %w", err)
	} else if blocked {
		return fmt.Errorf("post as requester: pair blocked")
	}
	if _, _, ok := talkjsConfigured(); !ok {
		return fmt.Errorf("post as requester: talkjs not configured")
	}

	// A requester who books by text has never opened a TalkJS client, so
	// their user may not exist there yet. PUT /users is create-or-update.
	if err := talkjsUpsertUser(ctx, pair.requesterEmail, pair.requesterName); err != nil {
		return fmt.Errorf("post as requester: %w", err)
	}
	convPath := "/conversations/" + url.PathEscape(talkjsConversationID(taskID))
	// PUT is create-or-update; participants given here are added with
	// ReadWrite. Subject/custom mirror what the clients set.
	status, _, err := talkjsRequest(ctx, http.MethodPut, convPath, map[string]any{
		"participants": []string{pair.requesterEmail, pair.supporterEmail},
		"subject":      relayOr(strings.TrimSpace(pair.title), "Task"),
		"custom":       map[string]string{"taskId": taskID},
	})
	if err != nil {
		return fmt.Errorf("post as requester: ensure conversation: %w", err)
	}
	if status != http.StatusOK {
		// Status only: TalkJS error bodies echo user ids, which are emails
		// here (S-12).
		return fmt.Errorf("post as requester: talkjs PUT conversation returned %d", status)
	}
	status, _, err = talkjsRequest(ctx, http.MethodPost, convPath+"/messages", []map[string]any{{
		"text":   text,
		"sender": pair.requesterEmail,
		"type":   "UserMessage",
	}})
	if err != nil {
		return fmt.Errorf("post as requester: send: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("post as requester: talkjs POST message returned %d", status)
	}
	return nil
}

// relayPairRow is what both directions check before anything crosses.
type relayPairRow struct {
	status                         string
	requesterID, supporterID       string
	requesterEmail, supporterEmail string
	requesterName, title           string
}

func relayPair(ctx context.Context, db *sql.DB, taskID string) (relayPairRow, error) {
	var p relayPairRow
	var rid, sid, re, se, rn, title sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT t.status, t.requester_id::text, t.assigned_to_id::text, t.title,
		       ru.email, su.email, rp.name
		  FROM public.tasks t
		  LEFT JOIN public.users ru ON ru.id = t.requester_id
		  LEFT JOIN public.users su ON su.id = t.assigned_to_id
		  LEFT JOIN public.profiles rp ON rp.id = t.requester_id
		 WHERE t.id = $1::uuid`, taskID).Scan(&p.status, &rid, &sid, &title, &re, &se, &rn)
	if err != nil {
		return p, err
	}
	p.requesterID, p.supporterID, p.title = rid.String, sid.String, title.String
	p.requesterEmail, p.supporterEmail, p.requesterName = re.String, se.String, rn.String
	return p, nil
}

// talkjsUpsertUser makes sure the user exists in TalkJS (id = email, as
// everywhere in this codebase). Idempotent.
func talkjsUpsertUser(ctx context.Context, email, name string) error {
	status, _, err := talkjsRequest(ctx, http.MethodPut, "/users/"+url.PathEscape(email), map[string]any{
		"name":  relayOr(strings.TrimSpace(name), "Requester"),
		"email": []string{email},
		"role":  "requester",
	})
	if err != nil {
		return fmt.Errorf("ensure talkjs user: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("ensure talkjs user: talkjs PUT user returned %d", status)
	}
	return nil
}

// relayChatMessageToRequester is the webhook's side: a message.sent from the
// supporter goes to the requester's number, when they book by text. The
// requester's own messages (posted by PostAsRequester, or from the app)
// are not echoed back. Runs on a background context — never on the webhook
// request path (S-32).
func relayChatMessageToRequester(db *sql.DB, conversationID, taskID, senderUID, supporterName string, msg talkjsMessage) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("[talkjs][relay] task=%s panic: %v", taskID, p)
		}
	}()
	hook := chatRelayHook
	if hook == nil || db == nil || taskID == "" {
		return
	}
	// custom.taskId is client-settable; the room itself must be the task's.
	if conversationID != talkjsConversationID(taskID) {
		log.Printf("[talkjs][relay] task=%s conversation mismatch — not relayed", taskID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pair, ok := relayAllowedToRequester(ctx, db, taskID, senderUID)
	if !ok {
		return
	}
	mediaURL := ""
	if msg.Attachment != nil {
		mediaURL = msg.Attachment.URL
	}
	hook(ctx, pair.requesterID, taskID, supporterName, pair.title, msg.Text, mediaURL)
}

// relayAllowedToRequester: only the assigned supporter's words, on an open
// task, with no block between the pair, reach the requester's phone — not
// the requester's own, not a former supporter who still has read access.
// Fails closed on any error.
func relayAllowedToRequester(ctx context.Context, db *sql.DB, taskID, senderUID string) (relayPairRow, bool) {
	pair, err := relayPair(ctx, db, taskID)
	if err != nil {
		log.Printf("[talkjs][relay] task=%s lookup: %v", taskID, err)
		return pair, false
	}
	if pair.status != "open" || senderUID == "" || senderUID != pair.supporterID || senderUID == pair.requesterID || pair.requesterID == "" {
		return pair, false
	}
	blocked, err := pairBlocked(ctx, db, senderUID, pair.requesterID)
	if err != nil {
		log.Printf("[talkjs][relay] task=%s block check: %v", taskID, err)
		return pair, false
	}
	return pair, !blocked
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
