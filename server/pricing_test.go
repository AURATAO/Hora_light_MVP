package main

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// freshPricingTable leaves only the migration's seed row, and puts back
// whatever was live when the test ends. The fixture recreates the task
// tables but not this one.
func freshPricingTable(t *testing.T) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `
		delete from public.pricing_schedules
		 where id <> (select id from public.pricing_schedules order by created_at asc, id asc limit 1)`); err != nil {
		t.Fatal(err)
	}
	prev := livePricing.Load()
	t.Cleanup(func() {
		livePricing.Store(prev)
		_, _ = db.Exec(context.Background(), `
			delete from public.pricing_schedules
			 where id <> (select id from public.pricing_schedules order by created_at asc, id asc limit 1)`)
	})
}

// withSchedule makes p the live schedule for one test.
func withSchedule(t *testing.T, p PriceSchedule) {
	t.Helper()
	prev := livePricing.Load()
	livePricing.Store(&p)
	t.Cleanup(func() { livePricing.Store(prev) })
}

var testHigherSchedule = PriceSchedule{
	ID: "test-higher", BaseFeeDefaultCents: 2000, BaseFeeCompanionshipCents: 3000,
	PerMinuteRateCents: 80, SurgeRateCentsPerMin: 120, PlatformFeeBps: 3500,
}

// A task is billed by the prices it was posted at, whatever the schedule
// says afterwards: base fee, platform fee and rate all stay put.
func TestPricingIsLockedAtPost(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open") // stores the compiled schedule: $15, 30%, $0.60
	withSchedule(t, testHigherSchedule)

	if got := taskBaseFeeCents(context.Background(), w.taskID); got != 1500 {
		t.Errorf("base fee moved with the schedule: %d", got)
	}
	if got := taskPlatformFeeBps(context.Background(), w.taskID); got != 3000 {
		t.Errorf("fee moved with the schedule: %d", got)
	}
	// 40 minutes: $15.00 + 25 × $0.60 at the stored price, not $20 + 25 × $0.80.
	if got := calcTaskCostCents(context.Background(), w.taskID, 40); got != 1500+25*60 {
		t.Errorf("settled at %s, want $30.00", formatCentsUSD(got))
	}
	// The supporter's cut is the stored 30%, not 35%.
	pay := supporterPayForBps(3000, 0, taskPlatformFeeBps(context.Background(), w.taskID))
	if pay.FeeCents != 900 || pay.FeeBps != 3000 {
		t.Errorf("pay %+v", pay)
	}
	// A new quote is today's.
	if q := quoteTask("delivery", 40, 0, resolveRateCentsPerMin(nyNoon())); q.BaseFeeCents != 2000 || q.PerMinuteRateCents != 80 {
		t.Errorf("a new quote did not use the live schedule: %+v", q)
	}
}

// A task from before prices were stored bills at the schedule it was posted
// under: $12 / $25 and 20%.
func TestPricingLegacyTaskKeepsTheOldSchedule(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	if _, err := db.Exec(context.Background(),
		`update public.tasks set base_fee_cents = null, platform_fee_bps = null where id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	if got := taskBaseFeeCents(context.Background(), w.taskID); got != 1200 {
		t.Errorf("legacy base %d", got)
	}
	if got := taskPlatformFeeBps(context.Background(), w.taskID); got != 2000 {
		t.Errorf("legacy fee %d", got)
	}
	if _, err := db.Exec(context.Background(),
		`update public.tasks set category = 'companionship' where id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	if got := taskBaseFeeCents(context.Background(), w.taskID); got != 2500 {
		t.Errorf("legacy companionship base %d", got)
	}
}

// The migration seeds today's schedule, the server reads the newest row, and
// a row the server would refuse is never made live.
func TestPricingLoadsTheNewestValidRow(t *testing.T) {
	setupStripeWebhookDB(t)
	freshPricingTable(t)

	if err := loadPricing(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := pricing(); p.BaseFeeDefaultCents != 1500 || p.PerMinuteRateCents != 60 || p.PlatformFeeBps != 3000 ||
		p.BaseFeeCompanionshipCents != 2500 || p.SurgeRateCentsPerMin != 100 || p.ID == "" {
		t.Fatalf("seeded schedule %+v", p)
	}
	seeded := pricing().ID
	if _, err := db.Exec(context.Background(), `
		insert into public.pricing_schedules
		  (base_fee_default_cents, base_fee_companionship_cents, per_minute_rate_cents,
		   surge_rate_cents_per_min, platform_fee_bps, note, created_at)
		values (1700, 2600, 65, 110, 3000, 'test', now() + interval '1 second')`); err != nil {
		t.Fatal(err)
	}
	if err := loadPricing(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := pricing(); p.BaseFeeDefaultCents != 1700 || p.ID == seeded {
		t.Fatalf("newest row not live: %+v", p)
	}
}

func TestPricingAdminEndpoints(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	freshPricingTable(t)

	set := func(uid, email, body string) (int, map[string]any) {
		return callAdminOps(t, adminSetPricing, "", "", uid, email, body)
	}
	ok := `{"base_fee_default_cents":1600,"base_fee_companionship_cents":2500,"per_minute_rate_cents":60,
	        "surge_rate_cents_per_min":100,"platform_fee_bps":3000,"note":"test bump"}`
	if code, _ := set(w.requesterID, requesterEmail, ok); code != http.StatusForbidden {
		t.Errorf("non-admin set: %d", code)
	}
	for _, bad := range []string{
		`{"base_fee_default_cents":0,"base_fee_companionship_cents":2500,"per_minute_rate_cents":60,"surge_rate_cents_per_min":100,"platform_fee_bps":3000,"note":"x"}`,
		`{"base_fee_default_cents":150000,"base_fee_companionship_cents":2500,"per_minute_rate_cents":60,"surge_rate_cents_per_min":100,"platform_fee_bps":3000,"note":"x"}`,
		`{"base_fee_default_cents":1500,"base_fee_companionship_cents":2500,"per_minute_rate_cents":60,"surge_rate_cents_per_min":50,"platform_fee_bps":3000,"note":"x"}`,
		`{"base_fee_default_cents":1500,"base_fee_companionship_cents":2500,"per_minute_rate_cents":60,"surge_rate_cents_per_min":100,"platform_fee_bps":30000,"note":"x"}`,
		`{"base_fee_default_cents":1500,"base_fee_companionship_cents":2500,"per_minute_rate_cents":60,"surge_rate_cents_per_min":100,"platform_fee_bps":3000,"note":"  "}`,
		// A field left out is refused, never read as 0 (a 0% fee by omission).
		`{"base_fee_default_cents":1500,"base_fee_companionship_cents":2500,"per_minute_rate_cents":60,"surge_rate_cents_per_min":100,"note":"x"}`,
	} {
		if code, out := set(w.adminID, adminEmail, bad); code != http.StatusBadRequest || out["message"] == nil {
			t.Errorf("%s: %d %v", bad, code, out)
		}
	}
	code, out := set(w.adminID, adminEmail, ok)
	if code != http.StatusCreated {
		t.Fatalf("set: %d %v", code, out)
	}
	if pricing().BaseFeeDefaultCents != 1600 {
		t.Errorf("not live after set: %+v", pricing())
	}
	// The history names who changed it.
	gw, _ := callAdminOps(t, adminGetPricing, "", "", w.adminID, adminEmail, "")
	if gw != http.StatusOK {
		t.Fatalf("get: %d", gw)
	}
	_, got := callAdminOps(t, adminGetPricing, "", "", w.adminID, adminEmail, "")
	hist, _ := got["history"].([]any)
	if len(hist) < 2 {
		t.Fatalf("history %v", got["history"])
	}
	first := hist[0].(map[string]any)
	if num(first["base_fee_default_cents"]) != 1600 || first["note"] != "test bump" || first["created_by"] == "" {
		t.Errorf("newest history row %v", first)
	}
}

// nyNoon is a standard-rate start (noon in New York).
func nyNoon() time.Time {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.Date(2026, 9, 15, 16, 0, 0, 0, time.UTC)
	}
	return time.Date(2026, 9, 15, 12, 0, 0, 0, loc)
}
