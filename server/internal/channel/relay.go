package channel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"unicode"
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
	// TaskForProviderMessage: the task a message exchanged with address was
	// about ("" if none), whether that message was a supporter's relayed
	// words (as opposed to one of our own prompts or status lines), and
	// whether the message is on record at all.
	TaskForProviderMessage(ctx context.Context, ch Channel, address, providerMessageID string) (taskID string, relay, known bool, err error)
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

// bubbleKind is what an inline reply was swiped on.
type bubbleKind int

const (
	bubbleNone    bubbleKind = iota // not a reply
	bubbleUnknown                   // a reply, but the message is not on record
	bubblePrompt                    // one of our own prompts / status lines
	bubbleRelay                     // a supporter's relayed words
)

// bubble: what the person replied to, and the task it was about (""
// when none). An error is a lookup failure — fail closed, the webhook
// retries — never "unknown".
func (r *Router) bubble(ctx context.Context, in Inbound, id Identity) (bubbleKind, string, error) {
	if r.Tasks == nil || !id.Linked() || in.ReplyToProviderID == "" {
		return bubbleNone, "", nil
	}
	tid, isRelay, known, err := r.Tasks.TaskForProviderMessage(ctx, in.Channel, in.From, in.ReplyToProviderID)
	if err != nil {
		return bubbleNone, "", fmt.Errorf("bubble: %w", err)
	}
	switch {
	case !known:
		return bubbleUnknown, "", nil
	case isRelay:
		return bubbleRelay, tid, nil
	default:
		return bubblePrompt, tid, nil
	}
}

// isActive reports whether the task is one the person can still talk to a
// supporter about. An error fails closed.
func (r *Router) isActive(ctx context.Context, id Identity, taskID string) (bool, error) {
	if r.Chat == nil || taskID == "" {
		return false, nil
	}
	active, err := r.Chat.ActiveTasks(ctx, id.UserID)
	if err != nil {
		return false, fmt.Errorf("isActive: %w", err)
	}
	for _, t := range active {
		if t.ID == taskID {
			return true, nil
		}
	}
	return false, nil
}

// routeReply decides what an inline reply means (step 5; I-L: the bubble
// decides). handled=false → the message goes on to the state machine.
//
//   - a reply to a supporter's relayed words is for the supporter, whatever
//     it says: relayed if the task is active, otherwise "that task has
//     finished" — it never reaches a pending YES/NO;
//   - a bare YES/NO on a bubble we cannot identify while a question is
//     pending is ambiguous and is asked again, never acted on;
//   - a reply to one of our prompts with a question pending is an answer
//     to us (state machine); with nothing pending it names that task (an
//     active one), except a bare YES/NO, which is a late acknowledgement;
//   - anything else: state machine.
func (r *Router) routeReply(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) (handled bool, err error) {
	kind, tid, err := r.bubble(ctx, in, id)
	if err != nil {
		return true, err
	}
	bare := IsYes(text) || IsNo(text)
	switch kind {
	case bubbleRelay:
		if r.Chat == nil {
			return true, r.Reply(ctx, in, CopyRelayGone, "")
		}
		active, err := r.isActive(ctx, id, tid)
		if err != nil {
			return true, err
		}
		if !active {
			return true, r.Reply(ctx, in, CopyRelayGone, "")
		}
		return true, r.relay(ctx, in, id, tid, text)
	case bubbleUnknown:
		if bare && awaitingAnswer(conv.State) {
			return true, r.Reply(ctx, in, CopyAmbiguousYes, "")
		}
		return false, nil
	case bubblePrompt:
		if awaitingAnswer(conv.State) {
			return false, nil
		}
		if bare {
			return true, r.Reply(ctx, in, CopyAck, "")
		}
		active, err := r.isActive(ctx, id, tid)
		if err != nil {
			return true, err
		}
		if active {
			return true, r.relay(ctx, in, id, tid, text)
		}
		return false, nil
	}
	return false, nil
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
				if n := DisplayName(t.SupporterName); n != "" {
					titles[t.ID] += " (" + n + ")"
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
		if n := DisplayName(t.SupporterName); n != "" {
			fmt.Fprintf(&b, " (%s)", n)
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
	// The hold is the durable part. A failed list is logged, not returned:
	// returning it would leave the message unprocessed and the recovery
	// sweep would append the same words to the hold again.
	if err := r.Reply(ctx, in, b.String(), ""); err != nil {
		log.Printf("[channel] askWhichTask identity=%s: list not sent: %v", conv.IdentityID, err)
	}
	return nil
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
		if err := r.releaseHold(ctx, id); err != nil {
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
		taskID := active[0].ID
		if !contains(choices, taskID) {
			// A different supporter than the ones the hold was for: the
			// held words are not theirs. Drop them, say so, and route the
			// new message on its own.
			if err := r.clearHold(ctx, in, conv, CopyHoldGone); err != nil {
				return err
			}
			conv.State, conv.PendingTaskChoices, conv.PendingRelayText = StateIdle, nil, ""
			return r.onIdle(ctx, in, id, conv, text)
		}
		// Deliver first; the hold is cleared only once the supporter has it.
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
		if err := r.releaseHold(ctx, id); err != nil {
			return fmt.Errorf("onAwaitingTaskChoice: %w", err)
		}
		r.noteRelayed(ctx, in, id, taskID, joined)
		return nil
	default:
		return r.askWhichTask(ctx, in, conv, active, joined)
	}
}

// releaseHold clears the hold on a freshly loaded row, so an approval armed
// while the TalkJS call was in flight is kept, not overwritten.
func (r *Router) releaseHold(ctx context.Context, id Identity) error {
	conv, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return err
	}
	conv.PendingTaskChoices, conv.PendingRelayText = nil, ""
	if conv.State == StateAwaitingTaskChoice {
		conv.State = StateIdle
	}
	return r.Conversations.SaveConversation(ctx, conv)
}

func contains(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
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
	who := firstNonEmpty(DisplayName(supporterName), "Your supporter")
	whoLower := firstNonEmpty(DisplayName(supporterName), "your supporter")
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
	case StateAwaitingBudget:
		return fmt.Sprintf(CopyRelayWhileBudget, who)
	}
	return ""
}

// DisplayName is what a supporter's name may look like in HO:RA's own
// voice on a trusted number: one line, letters and ordinary name
// punctuation only, at most 24 characters, and never one that reads as us
// ("HO:RA", "hora support"). Anything else → "" (the caller falls back to
// "your supporter"). The name is the person's own profile field, so this
// is where it is vetted, not where it is typed.
func DisplayName(name string) string {
	name = strings.Join(strings.Fields(name), " ") // one line, single spaces
	for _, c := range name {
		if !(unicode.IsLetter(c) || unicode.IsNumber(c) || c == ' ' || c == '\'' || c == '-' || c == '.') {
			return ""
		}
	}
	if utf8.RuneCountInString(name) > 24 {
		return ""
	}
	if strings.Contains(strings.ToLower(strings.ReplaceAll(name, " ", "")), "hora") {
		return ""
	}
	return name
}
