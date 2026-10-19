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
	CopyTextOnly           = "I can only read text for now. Tell me what you need and when."
	CopyParseFailed        = "Sorry, I couldn't work that out. Could you say what you need, where, and when?"
	CopyAskName            = "Got it: %s.\n\nBefore I price it, what's your first name?"
	CopyAskNameAgain       = "Just your first name is fine."
	CopyOneStepToBook      = "One step to book, about a minute: confirm your email and add a card. Nothing is charged until the task is done.\n\n%s\n\nI'll book it as soon as you finish."
	CopyOneStepToBookAgain = "Here's a fresh link to finish booking:\n\n%s"
	CopySetupAlreadySent   = "I just sent you a link to finish booking. It works for 30 minutes. Lost it? Text me again in a couple of minutes for a new one, or reply CANCEL."
	CopyNeedCard           = "To book, add a card first (nothing is charged until the task is done):\n\n%s"
	CopyDropped            = "No problem, I won't book that. Text me anytime."
	CopyBooked             = "Booked. I'm finding a supporter now and will text you when someone accepts."
	CopyBookingInFlight    = "One moment, I'm booking it now."
	CopyCardProblem        = "Your card couldn't be used for this booking."
	CopyRetryYes           = "Once that's sorted, reply YES to book."
	Copy3DSHandoff         = "Your bank wants to confirm this payment. A HO:RA teammate will text you shortly to finish it."
	CopyPostFailed         = "Something went wrong on our side while booking. Reply YES to try again."
	CopyHandoffHolding     = "A HO:RA teammate will get back to you shortly."
	TermsURL               = "my-hora.com/terms"
)

// Summary is the message that asks for the YES. It states the price and the
// terms in the same breath because the YES that answers it is the consent.
func (r *Router) Summary(p *PendingDraft) string {
	d := p.Draft
	var b strings.Builder
	b.WriteString(d.Title)
	b.WriteString("\n")
	b.WriteString(r.when(d.Scheduled, d.ScheduledTime))
	if p.QuoteMinutes > 0 {
		fmt.Fprintf(&b, " · about %d min", p.QuoteMinutes)
	}
	if where := where(d.Location1, d.Location2); where != "" {
		b.WriteString("\n")
		b.WriteString(where)
	}
	fmt.Fprintf(&b, "\nEstimate: %s (you pay for the time actually used)", Dollars(p.QuoteCents))
	b.WriteString("\n\nReply YES to book, or tell me what to change.")
	b.WriteString("\nBy booking you agree to our Terms: " + TermsURL)
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
