package channel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"hora-auth/internal/channel/llm"
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
	// key is the inbound provider message id, for at-most-once delivery
	// across the recovery sweep. ErrTaskNotActive (wrapped) when the task
	// is no longer open with a supporter, or is not the sender's.
	PostAsRequester(ctx context.Context, taskID, userID, text, key string) error
}

// ErrTaskNotActive: the task can no longer take a message (finished,
// cancelled, no supporter, blocked, or not the sender's).
var ErrTaskNotActive = errors.New("task not active")

// relayTextCap bounds what is sent on (WhatsApp rejects >4096; a hold that
// keeps growing must not become undeliverable).
const relayTextCap = 1500

// TaskLookup resolves which task one of our earlier messages was about, for
// inline replies (Sendblue reply_to, WhatsApp context).
type TaskLookup interface {
	// TaskForProviderMessage: the task a message was about ("" if none),
	// and whether that message was a supporter's relayed words (as opposed
	// to one of our own prompts or status lines).
	TaskForProviderMessage(ctx context.Context, ch Channel, providerMessageID string) (taskID string, relay bool, err error)
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
func (r *Router) inlineTarget(ctx context.Context, in Inbound, id Identity) (taskID string, relay bool) {
	if r.Chat == nil || r.Tasks == nil || !id.Linked() || in.ReplyToProviderID == "" {
		return "", false
	}
	tid, isRelay, err := r.Tasks.TaskForProviderMessage(ctx, in.Channel, in.ReplyToProviderID)
	if err != nil {
		log.Printf("[channel] inline target identity=%s: %v", id.ID, err)
		return "", false
	}
	if tid == "" {
		return "", false
	}
	// Only an active task: a reply to "Maya finished!" routes like any
	// other message instead of hitting a dead end.
	active, err := r.Chat.ActiveTasks(ctx, id.UserID)
	if err != nil {
		log.Printf("[channel] inline target identity=%s: %v", id.ID, err)
		return "", false
	}
	for _, t := range active {
		if t.ID == tid {
			return tid, isRelay
		}
	}
	return "", false
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
	if err := r.Chat.PostAsRequester(ctx, taskID, id.UserID, capText(text), in.ProviderMessageID); err != nil {
		log.Printf("[channel] relay identity=%s task=%s: %v", id.ID, taskID, err)
		if errors.Is(err, ErrTaskNotActive) {
			return r.Reply(ctx, in, CopyRelayGone, "")
		}
		return r.Reply(ctx, in, CopyRelayFailed, taskID)
	}
	r.noteRelayed(ctx, in, id, taskID, text)
	return nil
}

func capText(s string) string {
	if len(s) <= relayTextCap {
		return s
	}
	cut := relayTextCap
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// taskList renders the numbered list for the held ids (titles looked up
// fresh; an id no longer active is listed as finished).
func (r *Router) taskList(ctx context.Context, id Identity, ids []string) string {
	titles := map[string]string{}
	if r.Chat != nil {
		if active, err := r.Chat.ActiveTasks(ctx, id.UserID); err == nil {
			for _, t := range active {
				titles[t.ID] = t.Title
				if t.SupporterName != "" {
					titles[t.ID] += " (" + t.SupporterName + ")"
				}
			}
		}
	}
	var b strings.Builder
	for i, tid := range ids {
		fmt.Fprintf(&b, "%d. %s\n", i+1, firstNonEmpty(titles[tid], "(finished)"))
	}
	b.WriteString("\n" + CopyWhichTaskHint)
	return b.String()
}

func (r *Router) noteRelayed(ctx context.Context, in Inbound, id Identity, taskID, text string) {
	if r.Tasks != nil {
		if err := r.Tasks.SetMessageTask(ctx, in.Channel, in.ProviderMessageID, taskID); err != nil {
			log.Printf("[channel] relay identity=%s task=%s: %v", id.ID, taskID, fmt.Errorf("record task: %w", err))
		}
	}
	log.Printf("[channel] relayed identity=%s task=%s len=%d", id.ID, taskID, len(text))
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

// onAwaitingTaskChoice: a number picks and delivers the held message; the
// hold is cleared only once the supporter has it (a failed post keeps it,
// and the person is told to reply the number again). A number off the list
// re-asks and keeps the hold. Anything else is a fresh message: a new
// errand drafts; other small talk is added to the hold and the list is
// shown again (nothing is dropped unannounced).
func (r *Router) onAwaitingTaskChoice(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	choices, held := conv.PendingTaskChoices, conv.PendingRelayText
	if held == "" {
		return r.clearHold(ctx, in, conv, CopyWhichTaskNothingHeld)
	}
	if r.Chat == nil {
		// Relay switched off under a hold: say so, don't pretend.
		return r.clearHold(ctx, in, conv, CopyHoldGone)
	}
	// The way out: "never mind" / "cancel" drops the hold (rule-matched).
	if IsNo(text) {
		return r.clearHold(ctx, in, conv, CopyHoldDropped)
	}
	if n, ok := taskChoice(text); ok {
		if n > len(choices) {
			return r.Reply(ctx, in, CopyWhichTaskBadPick, "")
		}
		taskID := choices[n-1]
		if err := r.Chat.PostAsRequester(ctx, taskID, id.UserID, capText(held), in.ProviderMessageID); err != nil {
			log.Printf("[channel] relay identity=%s task=%s: %v", id.ID, taskID, err)
			if errors.Is(err, ErrTaskNotActive) {
				return r.clearHold(ctx, in, conv, CopyRelayGone)
			}
			return r.Reply(ctx, in, CopyRelayFailedPick, taskID)
		}
		conv.State, conv.PendingTaskChoices, conv.PendingRelayText = StateIdle, nil, ""
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("onAwaitingTaskChoice: %w", err)
		}
		r.noteRelayed(ctx, in, id, taskID, held)
		return nil
	}
	// Not a pick. A new errand drafts (the hold is dropped — the person has
	// moved on; they were asked once); anything else joins the hold.
	intent := r.classify(ctx, text)
	if intent == llm.IntentNewTask {
		conv.State, conv.PendingTaskChoices, conv.PendingRelayText = StateIdle, nil, ""
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("onAwaitingTaskChoice: %w", err)
		}
		return r.startDraft(ctx, in, id, conv, text)
	}
	active, err := r.Chat.ActiveTasks(ctx, id.UserID)
	if err != nil {
		return fmt.Errorf("onAwaitingTaskChoice: %w", err)
	}
	joined := capText(strings.TrimSpace(held + "\n" + text))
	switch len(active) {
	case 0:
		// Every supporter finished meanwhile: the held words have nowhere
		// to go, and the person is told so.
		return r.clearHold(ctx, in, conv, CopyHoldGone)
	case 1:
		// Deliver first; the hold is cleared only once the supporter has it.
		taskID := active[0].ID
		if err := r.Chat.PostAsRequester(ctx, taskID, id.UserID, joined, in.ProviderMessageID); err != nil {
			log.Printf("[channel] relay identity=%s task=%s: %v", id.ID, taskID, err)
			if errors.Is(err, ErrTaskNotActive) {
				return r.clearHold(ctx, in, conv, CopyRelayGone)
			}
			conv.PendingRelayText = joined
			if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
				return fmt.Errorf("onAwaitingTaskChoice: %w", err)
			}
			return r.Reply(ctx, in, CopyRelayFailed, taskID)
		}
		conv.State, conv.PendingTaskChoices, conv.PendingRelayText = StateIdle, nil, ""
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("onAwaitingTaskChoice: %w", err)
		}
		r.noteRelayed(ctx, in, id, taskID, joined)
		return nil
	default:
		return r.askWhichTask(ctx, in, conv, active, joined)
	}
}

func (r *Router) clearHold(ctx context.Context, in Inbound, conv Conversation, reply string) error {
	conv.State, conv.PendingTaskChoices, conv.PendingRelayText = StateIdle, nil, ""
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
		return fmt.Errorf("clear hold: %w", err)
	}
	return r.Reply(ctx, in, reply, "")
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
	whoLower := firstNonEmpty(strings.TrimSpace(supporterName), "your supporter")
	body := capText(strings.TrimSpace(text))
	if body == "" && mediaURL != "" {
		body = "(attachment)"
		if looksLikeImageURL(mediaURL) {
			body = "(photo)"
		}
	}
	if body == "" {
		return nil // a location, a sticker, a system line: nothing to say
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s · %s: %s", who, firstNonEmpty(strings.TrimSpace(taskTitle), "your task"), body)
	// Money rule: if this thread is waiting on a YES/NO, say what a bare
	// YES/NO still means, so the supporter's question cannot capture it.
	if conv, err := r.Conversations.GetConversation(ctx, t.Identity.ID); err != nil {
		return fmt.Errorf("RelayToChat: %w", err)
	} else if note := r.pendingNote(conv, whoLower); note != "" {
		b.WriteString("\n\n" + note)
	}
	out := Outbound{Channel: t.Identity.Channel, To: t.Identity.Address, Text: b.String(), MediaURL: mediaURL, Relay: true}
	return r.Send(ctx, out, taskID)
}

// pendingNote states what a bare YES/NO answers while one is pending; ""
// when nothing is.
func (r *Router) pendingNote(conv Conversation, who string) string {
	switch conv.State {
	case StateAwaitingConfirm:
		if conv.Pending == nil {
			return ""
		}
		return fmt.Sprintf(CopyRelayWhileConfirm, conv.Pending.Headline(), Dollars(conv.Pending.TotalCents()), who)
	case StateAwaitingApproval:
		return fmt.Sprintf(CopyRelayWhileApproval, who)
	}
	return ""
}
