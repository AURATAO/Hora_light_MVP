package main

// POST /webhooks/stripe/v2 — Stripe's v2 event destination (thin events).
//
// WHY A SECOND ENDPOINT. The accept gate keys on the transfers CAPABILITY, and
// on this platform the authoritative capability lives on the v2 Account
// (configuration.recipient.capabilities.stripe_balance.stripe_transfers — see
// transfersActiveFor). The v1 account.updated event fires when v1 fields
// change, and on 2026-09-22 the v2 capability stayed "restricted" while v1
// said "active": a later v2 activation would not necessarily fire a v1 event
// at all, and the cache would learn of it only when the supporter next
// opened the Earnings screen. The v2 event
// v2.core.account[configuration.recipient].capability_status_updated is the
// signal for exactly that transition, and it is delivered only by a v2 event
// destination, which uses thin payloads and its own signing secret.
//
// THIN MEANS THIN. The payload carries an id, a type and the related object's
// id — never the account's state. That is fine: the handler does not need the
// event's contents, it needs to know WHICH account to re-read, and it re-reads
// through readConnectStatus, the same path the Earnings screen takes, so the
// cache ends up exactly as a screen refresh would leave it.
//
// Verified with the same HMAC scheme as the v1 endpoint (stripe.ValidatePayload
// is what webhook.ConstructEvent calls), against STRIPE_V2_WEBHOOK_SECRET.
// Deduplicated through the same stripe_webhook_events table.

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v86"
)

// v2AccountEventPrefix matches every v2 event about an account:
// v2.core.account.created, v2.core.account[requirements].updated,
// v2.core.account[configuration.recipient].capability_status_updated, … All
// of them are answered the same way — re-read the account — so the handler
// does not enumerate them.
const v2AccountEventPrefix = "v2.core.account"

// thinEvent is the part of a v2 event notification this handler reads.
type thinEvent struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	RelatedObject *struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"related_object"`
	// The connected account the event is about, for events_from
	// other_accounts. Same id as related_object.id for account events, but
	// carried separately by Stripe and read as a fallback.
	Context string `json:"context"`
}

func handleStripeV2Webhook(c *gin.Context) {
	secret := strings.TrimSpace(os.Getenv("STRIPE_V2_WEBHOOK_SECRET"))
	if secret == "" {
		log.Printf("[stripe][webhook v2] no STRIPE_V2_WEBHOOK_SECRET set — rejecting")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, stripeMaxBodyBytes))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	if err := stripe.ValidatePayload(raw, c.GetHeader("Stripe-Signature"), secret); err != nil {
		log.Printf("[stripe][webhook v2] signature verification failed: %v", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var ev thinEvent
	if err := json.Unmarshal(raw, &ev); err != nil || ev.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	ctx := c.Request.Context()

	fresh, err := claimStripeEvent(ctx, ev.ID, ev.Type)
	if err != nil {
		log.Printf("[stripe][webhook v2] dedupe check failed event=%s: %v", ev.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "retry"})
		return
	}
	if !fresh {
		c.Status(http.StatusOK)
		return
	}

	if !strings.HasPrefix(ev.Type, v2AccountEventPrefix) {
		log.Printf("[stripe][webhook v2] unhandled type=%s — ignoring", ev.Type)
		markStripeEventProcessed(ctx, ev.ID)
		c.Status(http.StatusOK)
		return
	}

	accountID := ev.Context
	if ev.RelatedObject != nil && ev.RelatedObject.ID != "" {
		accountID = ev.RelatedObject.ID
	}
	var uid string
	if accountID == "" || db.QueryRow(ctx,
		`select id::text from public.users where stripe_account_id = $1`, accountID,
	).Scan(&uid) != nil {
		// Not ours, or a dashboard test event. Acknowledged, never retried.
		log.Printf("[stripe][webhook v2] event=%s type=%s account=%q has no user — ignoring", ev.ID, ev.Type, accountID)
		markStripeEventProcessed(ctx, ev.ID)
		c.Status(http.StatusOK)
		return
	}

	st, err := readConnectStatus(ctx, uid)
	if err != nil {
		// Leave the event unmarked so Stripe retries; processed_at stays
		// null, which is how "seen but not handled" reads in the table.
		log.Printf("[stripe][webhook v2][ERROR] event=%s account=%s user=%s: %v", ev.ID, accountID, uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "retry"})
		return
	}
	log.Printf("[stripe][webhook v2] event=%s type=%s account=%s user=%s → state=%s payouts_enabled=%v transfers_active=%v",
		ev.ID, ev.Type, accountID, uid, st.State, st.PayoutsEnabled, st.TransfersActive)
	markStripeEventProcessed(ctx, ev.ID)
	c.Status(http.StatusOK)
}
