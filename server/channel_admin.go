package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"hora-auth/internal/channel"
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
type opsEmailNotifier struct{ sqlDB *sql.DB }

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
	emailOpsAdmins(subject, body)
	return nil
}

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
