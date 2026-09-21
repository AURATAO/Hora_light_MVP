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
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
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

		// What a requester owes from a completion that could not be charged,
		// and the one-tap retry that clears it. GET is polled by the banner on
		// both clients; POST is the Settle button.
		p.GET("/outstanding-balance", outstandingBalanceHandler)
		p.POST("/settle-balance", settleOutstandingBalance)
	}
}

// ── The Customer ───────────────────────────────────────────────────────────

// Advisory-lock namespaces. Advisory locks live in ONE global int-keyed space
// shared by the whole database, so every taker picks a namespace constant and
// hashes its subject into the second slot; two features that both locked on
// the bare hash of a user's uuid would block each other for no reason.
//
// Arbitrary but FIXED. Changing one silently disables mutual exclusion against
// any process still running the old value, which during a rolling deploy is
// precisely the window these exist to protect.
const (
	// "this user's Stripe Customer is being created right now"
	advisoryLockCustomer = 0x484f5241 // "HORA"
	// "this user's Connect account is being created right now" — a different
	// object with the same get-or-create hazard (payments_connect.go).
	advisoryLockConnectAccount = 0x484f5242
	// "this requester's balance_due rows are being settled right now".
	// Not a creation, but the same shape of hazard: read a set of rows, act on
	// each one against Stripe, write the result back. Two overlapping passes
	// read the same set (payments_settlement.go).
	advisoryLockBalanceSettle = 0x484f5243
)

// lockPerUser takes the per-user advisory lock for one of the namespaces
// above, inside the caller's transaction.
//
// Transaction-scoped on purpose: it is released by the commit or the rollback,
// including the rollback a panic or a context timeout triggers, so there is no
// unlock to forget and no lock to leak when a Stripe call hangs.
func lockPerUser(ctx context.Context, tx pgx.Tx, namespace int, uid string) error {
	if _, err := tx.Exec(ctx,
		`select pg_advisory_xact_lock($1, hashtext($2))`, namespace, uid); err != nil {
		return fmt.Errorf("payments: lock creation for user %s: %w", uid, err)
	}
	return nil
}

// stripeCustomerFor returns the user's Stripe Customer id, creating it on
// first use. Get-or-create, and safe to call concurrently: N simultaneous
// first-calls for one user produce ONE Customer and N identical answers.
//
// Lazily, and this matters: most beta accounts never add a card, and a
// Customer minted at signup is a permanent record in Stripe's database for
// someone who never transacts. The first call that needs one — opening the
// card sheet — is the first call that creates one.
//
// ── WHY THERE IS A LOCK AROUND A NETWORK CALL ──────────────────────────────
//
// This function used to rely on two things, and they are both still here and
// both still correct:
//
//	the partial unique index on users.stripe_customer_id, so two users can
//	never share a wallet (migration 20260914120000); and
//
//	the conditional UPDATE below, which coalesces rather than overwrites, so
//	a loser adopts the winner's id rather than clobbering it.
//
// What neither of them covered is the CREATE itself. Stripe's idempotency
// cache replays a COMPLETED request; it does not serialize an in-flight one.
// Two concurrent customer.New calls carrying the same key make the second
// fail with HTTP 409 `idempotency_key_in_use` — not a replay of the first
// object, an error — and this function turned that into a 500.
//
// That is not hypothetical. It was found on 2026-09-18 by a local UI sweep:
// the beta notice's payment gate and Post Task's own gate both read
// GET /payments/payment-methods on the same page load, for a user with no
// Customer yet, and one of the two answered 500.
//
// So the create is serialized per user with a transaction-scoped advisory
// lock. The loser BLOCKS rather than racing, and by the time it is let through
// the winner has committed, so it takes the fast path out of the re-read below
// and never calls Stripe at all.
//
// The cost is a pool connection held across a Stripe round trip — which is
// worth naming, because it is normally a thing to avoid. It is bounded here:
// this path runs at most once in a user's lifetime, every later call returns
// from the unlocked read at the top, and the locked section carries its own
// timeout so a hung Stripe call cannot hold the lock (or the connection) for
// longer than stripeRefCreateTimeout.
func stripeCustomerFor(ctx context.Context, uid, email string) (string, error) {
	if !paymentsEnabled() {
		return "", errPaymentsDisabled
	}
	if strings.TrimSpace(uid) == "" {
		return "", errors.New("payments: customer needs a user id")
	}

	// The fast path, and the one almost every call takes: no lock, no
	// transaction, one indexed read.
	existing, err := readStripeCustomer(ctx, db, uid)
	if err != nil {
		return "", err
	}
	if existing != "" {
		return existing, nil
	}

	return createStripeCustomer(ctx, uid, email)
}

// stripeRefCreateTimeout bounds the locked section — both of them, here and in
// connectAccountFor. Generous next to Stripe's own latency and short next to a
// request that is already waiting on it: the point is only that a hung
// connection releases the advisory lock (and the pool connection behind it)
// rather than parking every other caller for that user behind it forever.
const stripeRefCreateTimeout = 30 * time.Second

// readStripeCustomer reads the stored Customer id, or "" when there is none.
// Takes the querier so it can run on the pool or inside the transaction.
func readStripeCustomer(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, uid string) (string, error) {
	var stored *string
	if err := q.QueryRow(ctx,
		`select stripe_customer_id from public.users where id = $1::uuid`, uid,
	).Scan(&stored); err != nil {
		return "", fmt.Errorf("payments: read customer for user %s: %w", uid, err)
	}
	if stored == nil {
		return "", nil
	}
	return strings.TrimSpace(*stored), nil
}

// createStripeCustomer is the slow path: take the per-user lock, look again,
// and create only if nobody else already did.
func createStripeCustomer(ctx context.Context, uid, email string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, stripeRefCreateTimeout)
	defer cancel()

	tx, err := db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("payments: begin customer creation for user %s: %w", uid, err)
	}
	// Rollback on every path that is not an explicit Commit. After a
	// successful commit this is a no-op error we deliberately drop.
	defer func() { _ = tx.Rollback(context.Background()) }()

	// Blocks until whoever else is creating this user's Customer has committed.
	if err := lockPerUser(ctx, tx, advisoryLockCustomer, uid); err != nil {
		return "", err
	}

	// The second half of double-checked locking, and the branch that makes
	// this whole mechanism work: the winner committed while we were queued, so
	// there is already a Customer and we must not make another.
	existing, err := readStripeCustomer(ctx, tx, uid)
	if err != nil {
		return "", err
	}
	if existing != "" {
		log.Printf("[payments] customer for user=%s was created by a concurrent request — adopting %s", uid, existing)
		return existing, nil
	}

	params := &stripe.CustomerParams{
		Metadata: map[string]string{"user_id": uid},
	}
	if email != "" {
		params.Email = stripe.String(email)
	}
	// Kept even though the lock above already prevents the concurrent case it
	// was added for. It still covers the sequential one: a create whose
	// response we never saw (a timeout, a dropped connection, a process
	// killed mid-flight) is replayed rather than duplicated when the user
	// retries, because Stripe's cache answers a COMPLETED request with the
	// object it produced.
	params.SetIdempotencyKey("customer_" + uid)

	cus, err := customer.New(params)
	if err != nil {
		// Reachable despite the lock, in exactly one shape: an earlier attempt
		// whose HTTP request is still open at Stripe while this process no
		// longer holds anything (its connection died, its context was
		// cancelled mid-call). The key is in use by a request we are no longer
		// waiting on, so there is nothing to adopt and nothing to retry
		// usefully inside this request.
		if isIdempotencyKeyInUse(err) {
			log.Printf("[payments] customer create for user=%s hit an in-flight idempotency key; "+
				"an earlier attempt is still open at Stripe", uid)
			return "", fmt.Errorf("payments: customer creation already in flight for user %s: %w", uid, err)
		}
		return "", fmt.Errorf("payments: create customer for user %s: %w", uid, err)
	}

	var stored string
	err = tx.QueryRow(ctx, `
		update public.users
		   set stripe_customer_id = coalesce(stripe_customer_id, $2)
		 where id = $1::uuid
		returning stripe_customer_id
	`, uid, cus.ID).Scan(&stored)
	if err != nil {
		// The Customer exists at Stripe and we cannot record it. Delete it
		// rather than leaving it behind: nothing references it, nobody will
		// ever look for it, and the next call will make another.
		log.Printf("[payments][ERROR] created customer=%s for user=%s but could not store it: %v", cus.ID, uid, err)
		discardStripeCustomer(cus.ID, uid, "could not be stored")
		return "", fmt.Errorf("payments: persist customer %s for user %s: %w", cus.ID, uid, err)
	}

	// Defensive: under the lock this cannot happen, because nobody else can be
	// between the re-read and this UPDATE. Kept because it is the branch that
	// silently leaked Customers before, and if the lock is ever removed or
	// misconfigured this is the line that says so in the log.
	if stored != cus.ID {
		log.Printf("[payments][WARN] customer race for user=%s DESPITE the advisory lock — keeping %s, discarding %s",
			uid, stored, cus.ID)
		if err := tx.Commit(ctx); err != nil {
			return "", fmt.Errorf("payments: commit customer for user %s: %w", uid, err)
		}
		discardStripeCustomer(cus.ID, uid, "lost a race that should not have been possible")
		return stored, nil
	}

	if err := tx.Commit(ctx); err != nil {
		// Same reasoning as the failed UPDATE above: the row did not change,
		// so this Customer belongs to nobody.
		log.Printf("[payments][ERROR] created customer=%s for user=%s but could not commit: %v", cus.ID, uid, err)
		discardStripeCustomer(cus.ID, uid, "its transaction did not commit")
		return "", fmt.Errorf("payments: commit customer for user %s: %w", uid, err)
	}

	log.Printf("[payments] created customer=%s for user=%s", cus.ID, uid)
	return stored, nil
}

// discardStripeCustomer deletes a Customer this server created and then could
// not claim, so a failed create leaves nothing behind in Stripe.
//
// Safe by construction: it is only ever called with an id minted moments
// earlier inside this function and never written to any row, so it cannot have
// a card, a charge or a second reference. Best-effort and never fatal — the
// caller is already returning the outcome that matters, and a Customer that
// survives this is inert rather than harmful.
//
// Deliberately NOT given the request's context: it runs on paths where that
// context has just been cancelled or timed out, and inheriting it would make
// the cleanup fail exactly when it is needed.
func discardStripeCustomer(customerID, uid, why string) {
	if strings.TrimSpace(customerID) == "" {
		return
	}
	if _, err := customer.Del(customerID, nil); err != nil {
		log.Printf("[payments][WARN] could not delete orphan customer=%s (user=%s, %s): %v — "+
			"it holds nothing, but it is worth removing by hand", customerID, uid, why, err)
		return
	}
	log.Printf("[payments] deleted orphan customer=%s (user=%s, %s)", customerID, uid, why)
}

// isIdempotencyKeyInUse recognises Stripe's refusal to start a request whose
// key is already open on another in-flight request.
//
// Distinct from every other Stripe error in what it means: nothing was
// declined and nothing is wrong with the input — two calls simply overlapped.
// Named so the log says that rather than presenting an internal collision as a
// payment failure.
func isIdempotencyKeyInUse(err error) bool {
	var se *stripe.Error
	if !errors.As(err, &se) {
		return false
	}
	return se.Code == stripe.ErrorCodeIdempotencyKeyInUse ||
		strings.Contains(strings.ToLower(se.Msg), "idempotency key")
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
	//
	// THE REFUSAL NAMES THE TASKS. It used to say "a task that hasn't finished
	// yet" — correct, and useless: a requester with three live tasks was left
	// to guess which one, and the build 11 device run put it plainly: the
	// message "doesn't say why or what to do". The count is in the sentence
	// and the tasks are in the payload, so both clients can link straight to
	// the thing that needs finishing.
	blocking, err := liveHoldTasksForCard(ctx, uid, customerID, pmID)
	if err != nil {
		log.Printf("[payments][cards] live-hold check uid=%s pm=%s: %v", uid, pmID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if len(blocking) > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error":             "payment_method_in_use",
			"message":           cardInUseMessage(len(blocking)),
			"active_task_count": len(blocking),
			"tasks":             blocking,
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
	tasks, err := liveHoldTasksForCard(ctx, uid, customerID, pmID)
	return len(tasks) > 0, err
}

// blockingTask is one task whose live hold stops a card being removed — just
// enough for a client to say what it is and link to it.
type blockingTask struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// liveHoldTasksForCard is the list behind paymentMethodHasLiveHold: the tasks
// whose holds this card is carrying, newest first. Empty when the card is not
// the charging card at all (see the note above) or nothing is live.
func liveHoldTasksForCard(ctx context.Context, uid, customerID, pmID string) ([]blockingTask, error) {
	defaultID, err := defaultPaymentMethodFor(customerID)
	if err != nil {
		return nil, err
	}
	if pmID != defaultID {
		return nil, nil
	}
	return liveHoldTasks(ctx, uid)
}

// liveHoldTasks is the DB half, separable so it can be tested without Stripe:
// every task of this requester's with a hold still standing on it.
//
// One row per TASK, not per payment: a task can carry two payment rows (the
// hold and a completion balance), and "2 active tasks" for one task is the
// kind of wrong number that makes a requester stop trusting the count.
func liveHoldTasks(ctx context.Context, uid string) ([]blockingTask, error) {
	// DISTINCT ON forces its own leading ORDER BY (t.id), so the newest-first
	// order the caller is promised has to be applied OUTSIDE it — the first
	// cut ordered by task id, which is a UUID, which is random. The test that
	// asserts the order passed on one run in two.
	rows, err := db.Query(ctx, `
		select id, title, status, created_at from (
			select distinct on (t.id) t.id::text as id, coalesce(t.title, '') as title,
			       t.status, t.created_at
			  from public.payments p
			  join public.tasks t on t.id = p.task_id
			 where p.requester_id = $1::uuid
			   and p.status in ('requires_auth', 'authorized')
			 order by t.id
		) live
		order by created_at desc
	`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []blockingTask{}
	for rows.Next() {
		var b blockingTask
		var createdAt time.Time
		if err := rows.Scan(&b.ID, &b.Title, &b.Status, &createdAt); err != nil {
			return nil, err
		}
		if b.Title == "" {
			b.Title = "Untitled task"
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// cardInUseMessage is the one sentence the refusal leads with. The count is IN
// the sentence, because "a task" and "three tasks" are different amounts of
// work to get through before the card comes free, and the requester deciding
// whether to bother deserves to know which.
func cardInUseMessage(n int) string {
	if n == 1 {
		return "This card has a hold from 1 active task. Complete or cancel it to remove the card."
	}
	return fmt.Sprintf("This card has holds from %d active tasks. Complete or cancel them to remove the card.", n)
}
