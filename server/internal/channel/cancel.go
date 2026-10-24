package channel

// Cancel a posted task by text (D-17, Arbiter 2026-10-24): recognised by
// rule, confirmed once with the charge the app's own cancellation policy
// quotes, carried out through the app's own cancel path. The YES is consent
// to the amount quoted: if the charge has gone up by the time it arrives (the
// grace window ended, the supporter clocked more time), the new amount is
// asked again — never charged silently. A charge that is moving right now
// (a work session running, the free window about to close) is not quoted at
// all: the app shows it live.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// cancelMentionRE: the person mentions cancelling at all — "I think I need
// to cancel the task thanks", "cancel", 取消. Arbiter 2026-10-24 23:03
// (staging: that sentence was relayed to the supporter): anything that
// mentions cancel is double-checked with us — the reason, then the charge —
// never passed on. Only an explicit negation ("don't cancel", 不要取消) is
// not a mention.
var cancelMentionRE = regexp.MustCompile(`(?i)\bcancel(?:l?ed|l?ing|s)?\b|\bcall(?:ed|ing)? (?:it |that |the \w+ )?off\b|取消`)

// cancelNegatedRE: a negated mention — the negator right before "cancel",
// or "don't want/need to cancel". Never a wider gap: "I can't make it so
// cancel" is a cancel (review r2).
var cancelNegatedRE = regexp.MustCompile(`(?i)\b(?:don'?t|do not|not|never|no need to|without|can'?t|cannot|won'?t|didn'?t)\s+cancel|\b(?:don'?t|do not|didn'?t|never)\s+(?:want|need|mean|plan|intend)(?:ed)?\s+to\s+cancel|(?:不要|不用|不需要|別|别|沒有|没有|沒要|不是要?|不想)取消`)

// IsCancelRequest reports a message about cancelling, by rule (never the
// model). Broad on purpose: the question that follows is the check. A
// negated mention ("don't cancel", "I don't want to cancel") is not one —
// unless the message also mentions cancelling un-negated ("cancel the
// laundry one, don't cancel the bagel one").
func IsCancelRequest(text string) bool {
	t := strings.ReplaceAll(text, "’", "'")
	mentions := len(cancelMentionRE.FindAllStringIndex(t, -1))
	negated := len(cancelNegatedRE.FindAllStringIndex(t, -1))
	return mentions > negated
}

// isNegatedCancel: the message says NOT to cancel and nothing else about
// cancelling ("no, I don't need to cancel it") — the reason step's NO.
func isNegatedCancel(text string) bool {
	t := strings.ReplaceAll(text, "’", "'")
	negated := len(cancelNegatedRE.FindAllStringIndex(t, -1))
	return negated > 0 && len(cancelMentionRE.FindAllStringIndex(t, -1)) <= negated
}

// CancelReasonChoice is one of the app's cancel reasons, as the chat lists
// them. The codes and labels are the app's own closed set
// (server/cancel_reasons.go — a parity test keeps them equal), so the
// supporter is told exactly what an app cancel with that reason tells them,
// and "Other" relays nothing.
type CancelReasonChoice struct {
	Code  string
	Label string
	// words: how a person says it in their own words (lower-case
	// substrings); a number always works.
	words []string
}

var CancelReasonChoices = []CancelReasonChoice{
	{"plans_changed", "Plans changed", []string{"plans changed", "plan changed", "changed my mind", "change of plan", "計畫", "计划", "改變主意", "改变主意"}},
	{"no_longer_needed", "No longer needed", []string{"no longer need", "no longer needed", "don't need", "dont need", "not needed", "不需要"}},
	{"found_another_way", "Found another way", []string{"another way", "someone else", "do it myself", "found someone", "找到別人", "自己來", "自己去"}},
	{"wrong_details", "Posted with wrong details", []string{"wrong detail", "wrong details", "wrong address", "wrong time", "wrong info", "wrong place", "details were wrong", "mistake in", "錯誤", "寫錯", "弄錯"}},
	{"posted_by_mistake", "Posted by mistake", []string{"by mistake", "by accident", "a mistake", "accident", "didn't mean", "dont mean", "didnt mean", "不小心", "誤發", "誤"}},
	{"timing_no_longer_works", "Timing no longer works", []string{"timing", "too late", "too long", "taking too", "時間不", "时间不", "太久", "來不及"}},
	{"other", "Other", []string{"something else", "其他", "別的"}},
}

// cancelReasonList renders the numbered reasons.
func cancelReasonList() string {
	var b strings.Builder
	for i, c := range CancelReasonChoices {
		fmt.Fprintf(&b, "\n%d. %s", i+1, c.Label)
	}
	return b.String()
}

// pickCancelReason reads an answer to the reason question: a number, or
// the reason in the person's own words. "" when nothing matched.
func pickCancelReason(text string) string {
	t := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(text, "’", "'")))
	if n, err := strconv.Atoi(strings.Trim(t, ".#)")); err == nil && n >= 1 && n <= len(CancelReasonChoices) {
		return CancelReasonChoices[n-1].Code
	}
	// The label itself, as listed (or "other" alone).
	for _, c := range CancelReasonChoices {
		if strings.Trim(t, " .!") == strings.ToLower(c.Label) {
			return c.Code
		}
	}
	for _, c := range CancelReasonChoices {
		for _, w := range c.words {
			if wordIn(t, w) {
				return c.Code
			}
		}
	}
	return ""
}

// wordIn: the phrase as whole words (Han phrases as substrings — no word
// boundaries in Chinese). "found a mistake" must not read as "found a…".
func wordIn(text, phrase string) bool {
	if phrase == "" {
		return false
	}
	if r := []rune(phrase)[0]; r > 0x2E80 {
		return strings.Contains(text, phrase)
	}
	i := 0
	for {
		j := strings.Index(text[i:], phrase)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(phrase)
		before := start == 0 || !isWordByte(text[start-1])
		after := end == len(text) || !isWordByte(text[end])
		if before && after {
			return true
		}
		i = start + 1
	}
}

func isWordByte(b byte) bool {
	return b == '\'' || b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// startCancel finds the task to cancel: none, one (quote it), or several
// (list them).
func (r *Router) startCancel(ctx context.Context, in Inbound, id Identity, conv Conversation) error {
	if r.Booker == nil {
		return fmt.Errorf("startCancel: router not fully configured")
	}
	open, err := r.Booker.OpenTasks(ctx, id.UserID)
	if err != nil {
		return fmt.Errorf("startCancel: %w", err)
	}
	more := len(open) > MaxCancelChoices
	if more {
		open = open[:MaxCancelChoices]
	}
	switch len(open) {
	case 0:
		return r.Reply(ctx, in, CopyCancelNone, "")
	case 1:
		return r.askCancelReason(ctx, in, id, open[0], 0)
	}
	asked, err := r.Conversations.AskCancel(ctx, id.ID, PendingCancel{Choices: open, AskedAtUnix: r.now().Unix()}, ShownUnconfirmed)
	if err != nil {
		return fmt.Errorf("startCancel: %w", err)
	}
	if !asked {
		return r.cancelPreempted(ctx, in, id)
	}
	var b strings.Builder
	for i, t := range open {
		fmt.Fprintf(&b, "\n%d. %s", i+1, t.Title)
	}
	if more {
		b.WriteString("\n" + CopyCancelMoreInApp)
	}
	if err := r.Reply(ctx, in, fmt.Sprintf(CopyCancelWhich, b.String()), ""); err != nil {
		return err
	}
	r.markShown(ctx, id.ID)
	return nil
}

// MaxCancelChoices: the numbered list's length; OpenTasks returns one more
// so the list can say there are others (cancelled in the app).
const MaxCancelChoices = 9

// ErrTaskNotOpen: the task is past cancelling (finished, cancelled,
// removed) — Booker.CancelQuote's answer for it.
var ErrTaskNotOpen = errors.New("task is not open")

// supporterAskedSince: a supporter's question ("…?", unanswered, on an
// active task) arrived after `since`. A failed lookup counts as asked (fail
// toward asking again).
func (r *Router) supporterAskedSince(ctx context.Context, in Inbound, id Identity, since *time.Time) bool {
	if r.Chat == nil || !id.Linked() || since == nil {
		return false
	}
	last, lastTask, lastAt, answered, err := r.lastSupporterLineErr(ctx, in, id)
	if err != nil {
		log.Printf("[channel] cancel identity=%s: %v", id.ID, err)
		return true
	}
	return lastTask != "" && lastAt.After(*since) && supporterQuestionOpen(last, lastAt, answered, r.now())
}

// cancelPreempted: the cancel question could not be opened — a supporter's
// question took the thread while the tasks were looked up. That one is
// answered first; the cancel is asked for again after.
func (r *Router) cancelPreempted(ctx context.Context, in Inbound, id Identity) error {
	fresh, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return fmt.Errorf("cancel: %w", err)
	}
	log.Printf("[channel] cancel identity=%s: thread taken (state %s), question not sent", id.ID, fresh.State)
	switch fresh.State {
	case StateAwaitingApproval:
		return r.Reply(ctx, in, CopyCancelAfterApproval+"\n\n"+approvalStillPending(fresh), "")
	case StateHandoff:
		return r.onHandoff(ctx, in, id)
	case StatePosting:
		return r.Reply(ctx, in, CopyBookingInFlight, "")
	}
	if fresh.State == StateIdle {
		// Nothing took the thread (its row was missing — a failed touch):
		// not "still waiting" on anything; try again.
		return r.Reply(ctx, in, CopyCancelRetry, "")
	}
	return r.Reply(ctx, in, stillWaiting(fresh), "")
}

// askCancelReason: the reason first (the app's own list), then the charge.
// Nothing is quoted or cancelled until a reason is picked; NO keeps the task.
func (r *Router) askCancelReason(ctx context.Context, in Inbound, id Identity, t TaskRef, tries int) error {
	asked, err := r.Conversations.AskCancel(ctx, id.ID,
		PendingCancel{TaskID: t.ID, Title: t.Title, ReasonTries: tries, AskedAtUnix: r.now().Unix()}, ShownUnconfirmed)
	if err != nil {
		return fmt.Errorf("askCancelReason: %w", err)
	}
	if !asked {
		return r.cancelPreempted(ctx, in, id)
	}
	copy := CopyCancelWhy
	if tries > 0 {
		copy = CopyCancelWhyReask
	}
	if err := r.Reply(ctx, in, fmt.Sprintf(copy, t.Title, cancelReasonList()), ""); err != nil {
		return err
	}
	r.markShown(ctx, id.ID)
	return nil
}

// MaxCancelReasonTries: answers to the reason question that pick nothing
// before the cancel is dropped and the message read fresh.
const MaxCancelReasonTries = 2

// quoteCancel asks to confirm cancelling one task at the charge quoted now,
// for the reason already chosen.
func (r *Router) quoteCancel(ctx context.Context, in Inbound, id Identity, conv Conversation, t TaskRef, reasonCode string) error {
	toIdle := func(copy string) error {
		conv.State, conv.PendingCancel = StateIdle, nil
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("quoteCancel: %w", err)
		}
		return r.Reply(ctx, in, fmt.Sprintf(copy, t.Title), "")
	}
	q, err := r.Booker.CancelQuote(ctx, id.UserID, t.ID)
	if errors.Is(err, ErrTaskNotOpen) {
		return toIdle(CopyCancelGone)
	}
	if err != nil {
		log.Printf("[channel] cancel quote identity=%s task=%s: %v", id.ID, t.ID, err)
		return toIdle(CopyCancelFailed)
	}
	title := firstNonEmpty(q.Title, t.Title)
	t.Title = title
	if q.Moving {
		log.Printf("[channel] cancel quote identity=%s task=%s: charge moving, sent to the app", id.ID, t.ID)
		return toIdle(CopyCancelInApp)
	}
	// R-1: no YES counts until the question is delivered (ShownUnconfirmed,
	// then markShown). Opened by compare-and-set, never a full save of the
	// snapshot read before the quote (review 10/24 r2).
	asked, err := r.Conversations.AskCancel(ctx, id.ID,
		PendingCancel{TaskID: t.ID, Title: title, QuotedCents: q.ChargeCents, ReasonCode: reasonCode, AskedAtUnix: r.now().Unix()}, ShownUnconfirmed)
	if err != nil {
		return fmt.Errorf("quoteCancel: %w", err)
	}
	if !asked {
		return r.cancelPreempted(ctx, in, id)
	}
	if err := r.Reply(ctx, in, cancelQuestion(title, q, reasonCode), ""); err != nil {
		return err
	}
	r.markShown(ctx, id.ID)
	return nil
}

func cancelQuestion(title string, q CancelQuote, reasonCode string) string {
	reason := ""
	for _, c := range CancelReasonChoices {
		if c.Code == reasonCode {
			reason = fmt.Sprintf(CopyCancelReasonEcho, c.Label)
		}
	}
	if q.ChargeCents <= 0 {
		return fmt.Sprintf(CopyCancelConfirmFree, title, reason)
	}
	return fmt.Sprintf(CopyCancelConfirmFee, title, reason, Dollars(q.ChargeCents), Dollars(q.BaseFeeCents))
}

// cancelLapsed: the question is older than CancelTTL (or carries no time —
// a row written before the field existed): nothing answers it any more.
func (r *Router) cancelLapsed(pc *PendingCancel) bool {
	return pc.AskedAtUnix == 0 || r.now().Sub(time.Unix(pc.AskedAtUnix, 0)) > CancelTTL
}

// onAwaitingCancel: a number picks the task; YES (sent after the question,
// within CancelTTL) cancels at the quoted charge; NO keeps it; anything else
// leaves the cancel and is read as a fresh message.
func (r *Router) onAwaitingCancel(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	pc := conv.PendingCancel
	leave := func() error {
		conv.State, conv.PendingCancel = StateIdle, nil
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("onAwaitingCancel: %w", err)
		}
		return nil
	}
	if pc == nil || r.cancelLapsed(pc) {
		if err := leave(); err != nil {
			return err
		}
		if pc != nil && pc.TaskID != "" {
			log.Printf("[channel] cancel identity=%s: question lapsed, message read fresh", id.ID)
			if err := r.Reply(ctx, in, fmt.Sprintf(CopyCancelNotDone, pc.Title), ""); err != nil {
				return err
			}
		}
		return r.onIdle(ctx, in, id, conv, text)
	}
	if pc.TaskID == "" { // choosing
		n, err := strconv.Atoi(strings.TrimSpace(strings.Trim(text, ".#)")))
		if err == nil && n >= 1 && n <= len(pc.Choices) {
			return r.askCancelReason(ctx, in, id, pc.Choices[n-1], 0)
		}
		if IsNo(text) && !IsCancelRequest(text) {
			if err := leave(); err != nil {
				return err
			}
			return r.Reply(ctx, in, CopyCancelNothing, "")
		}
		if IsCancelRequest(text) {
			return r.Reply(ctx, in, CopyStillWaitingCancelChoice, "")
		}
		if err := leave(); err != nil {
			return err
		}
		if err := r.Reply(ctx, in, CopyCancelNothing, ""); err != nil {
			return err
		}
		return r.onIdle(ctx, in, id, conv, text)
	}
	if pc.ReasonCode == "" { // the reason question
		t := TaskRef{ID: pc.TaskID, Title: pc.Title}
		text = strings.ReplaceAll(text, "’", "'")
		switch {
		case IsNo(text) && !IsCancelRequest(text), isNegatedCancel(text):
			// NO first: "no, I don't need to cancel" keeps the task — never
			// read as the "no longer needed" reason (review r1 M3).
			if err := leave(); err != nil {
				return err
			}
			return r.Reply(ctx, in, fmt.Sprintf(CopyCancelKept, pc.Title), "")
		case conv.QuestionShownAt == nil || in.ReceivedAt.IsZero() || in.ReceivedAt.Before(*conv.QuestionShownAt):
			// Sent before the reason question reached them (a redelivered
			// "2" from the task list, a double tap): the reason is relayed
			// to the supporter, so it is never taken from a message that
			// could not have answered this question (R-1, review r1 M4).
			return r.askCancelReason(ctx, in, id, t, pc.ReasonTries)
		}
		if code := pickCancelReason(text); code != "" {
			return r.quoteCancel(ctx, in, id, conv, t, code)
		}
		if IsCancelRequest(text) {
			return r.askCancelReason(ctx, in, id, t, min(pc.ReasonTries+1, MaxCancelReasonTries-1))
		}
		if pc.ReasonTries+1 < MaxCancelReasonTries {
			return r.askCancelReason(ctx, in, id, t, pc.ReasonTries+1)
		}
		// Twice nothing picked: not about the cancel after all — said so,
		// never a silent drop (review r1 M1).
		if err := leave(); err != nil {
			return err
		}
		if err := r.Reply(ctx, in, fmt.Sprintf(CopyCancelNotDone, pc.Title), ""); err != nil {
			return err
		}
		return r.onIdle(ctx, in, id, conv, text)
	}
	strictYes := IsStrictYesNo(text) && IsYes(text)
	switch {
	case strictYes && (conv.QuestionShownAt == nil || in.ReceivedAt.IsZero() || in.ReceivedAt.Before(*conv.QuestionShownAt)):
		// Sent before the question reached them (a double tap): asked again.
		return r.quoteCancel(ctx, in, id, conv, TaskRef{ID: pc.TaskID, Title: pc.Title}, pc.ReasonCode)
	case strictYes && in.Channel == WhatsApp && in.ReplyToProviderID != "":
		// A swiped "YES" may be on another message (the cancel question's
		// own id is not kept): the fee needs it typed on its own.
		return r.Reply(ctx, in, fmt.Sprintf(CopyCancelReask, pc.Title), "")
	case strictYes && r.supporterAskedSince(ctx, in, id, conv.QuestionShownAt):
		// The supporter asked something after our question: this YES may
		// be theirs. A cancel fee cannot be undone — asked again.
		log.Printf("[channel] cancel identity=%s: supporter asked after the question, re-asked", id.ID)
		return r.quoteCancel(ctx, in, id, conv, TaskRef{ID: pc.TaskID, Title: pc.Title}, pc.ReasonCode)
	case strictYes:
		return r.confirmCancel(ctx, in, id, conv, pc)
	case IsCancelRequest(text):
		// "cancel" again (IsNo would read it as a no): the question stands.
		return r.Reply(ctx, in, fmt.Sprintf(CopyCancelReask, pc.Title), "")
	case IsNo(text):
		if err := leave(); err != nil {
			return err
		}
		return r.Reply(ctx, in, fmt.Sprintf(CopyCancelKept, pc.Title), "")
	case IsYes(text):
		// "ok", "sure": the cancel needs the YES on its own (money).
		return r.Reply(ctx, in, fmt.Sprintf(CopyCancelReask, pc.Title), "")
	}
	if err := leave(); err != nil {
		return err
	}
	if err := r.Reply(ctx, in, fmt.Sprintf(CopyCancelNotDone, pc.Title), ""); err != nil {
		return err
	}
	return r.onIdle(ctx, in, id, conv, text)
}

// confirmCancel: a YES to the quoted charge. Re-quoted first (the charge may
// have moved since the question); then the question is claimed atomically
// — a second YES, or a supporter's question armed meanwhile, finds it gone
// and does not cancel twice — and only then the app's cancel runs.
func (r *Router) confirmCancel(ctx context.Context, in Inbound, id Identity, conv Conversation, pc *PendingCancel) error {
	q, err := r.Booker.CancelQuote(ctx, id.UserID, pc.TaskID)
	if errors.Is(err, ErrTaskNotOpen) {
		return r.endCancel(ctx, in, id, pc, CopyCancelGone)
	}
	if err != nil {
		log.Printf("[channel] cancel re-quote identity=%s task=%s: %v", id.ID, pc.TaskID, err)
		return r.endCancel(ctx, in, id, pc, CopyCancelFailed)
	}
	if q.Moving {
		log.Printf("[channel] cancel identity=%s task=%s: charge moving, sent to the app", id.ID, pc.TaskID)
		return r.endCancel(ctx, in, id, pc, CopyCancelInApp)
	}
	if q.ChargeCents > pc.QuotedCents {
		// The price went up since the question: the YES was not to this.
		log.Printf("[channel] cancel identity=%s task=%s: charge rose %d→%d, re-asked", id.ID, pc.TaskID, pc.QuotedCents, q.ChargeCents)
		return r.quoteCancel(ctx, in, id, conv, TaskRef{ID: pc.TaskID, Title: pc.Title}, pc.ReasonCode)
	}
	won, err := r.Conversations.ClaimCancel(ctx, id.ID, pc.TaskID)
	if err != nil {
		return fmt.Errorf("confirmCancel: %w", err)
	}
	if !won {
		after, gerr := r.Conversations.GetConversation(ctx, id.ID)
		if gerr == nil && after.State == StateAwaitingApproval {
			log.Printf("[channel] cancel identity=%s task=%s: a supporter's question took the thread first", id.ID, pc.TaskID)
			return r.Reply(ctx, in, fmt.Sprintf(CopyCancelTakenOver, pc.Title), "")
		}
		log.Printf("[channel] cancel identity=%s task=%s: already claimed (a second YES)", id.ID, pc.TaskID)
		return nil // the first YES answers
	}
	res := r.Booker.Cancel(ctx, id.UserID, pc.TaskID, pc.ReasonCode)
	log.Printf("[channel] cancel identity=%s task=%s ok=%t gone=%t bill=%d unknown=%t", id.ID, pc.TaskID, res.OK, res.Gone, res.BillCents, res.BillUnknown)
	switch {
	case res.OK && res.BillUnknown:
		return r.Reply(ctx, in, fmt.Sprintf(CopyCancelledUnknown, pc.Title), "")
	case res.OK && res.BillCents > 0:
		return r.Reply(ctx, in, fmt.Sprintf(CopyCancelledFee, pc.Title, Dollars(res.BillCents)), "")
	case res.OK:
		return r.Reply(ctx, in, fmt.Sprintf(CopyCancelledFree, pc.Title), "")
	case res.Gone:
		return r.Reply(ctx, in, fmt.Sprintf(CopyCancelGone, pc.Title), "")
	}
	return r.Reply(ctx, in, fmt.Sprintf(CopyCancelFailed, pc.Title), "")
}

// endCancel closes the question with copy — through the claim, so it never
// writes over a supporter's question armed meanwhile.
func (r *Router) endCancel(ctx context.Context, in Inbound, id Identity, pc *PendingCancel, copy string) error {
	won, err := r.Conversations.ClaimCancel(ctx, id.ID, pc.TaskID)
	if err != nil {
		return fmt.Errorf("endCancel: %w", err)
	}
	if !won {
		return nil
	}
	return r.Reply(ctx, in, fmt.Sprintf(copy, pc.Title), "")
}
