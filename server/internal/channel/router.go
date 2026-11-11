package channel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"

	"hora-auth/internal/channel/llm"
	"hora-auth/internal/tracing"
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
	// QuotedCents is the estimate the person said YES to. The post is
	// refused (PostPriceChanged) if it would now price higher — a surge
	// window opening since the summary; lower or equal books. 0 = no ceiling.
	QuotedCents int
	// BookingKey is the errand's idempotency key (DraftItem.BookingKey): a
	// post with a key that already made a task returns that task, and never
	// places a second hold.
	BookingKey string
	// Notes are the person's words for the supporter (notes.go), added to
	// the task description.
	Notes []string
}

type Booker interface {
	// Quote prices a draft with the budget the hold must also cover.
	Quote(ctx context.Context, d llm.TaskDraft, budgetCents int) (cents, minutes int, err error)
	// CheckArea says whether every stop of a draft is inside the service area,
	// by POST /tasks's own check. Rules on the geocoding result: the model's
	// part ends at reading the address out of the message.
	CheckArea(ctx context.Context, d llm.TaskDraft) AreaCheck
	Post(ctx context.Context, userID string, d llm.TaskDraft, opts PostOptions) PostResult
	// ApplyChatProfile writes the name and terms consent gathered in the chat
	// onto the user's profile (fills blanks only; never overwrites).
	ApplyChatProfile(ctx context.Context, userID, name string, termsAcceptedAt *time.Time) error
	// ResolveExtension answers a supporter's request for more time or
	// budget as userID (the requester). The decision itself is made by the
	// app's own extension code; this only carries the YES/NO (M3).
	ResolveExtension(ctx context.Context, userID, extensionID string, approve bool) ExtensionResult
	// ExtensionPending: the request still awaits the requester's answer
	// (a deferred question is sent only then — D-17 I-O).
	ExtensionPending(ctx context.Context, userID, extensionID string) (bool, error)
	// ItemQuestion is the request when it is an item question (kind=item,
	// answered 1 or 2 — item.go); ok=false for any other kind.
	ItemQuestion(ctx context.Context, userID, extensionID string) (PendingApproval, bool, error)
	// OpenTasks are the requester's posted tasks that can still be cancelled
	// (newest first).
	OpenTasks(ctx context.Context, userID string) ([]TaskRef, error)
	// CancelQuote prices cancelling one now, by the app's own cancellation
	// policy (cancellationPreview).
	CancelQuote(ctx context.Context, userID, taskID string) (CancelQuote, error)
	// Cancel cancels it through the app's own cancel path.
	// Cancel runs the app's own cancel with the reason code the person
	// chose (CancelReasonChoices) — relayed to the supporter exactly as an
	// app cancel with that reason is.
	Cancel(ctx context.Context, userID, taskID, reasonCode string) CancelResult
	// BookedTask is the posted task (not one parked on a payment step) that
	// a booking key made for userID; "" when none. Recovery of a booking
	// whose outcome was lost reads it (posting.go).
	BookedTask(ctx context.Context, userID, bookingKey string) (string, error)
}

// AreaOutcome is what the service-area check said about a draft. The zero
// value refuses: nothing books on an answer nobody gave.
type AreaOutcome int

const (
	AreaUnclear      AreaOutcome = iota // a stop can't be placed, or could be more than one place
	AreaInside                          // every stop is in the five boroughs
	AreaOutside                         // a stop is outside them
	AreaUnavailable                     // the check itself failed; nothing is known
	AreaWhichBorough                    // a stop with no city exists in several boroughs
)

// areaOutcomeName is the outcome's name for the trace (not a String
// method: that would change how existing log lines print it).
func areaOutcomeName(o AreaOutcome) string {
	switch o {
	case AreaInside:
		return "inside"
	case AreaOutside:
		return "outside"
	case AreaUnavailable:
		return "unavailable"
	case AreaWhichBorough:
		return "which_borough"
	}
	return "unclear"
}

// AreaCheck: Stop is the address that decided anything but Inside. Resolved,
// on Inside: the draft's stops in order, as the check found them.
type AreaCheck struct {
	Outcome  AreaOutcome
	Stop     string
	Resolved []string
}

// CancelQuote is what cancelling a task now would charge.
type CancelQuote struct {
	Title        string
	ChargeCents  int
	BaseFeeCents int
	Committed    bool // a supporter has accepted
	WithinGrace  bool // accepted moments ago: still free
	// Moving: the charge may change between this quote and the cancel
	// itself — a work session is running (every minute adds to it), or the
	// free window is about to close. The chat then quotes CeilingCents
	// ("up to $X"); with no ceiling (CeilingCents 0 while Moving: no time
	// cap on the task) the cancel is sent to the app, which shows it live.
	Moving bool
	// SessionOpen: a supporter is clocked in right now (the copy says so
	// only when true — the free window closing alone is not "working").
	SessionOpen bool
	// CeilingCents: the most the cancel can cost while Moving — the charge
	// with the running session taken to the agreed time cap (where the
	// clock stops), or the committed charge once the free window closes.
	// The chat quotes this ("up to $X"); a YES is consent to it, and the
	// cancel bills the real amount, never more (Arbiter 2026-10-25 21:58).
	CeilingCents int
}

// CancelResult is what a cancel did.
type CancelResult struct {
	OK        bool
	BillCents int
	Gone      bool // already closed or not this person's
	// BillUnknown: cancelled, but the charge could not be read back — the
	// reply must not say "no charge".
	BillUnknown bool
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
	PostBooked         PostOutcome = iota // task created (and held, when payments are on)
	PostNeedsCard                         // 402 payment_method_required
	PostNeeds3DS                          // 402 payment_authentication_required
	PostDeclined                          // 402 payment_required — card refused
	PostOwesBalance                       // 403 outstanding_balance
	PostFailed                            // anything else
	PostOutOfArea                         // 422 outside_service_area
	PostAddressUnclear                    // 422 address_unclear
	PostPriceChanged                      // 409 quote_changed — prices above the YES'd estimate; nothing written
	PostReconfirm                         // not posted: the summary is stale or its time has passed (posting.go)
)

type PostResult struct {
	Outcome PostOutcome
	TaskID  string
	// Message is the server's own wording for a refusal, when it has one;
	// for PostAddressUnclear, the address that could not be placed.
	Message string
	// QuoteCents: for PostPriceChanged, what the errand prices at now.
	QuoteCents int
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
	// Chat and Tasks bridge the task chat (relay.go). Chat nil = no relay:
	// a linked requester's small talk gets CopyAck as before.
	Chat  TaskChat
	Tasks TaskLookup
	// Live answers "where is he?" from the app's live tracking (D-12);
	// optional — without it the question is relayed as any other.
	Live LiveLookup
	// whereRelayed: when a "where is he?" was last relayed per
	// (identity|task) — a cooldown, in memory.
	whereMu      sync.Mutex
	whereRelayed map[string]time.Time
	// InContext labels a message when a supporter is on the job (D-17 P2);
	// nil = the plain Classifier with "to the supporter" as the default.
	InContext llm.ContextClassifier
	// QuestionReader tells an answer to a draft's question from a question
	// asked back, when the rules cannot (answers.go). Nil: rules only.
	QuestionReader llm.QuestionReader
	// Scope reads words the scope rules leave (scope.go): "allow" or a
	// refusal id, on the small model. Nil: the rules alone.
	Scope llm.ScopeChecker
	// SupporterApplyURL is the supporter application form (supporter.go;
	// SUPPORTER_APPLY_URL, default DefaultSupporterApplyURL). Supporters
	// reads an account's supporter status; nil = nobody is known to be one.
	SupporterApplyURL string
	Supporters        SupporterStatusLookup
	// Agent, when set, replaces InContext (D-18, behind CHANNEL_AGENT): it
	// proposes what a message is; the rules decide. nil = today's routing.
	Agent llm.Agent
	// OptOuts keeps the text opt-out mark (optout.go). Nil = off: opt-out
	// keywords are read as any other words, and sends are not checked.
	OptOuts OptOutStore
	// Go runs a reply off the request path when the caller is an HTTP
	// request (S-32: the setup page's finish step must not wait on
	// Sendblue/Meta). Default `go f()`; tests run it inline.
	Go func(f func())
	// Discovery finds places for an errand (D-23, discovery.go; behind
	// CHANNEL_DISCOVERY). Places looks a chosen place up again, History
	// reads the person's own past errands (the usual), DiscoveryStore keeps
	// the open offer and the daily Places count. Discovery nil = off.
	Discovery       llm.Discoverer
	Places          llm.PlaceSearcher
	DiscoveryStore  DiscoveryStore
	History         TaskHistory
	DiscoveryLimits DiscoveryLimits
	// Errand playbooks (D-25, answers.go; behind CHANNEL_PLAYBOOKS).
	// Playbooks nil = off. Facts looks an option up again with what an
	// answer may use; Answerer writes an answer from Google's summaries or
	// web excerpts (checked by rule); Web searches the web (nil = off);
	// Geocode places the customer's address for "which is closest".
	Playbooks *llm.PlaybookSet
	Facts     llm.FactSource
	Answerer  llm.Answerer
	Web       llm.WebSearcher
	WebLimits WebLimits
	Geocode   func(ctx context.Context, addr string) (lat, lng float64, err error)
	// SummaryTTL: how long a booking summary stays bookable (posting.go).
	// A YES — or the setup page finishing — after it re-quotes instead of
	// booking. Zero = DefaultSummaryTTL (CHANNEL_SUMMARY_TTL overrides).
	SummaryTTL time.Duration
	// Photos and Blobs keep the photos people text us (photos.go): the rows
	// and the private bucket. Either nil = photos are not taken (the old
	// "I can only read text" reply).
	Photos PhotoStore
	Blobs  PhotoBlobs
	// Voice rewrites the free words of each reply in HO:RA's voice (D-28,
	// voice.go; behind CHANNEL_VOICE). Nil = off: every reply is the copy
	// as written. VoiceTimeout: how long a reply waits for its rewrite
	// before the original is sent (0 = DefaultVoiceTimeout).
	Voice        llm.Voicer
	VoiceTimeout time.Duration
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
	words := customerWords(ctx)
	run(func() {
		c, cancel := context.WithTimeout(withCustomerWords(context.Background(), words), 60*time.Second)
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
	// The trace's session is the conversation, its user the account; never
	// the number (D-22).
	tracing.WithIdentity(ctx, id.UserID, id.ID)
	// Bookkeeping failures are logged, not fatal: the person still deserves
	// an answer, and neither row gates anything money-related.
	if err := r.Identities.AttachMessage(ctx, in.Channel, in.ProviderMessageID, id.ID); err != nil {
		log.Printf("[channel] attach message id=%s: %v", in.ProviderMessageID, err)
	}
	// STOP / START and a stopped number, ahead of every other rule: a
	// carrier keyword is never an answer to anything (optout.go).
	if handled, err := r.handleOptOut(ctx, in, id); handled {
		if err := r.Identities.TouchConversation(ctx, id.ID, in.ReceivedAt); err != nil {
			log.Printf("[channel] touch conversation identity=%s: %v", id.ID, err)
		}
		return err
	}
	if strings.TrimSpace(in.Text) == "" && (len(in.MediaURLs) == 0 || r.linkJustSent(ctx, id)) && r.sentWithText(ctx, id.ID, in) {
		// iMessage delivers a link's preview card as one or two extra
		// messages with no text, a moment after the link itself (staging
		// 2026-10-24: a Google Maps link drew two "I can only read text"
		// replies although the link had reached the supporter). The card
		// may carry the preview image as media (second staging pass, same
		// day): with a link just sent, media without text is still the
		// card. Part of the message just handled: nothing to answer.
		if err := r.Identities.TouchConversation(ctx, id.ID, in.ReceivedAt); err != nil {
			log.Printf("[channel] touch conversation identity=%s: %v", id.ID, err)
		}
		return nil
	}
	if err := r.Identities.TouchConversation(ctx, id.ID, in.ReceivedAt); err != nil {
		log.Printf("[channel] touch conversation identity=%s: %v", id.ID, err)
	}
	conv, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return fmt.Errorf("HandleInbound: %w", err)
	}
	text := strings.TrimSpace(in.Text)
	// The voice layer reads the person's words for tone and language only.
	ctx = withCustomerWords(ctx, text)
	// Photos are downloaded and stored first, whatever happens next:
	// provider links expire (photos.go).
	hasPhotos := false
	if len(in.MediaURLs) > 0 && r.photosOn() {
		res := r.ingestPhotos(ctx, in, id)
		if len(res.saved) == 0 {
			if text == "" {
				return r.Reply(ctx, in, res.problemCopy(), "")
			}
			// The problem, then the words as usual.
			if err := r.Reply(ctx, in, res.problemCopy(), ""); err != nil {
				return err
			}
		} else {
			ctx = withPhotos(ctx, res.saved)
			hasPhotos = true
		}
	}
	if text == "" && !hasPhotos {
		return r.Reply(ctx, in, CopyTextOnly, "")
	}
	if text == "" {
		// A photo alone. Ops on the thread see it in Ops Chats; a booking
		// in flight takes it on the next draft; otherwise it is placed.
		switch conv.State {
		case StateHandoff:
			return r.onHandoff(ctx, in, id)
		case StatePosting:
			return r.Reply(ctx, in, CopyBookingInFlight, "")
		}
		_, _, err := r.routePhotos(ctx, in, id, conv, "")
		return err
	}

	if r.handoffStale(conv) {
		if IsHumanRequest(text) || IsHelpKeyword(text) {
			// Asking for a person again: ops are told afresh and the clock
			// restarts — not "I'm back" followed by "getting a teammate".
			if err := r.notifyOps(ctx, id, HandoffHumanRequested, false, handoffOpen{}); err == nil {
				held, err := r.Conversations.TouchHandoff(ctx, id.ID)
				if err != nil {
					log.Printf("[channel] handoff identity=%s: %v", id.ID, err)
				}
				if held {
					return r.Reply(ctx, in, CopyHandoffStart, "")
				}
				// Released meanwhile (the sweeper, ops): not "a teammate
				// will text you" over a thread the bot holds — the re-read
				// below routes it, and IsHumanRequest hands off afresh.
			}
		}
		// Ops never picked it up (HandoffTimeout): back to the bot first,
		// then this message is handled by the bot as usual.
		if _, err := r.AutoRelease(ctx, id, false); err != nil {
			return fmt.Errorf("HandleInbound: %w", err)
		}
		// Read again whatever happened: the sweeper or ops may have
		// released it a moment ago (review: never "teammate coming" right
		// after "I'm back").
		if conv, err = r.Conversations.GetConversation(ctx, id.ID); err != nil {
			return fmt.Errorf("HandleInbound: %w", err)
		}
	}
	switch conv.State {
	case StateHandoff:
		return r.onHandoff(ctx, in, id)
	case StatePosting:
		// A safety message still reaches ops, and a stuck booking is
		// recovered — never "one moment" forever (posting.go).
		return r.onPosting(ctx, in, id, conv, text)
	}
	return r.routeMessage(ctx, in, id, conv, text)
}

// routeMessage routes a message in any state the person's next words are
// theirs to use (not handed off, no booking in flight).
func (r *Router) routeMessage(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) (err error) {
	// A mixed message's supporter application line goes after the errand's
	// reply, once (supporter.go).
	ctx, tail := withSupporterTail(ctx)
	defer func() {
		if err == nil && tail.want {
			r.sendSupporterLine(ctx, in, id)
		}
	}()
	// Any state where the next message is the person's to use: an explicit
	// ask for a human wins over the flow (rule-matched, never the model).
	if IsHumanRequest(text) {
		return r.handOff(ctx, in, id, conv, HandoffHumanRequested)
	}
	// HELP / INFO alone: the info line, or ops when a task is under way
	// (help.go). Rule only, ahead of every reading of the words.
	if handled, err := r.routeHelp(ctx, in, id, conv, text); handled {
		return err
	}
	// Someone who wants to WORK as a supporter (supporter.go): the
	// application link, never a draft. Rule first; a mixed message goes on
	// with its errand words only. Ahead of the scope gate: "can I drive for
	// you?" asks for a job, not a ride.
	if rest, handled, err := r.routeSupporterRules(ctx, in, id, conv, text); handled {
		return err
	} else {
		text = rest
	}
	// What HO:RA does not do about people and homes (scope.go): declined
	// with a fixed reply before anything can draft, search or relay it —
	// a romantic or sexual request never reaches a supporter. Rules first,
	// the small model for words they leave.
	if handled, err := r.scopeGate(ctx, in, id, conv, text); handled {
		return err
	}
	// Photos with the words (photos.go): onto the open draft, into the
	// task chat with the words, or kept for the next draft. Only after the
	// scope check: the words that come with a photo are checked as any.
	if handled, c, err := r.routePhotos(ctx, in, id, conv, text); handled {
		return err
	} else {
		conv = c
	}
	// Inline replies (relay.go, routeReply): the bubble decides. A "yes"
	// swiped on a supporter's words never books; a YES on our prompt does.
	if handled, err := r.routeReply(ctx, in, id, conv, text); handled {
		return err
	}
	// A new booking's question is open and the message only takes the
	// request back: dropped by rule, before any model reads it (withdrawal.go;
	// production 2026-11-03 re-drafted one as "… (CANCELLED)").
	if r.isDraftWithdrawal(conv, text) {
		return r.dropDraftWith(ctx, in, conv, CopyDroppedCancelHint)
	}
	// D-17 I-P (version B): with a supporter writing in the last minutes,
	// only an explicit YES/NO answers a question of ours; "ok", "sure",
	// "👍" go to the supporter, and the person is told what still waits.
	plain := stripFiller(text) // "oh yes" is answer-ish but not strict
	casual := (IsAffirmation(plain) || IsNo(plain)) && !IsStrictYesNo(text)
	if conv.State == StateAwaitingApproval {
		casual = approvalCasual(text) // R-2/R-3's wider words; 不要啦 is strict (I-AN)
	}
	if awaitingAnswer(conv.State) && casual {
		if handled, err := r.casualToSupporter(ctx, in, id, conv, text); handled {
			return err
		}
	}
	err = r.handleState(ctx, in, id, conv, text)
	if err == nil && (len(conv.Deferred) > 0 || conv.Unconfirmed()) {
		// A question held while the thread could not take it follows the
		// reply that left the thread idle (I-O). Any other state means the
		// reply asked or re-asked a question of ours: two questions in one
		// burst invite a YES for the wrong one, so the supporter's waits
		// for the next resume point (the booking's outcome, the answer).
		if after, gerr := r.Conversations.GetConversation(ctx, id.ID); gerr == nil &&
			(after.State == StateIdle || after.Unconfirmed()) {
			r.resumeDeferred(ctx, id)
		}
	}
	return err
}

func (r *Router) handleState(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
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
	case StateAwaitingCancel:
		return r.onAwaitingCancel(ctx, in, id, conv, text)
	case StateAwaitingTaskChoice:
		return r.onAwaitingTaskChoice(ctx, in, id, conv, text)
	default:
		return r.onIdle(ctx, in, id, conv, text)
	}
}

// awaitingAnswer: states where the next bare YES/NO is an answer to us.
func awaitingAnswer(s State) bool {
	switch s {
	case StateAwaitingConfirm, StateAwaitingApproval, StateAwaitingBudget, StateAwaitingName, StateAwaitingSetup, StateAwaitingCancel:
		return true
	}
	return false
}

// classify reads what an idle message is for, on the configured small
// model (LLM_SMALL_MODEL), with the errand type when the classifier gives
// one. Rules run before it (the search and errand fast paths); this is
// what decides when they do not match.
func (r *Router) classify(ctx context.Context, text string) llm.Classification {
	c := llm.Classification{Intent: llm.HeuristicIntent(text)}
	if dc, ok := r.Classifier.(llm.DetailClassifier); ok {
		got, err := dc.ClassifyDetail(ctx, text)
		if err != nil {
			log.Printf("[channel] classify (using fallback %q): %v", got.Intent, err)
		}
		return got
	}
	if r.Classifier != nil {
		got, err := r.Classifier.Classify(ctx, text)
		if err != nil {
			log.Printf("[channel] classify (using fallback %q): %v", got, err)
		}
		c.Intent = got
	}
	return c
}

func (r *Router) onIdle(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	// A bare YES/NO with nothing pending (a late duplicate YES after the
	// booking went through, a NO to nothing) never starts a draft: "YES" is
	// not an errand, and parsing it as one would book nonsense.
	// A bare YES/NO with nothing of ours pending: an answer to a supporter
	// who just asked (relayed), otherwise an acknowledgement.
	// "Can you cancel the task?" — by rule, before anything else reads it
	// (staging 2026-10-24 answered it "Got it"; Arbiter: cancel by chat).
	if IsCancelRequest(text) && id.Linked() {
		return r.startCancel(ctx, in, id, conv)
	}
	// A plain "yes" to "find places for these on one route?" asks to find
	// them, a "no" drops the question (discovery.go) — never a booking.
	// With options shown: a "yes" to "Want me to set that up at …?" starts
	// that option's draft (its summary still asks YES before any booking);
	// "no" / "never mind" drops the options.
	if (IsYes(text) || IsNo(text) || IsDraftDecline(text)) && r.discoveryOn() {
		if o := r.freshOffer(ctx, id); o != nil {
			at := time.Unix(o.AtUnix, 0)
			if !r.supporterSpokeSince(ctx, in, id, &at) {
				switch {
				case o.Kind == offerRouteAsk && (IsYes(text) || IsNo(text)):
					if handled, err := r.answerRouteAsk(ctx, in, id, conv, text, o); handled {
						return err
					}
				case o.Kind == offerOptions && IsYes(text) && o.Suggest > 0 && o.Suggest <= len(o.Options) && (len(o.SuggestItems) > 0 || o.SuggestCheck):
					// "Want your supporter to get a Margherita…?" (answers.go)
					return r.chooseSuggested(ctx, in, id, conv, o)
				case o.Kind == offerOptions && IsYes(text) && o.Suggest > 0 && o.Suggest <= len(o.Options):
					return r.chooseOption(ctx, in, id, conv, o, o.Suggest, o.SuggestWords)
				case o.Kind == offerOptions && (IsNo(text) || IsDraftDecline(text)) && !IsYes(text):
					// Dropped; the words then go on as before (a supporter
					// who asked something still gets the "no").
					r.saveOffer(ctx, id, nil)
					log.Printf("[channel] discovery options dropped identity=%s", id.ID)
				}
			}
		}
	}
	if IsYes(text) || IsNo(text) {
		if id.Linked() {
			if handled, err := r.answerToSupporter(ctx, in, id, conv, text); handled {
				return err
			}
			if isPoliteYes(text) {
				// A thanks is for the supporter on the job, asked or not.
				return r.relayOr(ctx, in, id, conv, text, CopyAck)
			}
			return r.Reply(ctx, in, CopyAck, "")
		}
		return r.Reply(ctx, in, CopyWelcome, "")
	}
	// "The usual", a pick from places just shown, or where to look: ours,
	// unless a supporter has written since (discovery.go).
	handled, tried, err := r.routeDiscoveryFirst(ctx, in, id, conv, text)
	if handled {
		return err
	}
	// With a supporter on the job, the thread's context decides (D-17).
	if handled, err := r.routeInContext(ctx, in, id, conv, text); handled {
		return err
	}
	// "Pick up dry cleaning, buy a card, mail a package" with no place for
	// any: find them on one route, or are there addresses? (discovery.go)
	if handled, err := r.askRoute(ctx, in, id, text); handled {
		return err
	}
	// "Where can I get…", "recommend me a pizzeria": a search by rule goes
	// to discovery before the classifier (production 2026-11-06: 3.7 s of
	// classifying before a search); not discovery after all, it is
	// classified as before.
	if !tried && r.discoveryOn() && LooksLikeDiscovery(text) {
		tried = true
		if handled, err := r.discover(ctx, in, id, conv, text, r.freshOffer(ctx, id), true, false); handled {
			return err
		}
	}
	// "Pick up my dry cleaning at…", "buy and bring me…": an errand by rule,
	// drafted without the classifier (the errand fast path; a search never
	// matches it). Anything the rules cannot place, the model reads.
	if !tried && IsClearErrand(text) {
		return r.startDraft(ctx, in, id, conv, text)
	}
	// The classifier takes a second or two: the split and the parse of the
	// same words start alongside it, so an errand's draft does not wait for
	// both in turn (the 6 s budget). They read words, they decide nothing;
	// used only if the classifier says it is an errand.
	if len(strings.Fields(text)) >= 3 {
		ctx = r.prefetchDraft(ctx, text)
	}
	c := r.classify(ctx, text)
	intent := c.Intent
	// Wanting to work as a supporter, in words no rule knows: the
	// application link, never a draft. Also asked beside an errand: the
	// line follows the errand's reply.
	if intent == llm.IntentSupporter {
		return r.answerSupporter(ctx, in, id, "model", c.Referral || SupporterReferral(text))
	}
	if c.AlsoSupporter {
		wantSupporterLine(ctx)
	}
	// "Help me find…", "is there a … open until 9?" in words no rule knows:
	// the classifier's find_place is a search (production 2026-11-07: GLM's
	// new_task drafted them as errands). Not a search after all (the
	// discovery model says not_discovery): an errand, as the words were.
	if intent == llm.IntentFind {
		if !tried && r.discoveryOn() {
			if handled, err := r.discover(ctx, in, id, conv, text, r.freshOffer(ctx, id), true, false); handled {
				return err
			}
		}
		intent = llm.IntentNewTask
	}
	// A question, or a follow-up to places just shown: place discovery,
	// which answers or leaves it to the switch.
	if !tried {
		if handled, err := r.routeDiscovery(ctx, in, id, conv, text, intent); handled {
			return err
		}
	}
	switch intent {
	case llm.IntentNewTask:
		return r.startDraftOpts(ctx, in, id, conv, text, draftOpts{playbookHint: c.Playbook})
	case llm.IntentQuestion:
		// With a supporter on the job, "where are you?" is for them.
		if id.Linked() {
			return r.relayOr(ctx, in, id, conv, text, CopyAbout)
		}
		return r.Reply(ctx, in, CopyAbout, "")
	case llm.IntentGreeting:
		if id.Linked() {
			return r.relayOr(ctx, in, id, conv, text, CopyHelloAgain)
		}
		return r.Reply(ctx, in, CopyWelcome, "")
	default:
		// "good", "thanks", "ok" — a reaction to a status update, or a word
		// for the supporter when one is on the job. A customer is not
		// re-introduced to the service; a stranger is.
		if id.Linked() {
			return r.relayOr(ctx, in, id, conv, text, CopyAck)
		}
		return r.Reply(ctx, in, CopyWelcome, "")
	}
}

// startDraft parses the request, prices it and moves to the name or
// confirmation step. source is everything the person has said about this
// task so far.
func (r *Router) startDraft(ctx context.Context, in Inbound, id Identity, conv Conversation, source string) error {
	return r.startDraftOpts(ctx, in, id, conv, source, draftOpts{})
}

// leadIn is the one-line notice on the first message of a draft started
// while a supporter was on the job (D-17 P6); "" afterwards.
func (r *Router) leadIn(p *PendingDraft) string {
	if p == nil || (p.WhileActive == "" && p.Intro == "") || p.WhileActiveShown {
		return ""
	}
	p.WhileActiveShown = true
	if p.WhileActive == "" {
		return p.Intro + "\n\n" // "Same as last time:" (discovery.go)
	}
	return fmt.Sprintf(CopyDraftWhileActive, p.WhileActive) + "\n\n"
}

// draftOpts: how a draft is started besides the person's words.
type draftOpts struct {
	// whileActive is the supporter's first name when the draft is started
	// with that supporter on the job.
	whileActive string
	// places: the place(s) chosen from a discovery answer, in visiting
	// order (discovery.go) — attached by rule after the parse, never
	// through the parser's words. check: the errand is to go and look.
	// Both are kept on the draft and re-applied on every re-draft.
	places []ChosenPlace
	check  bool
	// notes: words for the supporter added to the draft's notes (a
	// question asked with a pick — discovery.go).
	notes []string
	// prebuilt: the errand is already a draft (the usual — discovery.go):
	// not parsed, priced and checked like any other. intro: said once
	// before its first question.
	prebuilt *llm.TaskDraft
	intro    string
	// playbook: the errand type discovery recognised (D-25), "" none.
	playbook string
	// playbookHint: the type the classifier named, used only when no
	// playbook rule matches the words (rules first, the model otherwise).
	playbookHint string
}

// startDraftOpts parses the request, prices it and moves to the name or
// confirmation step.
func (r *Router) startDraftOpts(ctx context.Context, in Inbound, id Identity, conv Conversation, source string, o draftOpts) error {
	whileActive := o.whileActive
	if r.Parser == nil || r.Prompt == nil || r.Booker == nil {
		return fmt.Errorf("start draft: router not fully configured")
	}
	// Each message was checked as it came (scopeGate); everything said for
	// this draft, together, is checked again by rule ("come over" then
	// "keep me company" in two messages). No model call here.
	if ref := refusedByRule(source); ref != nil && o.prebuilt == nil {
		return r.refuseScope(ctx, in, id, conv, llm.ScopeDecision{Refusal: ref, Decided: true, By: "refuse:" + ref.ID + " (draft words)"})
	}
	// A draft from a discovery choice keeps its places through every
	// re-draft (a correction re-parses the words, not the place).
	if prev := conv.Pending; prev != nil && len(o.places) == 0 && o.prebuilt == nil {
		o.places, o.check = prev.Places, prev.Check
	}
	// One message may ask for several errands (M3). Each is parsed and priced
	// on its own; all of them wait on the one YES. Chosen places make one
	// errand: one supporter for the whole trip.
	errands := []string{source}
	var pre *draftPrefetch
	if o.prebuilt == nil {
		// The parse of the whole words runs alongside the split: one errand
		// (the common case) is parsed from everything said anyway.
		pre = r.prefetched(ctx, source, len(o.places) == 0)
	}
	if r.Splitter != nil && len(o.places) == 0 && o.prebuilt == nil {
		sr := pre.splitResult()
		got, usage, err := sr.errands, sr.usage, sr.err
		if err != nil {
			// The whole message is one errand (SplitErrands' own fallback).
			log.Printf("[channel] split fell back to one errand model=%s: %v", usage.Model, err)
		}
		if len(got) > 1 {
			errands = got
		}
		// One errand: parse everything said, not the splitter's copy of it.
		// The splitter keeps only the errand's words and drops follow-ups
		// that are not errands ("15 mins", "ASAP"), so a correction never
		// reached the parser or namesDuration (staging 2026-10-30: "15 mins"
		// said three times, the draft stayed at 30).
		log.Printf("[channel] split model=%s latency=%s errands=%d", usage.Model, usage.Latency.Round(time.Millisecond), len(errands))
	}
	if len(errands) > llm.MaxErrands {
		return r.Reply(ctx, in, fmt.Sprintf(CopyTooManyErrands, llm.MaxErrands), "")
	}
	prompt := r.Prompt(r.now().In(r.loc()))
	pb := r.draftPlaybook(o, conv.Pending, source)
	if len(errands) > 1 {
		pb = nil // several errands: no one type's questions
	}
	var items []DraftItem
	var unparsed []string
	// A correction re-drafts from everything said; a budget the person
	// already gave stands unless this message names a new amount (staging
	// 2026-10-24: "can you do 15 mins?" asked for the budget again).
	var prev []DraftItem
	if conv.Pending != nil {
		prev = conv.Pending.Items()
	}
	for i, text := range errands {
		var draft llm.TaskDraft
		if o.prebuilt != nil {
			draft = *o.prebuilt
		} else {
			var usage llm.Usage
			var err error
			if len(errands) == 1 && pre != nil && text == pre.source {
				pr := pre.parseResult() // started with the split
				draft, usage, err = pr.draft, pr.usage, pr.err
			} else {
				draft, usage, err = r.Parser.ParseTask(ctx, prompt, text)
			}
			if err != nil {
				log.Printf("[channel] parse failed model=%s latency=%s: %v", usage.Model, usage.Latency, err)
				unparsed = append(unparsed, text)
				continue
			}
			log.Printf("[channel] parsed model=%s latency=%s in=%d out=%d category=%s", usage.Model, usage.Latency.Round(time.Millisecond), usage.InputTokens, usage.OutputTokens, draft.Category)
		}
		if o.prebuilt != nil {
			// The usual: the past errand's own length stands.
		} else if !namesDuration(text) {
			// Default 30 minutes unless the person says otherwise (Arbiter
			// 2026-10-24 22:48: the model guessed 15 for "a pack of eggs";
			// the person then had to correct it). The Booker's default.
			draft.DurationMinutes = 0
		}
		if o.prebuilt == nil && draft.DurationMinutes > 0 && !saidMinutesIn(source, draft.DurationMinutes) {
			// A length the person never wrote — the model's reading of its
			// own copy of the errand (gap C, ownwords.go): the default
			// estimate instead.
			log.Printf("[channel] draft identity=%s: %d minutes not in the person's words — default estimate", id.ID, draft.DurationMinutes)
			draft.DurationMinutes = 0
		}
		if len(o.places) > 0 {
			applyPlaces(&draft, o.places, o.check, namesDuration(source))
		}
		applyPlaybookNotes(&draft, pb)
		if pb != nil && pb.Category != "" {
			// Public companionship is priced as companionship, whatever
			// the parser filed it under.
			draft.Category = pb.Category
		}
		if n := r.homeEntryNote(text); n != "" && !strings.Contains(draft.Description, n) {
			draft.Description = strings.TrimSpace(strings.TrimSpace(draft.Description) + "\n\n" + n)
		}
		if len(errands) == 1 && o.prebuilt == nil {
			// The newest line that is only a duration ("15 mins", "ASAP 15
			// mins", "make it 45 minutes") sets it, whatever the model read
			// from older lines (review 2026-10-30).
			// Newest first, so "15 mins" then "make it 4pm" keeps 15.
			lines := strings.Split(source, "\n")
			for j := len(lines) - 1; j >= 0; j-- {
				if m, ok := DurationOnly(lines[j]); ok {
					draft.DurationMinutes = m
					break
				}
			}
		}
		it := DraftItem{Draft: draft}
		if r.needsPurchase(ctx, text, draft) {
			it.NeedsBudget = true
			// An amount in the person's own words settles it without a
			// question ("budget 60", "$20 for the bagels").
			// On a correction the newest line wins: the source is every line
			// said, and its first amount is the old one (review 10/24).
			latestCents, latestNamed := AmountCents(latestLine(source))
			switch {
			case len(prev) > 0 && len(errands) == 1 && latestNamed:
				it.BudgetCents, it.BudgetAsked = latestCents, true
			default:
				// With several errands, text is the splitter's copy: its
				// amount stands only if the person wrote it (gap C); if not,
				// the budget question is asked.
				if cents, ok := LatestAmountCents(text); ok {
					if (cents == 0 && saidNoBudget(source)) || (cents > 0 && saidAmount(source, cents/100)) {
						it.BudgetCents, it.BudgetAsked = cents, true
					} else {
						log.Printf("[channel] draft identity=%s: errand %d's budget not in the person's words — asking", id.ID, i+1)
					}
				}
			}
			// A budget already given carries over to a correction of the
			// same kind of errand (Arbiter 2026-10-24 22:48: asked once,
			// never again) — not to a different kind now at that position.
			if !it.BudgetAsked && !latestNamed && len(prev) == len(errands) && prev[i].BudgetAsked && sameErrand(prev[i].Draft, draft) &&
				(len(errands) == 1 || categoryCount(prev, draft.Category) == 1) {
				it.BudgetCents, it.BudgetAsked = prev[i].BudgetCents, true
			}
		}
		cents, minutes, err := r.Booker.Quote(ctx, draft, it.BudgetCents)
		if err != nil {
			return fmt.Errorf("start draft: quote: %w", err)
		}
		it.QuoteCents, it.QuoteMinutes = cents, minutes
		items = append(items, it)
	}
	if len(prev) > 0 && statusWordAdded(prev, items) {
		// The parser read the message as a change of state ("… (CANCELLED)",
		// "booked"): it was a withdrawal. Never shown as a summary to say
		// YES to (production 2026-11-03).
		log.Printf("[channel] re-draft identity=%s: status word in title, read as a withdrawal", id.ID)
		return r.dropDraftWith(ctx, in, conv, CopyDroppedCancelHint)
	}
	if len(items) == 0 {
		if r.lastReplyWasParseFailure(ctx, id) {
			return r.handOff(ctx, in, id, conv, HandoffNotUnderstood)
		}
		return r.Reply(ctx, in, CopyParseFailed, "")
	}
	// The service area, by rule, before the person is asked anything else
	// about a task that may not be able to run. createTask checks again at
	// the post, before any hold.
	if prev := conv.Pending; prev != nil && prev.WhichBorough {
		applyBorough(items, prev.UnclearStop, latestLine(source))
	}
	var known map[string]string
	if conv.Pending != nil {
		known = conv.Pending.AreaStops
	}
	area, areaStops := r.checkArea(ctx, items, known)
	switch area.Outcome {
	case AreaOutside:
		return r.refuseOutOfArea(ctx, in, conv)
	case AreaUnavailable:
		return r.Reply(ctx, in, CopyAreaCheckDown, "")
	}
	p := &PendingDraft{Source: source, CreatedAtUnix: r.now().Unix(), Unparsed: unparsed, BudgetAskIndex: -1, WhileActive: whileActive,
		Places: o.places, Check: o.check, Intro: o.intro}
	if conv.Pending != nil && o.prebuilt == nil {
		// A correction re-drafts; the opt-out they already gave stands.
		p.NoAutoExtend = conv.Pending.NoAutoExtend
		p.Notes = conv.Pending.Notes
		p.Photos = conv.Pending.Photos
		p.WhileActive, p.WhileActiveShown = conv.Pending.WhileActive, conv.Pending.WhileActiveShown
	}
	for _, n := range o.notes {
		if n = strings.TrimSpace(n); n != "" && len(p.Notes) < MaxNotes {
			if utf8.RuneCountInString(n) > maxNoteRunes {
				n = string([]rune(n)[:maxNoteRunes]) + "…"
			}
			p.Notes = append(p.Notes, n)
		}
	}
	// A photo sent just before, with nothing open, is this errand's
	// (photos.go).
	r.claimDraftPhotos(ctx, id, p)
	if pb != nil {
		p.Playbook = pb.ID
	}
	if prev := conv.Pending; prev != nil && prev.Playbook == p.Playbook {
		// Asked once per draft — also with no type, for the home-entry
		// question alone.
		p.PlaybookAsked = prev.PlaybookAsked
	}
	p.SetItems(items)
	p.AreaStops = areaStops
	unclear := area.Outcome == AreaUnclear || area.Outcome == AreaWhichBorough
	if unclear {
		p.UnclearStop, p.WhichBorough = area.Stop, area.Outcome == AreaWhichBorough
	}
	p.DetailsAsked = needsDetails(items) || unclear
	if prev := conv.Pending; p.DetailsAsked && prev != nil && prev.DetailsAsked {
		p.DetailsTries = prev.DetailsTries + 1
	}
	conv.Pending = p
	if conv.State == StateAwaitingApproval {
		// Approval first, then the draft (Arbiter 2026-10-24): the draft is
		// kept and its next question comes back once the approval is
		// answered (ResumeState / resumeLine).
		held, err := r.holdDraft(ctx, in, id, conv)
		if held || err != nil {
			return err
		}
		// The approval ended while the draft was parsed: the draft goes
		// on as usual, on the thread as it is now.
		fresh, err := r.Conversations.GetConversation(ctx, id.ID)
		if err != nil {
			return fmt.Errorf("startDraft: %w", err)
		}
		p.NameAsked, p.BudgetAskIndex = false, -1
		fresh.Pending = p
		conv = fresh
		switch conv.State {
		case StateAwaitingApproval:
			// Another question armed in between: it waits on that one.
			return r.Reply(ctx, in, approvalStillPending(conv), "")
		case StateHandoff:
			return r.onHandoff(ctx, in, id)
		case StatePosting:
			return r.Reply(ctx, in, CopyBookingInFlight, "")
		}
	}

	if p.DetailsAsked {
		// Nothing concrete or no place: ask before any summary — a summary
		// of "book another task" was booked on staging (2026-10-24).
		if p.DetailsTries >= MaxDetailsTries {
			log.Printf("[channel] draft identity=%s: details still missing after %d answers, handed off", id.ID, p.DetailsTries)
			return r.handOff(ctx, in, id, conv, HandoffNotUnderstood)
		}
		conv.State = StateAwaitingConfirm
		lead := r.leadIn(p)
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("startDraft: %w", err)
		}
		if done, err := r.preempted(ctx, in, conv.IdentityID, p); done {
			return err
		}
		return r.Reply(ctx, in, lead+detailsQuestion(p), "")
	}
	if r.Playbooks != nil && !p.PlaybookAsked {
		// The type's questions, once per draft, before the summary
		// (playbooks.json asks): only the ones the words have not answered,
		// and the home-entry access question when a supporter goes inside.
		words := source + "\n" + strings.Join(p.Notes, "\n")
		if asks := withoutPhotoAsks(r.Playbooks.DraftAsks(pb, words), len(p.Photos) > 0); len(asks) > 0 {
			p.PlaybookAsked = true
			for _, a := range asks {
				p.PlaybookAsk = append(p.PlaybookAsk, a.Key)
			}
			conv.State = StateAwaitingConfirm
			lead := r.leadIn(p)
			if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
				return fmt.Errorf("startDraft: %w", err)
			}
			if done, err := r.preempted(ctx, in, conv.IdentityID, p); done {
				return err
			}
			log.Printf("[channel] draft identity=%s: playbook %q asks %v", id.ID, p.Playbook, p.PlaybookAsk)
			return r.Reply(ctx, in, lead+playbookAsksText(asks), "")
		}
	}
	if !id.Linked() && conv.PendingName == "" {
		conv.State = StateAwaitingName
		p.NameAsked = true
		lead := r.leadIn(p)
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("startDraft: %w", err)
		}
		if done, err := r.preempted(ctx, in, conv.IdentityID, p); done {
			return err
		}
		return r.Reply(ctx, in, lead+fmt.Sprintf(CopyAskName, p.Headline()), "")
	}
	return r.continueDraft(ctx, in, id, conv)
}

// checkArea asks about every errand. Outside wins over everything, then a
// failed check, then an unclear stop: "that's outside NYC" is final, while a
// clearer address could not change it. A stop that passes takes the address
// the check resolved, so the summary shows what was picked and the YES books
// that, not the words it was found from. A stop in known is not asked about
// again. Returns the stops that passed, for the next re-draft.
func (r *Router) checkArea(ctx context.Context, items []DraftItem, known map[string]string) (AreaCheck, map[string]string) {
	worst := AreaCheck{Outcome: AreaInside}
	passed := map[string]string{}
	for i := range items {
		d := &items[i].Draft
		if where(d.Location1, d.Location2) == "" {
			continue // no place yet: the details question asks for one
		}
		ask := *d // the stops still to check; the rest are blank
		stops, askStops := draftStops(d), draftStops(&ask)
		var open []*string
		var keys []string
		for j, s := range stops {
			key := stopKey(*s)
			found, ok := passed[key]
			if !ok {
				found, ok = known[key]
			}
			if ok {
				passed[key], *s, *askStops[j] = found, found, ""
				continue
			}
			open, keys = append(open, s), append(keys, key)
		}
		if len(open) == 0 {
			continue
		}
		actx, sp := tracing.StartSpan(ctx, "service_area.errand", tracing.KindChain)
		sp.Input(where(ask.Location1, ask.Location2))
		got := r.Booker.CheckArea(actx, ask)
		sp.OutputValue(map[string]any{"outcome": areaOutcomeName(got.Outcome), "stop": got.Stop, "resolved": got.Resolved})
		sp.End()
		switch got.Outcome {
		case AreaInside:
			if len(got.Resolved) != len(open) {
				continue
			}
			for j, s := range open {
				*s, passed[keys[j]] = got.Resolved[j], got.Resolved[j]
			}
		case AreaOutside:
			return got, nil
		case AreaUnavailable:
			worst = got
		default:
			if worst.Outcome != AreaUnavailable {
				worst = got
			}
		}
	}
	return worst, passed
}

// stopKey: the same words, however they were spaced or capitalised.
func stopKey(stop string) string {
	return strings.ToLower(strings.Join(strings.Fields(stop), " "))
}

// refuseOutOfArea: the draft ends here, with fixed copy, and every errand in
// it goes with it. An approval question still waits on its answer.
func (r *Router) refuseOutOfArea(ctx context.Context, in Inbound, conv Conversation) error {
	if conv.State == StateAwaitingApproval {
		return r.Reply(ctx, in, CopyOutOfArea+"\n\n"+approvalStillPending(conv), "")
	}
	return r.dropDraftWith(ctx, in, conv, CopyOutOfArea)
}

func draftStops(d *llm.TaskDraft) []*string {
	var stops []*string
	for _, s := range []*string{&d.Location1, &d.Location2} {
		if strings.TrimSpace(*s) != "" {
			stops = append(stops, s)
		}
	}
	return stops
}

var boroughRE = regexp.MustCompile(`(?i)\b(?:manhattan|brooklyn|queens|(?:the )?bronx|staten island)\b`)

// applyBorough: the answer to "which borough is it in?" joins the address by
// rule, whether the parser kept the old stop or read the borough as the place.
func applyBorough(items []DraftItem, stop, answer string) {
	borough := boroughRE.FindString(answer)
	if borough == "" {
		return
	}
	for i := range items {
		for _, s := range draftStops(&items[i].Draft) {
			if t := strings.TrimSpace(*s); strings.EqualFold(t, stop) || strings.EqualFold(t, borough) {
				*s = stop + ", " + borough
			}
		}
	}
}

func (r *Router) showSummary(ctx context.Context, in Inbound, conv Conversation) error {
	if conv.Pending == nil {
		// Nothing to summarise (a stale snapshot left awaiting_confirm
		// without a draft): back to idle, and say so.
		conv.State = StateIdle
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("showSummary: %w", err)
		}
		return r.Reply(ctx, in, CopyAck, "")
	}
	if conv.Pending.DetailsAsked {
		// Never a summary (a YES-able question) for a draft that still
		// lacks what or where (review 10/24 #6): the details question.
		return r.Reply(ctx, in, detailsQuestion(conv.Pending), "")
	}
	if r.AutoExtendMinutes <= 0 && !conv.Pending.NoAutoExtend {
		// The summary would state a consent with no figure. Configuration
		// error: fail the route (retried) rather than misstate exposure.
		return fmt.Errorf("showSummary: AutoExtendMinutes not configured")
	}
	return r.putSummary(ctx, in, conv, r.leadIn(conv.Pending))
}

// putSummary shows the draft's summary after lead: the prices it states are
// what a YES within SummaryTTL agrees to (posting.go).
func (r *Router) putSummary(ctx context.Context, in Inbound, conv Conversation, lead string) error {
	ensureBookingKeys(conv.Pending)
	summary := lead + r.Summary(conv.Pending)
	conv.Pending.SummaryShown = summary
	conv.Pending.SummaryShownAtUnix = r.now().Unix()
	conv.State = StateAwaitingConfirm
	shown := ShownUnconfirmed // in flight until delivered (R-1)
	conv.QuestionShownAt = &shown
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
		return fmt.Errorf("showSummary: %w", err)
	}
	if done, err := r.preempted(ctx, in, conv.IdentityID, conv.Pending); done {
		return err
	}
	if err := r.Reply(ctx, in, summary, ""); err != nil {
		return err
	}
	r.markShown(ctx, conv.IdentityID) // after delivery (R-1)
	return nil
}

func (r *Router) onName(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) error {
	if IsNo(text) {
		return r.dropDraft(ctx, in, conv)
	}
	if IsCancelRequest(text) {
		// "cancel task" is not a first name.
		return r.dropDraftWith(ctx, in, conv, CopyDroppedCancelHint)
	}
	name := CleanName(text)
	if name == "" {
		return r.Reply(ctx, in, CopyAskNameAgain, "")
	}
	if conv.Pending != nil {
		conv.Pending.NameAsked = false
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
	if conv.Pending == nil {
		// awaiting_confirm with no draft (a stale snapshot): nothing to
		// book. Back to idle, and the message is read as if idle — a real
		// errand typed now is drafted, not swallowed (round 5).
		conv.State = StateIdle
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("onConfirm: %w", err)
		}
		return r.onIdle(ctx, in, id, conv, text)
	}
	if len(conv.Pending.PlaybookAsk) > 0 {
		// The answer to the type's questions (answers.go).
		return r.answerPlaybookAsk(ctx, in, id, conv, text)
	}
	if conv.Pending.DetailsAsked {
		switch {
		case IsNo(text) || IsDraftDecline(text):
			return r.dropDraft(ctx, in, conv)
		case IsCancelRequest(text) || IsWithdrawal(text):
			// "No cancel the draft" to "what are the details?" (production
			// 2026-11-07): dropped by rule — it was re-parsed as details,
			// still had none, and the second try handed off.
			return r.dropDraftWith(ctx, in, conv, CopyDroppedCancelHint)
		case IsClearSearch(text) && r.discoveryOn():
			// A search while we wait on details: answered, the draft waits.
			if handled, err := r.routeDiscoveryWhileDraft(ctx, in, id, conv, text); handled {
				return err
			}
		case IsYes(text):
			// Nothing to say yes to yet.
			return r.Reply(ctx, in, detailsQuestion(conv.Pending), "")
		case forSupporterRE.MatchString(text) || (r.supporterSpokeSince(ctx, in, id, conv.LastOutboundAt) && !r.errandForDraft(ctx, in, id, text)):
			// For the supporter by name, or the supporter wrote after our
			// question and this is not plainly the errand: the usual
			// routing decides first (D-17). Otherwise our question is the
			// last thing said, and this is its answer.
			if handled, err := r.routeInContext(ctx, in, id, conv, text); handled {
				return err
			}
		}
		source := conv.Pending.Source + "\n" + text
		if vagueRequestRE.MatchString(conv.Pending.Source) {
			source = text // "book another thing?" carried nothing to keep
		}
		return r.startDraft(ctx, in, id, conv, source)
	}
	switch {
	case IsNo(text):
		return r.dropDraft(ctx, in, conv)
	case IsNoAutoExtend(text):
		// Opt out of the over-run consent the summary states; same price,
		// re-shown so the YES that follows is to the terms they chose.
		if conv.Pending != nil {
			conv.Pending.NoAutoExtend = true
		}
		// "no extra mins, make it 4pm": both — the opt-out above, and the
		// rest re-drafted as a correction (the opt-out carries over), so one
		// new summary shows the change on the terms they chose.
		if change := NoAutoExtendRest(text); change != "" {
			return r.startDraft(ctx, in, id, conv, conv.Pending.Source+"\n"+change)
		}
		return r.showSummary(ctx, in, conv)
	case !IsBookingYes(text) && IsAffirmation(text):
		// "book it please", "ok", "👍": agreeing, but not the plain YES a
		// booking needs (money rule). Asked again; nothing to re-draft.
		return r.Reply(ctx, in, CopyConfirmReask, "")
	case !conv.Pending.NoAutoExtend && IsExtraTimeAck(text, r.AutoExtendMinutes):
		// "15 mins is fine" to the summary's "up to 15 extra minutes": an
		// answer to that line, not a new length (extratime.go). The draft
		// stays as shown; the YES has to be plain.
		return r.Reply(ctx, in, fmt.Sprintf(CopyExtraTimeAck, r.AutoExtendMinutes), "")
	case IsBookingYes(text) && (conv.QuestionShownAt == nil || in.ReceivedAt.IsZero() || in.ReceivedAt.Before(*conv.QuestionShownAt)):
		// (No stamp: a draft shown before R-1 existed — put again once.)
		// Sent before this summary was (re)shown — the second tap of a
		// double YES that answered something else — or with no usable
		// send time: not a booking. The summary is put again (D-17 R-1;
		// no margin, Arbiter).
		log.Printf("[channel] booking identity=%s: YES sent %s before the summary, re-asked", id.ID, sentBefore(in, conv.QuestionShownAt))
		return r.showSummary(ctx, in, conv)
	case IsBookingYes(text):
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
		if IsCancelRequest(text) {
			// "I think I need to cancel this" with a draft waiting: the
			// draft is dropped — never relayed (Arbiter 2026-10-24 23:03) —
			// and the way to a posted task's cancel is named.
			return r.dropDraftWith(ctx, in, conv, CopyDroppedCancelHint)
		}
		// "Sorry no I don't need that", "never mind", "算了": a no in
		// words to the draft (staging 2026-10-30: it was re-drafted as a
		// correction, twice). Dropping a draft charges nothing and the
		// person can text again, so a no in words is enough.
		if IsDraftDecline(text) {
			return r.dropDraft(ctx, in, conv)
		}
		// "Find me a pharmacy open now" with the summary open: a search, not
		// a change to the booking — discovery answers and the draft stays as
		// it is (discovery.go). "make it 4pm" goes on below.
		if handled, err := r.routeDiscoveryWhileDraft(ctx, in, id, conv, text); handled {
			return err
		}
		// A new errand typed while the draft waits joins the draft — it is
		// not relayed to a supporter (staging 2026-10-24: "buy me a bagel"
		// went to the bagel supporter; Arbiter: draft-first).
		if r.errandForDraft(ctx, in, id, text) {
			return r.startDraft(ctx, in, id, conv, conv.Pending.Source+"\n"+text)
		}
		// With a supporter on the job, a sentence here may be for them
		// (D-17): relayed, the draft stays pending.
		handled, label, err := r.routeInContextLabel(ctx, in, id, conv, text)
		if handled {
			return err
		}
		if label == "" && r.Agent != nil {
			// No supporter on the job, so the agent was not asked above:
			// ask it now, with the booking question open, before reading
			// this as a correction. A "decline" drops the draft; an
			// "approve" is re-asked (the YES has to be plain); anything
			// else is the correction it was read as before.
			p := r.propose(ctx, id, text, nil, conv, "", "", time.Time{}, false)
			if p.Action == llm.ActionEscalate {
				return r.agentHandOff(ctx, in, id, p) // re-reads the conversation itself
			}
			if p.Action == llm.ActionInfo && r.discoveryOn() {
				// A search in words the rules did not know: answered, the
				// draft stays as it is (never re-drafted as a correction).
				if handled, err := r.discover(ctx, in, id, conv, text, nil, true, true); handled {
					return err
				}
			}
			// The model took seconds: a parallel YES may have booked the
			// draft, or a newer message replaced it (review r1). Act on
			// the conversation as it is now, or not at all.
			fresh, err := r.Conversations.GetConversation(ctx, id.ID)
			if err != nil {
				return fmt.Errorf("onConfirm: %w", err)
			}
			switch {
			case fresh.State == StatePosting:
				return r.Reply(ctx, in, CopyBookingInFlight, "") // a parallel YES is booking it
			case fresh.State != StateAwaitingConfirm || fresh.Pending == nil || conv.Pending == nil:
				// Dropped or booked meanwhile: nothing left to decline or
				// correct. Read as if idle, so a real errand is not lost.
				return r.onIdle(ctx, in, id, fresh, text)
			case fresh.Pending.CreatedAtUnix != conv.Pending.CreatedAtUnix || fresh.Pending.Source != conv.Pending.Source:
				// Replaced by a newer message: this one is a correction
				// to the draft as it is now, not lost (review r2).
				return r.startDraft(ctx, in, id, fresh, fresh.Pending.Source+"\n"+text)
			}
			conv = fresh
			switch {
			case p.Action == llm.ActionDecline && !declineCarriesChange(text):
				// A no with nothing to change in it. "no, I need it at 5"
				// or "forget it, make it laundry" carries a change: it is
				// re-drafted below, never dropped (a correction is not lost).
				// Not changeWordsRE: that one matches "to", "for", "one" —
				// "I don't need to task anymore" was re-drafted as a
				// correction (production 2026-11-03).
				return r.dropDraftWith(ctx, in, conv, CopyDroppedCancelHint)
			case p.Action == llm.ActionApprove:
				label = llm.IntentAnswer
			case p.Action == llm.ActionNote:
				label = intentNote
			}
		}
		if label == intentNote && !noteCarriesChange(text) {
			// Words for whoever runs the booking: kept as a note, the
			// summary shown again with it — time, price and addresses as
			// they were (notes.go).
			return r.addNote(ctx, in, conv, text)
		}
		if label == intentDecline && !declineCarriesChange(text) {
			// The agent read a no to the booking (with a supporter on
			// another job): dropped, never re-asked as "Reply YES".
			return r.dropDraftWith(ctx, in, conv, CopyDroppedCancelHint)
		}
		if label == llm.IntentAnswer || label == intentDecline {
			// "book it please": an answer in words — the YES has to be plain.
			return r.Reply(ctx, in, CopyConfirmReask, "")
		}
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
	if IsCancelRequest(text) {
		// "cancel task" while the link is out: the booking waiting on it is
		// dropped, as in every other drafting state — not a fresh link.
		return r.dropDraftWith(ctx, in, conv, CopyDroppedCancelHint)
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
	return r.dropDraftWith(ctx, in, conv, CopyDropped)
}

func (r *Router) dropDraftWith(ctx context.Context, in Inbound, conv Conversation, copy string) error {
	conv.Pending = nil
	conv.State = StateIdle
	if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
		return fmt.Errorf("dropDraft: %w", err)
	}
	return r.Reply(ctx, in, copy, "")
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
	// Nothing books on a stale summary, a time already past, or a price
	// above the one agreed (posting.go): asked again instead.
	if res, held := r.checkBeforePost(ctx, conv); held {
		return res, true, r.afterPost(ctx, ch, id, conv, res, 0, len(pending.Items()), async)
	}
	// Every errand in order. The first one that does not book stops the run;
	// it and the ones after it stay pending so a later YES retries exactly
	// those — never the ones already booked.
	items := pending.Items()
	booked := 0
	photosPut := false
	for i, it := range items {
		res = r.Booker.Post(ctx, id.UserID, it.Draft, PostOptions{BudgetCents: it.BudgetCents, AutoExtend: !pending.NoAutoExtend,
			QuotedCents: it.QuoteCents, BookingKey: it.BookingKey, Notes: pending.Notes})
		log.Printf("[channel] post identity=%s item=%d/%d outcome=%d task=%s", id.ID, i+1, len(items), res.Outcome, res.TaskID)
		if (res.Outcome == PostBooked || res.Outcome == PostNeeds3DS) && res.TaskID != "" && !photosPut {
			// The photos go on the first errand that made a task.
			r.attachBookedPhotos(ctx, id, pending, res.TaskID)
			photosPut = true
		}
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
		case PostPriceChanged:
			// Nothing written for this one; it and the rest are priced
			// again and asked about (afterPost → reconfirm).
			items[i].QuoteCents = res.QuoteCents
			pending.SetItems(items[i:])
		default:
			// Declined / no card / balance / failure: no task was created
			// (createTask discards it, or keeps it parked under the item's
			// booking key when the hold's outcome is unknown), so this item
			// and the rest wait for a retry — with the same keys.
			pending.SetItems(items[i:])
		}
		break
	}
	return res, true, r.afterPost(ctx, ch, id, conv, res, booked, len(items), async)
}

// afterPost moves the conversation out of StatePosting and replies. Every
// outcome is saved through FinishPosting: a thread handed to ops while the
// post ran stays theirs (handedOff), and the person is told how it went in
// words that do not ask them for a YES only ops can now act on.
func (r *Router) afterPost(ctx context.Context, ch Channel, id Identity, conv Conversation, res PostResult, booked, total int, async bool) error {
	out := Outbound{Channel: ch, To: id.Address}
	// Some of several booked before one failed: say so before the problem.
	partial := ""
	if booked > 0 && booked < total {
		partial = fmt.Sprintf(CopyBookedSome, booked, total)
	}
	switch res.Outcome {
	case PostReconfirm, PostPriceChanged:
		return r.reconfirm(ctx, ch, id, conv, res, partial, async)
	case PostBooked:
		conv.Pending = nil
		conv.State = StateIdle
		handedOff := r.finishPosting(ctx, id, conv)
		out.Text = CopyBooked
		if total > 1 {
			out.Text = fmt.Sprintf(CopyBookedAll, total)
		}
		if handedOff {
			out.Text = CopyBookedHandedOff
		}
		return r.dispatch(ctx, async, func(c context.Context) error {
			if err := r.Send(c, out, res.TaskID); err != nil {
				return err
			}
			if !handedOff {
				r.resumeDeferred(c, id) // a question held while the booking was in flight / setup
			}
			return nil
		})
	case PostOutOfArea:
		// Only a draft from before the check, or an address Google now reads
		// differently: the draft-time check stops the rest. Nothing is kept.
		conv.Pending = nil
		conv.State = StateIdle
		handedOff := r.finishPosting(ctx, id, conv)
		out.Text = partial + CopyOutOfArea
		return r.dispatch(ctx, async, func(c context.Context) error {
			if err := r.Send(c, out, ""); err != nil {
				return err
			}
			if !handedOff {
				r.resumeDeferred(c, id)
			}
			return nil
		})
	case PostAddressUnclear:
		conv.Pending.UnclearStop, conv.Pending.WhichBorough = res.Message, false
		conv.Pending.DetailsAsked = true
		conv.Pending.AreaStops = nil // the post disagreed with them
		conv.State = StateAwaitingConfirm
		out.Text = partial + detailsQuestion(conv.Pending)
		if r.finishPosting(ctx, id, conv) {
			out.Text = partial + CopyPostNotDoneHandedOff
		}
		return r.dispatch(ctx, async, func(c context.Context) error { return r.Send(c, out, "") })
	case PostNeedsCard:
		conv.State = StateAwaitingSetup
		if r.finishPosting(ctx, id, conv) {
			out.Text = partial + CopyPostNotDoneHandedOff
			return r.dispatch(ctx, async, func(c context.Context) error { return r.Send(c, out, "") })
		}
		in := Inbound{Channel: ch, From: id.Address}
		return r.dispatch(ctx, async, func(c context.Context) error { return r.sendSetupLink(c, in, id, conv, partial+CopyNeedCard) })
	case PostOwesBalance, PostDeclined:
		// Keep the draft: once the card or balance is sorted, YES retries.
		conv.State = StateAwaitingConfirm
		shown := ShownUnconfirmed // the retry prompt is a question of ours (R-1): in flight until delivered
		conv.QuestionShownAt = &shown
		handedOff := r.finishPosting(ctx, id, conv)
		out.Text = partial + firstNonEmpty(res.Message, CopyCardProblem) + " " + CopyRetryYes
		if handedOff {
			out.Text = partial + firstNonEmpty(res.Message, CopyCardProblem) + " " + CopyPostNotDoneHandedOff
		}
		return r.dispatch(ctx, async, func(c context.Context) error {
			if err := r.Send(c, out, ""); err != nil {
				return err
			}
			if !handedOff {
				r.markShown(c, id.ID) // the retry is a question of ours (R-1)
				r.resumeDeferred(c, id)
			}
			return nil
		})
	case PostNeeds3DS:
		conv.State = StateHandoff
		conv.PendingExtensionID = ""
		r.finishPosting(ctx, id, conv)
		out.Text = partial + Copy3DSHandoff
		// Ops notice and the person's reply both off the request path when
		// the caller is an HTTP request (S-32).
		return r.dispatch(ctx, async, func(c context.Context) error {
			if err := r.notifyOps(c, id, HandoffPayment3DS, false, handoffOpen{}); err != nil {
				// No one was told: the reply must not promise a teammate
				// (review 10/24 #7). The thread stays held — the next
				// message notifies ops again (onHandoff).
				log.Printf("[channel] 3ds handoff identity=%s: %v", id.ID, err)
				out.Text = partial + Copy3DSHandoffNoTeam
			}
			return r.Send(c, out, res.TaskID)
		})
	default:
		// A failure on our side (or a hold whose outcome is unknown): the
		// draft and its booking keys are kept, so the YES that retries it
		// finds whatever the first attempt made. Ops are told too.
		conv.State = StateAwaitingConfirm
		shown := ShownUnconfirmed // as above (R-1)
		conv.QuestionShownAt = &shown
		handedOff := r.finishPosting(ctx, id, conv)
		out.Text = partial + CopyPostFailed
		if handedOff {
			out.Text = partial + CopyPostNotDoneHandedOff
		}
		return r.dispatch(ctx, async, func(c context.Context) error {
			if err := r.Send(c, out, ""); err != nil {
				return err
			}
			if !handedOff {
				r.markShown(c, id.ID) // the retry is a question of ours (R-1)
			}
			r.alertBookingProblem(c, id, "booking failed on our side; the person was asked to reply YES to retry")
			if !handedOff {
				r.resumeDeferred(c, id)
			}
			return nil
		})
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
	_, _, err := r.sendStamped(ctx, out, taskID)
	return err
}

// sendStamped is Send, returning the thread's outbound stamp for this
// message. The stamp is taken BEFORE the provider call and a failure to
// take it fails the send: a message that may have gone out always moves
// the stamp, so the reaction-word rule (D-17 R-3) can only err toward
// asking again — never toward reading "Great" as an answer to a question
// that something else followed.
func (r *Router) sendStamped(ctx context.Context, out Outbound, taskID string) (time.Time, string, error) {
	a, ok := r.Adapters[out.Channel]
	if !ok {
		return time.Time{}, "", fmt.Errorf("send: no adapter for channel %q", out.Channel)
	}
	if r.OptOuts != nil {
		// A stopped number gets nothing (TCPA; the provider would refuse it
		// anyway). Fail closed: no answer, no text.
		opted, err := r.OptOuts.OptedOut(ctx, out.Channel, out.To)
		if err != nil {
			return time.Time{}, "", fmt.Errorf("send to %s: opt-out check: %w", Redact(out.To), err)
		}
		if opted {
			return time.Time{}, "", fmt.Errorf("send to %s: %w", Redact(out.To), ErrRecipientOptedOut)
		}
	}
	// The voice layer (D-28): before the stamp, so the stamp still moves
	// only for a message that is about to go out as it will be read.
	out.Text = r.voiceText(ctx, out)
	stamp, err := r.Conversations.TouchOutbound(ctx, out.Channel, out.To)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("send to %s: %w", Redact(out.To), err)
	}
	tracing.Replied(ctx, out.RecordedText())
	_, sp := tracing.StartSpan(ctx, "channel.send", tracing.KindTool)
	sp.Input(out.RecordedText())
	sp.Attr(attribute.String("hora.channel", string(out.Channel)), attribute.Bool("hora.relay", out.Relay), attribute.Bool("hora.media", out.MediaURL != ""))
	pid, err := a.Send(ctx, out)
	sp.Fail(err)
	sp.End()
	if err != nil {
		if errors.Is(err, ErrRecipientOptedOut) {
			// Stopped in a way we did not see (a keyword before this code,
			// or sent elsewhere): marked now, ops told once.
			r.recordOptOut(ctx, out.Channel, out.To, "provider")
		}
		return stamp, "", fmt.Errorf("send to %s: %w", Redact(out.To), err)
	}
	if err := r.Messages.RecordOutbound(ctx, out, pid, taskID); err != nil {
		// Sent but not recorded: the person got the message; only reply-to
		// resolution for it is lost. Log, don't resend.
		log.Printf("[channel] record outbound id=%s: %v", pid, err)
	}
	log.Printf("[channel] outbound channel=%s id=%s to=%s len=%d", out.Channel, pid, Redact(out.To), len(out.Text))
	return stamp, pid, nil
}

// markShown: a question of ours was just delivered to this thread (D-17
// R-1). After the send, so a YES sent while it was still on its way is
// "before" it. Best effort: logged.
func (r *Router) markShown(ctx context.Context, identityID string) {
	if err := r.Conversations.MarkQuestionShown(context.WithoutCancel(ctx), identityID, r.now()); err != nil {
		log.Printf("[channel] mark shown identity=%s: %v", identityID, err)
	}
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

// sentBefore: how long before the stamp the message was sent ("no time"
// when the provider gave none) — logged so a provider clock running behind
// ours shows up (D-17 R-1, R-1's accepted cost).
func sentBefore(in Inbound, stamp *time.Time) string {
	if in.ReceivedAt.IsZero() || stamp == nil {
		return "(no time)"
	}
	return stamp.Sub(in.ReceivedAt).Round(time.Millisecond).String()
}

// latestLine is the last line of a draft's source — the correction just
// typed (startDraft joins earlier words and the new ones with "\n").
// sameErrand: a re-drafted errand is the one drafted before — the same
// kind of task. (Not the place: a re-parse words it differently — staging
// 2026-10-24 asked the eggs budget twice over "farmer's market".)
func sameErrand(a, b llm.TaskDraft) bool {
	return a.Category == b.Category
}

// categoryCount: how many of the items are of this category (a budget
// carries by position only when the kind is unambiguous — review r2).
func categoryCount(items []DraftItem, category string) int {
	n := 0
	for _, it := range items {
		if it.Draft.Category == category {
			n++
		}
	}
	return n
}

// durationRE: the person named how long — minutes, hours, 分鐘/小時.
var durationRE = regexp.MustCompile(`(?i)\b(?:\d+|one|two|three|four|five|six|seven|eight|nine|ten|twelve|a couple of|a few|half an?|an?)\s*(?:h|hr|hrs|hour|hours|min|mins|minute|minutes)\b|[\d一二三四五六七八九十兩]+\s*(?:分鐘|分|個?小時|個?鐘頭)|半小時|半個小時`)

// namesDuration reports whether the errand text says how long it takes.
func namesDuration(text string) bool { return durationRE.MatchString(text) }

// durationOnlyRE: a message that is nothing but a duration, with the usual
// filler around it. "I'll be there in 10 min" is not one (it says when, not
// how long) and is left to the parser.
var durationOnlyRE = regexp.MustCompile(`(?i)^\s*(?:(?:asap|about|around|approx(?:imately)?|roughly|maybe|just|only|make it|can you do|could you do|let'?s do|it'?s|it will take|takes?|for|~|,)\s*)*` +
	`(?:(\d{1,3})\s*(?:m|mins?|minutes?)|(\d{1,2})\s*(?:h|hrs?|hours?)|(half an? hour|30分鐘|半小時|半個小時)|(an? hour|one hour|1 hour|一個小時|一小時)|(\d{1,3})\s*分鐘|(\d{1,2})\s*個?小時)` +
	`(?:\s*(?:is fine|is ok|please|max|maximum|tops|or so|ok\??))*[\s?.!。？！~]*$`)

// DurationOnly: the minutes a duration-only message names, within 5..480.
func DurationOnly(text string) (int, bool) {
	m := durationOnlyRE.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	n := 0
	switch {
	case m[1] != "":
		n, _ = strconv.Atoi(m[1])
	case m[2] != "":
		n, _ = strconv.Atoi(m[2])
		n *= 60
	case m[3] != "":
		n = 30
	case m[4] != "":
		n = 60
	case m[5] != "":
		n, _ = strconv.Atoi(m[5])
	case m[6] != "":
		n, _ = strconv.Atoi(m[6])
		n *= 60
	}
	if n < 5 || n > 480 {
		return 0, false
	}
	return n, true
}

func latestLine(source string) string {
	if i := strings.LastIndex(source, "\n"); i >= 0 {
		return source[i+1:]
	}
	return source
}

// previewWindow: how soon after a message an empty one is taken as its
// link preview.
const previewWindow = 15 * time.Second

// sentWithText reports whether the person wrote within previewWindow
// before this (empty) message.
func (r *Router) sentWithText(ctx context.Context, identityID string, in Inbound) bool {
	prev, err := r.Conversations.GetConversation(ctx, identityID)
	if err != nil {
		log.Printf("[channel] link preview check identity=%s: %v", identityID, err)
		return false
	}
	if prev.LastInboundAt == nil {
		return false
	}
	at := in.ReceivedAt
	if at.IsZero() {
		at = r.now()
	}
	gap := at.Sub(*prev.LastInboundAt)
	return gap >= -previewWindow && gap <= previewWindow
}

// linkJustSent: the person's previous message carried a URL (its preview
// card may follow as media with no text).
func (r *Router) linkJustSent(ctx context.Context, id Identity) bool {
	if r.Thread == nil {
		return false
	}
	msgs, err := r.Thread.RecentMessages(ctx, id.Channel, id.Address, 4)
	if err != nil {
		log.Printf("[channel] link preview check identity=%s: %v", id.ID, err)
		return false
	}
	for i := len(msgs) - 1; i >= 0; i-- { // oldest first: walk back to the last inbound with text
		m := msgs[i]
		if m.Direction != "inbound" || strings.TrimSpace(m.Text) == "" {
			continue
		}
		return urlRE.MatchString(m.Text)
	}
	return false
}

var urlRE = regexp.MustCompile(`(?i)\bhttps?://\S+`)

// holdDraft keeps a draft made while an approval question waits: its next
// step (name, budget or summary) is set for ResumeState, the approval stays
// the open question, and the person is told the errand is next.
func (r *Router) holdDraft(ctx context.Context, in Inbound, id Identity, conv Conversation) (bool, error) {
	p := conv.Pending
	switch {
	case p.DetailsAsked:
		// asked when the approval is answered (resumeLine)
	case !id.Linked() && conv.PendingName == "":
		p.NameAsked = true
	default:
		if i := nextBudgetAsk(p); i >= 0 {
			p.BudgetAskIndex = i
		}
	}
	lead := r.leadIn(p) // P6: said once, now, while the supporter is on the job
	// Narrowly (review 10/24): only the draft, only while THIS approval is
	// still the armed one — never a full save of the snapshot read before
	// the parse.
	held, err := r.Conversations.HoldDraft(ctx, id.ID, conv.PendingExtensionID, *p)
	if err != nil {
		return true, fmt.Errorf("holdDraft: %w", err)
	}
	if !held {
		p.WhileActiveShown = false
		return false, nil
	}
	return true, r.Reply(ctx, in, lead+fmt.Sprintf(CopyErrandAfterApproval, p.Headline())+"\n\n"+approvalStillPending(conv), "")
}

// vagueRequestRE: a request to book with nothing in it — "book another
// thing?", "can I book something", "I'd like to make a new request".
var vagueRequestRE = regexp.MustCompile(`(?i)^\W*(?:hi\W+)?(?:can i|could i|may i|i want to|i'd like to|id like to|let me|i need to)?\s*(?:book|post|order|make|request|get|add)\s*(?:me\s+)?(?:another|a new|one more|a|something|an?other)?\s*(?:thing|task|errand|one|request|booking|order)?\s*(?:please|pls)?\W*$`)

// preempted: after a full-row save of our question, a supporter's approval
// question armed meanwhile (the save keeps it — S-11/5) is the open one, so
// ours must not go out on top of it: two YES-able questions in one thread
// (review 10/24 r2). The draft was saved with it and comes back after the
// answer (ResumeState / resumeLine). done=true: replied instead.
func (r *Router) preempted(ctx context.Context, in Inbound, identityID string, p *PendingDraft) (bool, error) {
	fresh, err := r.Conversations.GetConversation(ctx, identityID)
	if err != nil {
		return true, fmt.Errorf("preempted: %w", err)
	}
	if fresh.State != StateAwaitingApproval {
		return false, nil
	}
	log.Printf("[channel] identity=%s: an approval question armed while ours was prepared; ours waits", identityID)
	text := approvalStillPending(fresh)
	if p != nil {
		text = fmt.Sprintf(CopyErrandAfterApproval, p.Headline()) + "\n\n" + text
	}
	return true, r.Reply(ctx, in, text, "")
}

// supporterSpokeSince: a supporter on an active task wrote after `since`
// (nil: any line counts).
func (r *Router) supporterSpokeSince(ctx context.Context, in Inbound, id Identity, since *time.Time) bool {
	if r.Chat == nil || !id.Linked() {
		return false
	}
	_, lastTask, lastAt, _ := r.lastSupporterLine(ctx, in, id)
	if lastTask == "" || lastAt.IsZero() {
		return false
	}
	return since == nil || lastAt.After(*since)
}

// MaxDetailsTries: answers to the details question that still leave the
// draft without a place before the thread goes to a person.
const MaxDetailsTries = 2

// needsDetails: a draft that names no place for some errand, or came from a
// request with nothing in it.
func needsDetails(items []DraftItem) bool {
	for _, it := range items {
		if strings.TrimSpace(it.Draft.Location1) == "" {
			return true
		}
	}
	return false
}

// detailsQuestion asks what is missing: the borough of a stop in several, a
// clearer address for a stop that could not be placed, what and where for an empty request, where for a draft
// without a place.
func detailsQuestion(p *PendingDraft) string {
	if p.WhichBorough {
		return fmt.Sprintf(CopyWhichBorough, p.UnclearStop)
	}
	if p.UnclearStop != "" {
		return fmt.Sprintf(CopyAskClearerAddress, p.UnclearStop)
	}
	if vagueRequestRE.MatchString(p.Source) {
		return CopyAskWhatWhere
	}
	return fmt.Sprintf(CopyAskWhere, p.Headline())
}

// draftDeclineRE: a no in words to a pending draft, as the whole message.
// The object, if any, is the draft itself ("it", "that"), so "no I don't
// need the extra time" or "no, make it 4pm" is not one.
var draftDeclineRE = regexp.MustCompile(`(?i)^\s*(?:(?:ok(?:ay)?|sorry|actually|hmm+|oh|wait|no|nah|nope|thanks|thank you)[\s,.!]*)*` +
	`(?:(?:(?:i|we)\s+)?(?:` +
	`(?:don'?t|do not|no longer)\s+(?:really\s+)?(?:need|want)(?:\s+(?:it|that|this|them|one|the task|this task|that task|this one|that one))?(?:\s+(?:anymore|any more|after all))?` +
	`|never ?mind|forget (?:it|that|about it)|no thanks|no thank you|not anymore|not needed|skip it|(?:i )?changed? my mind|not now` +
	`|算了|不用了|不用|不需要了?|不要了|先不用了?|沒事了?)` +
	`[\s,.!。！~，]*)+(?:(?:thanks|thank you|sorry|謝謝)[\s,.!。！~，]*)?$`)

// okNoRE: "ok no", "okay, no" — a no after a filler.
var okNoRE = regexp.MustCompile(`(?i)^\s*(?:ok(?:ay)?|sorry|hmm+|actually)[\s,.]*(?:no|nope|nah)[\s.!'"‘’“”]*$`)

// IsDraftDecline reports a no in words to the pending draft.
func IsDraftDecline(text string) bool {
	t := strings.NewReplacer("’", "'", "‘", "'").Replace(text)
	return draftDeclineRE.MatchString(t) || okNoRE.MatchString(t)
}

// changeWordsRE: a message that changes something in the draft — a
// number, a time, a place or an errand verb. An agent "decline" on one of
// these is read as the correction it carries.
var changeWordsRE = regexp.MustCompile(`(?i)\d|\b(?:make it|instead|change|but|rather|tomorrow|tonight|today|later|morning|afternoon|evening|pm|am|o'?clock|at|bring|deliver|drop|pick|buy|get|address|street|ave|min|mins|minutes?|hours?|bags?|budget|one|two|three|four|five|other|different|wrong|from|to|for|use|send|put|blue|red|black|white|office|home|store|shop)\b|[一二三四五六七八九十兩點分時明今晚早午改換送拿買到色]|不是`)

// ── the draft's model calls, side by side ───────────────────────────────

// draftPrefetch: the split and the parse of source, started together.
type draftPrefetch struct {
	source string
	split  chan splitResult
	parse  chan parseResult
	// Each result is received once and kept: a second read never blocks.
	splitOnce, parseOnce sync.Once
	splitGot             splitResult
	parseGot             parseResult
}

func (p *draftPrefetch) splitResult() splitResult {
	p.splitOnce.Do(func() { p.splitGot = <-p.split })
	return p.splitGot
}

func (p *draftPrefetch) parseResult() parseResult {
	p.parseOnce.Do(func() { p.parseGot = <-p.parse })
	return p.parseGot
}

type splitResult struct {
	errands []string
	usage   llm.Usage
	err     error
}

type parseResult struct {
	draft llm.TaskDraft
	usage llm.Usage
	err   error
}

type prefetchKey struct{}

// prefetchDraft starts the split and the parse of text and carries them in
// the returned context, for startDraftOpts.
func (r *Router) prefetchDraft(ctx context.Context, text string) context.Context {
	if r.Parser == nil || r.Prompt == nil {
		return ctx
	}
	return context.WithValue(ctx, prefetchKey{}, r.startPrefetch(ctx, text, true))
}

// prefetched is the prefetch started for exactly these words, else a new
// one (split only when a split is wanted).
func (r *Router) prefetched(ctx context.Context, source string, split bool) *draftPrefetch {
	if p, ok := ctx.Value(prefetchKey{}).(*draftPrefetch); ok && p.source == source && split {
		return p
	}
	return r.startPrefetch(ctx, source, split)
}

func (r *Router) startPrefetch(ctx context.Context, source string, split bool) *draftPrefetch {
	p := &draftPrefetch{source: source, split: make(chan splitResult, 1), parse: make(chan parseResult, 1)}
	run := r.Go
	if run == nil {
		run = func(fn func()) { go fn() }
	}
	prompt := r.Prompt(r.now().In(r.loc()))
	if split && r.Splitter != nil {
		run(func() {
			e, u, err := r.Splitter.SplitErrands(ctx, source)
			p.split <- splitResult{e, u, err}
		})
	} else {
		p.split <- splitResult{}
	}
	run(func() {
		d, u, err := r.Parser.ParseTask(ctx, prompt, source)
		p.parse <- parseResult{d, u, err}
	})
	return p
}
