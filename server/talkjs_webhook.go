package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"hora-auth/internal/notify"
)

// Chat lives entirely in TalkJS, so a backgrounded or killed app never learns
// about a new message on its own. This webhook is the bridge: TalkJS posts
// every message.sent here and we fan it out over the same Expo push channel
// task events already use.
//
// Deliberately push-only. No notifications row is written and no new
// notifications.type value is introduced, because the in-app notifications
// list does not show chat — TalkJS owns that history. notify.SendPush is
// already independent of notify.Create's INSERT, so calling it directly is the
// whole of the decoupling: nothing was pulled apart to make this work.
//
// Known limitation, accepted for beta: TalkJS fires one event per message, so
// a rapid burst produces a push per message. A debounce/coalescing queue is a
// future refinement, not built here.

const (
	talkjsSignatureHeader = "X-TalkJS-Signature"
	talkjsTimestampHeader = "X-TalkJS-Timestamp"
	// Roughly a notification-shade line of text; the rest is read in the app.
	talkjsBodyPreviewLimit = 100
)

func RegisterTalkJSWebhooks(r *gin.Engine, db *sql.DB) {
	// Unauthenticated by design — TalkJS carries no session. The HMAC
	// signature is the entire authentication, so it is checked before the body
	// is parsed or trusted for anything.
	r.POST("/webhooks/talkjs", func(c *gin.Context) { handleTalkJSWebhook(c, db) })
}

// ── Webhook payload shapes (talkjs.com/docs/Reference/Webhooks) ─────────────

type talkjsWebhookEvent struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Sender           talkjsUser         `json:"sender"`
		Conversation     talkjsConversation `json:"conversation"`
		Message          talkjsMessage      `json:"message"`
		MentionedUserIDs []string           `json:"mentionedUserIds"`
	} `json:"data"`
}

type talkjsUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type talkjsConversation struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	// Both clients set custom.taskId when they build the conversation
	// (mobile src/app/task/[id]/chat.tsx, web TaskChatBox.jsx).
	Custom       map[string]string            `json:"custom"`
	Participants map[string]talkjsParticipant `json:"participants"`
}

type talkjsParticipant struct {
	Access string `json:"access"`
	// boolean | "MentionsOnly" — two JSON types in one field, so it is decoded
	// raw and interpreted in participantWantsPush.
	Notify json.RawMessage `json:"notify"`
}

type talkjsMessage struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversationId"`
	// "UserMessage" | "SystemMessage" — system messages are bookkeeping and
	// are never pushed.
	Type     string `json:"type"`
	SenderID string `json:"senderId"`
	Text     string `json:"text"`
	// Participants TalkJS already considers to have seen this message. A user
	// with the conversation open is marked read at send time, which is how we
	// avoid pushing someone who is actively looking at the chat — the payload
	// carries no separate presence field.
	ReadBy     []string        `json:"readBy"`
	Attachment *talkjsFile     `json:"attachment"`
	Location   json.RawMessage `json:"location"`
}

type talkjsFile struct {
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

// ── Handler ────────────────────────────────────────────────────────────────

func handleTalkJSWebhook(c *gin.Context, db *sql.DB) {
	secret := os.Getenv("TALKJS_WEBHOOK_SECRET")
	if secret == "" {
		// Without a secret nothing can be authenticated, so nothing is
		// trusted. Fail closed rather than accepting unsigned traffic.
		log.Printf("[talkjs][webhook] TALKJS_WEBHOOK_SECRET not set — rejecting")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	// The signature covers the exact bytes TalkJS sent, so the raw body has to
	// be read before any decoding.
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		log.Printf("[talkjs][webhook] body read error: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}

	if !verifyTalkJSSignature(secret, c.GetHeader(talkjsTimestampHeader), raw, c.GetHeader(talkjsSignatureHeader)) {
		log.Printf("[talkjs][webhook] signature mismatch — rejecting")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var ev talkjsWebhookEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		log.Printf("[talkjs][webhook] decode error: %v", err)
		// Authenticated but unparseable: ACK so TalkJS stops retrying a
		// payload that will never parse.
		c.Status(http.StatusOK)
		return
	}

	// Only message.sent is subscribed, but the endpoint may receive others if
	// the dashboard is reconfigured; ignore them quietly.
	if ev.Type != "message.sent" || ev.Data.Message.Type == "SystemMessage" {
		c.Status(http.StatusOK)
		return
	}

	taskID := talkjsTaskID(ev.Data.Conversation, ev.Data.Message)
	recipients := talkjsPushRecipients(&ev)
	title := talkjsPushTitle(ev.Data.Sender)
	body := talkjsPushBody(ev.Data.Message)

	log.Printf("[talkjs][webhook] message.sent conv=%s task=%s sender=%s recipients=%d",
		ev.Data.Conversation.ID, taskID, ev.Data.Sender.ID, len(recipients))

	for _, talkjsID := range recipients {
		userID, err := userIDForTalkJSID(c.Request.Context(), db, talkjsID)
		if err != nil {
			log.Printf("[talkjs][webhook] recipient lookup failed talkjs_id=%s err=%v", talkjsID, err)
			continue
		}
		if userID == "" {
			log.Printf("[talkjs][webhook] no HO:RA user for talkjs_id=%s — skipping", talkjsID)
			continue
		}
		// Same shape every task-event push uses: data carries task_id + type,
		// which is all the mobile tap handler needs to open the chat. Fired in
		// a goroutine on a background context so a slow Expo call can never
		// hold up the webhook response (S-32).
		go notify.SendPush(context.Background(), db, userID, taskID, "NEW_MESSAGE", title, body)
	}

	c.Status(http.StatusOK)
}

// ── Signature ──────────────────────────────────────────────────────────────

// verifyTalkJSSignature checks the X-TalkJS-Signature header per
// talkjs.com/docs/Features/Security: HMAC-SHA256 over `timestamp + "." + raw
// body`, keyed with the secret marked "use for webhooks", hex-encoded in
// uppercase. Compared in constant time.
func verifyTalkJSSignature(secret, timestamp string, rawBody []byte, got string) bool {
	if secret == "" || timestamp == "" || got == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(rawBody)
	want := strings.ToUpper(hex.EncodeToString(mac.Sum(nil)))
	// Uppercase per the docs, but compare case-insensitively so a correct
	// digest is never rejected over presentation alone.
	return hmac.Equal([]byte(want), []byte(strings.ToUpper(got)))
}

// ── Recipient resolution ───────────────────────────────────────────────────

// talkjsPushRecipients returns the TalkJS user ids that should receive a push
// for this message: every conversation participant except the sender, anyone
// TalkJS already marked as having read it (i.e. sitting in the conversation
// right now), and anyone whose participant `notify` setting opts out.
func talkjsPushRecipients(ev *talkjsWebhookEvent) []string {
	senderID := ev.Data.Sender.ID
	if senderID == "" {
		senderID = ev.Data.Message.SenderID
	}

	readBy := make(map[string]bool, len(ev.Data.Message.ReadBy))
	for _, id := range ev.Data.Message.ReadBy {
		readBy[id] = true
	}

	mentioned := make(map[string]bool, len(ev.Data.MentionedUserIDs))
	for _, id := range ev.Data.MentionedUserIDs {
		mentioned[id] = true
	}

	out := make([]string, 0, len(ev.Data.Conversation.Participants))
	for id, p := range ev.Data.Conversation.Participants {
		if id == "" || id == senderID {
			continue // self-echo
		}
		if readBy[id] {
			continue // already looking at the conversation
		}
		if !participantWantsPush(p, mentioned[id]) {
			continue
		}
		out = append(out, id)
	}
	// Map iteration order is random; sort so behaviour (and tests) are stable.
	sortStrings(out)
	return out
}

// participantWantsPush interprets the participant's `notify` setting, which is
// either a boolean or the string "MentionsOnly". Absent means TalkJS's default
// of true.
func participantWantsPush(p talkjsParticipant, isMentioned bool) bool {
	raw := strings.TrimSpace(string(p.Notify))
	switch {
	case raw == "" || raw == "null":
		return true // default
	case raw == "true":
		return true
	case raw == "false":
		return false
	case raw == `"MentionsOnly"`:
		return isMentioned
	default:
		return true
	}
}

// talkjsTaskID recovers the HO:RA task id a conversation belongs to. Both
// clients set custom.taskId; the `task_<uuid>` conversation id they also both
// use is the fallback for any conversation created before that attribute
// existed.
func talkjsTaskID(conv talkjsConversation, msg talkjsMessage) string {
	if id := strings.TrimSpace(conv.Custom["taskId"]); id != "" {
		return id
	}
	convID := conv.ID
	if convID == "" {
		convID = msg.ConversationID
	}
	return strings.TrimPrefix(convID, "task_")
}

func talkjsPushTitle(sender talkjsUser) string {
	if name := strings.TrimSpace(sender.Name); name != "" {
		return name
	}
	if id := strings.TrimSpace(sender.ID); id != "" {
		// Ids are emails; show the local part rather than a bare blank title.
		if at := strings.Index(id, "@"); at > 0 {
			return id[:at]
		}
		return id
	}
	return "New message"
}

// talkjsPushBody builds the notification body: a trimmed preview of the text,
// or a stand-in when the message carries a file instead of words.
func talkjsPushBody(msg talkjsMessage) string {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		if msg.Attachment != nil {
			return "Sent an attachment"
		}
		if len(msg.Location) > 0 && string(msg.Location) != "null" {
			return "Shared a location"
		}
		return "Sent a message"
	}
	return truncatePreview(text, talkjsBodyPreviewLimit)
}

// truncatePreview cuts to at most limit runes, appending an ellipsis when it
// actually cut something. Rune-aware so a multi-byte character is never split.
func truncatePreview(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return strings.TrimRight(string(runes[:limit]), " ") + "…"
}

// userIDForTalkJSID reverses the client-side identity scheme: a TalkJS user id
// is the user's email (see talkjsSignatureHandler, which signs exactly that,
// and both chat clients, which set Talk.User id to the email). Returns "" when
// no user matches.
func userIDForTalkJSID(ctx context.Context, db *sql.DB, talkjsID string) (string, error) {
	if db == nil || talkjsID == "" {
		return "", nil
	}
	var uid string
	err := db.QueryRowContext(ctx,
		`SELECT id::text FROM public.users WHERE lower(email) = lower($1) LIMIT 1`, talkjsID).Scan(&uid)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return uid, nil
}

// sortStrings is a tiny insertion sort — the participant list is two entries in
// this product, so pulling in a dependency for it would be noise.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
