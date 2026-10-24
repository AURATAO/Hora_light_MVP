package channel

// Cancel a posted task by text (D-17, Arbiter 2026-10-24): recognised by
// rule, confirmed once with the charge the app's own cancellation policy
// quotes, carried out through the app's own cancel path. The YES is consent
// to the amount quoted: if the charge has gone up by the time it arrives (the
// grace window ended, the supporter clocked more time), the new amount is
// asked again — never charged silently.

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
)

// cancelRequestRE: an ask to cancel a posted task. "don't cancel" is not.
var cancelRequestRE = regexp.MustCompile(`(?i)\b(cancel|call off)\b|取消`)
var cancelNegatedRE = regexp.MustCompile(`(?i)\b(don'?t|do not|not|never)\s+(cancel|call off)\b|不要取消|別取消`)

// IsCancelRequest reports an ask to cancel, by rule (never the model).
func IsCancelRequest(text string) bool {
	return cancelRequestRE.MatchString(text) && !cancelNegatedRE.MatchString(text)
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
	conv.PendingCancel = &PendingCancel{Choices: open}
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
	q, err := r.Booker.CancelQuote(ctx, id.UserID, t.ID)
	if err != nil {
		log.Printf("[channel] cancel quote identity=%s task=%s: %v", id.ID, t.ID, err)
		conv.State, conv.PendingCancel = StateIdle, nil
		if serr := r.Conversations.SaveConversation(ctx, conv); serr != nil {
			return fmt.Errorf("quoteCancel: %w", serr)
		}
		return r.Reply(ctx, in, fmt.Sprintf(CopyCancelFailed, t.Title), "")
	}
	title := firstNonEmpty(q.Title, t.Title)
	conv.State = StateAwaitingCancel
	conv.PendingCancel = &PendingCancel{TaskID: t.ID, Title: title, QuotedCents: q.ChargeCents}
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

// onAwaitingCancel: a number picks the task; YES (sent after the question)
// cancels at the quoted charge; NO keeps it; anything else leaves the cancel
// and is read as a fresh message.
func (r *Router) onAwaitingCancel(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	pc := conv.PendingCancel
	leave := func() error {
		conv.State, conv.PendingCancel = StateIdle, nil
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("onAwaitingCancel: %w", err)
		}
		return nil
	}
	if pc == nil {
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
		if IsNo(text) {
			if err := leave(); err != nil {
				return err
			}
			return r.Reply(ctx, in, CopyCancelNothing, "")
		}
		if err := leave(); err != nil {
			return err
		}
		return r.onIdle(ctx, in, id, conv, text)
	}
	switch {
	case IsStrictYesNo(text) && IsYes(text) && (conv.QuestionShownAt == nil || in.ReceivedAt.IsZero() || in.ReceivedAt.Before(*conv.QuestionShownAt)):
		// Sent before the question reached them (a double tap): asked again.
		return r.quoteCancel(ctx, in, id, conv, TaskRef{ID: pc.TaskID, Title: pc.Title})
	case IsStrictYesNo(text) && IsYes(text):
		q, err := r.Booker.CancelQuote(ctx, id.UserID, pc.TaskID)
		if err != nil {
			log.Printf("[channel] cancel re-quote identity=%s task=%s: %v", id.ID, pc.TaskID, err)
			if lerr := leave(); lerr != nil {
				return lerr
			}
			return r.Reply(ctx, in, fmt.Sprintf(CopyCancelFailed, pc.Title), "")
		}
		if q.ChargeCents > pc.QuotedCents {
			// The price went up since the question: the YES was not to this.
			log.Printf("[channel] cancel identity=%s task=%s: charge rose %d→%d, re-asked", id.ID, pc.TaskID, pc.QuotedCents, q.ChargeCents)
			return r.quoteCancel(ctx, in, id, conv, TaskRef{ID: pc.TaskID, Title: pc.Title})
		}
		res := r.Booker.Cancel(ctx, id.UserID, pc.TaskID)
		log.Printf("[channel] cancel identity=%s task=%s ok=%t gone=%t bill=%d", id.ID, pc.TaskID, res.OK, res.Gone, res.BillCents)
		if err := leave(); err != nil {
			return err
		}
		switch {
		case res.OK && res.BillCents > 0:
			return r.Reply(ctx, in, fmt.Sprintf(CopyCancelledFee, pc.Title, Dollars(res.BillCents)), "")
		case res.OK:
			return r.Reply(ctx, in, fmt.Sprintf(CopyCancelledFree, pc.Title), "")
		case res.Gone:
			return r.Reply(ctx, in, fmt.Sprintf(CopyCancelGone, pc.Title), "")
		}
		return r.Reply(ctx, in, fmt.Sprintf(CopyCancelFailed, pc.Title), "")
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
