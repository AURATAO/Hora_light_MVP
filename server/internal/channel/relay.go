package channel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

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
// With a supporter on the job the person's words default to the supporter
// (D-17); a clear new errand still drafts. Relaying moves no money and
// changes no task state.

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

// Relayed is a supporter's relayed line as the thread remembers it.
type Relayed struct {
	Text     string
	TaskID   string
	At       time.Time
	Answered bool // an inbound message followed it
	OK       bool
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
	// LastRelayed: the supporter's most recent relayed message on this
	// thread since the given time, and whether the person has written
	// anything since it (then it is answered) — not counting the message
	// being routed now (excludeProviderID). OK=false when none.
	LastRelayed(ctx context.Context, ch Channel, address string, since time.Time, excludeProviderID string) (Relayed, error)
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
	plain := stripFiller(text) // "oh ok" is as bare as "ok"
	bare := IsYes(plain) || IsNo(plain)
	if conv.State == StateAwaitingApproval {
		bare = approvalAnswerish(stripFiller(text)) // the wider approval words (D-17 R-2/R-3); "oh ok" too
	}
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
		if awaitingAnswer(conv.State) || conv.State == StateAwaitingTaskChoice || tid == "" {
			// A question of ours is open (the list included), or the bubble
			// is not about a task: the state machine decides.
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
	_, err := r.relaySent(ctx, in, id, taskID, text)
	return err
}

// relaySent relays and reports whether the supporter got it; on failure
// the person has already been told.
func (r *Router) relaySent(ctx context.Context, in Inbound, id Identity, taskID, text string) (bool, error) {
	if err := r.Chat.PostAsRequester(ctx, taskID, id.UserID, capText(text), in.ProviderMessageID); err != nil {
		log.Printf("[channel] relay identity=%s task=%s: %v", id.ID, taskID, err)
		if errors.Is(err, ErrTaskNotActive) {
			return false, r.Reply(ctx, in, CopyRelayGone, "")
		}
		return false, r.Reply(ctx, in, CopyRelayFailed, taskID)
	}
	r.noteRelayed(ctx, in, id, taskID, text)
	return true, nil
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
	// A worded reply to the list ("the bagels one, everything"): the
	// context decides, defaulting to the supporter (P2/P7) — the plain
	// classifier's "when in doubt, new_task" has no place under a hold.
	active0, err := r.Chat.ActiveTasks(ctx, id.UserID)
	if err != nil {
		return fmt.Errorf("onAwaitingTaskChoice: %w", err)
	}
	if t, ok := supporterNamed(active0, text); ok && contains(choices, t.ID) {
		// Named the supporter: deliver the hold with these words to them.
		joined := capText(strings.TrimSpace(held + "\n" + text))
		if err := r.Chat.PostAsRequester(ctx, t.ID, id.UserID, joined, in.ProviderMessageID); err != nil {
			log.Printf("[channel] relay identity=%s task=%s: %v", id.ID, t.ID, err)
			if errors.Is(err, ErrTaskNotActive) {
				return r.clearHold(ctx, in, conv, CopyRelayGone)
			}
			return r.Reply(ctx, in, CopyRelayFailedPick, t.ID)
		}
		if err := r.releaseHold(ctx, id); err != nil {
			return fmt.Errorf("onAwaitingTaskChoice: %w", err)
		}
		r.noteRelayed(ctx, in, id, t.ID, joined)
		return nil
	}
	intent, p := r.classifyActive(ctx, id, text, active0, conv)
	if p.Action == llm.ActionEscalate {
		return r.agentHandOff(ctx, in, id, p)
	}
	if p.Action == llm.ActionReply && r.saidToUs(ctx, in, id, text, time.Time{}) {
		// "ok never mind" to our "which supporter?": the held words are
		// dropped, not delivered with it (review 10/25 #7).
		return r.clearHold(ctx, in, conv, CopyHoldDropped)
	}
	if intent == llm.IntentNewTask {
		conv.State, conv.PendingTaskChoices, conv.PendingRelayText = StateIdle, nil, ""
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("onAwaitingTaskChoice: %w", err)
		}
		return r.startDraftWhileActive(ctx, in, id, conv, text, active0)
	}
	all, err := r.Chat.ActiveTasks(ctx, id.UserID)
	if err != nil {
		return fmt.Errorf("onAwaitingTaskChoice: %w", err)
	}
	// The held words were for the tasks on the list they were held for;
	// a task assigned since is not among the candidates.
	var active []ActiveTask
	for _, t := range all {
		if contains(choices, t.ID) {
			active = append(active, t)
		}
	}
	joined := capText(strings.TrimSpace(held + "\n" + text))
	switch len(active) {
	case 0:
		// Every supporter on the list finished meanwhile: the held words
		// have nowhere to go. Say so, and route the new message on its own.
		if err := r.clearHold(ctx, in, conv, CopyHoldGone); err != nil {
			return err
		}
		conv.State, conv.PendingTaskChoices, conv.PendingRelayText = StateIdle, nil, ""
		return r.onIdle(ctx, in, id, conv, text)
	case 1:
		taskID := active[0].ID
		// Deliver first; the hold is cleared only once the supporter has it.
		if err := r.Chat.PostAsRequester(ctx, taskID, id.UserID, joined, in.ProviderMessageID); err != nil {
			log.Printf("[channel] relay identity=%s task=%s: %v", id.ID, taskID, err)
			if errors.Is(err, ErrTaskNotActive) {
				return r.clearHold(ctx, in, conv, CopyRelayGone)
			}
			// Keep the grown hold, on a fresh row (an approval may have
			// been armed meanwhile).
			if err := r.updateHold(ctx, id, joined); err != nil {
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

// updateHold rewrites the held text on a freshly loaded row.
func (r *Router) updateHold(ctx context.Context, id Identity, text string) error {
	conv, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return err
	}
	conv.PendingRelayText = text
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
	} else if note := r.pendingNote(conv, whoLower, t.Identity.Channel); note != "" {
		b.WriteString("\n\n" + note)
	}
	out := Outbound{Channel: t.Identity.Channel, To: t.Identity.Address, Text: b.String(), MediaURL: mediaURL, Relay: true}
	return r.Send(ctx, out, taskID)
}

// pendingNote states what a bare YES/NO answers while one is pending; ""
// when nothing is.
func (r *Router) pendingNote(conv Conversation, who string, ch Channel) string {
	im := ch == IMessage // no reply context there (D-17 P5)
	switch conv.State {
	case StateAwaitingConfirm:
		if conv.Pending == nil {
			return ""
		}
		if im {
			return fmt.Sprintf(CopyRelayWhileConfirmIM, conv.Pending.Headline(), Dollars(conv.Pending.TotalCents()), who)
		}
		return fmt.Sprintf(CopyRelayWhileConfirm, conv.Pending.Headline(), Dollars(conv.Pending.TotalCents()), who)
	case StateAwaitingApproval:
		if conv.Unconfirmed() {
			return "" // a question not yet confirmed delivered is not described
		}
		if im {
			return fmt.Sprintf(CopyRelayWhileApprovalIM, who)
		}
		return fmt.Sprintf(CopyRelayWhileApproval, who)
	case StateAwaitingBudget:
		if im {
			return fmt.Sprintf(CopyRelayWhileBudgetIM, who)
		}
		return fmt.Sprintf(CopyRelayWhileBudget, who)
	}
	return ""
}

// DisplayName is what a supporter's name may look like in HO:RA's own
// voice on a trusted number. The name is the person's own profile field, so
// this is where it is vetted, not where it is typed. Allowed: one line of
// letters (one script — no Latin/Cyrillic mixing), spaces, ' and -, at most
// 24 characters, NFKC-normalised (fullwidth letters fold to ASCII). Refused:
// digits, punctuation, anything whose confusable-folded letters contain a
// reserved word (hora, admin, support, staff, official, system, billing,
// security, team, help). Refused → "" and the caller says "your supporter".
func DisplayName(name string) string {
	name = norm.NFKC.String(name)
	name = strings.Join(strings.Fields(name), " ") // one line, single spaces
	if name == "" || utf8.RuneCountInString(name) > 24 {
		return ""
	}
	var folded strings.Builder
	latin, other := false, false
	for _, c := range name {
		switch {
		case c == ' ' || c == '\'' || c == '-':
			continue
		case !unicode.IsLetter(c):
			return ""
		}
		if unicode.Is(unicode.Latin, c) {
			latin = true
		} else {
			other = true
		}
		folded.WriteRune(foldConfusable(unicode.ToLower(c)))
	}
	if latin && other {
		return "" // mixed scripts: the classic look-alike trick
	}
	f := folded.String()
	for _, w := range reservedNameWords {
		if strings.Contains(f, w) {
			return ""
		}
	}
	return name
}

var reservedNameWords = []string{"hora", "admin", "support", "staff", "official", "system", "billing", "security", "team", "help"}

// foldConfusable maps the common Cyrillic and Greek look-alikes of Latin
// letters onto them, for the reserved-word check only.
func foldConfusable(c rune) rune {
	switch c {
	case 'а', 'α':
		return 'a'
	case 'е', 'ε':
		return 'e'
	case 'о', 'ο':
		return 'o'
	case 'р', 'ρ':
		return 'p'
	case 'с':
		return 'c'
	case 'х', 'χ':
		return 'x'
	case 'у', 'γ':
		return 'y'
	case 'н', 'η':
		return 'h'
	case 'і', 'ι':
		return 'i'
	case 'т', 'τ':
		return 't'
	case 'к', 'κ':
		return 'k'
	case 'м':
		return 'm'
	case 'в':
		return 'b'
	}
	return c
}

// FirstName is the first word of a full name — what the relay prefix and
// the status lines show.
func FirstName(name string) string {
	f := strings.Fields(name)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// ── D-17: context decides ──────────────────────────────────────────────────

// supporterQuestionWindow: how recently the supporter must have asked for
// a short reply to be taken as the answer without consulting the model.
// supporterContextWindow: how far back the model is shown the supporter's
// last line.
const (
	supporterQuestionWindow = 10 * time.Minute
	supporterContextWindow  = 2 * time.Hour
)

var (
	// "for Maya" / "for my supporter" / "tell Sam": the words were for them
	// (D-17 P6). The name is checked against the active supporters.
	forSupporterRE = regexp.MustCompile(`(?is)^\s*(?:for|tell)\s+(my\s+supporter|[\p{L}'-]+)\s*[:,.!]?\s*(.*)$`)
	// "new task: …" / "another errand …": force a draft.
	newTaskRE = regexp.MustCompile(`(?is)^\s*(?:new\s+(?:task|errand|request)|another\s+(?:task|errand|request))\b[\s:,-]*(.*)$`)
)

// openQuestion names the question of ours the thread is waiting on, for the
// classifier's context.
func openQuestion(s State) string {
	switch s {
	case StateAwaitingConfirm:
		return "booking"
	case StateAwaitingBudget:
		return "budget"
	case StateAwaitingApproval:
		return "approval"
	case StateAwaitingName:
		return "name"
	}
	return ""
}

// routeInContext is D-17 step 6–7: with a supporter on the job, decide
// whether the message is for them. handled=false → the caller's own
// routing (intent classification, correction, re-ask) continues.
func (r *Router) routeInContext(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) (handled bool, err error) {
	handled, _, err = r.routeInContextLabel(ctx, in, id, conv, text)
	return handled, err
}

// routeInContextLabel is routeInContext that also returns the label the
// message got when it was not handled ("" when no label was taken), so the
// caller can re-ask instead of re-drafting an answer-in-words.
func (r *Router) routeInContextLabel(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) (handled bool, label llm.Intent, err error) {
	if r.Chat == nil || !id.Linked() {
		return false, "", nil
	}
	active, err := r.Chat.ActiveTasks(ctx, id.UserID)
	if err != nil {
		return true, "", fmt.Errorf("routeInContext: %w", err)
	}
	// Step 6: the one-word ways out — except that a pending approval is
	// answered first (P4): "new task:" then is re-asked, "for Maya" relays
	// but keeps the question armed.
	if m := newTaskRE.FindStringSubmatch(text); m != nil && strings.TrimSpace(m[1]) != "" {
		if conv.State == StateAwaitingApproval {
			return true, "", r.Reply(ctx, in, CopyApprovalFirst, "")
		}
		return true, "", r.startDraft(ctx, in, id, conv, strings.TrimSpace(m[1]))
	}
	if m := forSupporterRE.FindStringSubmatch(text); m != nil {
		if t, ok := supporterByName(active, m[1]); ok {
			words := strings.TrimSpace(m[2])
			dropDraft := false
			if words == "" && conv.Pending != nil {
				// "for Maya" alone: the draft's words were for her.
				words, dropDraft = conv.Pending.Source, true
			}
			if words == "" {
				return true, "", r.Reply(ctx, in, CopyWhichTaskNothingHeld, "")
			}
			if !dropDraft {
				// "tell Sam I'm 5 min late": the words are new; whatever
				// draft or question is open stays as it is.
				return true, "", r.relayKeepingDraft(ctx, in, id, conv, t.ID, words)
			}
			// Deliver first; the draft is dropped only once the supporter
			// has the words (a failed relay keeps it, and the person has
			// been told). A pending approval stays armed.
			sent, err := r.relaySent(ctx, in, id, t.ID, words)
			if err != nil || !sent {
				return true, "", err
			}
			fresh, err := r.Conversations.GetConversation(ctx, id.ID)
			if err != nil {
				return true, "", fmt.Errorf("routeInContext: %w", err)
			}
			fresh.Pending = nil
			if fresh.State != StateAwaitingApproval {
				fresh.State = fresh.ResumeState()
			}
			if err := r.Conversations.SaveConversation(ctx, fresh); err != nil {
				return true, "", fmt.Errorf("routeInContext: %w", err)
			}
			reply := fmt.Sprintf(CopyRelayedDraftDropped, firstNonEmpty(DisplayName(t.SupporterName), "your supporter"))
			if fresh.State == StateAwaitingApproval {
				reply += "\n\n" + approvalStillPending(fresh)
			}
			return true, "", r.Reply(ctx, in, reply, t.ID)
		}
	}
	if len(active) == 0 {
		return false, "", nil
	}
	// The supporter's last line counts only while that task is active and
	// the person has not answered it yet.
	last, lastTask, lastAt, answered := r.lastSupporterLine(ctx, in, id)
	if _, ok := taskIn(active, lastTask); !ok {
		last, lastTask = "", ""
	}
	// Step 7a (idle only): the supporter just asked, this is a short reply,
	// and nothing has been said since. Mid-question the model decides, with
	// the open question in view (a correction to our draft is not an answer
	// to the supporter).
	if conv.State == StateIdle && supporterQuestionOpen(last, lastAt, answered, r.now()) && len(strings.Fields(text)) <= 12 {
		return true, "", r.relayKeepingDraft(ctx, in, id, conv, lastTask, text)
	}
	// The one who last wrote counts only until the person answered — as a
	// target and as context.
	if answered {
		last, lastTask = "", ""
	}
	// Step 7b: the model, with the context. Error → to the supporter (P7).
	// Without a context classifier, the plain one decides only "new
	// errand or not"; everything else is for the supporter.
	intent := llm.IntentToSupporter
	agentTarget, agentAsks := "", false
	switch {
	case r.Agent != nil:
		// D-18: the agent proposes; the rules above have already run, and
		// nothing below moves money or time.
		p := r.propose(ctx, id, text, active, conv, last, lastTask, lastAt, answered)
		if p.Action == llm.ActionEscalate {
			return true, "", r.agentHandOff(ctx, in, id, p)
		}
		if p.Action == llm.ActionReply && r.saidToUs(ctx, in, id, text, lastAt) {
			// A short acknowledgement of our own last line (staging
			// 2026-10-25: "Ok never mind" after "cancel it in the app" went
			// to the supporter): answered by us. Anything longer, or with
			// the supporter the more recent voice, still goes to the
			// supporter — a wrong "reply" must never swallow words meant
			// for a person (P7). An open question of ours is said again.
			return true, llm.IntentOther, r.Reply(ctx, in, r.ackWithQuestion(conv), "")
		}
		intent = llm.ToIntent(p)
		if p.Task >= 0 && p.Task < len(active) {
			agentTarget = active[p.Task].ID
		}
		agentAsks = p.Action == llm.ActionAsk
	case r.InContext == nil:
		if r.Classifier != nil {
			if got, err := r.Classifier.Classify(ctx, text); err == nil && got == llm.IntentNewTask {
				intent = llm.IntentNewTask
			}
		}
	default:
		c := llm.Context{LastSupporterLine: last, OpenQuestion: openQuestion(conv.State)}
		for _, t := range active {
			c.ActiveTasks = append(c.ActiveTasks, llm.ContextTask{Title: t.Title, SupporterName: DisplayName(FirstName(t.SupporterName))})
		}
		got, err := r.InContext.ClassifyInContext(ctx, text, c)
		if err != nil {
			log.Printf("[channel] classify in context (using %q): %v", got, err)
		}
		intent = got
	}
	switch intent {
	case llm.IntentCorrection, llm.IntentAnswer:
		return false, intent, nil // the caller's own path: re-draft / re-ask for a plain YES/NO
	case llm.IntentNewTask:
		if conv.State != StateIdle {
			return false, intent, nil // a correction / re-ask: the caller's path
		}
		// Step 7b → draft, saying so (P6).
		return true, intent, r.startDraftWhileActive(ctx, in, id, conv, text, active)
	default:
		// Which supporter: one named in the message, else the one who last
		// wrote, else the only one; several and no signal → the list
		// (idle), or a line naming the way out (mid-question, where the
		// list cannot hold).
		// With the agent (D-18): a supporter named in the words still wins;
		// then the errand the agent named; an agent that cannot tell skips
		// the "who last wrote" guess.
		target := ""
		if t, ok := supporterNamed(active, text); ok {
			target = t.ID
		} else if agentTarget != "" {
			target = agentTarget
		} else if lastTask != "" && !agentAsks {
			target = lastTask
		} else if len(active) == 1 {
			target = active[0].ID
		}
		if target == "" {
			if conv.State != StateIdle {
				return true, intent, r.Reply(ctx, in, fmt.Sprintf(CopyWhichSupporterMidQuestion, supporterForms(active)), "")
			}
			return true, intent, r.askWhichTask(ctx, in, conv, active, text)
		}
		return true, intent, r.relayKeepingDraft(ctx, in, id, conv, target, text)
	}
}

// supporterQuestionOpen: the supporter asked something recently and the
// person has not written since.
func supporterQuestionOpen(last string, lastAt time.Time, answered bool, now time.Time) bool {
	return last != "" && !answered && strings.HasSuffix(strings.TrimSpace(last), "?") && now.Sub(lastAt) <= supporterQuestionWindow
}

// answerToSupporter: a bare YES/NO in idle that answers an open supporter
// question (F2) — relayed; handled=false when there is none.
func (r *Router) answerToSupporter(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) (bool, error) {
	if r.Chat == nil || !id.Linked() {
		return false, nil
	}
	active, err := r.Chat.ActiveTasks(ctx, id.UserID)
	if err != nil {
		return true, fmt.Errorf("answerToSupporter: %w", err)
	}
	last, lastTask, lastAt, answered := r.lastSupporterLine(ctx, in, id)
	if _, ok := taskIn(active, lastTask); !ok || !supporterQuestionOpen(last, lastAt, answered, r.now()) {
		return false, nil
	}
	return true, r.relayKeepingDraft(ctx, in, id, conv, lastTask, text)
}

func taskIn(active []ActiveTask, id string) (ActiveTask, bool) {
	for _, t := range active {
		if t.ID == id && id != "" {
			return t, true
		}
	}
	return ActiveTask{}, false
}

// supporterNamed: the message mentions exactly one active supporter's
// first name ("tell Sam…", "Maya can wait").
func supporterNamed(active []ActiveTask, text string) (ActiveTask, bool) {
	var found ActiveTask
	n := 0
	for _, t := range active {
		name := DisplayName(FirstName(t.SupporterName))
		if name == "" {
			continue
		}
		for _, w := range strings.FieldsFunc(text, func(c rune) bool { return !unicode.IsLetter(c) && c != '\'' && c != '-' }) {
			// Case-sensitive: "Will" the supporter, not "I will be there".
			if w == name {
				found, n = t, n+1
				break
			}
		}
	}
	return found, n == 1
}

// supporterForms renders the way out for copy: “for Maya” or “for Sam”.
func supporterForms(active []ActiveTask) string {
	var forms []string
	for _, t := range active {
		if n := DisplayName(FirstName(t.SupporterName)); n != "" {
			forms = append(forms, "\u201cfor "+n+"\u201d")
		}
	}
	if len(forms) == 0 {
		return "\u201cfor my supporter\u201d"
	}
	return strings.Join(forms, " or ")
}

// relayKeepingDraft relays and, if a question of ours is open, says in one
// line that it still is.
func (r *Router) relayKeepingDraft(ctx context.Context, in Inbound, id Identity, conv Conversation, taskID, text string) error {
	sent, err := r.relaySent(ctx, in, id, taskID, text)
	if err != nil || !sent {
		return err
	}
	switch conv.State {
	case StateAwaitingConfirm:
		if conv.Pending != nil {
			return r.Reply(ctx, in, CopyDraftStillPending, "")
		}
	case StateAwaitingBudget:
		if conv.Pending != nil {
			return r.Reply(ctx, in, CopyBudgetStillPending, "")
		}
	case StateAwaitingApproval:
		return r.Reply(ctx, in, approvalStillPending(conv), "")
	}
	return nil
}

func approvalStillPending(conv Conversation) string {
	if conv.Unconfirmed() {
		return CopyApprovalOnItsWay
	}
	return CopyApprovalStillPending
}

// startDraftWhileActive drafts a new errand while a supporter is on the
// job and marks the draft so the summary offers the way out.
func (r *Router) startDraftWhileActive(ctx context.Context, in Inbound, id Identity, conv Conversation, text string, active []ActiveTask) error {
	return r.startDraftOpts(ctx, in, id, conv, text, supporterForms(active))
}

func supporterByName(active []ActiveTask, name string) (ActiveTask, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "my supporter" {
		if len(active) == 1 {
			return active[0], true
		}
		return ActiveTask{}, false
	}
	var found ActiveTask
	hits := 0
	for _, t := range active {
		if strings.EqualFold(FirstName(t.SupporterName), n) {
			found, hits = t, hits+1
		}
	}
	return found, hits == 1
}

// lastSupporterLine: the supporter's most recent relayed message on this
// thread within the window, with its task and time.
func (r *Router) lastSupporterLine(ctx context.Context, in Inbound, id Identity) (text, taskID string, at time.Time, answered bool) {
	text, taskID, at, answered, err := r.lastSupporterLineErr(ctx, in, id)
	if err != nil {
		log.Printf("[channel] last relayed identity=%s: %v", id.ID, err)
	}
	return text, taskID, at, answered
}

// lastSupporterLineErr is lastSupporterLine with the lookup error, for the
// one caller where a miss must fail closed (I-P).
func (r *Router) lastSupporterLineErr(ctx context.Context, in Inbound, id Identity) (text, taskID string, at time.Time, answered bool, err error) {
	if r.Tasks == nil {
		return "", "", time.Time{}, false, nil
	}
	since := r.now().Add(-supporterContextWindow)
	rl, err := r.Tasks.LastRelayed(ctx, in.Channel, in.From, since, in.ProviderMessageID)
	if err != nil {
		return "", "", time.Time{}, false, fmt.Errorf("last relayed: %w", err)
	}
	if !rl.OK {
		return "", "", time.Time{}, false, nil
	}
	text, taskID, at, answered = rl.Text, rl.TaskID, rl.At, rl.Answered
	// The stored line is "Maya · Task: words[\n\n(footer)]"; hand the model
	// the words only.
	if i := strings.Index(text, ": "); i >= 0 {
		text = text[i+2:]
	}
	if i := strings.Index(text, "\n\n("); i >= 0 {
		text = text[:i]
	}
	return strings.TrimSpace(text), taskID, at, answered, nil
}

// classifyActive labels a message with the context classifier (or, without
// one, the plain classifier for "new errand or not"); any failure is
// "to the supporter" (P7).
func (r *Router) classifyActive(ctx context.Context, id Identity, text string, active []ActiveTask, conv Conversation) (llm.Intent, llm.Proposal) {
	if r.Agent != nil {
		// "New errand or not" is read here (a held relay's choice list); a
		// target is not acted on mid-choice. An escalation is returned to
		// the caller, which hands off — a safety message must not be joined
		// to the hold and delivered to the supporter (D-18 review r1).
		p := r.propose(ctx, id, text, active, conv, "", "", time.Time{}, false)
		return llm.ToIntent(p), p
	}
	if r.InContext == nil {
		if r.Classifier != nil {
			if got, err := r.Classifier.Classify(ctx, text); err == nil && got == llm.IntentNewTask {
				return llm.IntentNewTask, llm.SafeProposal()
			}
		}
		return llm.IntentToSupporter, llm.SafeProposal()
	}
	c := llm.Context{OpenQuestion: openQuestion(conv.State)}
	for _, t := range active {
		c.ActiveTasks = append(c.ActiveTasks, llm.ContextTask{Title: t.Title, SupporterName: DisplayName(FirstName(t.SupporterName))})
	}
	got, err := r.InContext.ClassifyInContext(ctx, text, c)
	if err != nil {
		log.Printf("[channel] classify in context (using %q): %v", got, err)
	}
	return got, llm.SafeProposal()
}

// casualToSupporter (D-17 I-P, version B): a casual affirmative while a
// question of ours is open goes to the supporter who wrote in the last
// minutes — and the person is told the question still waits. handled=false
// when no supporter wrote recently (the word then answers us, as before).
func (r *Router) casualToSupporter(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) (bool, error) {
	if r.Chat == nil || !id.Linked() {
		return false, nil
	}
	// A reply swiped on one of our own prompts is an answer to us (I-L) —
	// only where reply context is real (WhatsApp; Sendblue's is ignored at
	// ingest, P5/I-N).
	if in.Channel == WhatsApp {
		if kind, _, err := r.bubble(ctx, in, id); err != nil {
			return true, err
		} else if kind == bubblePrompt {
			return false, nil
		}
	}
	last, lastTask, lastAt, _, err := r.lastSupporterLineErr(ctx, in, id)
	if err != nil {
		return true, err // fail closed: the word must not answer us by accident
	}
	if last == "" || r.now().Sub(lastAt) > supporterQuestionWindow {
		return false, nil
	}
	active, err := r.Chat.ActiveTasks(ctx, id.UserID)
	if err != nil {
		return true, fmt.Errorf("casualToSupporter: %w", err)
	}
	t, ok := taskIn(active, lastTask)
	if !ok {
		return false, nil
	}
	sent, err := r.relaySent(ctx, in, id, t.ID, text)
	if err != nil {
		return true, err
	}
	if !sent {
		// Told it failed; the question still waits.
		return true, r.Reply(ctx, in, stillWaiting(conv), "")
	}
	who := firstNonEmpty(DisplayName(FirstName(t.SupporterName)), "your supporter")
	return true, r.Reply(ctx, in, fmt.Sprintf(CopyPassedOn, strings.TrimSpace(text), who)+" "+stillWaiting(conv), "")
}

// stillWaiting names what a plain YES/NO (or amount) still answers.
func stillWaiting(conv Conversation) string {
	switch conv.State {
	case StateAwaitingConfirm:
		if conv.Pending != nil {
			return fmt.Sprintf(CopyStillWaitingBooking, conv.Pending.Headline(), Dollars(conv.Pending.TotalCents()))
		}
	case StateAwaitingApproval:
		if conv.Unconfirmed() {
			return CopyApprovalOnItsWay
		}
		return CopyStillWaitingApproval
	case StateAwaitingBudget:
		return CopyStillWaitingBudget
	case StateAwaitingName:
		return CopyStillWaitingName
	case StateAwaitingSetup:
		return CopyStillWaitingSetup
	case StateAwaitingCancel:
		if pc := conv.PendingCancel; pc != nil {
			switch {
			case pc.TaskID == "":
				return CopyStillWaitingCancelChoice
			case pc.ReasonCode == "":
				return fmt.Sprintf(CopyStillWaitingCancelReason, pc.Title)
			}
			return fmt.Sprintf(CopyStillWaitingCancel, pc.Title)
		}
	}
	return CopyStillWaitingGeneric
}

// propose asks the requester agent (D-18) with the thread's context: the
// active errands (t1, t2… — no ids), our open question, and the
// supporter's last line. Any failure is the safe proposal: for the
// supporter, no target (D-17 P7). The person's words and the proposal's
// fields are never logged beyond the action.
func (r *Router) propose(ctx context.Context, id Identity, text string, active []ActiveTask, conv Conversation, last, lastTask string, lastAt time.Time, answered bool) llm.Proposal {
	minutes := 0
	if !lastAt.IsZero() {
		minutes = int(r.now().Sub(lastAt).Minutes())
	}
	c := AgentContextFor(active, openQuestion(conv.State), SupporterLine{Text: last, TaskID: lastTask, MinutesAgo: minutes, Answered: answered}, r.ourLastLine(ctx, id))
	p, err := r.Agent.Propose(ctx, text, c)
	if err != nil {
		log.Printf("[channel] agent (using %s): %v", llm.SafeProposal().Action, err)
		return llm.SafeProposal()
	}
	log.Printf("[channel] agent identity=%s action=%s", id.ID, p.Action)
	return p
}

// saidToUs: the premise of a "reply" the router accepts — HO:RA spoke more
// recently than the supporter, and the message is short (an
// acknowledgement, a withdrawal), so nothing a supporter needs is lost.
func (r *Router) saidToUs(ctx context.Context, in Inbound, id Identity, text string, supporterAt time.Time) bool {
	if len(strings.Fields(text)) > 5 && len([]rune(text)) > 12 {
		return false
	}
	ours := r.ourLastLineAt(ctx, id)
	if ours.IsZero() {
		return false
	}
	return supporterAt.IsZero() || ours.After(supporterAt)
}

// ackWithQuestion: the acknowledgement, plus the question of ours that is
// still open (never dropped behind an ack).
func (r *Router) ackWithQuestion(conv Conversation) string {
	if awaitingAnswer(conv.State) || conv.State == StateAwaitingTaskChoice {
		return CopyAck + " " + stillWaiting(conv)
	}
	return CopyAck
}

// ourLastLineAt: when HO:RA last wrote (zero when unknown).
func (r *Router) ourLastLineAt(ctx context.Context, id Identity) time.Time {
	_, at := r.ourLastLineText(ctx, id)
	return at
}

// ourLastLine: HO:RA's latest message in the thread (text and age), for
// the agent's context; empty when none or unreadable.
func (r *Router) ourLastLine(ctx context.Context, id Identity) SupporterLine {
	text, at := r.ourLastLineText(ctx, id)
	if text == "" {
		return SupporterLine{}
	}
	minutes := 0
	if !at.IsZero() {
		minutes = int(r.now().Sub(at).Minutes())
	}
	return SupporterLine{Text: text, MinutesAgo: minutes}
}

// relayedLineRE: a relayed supporter line as the thread stores it —
// "Name · Task: words" at the start (relay.go). Anchored, so our own
// booking summary ("… · about 30 min … Terms: …") is not mistaken for one.
var relayedLineRE = regexp.MustCompile(`^[^\n:]{1,60} · [^\n:]{1,120}: `)

func (r *Router) ourLastLineText(ctx context.Context, id Identity) (string, time.Time) {
	if r.Thread == nil {
		return "", time.Time{}
	}
	msgs, err := r.Thread.RecentMessages(ctx, id.Channel, id.Address, 6)
	if err != nil {
		log.Printf("[channel] our last line identity=%s: %v", id.ID, err)
		return "", time.Time{}
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Direction != "outbound" || strings.TrimSpace(m.Text) == "" {
			continue
		}
		if m.Relay || relayedLineRE.MatchString(m.Text) {
			continue // a relayed supporter line (the store's flag; the shape as a fallback), not ours
		}
		return m.Text, m.At
	}
	return "", time.Time{}
}

// agentHandOff hands the thread to ops on an agent escalation (D-18). The
// agent call took seconds: the conversation read before it may be stale (a
// YES may have booked meanwhile, an approval may have been armed), so it is
// read again and the hand-off saves THAT — never the snapshot from before
// the call (review r1). While a booking is being posted the thread is not
// taken over: ops are notified, the person is told the booking is in
// flight, and the words are not relayed (they may be about the supporter).
func (r *Router) agentHandOff(ctx context.Context, in Inbound, id Identity, p llm.Proposal) error {
	fresh, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return fmt.Errorf("agentHandOff: %w", err)
	}
	log.Printf("[channel] agent escalation identity=%s reason=%s state=%s", id.ID, p.Escalation, fresh.State)
	switch fresh.State {
	case StatePosting:
		// Not taken over mid-booking, but ops are told now — a safety or
		// dispute message must not be lost (review r2).
		if err := r.notifyOps(ctx, id, HandoffAgentEscalated, false, handoffOpen{detail: string(p.Escalation)}); err != nil {
			// Said, not swallowed (review 10/24 #7): the message did not
			// reach a person.
			log.Printf("[channel] agent escalation identity=%s while posting: %v", id.ID, err)
			return r.Reply(ctx, in, CopyBookingInFlight+" "+CopyHandoffFailed, "")
		}
		return r.Reply(ctx, in, CopyBookingInFlight, "")
	case StateHandoff:
		return r.onHandoff(ctx, in, id)
	}
	return r.handOffDetail(ctx, in, id, fresh, HandoffAgentEscalated, string(p.Escalation))
}

// SupporterLine is the supporter's last relayed line as the router knows it.
type SupporterLine struct {
	Text       string
	TaskID     string
	MinutesAgo int
	Answered   bool
}

// AgentContextFor is the ONE place the agent's context is assembled — the
// router (propose) and the test runner (cmd/agenttest) both call it, so
// what the model is tested on is what it sees (D-18 review r2). An empty
// or answered line is not shown (the person already replied to it).
func AgentContextFor(active []ActiveTask, openQuestion string, line SupporterLine, ours ...SupporterLine) llm.AgentContext {
	c := llm.AgentContext{OpenQuestion: openQuestion}
	if len(ours) > 0 && ours[0].Text != "" {
		c.HoraLine = &llm.AgentLine{Task: -1, Text: ours[0].Text, MinutesAgo: ours[0].MinutesAgo}
	}
	for _, t := range active {
		c.Tasks = append(c.Tasks, llm.AgentTask{Title: t.Title, Supporter: DisplayName(FirstName(t.SupporterName))})
	}
	if line.Text != "" && !line.Answered {
		l := &llm.AgentLine{Task: -1, Text: line.Text, MinutesAgo: line.MinutesAgo}
		for i, t := range active {
			if t.ID == line.TaskID {
				l.Task = i
			}
		}
		c.SupporterLine = l
	}
	return c
}

// newErrandRE: the person says outright this is a NEW errand — "book
// another", "a new task", "another errand". Read by rule while a question
// of ours waits (Arbiter 2026-10-24: approval first, then the draft, then
// messages).
var newErrandRE = regexp.MustCompile(`(?i)\b(book (another|a new|one more)|another (task|errand|job|order)|new (task|errand|job|order)|one more (task|errand))\b|再(開|开|訂|订|叫)一|新的?(任務|任务|單|单)|另一個(任務|單)`)

// errandRE: words that describe an errand to do — a purchase (purchaseRE)
// or one of these. Weaker than newErrandRE: "pick up the kids at 5" may be
// for the supporter on the job, so it decides only when no supporter has
// written within supporterContextWindow (review 10/24 #5); otherwise the
// classifier / agent, which sees the supporter's line, decides.
var errandRE = regexp.MustCompile(`(?i)\b(pick up|drop off|deliver|need someone|can someone|i want (you|someone) to|walk my|wait in line)\b|幫我|帮我`)

// errandForDraft: while a draft or an approval question waits, this
// message describes an errand for US — not words for a supporter: it
// names no supporter and answers no question a supporter asked; a weak
// errand reading also needs no supporter line within
// supporterContextWindow.
func (r *Router) errandForDraft(ctx context.Context, in Inbound, id Identity, text string) bool {
	strong := newErrandRE.MatchString(text)
	if !strong && !errandRE.MatchString(text) && !LooksLikePurchase(text) {
		return false
	}
	if forSupporterRE.MatchString(text) {
		return false
	}
	if r.Chat != nil && id.Linked() {
		active, err := r.Chat.ActiveTasks(ctx, id.UserID)
		if err != nil {
			return false // cannot tell: the usual routing decides
		}
		if _, named := supporterNamed(active, text); named {
			return false
		}
		last, lastTask, lastAt, answered, err := r.lastSupporterLineErr(ctx, in, id)
		if err != nil {
			log.Printf("[channel] errand rule identity=%s: %v", id.ID, err)
			return false // cannot tell who spoke: the usual routing decides (P7)
		}
		if _, ok := taskIn(active, lastTask); ok {
			if supporterQuestionOpen(last, lastAt, answered, r.now()) {
				return false
			}
			if !strong && !lastAt.IsZero() && r.now().Sub(lastAt) <= supporterContextWindow {
				return false // a supporter is in the conversation: the model decides
			}
		}
	}
	return true
}
