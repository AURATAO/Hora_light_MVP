package channel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"hora-auth/internal/channel/llm"
)

// The booking in flight, and what may book (audit 2026-10-30).
//
// Gap A — a YES books the price the person saw, or nothing. A summary stays
// bookable for SummaryTTL; the estimate it showed travels with the post as
// a ceiling (PostOptions.QuotedCents) because createTask resolves the rate at
// the post, and a surge window opening in between would otherwise raise it
// unasked; and a time already past is never booked. Each is asked again
// instead (checkBeforePost → reconfirm), on the YES path and the setup
// page's finish step alike — both go through claimAndPost.
//
// Gap B — a booking in flight never strands the thread. A message during it
// that asks for a person, or that the agent escalates (safety, a dispute…),
// goes to ops at once (onPosting). A claim older than PostingStuckAfter is a
// crash mid-post: the next message, or the sweeper, recovers it by asking
// the store what the booking keys made (RecoverStuckPosting). Every errand's
// key is kept through retries, so a retry can never make a second task or
// hold (server/task_booking_key.go).

// DefaultSummaryTTL: how long a booking summary stays bookable.
const DefaultSummaryTTL = 30 * time.Minute

// PostingStuckAfter: a claim this old is a crash, not a post in flight.
// Routing runs under a 60 s timeout, so a live post never gets this old.
const PostingStuckAfter = 3 * time.Minute

func (r *Router) summaryTTL() time.Duration {
	if r.SummaryTTL > 0 {
		return r.SummaryTTL
	}
	return DefaultSummaryTTL
}

// newBookingKey: 128 random bits, hex.
func newBookingKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; a key that is
		// not unique would merge two errands, so refuse to make one.
		panic(fmt.Sprintf("booking key: %v", err))
	}
	return "chat_" + hex.EncodeToString(b[:])
}

// ensureBookingKeys gives every errand without one its key. Kept ones stay:
// a re-shown summary is the same errand.
func ensureBookingKeys(p *PendingDraft) {
	items := p.Items()
	for i := range items {
		if items[i].BookingKey == "" {
			items[i].BookingKey = newBookingKey()
		}
	}
	p.SetItems(items)
}

// summaryFresh: the summary was shown within SummaryTTL, and every errand has
// its key (a draft from before keys existed is shown again, and gets them).
func (r *Router) summaryFresh(p *PendingDraft) bool {
	if p.SummaryShownAtUnix == 0 || r.now().Sub(time.Unix(p.SummaryShownAtUnix, 0)) > r.summaryTTL() {
		return false
	}
	for _, it := range p.Items() {
		if it.BookingKey == "" {
			return false
		}
	}
	return true
}

// pastItem: the first errand scheduled for a time that is not in the future.
func (r *Router) pastItem(p *PendingDraft) (DraftItem, bool) {
	for _, it := range p.Items() {
		if !it.Draft.Scheduled {
			continue
		}
		t, err := time.Parse(time.RFC3339, it.Draft.ScheduledTime)
		if err != nil {
			continue // createTask refuses it (400), which is said as a failure
		}
		if !t.After(r.now()) {
			return it, true
		}
	}
	return DraftItem{}, false
}

// checkBeforePost decides, on the claimed draft, whether it may book now.
// held: no; res says why (PostReconfirm, PostPriceChanged) for afterPost.
func (r *Router) checkBeforePost(ctx context.Context, conv Conversation) (res PostResult, held bool) {
	p := conv.Pending
	if _, past := r.pastItem(p); past {
		log.Printf("[channel] booking identity=%s: scheduled time has passed — asking for a new one", conv.IdentityID)
		return PostResult{Outcome: PostReconfirm}, true
	}
	if !r.summaryFresh(p) {
		log.Printf("[channel] booking identity=%s: summary shown %s ago (ttl %s) — shown again", conv.IdentityID,
			shownAgo(r.now(), p.SummaryShownAtUnix), r.summaryTTL())
		return PostResult{Outcome: PostReconfirm}, true
	}
	for _, it := range p.Items() {
		cents, _, err := r.Booker.Quote(ctx, it.Draft, it.BudgetCents)
		if err != nil {
			// The ceiling still travels with the post: createTask refuses a
			// higher price itself.
			log.Printf("[channel] booking identity=%s: price check: %v", conv.IdentityID, err)
			continue
		}
		if cents > it.QuoteCents {
			log.Printf("[channel] booking identity=%s: price now %d, agreed %d — asked again", conv.IdentityID, cents, it.QuoteCents)
			return PostResult{Outcome: PostPriceChanged}, true
		}
	}
	return PostResult{}, false
}

func shownAgo(now time.Time, unix int64) string {
	if unix == 0 {
		return "(never stamped)"
	}
	return now.Sub(time.Unix(unix, 0)).Round(time.Second).String()
}

// refreshQuotes prices every errand again. res.QuoteCents, from a refused
// post, is a floor for the first: createTask's own figure at that instant.
func (r *Router) refreshQuotes(ctx context.Context, p *PendingDraft, res PostResult) error {
	items := p.Items()
	for i := range items {
		cents, minutes, err := r.Booker.Quote(ctx, items[i].Draft, items[i].BudgetCents)
		if err != nil {
			return fmt.Errorf("refresh quotes: %w", err)
		}
		if i == 0 && res.Outcome == PostPriceChanged && res.QuoteCents > cents {
			cents = res.QuoteCents
		}
		items[i].QuoteCents, items[i].QuoteMinutes = cents, minutes
	}
	p.SetItems(items)
	return nil
}

// reconfirm ends a claim that did not book (PostReconfirm, PostPriceChanged)
// by putting the draft to the person again: a new time when its time has
// passed, otherwise a fresh summary with today's prices and a new YES.
func (r *Router) reconfirm(ctx context.Context, ch Channel, id Identity, conv Conversation, res PostResult, partial string, async bool) error {
	p := conv.Pending
	var text string
	if it, past := r.pastItem(p); past {
		text = fmt.Sprintf(CopyTimePassed, it.Draft.Title, r.when(true, it.Draft.ScheduledTime))
	} else {
		if err := r.refreshQuotes(ctx, p, res); err != nil {
			log.Printf("[channel] reconfirm identity=%s: %v", id.ID, err)
			return r.afterPost(ctx, ch, id, conv, PostResult{Outcome: PostFailed}, 0, len(p.Items()), async)
		}
		ensureBookingKeys(p)
		summary := r.Summary(p)
		p.SummaryShown = summary
		p.SummaryShownAtUnix = r.now().Unix()
		lead := CopySummaryStale
		if res.Outcome == PostPriceChanged {
			lead = CopyPriceChanged
		}
		text = lead + "\n\n" + summary
	}
	conv.State = StateAwaitingConfirm
	shown := ShownUnconfirmed // a question of ours: in flight until delivered (R-1)
	conv.QuestionShownAt = &shown
	handedOff := r.finishPosting(ctx, id, conv)
	if handedOff {
		text = CopyPostNotDoneHandedOff
	}
	out := Outbound{Channel: ch, To: id.Address, Text: partial + text}
	return r.dispatch(ctx, async, func(c context.Context) error {
		if err := r.Send(c, out, ""); err != nil {
			return err
		}
		if !handedOff {
			r.markShown(c, id.ID)
			r.resumeDeferred(c, id)
		}
		return nil
	})
}

// finishPosting saves a booking's outcome, ending StatePosting. handedOff:
// the thread went to ops while the post ran (a safety message) — it stays
// theirs, with conv's draft, and the outcome is said as such.
func (r *Router) finishPosting(ctx context.Context, id Identity, conv Conversation) (handedOff bool) {
	st, err := r.Conversations.FinishPosting(ctx, conv)
	if err != nil {
		log.Printf("[channel] save after post identity=%s: %v", id.ID, err)
		return false
	}
	if st == "" {
		log.Printf("[channel] save after post identity=%s: the thread was no longer posting — outcome not saved", id.ID)
	}
	return st == StateHandoff && conv.State != StateHandoff
}

// postingStuck: the booking in flight was claimed PostingStuckAfter ago or
// more (or by a build that kept no claim time — the store decides).
func (r *Router) postingStuck(conv Conversation) bool {
	return conv.State == StatePosting &&
		(conv.PostingStartedAt == nil || r.now().Sub(*conv.PostingStartedAt) >= PostingStuckAfter)
}

// onPosting is a message while a booking is in flight. Safety first: a
// request for a person, or anything the agent escalates, goes to ops whatever
// the booking is doing. A stuck booking is recovered before anything else is
// said about it. Otherwise the person is told it is on its way.
func (r *Router) onPosting(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	var reason HandoffReason
	detail := ""
	// A bare HELP here is a plea: a booking is being made (help.go).
	if IsHumanRequest(text) || IsHelpKeyword(text) {
		reason = HandoffHumanRequested
	} else if r.Agent != nil {
		if p := r.propose(ctx, id, text, nil, conv, "", "", time.Time{}, false); p.Action == llm.ActionEscalate {
			reason, detail = HandoffAgentEscalated, string(p.Escalation)
		}
	}
	if !r.postingStuck(conv) {
		if reason != "" {
			return r.handOffWhilePosting(ctx, in, id, reason, detail)
		}
		return r.Reply(ctx, in, CopyBookingInFlight, "")
	}
	recovered, err := r.RecoverStuckPosting(ctx, id, false)
	if err != nil {
		return fmt.Errorf("onPosting: %w", err)
	}
	fresh, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return fmt.Errorf("onPosting: %w", err)
	}
	switch {
	case reason != "" && fresh.State == StatePosting:
		return r.handOffWhilePosting(ctx, in, id, reason, detail) // another recoverer holds it
	case reason != "" && fresh.State == StateHandoff:
		return r.onHandoff(ctx, in, id)
	case reason != "":
		return r.handOffDetail(ctx, in, id, fresh, reason, detail)
	case !recovered || fresh.State == StatePosting:
		return r.Reply(ctx, in, CopyBookingInFlight, "")
	case IsYes(text) || IsNo(text) || IsAffirmation(text):
		// "yes?", "ok": the recovery's own line answers it. Read as a YES it
		// would be the answer to a question asked after it was sent.
		return nil
	}
	// Anything else (a new errand, a correction) is read as usual.
	return r.routeMessage(ctx, in, id, fresh, text)
}

// handOffWhilePosting hands a thread to ops while its booking is in flight.
// Ops are told first (never "a teammate will text you" with nobody told);
// the post then finishes into the handed-off thread (FinishPosting) and the
// person is told how it went.
func (r *Router) handOffWhilePosting(ctx context.Context, in Inbound, id Identity, reason HandoffReason, detail string) error {
	if err := r.notifyOps(ctx, id, reason, false, handoffOpen{detail: detail}); err != nil {
		log.Printf("[channel] handoff identity=%s reason=%s while posting: ops NOT reached: %v", id.ID, reason, err)
		return r.Reply(ctx, in, CopyBookingInFlight+" "+CopyHandoffFailed, "")
	}
	held, err := r.Conversations.HandOffWhilePosting(ctx, id.ID)
	if err != nil {
		log.Printf("[channel] handoff identity=%s reason=%s while posting: ops told, thread NOT held: %v", id.ID, reason, err)
		return r.Reply(ctx, in, CopyHandoffStart, "")
	}
	if held {
		log.Printf("[channel] handoff identity=%s reason=%s while posting", id.ID, reason)
		return r.Reply(ctx, in, CopyHandoffWhileBooking, "")
	}
	// The booking finished meanwhile: the thread is handed off as it is now
	// (ops were told above — not told twice).
	fresh, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return fmt.Errorf("handOffWhilePosting: %w", err)
	}
	if fresh.State != StateHandoff {
		fresh.State, fresh.PendingExtensionID = StateHandoff, ""
		fresh.PendingTaskChoices, fresh.PendingRelayText = nil, ""
		if err := r.Conversations.SaveConversation(ctx, fresh); err != nil {
			log.Printf("[channel] handoff identity=%s reason=%s: ops told, thread NOT held: %v", id.ID, reason, err)
		}
	}
	return r.Reply(ctx, in, CopyHandoffStart, "")
}

// RecoverStuckPosting settles a booking whose claim is older than
// PostingStuckAfter (the server's sweeper, and the person's next message).
// Each errand's booking key says whether its task was made: made ones are
// booked and said so; the rest go back to awaiting a YES, the person is told
// to reply YES (the retry reuses the keys, so it cannot book twice), and ops
// are told. A question held behind the booking is released. recovered=false:
// not stuck, or another caller is recovering it.
func (r *Router) RecoverStuckPosting(ctx context.Context, id Identity, async bool) (bool, error) {
	pending, ok, err := r.Conversations.ReclaimStuckPosting(ctx, id.ID, PostingStuckAfter)
	if err != nil || !ok {
		return false, err
	}
	conv, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return false, fmt.Errorf("RecoverStuckPosting: %w", err)
	}
	conv.Pending = pending
	log.Printf("[channel] booking identity=%s stuck in flight — recovering", id.ID)
	if pending == nil {
		conv.State = StateIdle
		handedOff := r.finishPosting(ctx, id, conv)
		return true, r.dispatch(ctx, async, func(c context.Context) error {
			if !handedOff {
				if err := r.Send(c, Outbound{Channel: id.Channel, To: id.Address, Text: CopyPostingLost}, ""); err != nil {
					return err
				}
			}
			r.alertBookingProblem(c, id, "stuck booking recovered: no draft left to retry")
			if !handedOff {
				r.resumeDeferred(c, id)
			}
			return nil
		})
	}
	items := pending.Items()
	var left []DraftItem
	lastTask := ""
	for _, it := range items {
		taskID := ""
		if it.BookingKey != "" && id.UserID != "" {
			if taskID, err = r.Booker.BookedTask(ctx, id.UserID, it.BookingKey); err != nil {
				// Unknown: nothing is said or retried on a guess. The claim
				// was renewed; the next sweep asks again.
				return false, fmt.Errorf("RecoverStuckPosting: %w", err)
			}
		}
		if taskID != "" {
			if lastTask == "" {
				r.attachBookedPhotos(ctx, id, pending, taskID)
			}
			lastTask = taskID
			continue
		}
		left = append(left, it)
	}
	booked := len(items) - len(left)
	out := Outbound{Channel: id.Channel, To: id.Address}
	if len(left) == 0 {
		conv.Pending, conv.State = nil, StateIdle
		handedOff := r.finishPosting(ctx, id, conv)
		log.Printf("[channel] booking identity=%s recovered: all %d booked", id.ID, booked)
		out.Text = CopyBooked
		if booked > 1 {
			out.Text = fmt.Sprintf(CopyBookedAll, booked)
		}
		if handedOff {
			out.Text = CopyBookedHandedOff
		}
		return true, r.dispatch(ctx, async, func(c context.Context) error {
			if err := r.Send(c, out, lastTask); err != nil {
				return err
			}
			if !handedOff {
				r.resumeDeferred(c, id)
			}
			return nil
		})
	}
	pending.SetItems(left)
	conv.State = StateAwaitingConfirm
	shown := ShownUnconfirmed // the retry is a question of ours (R-1)
	conv.QuestionShownAt = &shown
	handedOff := r.finishPosting(ctx, id, conv)
	log.Printf("[channel] booking identity=%s recovered: %d of %d booked, the rest wait on a YES", id.ID, booked, len(items))
	if booked > 0 {
		out.Text = fmt.Sprintf(CopyBookedSome, booked, len(items))
	}
	out.Text += CopyPostingStuck
	if handedOff {
		out.Text = CopyPostNotDoneHandedOff
	}
	return true, r.dispatch(ctx, async, func(c context.Context) error {
		if err := r.Send(c, out, ""); err != nil {
			return err
		}
		if !handedOff {
			r.markShown(c, id.ID)
		}
		r.alertBookingProblem(c, id, fmt.Sprintf("stuck booking recovered: %d of %d errands not booked; the person was asked to reply YES to retry", len(left), len(items)))
		if !handedOff {
			r.resumeDeferred(c, id)
		}
		return nil
	})
}

// alertBookingProblem tells ops a text booking failed (the hand-off notice
// path — last four digits only). Not a hand-off: the thread stays with the
// bot. Best effort: logged.
func (r *Router) alertBookingProblem(ctx context.Context, id Identity, problem string) {
	if err := r.notifyOps(ctx, id, HandoffBookingFailed, false, handoffOpen{bookingProblem: problem}); err != nil {
		log.Printf("[channel] booking problem identity=%s: ops NOT told: %v", id.ID, err)
	}
}
