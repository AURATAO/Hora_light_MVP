package main

// App Review bypass — one env-configured account that signs in with a fixed
// code instead of a Supabase-issued OTP, so Apple's reviewers can get into a
// TestFlight / App Store build without access to an inbox.
//
// Deliberately narrow:
//   - Exactly one email, compared server-side. The client never learns which
//     address it is; it just retries a failed OTP here and gets 401 for
//     anything else. Every other account's OTP is still verified by Supabase,
//     on a code path this file does not touch.
//   - A successful review login gets an ordinary hora_session from
//     issueHoraSession — the same cookie, claims and TTL /auth/exchange hands
//     out. No admin flag, no extra scope, no longer-lived token.
//   - Leave REVIEW_ACCOUNT_EMAIL or REVIEW_ACCOUNT_CODE unset and the route is
//     never registered, so the bypass simply does not exist outside a review
//     window. That is how it gets switched off after approval.
//   - Wrong codes aimed at the review address are counted and throttled, so a
//     fixed six-digit credential on a public route isn't a brute-force target.
//   - Every accepted login is logged, so we can see when Apple used it.

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Guessing budget for the review credential. Six digits is a small space and
// this service has no rate-limiting middleware in front of it, so uncapped the
// route is brute-forceable by anyone who guesses the address. Ten tries per
// window leaves a reviewer plenty of room while putting a brute-force attempt
// well out of reach; a successful login clears the count.
const (
	reviewMaxFailures   = 10
	reviewFailureWindow = 15 * time.Minute
)

// reviewThrottle is a fixed-window failure counter for the review credential.
// Per-process and global rather than per-IP: there is exactly one review
// account, and a per-IP bucket would be trivially sidestepped.
type reviewThrottle struct {
	mu          sync.Mutex
	failures    int
	windowStart time.Time
}

// rollLocked starts a fresh window once the current one has elapsed. The zero
// windowStart makes the first call roll over, which is the intended start.
func (t *reviewThrottle) rollLocked(now time.Time) {
	if now.Sub(t.windowStart) >= reviewFailureWindow {
		t.windowStart = now
		t.failures = 0
	}
}

func (t *reviewThrottle) allow(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rollLocked(now)
	return t.failures < reviewMaxFailures
}

func (t *reviewThrottle) recordFailure(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rollLocked(now)
	t.failures++
}

// reset clears the budget after a successful login, so a reviewer who
// fat-fingers the code a few times isn't later locked out by their own misses.
func (t *reviewThrottle) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failures = 0
}

// Profile fields the review account is seeded with. Not secrets — env vars
// exist only so the values can be corrected without a redeploy of code.
// REVIEW_ACCOUNT_AVATAR_URL is optional: mobile's profile gate also requires an
// avatar (mobile/src/lib/onboarding.ts), so leaving it empty means the reviewer
// picks a photo once before reaching the tabs.
// +1 212 555 01xx is the reserved fictional range, so the seeded number can't
// collide with a real one.
const (
	defaultReviewName  = "Hora Review"
	defaultReviewPhone = "+1 212 555 0100"
	defaultReviewCity  = "New York"
)

type reviewAccount struct {
	Email     string
	Code      string
	Name      string
	Phone     string
	City      string
	AvatarURL string
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// loadReviewAccount reads the bypass config. ok is false when either half of
// the credential is missing, which is the disabled state.
func loadReviewAccount() (reviewAccount, bool) {
	ra := reviewAccount{
		Email:     normalizeReviewEmail(os.Getenv("REVIEW_ACCOUNT_EMAIL")),
		Code:      strings.TrimSpace(os.Getenv("REVIEW_ACCOUNT_CODE")),
		Name:      envOr("REVIEW_ACCOUNT_NAME", defaultReviewName),
		Phone:     envOr("REVIEW_ACCOUNT_PHONE", defaultReviewPhone),
		City:      envOr("REVIEW_ACCOUNT_CITY", defaultReviewCity),
		AvatarURL: strings.TrimSpace(os.Getenv("REVIEW_ACCOUNT_AVATAR_URL")),
	}
	return ra, ra.Email != "" && ra.Code != ""
}

func normalizeReviewEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// isEmail reports whether the submitted address is the review account. Used on
// its own to decide whether a rejection is worth logging.
func (ra reviewAccount) isEmail(submitted string) bool {
	return ra.Email != "" && normalizeReviewEmail(submitted) == ra.Email
}

// matches is the full credential check. The code is compared in constant time:
// this endpoint is reachable by anyone who can guess the email, so a timing
// oracle on a 6-digit code would meaningfully shrink the search space.
func (ra reviewAccount) matches(submittedEmail, submittedCode string) bool {
	if ra.Email == "" || ra.Code == "" {
		return false
	}
	codeOK := subtle.ConstantTimeCompare([]byte(ra.Code), []byte(strings.TrimSpace(submittedCode))) == 1
	return ra.isEmail(submittedEmail) && codeOK
}

// registerReviewAccountRoute wires POST /auth/review-login, or logs that the
// bypass is off and returns.
func registerReviewAccountRoute(r *gin.Engine, sdb *sql.DB) {
	ra, ok := loadReviewAccount()
	if !ok {
		log.Println("[review-login] disabled (REVIEW_ACCOUNT_EMAIL / REVIEW_ACCOUNT_CODE not both set)")
		return
	}
	log.Printf("[review-login] ENABLED for %s — remove the env vars once App Review is done", ra.Email)
	throttle := &reviewThrottle{}

	r.POST("/auth/review-login", func(c *gin.Context) {
		var body struct {
			Email string `json:"email"`
			Code  string `json:"code"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing email or code"})
			return
		}

		// The mobile client retries every OTP Supabase rejects against this
		// route, so most traffic here is ordinary users mistyping their own
		// code. Those are not review-account attempts: they get a flat 401
		// without spending the guessing budget or reaching the log.
		aimedAtReviewAccount := ra.isEmail(body.Email)

		if aimedAtReviewAccount && !throttle.allow(time.Now()) {
			log.Printf("[review-login] THROTTLED ip=%s ua=%q", c.ClientIP(), c.Request.UserAgent())
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many attempts"})
			return
		}

		if !ra.matches(body.Email, body.Code) {
			if aimedAtReviewAccount {
				throttle.recordFailure(time.Now())
				log.Printf("[review-login] REJECTED wrong code ip=%s ua=%q", c.ClientIP(), c.Request.UserAgent())
			}
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid code"})
			return
		}
		throttle.reset()

		internalID, err := ensureReviewAccount(c.Request.Context(), sdb, ra)
		if err != nil {
			log.Printf("[review-login] seed failed email=%s err=%v", ra.Email, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "user upsert failed"})
			return
		}

		resp, err := issueHoraSession(c, internalID, ra.Email)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "session error"})
			return
		}

		log.Printf("[review-login] OK email=%s uid=%s ip=%s ua=%q", ra.Email, internalID, c.ClientIP(), c.Request.UserAgent())
		c.JSON(http.StatusOK, resp)
	})
}

// ensureReviewAccount returns the internal users.id for the review email,
// creating the row and its seeded profile if needed.
//
// supabase_sub is left NULL: no Supabase user is required for this login. If
// one is ever created for the same address, /auth/exchange's email-linking
// branch attaches the sub to this same row rather than forking a second one.
func ensureReviewAccount(ctx context.Context, sdb *sql.DB, ra reviewAccount) (string, error) {
	var internalID string
	err := sdb.QueryRowContext(ctx,
		`select id from public.users where email = $1`, ra.Email,
	).Scan(&internalID)
	if errors.Is(err, sql.ErrNoRows) {
		err = sdb.QueryRowContext(ctx, `
			insert into public.users (email, name)
			values ($1, $2)
			returning id
		`, ra.Email, ra.Name).Scan(&internalID)
	}
	if err != nil {
		return "", err
	}
	if internalID == "" {
		return "", errors.New("empty internal id for review account")
	}
	return internalID, seedReviewProfile(ctx, sdb, internalID, ra)
}

// seedReviewProfile puts the account in the state a reviewer needs on arrival:
// past the beta gate, profile filled in, and already an approved supporter — so
// both the requester flow and the supporter Work tab are reachable without the
// manual approval we can't perform inside a review window.
//
// beta_accepted and is_verified_supporter are re-forced on every login; they
// are the entire point of the seed and must survive a reviewer toggling
// something. The display fields are only filled when empty, so a reviewer who
// edits the profile doesn't watch it revert on their next sign-in.
func seedReviewProfile(ctx context.Context, sdb *sql.DB, internalID string, ra reviewAccount) error {
	_, err := sdb.ExecContext(ctx, `
		insert into public.profiles
			(id, email, name, phone, city, avatar_url, bio, beta_accepted, is_verified_supporter, created_at, updated_at)
		values
			($1::uuid, $2, $3, $4, $5, $6, '', true, true, now(), now())
		on conflict (email) do update set
			beta_accepted         = true,
			is_verified_supporter = true,
			name       = case when profiles.name       = '' then excluded.name       else profiles.name       end,
			phone      = case when profiles.phone      = '' then excluded.phone      else profiles.phone      end,
			city       = case when profiles.city       = '' then excluded.city       else profiles.city       end,
			avatar_url = case when profiles.avatar_url = '' then excluded.avatar_url else profiles.avatar_url end,
			updated_at = now()
	`, internalID, ra.Email, ra.Name, ra.Phone, ra.City, ra.AvatarURL)
	return err
}
