package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// Covers the parts of the TalkJS push bridge that need no database: the HMAC
// gate on the endpoint, who a message.sent event should wake, and how the
// event maps onto a push. Recipient → HO:RA user id resolution is a single
// email lookup and is exercised on a device instead (see the manual steps in
// the PR notes).
//
//	go test ./ -run TalkJS -v

const testWebhookSecret = "sk_test_talkjs_webhook_secret"

func signTalkJS(t *testing.T, secret, timestamp string, body []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return strings.ToUpper(hex.EncodeToString(mac.Sum(nil)))
}

func postTalkJSWebhook(t *testing.T, body []byte, timestamp, signature string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Nil DB: every test here stops before the user lookup, which no-ops on a
	// nil handle rather than panicking.
	RegisterTalkJSWebhooks(r, nil)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/talkjs", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if timestamp != "" {
		req.Header.Set(talkjsTimestampHeader, timestamp)
	}
	if signature != "" {
		req.Header.Set(talkjsSignatureHeader, signature)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func sampleEventJSON() []byte {
	return []byte(`{
	  "id": "evt_1",
	  "type": "message.sent",
	  "createdAt": 1755500000000,
	  "data": {
	    "sender": { "id": "alice@example.com", "name": "Alice" },
	    "conversation": {
	      "id": "task_11111111-2222-3333-4444-555555555555",
	      "subject": "Grocery run",
	      "custom": { "taskId": "11111111-2222-3333-4444-555555555555" },
	      "participants": {
	        "alice@example.com": { "access": "ReadWrite", "notify": true },
	        "bob@example.com": { "access": "ReadWrite", "notify": true }
	      }
	    },
	    "message": {
	      "id": "msg_1",
	      "conversationId": "task_11111111-2222-3333-4444-555555555555",
	      "type": "UserMessage",
	      "senderId": "alice@example.com",
	      "text": "On my way",
	      "readBy": []
	    },
	    "mentionedUserIds": []
	  }
	}`)
}

// ── Signature gate ─────────────────────────────────────────────────────────

func TestTalkJSWebhookRejectsBadSignature(t *testing.T) {
	t.Setenv("TALKJS_WEBHOOK_SECRET", testWebhookSecret)
	body := sampleEventJSON()

	w := postTalkJSWebhook(t, body, "1755500000000", "DEADBEEF")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature: got %d, want 401", w.Code)
	}
}

func TestTalkJSWebhookRejectsMissingSignatureHeaders(t *testing.T) {
	t.Setenv("TALKJS_WEBHOOK_SECRET", testWebhookSecret)
	body := sampleEventJSON()

	if w := postTalkJSWebhook(t, body, "1755500000000", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("missing signature: got %d, want 401", w.Code)
	}
	if w := postTalkJSWebhook(t, body, "", signTalkJS(t, testWebhookSecret, "", body)); w.Code != http.StatusUnauthorized {
		t.Errorf("missing timestamp: got %d, want 401", w.Code)
	}
}

func TestTalkJSWebhookRejectsWhenSecretUnset(t *testing.T) {
	t.Setenv("TALKJS_WEBHOOK_SECRET", "")
	body := sampleEventJSON()
	ts := "1755500000000"

	w := postTalkJSWebhook(t, body, ts, signTalkJS(t, testWebhookSecret, ts, body))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unset secret must fail closed: got %d, want 401", w.Code)
	}
}

func TestTalkJSWebhookAcceptsValidSignature(t *testing.T) {
	t.Setenv("TALKJS_WEBHOOK_SECRET", testWebhookSecret)
	body := sampleEventJSON()
	ts := "1755500000000"

	w := postTalkJSWebhook(t, body, ts, signTalkJS(t, testWebhookSecret, ts, body))
	if w.Code != http.StatusOK {
		t.Fatalf("valid signature: got %d, want 200", w.Code)
	}
}

// A signature over a different body must not pass — this is the replay/tamper
// case, not just a malformed header.
func TestTalkJSWebhookRejectsTamperedBody(t *testing.T) {
	t.Setenv("TALKJS_WEBHOOK_SECRET", testWebhookSecret)
	ts := "1755500000000"
	sig := signTalkJS(t, testWebhookSecret, ts, sampleEventJSON())

	tampered := strings.Replace(string(sampleEventJSON()), "On my way", "Send me money", 1)
	if w := postTalkJSWebhook(t, []byte(tampered), ts, sig); w.Code != http.StatusUnauthorized {
		t.Fatalf("tampered body: got %d, want 401", w.Code)
	}
}

func TestVerifyTalkJSSignatureIsCaseInsensitiveOnHex(t *testing.T) {
	body := sampleEventJSON()
	ts := "1755500000000"
	upper := signTalkJS(t, testWebhookSecret, ts, body)

	if !verifyTalkJSSignature(testWebhookSecret, ts, body, upper) {
		t.Error("uppercase hex signature should verify")
	}
	if !verifyTalkJSSignature(testWebhookSecret, ts, body, strings.ToLower(upper)) {
		t.Error("lowercase hex signature should verify")
	}
	if verifyTalkJSSignature("other_secret", ts, body, upper) {
		t.Error("wrong secret must not verify")
	}
}

// ── Recipient resolution ───────────────────────────────────────────────────

func parseEvent(t *testing.T, raw []byte) *talkjsWebhookEvent {
	t.Helper()
	var ev talkjsWebhookEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &ev
}

func TestTalkJSRecipientsExcludeSender(t *testing.T) {
	ev := parseEvent(t, sampleEventJSON())
	got := talkjsPushRecipients(ev)
	want := []string{"bob@example.com"}

	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("recipients = %v, want %v (sender must be excluded)", got, want)
	}
}

func TestTalkJSRecipientsExcludeReadByAndOptOuts(t *testing.T) {
	raw := []byte(`{
	  "type": "message.sent",
	  "data": {
	    "sender": { "id": "alice@example.com", "name": "Alice" },
	    "conversation": {
	      "id": "task_abc",
	      "participants": {
	        "alice@example.com": { "notify": true },
	        "bob@example.com":   { "notify": true },
	        "carol@example.com": { "notify": true },
	        "dave@example.com":  { "notify": false },
	        "erin@example.com":  { "notify": "MentionsOnly" },
	        "frank@example.com": { "notify": "MentionsOnly" }
	      }
	    },
	    "message": {
	      "type": "UserMessage",
	      "senderId": "alice@example.com",
	      "text": "hello",
	      "readBy": ["carol@example.com"]
	    },
	    "mentionedUserIds": ["frank@example.com"]
	  }
	}`)

	got := talkjsPushRecipients(parseEvent(t, raw))
	// bob: plain participant. frank: MentionsOnly and mentioned.
	// alice = sender, carol = already read it (in the conversation),
	// dave = notify off, erin = MentionsOnly but not mentioned.
	want := []string{"bob@example.com", "frank@example.com"}

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("recipients = %v, want %v", got, want)
	}
}

func TestTalkJSRecipientsDefaultNotifyWhenAbsent(t *testing.T) {
	raw := []byte(`{
	  "type": "message.sent",
	  "data": {
	    "sender": { "id": "alice@example.com" },
	    "conversation": {
	      "id": "task_abc",
	      "participants": { "alice@example.com": {}, "bob@example.com": {} }
	    },
	    "message": { "type": "UserMessage", "senderId": "alice@example.com", "text": "hi" }
	  }
	}`)

	got := talkjsPushRecipients(parseEvent(t, raw))
	if len(got) != 1 || got[0] != "bob@example.com" {
		t.Fatalf("recipients = %v, want [bob@example.com] (absent notify defaults to true)", got)
	}
}

// A sender who is somehow absent from data.sender still must not be pushed.
func TestTalkJSRecipientsFallBackToMessageSenderID(t *testing.T) {
	raw := []byte(`{
	  "type": "message.sent",
	  "data": {
	    "sender": {},
	    "conversation": {
	      "id": "task_abc",
	      "participants": { "alice@example.com": {}, "bob@example.com": {} }
	    },
	    "message": { "type": "UserMessage", "senderId": "alice@example.com", "text": "hi" }
	  }
	}`)

	got := talkjsPushRecipients(parseEvent(t, raw))
	if len(got) != 1 || got[0] != "bob@example.com" {
		t.Fatalf("recipients = %v, want [bob@example.com]", got)
	}
}

// ── Payload mapping ────────────────────────────────────────────────────────

func TestTalkJSTaskIDPrefersCustomAttribute(t *testing.T) {
	ev := parseEvent(t, sampleEventJSON())
	got := talkjsTaskID(ev.Data.Conversation, ev.Data.Message)
	want := "11111111-2222-3333-4444-555555555555"
	if got != want {
		t.Fatalf("taskID = %q, want %q", got, want)
	}
}

func TestTalkJSTaskIDFallsBackToConversationIDPrefix(t *testing.T) {
	conv := talkjsConversation{ID: "task_99999999-8888-7777-6666-555555555555"}
	got := talkjsTaskID(conv, talkjsMessage{})
	want := "99999999-8888-7777-6666-555555555555"
	if got != want {
		t.Fatalf("taskID = %q, want %q", got, want)
	}
}

func TestTalkJSPushBody(t *testing.T) {
	long := strings.Repeat("a", 150)

	cases := []struct {
		name string
		msg  talkjsMessage
		want string
	}{
		{"plain text", talkjsMessage{Text: "On my way"}, "On my way"},
		{"attachment with no text", talkjsMessage{Attachment: &talkjsFile{URL: "https://x/y.jpg"}}, "Sent an attachment"},
		{"attachment caption wins", talkjsMessage{Text: "here it is", Attachment: &talkjsFile{URL: "https://x/y.jpg"}}, "here it is"},
		{"location", talkjsMessage{Location: json.RawMessage(`[1.0,2.0]`)}, "Shared a location"},
		{"empty", talkjsMessage{}, "Sent a message"},
		{"truncated", talkjsMessage{Text: long}, strings.Repeat("a", talkjsBodyPreviewLimit) + "…"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := talkjsPushBody(tc.msg); got != tc.want {
				t.Fatalf("body = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTalkJSPushBodyTruncationIsRuneSafe(t *testing.T) {
	// 150 multi-byte characters: cutting on bytes would corrupt the last one.
	msg := talkjsMessage{Text: strings.Repeat("é", 150)}
	got := talkjsPushBody(msg)

	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis, got %q", got)
	}
	trimmed := strings.TrimSuffix(got, "…")
	if n := len([]rune(trimmed)); n != talkjsBodyPreviewLimit {
		t.Fatalf("preview = %d runes, want %d", n, talkjsBodyPreviewLimit)
	}
	if !strings.HasPrefix(trimmed, "éé") {
		t.Fatalf("preview corrupted: %q", trimmed)
	}
}

func TestTalkJSPushTitle(t *testing.T) {
	cases := []struct {
		name   string
		sender talkjsUser
		want   string
	}{
		{"name", talkjsUser{ID: "alice@example.com", Name: "Alice"}, "Alice"},
		{"email local part", talkjsUser{ID: "alice@example.com"}, "alice"},
		{"neither", talkjsUser{}, "New message"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := talkjsPushTitle(tc.sender); got != tc.want {
				t.Fatalf("title = %q, want %q", got, tc.want)
			}
		})
	}
}

// System messages are bookkeeping (participant joined, etc.) and must not push.
func TestTalkJSWebhookIgnoresSystemMessages(t *testing.T) {
	t.Setenv("TALKJS_WEBHOOK_SECRET", testWebhookSecret)
	body := []byte(strings.Replace(string(sampleEventJSON()), `"type": "UserMessage"`, `"type": "SystemMessage"`, 1))
	ts := "1755500000000"

	w := postTalkJSWebhook(t, body, ts, signTalkJS(t, testWebhookSecret, ts, body))
	if w.Code != http.StatusOK {
		t.Fatalf("system message should be ACKed: got %d, want 200", w.Code)
	}
}
