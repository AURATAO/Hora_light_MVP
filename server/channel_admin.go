package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"hora-auth/internal/channel"
	"hora-auth/internal/notify"
)

// Ops side of a chat handoff (channel/handoff.go, M3):
//
//	GET  /admin/channel/handoffs            the conversations ops hold
//	GET  /admin/channel/:identity/thread    recent messages of one
//	POST /admin/channel/:identity/send      {text} → the person (queued, 202)
//	POST /admin/channel/:identity/release   back to the bot
//	POST /admin/channel/:identity/claim     on it (restarts the 10-minute timeout)
//
// Auth (S-13): dualAuth + requireOpsAdmin on every route, the same control
// as the rest of /admin. Every send and release writes an audit row with the
// actor and the identity id — never the number or the text (S-12).

// opsEmailNotifier is the OpsNotifier: one email to the ops allowlist per
// handoff, and one per message the person writes while held. The thread is
// in the email because that is what ops act on; the number is not (last four
// digits only), and the linked account is named by its email, as every other
// ops email does.
type opsEmailNotifier struct {
	sqlDB *sql.DB
	// emails: CHANNEL_OPS_EMAILS, else the ops-admin allowlist.
	emails []string
	// sms + numbers: CHANNEL_OPS_NUMBERS (E.164, comma-separated), texted
	// through the iMessage line on a new hand-off. Optional.
	sms     channel.Adapter
	numbers []string
	// touch stamps our outbound on the number's own thread before a text
	// goes out (D-17 R-3): an ops number may also be a requester's — a
	// teammate testing, or Aura's own phone — and our hand-off text must
	// make a question on that thread no longer the last thing we said, as
	// every other send does. No stamp, no text (fail closed).
	touch outboundToucher
	// lookup: the handed-off identity's address (tests); nil = the DB.
	lookup func(ctx context.Context, identityID string) (string, error)
}

// outboundToucher is PGStore.TouchOutbound.
type outboundToucher interface {
	TouchOutbound(ctx context.Context, ch channel.Channel, address string) (time.Time, error)
}

// opsSendTimeout bounds each hand-off send: the person is waiting on the
// outcome (notify.SendEmail has no timeout of its own).
var opsSendTimeout = 10 * time.Second

// newOpsNotifier reads the hand-off recipients from the environment.
// CHANNEL_OPS_EMAILS lets a deployment (staging) notify a different list
// from the admin allowlist, which also grants admin access.
func newOpsNotifier(sqlDB *sql.DB, sms channel.Adapter, touch outboundToucher) opsEmailNotifier {
	n := opsEmailNotifier{sqlDB: sqlDB, sms: sms, touch: touch}
	if v := strings.TrimSpace(os.Getenv("CHANNEL_OPS_EMAILS")); v != "" {
		for _, e := range strings.Split(v, ",") {
			if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
				n.emails = append(n.emails, e)
			}
		}
	} else {
		for e := range opsAdmins {
			n.emails = append(n.emails, e)
		}
		sortStrings(n.emails)
	}
	for _, raw := range strings.Split(os.Getenv("CHANNEL_OPS_NUMBERS"), ",") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if e164, err := channel.NormalizeE164(raw); err == nil {
			n.numbers = append(n.numbers, e164)
		} else {
			log.Printf("[channel-admin] CHANNEL_OPS_NUMBERS: an entry is not a phone number, skipped")
		}
	}
	return n
}

func (o opsEmailNotifier) NotifyOps(ctx context.Context, n channel.HandoffNotice) error {
	who := "not linked to an account"
	if n.UserID != "" {
		var email string
		if err := o.sqlDB.QueryRowContext(ctx,
			`select coalesce(email,'') from public.users where id = $1::uuid`, n.UserID).Scan(&email); err != nil {
			log.Printf("[channel-admin] handoff notice: user lookup uid=%s: %v", n.UserID, err)
		} else if email != "" {
			who = html.EscapeString(email)
		}
	}
	subject := fmt.Sprintf("[HO:RA] Chat handoff — %s %s", n.Channel, n.PhoneHint)
	if n.Reason == channel.HandoffOptedOutMessage {
		subject = optedOutMessageSubject(n)
	} else if n.OptedOut {
		subject = fmt.Sprintf("[HO:RA] Texts stopped — %s %s%s", n.Channel, n.PhoneHint, optOutTasksShort(n))
	} else if n.AutoReleased {
		subject = fmt.Sprintf("[HO:RA] Chat handoff — back to the bot (no reply in %s) — %s %s", channel.HandoffTimeout, n.Channel, n.PhoneHint)
	} else if n.Update {
		subject = fmt.Sprintf("[HO:RA] Chat handoff — new message — %s %s", n.Channel, n.PhoneHint)
	} else if n.Reason == channel.HandoffBookingFailed {
		subject = fmt.Sprintf("[HO:RA] Text booking failed — %s %s", n.Channel, n.PhoneHint)
	} else if n.Reason == channel.HandoffSupporterInterest {
		subject = fmt.Sprintf("[HO:RA] Someone asked about becoming a supporter — %s %s", n.Channel, n.PhoneHint)
	} else if n.Reason == channel.HandoffScopeUnchecked && n.Urgent {
		subject = fmt.Sprintf("[HO:RA] URGENT chat handoff (%s) — %s %s", n.UrgentReason, n.Channel, n.PhoneHint)
	} else if n.Reason == channel.HandoffScopeAlert {
		subject = fmt.Sprintf("[HO:RA] URGENT (%s) — declined, chat still with the bot — %s %s%s", n.UrgentReason, n.Channel, n.PhoneHint, optOutTasksShort(n))
	}
	body := opsNoticeHTML(n, who)
	// Sent here, not in the background: the router must know whether anyone
	// was reached before it tells the person a teammate is coming. In
	// parallel, each bounded by opsSendTimeout.
	reached := 0
	send, timeout := sendOpsEmail, opsSendTimeout // read once: sends may outlive this call
	if len(o.emails) > 0 {
		ok := make(chan bool, len(o.emails))
		for i, to := range o.emails {
			go func(i int, to string) {
				err := send(notify.EmailPayload{To: to, Subject: subject, Html: body})
				if err != nil {
					log.Printf("[channel-admin] handoff email #%d failed: %v", i+1, err)
				}
				ok <- err == nil
			}(i, to)
		}
		deadline := time.NewTimer(timeout)
		defer deadline.Stop()
	collect:
		for range o.emails {
			select {
			case good := <-ok:
				if good {
					reached++
				}
			case <-deadline.C:
				log.Printf("[channel-admin] handoff email: timed out after %s", timeout)
				break collect
			}
		}
	}
	// Supporter interest is a non-urgent notice: email only.
	if !n.Update && !n.AutoReleased && o.sms != nil && n.Reason != channel.HandoffSupporterInterest {
		text := fmt.Sprintf("[HO:RA] Chat handoff (%s) — %s %s. Details in the ops email / admin.", reasonLabelOps(n.Reason), n.Channel, n.PhoneHint)
		if n.OptedOut {
			text = fmt.Sprintf("[HO:RA] Texts stopped — %s %s opted out%s. Details in the ops email.", n.Channel, n.PhoneHint, optOutTasksShort(n))
		}
		if n.Reason == channel.HandoffOptedOutMessage {
			text = optedOutMessageText(n)
		}
		if n.Reason == channel.HandoffBookingFailed {
			text = fmt.Sprintf("[HO:RA] Text booking failed — %s %s. The bot still has the chat. Details in the ops email.", n.Channel, n.PhoneHint)
		}
		if n.Reason == channel.HandoffScopeUnchecked && n.Urgent {
			text = fmt.Sprintf("[HO:RA] URGENT chat handoff (%s) — %s %s. Nothing was drafted. Details in the ops email / admin.", n.UrgentReason, n.Channel, n.PhoneHint)
		}
		if n.Reason == channel.HandoffScopeAlert {
			text = fmt.Sprintf("[HO:RA] URGENT (%s) — %s %s was declined, nothing drafted%s. Check on any supporter with them. Details in the ops email.", n.UrgentReason, n.Channel, n.PhoneHint, optOutTasksShort(n))
		}
		own, err := o.addressOf(ctx, n.IdentityID)
		if err != nil {
			// Cannot tell whether an ops number is the person handed off:
			// no texts (fail closed); the emails above still went.
			log.Printf("[channel-admin] handoff texts not sent: identity lookup: %v", err)
		}
		for _, to := range o.numbers {
			if err != nil {
				break
			}
			if to == own {
				// The person handed off is this ops number: texting them
				// about themselves reaches no one else.
				log.Printf("[channel-admin] handoff text to %s skipped: it is the handed-off number", channel.PhoneHint(to))
				continue
			}
			if o.touch == nil {
				log.Printf("[channel-admin] handoff text to %s not sent: no outbound stamp", channel.PhoneHint(to))
				continue
			}
			if _, err := o.touch.TouchOutbound(ctx, o.sms.Channel(), to); err != nil {
				log.Printf("[channel-admin] handoff text to %s not sent: stamp: %v", channel.PhoneHint(to), err)
				continue
			}
			sctx, cancel := context.WithTimeout(ctx, timeout)
			_, err := o.sms.Send(sctx, channel.Outbound{Channel: o.sms.Channel(), To: to, Text: text})
			cancel()
			if err != nil {
				log.Printf("[channel-admin] handoff text failed to=%s: %v", channel.PhoneHint(to), err)
				continue
			}
			reached++
		}
	}
	if reached == 0 {
		return fmt.Errorf("handoff notice reached no one (%d emails, %d numbers configured)", len(o.emails), len(o.numbers))
	}
	return nil
}

// addressOf: the handed-off thread's own number. A test seam (lookup);
// without one it reads channel_identities.
func (o opsEmailNotifier) addressOf(ctx context.Context, identityID string) (string, error) {
	if o.lookup != nil {
		return o.lookup(ctx, identityID)
	}
	if o.sqlDB == nil {
		return "", fmt.Errorf("no database")
	}
	var addr string
	if err := o.sqlDB.QueryRowContext(ctx,
		`select address from channel_identities where id = $1::uuid`, identityID).Scan(&addr); err != nil {
		return "", fmt.Errorf("identity %s: %w", identityID, err)
	}
	return addr, nil
}

// sendOpsEmail is notify.SendEmail; a variable so tests can stand in.
var sendOpsEmail = notify.SendEmail

// opsNoticeHTML renders the notice. Every user-typed value is escaped; the
// number never appears (PhoneHint is the last four digits).
func opsNoticeHTML(n channel.HandoffNotice, who string) string {
	heading := fmt.Sprintf("Chat handed off (%s)", reasonLabelOps(n.Reason))
	if n.Detail != "" {
		heading = fmt.Sprintf("Chat handed off (%s: %s)", reasonLabelOps(n.Reason), n.Detail)
	}
	if n.Update {
		heading = "New message in a handed-off chat"
	}
	if n.Reason == channel.HandoffOptedOutMessage {
		return optedOutMessageHTML(n, who)
	}
	if n.OptedOut {
		return optOutNoticeHTML(n, who)
	}
	if n.AutoReleased {
		heading = fmt.Sprintf("No one replied within %s — the chat went back to the bot and the person was told. Follow up by email if it was a payment or a problem.", channel.HandoffTimeout)
	}
	if n.Reason == channel.HandoffSupporterInterest {
		// Not a hand-off: the bot sent the application link.
		heading = "Someone texted HO:RA asking about becoming a supporter. The bot sent them the application link; the chat is still with the bot. Nothing to do unless you want to follow up."
	}
	if n.Reason == channel.HandoffScopeAlert {
		// Not a hand-off: the person got only the decline; the chat is
		// still with the bot. Supporter safety comes first.
		heading = fmt.Sprintf("URGENT (%s): HO:RA declined this request and drafted nothing (%s). The chat is still with the bot. "+
			"If a supporter is on a task with this person, check on the supporter now; review the account.", n.UrgentReason, n.Detail)
	}
	if n.Reason == channel.HandoffBookingFailed {
		// Not a hand-off: nothing to reply to or release unless ops choose to.
		heading = "A text booking failed: " + n.BookingProblem + ". The chat is still with the bot. " +
			"If a card hold may have been placed, check Stripe for this account before they retry."
	}
	var lines strings.Builder
	for _, m := range n.Thread {
		side := "Them"
		if m.Direction == "outbound" {
			side = "HO:RA"
		}
		fmt.Fprintf(&lines, "<p style=\"margin:0 0 8px\"><b>%s</b> <span style=\"color:#888\">%s</span><br>%s</p>",
			side, m.At.Format("Jan 2 15:04"), html.EscapeString(m.Text))
	}
	return fmt.Sprintf(`<p><strong>%s</strong></p>
<ul>
  <li>Channel: %s</li>
  <li>Number: %s</li>
  <li>Account: %s</li>
  <li>Identity id: <code>%s</code></li>
</ul>
%s<p>Reply with <code>POST /admin/channel/%s/send {"text": "…"}</code>; hand back with <code>POST /admin/channel/%s/release</code>.</p>
<hr>%s`,
		html.EscapeString(heading), n.Channel, html.EscapeString(n.PhoneHint), who,
		html.EscapeString(n.IdentityID), openRequestHTML(n)+scopeTasksHTML(n), html.EscapeString(n.IdentityID), html.EscapeString(n.IdentityID), lines.String())
}

// scopeTasksHTML: for a scope alert, the person's tasks under way — a
// supporter may be with them right now.
func scopeTasksHTML(n channel.HandoffNotice) string {
	if n.Reason != channel.HandoffScopeAlert {
		return ""
	}
	switch {
	case n.OpenTasksUnknown:
		return "<p><b>Open tasks: could not be read.</b> Check the account in the admin panel — a supporter may be with them.</p>\n"
	case len(n.OpenTasks) == 0:
		return "<p>No open tasks.</p>\n"
	}
	var b strings.Builder
	b.WriteString("<p><b>Open tasks (a supporter may be with them):</b></p>\n<ul>\n")
	for _, t := range n.OpenTasks {
		fmt.Fprintf(&b, "  <li>%s <code>%s</code></li>\n", html.EscapeString(t.Title), html.EscapeString(t.ID))
	}
	b.WriteString("</ul>\n")
	return b.String()
}

// optOutTasksShort is the open-task part of the subject and the ops text.
func optOutTasksShort(n channel.HandoffNotice) string {
	switch {
	case n.OpenTasksUnknown:
		return " — open tasks UNKNOWN (lookup failed)"
	case len(n.OpenTasks) == 1:
		return " — HAS 1 OPEN TASK"
	case len(n.OpenTasks) > 1:
		return fmt.Sprintf(" — HAS %d OPEN TASKS", len(n.OpenTasks))
	}
	return ""
}

// optOutNoticeHTML: the person texted an opt-out keyword (or the provider
// refused a send with OPTED_OUT). Not a hand-off: there is nothing to reply
// to or release, and nothing can be texted to them until they send START.
func optOutNoticeHTML(n channel.HandoffNotice, who string) string {
	return fmt.Sprintf(`<p><strong>A number stopped texts from HO:RA</strong></p>
<p>It sent an opt-out keyword (STOP, CANCEL, END, QUIT, REVOKE, OPT OUT, UNSUBSCRIBE), or Sendblue refused a send with OPTED_OUT. Nothing more is texted to it until the person texts START; the app tells them how.</p>
<ul>
  <li>Channel: %s</li>
  <li>Number: %s</li>
  <li>Account: %s</li>
  <li>Identity id: <code>%s</code></li>
</ul>
%s<p>This chat is not handed off: it cannot be written to. Reach the person by email.</p>`,
		n.Channel, html.EscapeString(n.PhoneHint), who, html.EscapeString(n.IdentityID), optOutTasksHTML(n))
}

// optOutTasksHTML: the stopped person's tasks under way, for ops.
func optOutTasksHTML(n channel.HandoffNotice) string {
	var tasks strings.Builder
	switch {
	case n.OpenTasksUnknown:
		tasks.WriteString("<p><b>Open tasks: could not be read.</b> Check the account in the admin panel — a supporter may be waiting on them.</p>\n")
	case len(n.OpenTasks) > 0:
		tasks.WriteString("<p><b>They have open tasks.</b> Their supporter's chat messages no longer reach them by text, and approval questions are not asked (they decline on their timer). The supporter's task screen says the requester can't be reached by text.</p>\n<ul>\n")
		for _, t := range n.OpenTasks {
			fmt.Fprintf(&tasks, "  <li>%s <code>%s</code></li>\n", html.EscapeString(t.Title), html.EscapeString(t.ID))
		}
		tasks.WriteString("</ul>\n")
	case n.UserID != "":
		tasks.WriteString("<p>No open tasks.</p>\n")
	}
	return tasks.String()
}

// A number that stopped texts wrote to us (channel/optout_forward.go). Ops
// read it and reach the person another way; nothing can be texted back.

func optedOutMessageSubject(n channel.HandoffNotice) string {
	if n.Urgent {
		return fmt.Sprintf("[HO:RA] URGENT (%s) — message from a number that stopped texts — %s %s%s", n.UrgentReason, n.Channel, n.PhoneHint, optOutTasksShort(n))
	}
	return fmt.Sprintf("[HO:RA] Message from a number that stopped texts — %s %s%s", n.Channel, n.PhoneHint, optOutTasksShort(n))
}

// optedOutMessageText is the ops text: the newest words, cut short.
func optedOutMessageText(n channel.HandoffNotice) string {
	last := ""
	if len(n.Thread) > 0 {
		last = strings.TrimSpace(n.Thread[len(n.Thread)-1].Text)
	}
	if r := []rune(last); len(r) > opsTextQuoteRunes {
		last = string(r[:opsTextQuoteRunes]) + "…"
	}
	head := "[HO:RA]"
	if n.Urgent {
		head = fmt.Sprintf("[HO:RA] URGENT (%s):", n.UrgentReason)
	}
	more := ""
	if len(n.Thread) > 1 {
		more = fmt.Sprintf(" (%d messages, newest shown)", len(n.Thread))
	}
	return fmt.Sprintf("%s %s %s stopped texts and wrote%s: \"%s\"%s. Do NOT text them — call or email. Details in the ops email.",
		head, n.Channel, n.PhoneHint, more, last, optOutTasksShort(n))
}

// opsTextQuoteRunes: how much of the person's words an ops text quotes.
const opsTextQuoteRunes = 160

func optedOutMessageHTML(n channel.HandoffNotice, who string) string {
	heading := "A number that stopped texts wrote to HO:RA"
	if n.Urgent {
		heading = fmt.Sprintf("URGENT (%s): a number that stopped texts wrote to HO:RA", n.UrgentReason)
	}
	var lines strings.Builder
	for _, m := range n.Thread {
		fmt.Fprintf(&lines, "<p style=\"margin:0 0 8px\"><b>Them</b> <span style=\"color:#888\">%s</span><br>%s</p>",
			m.At.Format("Jan 2 15:04"), html.EscapeString(m.Text))
	}
	return fmt.Sprintf(`<p><strong>%s</strong></p>
<p><b>Do not reply by text.</b> The number opted out of texts (TCPA): nothing is sent to it, and Sendblue would refuse it. Reach the person by phone call, email or through the app.</p>
<ul>
  <li>Channel: %s</li>
  <li>Number: %s</li>
  <li>Account: %s</li>
  <li>Identity id: <code>%s</code></li>
</ul>
%s<p>Ordinary messages from this number come at most once every %s, batched; urgent ones come at once. Texting START turns texts back on.</p>
<hr>%s`,
		html.EscapeString(heading), n.Channel, html.EscapeString(n.PhoneHint), who, html.EscapeString(n.IdentityID),
		optOutTasksHTML(n), channel.OptedOutForwardWindow, lines.String())
}

// openRequestHTML: an approval question armed at hand-off. It stays pending
// in the app (auto-declines on its timer) and is not asked again by text.
func openRequestHTML(n channel.HandoffNotice) string {
	if n.OpenRequestID == "" {
		return ""
	}
	if n.OpenRequestUnconfirmed {
		return fmt.Sprintf("<p><b>Open request:</b> the supporter's request <code>%s</code> on task <code>%s</code> was being sent when the chat was handed off. It may have reached the requester; any YES/NO they send now comes to you. It WILL be asked again by text after release unless it is settled first.</p>\n",
			html.EscapeString(n.OpenRequestID), html.EscapeString(n.OpenRequestTaskID))
	}
	return fmt.Sprintf("<p><b>Open request:</b> the supporter's request <code>%s</code> on task <code>%s</code> was waiting for a YES/NO. It is still pending in the app and will NOT be asked again by text — settle it with the requester.</p>\n",
		html.EscapeString(n.OpenRequestID), html.EscapeString(n.OpenRequestTaskID))
}

func reasonLabelOps(r channel.HandoffReason) string {
	switch r {
	case channel.HandoffHumanRequested:
		return "asked for a person"
	case channel.HandoffNotUnderstood:
		return "not understood twice"
	case channel.HandoffPayment3DS:
		return "payment needs authentication"
	case channel.HandoffOptedOut:
		return "stopped texts"
	case channel.HandoffBookingFailed:
		return "a text booking failed"
	case channel.HandoffScopeAlert:
		return "a romantic, sexual or physical request was declined"
	case channel.HandoffSupporterInterest:
		return "asked about becoming a supporter"
	case channel.HandoffScopeUnchecked:
		return "the scope check could not read a message — decide whether HO:RA can do it; nothing was drafted"
	case channel.HandoffAgentEscalated:
		return "the assistant judged a person should handle it (safety, dispute, legal/medical or repeated confusion)"
	}
	return string(r)
}

func registerChannelAdminRoutes(r *gin.Engine, sqlDB *sql.DB, store *channel.PGStore, router *channel.Router) {
	r.GET("/admin/channel/handoffs", dualAuth(sqlDB), requireOpsAdmin(), func(c *gin.Context) {
		rows, err := store.HandedOff(c.Request.Context())
		if err != nil {
			log.Printf("[channel-admin] handoffs: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
			return
		}
		if rows == nil {
			rows = []channel.HandoffRow{}
		}
		c.JSON(http.StatusOK, gin.H{"handoffs": rows})
	})
	// The ledger behind the ops Chats page: every conversation, newest
	// first. Hints and states only — the thread endpoint carries text, one
	// chat at a time, and never a full number (S-12).
	r.GET("/admin/channel/conversations", dualAuth(sqlDB), requireOpsAdmin(), func(c *gin.Context) {
		rows, err := store.Conversations(c.Request.Context(), 100)
		if err != nil {
			log.Printf("[channel-admin] conversations: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
			return
		}
		if rows == nil {
			rows = []channel.ConversationRow{}
		}
		c.JSON(http.StatusOK, gin.H{"conversations": rows, "handoff_timeout_minutes": int(channel.HandoffTimeout.Minutes())})
	})
	r.GET("/admin/channel/:identity/thread", dualAuth(sqlDB), requireOpsAdmin(), func(c *gin.Context) {
		id, ok := loadIdentityParam(c, store)
		if !ok {
			return
		}
		msgs, err := store.RecentMessages(c.Request.Context(), id.Channel, id.Address, 50)
		if err != nil {
			log.Printf("[channel-admin] thread identity=%s: %v", id.ID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
			return
		}
		if msgs == nil {
			msgs = []channel.ThreadMessage{}
		}
		attachThreadPhotos(c.Request.Context(), store, id.Channel, msgs)
		c.JSON(http.StatusOK, gin.H{"identity_id": id.ID, "channel": id.Channel, "phone_hint": channel.PhoneHint(id.Address), "messages": msgs})
	})
	r.POST("/admin/channel/:identity/send", dualAuth(sqlDB), requireOpsAdmin(), func(c *gin.Context) {
		id, ok := loadIdentityParam(c, store)
		if !ok {
			return
		}
		var body struct {
			Text string `json:"text"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Text) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing_text"})
			return
		}
		text := strings.TrimSpace(body.Text)
		if len(text) > 1500 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "text_too_long", "message": "Keep it under 1500 characters."})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
		defer cancel()
		err := router.OpsSend(ctx, id, text)
		switch {
		case errors.Is(err, channel.ErrNotInHandoff):
			c.JSON(http.StatusConflict, gin.H{"error": "not_handed_off", "message": "This chat is with the bot. Nothing was sent."})
			return
		case err != nil:
			log.Printf("[channel-admin] send identity=%s by=%s: %v", id.ID, c.GetString("uid"), err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
			return
		}
		writeAudit(ctx, systemActorUID, c.GetString("uid"), "CHANNEL_OPS_SEND", "", map[string]any{
			"identity_id": id.ID, "channel": id.Channel, "chars": len(text),
		})
		// Queued: delivery runs off the request path (S-32).
		c.JSON(http.StatusAccepted, gin.H{"queued": true})
	})
	r.POST("/admin/channel/:identity/claim", dualAuth(sqlDB), requireOpsAdmin(), func(c *gin.Context) {
		// Ops are on it without writing yet: the 10-minute timeout restarts.
		id, ok := loadIdentityParam(c, store)
		if !ok {
			return
		}
		err := router.OpsClaim(c.Request.Context(), id)
		switch {
		case errors.Is(err, channel.ErrNotInHandoff):
			c.JSON(http.StatusConflict, gin.H{"error": "not_handed_off", "message": "This chat is with the bot."})
			return
		case err != nil:
			log.Printf("[channel-admin] claim identity=%s by=%s: %v", id.ID, c.GetString("uid"), err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
			return
		}
		writeAudit(c.Request.Context(), systemActorUID, c.GetString("uid"), "CHANNEL_OPS_CLAIM", "", map[string]any{
			"identity_id": id.ID, "channel": id.Channel,
		})
		c.JSON(http.StatusOK, gin.H{"claimed": true})
	})
	r.POST("/admin/channel/:identity/release", dualAuth(sqlDB), requireOpsAdmin(), func(c *gin.Context) {
		id, ok := loadIdentityParam(c, store)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
		defer cancel()
		err := router.Release(ctx, id)
		switch {
		case errors.Is(err, channel.ErrNotInHandoff):
			c.JSON(http.StatusConflict, gin.H{"error": "not_handed_off", "message": "This chat is already with the bot."})
			return
		case err != nil:
			log.Printf("[channel-admin] release identity=%s by=%s: %v", id.ID, c.GetString("uid"), err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
			return
		}
		writeAudit(ctx, systemActorUID, c.GetString("uid"), "CHANNEL_OPS_RELEASE", "", map[string]any{
			"identity_id": id.ID, "channel": id.Channel,
		})
		c.JSON(http.StatusOK, gin.H{"released": true})
	})
}

// watchHandoffs hands stale hand-offs back to the bot every `every`
// (channel.HandoffTimeout without ops activity), settles bookings stuck in
// flight (channel.PostingStuckAfter), and sends ops the batched messages of
// stopped numbers (channel.OptedOutForwardWindow). The person's next message does the
// same on its own; this tells them without waiting for one.
func watchHandoffs(ctx context.Context, store *channel.PGStore, router *channel.Router, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			func() {
				// A panic here must not take the API down with it.
				defer func() {
					if p := recover(); p != nil {
						log.Printf("[channel-admin] handoff sweep panic: %v", p)
					}
				}()
				sweepHandoffs(ctx, store, router)
			}()
			func() {
				defer func() {
					if p := recover(); p != nil {
						log.Printf("[channel-admin] posting sweep panic: %v", p)
					}
				}()
				sweepPostings(ctx, store, router)
			}()
			func() {
				defer func() {
					if p := recover(); p != nil {
						log.Printf("[channel-admin] opted-out forward sweep panic: %v", p)
					}
				}()
				sweepOptedOutForwards(ctx, store, router)
			}()
			func() {
				defer func() {
					if p := recover(); p != nil {
						log.Printf("[channel-admin] photo sweep panic: %v", p)
					}
				}()
				runPhotoSweep(ctx, "retention")
			}()
		}
	}
}

func sweepHandoffs(ctx context.Context, store *channel.PGStore, router *channel.Router) {
	if err := store.StartHandoffClocks(ctx); err != nil {
		log.Printf("[channel-admin] handoff sweep: %v", err)
		return
	}
	ids, err := store.StaleHandoffs(ctx, time.Now().Add(-channel.HandoffTimeout))
	if err != nil {
		log.Printf("[channel-admin] handoff sweep: %v", err)
		return
	}
	for _, iid := range ids {
		id, err := store.IdentityByID(ctx, iid)
		if err != nil {
			log.Printf("[channel-admin] handoff sweep identity=%s: %v", iid, err)
			continue
		}
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		if _, err := router.AutoRelease(c, id, false); err != nil {
			log.Printf("[channel-admin] handoff sweep identity=%s: %v", iid, err)
		}
		cancel()
	}
}

// sweepPostings settles every booking claimed more than
// channel.PostingStuckAfter ago: booked if its task was made, otherwise back
// to awaiting a YES with the person and ops told (Router.RecoverStuckPosting).
func sweepPostings(ctx context.Context, store *channel.PGStore, router *channel.Router) {
	ids, err := store.StalePostings(ctx, time.Now().Add(-channel.PostingStuckAfter))
	if err != nil {
		log.Printf("[channel-admin] posting sweep: %v", err)
		return
	}
	for _, iid := range ids {
		id, err := store.IdentityByID(ctx, iid)
		if err != nil {
			log.Printf("[channel-admin] posting sweep identity=%s: %v", iid, err)
			continue
		}
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		if _, err := router.RecoverStuckPosting(c, id, false); err != nil {
			log.Printf("[channel-admin] posting sweep identity=%s: %v", iid, err)
		}
		cancel()
	}
}

// sweepOptedOutForwards sends ops the messages a stopped number wrote while
// its forward window was closed, once it has passed (channel/optout_forward.go).
func sweepOptedOutForwards(ctx context.Context, store *channel.PGStore, router *channel.Router) {
	ids, err := store.WaitingOptedOutForwards(ctx, channel.OptedOutForwardWindow)
	if err != nil {
		log.Printf("[channel-admin] opted-out forward sweep: %v", err)
		return
	}
	for _, iid := range ids {
		id, err := store.IdentityByID(ctx, iid)
		if err != nil {
			log.Printf("[channel-admin] opted-out forward sweep identity=%s: %v", iid, err)
			continue
		}
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		if err := router.FlushOptedOut(c, id); err != nil {
			log.Printf("[channel-admin] opted-out forward sweep identity=%s: %v", iid, err)
		}
		cancel()
	}
}

func loadIdentityParam(c *gin.Context, store *channel.PGStore) (channel.Identity, bool) {
	id, err := store.IdentityByID(c.Request.Context(), c.Param("identity"))
	if errors.Is(err, channel.ErrIdentityNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return channel.Identity{}, false
	}
	if err != nil {
		log.Printf("[channel-admin] identity lookup: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
		return channel.Identity{}, false
	}
	return id, true
}
