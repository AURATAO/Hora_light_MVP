package main

// THE PRICE SCHEDULE, AS DATA (Aura 2026-11-10).
//
// Until now the five prices lived as literals in BillingConfig, so changing
// one meant a code change and a deploy. They now live in
// public.pricing_schedules — append-only, the newest row is the schedule —
// and an admin changes them from the Ops page (POST /admin/pricing). The
// server re-reads the table every pricingRefresh and right after a change.
//
// WHAT IS LIVE AND WHAT IS LOCKED. A price is a term of an agreement, so it is
// fixed when the agreement is made (the same rule resolveRateCentsPerMin
// already followed for the per-minute rate):
//
//   - at post, the task stores the three numbers it will be billed by:
//     rate_cents_per_min (already), base_fee_cents and platform_fee_bps;
//   - every later read for that task — the hold, the settlement, a cancel,
//     the supporter's cut, the payout rows — uses the stored numbers;
//   - the live schedule prices only what has no task yet: estimates, quotes,
//     the next post.
//
// So changing a price never re-prices a task in flight, never makes a
// settlement disagree with its hold, and never relabels a payout with a fee
// it was not cut at.
//
// WHEN THE TABLE CANNOT BE READ the server keeps the last schedule it read,
// and before the first read it uses the compiled defaults (Billing). It never
// falls to zero: a price of $0 is a bug, not a fallback.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// PriceSchedule is the part of BillingConfig that is data. Everything else in
// BillingConfig (inclusion, grace windows, timeouts) is product policy and
// stays in code.
type PriceSchedule struct {
	ID                        string    `json:"id,omitempty"`
	BaseFeeDefaultCents       int       `json:"base_fee_default_cents"`
	BaseFeeCompanionshipCents int       `json:"base_fee_companionship_cents"`
	PerMinuteRateCents        int       `json:"per_minute_rate_cents"`
	SurgeRateCentsPerMin      int       `json:"surge_rate_cents_per_min"`
	PlatformFeeBps            int       `json:"platform_fee_bps"`
	Note                      string    `json:"note,omitempty"`
	CreatedBy                 string    `json:"created_by,omitempty"`
	CreatedAt                 time.Time `json:"created_at,omitempty"`
}

// The schedule every task posted before tasks.base_fee_cents existed was
// billed under. Read only for such a task's stored-price fallback, so its
// history never moves.
const (
	legacyBaseFeeDefaultCents       = 1200
	legacyBaseFeeCompanionshipCents = 2500
	legacyPlatformFeeBps            = 2000
	legacyPerMinuteRateCents        = 50
)

const pricingRefresh = time.Minute

// Bounds an admin can set. Wide enough for any real decision, narrow enough
// that a typo (1500 meant as $1500.00, a fee of 300%) is refused.
const (
	maxBaseFeeCents  = 20000 // $200
	maxRateCents     = 1000  // $10/min
	maxPlatformFeeBp = 6000  // 60%
)

var livePricing atomic.Pointer[PriceSchedule]

// compiledSchedule is BillingConfig's prices: the schedule before the first
// successful read, and the one tests run against.
func compiledSchedule() PriceSchedule {
	return PriceSchedule{
		BaseFeeDefaultCents:       Billing.BaseFeeDefaultCents,
		BaseFeeCompanionshipCents: Billing.BaseFeeCompanionshipCents,
		PerMinuteRateCents:        Billing.PerMinuteRateCents,
		SurgeRateCentsPerMin:      Billing.SurgeRateCentsPerMin,
		PlatformFeeBps:            Billing.PlatformFeeBps,
	}
}

// pricing is the live schedule. Safe for concurrent use.
func pricing() PriceSchedule {
	if p := livePricing.Load(); p != nil {
		return *p
	}
	return compiledSchedule()
}

func (p PriceSchedule) validate() error {
	switch {
	case p.BaseFeeDefaultCents <= 0 || p.BaseFeeDefaultCents > maxBaseFeeCents:
		return fmt.Errorf("base fee must be between $0.01 and %s", formatCentsUSD(maxBaseFeeCents))
	case p.BaseFeeCompanionshipCents <= 0 || p.BaseFeeCompanionshipCents > maxBaseFeeCents:
		return fmt.Errorf("companionship base fee must be between $0.01 and %s", formatCentsUSD(maxBaseFeeCents))
	case p.PerMinuteRateCents <= 0 || p.PerMinuteRateCents > maxRateCents:
		return fmt.Errorf("per-minute rate must be between $0.01 and %s", formatCentsUSD(maxRateCents))
	case p.SurgeRateCentsPerMin <= 0 || p.SurgeRateCentsPerMin > maxRateCents:
		return fmt.Errorf("evening rate must be between $0.01 and %s", formatCentsUSD(maxRateCents))
	case p.SurgeRateCentsPerMin < p.PerMinuteRateCents:
		// isSurgeRate labels a stored rate by comparing it with this one; an
		// evening rate below the day rate would label every task "evening".
		return errors.New("evening rate can't be lower than the standard rate")
	case p.PlatformFeeBps < 0 || p.PlatformFeeBps > maxPlatformFeeBp:
		return fmt.Errorf("platform fee must be between 0%% and %d%%", maxPlatformFeeBp/100)
	}
	return nil
}

// loadPricing reads the newest schedule. A missing table (a database not yet
// migrated), no row, or an invalid row keeps what is live.
func loadPricing(ctx context.Context) error {
	var p PriceSchedule
	var by *string
	err := db.QueryRow(ctx, `
		select id::text, base_fee_default_cents, base_fee_companionship_cents,
		       per_minute_rate_cents, surge_rate_cents_per_min, platform_fee_bps,
		       coalesce(note,''), created_by::text, created_at
		  from public.pricing_schedules
		 order by created_at desc, id desc
		 limit 1`).Scan(&p.ID, &p.BaseFeeDefaultCents, &p.BaseFeeCompanionshipCents,
		&p.PerMinuteRateCents, &p.SurgeRateCentsPerMin, &p.PlatformFeeBps, &p.Note, &by, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if by != nil {
		p.CreatedBy = *by
	}
	if err := p.validate(); err != nil {
		return fmt.Errorf("schedule %s refused: %w", p.ID, err)
	}
	if cur := livePricing.Load(); cur == nil || cur.ID != p.ID {
		log.Printf("[pricing] schedule %s live: base %s / companionship %s, %s/min, evening %s/min, fee %d bps",
			p.ID, formatCentsUSD(p.BaseFeeDefaultCents), formatCentsUSD(p.BaseFeeCompanionshipCents),
			formatCentsUSD(p.PerMinuteRateCents), formatCentsUSD(p.SurgeRateCentsPerMin), p.PlatformFeeBps)
	}
	livePricing.Store(&p)
	return nil
}

// watchPricing keeps the live schedule fresh. One failed read is logged once
// per error text, not every minute.
func watchPricing() {
	var lastErr string
	read := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := loadPricing(ctx); err != nil {
			if err.Error() != lastErr {
				log.Printf("[pricing] keeping the current schedule: %v", err)
				lastErr = err.Error()
			}
			return
		}
		lastErr = ""
	}
	read()
	for range time.Tick(pricingRefresh) {
		read()
	}
}

// ── The price a task was posted at ─────────────────────────────────────────

// newTaskPrice is what a task posted now is billed by, from the live
// schedule: the base fee for its category and the platform fee. (The rate is
// resolveRateCentsPerMin, from the task's start.)
func newTaskPrice(category string) (baseFeeCents, platformFeeBps int) {
	p := pricing()
	if isCompanionship(category) {
		return p.BaseFeeCompanionshipCents, p.PlatformFeeBps
	}
	return p.BaseFeeDefaultCents, p.PlatformFeeBps
}

// storedBaseFee is a task's stored base fee, or — for a task posted before
// the column existed — the legacy schedule's, so its history never moves.
func storedBaseFee(base *int, category string) int {
	if base != nil && *base > 0 {
		return *base
	}
	if isCompanionship(category) {
		return legacyBaseFeeCompanionshipCents
	}
	return legacyBaseFeeDefaultCents
}

// storedFeeBps is a task's stored platform fee, or the legacy 20%.
func storedFeeBps(bps *int) int {
	if bps != nil && *bps >= 0 {
		return *bps
	}
	return legacyPlatformFeeBps
}

// taskStoredPrice is the base fee and platform fee a task was posted at, read
// together so the two can never come from different schedules. A task that
// cannot be read (no row: a caller pricing something that is not yet a task)
// is priced as a task posted now would be — never as free.
func taskStoredPrice(ctx context.Context, taskID string) (baseFeeCents, platformFeeBps int) {
	var base, bps *int
	var category string
	if err := db.QueryRow(ctx,
		`select base_fee_cents, platform_fee_bps, coalesce(category,'') from public.tasks where id = $1::uuid`, taskID,
	).Scan(&base, &bps, &category); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("[pricing] task=%s: stored price unreadable, pricing as a new task: %v", taskID, err)
		}
		return newTaskPrice(category)
	}
	return storedBaseFee(base, category), storedFeeBps(bps)
}

// taskBaseFeeCents is the base fee a task was posted at.
func taskBaseFeeCents(ctx context.Context, taskID string) int {
	b, _ := taskStoredPrice(ctx, taskID)
	return b
}

// taskPlatformFeeBps is the platform fee a task was posted at.
func taskPlatformFeeBps(ctx context.Context, taskID string) int {
	_, f := taskStoredPrice(ctx, taskID)
	return f
}

// ── Admin ──────────────────────────────────────────────────────────────────

// GET /admin/pricing — the live schedule and the history behind it.
func adminGetPricing(c *gin.Context) {
	rows, err := db.Query(c.Request.Context(), `
		select s.id::text, s.base_fee_default_cents, s.base_fee_companionship_cents,
		       s.per_minute_rate_cents, s.surge_rate_cents_per_min, s.platform_fee_bps,
		       coalesce(s.note,''), coalesce(nullif(p.name,''), u.email, ''), s.created_at
		  from public.pricing_schedules s
		  left join public.users u    on u.id = s.created_by
		  left join public.profiles p on lower(p.email) = lower(u.email)
		 order by s.created_at desc, s.id desc
		 limit 50`)
	if err != nil {
		log.Printf("[pricing] admin list: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()
	history := []PriceSchedule{}
	for rows.Next() {
		var p PriceSchedule
		if err := rows.Scan(&p.ID, &p.BaseFeeDefaultCents, &p.BaseFeeCompanionshipCents,
			&p.PerMinuteRateCents, &p.SurgeRateCentsPerMin, &p.PlatformFeeBps, &p.Note, &p.CreatedBy, &p.CreatedAt); err != nil {
			break
		}
		history = append(history, p)
	}
	c.JSON(http.StatusOK, gin.H{
		"live":    pricing(),
		"history": history,
		"bounds": gin.H{
			"max_base_fee_cents":   maxBaseFeeCents,
			"max_rate_cents":       maxRateCents,
			"max_platform_fee_bps": maxPlatformFeeBp,
		},
	})
}

// POST /admin/pricing — a new schedule, for tasks posted from now on.
func adminSetPricing(c *gin.Context) {
	// Pointers: a field left out is refused, never read as 0 (a 0% fee
	// set by omission).
	var raw struct {
		BaseFeeDefaultCents       *int   `json:"base_fee_default_cents"`
		BaseFeeCompanionshipCents *int   `json:"base_fee_companionship_cents"`
		PerMinuteRateCents        *int   `json:"per_minute_rate_cents"`
		SurgeRateCentsPerMin      *int   `json:"surge_rate_cents_per_min"`
		PlatformFeeBps            *int   `json:"platform_fee_bps"`
		Note                      string `json:"note"`
	}
	if err := c.ShouldBindJSON(&raw); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	if raw.BaseFeeDefaultCents == nil || raw.BaseFeeCompanionshipCents == nil || raw.PerMinuteRateCents == nil ||
		raw.SurgeRateCentsPerMin == nil || raw.PlatformFeeBps == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_schedule", "message": "Every price is required."})
		return
	}
	in := PriceSchedule{
		BaseFeeDefaultCents:       *raw.BaseFeeDefaultCents,
		BaseFeeCompanionshipCents: *raw.BaseFeeCompanionshipCents,
		PerMinuteRateCents:        *raw.PerMinuteRateCents,
		SurgeRateCentsPerMin:      *raw.SurgeRateCentsPerMin,
		PlatformFeeBps:            *raw.PlatformFeeBps,
		Note:                      raw.Note,
	}
	if err := in.validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_schedule", "message": err.Error()})
		return
	}
	note := strings.TrimSpace(in.Note)
	if note == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "note_required", "message": "Say why the prices are changing."})
		return
	}
	note = truncateRunes(note, 280)
	ctx := c.Request.Context()
	uid := c.GetString("uid")
	var id string
	if err := db.QueryRow(ctx, `
		insert into public.pricing_schedules
		  (base_fee_default_cents, base_fee_companionship_cents, per_minute_rate_cents,
		   surge_rate_cents_per_min, platform_fee_bps, note, created_by)
		values ($1, $2, $3, $4, $5, $6, nullif($7,'')::uuid)
		returning id::text`,
		in.BaseFeeDefaultCents, in.BaseFeeCompanionshipCents, in.PerMinuteRateCents,
		in.SurgeRateCentsPerMin, in.PlatformFeeBps, note, uid).Scan(&id); err != nil {
		log.Printf("[pricing] admin set: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if err := loadPricing(ctx); err != nil {
		log.Printf("[pricing] reload after set: %v", err)
	}
	log.Printf("[pricing] schedule %s set by admin uid=%s", id, uid)
	c.JSON(http.StatusCreated, gin.H{"id": id, "live": pricing()})
}

// GET /pricing — the requester-facing prices for the clients' marketing
// sentences ("$15 base fee includes the first 15 minutes…"), so no client
// hardcodes a price that can now change without a release. Public: prices
// are public. The platform fee is not one of them (it is the supporter side's
// business; Earnings carries it).
func getPublicPricing(c *gin.Context) {
	p := pricing()
	c.JSON(http.StatusOK, gin.H{
		"base_fee_default_cents":       p.BaseFeeDefaultCents,
		"base_fee_companionship_cents": p.BaseFeeCompanionshipCents,
		"per_minute_rate_cents":        p.PerMinuteRateCents,
		"surge_rate_cents_per_min":     p.SurgeRateCentsPerMin,
		"included_minutes":             Billing.IncludedMinutes,
		"surge_window":                 surgeWindowLabel(),
	})
}
