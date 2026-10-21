package channel

import (
	"fmt"
	"strings"
	"time"
)

// Everything the agent says. Short, plain, one idea per message. Every
// message answers something the person sent, so Sendblue's pre-reply limits
// (300 chars, no links) do not apply — but the first replies stay short anyway.

const (
	CopyWelcome = "Hi, I'm HO:RA. Real people in NYC who run your errands: groceries, laundry, pickups, waiting in line, and more.\n\n" +
		"What do you need, and when?"
	CopyAbout = "HO:RA sends a vetted local supporter to do the errand for you. You see the estimate before anything is booked, " +
		"and you pay for the time actually used.\n\nTell me what you need and when, and I'll price it."
	CopyHelloAgain           = "Hi again! What do you need, and when?"
	CopyAck                  = "Got it. Text me anytime you need something done."
	CopyOnMyWay              = "%s is on the way for %s. I'll text you when they arrive."
	CopyTextOnly             = "I can only read text for now. Tell me what you need and when."
	CopyParseFailed          = "Sorry, I couldn't work that out. Could you say what you need, where, and when?"
	CopyAskName              = "Got it: %s.\n\nBefore I price it, what's your first name?"
	CopyAskBudget            = "Will your supporter be buying something for this? About how much should I set aside, e.g. $20? Reply \"nothing\" if there's nothing to buy."
	CopyAskBudgetFor         = "For \u201c%s\u201d: will your supporter be buying something? About how much should I set aside, e.g. $20? Reply \"nothing\" if there's nothing to buy."
	CopyAskBudgetAgain       = "Just an amount is fine, like $20 — or \"nothing\" if there's nothing to buy."
	CopyAutoExtendOn         = "If it runs a little over, up to %d extra minutes are OK without asking. Say \"no extra time\" to turn that off."
	CopyAutoExtendOff        = "Your supporter will ask before going over the estimate."
	CopyWhichTask            = "Which task is that about?"
	CopyWhichTaskHint        = "Reply with the number, or tell me about a new errand."
	CopyWhichTaskBadPick     = "That number isn't on the list. Which one did you mean?"
	CopyWhichTaskNothingHeld = "Got it. What would you like to tell them?"
	CopyRelayFailed          = "I couldn't pass that on to your supporter just now. Please try again in a moment."
	CopyRelayFailedPick      = "I couldn't pass that on to your supporter just now. Reply the number again in a moment."
	// Appended to a relayed supporter message while a YES/NO is pending, so
	// the next bare YES/NO cannot be read two ways (money rule: the YES
	// answers what the thread says it answers).
	// Footers while a question of ours is open. WhatsApp: a reply to the
	// bubble is reliable context (I-L). iMessage: it is not (D-17 P5), so
	// the person just writes.
	CopyRelayWhileConfirm    = "(To answer %[3]s, reply to this message. A YES, OK or NO sent on its own answers my question above — book “%[1]s” for %[2]s.)"
	CopyRelayWhileApproval   = "(To answer %[1]s, reply to this message. A YES, OK or NO sent on its own answers my question above about %[1]s’s request.)"
	CopyRelayWhileConfirmIM  = "(To answer %[3]s, just write your reply. A YES, OK or NO sent on its own answers my question above — book “%[1]s” for %[2]s.)"
	CopyRelayWhileApprovalIM = "(To answer %[1]s, just write your reply. A YES, OK or NO sent on its own answers my question above about %[1]s’s request.)"
	CopyRelayWhileBudgetIM   = "(To answer %[1]s, just write your reply. An amount sent on its own answers my question above about the shopping budget.)"
	CopyDraftWhileActive     = "Booking this as a new errand. If it was for %s instead, reply “for %s”."
	CopyRelayedDraftDropped  = "Passed that on to %s. The new errand is dropped."
	CopyDraftStillPending    = "(Still waiting on your YES for the booking above — or tell me what to change.)"
	CopyBudgetStillPending   = "(Still waiting on the shopping budget above — an amount, or “nothing”.)"
	CopyHoldGone             = "Your supporter has finished, so I couldn't pass that on. Tell me if you need something new."
	CopyRelayWhileBudget     = "(To answer %[1]s, reply to this message. An amount sent on its own answers my question above about the shopping budget.)"
	CopyAmbiguousYes         = "I can't tell which message that answers. To answer my question, send YES or NO on its own; to answer your supporter, reply to their message."
	CopyRelayGone            = "That task has finished, so I can't pass this on. Tell me if you need something new."
	CopyHoldDropped          = "OK, I won't send that."
	CopyBackToHold           = "Your earlier message is still waiting — which task is it for?"
	CopyTooManyErrands       = "That's more than I can book in one go (up to %d at a time). Send me the first few and we'll take it from there."
	CopyAskNameAgain         = "Just your first name is fine."
	CopyOneStepToBook        = "One step to book, about a minute: confirm your email and add a card. Nothing is charged until the task is done.\n\n%s\n\nI'll book it as soon as you finish."
	CopyOneStepToBookAgain   = "Here's a fresh link to finish booking:\n\n%s"
	CopySetupAlreadySent     = "I just sent you a link to finish booking. It works for 30 minutes. Lost it? Text me again in a couple of minutes for a new one, or reply CANCEL."
	CopyNeedCard             = "To book, add a card first (nothing is charged until the task is done):\n\n%s"
	CopyDropped              = "No problem, I won't book that. Text me anytime."
	CopyBooked               = "Booked. I'm finding a supporter now and will text you when someone accepts."
	CopyBookedAll            = "Booked all %d. I'm finding supporters now and will text you as each one is accepted."
	CopyBookedSome           = "Booked %d of %d so far. "
	CopyBookingInFlight      = "One moment, I'm booking it now."
	CopyCardProblem          = "Your card couldn't be used for this booking."
	CopyRetryYes             = "Once that's sorted, reply YES to book."
	Copy3DSHandoff           = "Your bank wants to confirm this payment. A HO:RA teammate will text you shortly to finish it."
	CopyPostFailed           = "Something went wrong on our side while booking. Reply YES to try again."
	CopyHandoffHolding       = "A HO:RA teammate will get back to you shortly."
	CopyHandoffStart         = "Let me get a HO:RA teammate for you. They'll text you here shortly — you can keep writing in the meantime."
	CopyHandoffReleased      = "You're back with me. What can I do for you?"
	CopyApprovalNote         = "%s's note: %s"
	CopyApprovalAsk          = "Reply YES to approve or NO to decline."
	CopyApprovalAskTimed     = "Reply YES to approve or NO to decline. No answer within %d minutes counts as a no."
	CopyApprovalReask        = "Your supporter is waiting on an answer: reply YES to approve or NO to decline."
	CopyApproved             = "Approved: %s. Your supporter can go ahead."
	CopyDeclined             = "Declined: %s. Your supporter has been told."
	CopyApprovalExpired      = "That request already timed out and was declined automatically."
	CopyApprovalAlready      = "That request was already %s."
	CopyApprovalGone         = "I couldn't find that request anymore — nothing was changed."
	CopyApprovalTimedOut     = "%s"
	CopyBackToDraft          = "Back to your new request:"
	TermsURL                 = "my-hora.com/terms"
)

// Summary is the message that asks for the YES. It states the price and the
// terms in the same breath because the YES that answers it is the consent.
func (r *Router) Summary(p *PendingDraft) string {
	items := p.Items()
	var b strings.Builder
	if len(items) == 1 {
		b.WriteString(r.itemLines(items[0], ""))
		fmt.Fprintf(&b, "\nEstimate: %s (you pay for the time actually used)", Dollars(p.QuoteCents))
	} else {
		for i, it := range items {
			if i > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(r.itemLines(it, fmt.Sprintf("%d. ", i+1)))
			fmt.Fprintf(&b, "\nEstimate: %s", Dollars(it.QuoteCents))
		}
		fmt.Fprintf(&b, "\n\nTotal estimate: %s (you pay for the time actually used)", Dollars(p.TotalCents()))
	}
	// The over-run consent, stated where the YES can see it (the app's
	// checkbox). One line either way, so the person always knows which.
	if p.NoAutoExtend {
		b.WriteString("\n" + CopyAutoExtendOff)
	} else {
		fmt.Fprintf(&b, "\n"+CopyAutoExtendOn, r.AutoExtendMinutes)
	}
	if len(items) == 1 {
		b.WriteString("\n\nReply YES to book, or tell me what to change.")
	} else {
		fmt.Fprintf(&b, "\n\nReply YES to book all %d, or tell me what to change.", len(items))
	}
	for _, u := range p.Unparsed {
		fmt.Fprintf(&b, "\n\nI couldn't work out \u201c%s\u201d — tell me a bit more and I'll add it.", u)
	}
	b.WriteString("\nBy booking you agree to our Terms: " + TermsURL)
	return b.String()
}

// itemLines is one errand: title, when, where.
func (r *Router) itemLines(it DraftItem, prefix string) string {
	d := it.Draft
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString(d.Title)
	b.WriteString("\n")
	b.WriteString(r.when(d.Scheduled, d.ScheduledTime))
	if it.QuoteMinutes > 0 {
		fmt.Fprintf(&b, " · about %d min", it.QuoteMinutes)
	}
	if where := where(d.Location1, d.Location2); where != "" {
		b.WriteString("\n")
		b.WriteString(where)
	}
	if it.BudgetCents > 0 {
		fmt.Fprintf(&b, "\nShopping budget: %s (covered by the hold; you pay what the receipt says)", Dollars(it.BudgetCents))
	}
	return b.String()
}

func (r *Router) when(scheduled bool, rfc string) string {
	if !scheduled {
		return "As soon as possible"
	}
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return "As soon as possible"
	}
	loc := r.loc()
	t = t.In(loc)
	now := r.now().In(loc)
	y1, m1, d1 := now.Date()
	y2, m2, d2 := t.Date()
	day := t.Format("Mon, Jan 2")
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		day = "Today"
	case now.AddDate(0, 0, 1).Format("2006-01-02") == t.Format("2006-01-02"):
		day = "Tomorrow"
	}
	return day + ", " + t.Format("3:04 PM")
}

func where(a, b string) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	switch {
	case a != "" && b != "":
		return a + " → " + b
	default:
		return a + b
	}
}

// Dollars renders cents as "$12" or "$12.50".
func Dollars(cents int) string {
	if cents%100 == 0 {
		return fmt.Sprintf("$%d", cents/100)
	}
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}
