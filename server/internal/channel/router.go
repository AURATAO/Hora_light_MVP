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
// PostOptions carries the consents gathered in the chat alongside the draft.
type PostOptions struct {
	// BudgetCents is the shopping ceiling the person agreed to (0 = none).
	BudgetCents int
	// AutoExtend: may the supporter run up to the auto-extend window past
	// the estimate without asking. Always sent explicitly.
	AutoExtend bool
}

type Booker interface {
	// Quote prices a draft with the budget the hold must also cover.
	Quote(ctx context.Context, d llm.TaskDraft, budgetCents int) (cents, minutes int, err error)
	Post(ctx context.Context, userID string, d llm.TaskDraft, opts PostOptions) PostResult
	// ApplyChatProfile writes the name and terms consent gathered in the chat
	// onto the user's profile (fills blanks only; never overwrites).
	ApplyChatProfile(ctx context.Context, userID, name string, termsAcceptedAt *time.Time) error
	// ResolveExtension answers a supporter's request for more time or
	// budget as userID (the requester). The decision itself is made by the
	// app's own extension code; this only carries the YES/NO (M3).
	ResolveExtension(ctx context.Context, userID, extensionID string, approve bool) ExtensionResult
}

// ExtensionOutcome is what answering an extension request produced.
type ExtensionOutcome int

const (
	ExtResolved ExtensionOutcome = iota // this answer decided it
	ExtAlready                          // already answered or expired (see Status)
	ExtNotFound                         // unknown, or not this person's to answer
	ExtFailed                           // server fault
)

type ExtensionResult struct {
	Outcome ExtensionOutcome
	// Status of the request after the call: approved / denied / expired.
	Status string
	// Ask is the request in words: "15 more minutes on “Laundry pickup”".
	Ask string
	// TaskID the request belongs to, for threading the reply.
	TaskID string
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
	// Splitter separates a message asking for several errands (M3). Nil
	// means every message is one errand.
	Splitter llm.Splitter
	// Purchases is the model tiebreak for "does this errand involve buying
	// something" (budget.go). Nil = rule only.
	Purchases PurchaseChecker
	// AutoExtendMinutes is the over-run window the summary states — the
	// server's own BillingConfig value, passed in so what the person says
	// YES to is what the booked task gets. Required when the summary is
	// shown: zero is a configuration error, not a default.
	AutoExtendMinutes int
	Classifier        llm.Classifier
	Booker            Booker
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
	// Ops receives handoffs (handoff.go); Thread supplies the recent
	// conversation for them. Both nil = handoff not configured (a human
	// request then gets the holding copy and an error in the log).
	Ops    OpsNotifier
	Thread ThreadReader
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
	case StateHandoff:
		return r.onHandoff(ctx, in, id)
	case StatePosting:
		return r.Reply(ctx, in, CopyBookingInFlight, "")
	}
	// Any state where the next message is the person's to use: an explicit
	// ask for a human wins over the flow (rule-matched, never the model).
	if IsHumanRequest(text) {
		return r.handOff(ctx, in, id, conv, HandoffHumanRequested)
	}
	switch conv.State {
	case StateAwaitingName:
		return r.onName(ctx, in, id, conv, text)
	case StateAwaitingConfirm:
		return r.onConfirm(ctx, in, id, conv, text)
	case StateAwaitingSetup:
		return r.onAwaitingSetup(ctx, in, id, conv, text)
	case StateAwaitingApproval:
		return r.onAwaitingApproval(ctx, in, id, conv, text)
	case StateAwaitingBudget:
		return r.onAwaitingBudget(ctx, in, id, conv, text)
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
	// One message may ask for several errands (M3). Each is parsed and priced
	// on its own; all of them wait on the one YES.
	errands := []string{source}
	if r.Splitter != nil {
		got, usage, err := r.Splitter.SplitErrands(ctx, source)
		if err != nil {
			// The whole message is one errand (SplitErrands' own fallback).
			log.Printf("[channel] split fell back to one errand model=%s: %v", usage.Model, err)
		}
		if len(got) > 0 {
			errands = got
		}
		log.Printf("[channel] split model=%s latency=%s errands=%d", usage.Model, usage.Latency.Round(time.Millisecond), len(errands))
	}
	if len(errands) > llm.MaxErrands {
		return r.Reply(ctx, in, fmt.Sprintf(CopyTooManyErrands, llm.MaxErrands), "")
	}
	prompt := r.Prompt(r.now().In(r.loc()))
	var items []DraftItem
	var unparsed []string
	for _, text := range errands {
		draft, usage, err := r.Parser.ParseTask(ctx, prompt, text)
		if err != nil {
			log.Printf("[channel] parse failed model=%s latency=%s: %v", usage.Model, usage.Latency, err)
			unparsed = append(unparsed, text)
			continue
		}
		log.Printf("[channel] parsed model=%s latency=%s in=%d out=%d category=%s", usage.Model, usage.Latency.Round(time.Millisecond), usage.InputTokens, usage.OutputTokens, draft.Category)
		it := DraftItem{Draft: draft}
		if r.needsPurchase(ctx, text, draft) {
			it.NeedsBudget = true
			// An amount in the person's own words settles it without a
			// question ("budget 60", "$20 for the bagels").
			if cents, ok := AmountCents(text); ok {
				it.BudgetCents, it.BudgetAsked = cents, true
			}
		}
		cents, minutes, err := r.Booker.Quote(ctx, draft, it.BudgetCents)
		if err != nil {
			return fmt.Errorf("start draft: quote: %w", err)
		}
		it.QuoteCents, it.QuoteMinutes = cents, minutes
		items = append(items, it)
	}
	if len(items) == 0 {
		if r.lastReplyWasParseFailure(ctx, id) {
			return r.handOff(ctx, in, id, conv, HandoffNotUnderstood)
		}
		return r.Reply(ctx, in, CopyParseFailed, "")
	}
	p := &PendingDraft{Source: source, CreatedAtUnix: r.now().Unix(), Unparsed: unparsed, BudgetAskIndex: -1}
	if conv.Pending != nil {
		// A correction re-drafts; the opt-out they already gave stands.
		p.NoAutoExtend = conv.Pending.NoAutoExtend
	}
	p.SetItems(items)
	conv.Pending = p

	if !id.Linked() && conv.PendingName == "" {
		conv.State = StateAwaitingName
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("startDraft: %w", err)
		}
		return r.Reply(ctx, in, fmt.Sprintf(CopyAskName, p.Headline()), "")
	}
	return r.continueDraft(ctx, in, id, conv)
}

func (r *Router) showSummary(ctx context.Context, in Inbound, conv Conversation) error {
	if r.AutoExtendMinutes <= 0 && !conv.Pending.NoAutoExtend {
		// The summary would state a consent with no figure. Configuration
		// error: fail the route (retried) rather than misstate exposure.
		return fmt.Errorf("showSummary: AutoExtendMinutes not configured")
	}
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
	return r.continueDraft(ctx, in, id, conv)
}

func (r *Router) onConfirm(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	switch {
	case IsNo(text):
		return r.dropDraft(ctx, in, conv)
	case IsNoAutoExtend(text):
		// Opt out of the over-run consent the summary states; same price,
		// re-shown so the YES that follows is to the terms they chose.
		if conv.Pending != nil {
			conv.Pending.NoAutoExtend = true
		}
		return r.showSummary(ctx, in, conv)
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
	// Every errand in order. The first one that does not book stops the run;
	// it and the ones after it stay pending so a later YES retries exactly
	// those — never the ones already booked.
	items := pending.Items()
	booked := 0
	for i, it := range items {
		res = r.Booker.Post(ctx, id.UserID, it.Draft, PostOptions{BudgetCents: it.BudgetCents, AutoExtend: !pending.NoAutoExtend})
		log.Printf("[channel] post identity=%s item=%d/%d outcome=%d task=%s", id.ID, i+1, len(items), res.Outcome, res.TaskID)
		switch res.Outcome {
		case PostBooked:
			booked++
			continue
		case PostNeeds3DS:
			// The task EXISTS (parked until the bank confirms), so this item
			// is spent: a later YES must never book it again. Only the items
			// after it stay pending.
			if i+1 < len(items) {
				pending.SetItems(items[i+1:])
			} else {
				conv.Pending = nil
			}
		default:
			// Declined / no card / balance / failure: no task was created
			// (createTask discards it), so this item and the rest wait for
			// a retry.
			pending.SetItems(items[i:])
		}
		break
	}
	return res, true, r.afterPost(ctx, ch, id, conv, res, booked, len(items), async)
}

// afterPost moves the conversation out of StatePosting and replies.
func (r *Router) afterPost(ctx context.Context, ch Channel, id Identity, conv Conversation, res PostResult, booked, total int, async bool) error {
	out := Outbound{Channel: ch, To: id.Address}
	// Some of several booked before one failed: say so before the problem.
	partial := ""
	if booked > 0 && booked < total {
		partial = fmt.Sprintf(CopyBookedSome, booked, total)
	}
	switch res.Outcome {
	case PostBooked:
		conv.Pending = nil
		conv.State = StateIdle
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			log.Printf("[channel] save after booking identity=%s: %v", id.ID, err)
		}
		out.Text = CopyBooked
		if total > 1 {
			out.Text = fmt.Sprintf(CopyBookedAll, total)
		}
		return r.dispatch(ctx, async, func(c context.Context) error { return r.Send(c, out, res.TaskID) })
	case PostNeedsCard:
		in := Inbound{Channel: ch, From: id.Address}
		return r.dispatch(ctx, async, func(c context.Context) error { return r.sendSetupLink(c, in, id, conv, partial+CopyNeedCard) })
	case PostOwesBalance, PostDeclined:
		// Keep the draft: once the card or balance is sorted, YES retries.
		conv.State = StateAwaitingConfirm
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			log.Printf("[channel] save after post identity=%s: %v", id.ID, err)
		}
		out.Text = partial + firstNonEmpty(res.Message, CopyCardProblem) + " " + CopyRetryYes
		return r.dispatch(ctx, async, func(c context.Context) error { return r.Send(c, out, "") })
	case PostNeeds3DS:
		conv.State = StateHandoff
		conv.PendingExtensionID = ""
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			log.Printf("[channel] save after post identity=%s: %v", id.ID, err)
		}
		out.Text = partial + Copy3DSHandoff
		// Ops notice and the person's reply both off the request path when
		// the caller is an HTTP request (S-32).
		return r.dispatch(ctx, async, func(c context.Context) error {
			if err := r.notifyOps(c, id, HandoffPayment3DS, false); err != nil {
				log.Printf("[channel] 3ds handoff identity=%s: %v", id.ID, err)
			}
			return r.Send(c, out, res.TaskID)
		})
	default:
		conv.State = StateAwaitingConfirm
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			log.Printf("[channel] save after post identity=%s: %v", id.ID, err)
		}
		out.Text = partial + CopyPostFailed
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
