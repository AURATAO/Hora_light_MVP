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
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// cancelWordRE finds the verb; the words before it decide whether it is a
// request (IsCancelRequest).
var cancelWordRE = regexp.MustCompile(`(?i)\b(cancel|call off)\b`)

// cancelLeadWords: all a request may say before "cancel" — "please",
// "can you", "I want to", "let's just". Anything else ("I had to cancel my
// meeting", "don't cancel", "no need to cancel") is not a request to us.
var cancelLeadWords = map[string]bool{
	"please": true, "pls": true, "plz": true, "hey": true, "hi": true, "ok": true, "okay": true,
	"actually": true, "sorry": true, "so": true, "just": true, "can": true, "could": true,
	"would": true, "will": true, "you": true, "u": true, "i": true, "i'd": true, "id": true,
	"want": true, "wanna": true, "need": true, "to": true, "like": true, "let's": true,
	"lets": true, "go": true, "ahead": true, "and": true, "then": true,
}

// cancelZhLeadRE: the same for 取消 — only "please / help me / I want /
// can you" before it (會議取消了 is not a request; 不要取消 is a no).
var cancelZhLeadRE = regexp.MustCompile(`^(?:好|好啊|那|那就|麻煩|麻烦|請|请|幫我|帮我|你|我要|我想|我想要|可以|可不可以|能不能|直接|先)*$`)

// IsCancelRequest reports an ask to cancel, by rule (never the model).
func IsCancelRequest(text string) bool {
	t := strings.TrimSpace(strings.ReplaceAll(text, "’", "'"))
	if i := strings.Index(t, "取消"); i >= 0 {
		lead := strings.Trim(t[:i], " ,，。.!！~～")
		return cancelZhLeadRE.MatchString(lead)
	}
	loc := cancelWordRE.FindStringIndex(t)
	if loc == nil {
		return false
	}
	for _, w := range strings.FieldsFunc(strings.ToLower(t[:loc[0]]), func(r rune) bool {
		return r == ' ' || r == ',' || r == '!' || r == '.' || r == '\t'
	}) {
		if !cancelLeadWords[w] {
			return false
		}
	}
	return true
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
	switch len(open) {
	case 0:
		return r.Reply(ctx, in, CopyCancelNone, "")
	case 1:
		return r.quoteCancel(ctx, in, id, conv, open[0])
	}
	conv.State = StateAwaitingCancel
	conv.PendingCancel = &PendingCancel{Choices: open, AskedAtUnix: r.now().Unix()}
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
		return fmt.Errorf("startCancel: %w", err)
	}
	var b strings.Builder
	for i, t := range open {
		fmt.Fprintf(&b, "\n%d. %s", i+1, t.Title)
	}
	return r.Reply(ctx, in, fmt.Sprintf(CopyCancelWhich, b.String()), "")
}

// quoteCancel asks to confirm cancelling one task at the charge quoted now.
func (r *Router) quoteCancel(ctx context.Context, in Inbound, id Identity, conv Conversation, t TaskRef) error {
	toIdle := func(copy string) error {
		conv.State, conv.PendingCancel = StateIdle, nil
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("quoteCancel: %w", err)
		}
		return r.Reply(ctx, in, fmt.Sprintf(copy, t.Title), "")
	}
	q, err := r.Booker.CancelQuote(ctx, id.UserID, t.ID)
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
	conv.State = StateAwaitingCancel
	conv.PendingCancel = &PendingCancel{TaskID: t.ID, Title: title, QuotedCents: q.ChargeCents, AskedAtUnix: r.now().Unix()}
	shown := ShownUnconfirmed // R-1: no YES counts until the question is delivered
	conv.QuestionShownAt = &shown
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
		return fmt.Errorf("quoteCancel: %w", err)
	}
	if err := r.Reply(ctx, in, cancelQuestion(title, q), ""); err != nil {
		return err
	}
	r.markShown(ctx, id.ID)
	return nil
}

func cancelQuestion(title string, q CancelQuote) string {
	if q.ChargeCents <= 0 {
		return fmt.Sprintf(CopyCancelConfirmFree, title)
	}
	return fmt.Sprintf(CopyCancelConfirmFee, title, Dollars(q.ChargeCents), Dollars(q.BaseFeeCents))
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
		if pc != nil {
			log.Printf("[channel] cancel identity=%s: question lapsed, message read fresh", id.ID)
		}
		if err := leave(); err != nil {
			return err
		}
		return r.onIdle(ctx, in, id, conv, text)
	}
	if pc.TaskID == "" { // choosing
		n, err := strconv.Atoi(strings.TrimSpace(strings.Trim(text, ".#)")))
		if err == nil && n >= 1 && n <= len(pc.Choices) {
			return r.quoteCancel(ctx, in, id, conv, pc.Choices[n-1])
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
		return r.onIdle(ctx, in, id, conv, text)
	}
	strictYes := IsStrictYesNo(text) && IsYes(text)
	switch {
	case strictYes && (conv.QuestionShownAt == nil || in.ReceivedAt.IsZero() || in.ReceivedAt.Before(*conv.QuestionShownAt)):
		// Sent before the question reached them (a double tap): asked again.
		return r.quoteCancel(ctx, in, id, conv, TaskRef{ID: pc.TaskID, Title: pc.Title})
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
	return r.onIdle(ctx, in, id, conv, text)
}

// confirmCancel: a YES to the quoted charge. Re-quoted first (the charge may
// have moved since the question); then the question is claimed atomically
// — a second YES, or a supporter's question armed meanwhile, finds it gone
// and does not cancel twice — and only then the app's cancel runs.
func (r *Router) confirmCancel(ctx context.Context, in Inbound, id Identity, conv Conversation, pc *PendingCancel) error {
	q, err := r.Booker.CancelQuote(ctx, id.UserID, pc.TaskID)
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
		return r.quoteCancel(ctx, in, id, conv, TaskRef{ID: pc.TaskID, Title: pc.Title})
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
	res := r.Booker.Cancel(ctx, id.UserID, pc.TaskID)
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
