package main

// Phase 3, the near half: the supporter's connected account.
//
// Phases 1–2b moved money from the requester onto the PLATFORM's Stripe
// balance and stopped there. This file is the account that money eventually
// leaves to, and everything around getting a supporter into it: creating the
// Express account, minting the onboarding link, reading back what Stripe will
// and will not let us do with it, and the gate that stops a supporter working
// a task we have no way to pay them for.
//
// The far half — the Transfer itself — is payments_payouts.go.
//
// WHY EXPRESS. Three account shapes exist and only one fits. Standard means
// the supporter brings their own full Stripe account, which is a business
// onboarding flow aimed at merchants and completely wrong for somebody who
// wants to walk a dog for forty minutes. Custom means we build and maintain
// the entire KYC collection UI ourselves, own identity-document upload, and
// keep it current with every jurisdiction's requirement changes — a compliance
// product, not a feature. Express is Stripe's hosted onboarding plus a hosted
// dashboard the supporter can see their own payouts in: they hand Stripe their
// SSN and bank details on Stripe's domain, we never touch either, and we get
// back one boolean that says whether they can be paid.
//
// WHAT THE PLATFORM CARRIES. Express means the platform is liable for negative
// balances on these accounts and pays the Stripe fees on them. That is the
// deliberate trade for not making supporters into merchants. It is also why
// there is a gate: a supporter who has not onboarded cannot be paid, and a
// task worked by an unpayable supporter is a debt with no settlement path.
//
// THE FLAG, AGAIN. Every gate in this file is downstream of paymentsEnforced()
// — the same PAYMENTS_ENFORCED that decides whether posting needs a card. With
// it off (the running beta) nothing here refuses anybody anything; onboarding
// is available to any approved supporter who wants it, and nobody is stopped
// from accepting. Flipping the flag turns on the card requirement for
// requesters and the payout requirement for supporters in the same motion,
// which is correct: those are the two halves of one decision.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/account"
	"github.com/stripe/stripe-go/v86/accountlink"
	"github.com/stripe/stripe-go/v86/loginlink"

	"hora-auth/internal/notify"
)

// connectAccountCountry is the country every Express account is created in.
//
// Hardcoded to the one market this beta operates in rather than read from the
// supporter's profile, because country is IMMUTABLE on a Stripe account: an
// account created in the wrong country cannot be corrected, only abandoned and
// recreated, and a profile `city` field is nowhere near reliable enough to
// decide something permanent. When the product opens a second market this
// becomes a choice the supporter makes explicitly, on screen, before the
// account is created.
const connectAccountCountry = "US"

// onboardingStates, as the clients render them. One word, decided here, so
// that two clients cannot disagree about what "in progress" means (S-05).
const (
	// No connected account at all. The supporter has never opened the flow.
	onboardingNotStarted = "not_started"
	// An account exists but Stripe still wants something — either the
	// supporter abandoned the form, or a requirement came due later.
	onboardingInProgress = "in_progress"
	// payouts_enabled. Money can move.
	onboardingComplete = "complete"
)

// ── The connected account ──────────────────────────────────────────────────

// connectAccountFor returns the supporter's Express account id, creating one
// on first use.
//
// Lazily, and later than stripeCustomerFor is: a Customer is minted when
// someone opens the card sheet, and this is minted when someone starts payout
// onboarding. Neither is created at signup, and this one is not created at
// supporter APPROVAL either — most approved supporters in the beta will never
// open the Earnings screen, and a Connect account is a heavier thing to leave
// lying around than a Customer. It carries KYC obligations, it appears in the
// platform's Connect dashboard, and it counts against the platform's account
// review.
//
// CONCURRENCY. Same get-or-create hazard, same fix, as stripeCustomerFor —
// see the long note there for why an idempotency key is not enough on its own.
// In short: Stripe's cache replays a COMPLETED request and does not serialize
// an in-flight one, so two overlapping creates with one key make the second
// fail with 409 `idempotency_key_in_use`. Two taps on "Set up payouts" are all
// it takes; the supporter got "We couldn't start payout setup just now".
//
// The lock makes the loser wait and then find the winner's account in the
// re-read below, so it never calls Stripe at all. Both halves of the older
// defence are still here and still correct: the partial unique index on
// users.stripe_account_id (migration 20260916120000) and the coalescing
// UPDATE, which means a late writer adopts rather than overwrites.
func connectAccountFor(ctx context.Context, uid, email string) (string, error) {
	if !paymentsEnabled() {
		return "", errPaymentsDisabled
	}
	if uid == "" {
		return "", errors.New("payments: connect account needs a user id")
	}

	// The fast path: no lock, no transaction, one indexed read. Every call
	// after the first takes it.
	existing, err := readConnectAccount(ctx, db, uid)
	if err != nil {
		return "", err
	}
	if existing != "" {
		return existing, nil
	}

	ctx, cancel := context.WithTimeout(ctx, stripeRefCreateTimeout)
	defer cancel()

	tx, err := db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("payments: begin connect account creation for user %s: %w", uid, err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if err := lockPerUser(ctx, tx, advisoryLockConnectAccount, uid); err != nil {
		return "", err
	}

	// Double-checked: the winner committed while we were queued.
	if existing, err := readConnectAccount(ctx, tx, uid); err != nil {
		return "", err
	} else if existing != "" {
		log.Printf("[payments][connect] account for user=%s was created by a concurrent request — adopting %s",
			uid, existing)
		return existing, nil
	}

	params := &stripe.AccountParams{
		// `type=express` rather than the controller-properties spelling.
		//
		// The SDK marks Type deprecated and the docs steer NEW platforms
		// toward controller properties or Accounts v2 — but the two are not
		// interchangeable here. An account created with controller properties
		// reports `type: "none"`, and the equivalent-to-Express controller set
		// differs from real Express in `controller.fees.payer`
		// (`application` vs `application_express`), which changes Stripe's fee
		// billing behaviour. `type=express` is still the documented way to
		// create an Express account, it is what login links and the Express
		// Dashboard are defined against, and it is what this integration was
		// asked for. Migrating to Accounts v2 is a redesign, not a parameter
		// change, and it would be the wrong thing to do silently inside a
		// payouts feature.
		Type:    stripe.String("express"),
		Country: stripe.String(connectAccountCountry),
		Capabilities: &stripe.AccountCapabilitiesParams{
			// TRANSFERS ONLY, and this is a deliberate minimum. Requesting
			// card_payments as well would make Stripe collect the full
			// merchant onboarding set — business details, statement
			// descriptors, the lot — from someone who is never going to charge
			// a card. They only ever RECEIVE money from the platform, which is
			// exactly what `transfers` covers, and asking for less means a
			// shorter form and fewer people who abandon it halfway.
			Transfers: &stripe.AccountCapabilitiesTransfersParams{
				Requested: stripe.Bool(true),
			},
		},
		// Tax information, identity documents and the bank account are all
		// collected by Stripe's hosted onboarding under its own defaults. We
		// deliberately prefill nothing beyond the email: once an Account Link
		// has been created for an Express account its KYC fields can no longer
		// be read or updated by the platform, so prefilling from a profile we
		// do not verify would bake unverified data into a compliance record.
		Metadata: map[string]string{"user_id": uid},
	}
	if email != "" {
		params.Email = stripe.String(email)
	}
	// Kept even though the lock above already covers the concurrent case this
	// was added for. It still covers the SEQUENTIAL one: a create whose
	// response we never saw (a timeout, a dropped connection, a process killed
	// mid-flight) is replayed rather than duplicated when the supporter tries
	// again, because Stripe's cache answers a COMPLETED request with the
	// object it produced.
	params.SetIdempotencyKey("connect_account_" + uid)

	acct, err := account.New(params)
	if err != nil {
		if isAccountsV1Disabled(err) {
			// Stripe refuses v1 account creation by default on platforms
			// onboarded after it started steering new integrations to
			// Accounts v2. The fix is one dashboard toggle, and it is a
			// SUPPORTED compatibility path rather than a workaround — but the
			// raw error is four sentences of migration advice that reads like
			// a dead end, and whoever hits this first will be a supporter
			// trying to get paid.
			log.Printf("[payments][connect][SETUP] Express account creation is blocked: this platform has " +
				"Accounts v1 support turned OFF. Enable it at " +
				"https://dashboard.stripe.com/settings/features/feat_accounts_v1_support " +
				"(Settings → Features → Accounts v1 support). Nothing else in this deploy needs to change.")
			return "", errConnectV1Disabled
		}
		if isIdempotencyKeyInUse(err) {
			// Reachable despite the lock in one shape only: an earlier attempt
			// whose HTTP request is still open at Stripe while this process no
			// longer holds anything. Nothing to adopt, nothing to usefully
			// retry inside this request.
			log.Printf("[payments][connect] account create for user=%s hit an in-flight idempotency key; "+
				"an earlier attempt is still open at Stripe", uid)
		}
		return "", fmt.Errorf("payments: create connect account for user %s: %w", uid, err)
	}

	var stored string
	err = tx.QueryRow(ctx, `
		update public.users
		   set stripe_account_id = coalesce(stripe_account_id, $2)
		 where id = $1::uuid
		returning coalesce(stripe_account_id, '')
	`, uid, acct.ID).Scan(&stored)
	if err != nil {
		// The account exists at Stripe and we cannot record it. Logged with
		// the id so it is recoverable by hand rather than merely lost; the
		// next call will create a second one, which is untidy but not unsafe
		// — an Express account with no transfers against it holds nothing.
		log.Printf("[payments][connect][ERROR] created account=%s for user=%s but could not store it: %v",
			acct.ID, uid, err)
		return "", fmt.Errorf("payments: store connect account: %w", err)
	}
	// Defensive: unreachable under the lock, because nobody else can be between
	// the re-read and this UPDATE. Kept because it is the branch that leaked
	// accounts before, and if the lock is ever removed this line says so.
	//
	// The orphan is NOT deleted, unlike an orphaned Customer. An empty Express
	// account holds nothing and is inert, but deleting a connected account is a
	// heavier and less reversible act than deleting an empty Customer — it can
	// carry KYC state Stripe keeps for its own compliance reasons — and doing
	// it automatically from an unreachable branch is the wrong trade.
	if stored != acct.ID {
		log.Printf("[payments][connect][WARN] user=%s raced DESPITE the advisory lock: keeping %s, abandoning %s",
			uid, stored, acct.ID)
		if err := tx.Commit(ctx); err != nil {
			return "", fmt.Errorf("payments: commit connect account for user %s: %w", uid, err)
		}
		return stored, nil
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("[payments][connect][ERROR] created account=%s for user=%s but could not commit: %v",
			acct.ID, uid, err)
		return "", fmt.Errorf("payments: commit connect account for user %s: %w", uid, err)
	}

	log.Printf("[payments][connect] user=%s account=%s created (country=%s)", uid, acct.ID, connectAccountCountry)
	cacheConnectStatus(ctx, uid, acct)
	return acct.ID, nil
}

// readConnectAccount reads the stored Express account id, or "" when there is
// none. Takes the querier so it can run on the pool or inside the transaction.
func readConnectAccount(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, uid string) (string, error) {
	var stored *string
	if err := q.QueryRow(ctx,
		`select stripe_account_id from public.users where id = $1::uuid`, uid,
	).Scan(&stored); err != nil {
		return "", fmt.Errorf("payments: read connect account for user %s: %w", uid, err)
	}
	if stored == nil {
		return "", nil
	}
	return strings.TrimSpace(*stored), nil
}

// errConnectV1Disabled is the platform-configuration failure, named so the
// handler can answer with something an operator can act on instead of relaying
// Stripe's migration essay to a supporter.
var errConnectV1Disabled = errors.New("connect: Accounts v1 support is disabled on this Stripe platform")

// isAccountsV1Disabled recognises Stripe's refusal to create a v1 connected
// account.
//
// Matched on the message rather than a code because Stripe returns a plain
// `invalid_request_error` with no distinguishing code — the whole signal is in
// the prose. Brittle, and deliberately so: the alternative is treating a
// one-click configuration problem as an unknown 502 forever.
func isAccountsV1Disabled(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Accounts v1") || strings.Contains(msg, "/v2/core/accounts")
}

// ── The status cache ───────────────────────────────────────────────────────

// ConnectStatus is what both clients branch on, and the only shape either of
// them sees. Nothing here names a bank account, a document, a date of birth or
// an SSN — all of that lives at Stripe and is reachable only through the
// Express dashboard, by the person it belongs to.
type ConnectStatus struct {
	// not_started / in_progress / complete.
	State string `json:"state"`
	// True once payouts_enabled. The one field the accept gate reads.
	PayoutsEnabled bool `json:"payouts_enabled"`
	// True once the supporter finished the hosted form at least once. A
	// supporter can be details_submitted and still not payouts_enabled while
	// Stripe verifies — which is a real state the Earnings screen has to be
	// able to say something honest about.
	DetailsSubmitted bool `json:"details_submitted"`
	// Stripe's currently_due field names. Sent so the screen can say how much
	// is left rather than only that something is; clients render the COUNT,
	// not the raw names, which are Stripe-internal spellings.
	RequirementsDue []string `json:"requirements_due"`
	// Whether payouts are gated right now (PAYMENTS_ENFORCED). Reported for
	// the same reason payments_enforced is on the card list: so the clients
	// can put the prompt in front of a supporter BEFORE they pick a task, and
	// so beta users are shown nothing while the flag is off.
	PayoutsEnforced bool `json:"payouts_enforced"`
}

// stateFor collapses the two booleans into the one word the clients use.
func stateFor(payoutsEnabled, detailsSubmitted bool, hasAccount bool) string {
	switch {
	case payoutsEnabled:
		return onboardingComplete
	case hasAccount || detailsSubmitted:
		return onboardingInProgress
	default:
		return onboardingNotStarted
	}
}

// cacheConnectStatus writes what Stripe just told us about an account.
//
// Best-effort and never fatal: the caller has a live Account object in hand
// and is about to answer from it, so a failed cache write costs a stale gate
// on the NEXT request, not a wrong answer on this one.
func cacheConnectStatus(ctx context.Context, uid string, acct *stripe.Account) {
	if acct == nil || uid == "" {
		return
	}
	due := requirementsDue(acct)
	raw, err := json.Marshal(due)
	if err != nil {
		raw = []byte("[]")
	}
	if _, err := db.Exec(ctx, `
		update public.users
		   set stripe_payouts_enabled    = $2,
		       stripe_details_submitted  = $3,
		       stripe_requirements_due   = $4::jsonb,
		       stripe_account_updated_at = now()
		 where id = $1::uuid
	`, uid, acct.PayoutsEnabled, acct.DetailsSubmitted, string(raw)); err != nil {
		log.Printf("[payments][connect] could not cache status for user=%s account=%s: %v", uid, acct.ID, err)
	}
}

// requirementsDue is what Stripe still wants, newest state first.
//
// currently_due AND past_due, merged. past_due is the subset that has blown
// its deadline, and a supporter whose account has gone past due sees nothing
// at all if only currently_due is read — which is precisely the moment they
// most need telling.
func requirementsDue(acct *stripe.Account) []string {
	if acct == nil || acct.Requirements == nil {
		return []string{}
	}
	seen := map[string]bool{}
	out := []string{}
	for _, list := range [][]string{acct.Requirements.CurrentlyDue, acct.Requirements.PastDue} {
		for _, f := range list {
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// readConnectStatus answers from Stripe when there is an account, and from
// nothing when there is not.
//
// It deliberately makes an API call rather than reading the cache: this is the
// function behind the Earnings screen and the return from onboarding, and both
// are moments where being RIGHT matters more than being fast. The cache exists
// for the accept gate, which runs on every accept and must not add a network
// round trip to it — see supporterPayoutsReady.
func readConnectStatus(ctx context.Context, uid string) (ConnectStatus, error) {
	st := ConnectStatus{
		State:           onboardingNotStarted,
		RequirementsDue: []string{},
		PayoutsEnforced: paymentsEnforced(),
	}

	var accountID *string
	if err := db.QueryRow(ctx,
		`select stripe_account_id from public.users where id = $1::uuid`, uid,
	).Scan(&accountID); err != nil {
		return st, fmt.Errorf("read connect account: %w", err)
	}
	if accountID == nil || *accountID == "" {
		return st, nil
	}

	acct, err := account.GetByID(*accountID, &stripe.AccountParams{})
	if err != nil {
		// Stripe is unreachable or the account is gone. Fall back to the
		// cache, which is the whole reason it exists — an Earnings screen that
		// errors out because Stripe is having a slow minute is worse than one
		// showing a status from four minutes ago.
		log.Printf("[payments][connect] user=%s account=%s unreadable (%v) — answering from cache", uid, *accountID, err)
		return cachedConnectStatus(ctx, uid)
	}

	cacheConnectStatus(ctx, uid, acct)
	st.PayoutsEnabled = acct.PayoutsEnabled
	st.DetailsSubmitted = acct.DetailsSubmitted
	st.RequirementsDue = requirementsDue(acct)
	st.State = stateFor(st.PayoutsEnabled, st.DetailsSubmitted, true)
	return st, nil
}

// cachedConnectStatus answers from the users columns alone, with no network
// call. The accept gate's source, and readConnectStatus's fallback.
func cachedConnectStatus(ctx context.Context, uid string) (ConnectStatus, error) {
	st := ConnectStatus{
		State:           onboardingNotStarted,
		RequirementsDue: []string{},
		PayoutsEnforced: paymentsEnforced(),
	}
	var accountID *string
	var rawDue []byte
	err := db.QueryRow(ctx, `
		select stripe_account_id, stripe_payouts_enabled, stripe_details_submitted,
		       coalesce(stripe_requirements_due, '[]'::jsonb)
		  from public.users where id = $1::uuid
	`, uid).Scan(&accountID, &st.PayoutsEnabled, &st.DetailsSubmitted, &rawDue)
	if err != nil {
		return st, fmt.Errorf("read cached connect status: %w", err)
	}
	if len(rawDue) > 0 {
		_ = json.Unmarshal(rawDue, &st.RequirementsDue)
	}
	if st.RequirementsDue == nil {
		st.RequirementsDue = []string{}
	}
	st.State = stateFor(st.PayoutsEnabled, st.DetailsSubmitted, accountID != nil && *accountID != "")
	return st, nil
}

// ── The accept gate ────────────────────────────────────────────────────────

// errPayoutsOnboardingRequired is the one refusal this file produces.
var errPayoutsOnboardingRequired = errors.New("payouts_onboarding_required")

// supporterPayoutsReady reports whether this supporter may accept a task.
//
// TRUE WHENEVER THE FLAG IS OFF, unconditionally and before any read. That is
// the running beta, and it must behave exactly as it did before this file
// existed — nobody is blocked, nobody is asked to onboard, and the whole
// payouts surface is optional.
//
// With the flag on it reads the CACHE rather than calling Stripe. Accept is a
// hot, latency-sensitive path where a supporter is racing other supporters for
// a task, and a Stripe round trip inside the accept would be both slow and a
// new way for accepting to fail. The cache is refreshed on every Earnings
// view, on every onboarding return, and by the account.updated webhook — which
// is the event that fires the moment payouts_enabled changes. The worst stale
// read is a supporter who finished onboarding seconds ago being asked to
// finish onboarding, which one refresh of the Earnings screen fixes.
//
// FAILING CLOSED. A database error refuses the accept. The alternative is
// letting somebody work a task the platform may have no way to pay them for,
// and between "try again in a moment" and "we cannot pay you", the first is
// the kinder failure.
func supporterPayoutsReady(ctx context.Context, uid string) (bool, error) {
	if !paymentsEnforced() {
		return true, nil
	}
	var enabled bool
	if err := db.QueryRow(ctx,
		`select stripe_payouts_enabled from public.users where id = $1::uuid`, uid,
	).Scan(&enabled); err != nil {
		return false, fmt.Errorf("read payout readiness for %s: %w", uid, err)
	}
	return enabled, nil
}

// ── Routes ─────────────────────────────────────────────────────────────────

// RegisterConnectRoutes mounts the supporter payout surface.
//
// Under /payments like the card routes, session-authenticated, and gated by
// requirePaymentsEnabled so an unconfigured Stripe answers 503 before any
// handler runs. Not gated on being an approved supporter: a supporter whose
// application is still pending can look at the Earnings screen and see
// "not started", which is honest and harmless. What they cannot do is accept a
// task, and that gate lives on accept where it belongs.
func RegisterConnectRoutes(r *gin.Engine, authMiddleware gin.HandlerFunc) {
	p := r.Group("/payments/connect")
	p.Use(authMiddleware, requirePaymentsEnabled())
	{
		p.POST("/onboarding-link", connectOnboardingLinkHandler)
		p.GET("/status", connectStatusHandler)
		// The Express dashboard, where a supporter sees their own balance,
		// payout schedule and bank account. A one-time login URL, minted per
		// click and never stored.
		p.POST("/login-link", connectLoginLinkHandler)
	}
	r.GET("/payments/earnings", authMiddleware, requirePaymentsEnabled(), earningsHandler)
}

// connectReturnURL and connectRefreshURL are where Stripe sends the supporter
// back to.
//
// BOTH POINT AT THE WEB APP, including for the mobile flow, and that is not an
// oversight. An Account Link is an ordinary https URL opened in a browser (an
// in-app browser on mobile), and Stripe will only redirect to http/https — a
// `hora://` scheme is rejected outright in live mode. The web route closes the
// loop for both clients: on web it is the page the supporter lands back on,
// and on mobile the in-app browser is dismissed by the app when it sees this
// URL, without the page ever needing to render.
//
// NO STATE IS PASSED THROUGH EITHER URL, by Stripe's design, and nothing here
// tries to smuggle any: arriving at the return URL means only that the flow
// was entered and exited, never that it was completed. What the supporter is
// actually shown is decided by re-reading the account — which is what the
// screen does on mount.
func connectReturnURL() string {
	return appBase() + "/profile/earnings?onboarding=return"
}

func connectRefreshURL() string {
	return appBase() + "/profile/earnings?onboarding=refresh"
}

// appBase mirrors internal/notify's link builder: one env var, one default, so
// a deploy pointing at a different host moves every link at once.
func appBase() string {
	base := strings.TrimSpace(os.Getenv("APP_BASE_URL"))
	if base == "" {
		base = "https://horaapp.co"
	}
	return strings.TrimRight(base, "/")
}

// POST /payments/connect/onboarding-link
//
// Creates the account if this is the first time, then mints a single-use
// Account Link and hands back its URL. Both clients open it in a browser —
// the web app by navigation, mobile in an in-app browser.
//
// The URL is single-use and short-lived BY DESIGN: it grants access to the
// account holder's own personal information, so it is minted per click,
// returned to an authenticated session, and never stored, logged or emailed.
func connectOnboardingLinkHandler(c *gin.Context) {
	uid := c.GetString("uid")
	email := strings.TrimSpace(c.GetString("email"))
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	accountID, err := connectAccountFor(ctx, uid, email)
	if errors.Is(err, errConnectV1Disabled) {
		// A platform misconfiguration, not a supporter problem. Distinct code
		// so the clients can say "payouts aren't switched on yet" rather than
		// "try again in a moment" — which would be advice to repeat something
		// that cannot work until somebody changes a Stripe setting.
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "payouts_not_configured",
			"message": "Payouts aren't switched on yet. We're on it — please check back shortly.",
		})
		return
	}
	if err != nil {
		log.Printf("[payments][connect] onboarding for uid=%s: %v", uid, err)
		c.JSON(http.StatusBadGateway, gin.H{
			"error":   "payouts_error",
			"message": "We couldn't start payout setup just now. Please try again in a moment.",
		})
		return
	}

	url, err := accountLinkFor(accountID)
	if err != nil {
		log.Printf("[payments][connect] account link for uid=%s account=%s: %v", uid, accountID, err)
		c.JSON(http.StatusBadGateway, gin.H{
			"error":   "payouts_error",
			"message": "We couldn't start payout setup just now. Please try again in a moment.",
		})
		return
	}

	// The account id is logged; the URL is not. See above — it is a bearer
	// credential for somebody's identity documents (S-12).
	log.Printf("[payments][connect] uid=%s account=%s onboarding link issued", uid, accountID)
	c.JSON(http.StatusOK, gin.H{"url": url})
}

// accountLinkFor mints a single-use onboarding URL for a connected account.
//
// Split out of the handler so the smoke test can exercise the Stripe call
// without going through an HTTP request, and so the two redirect URLs are
// named in exactly one place — they have to be identical across every link
// minted for an account or the refresh loop sends the supporter somewhere
// unexpected mid-onboarding.
func accountLinkFor(accountID string) (string, error) {
	link, err := accountlink.New(&stripe.AccountLinkParams{
		Account: stripe.String(accountID),
		Type:    stripe.String("account_onboarding"),
		// Stripe sends the supporter here when the link is stale — expired,
		// already visited, or used after a back button. The page's job is to
		// ask for a fresh link and send them straight back in, which is what
		// keeps an interrupted onboarding from becoming a dead end.
		RefreshURL: stripe.String(connectRefreshURL()),
		ReturnURL:  stripe.String(connectReturnURL()),
	})
	if err != nil {
		return "", err
	}
	return link.URL, nil
}

// GET /payments/connect/status
func connectStatusHandler(c *gin.Context) {
	uid := c.GetString("uid")
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	st, err := readConnectStatus(c.Request.Context(), uid)
	if err != nil {
		log.Printf("[payments][connect] status for uid=%s: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "payouts_error"})
		return
	}
	c.JSON(http.StatusOK, st)
}

// POST /payments/connect/login-link
//
// A one-time URL into the Express dashboard. Refused for an account that is
// not onboarded, because Stripe refuses it too and its error is not something
// to show a person.
func connectLoginLinkHandler(c *gin.Context) {
	uid := c.GetString("uid")
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	var accountID *string
	if err := db.QueryRow(ctx,
		`select stripe_account_id from public.users where id = $1::uuid`, uid,
	).Scan(&accountID); err != nil || accountID == nil || *accountID == "" {
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "no_connect_account",
			"message": "Set up payouts first.",
		})
		return
	}

	link, err := loginlink.New(&stripe.LoginLinkParams{Account: accountID})
	if err != nil {
		log.Printf("[payments][connect] login link for uid=%s account=%s: %v", uid, *accountID, err)
		c.JSON(http.StatusBadGateway, gin.H{
			"error":   "payouts_error",
			"message": "We couldn't open your payouts dashboard just now.",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": link.URL})
}

// ── Earnings ───────────────────────────────────────────────────────────────

// EarningsTransfer is one payout, as its own recipient sees it.
//
// SUPPORTER-FACING AND SUPPORTER-SCOPED. Every field is about money arriving;
// none of it is about where the money came from. There is no requester, no
// card, no hold, no capture — a supporter has no business knowing what the
// requester was charged or on which card, any more than the requester has
// business knowing the supporter's bank. The two views of one task's money are
// deliberately disjoint (see TaskPayment's note, and the leak tests).
type EarningsTransfer struct {
	TaskID    string `json:"task_id"`
	TaskTitle string `json:"task_title"`
	// What landed, net. Split so the copy can say "$19.50 (time) + $12.40
	// (reimbursement)" without the client adding anything up (S-05).
	AmountCents  int       `json:"amount_cents"`
	TimeCents    int       `json:"time_cents"`
	ReceiptCents int       `json:"receipt_cents"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

// Earnings is the whole Earnings section in one response: the onboarding state
// machine, the lifetime figure, and the recent transfers.
//
// One call rather than three because the screen renders as a unit and cannot
// usefully show any one of them without the others — an earnings total above a
// "set up payouts" prompt is incoherent.
type Earnings struct {
	Onboarding ConnectStatus `json:"onboarding"`
	// Lifetime PAID, not lifetime earned-on-paper: pending and failed
	// transfers are excluded. A number a supporter can reconcile against their
	// own bank statement is worth more than one that includes money still in
	// flight.
	LifetimeEarnedCents int                `json:"lifetime_earned_cents"`
	Transfers           []EarningsTransfer `json:"transfers"`
}

// earningsRecentLimit caps the list. The Express dashboard is the complete
// record — "Manage payouts" goes there — so this is a recent-activity strip,
// not a ledger, and paginating it would be building a worse copy of something
// Stripe already hosts.
const earningsRecentLimit = 20

// GET /payments/earnings
func earningsHandler(c *gin.Context) {
	uid := c.GetString("uid")
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	st, err := readConnectStatus(ctx, uid)
	if err != nil {
		log.Printf("[payments][earnings] status for uid=%s: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "payouts_error"})
		return
	}

	out := Earnings{Onboarding: st, Transfers: []EarningsTransfer{}}
	out.LifetimeEarnedCents = lifetimePaidCents(ctx, uid)

	rows, err := db.Query(ctx, `
		select po.task_id::text, coalesce(t.title,''), po.amount_cents, po.status, po.created_at,
		       coalesce(p.time_cost_cents, 0), coalesce(p.shopping_receipt_cents, 0)
		  from public.payouts po
		  join public.tasks t    on t.id = po.task_id
		  join public.payments p on p.id = po.payment_id
		 where po.supporter_id = $1::uuid
		 order by po.created_at desc
		 limit $2
	`, uid, earningsRecentLimit)
	if err != nil {
		log.Printf("[payments][earnings] list for uid=%s: %v", uid, err)
		c.JSON(http.StatusOK, out) // the status half is still worth rendering
		return
	}
	defer rows.Close()

	for rows.Next() {
		var e EarningsTransfer
		if err := rows.Scan(&e.TaskID, &e.TaskTitle, &e.AmountCents, &e.Status, &e.CreatedAt,
			&e.TimeCents, &e.ReceiptCents); err != nil {
			break
		}
		out.Transfers = append(out.Transfers, e)
	}
	c.JSON(http.StatusOK, out)
}

// lifetimePaidCents sums what has actually reached this supporter.
func lifetimePaidCents(ctx context.Context, uid string) int {
	var total int
	if err := db.QueryRow(ctx, `
		select coalesce(sum(amount_cents), 0) from public.payouts
		 where supporter_id = $1::uuid and status = $2
	`, uid, payoutStatusPaid).Scan(&total); err != nil {
		log.Printf("[payments][earnings] lifetime for uid=%s: %v", uid, err)
		return 0
	}
	return total
}

// ── account.updated ────────────────────────────────────────────────────────

// onAccountUpdated keeps the cache honest and tells a supporter when their
// ability to be paid goes away.
//
// This event is the reason the accept gate can read a column instead of
// calling Stripe. It fires whenever requirements or capabilities change —
// when onboarding completes, when a document expires, when a bank account is
// closed, when a verification deadline passes.
//
// SCOPE. account.updated for a CONNECTED account is a connected-account-scoped
// event, which means it arrives at a webhook endpoint configured with
// "Events from: Connected accounts" — a different endpoint, with a different
// signing secret, from the one payment_intent.* arrives at. See
// handleStripeWebhook's dual-secret verification and the deploy notes.
func onAccountUpdated(ctx context.Context, event *stripe.Event) error {
	var acct stripe.Account
	if err := json.Unmarshal(event.Data.Raw, &acct); err != nil {
		return fmt.Errorf("decode account: %w", err)
	}
	if acct.ID == "" {
		return fmt.Errorf("event %s carries no account id", event.ID)
	}

	var uid, email string
	var wasEnabled bool
	err := db.QueryRow(ctx, `
		select id::text, coalesce(email,''), stripe_payouts_enabled
		  from public.users where stripe_account_id = $1
	`, acct.ID).Scan(&uid, &email, &wasEnabled)
	if err != nil {
		// An account we have no user for. Almost always a test event fired
		// from the dashboard, or an account created outside this backend.
		// Acknowledged, not retried.
		log.Printf("[stripe][webhook] no user for connect account=%s — ignoring", acct.ID)
		return nil
	}

	cacheConnectStatus(ctx, uid, &acct)
	log.Printf("[stripe][webhook] connect account=%s user=%s payouts_enabled=%v details_submitted=%v due=%d",
		acct.ID, uid, acct.PayoutsEnabled, acct.DetailsSubmitted, len(requirementsDue(&acct)))

	// The transition that costs somebody money. Going from payable to not
	// payable means their next accept will be refused and any pending transfer
	// will fail, and they will have no idea why unless they are told.
	// The reverse transition needs no announcement — they just finished
	// onboarding and are looking at the screen that says so.
	if wasEnabled && !acct.PayoutsEnabled {
		log.Printf("[stripe][webhook][PAYOUTS DISABLED] user=%s account=%s reason=%s",
			uid, acct.ID, disabledReason(&acct))
		writeAudit(ctx, systemActorUID, systemActorUID, "PAYOUTS_DISABLED", disabledReason(&acct),
			map[string]any{
				"user_id":           uid,
				"stripe_account_id": acct.ID,
				"requirements_due":  requirementsDue(&acct),
			})
		notifySupporterPayoutsDisabled(email, requirementsDue(&acct))
	}
	return nil
}

// disabledReason is Stripe's own word for why an account is held, or empty.
func disabledReason(acct *stripe.Account) string {
	if acct == nil || acct.Requirements == nil {
		return ""
	}
	return string(acct.Requirements.DisabledReason)
}

// notifySupporterPayoutsDisabled emails the supporter.
//
// EMAIL RATHER THAN A NOTIFICATION ROW, and the reason is structural rather
// than stylistic: notifications.task_id is NOT NULL and every value in the
// notification_type enum is a task lifecycle event. "Your payouts are on hold"
// belongs to an account, not to a task, and filing it against an invented task
// id to fit the schema would put a lie in the one table whose job is saying
// what happened to which task. Same call, same reasoning, as the dispute and
// balance-due ops alerts.
//
// Fired on a background context in a goroutine (S-32): this runs inside a
// Stripe webhook, and a webhook held open by a slow mail provider is a webhook
// Stripe retries.
func notifySupporterPayoutsDisabled(email string, due []string) {
	if strings.TrimSpace(email) == "" {
		return
	}
	what := "Stripe needs more information before you can be paid."
	if len(due) > 0 {
		what = fmt.Sprintf("Stripe needs %d more piece(s) of information before you can be paid.", len(due))
	}
	body := fmt.Sprintf(
		`<p><strong>Your payouts are on hold.</strong></p>
<p>%s Until it's sorted you won't be able to accept new tasks, and any payment we send won't reach your bank.</p>
<p>Open HO:RA &rarr; Profile &rarr; Earnings and tap <strong>Continue setup</strong> to finish.</p>
<p>Anything you've already earned is safe.</p>`, what)

	go func() {
		if err := notify.SendEmail(notify.EmailPayload{
			To:      email,
			Subject: "[HO:RA] Your payouts are on hold",
			Html:    body,
		}); err != nil {
			log.Printf("[payments][connect] payouts-disabled email failed to=%s: %v", email, err)
		}
	}()
}
