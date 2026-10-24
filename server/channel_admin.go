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
	if n.Update {
		subject = fmt.Sprintf("[HO:RA] Chat handoff — new message — %s %s", n.Channel, n.PhoneHint)
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
	if !n.Update && o.sms != nil {
		text := fmt.Sprintf("[HO:RA] Chat handoff (%s) — %s %s. Details in the ops email / admin.", reasonLabelOps(n.Reason), n.Channel, n.PhoneHint)
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
		html.EscapeString(n.IdentityID), openRequestHTML(n), html.EscapeString(n.IdentityID), html.EscapeString(n.IdentityID), lines.String())
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
	r.POST("/admin/channel/:identity/release", dualAuth(sqlDB), requireOpsAdmin(), func(c *gin.Context) {
		id, ok := loadIdentityParam(c, store)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
		defer cancel()
		if err := router.Release(ctx, id); err != nil {
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
// (channel.HandoffTimeout without ops activity). The person's next message
// does the same on its own; this tells them without waiting for one.
func watchHandoffs(ctx context.Context, store *channel.PGStore, router *channel.Router, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepHandoffs(ctx, store, router)
		}
	}
}

func sweepHandoffs(ctx context.Context, store *channel.PGStore, router *channel.Router) {
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
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		if _, err := router.AutoRelease(c, id, false); err != nil {
			log.Printf("[channel-admin] handoff sweep identity=%s: %v", iid, err)
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
