package channel

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
)

// Chat relay (PLAN §6 steps 5–7, §7; M3.2). The supporter talks in the
// task's chat room (TalkJS); the requester talks in iMessage/WhatsApp. This
// file bridges the two for a requester who books by text:
//
//   - supporter → requester: every message in the task chat reaches the
//     requester's thread as "Maya · Laundry pickup: <text>" (status.go's
//     hook delivers it; see RelayToChat).
//   - requester → supporter: a message from a linked number with an active
//     task is posted into that task's chat as the requester. An inline reply
//     to one of our messages goes to that message's task; otherwise exactly
//     one active task → that one; several → a numbered list, and the next
//     message picks by number. Never guessed.
//
// A new errand still wins: the intent classifier runs first, and new_task
// drafts as before. Relaying moves no money and changes no task state.

// ActiveTask is a task the requester could be talking to a supporter about:
// open, with a supporter assigned.
type ActiveTask struct {
	ID            string
	Title         string
	SupporterName string
}

// TaskChat is the supporter's side of the bridge.
type TaskChat interface {
	// ActiveTasks lists the requester's tasks with a supporter, newest first.
	ActiveTasks(ctx context.Context, userID string) ([]ActiveTask, error)
	// PostAsRequester puts text into the task's chat room as the requester.
	PostAsRequester(ctx context.Context, taskID, userID, text string) error
}

// TaskLookup resolves which task one of our earlier messages was about, for
// inline replies (Sendblue reply_to, WhatsApp context).
type TaskLookup interface {
	TaskForProviderMessage(ctx context.Context, ch Channel, providerMessageID string) (string, error)
	// SetMessageTask records which task an inbound message turned out to be
	// about, so a later inline reply to the supporter's answer threads back.
	SetMessageTask(ctx context.Context, ch Channel, providerMessageID, taskID string) error
}

var choiceRE = regexp.MustCompile(`^\s*#?\s*(\d{1,2})\s*[.)]?\s*$`)

// taskChoice reads "2" / "#2" / "2." as a 1-based pick; ok=false otherwise.
func taskChoice(text string) (int, bool) {
	m := choiceRE.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// inlineTarget: an inline reply to one of our messages names its task
// (step 5). "" when the message is not a reply, the replied-to message was
// not about a task, or the lookup is unavailable (logged; the message then
// routes by intent like any other).
func (r *Router) inlineTarget(ctx context.Context, in Inbound, id Identity) string {
	if r.Chat == nil || r.Tasks == nil || !id.Linked() || in.ReplyToProviderID == "" {
		return ""
	}
	tid, err := r.Tasks.TaskForProviderMessage(ctx, in.Channel, in.ReplyToProviderID)
	if err != nil {
		log.Printf("[channel] inline target identity=%s: %v", id.ID, err)
		return ""
	}
	return tid
}

// relayOr sends a non-errand message from a linked requester to their
// supporter: one active task → relayed; several → the numbered list; none
// (or relay not configured) → the fallback copy, as before M3.2.
func (r *Router) relayOr(ctx context.Context, in Inbound, id Identity, conv Conversation, text, fallback string) error {
	if r.Chat == nil {
		return r.Reply(ctx, in, fallback, "")
	}
	active, err := r.Chat.ActiveTasks(ctx, id.UserID)
	if err != nil {
		return fmt.Errorf("relayOr: %w", err)
	}
	switch len(active) {
	case 0:
		return r.Reply(ctx, in, fallback, "")
	case 1:
		return r.relay(ctx, in, id, active[0].ID, text)
	default:
		return r.askWhichTask(ctx, in, conv, active, text)
	}
}

// relay posts the message to the supporter and records which task it was
// about. The requester gets no echo: their own message is already on their
// screen.
func (r *Router) relay(ctx context.Context, in Inbound, id Identity, taskID, text string) error {
	if err := r.Chat.PostAsRequester(ctx, taskID, id.UserID, text); err != nil {
		log.Printf("[channel] relay identity=%s task=%s: %v", id.ID, taskID, err)
		return r.Reply(ctx, in, CopyRelayFailed, taskID)
	}
	if r.Tasks != nil {
		if err := r.Tasks.SetMessageTask(ctx, in.Channel, in.ProviderMessageID, taskID); err != nil {
			log.Printf("[channel] relay identity=%s task=%s: %v", id.ID, taskID, fmt.Errorf("record task: %w", err))
		}
	}
	log.Printf("[channel] relayed identity=%s task=%s len=%d", id.ID, taskID, len(text))
	return nil
}

// askWhichTask shows the numbered list and keeps the message for after the
// pick (pending_task_choices + the text, via the conversation).
func (r *Router) askWhichTask(ctx context.Context, in Inbound, conv Conversation, choices []ActiveTask, text string) error {
	var b strings.Builder
	b.WriteString(CopyWhichTask)
	ids := make([]string, 0, len(choices))
	for i, t := range choices {
		fmt.Fprintf(&b, "\n%d. %s", i+1, t.Title)
		if t.SupporterName != "" {
			fmt.Fprintf(&b, " (%s)", t.SupporterName)
		}
		ids = append(ids, t.ID)
	}
	b.WriteString("\n\n" + CopyWhichTaskHint)
	conv.State = StateAwaitingTaskChoice
	conv.PendingTaskChoices = ids
	conv.PendingRelayText = text
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
		return fmt.Errorf("askWhichTask: %w", err)
	}
	return r.Reply(ctx, in, b.String(), "")
}

// onAwaitingTaskChoice: a number picks; anything else is treated as a fresh
// message (a new errand drafts; "new" explicitly starts one).
func (r *Router) onAwaitingTaskChoice(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	choices, held := conv.PendingTaskChoices, conv.PendingRelayText
	conv.State = StateIdle
	conv.PendingTaskChoices = nil
	conv.PendingRelayText = ""
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
		return fmt.Errorf("onAwaitingTaskChoice: %w", err)
	}
	if n, ok := taskChoice(text); ok {
		if n > len(choices) {
			return r.Reply(ctx, in, CopyWhichTaskBadPick, "")
		}
		if held == "" {
			return r.Reply(ctx, in, CopyWhichTaskNothingHeld, "")
		}
		return r.relay(ctx, in, id, choices[n-1], held)
	}
	if strings.EqualFold(strings.TrimSpace(text), "new") {
		return r.Reply(ctx, in, CopyHelloAgain, "")
	}
	// Not a pick: handle it as an ordinary message.
	return r.onIdle(ctx, in, id, conv, text)
}

// RelayToChat delivers a supporter's chat message to the requester's thread
// (called from the TalkJS webhook through the status hook). Threaded to the
// task so the requester's inline reply finds it.
func (r *Router) RelayToChat(ctx context.Context, requesterID, taskID, supporterName, taskTitle, text, mediaURL string) error {
	if r.Status == nil {
		return nil
	}
	t, found, err := r.Status.StatusTargetFor(ctx, requesterID, taskID)
	if err != nil {
		return fmt.Errorf("RelayToChat: %w", err)
	}
	if !found {
		return nil // app-only requester: the app's own push already went
	}
	if t.Identity.Channel == WhatsApp && (t.LastInboundAt == nil || r.now().Sub(*t.LastInboundAt) > WhatsAppWindow) {
		log.Printf("[channel] relay to chat task=%s not sent: outside WhatsApp 24h window", taskID)
		return nil
	}
	who := firstNonEmpty(strings.TrimSpace(supporterName), "Your supporter")
	body := strings.TrimSpace(text)
	if body == "" && mediaURL != "" {
		body = "(photo)"
	}
	out := Outbound{Channel: t.Identity.Channel, To: t.Identity.Address,
		Text: fmt.Sprintf("%s · %s: %s", who, firstNonEmpty(strings.TrimSpace(taskTitle), "your task"), body), MediaURL: mediaURL}
	return r.Send(ctx, out, taskID)
}
