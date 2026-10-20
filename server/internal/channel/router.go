package channel

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"hora-auth/internal/channel/llm"
)

// Booker is the bridge to the existing task and pricing code in package main.
// The channel never prices or posts a task itself: Quote is the same
// arithmetic as POST /tasks/estimate and Post runs the real POST /tasks
// handler, so holds, card checks and every refusal behave exactly as in the
// app (PLAN §1: business rules unchanged).
type Booker interface {
	Quote(ctx context.Context, d llm.TaskDraft) (cents, minutes int, err error)
	Post(ctx context.Context, userID string, d llm.TaskDraft) PostResult
	// ApplyChatProfile writes the name and terms consent gathered in the chat
	// onto the user's profile (fills blanks only; never overwrites).
	ApplyChatProfile(ctx context.Context, userID, name string, termsAcceptedAt *time.Time) error
}

// PostOutcome is what happened when a confirmed draft was posted.
type PostOutcome int

const (
	PostBooked      PostOutcome = iota // task created (and held, when payments are on)
	PostNeedsCard                      // 402 payment_method_required
	PostNeeds3DS                       // 402 payment_authentication_required
	PostDeclined                       // 402 payment_required — card refused
	PostOwesBalance                    // 403 outstanding_balance
	PostFailed                         // anything else
)

type PostResult struct {
	Outcome PostOutcome
	TaskID  string
	// Message is the server's own wording for a refusal, when it has one.
	Message string
}

// Router is the conversation state machine (PLAN §6, flow agreed 2026-10-20):
//
//	idle ──greeting/question──▶ welcome
//	idle ──request──▶ draft + quote ──(no account, no name)──▶ awaiting_name
//	                                 └────────────────────────▶ awaiting_confirm
//	awaiting_confirm ──YES──▶ linked: post now │ not linked: one setup link
//	                 ──NO───▶ idle (draft dropped)
//	                 ──text─▶ re-draft with the added detail
//	awaiting_setup ──(page finishes)──▶ post the draft ──▶ idle
//
// Help first: nobody gets a link before they have seen a price and said YES.
type Router struct {
	Messages      Store
	Identities    IdentityStore
	Conversations ConversationStore
	Adapters      map[Channel]Adapter
	Parser        llm.Parser
	Classifier    llm.Classifier
	Booker        Booker
	// Prompt builds the extraction prompt for "now" — the same one the app's
	// AI posting uses (server/ai_task_parser.go).
	Prompt   func(now time.Time) string
	SetupURL func(token string) string
	Now      func() time.Time
	// Location renders times in the requester's zone (America/New_York).
	Location *time.Location
	// Status finds the thread for task status updates (status.go). Nil turns
	// them off.
	Status StatusStore
	// Go runs a reply off the request path when the caller is an HTTP
	// request (S-32: the setup page's finish step must not wait on
	// Sendblue/Meta). Default `go f()`; tests run it inline.
	Go func(f func())
}

// dispatch runs a reply: inline when routing a webhook message (already off
// any request path), on its own goroutine when async — the booking result is
// decided and saved before this is called; only the message waits.
func (r *Router) dispatch(ctx context.Context, async bool, f func(context.Context) error) error {
	if !async {
		return f(ctx)
	}
	run := r.Go
	if run == nil {
		run = func(fn func()) { go fn() }
	}
	run(func() {
		c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := f(c); err != nil {
			log.Printf("[channel] async reply: %v", err)
		}
	})
	return nil
}

const resendAfter = 2 * time.Minute

func (r *Router) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Router) HandleInbound(ctx context.Context, in Inbound) error {
	id, err := r.Identities.UpsertIdentity(ctx, in.Channel, in.From)
	if err != nil {
		return fmt.Errorf("HandleInbound: %w", err)
	}
	// Bookkeeping failures are logged, not fatal: the person still deserves
	// an answer, and neither row gates anything money-related.
	if err := r.Identities.AttachMessage(ctx, in.Channel, in.ProviderMessageID, id.ID); err != nil {
		log.Printf("[channel] attach message id=%s: %v", in.ProviderMessageID, err)
	}
	if err := r.Identities.TouchConversation(ctx, id.ID, in.ReceivedAt); err != nil {
		log.Printf("[channel] touch conversation identity=%s: %v", id.ID, err)
	}
	conv, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return fmt.Errorf("HandleInbound: %w", err)
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return r.Reply(ctx, in, CopyTextOnly, "")
	}

	switch conv.State {
	case StateAwaitingName:
		return r.onName(ctx, in, id, conv, text)
	case StateAwaitingConfirm:
		return r.onConfirm(ctx, in, id, conv, text)
	case StateAwaitingSetup:
		return r.onAwaitingSetup(ctx, in, id, conv, text)
	case StateHandoff:
		return r.Reply(ctx, in, CopyHandoffHolding, "")
	case StatePosting:
		return r.Reply(ctx, in, CopyBookingInFlight, "")
	default:
		return r.onIdle(ctx, in, id, conv, text)
	}
}

func (r *Router) onIdle(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	// A bare YES/NO with nothing pending (a late duplicate YES after the
	// booking went through, a NO to nothing) never starts a draft: "YES" is
	// not an errand, and parsing it as one would book nonsense.
	if IsYes(text) || IsNo(text) {
		if id.Linked() {
			return r.Reply(ctx, in, CopyAck, "")
		}
		return r.Reply(ctx, in, CopyWelcome, "")
	}
	intent := llm.HeuristicIntent(text)
	if r.Classifier != nil {
		got, err := r.Classifier.Classify(ctx, text)
		if err != nil {
			log.Printf("[channel] classify (using fallback %q): %v", got, err)
		}
		intent = got
	}
	switch intent {
	case llm.IntentNewTask:
		return r.startDraft(ctx, in, id, conv, text)
	case llm.IntentQuestion:
		return r.Reply(ctx, in, CopyAbout, "")
	case llm.IntentGreeting:
		if id.Linked() {
			return r.Reply(ctx, in, CopyHelloAgain, "")
		}
		return r.Reply(ctx, in, CopyWelcome, "")
	default:
		// "good", "thanks", "ok" — typically a reaction to a status update.
		// A customer is not re-introduced to the service; a stranger is.
		if id.Linked() {
			return r.Reply(ctx, in, CopyAck, "")
		}
		return r.Reply(ctx, in, CopyWelcome, "")
	}
}

// startDraft parses the request, prices it and moves to the name or
// confirmation step. source is everything the person has said about this
// task so far.
func (r *Router) startDraft(ctx context.Context, in Inbound, id Identity, conv Conversation, source string) error {
	if r.Parser == nil || r.Prompt == nil || r.Booker == nil {
		return fmt.Errorf("start draft: router not fully configured")
	}
	draft, usage, err := r.Parser.ParseTask(ctx, r.Prompt(r.now().In(r.loc())), source)
	if err != nil {
		log.Printf("[channel] parse failed model=%s latency=%s: %v", usage.Model, usage.Latency, err)
		return r.Reply(ctx, in, CopyParseFailed, "")
	}
	log.Printf("[channel] parsed model=%s latency=%s in=%d out=%d category=%s", usage.Model, usage.Latency.Round(time.Millisecond), usage.InputTokens, usage.OutputTokens, draft.Category)
	cents, minutes, err := r.Booker.Quote(ctx, draft)
	if err != nil {
		return fmt.Errorf("start draft: quote: %w", err)
	}
	conv.Pending = &PendingDraft{Draft: draft, Source: source, QuoteCents: cents, QuoteMinutes: minutes, CreatedAtUnix: r.now().Unix()}

	if !id.Linked() && conv.PendingName == "" {
		conv.State = StateAwaitingName
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("startDraft: %w", err)
		}
		return r.Reply(ctx, in, fmt.Sprintf(CopyAskName, draft.Title), "")
	}
	return r.showSummary(ctx, in, conv)
}

func (r *Router) showSummary(ctx context.Context, in Inbound, conv Conversation) error {
	summary := r.Summary(conv.Pending)
	conv.Pending.SummaryShown = summary
	conv.State = StateAwaitingConfirm
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
		return fmt.Errorf("showSummary: %w", err)
	}
	return r.Reply(ctx, in, summary, "")
}

func (r *Router) onName(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	if IsNo(text) {
		return r.dropDraft(ctx, in, conv)
	}
	name := CleanName(text)
	if name == "" {
		return r.Reply(ctx, in, CopyAskNameAgain, "")
	}
	conv.PendingName = name
	if conv.Pending == nil { // nothing to book; just remember the name
		conv.State = StateIdle
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("onName: %w", err)
		}
		return r.Reply(ctx, in, CopyWelcome, "")
	}
	return r.showSummary(ctx, in, conv)
}

func (r *Router) onConfirm(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	switch {
	case IsNo(text):
		return r.dropDraft(ctx, in, conv)
	case IsYes(text):
		now := r.now()
		conv.TermsAcceptedAt = &now
		if !id.Linked() {
			return r.sendSetupLink(ctx, in, id, conv, CopyOneStepToBook)
		}
		// Persist the consent before claiming — narrowly. A full save here
		// could resurrect a draft a parallel YES has already booked.
		if err := r.Conversations.AcceptTerms(ctx, id.ID, now); err != nil {
			return fmt.Errorf("onConfirm: %w", err)
		}
		_, claimed, err := r.claimAndPost(ctx, in.Channel, id, StateAwaitingConfirm, false)
		if err == nil && !claimed {
			// A parallel YES (or the page) holds the claim and is booking it.
			return r.Reply(ctx, in, CopyBookingInFlight, "")
		}
		return wrapErr("onConfirm", err)
	default:
		// Not a yes or no: treat it as a correction ("make it 4pm") and
		// re-draft with everything said so far.
		source := text
		if conv.Pending != nil {
			source = conv.Pending.Source + "\n" + text
		}
		return r.startDraft(ctx, in, id, conv, source)
	}
}

func (r *Router) onAwaitingSetup(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	if IsNo(text) {
		return r.dropDraft(ctx, in, conv)
	}
	recent, err := r.Identities.SetupTokenIssuedSince(ctx, id.ID, r.now().Add(-resendAfter))
	if err != nil {
		return fmt.Errorf("onAwaitingSetup: %w", err)
	}
	if recent {
		return r.Reply(ctx, in, CopySetupAlreadySent, "")
	}
	return r.sendSetupLink(ctx, in, id, conv, CopyOneStepToBookAgain)
}

func (r *Router) dropDraft(ctx context.Context, in Inbound, conv Conversation) error {
	conv.Pending = nil
	conv.State = StateIdle
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
		return fmt.Errorf("dropDraft: %w", err)
	}
	return r.Reply(ctx, in, CopyDropped, "")
}

func (r *Router) sendSetupLink(ctx context.Context, in Inbound, id Identity, conv Conversation, copyFmt string) error {
	if r.SetupURL == nil {
		return fmt.Errorf("send setup link: SetupURL not configured")
	}
	token, hash, err := NewSetupToken()
	if err != nil {
		return fmt.Errorf("sendSetupLink: %w", err)
	}
	if err := r.Identities.CreateSetupToken(ctx, id.ID, hash, r.now().Add(SetupTokenTTL)); err != nil {
		return fmt.Errorf("sendSetupLink: %w", err)
	}
	conv.State = StateAwaitingSetup
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
		return fmt.Errorf("sendSetupLink: %w", err)
	}
	// The person gets the real link; the stored copy of the message never
	// holds the raw token (only its hash exists, in channel_setup_tokens).
	return r.Send(ctx, Outbound{
		Channel:    in.Channel,
		To:         in.From,
		Text:       fmt.Sprintf(copyFmt, r.SetupURL(token)),
		StoredText: fmt.Sprintf(copyFmt, SetupLinkPlaceholder),
	}, "")
}

// SetupLinkPlaceholder stands in for a setup link in stored messages.
const SetupLinkPlaceholder = "[setup link]"

// claimAndPost books the pending draft for a linked number exactly once and
// tells the person. Whoever loses the claim (a second YES, the page's finish
// step racing a YES) posts nothing.
func (r *Router) claimAndPost(ctx context.Context, ch Channel, id Identity, from State, async bool) (res PostResult, claimed bool, err error) {
	pending, ok, err := r.Conversations.ClaimForPosting(ctx, id.ID, from)
	if err != nil {
		return PostResult{Outcome: PostFailed}, false, fmt.Errorf("claimAndPost: %w", err)
	}
	if !ok {
		log.Printf("[channel] claim lost identity=%s from=%s — not posting", id.ID, from)
		return PostResult{Outcome: PostFailed}, false, nil
	}
	conv, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return PostResult{Outcome: PostFailed}, true, fmt.Errorf("claimAndPost: %w", err)
	}
	conv.Pending = pending
	res = r.Booker.Post(ctx, id.UserID, pending.Draft)
	log.Printf("[channel] post identity=%s outcome=%d task=%s", id.ID, res.Outcome, res.TaskID)
	return res, true, r.afterPost(ctx, ch, id, conv, res, async)
}

// afterPost moves the conversation out of StatePosting and replies.
func (r *Router) afterPost(ctx context.Context, ch Channel, id Identity, conv Conversation, res PostResult, async bool) error {
	out := Outbound{Channel: ch, To: id.Address}
	switch res.Outcome {
	case PostBooked:
		conv.Pending = nil
		conv.State = StateIdle
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			log.Printf("[channel] save after booking identity=%s: %v", id.ID, err)
		}
		out.Text = CopyBooked
		return r.dispatch(ctx, async, func(c context.Context) error { return r.Send(c, out, res.TaskID) })
	case PostNeedsCard:
		in := Inbound{Channel: ch, From: id.Address}
		return r.dispatch(ctx, async, func(c context.Context) error { return r.sendSetupLink(c, in, id, conv, CopyNeedCard) })
	case PostOwesBalance, PostDeclined:
		// Keep the draft: once the card or balance is sorted, YES retries.
		conv.State = StateAwaitingConfirm
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			log.Printf("[channel] save after post identity=%s: %v", id.ID, err)
		}
		out.Text = firstNonEmpty(res.Message, CopyCardProblem) + " " + CopyRetryYes
		return r.dispatch(ctx, async, func(c context.Context) error { return r.Send(c, out, "") })
	case PostNeeds3DS:
		conv.State = StateHandoff
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			log.Printf("[channel] save after post identity=%s: %v", id.ID, err)
		}
		out.Text = Copy3DSHandoff
		return r.dispatch(ctx, async, func(c context.Context) error { return r.Send(c, out, res.TaskID) })
	default:
		conv.State = StateAwaitingConfirm
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			log.Printf("[channel] save after post identity=%s: %v", id.ID, err)
		}
		out.Text = CopyPostFailed
		return r.dispatch(ctx, async, func(c context.Context) error { return r.Send(c, out, "") })
	}
}

// FinishSetup runs when the setup page completes for userID: every linked
// number waiting on setup gets its pending task posted. Returns how many
// tasks were booked and the outcome of the last one, for the page.
func (r *Router) FinishSetup(ctx context.Context, userID string) (booked int, last PostResult, err error) {
	convs, ids, err := r.Conversations.ConversationsForUser(ctx, userID)
	if err != nil {
		return 0, PostResult{}, fmt.Errorf("FinishSetup: %w", err)
	}
	last = PostResult{Outcome: PostFailed}
	for i, c := range convs {
		if c.State != StateAwaitingSetup || c.Pending == nil {
			continue
		}
		res, claimed, err := r.claimAndPost(ctx, ids[i].Channel, ids[i], StateAwaitingSetup, true)
		if err != nil && !claimed {
			// Could not even claim (a database failure): the page must not
			// say "all set" for a booking that was never attempted.
			return booked, last, fmt.Errorf("FinishSetup: identity %s: %w", ids[i].ID, err)
		}
		if err != nil {
			// Claimed and posted; only the follow-up (save/reply) failed.
			log.Printf("[channel] finish setup identity=%s: %v", ids[i].ID, err)
		}
		if !claimed {
			continue
		}
		last = res
		if res.Outcome == PostBooked {
			booked++
		}
	}
	return booked, last, nil
}

// PendingFor returns the draft a linked user is about to book, for the page.
func (r *Router) PendingFor(ctx context.Context, identityID string) *PendingDraft {
	c, err := r.Conversations.GetConversation(ctx, identityID)
	if err != nil {
		// The page still works without the booking line; the failure is logged.
		log.Printf("[channel] pending for identity=%s: %v", identityID, fmt.Errorf("PendingFor: %w", err))
		return nil
	}
	return c.Pending
}

// Reply answers an inbound message on its own channel.
func (r *Router) Reply(ctx context.Context, in Inbound, text, taskID string) error {
	return r.Send(ctx, Outbound{Channel: in.Channel, To: in.From, Text: text}, taskID)
}

// Send delivers one message and records it. A send failure is returned so the
// inbound row stays unprocessed for the recovery sweep.
func (r *Router) Send(ctx context.Context, out Outbound, taskID string) error {
	a, ok := r.Adapters[out.Channel]
	if !ok {
		return fmt.Errorf("send: no adapter for channel %q", out.Channel)
	}
	pid, err := a.Send(ctx, out)
	if err != nil {
		return fmt.Errorf("send to %s: %w", Redact(out.To), err)
	}
	if err := r.Messages.RecordOutbound(ctx, out, pid, taskID); err != nil {
		// Sent but not recorded: the person got the message; only reply-to
		// resolution for it is lost. Log, don't resend.
		log.Printf("[channel] record outbound id=%s: %v", pid, err)
	}
	log.Printf("[channel] outbound channel=%s id=%s to=%s len=%d", out.Channel, pid, Redact(out.To), len(out.Text))
	return nil
}

func (r *Router) loc() *time.Location {
	if r.Location != nil {
		return r.Location
	}
	return time.UTC
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
