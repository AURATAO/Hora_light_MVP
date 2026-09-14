package main

// The card on file: a Stripe Customer per user, a SetupIntent to save a card
// against it, and the list/remove surface behind Profile → Payment methods.
//
// WHY A SETUPINTENT AND NOT A PAYMENTINTENT. The card is collected long before
// it is charged — often days before, and by definition while the requester is
// looking at their profile rather than posting a task. A SetupIntent is the
// flow whose entire purpose is "authenticate this card now so it can be
// charged later without the customer present": it runs 3DS at save time, and
// the resulting PaymentMethod carries the issuer's consent for off-session
// use. Collecting the card with a PaymentIntent instead would leave every
// future pre-auth needing a fresh challenge, which is the one thing the
// off-session hold at post time cannot survive.
//
// WHAT THE CLIENT GETS. POST /payments/setup-intent returns everything Stripe's
// PaymentSheet needs in one call — client secret, customer, ephemeral key —
// because PaymentSheet in setup mode needs all three and a client that has to
// assemble them from three round trips has three ways to get it wrong.
//
// IDENTITY. Every handler here resolves the Customer from the session uid via
// users.stripe_customer_id. The client never names a customer, never names a
// PaymentMethod it does not own, and cannot reach another account's cards by
// guessing a pm_… id — see requireOwnedPaymentMethod.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/customer"
	"github.com/stripe/stripe-go/v86/ephemeralkey"
	"github.com/stripe/stripe-go/v86/paymentmethod"
	"github.com/stripe/stripe-go/v86/setupintent"
)

// stripeEphemeralKeyVersion is the API version the ephemeral key is minted
// for. It must match the version the MOBILE SDK speaks, not the version this
// server speaks — the key is used by the phone, not by us, and Stripe rejects
// a mismatched key with an unhelpful 400 at sheet-present time.
//
// Bump this only in lockstep with @stripe/stripe-react-native; the value is
// the one that SDK's release notes name, not stripe.APIVersion.
const stripeEphemeralKeyVersion = "2025-08-27.basil"

// RegisterPaymentRoutes mounts the card-on-file surface. All four are session-
// authenticated and gated by requirePaymentsEnabled, so an unconfigured Stripe
// answers 503 before any handler runs rather than 500 from inside one.
func RegisterPaymentRoutes(r *gin.Engine, authMiddleware gin.HandlerFunc) {
	p := r.Group("/payments")
	p.Use(authMiddleware, requirePaymentsEnabled())
	{
		p.POST("/setup-intent", createSetupIntentHandler)
		p.GET("/payment-methods", listPaymentMethodsHandler)
		p.DELETE("/payment-methods/:id", deletePaymentMethodHandler)
	}
}

// ── The Customer ───────────────────────────────────────────────────────────

// stripeCustomerFor returns the user's Stripe Customer id, creating it on
// first use.
//
// Lazily, and this matters: most beta accounts never add a card, and a
// Customer minted at signup is a permanent record in Stripe's database for
// someone who never transacts. The first call that needs one — opening the
// card sheet — is the first call that creates one.
//
// The UPDATE is conditional on the column still being null, so two concurrent
// first-calls cannot leave one Customer orphaned and unnamed: the loser of the
// race re-reads the winner's id and abandons its own. The orphan it created is
// an empty Customer with no cards and no charges, which is inert — the
// alternative (an advisory lock around a network call) buys nothing.
func stripeCustomerFor(ctx context.Context, uid, email string) (string, error) {
	if !paymentsEnabled() {
		return "", errPaymentsDisabled
	}

	var existing *string
	if err := db.QueryRow(ctx,
		`select stripe_customer_id from public.users where id = $1::uuid`, uid,
	).Scan(&existing); err != nil {
		return "", fmt.Errorf("payments: read customer for user %s: %w", uid, err)
	}
	if existing != nil && *existing != "" {
		return *existing, nil
	}

	params := &stripe.CustomerParams{
		Metadata: map[string]string{"user_id": uid},
	}
	if email != "" {
		params.Email = stripe.String(email)
	}
	// Two requests from one user arriving together would otherwise mint two
	// Customers; keyed on the uid so the second is answered from Stripe's own
	// idempotency cache with the first one's object.
	params.SetIdempotencyKey("customer_" + uid)

	cus, err := customer.New(params)
	if err != nil {
		return "", fmt.Errorf("payments: create customer for user %s: %w", uid, err)
	}

	var stored string
	err = db.QueryRow(ctx, `
		update public.users
		   set stripe_customer_id = coalesce(stripe_customer_id, $2)
		 where id = $1::uuid
		returning stripe_customer_id
	`, uid, cus.ID).Scan(&stored)
	if err != nil {
		return "", fmt.Errorf("payments: persist customer %s for user %s: %w", cus.ID, uid, err)
	}
	if stored != cus.ID {
		log.Printf("[payments] customer race for user=%s — keeping %s, discarding %s", uid, stored, cus.ID)
		return stored, nil
	}

	log.Printf("[payments] created customer=%s for user=%s", cus.ID, uid)
	return stored, nil
}

// ── POST /payments/setup-intent ────────────────────────────────────────────

type setupIntentResponse struct {
	ClientSecret    string `json:"client_secret"`
	CustomerID      string `json:"customer_id"`
	EphemeralKey    string `json:"ephemeral_key"`
	PublishableKey  string `json:"publishable_key"`
	MerchantDisplay string `json:"merchant_display_name"`
}

func createSetupIntentHandler(c *gin.Context) {
	uid := c.GetString("uid")
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	customerID, err := stripeCustomerFor(ctx, uid, strings.TrimSpace(c.GetString("email")))
	if err != nil {
		log.Printf("[payments][setup-intent] customer for uid=%s: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "payments_error"})
		return
	}

	si, err := setupintent.New(&stripe.SetupIntentParams{
		Customer: stripe.String(customerID),
		// The whole reason this endpoint exists: the saved card has to work at
		// post time with nobody looking at the phone.
		Usage:              stripe.String(string(stripe.SetupIntentUsageOffSession)),
		PaymentMethodTypes: stripe.StringSlice([]string{"card"}),
		Metadata:           map[string]string{"user_id": uid},
	})
	if err != nil {
		log.Printf("[payments][setup-intent] create for uid=%s: %v", uid, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "payments_error", "message": stripeUserMessage(err)})
		return
	}

	// PaymentSheet needs an ephemeral key to read and attach payment methods on
	// the customer directly. It is short-lived (about an hour) and scoped to
	// this one customer, which is what makes handing it to a phone safe.
	key, err := ephemeralkey.New(&stripe.EphemeralKeyParams{
		Customer:      stripe.String(customerID),
		StripeVersion: stripe.String(stripeEphemeralKeyVersion),
	})
	if err != nil {
		log.Printf("[payments][setup-intent] ephemeral key for uid=%s: %v", uid, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "payments_error"})
		return
	}

	c.JSON(http.StatusOK, setupIntentResponse{
		ClientSecret: si.ClientSecret,
		CustomerID:   customerID,
		EphemeralKey: key.Secret,
		// Sent rather than configured per client so a key rotation is one
		// backend env change instead of a web deploy plus a native rebuild.
		PublishableKey:  stripePublishableKey(),
		MerchantDisplay: "HO:RA",
	})
}

// ── GET /payments/payment-methods ──────────────────────────────────────────

// SavedCard is one card on file, reduced to what a list row can render. No
// fingerprint, no token, nothing that would let a leaked response be replayed
// — the pm_… id is included because removing a card needs it and because it is
// useless without this account's session.
type SavedCard struct {
	ID        string `json:"id"`
	Brand     string `json:"brand"`
	Last4     string `json:"last4"`
	ExpMonth  int64  `json:"exp_month"`
	ExpYear   int64  `json:"exp_year"`
	IsDefault bool   `json:"is_default"`
}

type paymentMethodsResponse struct {
	Cards []SavedCard `json:"cards"`
	// Convenience for the post-task gate, which asks one question ("can this
	// requester pay?") and should not have to learn the shape of a card to ask
	// it.
	HasCard bool `json:"has_card"`
	// The publishable key, sent here as well as on /setup-intent so a surface
	// that only needs to RUN a 3DS challenge (Post Task) can get a Stripe
	// instance without minting a SetupIntent it will never confirm. Public by
	// design (S-12); it is the key the card form is built with.
	PublishableKey string `json:"publishable_key"`
	// Whether posting currently requires a card (PAYMENTS_ENFORCED).
	//
	// The flag itself stays server-side — a 402 from POST /tasks is the only
	// thing that actually enforces it, and a client that lied about this field
	// would simply meet that 402. It is reported so the clients can put the
	// "add a card first" prompt in front of the requester BEFORE they fill in
	// a whole form, and so that beta users are shown nothing at all while the
	// flag is off.
	PaymentsEnforced bool `json:"payments_enforced"`
}

func listPaymentMethodsHandler(c *gin.Context) {
	uid := c.GetString("uid")
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	customerID, err := stripeCustomerFor(ctx, uid, strings.TrimSpace(c.GetString("email")))
	if err != nil {
		log.Printf("[payments][cards] customer for uid=%s: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "payments_error"})
		return
	}

	cards, _, err := savedCardsFor(customerID)
	if err != nil {
		log.Printf("[payments][cards] list for customer=%s: %v", customerID, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "payments_error"})
		return
	}

	c.JSON(http.StatusOK, paymentMethodsResponse{
		Cards:            cards,
		HasCard:          len(cards) > 0,
		PublishableKey:   stripePublishableKey(),
		PaymentsEnforced: paymentsEnforced(),
	})
}

// savedCardsFor lists the customer's cards newest-first and reports which one
// a pre-auth would use. Stripe returns payment methods in reverse creation
// order, so "the default" with nothing explicitly set is the first element —
// the card the requester most recently chose to save.
func savedCardsFor(customerID string) (cards []SavedCard, defaultID string, err error) {
	cus, err := customer.Get(customerID, &stripe.CustomerParams{})
	if err != nil {
		return nil, "", err
	}
	if cus.InvoiceSettings != nil && cus.InvoiceSettings.DefaultPaymentMethod != nil {
		defaultID = cus.InvoiceSettings.DefaultPaymentMethod.ID
	}

	iter := paymentmethod.List(&stripe.PaymentMethodListParams{
		Customer: stripe.String(customerID),
		Type:     stripe.String("card"),
	})
	for iter.Next() {
		pm := iter.PaymentMethod()
		if pm.Card == nil {
			continue
		}
		cards = append(cards, SavedCard{
			ID:       pm.ID,
			Brand:    string(pm.Card.Brand),
			Last4:    pm.Card.Last4,
			ExpMonth: pm.Card.ExpMonth,
			ExpYear:  pm.Card.ExpYear,
		})
	}
	if err := iter.Err(); err != nil {
		return nil, "", err
	}

	// Nothing explicitly default: the newest card is what gets charged, so say
	// so rather than showing a list with no marked card and then silently
	// charging one of them.
	if defaultID == "" && len(cards) > 0 {
		defaultID = cards[0].ID
	}
	for i := range cards {
		cards[i].IsDefault = cards[i].ID == defaultID
	}
	return cards, defaultID, nil
}

// defaultPaymentMethodFor is what CreatePreAuth charges. Empty means the
// requester has no card on file — the caller answers 402, never 500.
func defaultPaymentMethodFor(customerID string) (string, error) {
	_, defaultID, err := savedCardsFor(customerID)
	return defaultID, err
}

// ── DELETE /payments/payment-methods/:id ───────────────────────────────────

var errPaymentMethodNotOwned = errors.New("payments: payment method does not belong to this customer")

// requireOwnedPaymentMethod refuses to act on a pm_… the session's customer
// does not own. Without this check the id in the URL would be the only thing
// naming the card, and pm ids are guessable enough in aggregate that "detach
// by id" would be a way to remove strangers' cards.
func requireOwnedPaymentMethod(pmID, customerID string) (*stripe.PaymentMethod, error) {
	pm, err := paymentmethod.Get(pmID, &stripe.PaymentMethodParams{})
	if err != nil {
		return nil, err
	}
	if pm.Customer == nil || pm.Customer.ID != customerID {
		return nil, errPaymentMethodNotOwned
	}
	return pm, nil
}

func deletePaymentMethodHandler(c *gin.Context) {
	uid := c.GetString("uid")
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	pmID := strings.TrimSpace(c.Param("id"))
	if !strings.HasPrefix(pmID, "pm_") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_payment_method"})
		return
	}
	ctx := c.Request.Context()

	customerID, err := stripeCustomerFor(ctx, uid, strings.TrimSpace(c.GetString("email")))
	if err != nil {
		log.Printf("[payments][cards] customer for uid=%s: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "payments_error"})
		return
	}

	if _, err := requireOwnedPaymentMethod(pmID, customerID); err != nil {
		if errors.Is(err, errPaymentMethodNotOwned) {
			// 404, not 403: confirming that a card exists but belongs to
			// somebody else is itself the leak.
			log.Printf("[payments][cards] uid=%s tried to remove unowned pm=%s", uid, pmID)
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		log.Printf("[payments][cards] fetch pm=%s: %v", pmID, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}

	// A card in use by a live hold cannot be removed: detaching it does not
	// release the hold, it just removes our ability to name what is holding
	// the requester's money. They cancel the task first, or keep the card.
	inUse, err := paymentMethodHasLiveHold(ctx, uid, customerID, pmID)
	if err != nil {
		log.Printf("[payments][cards] live-hold check uid=%s pm=%s: %v", uid, pmID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if inUse {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "payment_method_in_use",
			"message": "This card is holding funds for a task that hasn't finished yet. Cancel or complete that task first.",
		})
		return
	}

	if _, err := paymentmethod.Detach(pmID, &stripe.PaymentMethodDetachParams{}); err != nil {
		log.Printf("[payments][cards] detach pm=%s: %v", pmID, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "payments_error", "message": stripeUserMessage(err)})
		return
	}

	log.Printf("[payments][cards] removed pm=%s for uid=%s", pmID, uid)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// paymentMethodHasLiveHold reports whether removing this card would strand a
// hold the requester can no longer see.
//
// Only the card a pre-auth would actually charge is protected. With several on
// file, detaching one that is not the charging card cannot affect any live
// hold, so it is allowed; with one on file — every beta account — the two
// questions collapse into the same answer.
//
// Which live hold used which card is not recorded locally, so the check is
// "does this requester have ANY live hold". Erring toward blocked costs a
// requester one extra tap; erring the other way costs them money they cannot
// account for.
func paymentMethodHasLiveHold(ctx context.Context, uid, customerID, pmID string) (bool, error) {
	defaultID, err := defaultPaymentMethodFor(customerID)
	if err != nil {
		return false, err
	}
	if pmID != defaultID {
		return false, nil
	}
	var live bool
	err = db.QueryRow(ctx, `
		select exists (
			select 1 from public.payments
			 where requester_id = $1::uuid
			   and status in ('requires_auth','authorized')
		)
	`, uid).Scan(&live)
	return live, err
}
