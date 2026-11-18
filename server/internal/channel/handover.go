package channel

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"hora-auth/internal/channel/llm"
)

// Replies to a supporter drop-off (2026-11-18). When a supporter cannot
// finish, ops either cancel the errand ("Your supporter couldn't finish …,
// so we've cancelled it. … Want it done by someone else? Just tell us.") or
// put another supporter on it ("… so Sam is taking over. You only pay for
// Sam's time." / "Your task … has a new supporter: Sam."). Both reach a
// texting customer verbatim (status.go, CANCELLED / TASK_REASSIGNED); this
// file is what the chat does with the answer.
//
// A reply counts as answering one of these notices only within
// HandOverWindow of it, and only when nothing else of ours is waiting for an
// answer that the same words could be for: a summary, a question about a
// draft, an approval, a cancel, places just offered, or a supporter who wrote
// after the notice. When both could apply, the customer is asked which one.
// Words that can only be about the notice ("was I charged?") are answered
// whatever else is open, and the open question is said again after it.
//
// Nothing here books, cancels or charges. A redo is a new draft from the old
// errand that goes through the scope check and ends in a summary the
// customer answers YES to; a cancel is the existing cancel flow; wanting the
// first supporter back goes to a person. Replies never promise a refund,
// compensation or a time, and say nothing about why the supporter left
// beyond that they couldn't finish.

// HandOverWindow: how long after a notice a reply can be its answer.
const HandOverWindow = 24 * time.Hour

const (
	HandOverCancelled  = "cancelled"
	HandOverReassigned = "reassigned"
)

// HandOverNotice is one drop-off notice this thread received.
type HandOverNotice struct {
	Kind   string // HandOverCancelled | HandOverReassigned
	TaskID string
	Title  string
	// Text is the message as the customer received it; SentAt when.
	Text   string
	SentAt time.Time
	// Closed: already answered for good — a decline was acknowledged, the
	// errand was booked again (any newer task), or a reassigned errand is
	// no longer open.
	Closed bool

	// Reassigned: the supporter now on it, and whether the one before had
	// started (their time was taken off the task).
	Supporter string
	Started   bool

	// Cancelled: the errand as it was booked, for a redo.
	Category, Description string
	Location1, Location2  string
	Minutes               int
	BudgetCents           int
	ScheduledAt           *time.Time // nil: as soon as possible
}

// HandOverStore finds the notices a thread received.
type HandOverStore interface {
	// HandOverNotices: the notices sent to (ch, address) for userID's
	// tasks since since, newest first.
	HandOverNotices(ctx context.Context, userID string, ch Channel, address string, since time.Time) ([]HandOverNotice, error)
}

// ── rules: the short replies, in English, Chinese and Spanish ────────────

var (
	hoRedoRE    = regexp.MustCompile(`(?i)^[\s\p{P}]*(?:yes|yeah|yep|yup|ya|sure|please|yes,? please|yes,? do it|ok(?:ay)?,? (?:please|do it|go ahead)|go ahead|do it|redo(?: it)?|please redo(?: it)?|try again|book (?:it )?again|rebook(?: it)?|find (?:me )?(?:someone|somebody) else|someone else|somebody else|someone else please|another (?:supporter|person|one)|s[ií](?:,? por favor)?|claro(?: que s[ií])?|por favor|otra vez|de nuevo|b[uú]sca(?:me)? a otr[ao](?: persona)?|b[uú]squen a otr[ao](?: persona)?|alguien m[aá]s|otra persona|好|好的|好啊|要|是|是的|可以|麻烦了|麻烦你了|拜托了|重新(?:找|安排|下单|预约)?(?:一下|一个)?(?:吧|人)?|再找(?:一个|个)?(?:人)?(?:吧)?|换(?:一个|个)人(?:吧)?|找别人(?:吧)?|另外找(?:一个|个)?(?:人)?(?:吧)?)[\s!.。！?？\p{So}]*$`)
	hoDeclineRE = regexp.MustCompile(`(?i)^[\s\p{P}]*(?:no|nope|nah|no,? thanks?|no,? thank you|never ?mind|not now|not anymore|no need|it'?s ok(?:ay)?,? (?:no|never ?mind)|no,? gracias|no hace falta|ya no(?: hace falta)?|d[eé]jalo|olv[ií]dalo|不用|不用了|不需要了?|不要了?|算了(?:吧)?|没事了?|不用了谢谢|不用了，谢谢)[\s!.。！,，\p{So}]*$`)
	hoAckRE     = regexp.MustCompile(`(?i)^[\s\p{P}]*(?:ok(?:ay)?|k|kk|thanks?|thank you|thx|ty|got it|noted|understood|cool|great|alright|all right|ok(?:ay)?,? thanks?(?: you)?|thanks?,? ok(?:ay)?|gracias|vale|de acuerdo|entendido|ok(?:ay)?,? gracias|vale,? gracias|好吧|谢谢|谢了|多谢|知道了|收到|明白了?|了解|行|👍|🙏)[\s!.。！,，\p{So}]*$`)
	hoWhyRE     = regexp.MustCompile(`(?i)^[\s\p{P}]*(?:why|why\?|how come|what happened|what happened\?|what went wrong|por qu[eé]|¿?qu[eé] pas[oó]|qu[eé] pas[oó]|为什么|为啥|怎么了|怎么回事|发生什么事了?|發生什麼事了?|為什麼)[\s!.。！?？¿\p{So}]*$`)
)

// ReadHandOverByRule: the label for the short, unambiguous replies; ok =
// false leaves the words to the model.
func ReadHandOverByRule(kind, text string) (llm.HandOverReading, bool) {
	t := strings.TrimSpace(text)
	switch {
	case hoWhyRE.MatchString(t):
		return llm.ReadWhy, true
	case kind == HandOverCancelled && hoRedoRE.MatchString(t):
		return llm.ReadRedo, true
	case kind == HandOverCancelled && hoDeclineRE.MatchString(t):
		return llm.ReadDecline, true
	case hoAckRE.MatchString(t):
		return llm.ReadAck, true
	case kind == HandOverReassigned && (IsYes(t) || hoRedoRE.MatchString(t) && !strings.Contains(strings.ToLower(t), "else")):
		// "yes" to "has a new supporter": an acknowledgement.
		return llm.ReadAck, true
	}
	return "", false
}

// bareHandOverReply: words that could just as well answer another question
// of ours (a yes, a no, an ok) — never taken for the notice's while
// something newer is open.
func bareHandOverReply(text string) bool {
	t := strings.TrimSpace(text)
	return IsYes(t) || IsNo(t) || IsDraftDecline(t) || IsAffirmation(stripFiller(t)) ||
		hoRedoRE.MatchString(t) || hoDeclineRE.MatchString(t) || hoAckRE.MatchString(t)
}

// ── routing ───────────────────────────────────────────────────────────────

func (r *Router) handOversOn() bool { return r.HandOvers != nil }

// openHandOver: the newest notice this thread can still be answering, or
// nil. Errors are logged and read as none — the message is then routed as
// it was before this file.
func (r *Router) openHandOver(ctx context.Context, id Identity) *HandOverNotice {
	if !r.handOversOn() || !id.Linked() {
		return nil
	}
	ns, err := r.HandOvers.HandOverNotices(ctx, id.UserID, id.Channel, id.Address, r.now().Add(-HandOverWindow))
	if err != nil {
		log.Printf("[channel] hand-over notices identity=%s: %v", id.ID, err)
		return nil
	}
	if len(ns) == 0 || ns[0].Closed {
		// Only the newest counts: a later notice replaces an earlier one.
		return nil
	}
	n := ns[0]
	return &n
}

// competingSince: something of ours other than the notice is open for an
// answer, and it was put to the person after the notice (newer = true) or
// before it (newer = false). open = false: nothing competes.
func (r *Router) competingSince(ctx context.Context, in Inbound, id Identity, conv Conversation, n *HandOverNotice) (open, newer bool) {
	if conv.State != StateIdle || conv.Pending != nil {
		at := time.Time{}
		switch {
		case conv.QuestionShownAt != nil && !conv.Unconfirmed():
			at = *conv.QuestionShownAt
		case conv.Pending != nil && conv.Pending.SummaryShownAtUnix > 0:
			at = time.Unix(conv.Pending.SummaryShownAtUnix, 0)
		case conv.Pending != nil && conv.Pending.CreatedAtUnix > 0:
			at = time.Unix(conv.Pending.CreatedAtUnix, 0)
		}
		return true, at.After(n.SentAt)
	}
	if r.discoveryOn() {
		if o := r.freshOffer(ctx, id); o != nil {
			return true, time.Unix(o.AtUnix, 0).After(n.SentAt)
		}
	}
	sent := n.SentAt
	if r.supporterSpokeSince(ctx, in, id, &sent) {
		return true, true
	}
	return false, false
}

// routeHandOver answers a reply to a drop-off notice. handled = false: the
// message is not one (or the notice no longer counts) and is routed as
// before.
func (r *Router) routeHandOver(ctx context.Context, in Inbound, id Identity, conv Conversation, text string) (bool, error) {
	n := r.openHandOver(ctx, id)
	if n == nil {
		return false, nil
	}
	if conv.Pending != nil && conv.Pending.RedoOf == n.TaskID {
		// The redo is under way: its questions and summary take the
		// answers, as any draft's ("tomorrow at 10am" is the time asked).
		return false, nil
	}
	reading, by := r.readHandOver(ctx, n, text)
	log.Printf("[channel] hand-over reply identity=%s kind=%s task=%s reading=%s by=%s", id.ID, n.Kind, n.TaskID, reading, by)
	if reading == llm.ReadOther {
		return false, nil
	}
	ambiguous := bareHandOverReply(text) || reading == llm.ReadCancel
	if open, newer := r.competingSince(ctx, in, id, conv, n); open && ambiguous {
		if newer {
			// What we asked since is the latest thing said: its answer.
			return false, nil
		}
		if !r.justAskedWhich(ctx, id) {
			return true, r.askWhichOne(ctx, in, conv, n)
		}
		// Asked which already: the reply named its choice the way the
		// question said (REDO, "cancel task"), or it is the other one's.
		if !(reading == llm.ReadRedo && hoRedoWordRE.MatchString(text)) && !(reading == llm.ReadCancel && IsCancelRequest(text)) {
			return false, nil
		}
	}
	err := r.answerHandOver(ctx, in, id, conv, n, reading, text)
	if err == nil && reading != llm.ReadRedo && reading != llm.ReadCancel && reading != llm.ReadOriginal && reading != llm.ReadUnclear &&
		(awaitingAnswer(conv.State) || conv.State == StateAwaitingTaskChoice) {
		// The notice answered; what is still open is said again.
		err = r.Reply(ctx, in, stillWaiting(conv), "")
	}
	return true, err
}

// hoRedoWordRE: the word the which-question offers for a redo.
var hoRedoWordRE = regexp.MustCompile(`(?i)\bredo\b|重新|otra vez|de nuevo`)

func (r *Router) readHandOver(ctx context.Context, n *HandOverNotice, text string) (llm.HandOverReading, string) {
	if got, ok := ReadHandOverByRule(n.Kind, text); ok {
		return got, "rule"
	}
	if r.HandOverReader == nil {
		return llm.ReadOther, "none"
	}
	got, err := r.HandOverReader.ReadHandOver(ctx, llm.HandOverQuery{Kind: n.Kind, Notice: n.Text, Text: text})
	if err != nil {
		// No reading: routed as before (never a hand-off for a model
		// outage, never a draft).
		log.Printf("[channel] hand-over reply: %v", err)
		return llm.ReadOther, "error"
	}
	return got, "model"
}

// justAskedWhich: our last line was the which-one question.
func (r *Router) justAskedWhich(ctx context.Context, id Identity) bool {
	text, at := r.ourLastLineText(ctx, id)
	return strings.HasPrefix(text, CopyHandOverWhichLead) && r.now().Sub(at) < ackWindow
}

func (r *Router) askWhichOne(ctx context.Context, in Inbound, conv Conversation, n *HandOverNotice) error {
	open := stillWaiting(conv)
	if conv.Pending != nil {
		open = fmt.Sprintf(CopyHandOverWhichDraft, conv.Pending.Headline())
	}
	q := CopyHandOverWhichCancelled
	if n.Kind == HandOverReassigned {
		q = CopyHandOverWhichReassigned
	}
	return r.Reply(ctx, in, CopyHandOverWhichLead+" "+fmt.Sprintf(q, quoted(n.Title))+" "+open, "")
}

func (r *Router) answerHandOver(ctx context.Context, in Inbound, id Identity, conv Conversation, n *HandOverNotice, reading llm.HandOverReading, text string) error {
	task := quoted(n.Title)
	who := firstNonEmpty(DisplayName(FirstName(n.Supporter)), "your new supporter")
	switch reading {
	case llm.ReadRedo:
		return r.redoHandOver(ctx, in, id, conv, n, text)
	case llm.ReadDecline:
		// Tagged with the task: the notice is answered for good (the store
		// reads it as closed).
		return r.Reply(ctx, in, CopyHandOverDeclined, n.TaskID)
	case llm.ReadWhy:
		if n.Kind == HandOverCancelled {
			return r.Reply(ctx, in, fmt.Sprintf(CopyHandOverWhyCancelled, task), "")
		}
		if n.Started {
			return r.Reply(ctx, in, fmt.Sprintf(CopyHandOverWhyTakenOver, task, who), "")
		}
		return r.Reply(ctx, in, fmt.Sprintf(CopyHandOverWhyChanged, task, who), "")
	case llm.ReadCharged:
		if n.Kind == HandOverCancelled {
			return r.Reply(ctx, in, fmt.Sprintf(CopyHandOverNotCharged, task), "")
		}
		return r.Reply(ctx, in, fmt.Sprintf(CopyHandOverNotTwice, who, task), "")
	case llm.ReadAck:
		if n.Kind == HandOverCancelled {
			return r.Reply(ctx, in, CopyHandOverAckCancelled, "")
		}
		return r.Reply(ctx, in, CopyAckShort, "")
	case llm.ReadStatus:
		line := fmt.Sprintf(CopyHandOverWho, who, task)
		if r.Live != nil && IsWhereQuestion(text) || r.Live != nil && hoWhenRE.MatchString(text) {
			if where, _, err := r.whereLine(ctx, id, ActiveTask{ID: n.TaskID, Title: n.Title, SupporterName: n.Supporter}); err == nil && where != "" {
				line += " " + strings.TrimSpace(where)
				if !strings.ContainsAny(line[len(line)-1:], ".!?") {
					line += "."
				}
			} else if err != nil {
				log.Printf("[channel] hand-over where identity=%s task=%s: %v", id.ID, n.TaskID, err)
			}
		}
		return r.Reply(ctx, in, line, n.TaskID)
	case llm.ReadCancel:
		if conv.Pending != nil && draftState(conv.State) {
			// Asked which one, the errand it is: the cancel flow runs from a
			// thread with nothing else open, so the draft goes, said so.
			dropped := conv.Pending.Headline()
			conv.Pending, conv.State = nil, StateIdle
			if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
				return fmt.Errorf("hand-over cancel: %w", err)
			}
			if err := r.Reply(ctx, in, fmt.Sprintf(CopyHandOverDraftDropped, dropped), ""); err != nil {
				return err
			}
		}
		return r.startCancel(ctx, in, id, conv)
	case llm.ReadOriginal:
		return r.handOffDetail(ctx, in, id, conv, HandoffSupporterChange,
			fmt.Sprintf("after the supporter change on %s, the customer asked about the supporter (wants the original one or not a different one)", task))
	}
	return r.handOffDetail(ctx, in, id, conv, HandoffSupporterChange,
		fmt.Sprintf("a reply to the %s notice on %s that the assistant could not place", n.Kind, task))
}

// hoWhenRE: asking when the supporter comes.
var hoWhenRE = regexp.MustCompile(`(?i)\bwhen\b|\beta\b|how long|什么时候|幾點|几点|多久|cu[aá]ndo|a qu[eé] hora`)

// ── redo ──────────────────────────────────────────────────────────────────

// redoHandOver starts a new draft from the cancelled errand: its places,
// what it was, its notes and its shopping budget. The words are checked for
// scope as any request (companionship may have been paused since), a time
// that has passed (or "as soon as possible", which then meant another
// moment) is asked again, and the draft ends in the usual summary: nothing
// is booked without the customer's YES.
func (r *Router) redoHandOver(ctx context.Context, in Inbound, id Identity, conv Conversation, n *HandOverNotice, text string) error {
	d := llm.TaskDraft{Title: n.Title, Category: n.Category, Description: strings.TrimSpace(n.Description),
		Location1: n.Location1, Location2: n.Location2, DurationMinutes: n.Minutes}
	keepTime := n.ScheduledAt != nil && n.ScheduledAt.After(r.now().Add(redoMinLead))
	if keepTime {
		d.Scheduled, d.ScheduledTime = true, n.ScheduledAt.In(r.loc()).Format(time.RFC3339)
	}
	if err := d.Validate(); err != nil {
		log.Printf("[channel] hand-over redo identity=%s task=%s: %v — handed off", id.ID, n.TaskID, err)
		return r.handOffDetail(ctx, in, id, conv, HandoffSupporterChange,
			fmt.Sprintf("the customer asked to redo %s and it could not be drafted again", quoted(n.Title)))
	}
	when := ""
	if keepTime {
		// The day itself, never "Tomorrow": a correction re-reads these
		// words later.
		when = n.ScheduledAt.In(r.loc()).Format("Monday, January 2 at 3:04 PM")
	}
	source := redoSource(d, n.BudgetCents, when, keepTime)
	// A draft of something else that was open (the customer chose the redo
	// when asked which one): this replaces it, and nothing of it carries.
	conv.Pending, conv.State = nil, StateIdle
	// The scope check, as for any request: the paused types by their
	// category too, then the rules and the model on the errand's words.
	if pbs, err := llm.Playbooks(); err == nil {
		if ref := pbs.PausedRefusal(source, n.Category); ref != nil {
			return r.refuseScope(ctx, in, id, conv, llm.ScopeDecision{Refusal: ref, Decided: true, By: "paused:redo"})
		}
	}
	if handled, _, err := r.scopeGate(ctx, in, id, conv, source); handled {
		return err
	}
	askTime := !keepTime
	if extra := redoExtra(text); extra != "" && saysWhen(extra) {
		// "yes, tomorrow at 5 please": the time is read from the reply; the
		// errand stays the one booked (a re-parse of the old words with a
		// reply in another language lost the title on 2026-11-18). Any
		// other change is made at the summary, as always.
		if at, asap, ok := r.redoTime(ctx, source, extra); ok {
			d.Scheduled, d.ScheduledTime, askTime = !asap, at, false
			if !asap {
				if t, err := time.Parse(time.RFC3339, at); err == nil {
					source = redoSource(d, n.BudgetCents, t.In(r.loc()).Format("Monday, January 2 at 3:04 PM"), true)
				}
			}
		}
	}
	o := draftOpts{intro: fmt.Sprintf(CopyHandOverRedoIntro, quoted(n.Title)), askTime: askTime, prebuilt: &d, redoOf: n.TaskID}
	log.Printf("[channel] hand-over redo identity=%s task=%s keep_time=%t ask_time=%t", id.ID, n.TaskID, keepTime, askTime)
	return r.startDraftOpts(ctx, in, id, conv, source, o)
}

// redoTime reads the time a redo reply names, with the parser the drafts
// use: an RFC 3339 time, or asap. ok = false: no time read (the time is
// then asked).
func (r *Router) redoTime(ctx context.Context, source, extra string) (at string, asap, ok bool) {
	if r.Parser == nil || r.Prompt == nil {
		return "", false, false
	}
	pd, _, err := r.Parser.ParseTask(ctx, r.Prompt(r.now().In(r.loc())), source+"\n"+extra)
	switch {
	case err != nil:
		log.Printf("[channel] hand-over redo: time not read: %v", err)
		return "", false, false
	case pd.Scheduled && pd.ScheduledTime != "":
		if t, err := time.Parse(time.RFC3339, pd.ScheduledTime); err != nil || !t.After(r.now()) {
			return "", false, false
		}
		return pd.ScheduledTime, false, true
	case saysASAPRE.MatchString(extra):
		return "", true, true
	}
	return "", false, false
}

// saysASAPRE: "now", "as soon as possible".
var saysASAPRE = regexp.MustCompile(`(?i)\b(?:now|asap|right away|as soon as possible|ahora|ya mismo|lo antes posible|cuanto antes)\b|现在|马上|尽快|立刻`)

// redoMinLead: a booked time closer than this is asked again.
const redoMinLead = 15 * time.Minute

// redoSource: the errand in words, as a correction re-drafts from it — with
// the shopping budget the customer gave (they named it for this errand) and
// the time when it still stands.
func redoSource(d llm.TaskDraft, budgetCents int, when string, keepTime bool) string {
	var b strings.Builder
	b.WriteString(d.Title)
	if d.Description != "" && d.Description != d.Title {
		b.WriteString(". " + d.Description)
	}
	if d.Location1 != "" {
		b.WriteString("\nAt: " + d.Location1)
	}
	if d.Location2 != "" {
		b.WriteString("\nTo: " + d.Location2)
	}
	if budgetCents > 0 {
		b.WriteString("\nShopping budget: " + Dollars(budgetCents))
	}
	if d.DurationMinutes > 0 && d.DurationMinutes != 30 {
		fmt.Fprintf(&b, "\nAbout %d minutes", d.DurationMinutes)
	}
	if keepTime && when != "" {
		b.WriteString("\nWhen: " + when)
	}
	return b.String()
}

// redoExtra: what a redo reply says beyond yes / do it again.
func redoExtra(text string) string {
	t := strings.TrimSpace(text)
	if hoRedoRE.MatchString(t) || IsYes(t) {
		return ""
	}
	return t
}

// saysWhenRE: a time or "now" in words.
var saysWhenRE = regexp.MustCompile(`(?i)\b(?:now|asap|right away|today|tonight|tomorrow|morning|afternoon|evening|noon|monday|tuesday|wednesday|thursday|friday|saturday|sunday|\d{1,2}(?::\d{2})?\s*(?:am|pm)|\d{1,2}:\d{2}|ahora|hoy|mañana|manana|ma[ñn]ana|tarde|noche|lunes|martes|mi[eé]rcoles|jueves|viernes|s[aá]bado|domingo)\b|现在|马上|尽快|今天|明天|后天|下午|上午|晚上|早上|中午|点|星期|周[一二三四五六日天]`)

func saysWhen(text string) bool { return saysWhenRE.MatchString(text) }
