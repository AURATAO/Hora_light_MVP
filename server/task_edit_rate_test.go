package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// What PATCH /tasks/:id does to the two terms of the agreement it used to
// ignore: the per-minute rate, which follows the start time, and the
// auto-extend consent, which the edit screen sends and the UPDATE dropped.
//
// DB-backed, on the created_via fixture (same handler, same tables):
//
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55435/horatest' \
//	  go test ./ -run 'TaskEdit|TaskStartMoved' -v

// nyTomorrowAt is tomorrow at the given New York hour, as RFC3339 — a start
// that is always in the future and always on a known side of the surge window.
func nyTomorrowAt(t *testing.T, hour int) string {
	t.Helper()
	loc, err := time.LoadLocation(Billing.SurgeTimezone)
	if err != nil {
		t.Fatalf("load %s: %v", Billing.SurgeTimezone, err)
	}
	d := time.Now().In(loc).AddDate(0, 0, 1)
	return time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, loc).Format(time.RFC3339)
}

func patchTaskJSON(t *testing.T, id, body string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPatch, "/tasks/"+id, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: id}}
	c.Set("email", createdViaEmail)
	updateTask(c)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func rateAndConsentOf(t *testing.T, id string) (int, bool) {
	t.Helper()
	var rate int
	var consent bool
	if err := db.QueryRow(context.Background(),
		`select rate_cents_per_min, auto_extend_consent from public.tasks where id=$1::uuid`, id,
	).Scan(&rate, &consent); err != nil {
		t.Fatalf("read rate/consent: %v", err)
	}
	return rate, consent
}

func scheduledBody(at string, extra string) string {
	return `{"title":"Coffee run","category":"delivery","estimated_minutes":60,` +
		`"is_immediate":false,"scheduled_at":"` + at + `"` + extra + `}`
}

func TestTaskStartMoved(t *testing.T) {
	two := time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC)
	sameInstant := two.In(time.FixedZone("EDT", -4*3600))
	ten := two.Add(8 * time.Hour)
	cases := []struct {
		name   string
		wasImm bool
		wasAt  *time.Time
		isImm  bool
		at     *time.Time
		want   bool
	}{
		{"ASAP stays ASAP", true, &two, true, &ten, false},
		{"ASAP to scheduled", true, &two, false, &ten, true},
		{"scheduled to ASAP", false, &two, true, &ten, true},
		{"scheduled, same instant in another zone", false, &two, false, &sameInstant, false},
		{"scheduled, new time", false, &two, false, &ten, true},
		{"scheduled, time added", false, nil, false, &two, true},
		{"scheduled, never had a time", false, nil, false, nil, false},
	}
	for _, tc := range cases {
		if got := taskStartMoved(tc.wasImm, tc.wasAt, tc.isImm, tc.at); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The underbill: posted for the afternoon, edited to the evening.
func TestTaskEditMovingTheStartReResolvesTheRate(t *testing.T) {
	setupCreatedViaDB(t)
	afternoon, evening := nyTomorrowAt(t, 14), nyTomorrowAt(t, 22)

	code, body := postTaskJSON(t, scheduledBody(afternoon, ""))
	if code != 201 {
		t.Fatalf("seed: expected 201, got %d (%v)", code, body)
	}
	id, _ := body["id"].(string)
	if rate, _ := rateAndConsentOf(t, id); rate != Billing.PerMinuteRateCents {
		t.Fatalf("posted for 2 PM at %d¢/min, want %d", rate, Billing.PerMinuteRateCents)
	}

	if code, out := patchTaskJSON(t, id, scheduledBody(evening, "")); code != 200 {
		t.Fatalf("edit to 10 PM: %d (%v)", code, out)
	}
	if rate, _ := rateAndConsentOf(t, id); rate != Billing.SurgeRateCentsPerMin {
		t.Errorf("edited to 10 PM but still %d¢/min, want %d", rate, Billing.SurgeRateCentsPerMin)
	}

	// And back: the rate follows the start in both directions.
	if code, out := patchTaskJSON(t, id, scheduledBody(afternoon, "")); code != 200 {
		t.Fatalf("edit back to 2 PM: %d (%v)", code, out)
	}
	if rate, _ := rateAndConsentOf(t, id); rate != Billing.PerMinuteRateCents {
		t.Errorf("edited back to 2 PM but still %d¢/min, want %d", rate, Billing.PerMinuteRateCents)
	}
}

// An edit that does not move the start must not touch the rate. A sentinel
// rate no resolution could produce makes "left alone" distinguishable from
// "re-resolved to the same answer".
func TestTaskEditThatKeepsTheStartKeepsTheRate(t *testing.T) {
	setupCreatedViaDB(t)
	const sentinel = 77
	afternoon := nyTomorrowAt(t, 14)

	for name, post := range map[string]string{
		"scheduled": scheduledBody(afternoon, ""),
		"asap":      `{"title":"Coffee run","category":"delivery","estimated_minutes":60,"is_immediate":true}`,
	} {
		code, body := postTaskJSON(t, post)
		if code != 201 {
			t.Fatalf("%s seed: expected 201, got %d (%v)", name, code, body)
		}
		id, _ := body["id"].(string)
		if _, err := db.Exec(context.Background(),
			`update public.tasks set rate_cents_per_min=$2 where id=$1::uuid`, id, sentinel); err != nil {
			t.Fatalf("%s: set sentinel: %v", name, err)
		}
		edit := strings.Replace(post, "Coffee run", "Coffee run, oat milk", 1)
		if code, out := patchTaskJSON(t, id, edit); code != 200 {
			t.Fatalf("%s edit: %d (%v)", name, code, out)
		}
		if rate, _ := rateAndConsentOf(t, id); rate != sentinel {
			t.Errorf("%s: a title edit re-priced the task to %d¢/min", name, rate)
		}
	}
}

// A move into the evening band is an increase like any other: it is measured
// against the hold, and refused — rate untouched — when the hold cannot cover it.
func TestTaskEditIntoEveningIsRefusedWhenTheHoldCannotCoverIt(t *testing.T) {
	setupCreatedViaDB(t)
	afternoon, evening := nyTomorrowAt(t, 14), nyTomorrowAt(t, 22)

	code, body := postTaskJSON(t, scheduledBody(afternoon, ""))
	if code != 201 {
		t.Fatalf("seed: expected 201, got %d (%v)", code, body)
	}
	id, _ := body["id"].(string)
	requesterID, _ := body["requester_id"].(string)
	held := preAuthAmountCents("delivery", 60, 0, Billing.PerMinuteRateCents)
	if _, err := db.Exec(context.Background(), `
		insert into public.payments (task_id, requester_id, kind, status,
		                             stripe_payment_intent_id, authorized_cents)
		values ($1::uuid, $2::uuid, $3, 'authorized', $4, $5)
	`, id, requesterID, paymentKindTaskPayment, "pi_edit_rate", held); err != nil {
		t.Fatalf("seed hold: %v", err)
	}

	code, out := patchTaskJSON(t, id, scheduledBody(evening, ""))
	if code != http.StatusConflict || out["error"] != "exceeds_authorized_hold" {
		t.Fatalf("edit to 10 PM under a daytime hold: %d (%v), want 409 exceeds_authorized_hold", code, out)
	}
	if rate, _ := rateAndConsentOf(t, id); rate != Billing.PerMinuteRateCents {
		t.Errorf("a refused edit changed the rate to %d¢/min", rate)
	}
	// Moving within the standard band still fits the hold.
	if code, out := patchTaskJSON(t, id, scheduledBody(nyTomorrowAt(t, 16), "")); code != 200 {
		t.Errorf("edit to 4 PM under the same hold: %d (%v)", code, out)
	}
}

func TestTaskEditSavesAutoExtendConsentAndAbsentLeavesIt(t *testing.T) {
	setupCreatedViaDB(t)
	at := nyTomorrowAt(t, 14)

	code, body := postTaskJSON(t, scheduledBody(at, ""))
	if code != 201 {
		t.Fatalf("seed: expected 201, got %d (%v)", code, body)
	}
	id, _ := body["id"].(string)
	if _, consent := rateAndConsentOf(t, id); !consent {
		t.Fatal("a post without the field should default to consent")
	}

	if code, out := patchTaskJSON(t, id, scheduledBody(at, `,"auto_extend_consent":false`)); code != 200 {
		t.Fatalf("edit withdrawing consent: %d (%v)", code, out)
	}
	if _, consent := rateAndConsentOf(t, id); consent {
		t.Error("the edit withdrew consent and it was not saved")
	}

	// Web's edit form sends no field at all; that must not hand consent back.
	if code, out := patchTaskJSON(t, id, scheduledBody(at, "")); code != 200 {
		t.Fatalf("edit without the field: %d (%v)", code, out)
	}
	if _, consent := rateAndConsentOf(t, id); consent {
		t.Error("an edit that omitted the field reset consent to true")
	}
}

// A promo task's hold is the DISCOUNTED amount, so the edit guard has to
// price the edit the same way. Comparing the undiscounted price against it
// refused every edit on a promo task, the no-op included.
func TestTaskEditUnderAPromoHoldComparesLikeWithLike(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	id := seedReassignTask(t, "open", w.requesterID, requesterEmail, "", "")
	// This fixture's tasks table is the payments slice of the schema; the
	// edit also writes one column it does not carry.
	mustExec(t, `alter table public.tasks add column if not exists transport_required text not null default 'none'`)
	mustExec(t, `update public.tasks set category='delivery', estimated_minutes=30, is_immediate=true,
	                    rate_cents_per_min=$2 where id=$1::uuid`, id, Billing.PerMinuteRateCents)
	promoID := seedPromoCode(t, "WELCOME10", 1000, promoOpts{})
	if err := seedRedemption(t, promoID, w.requesterID, id, 1000); err != nil {
		t.Fatalf("seed redemption: %v", err)
	}
	// $19.50 less $10.00, exactly what CreatePreAuth holds for this task.
	held := preAuthAfterPromoCents("delivery", 30, 0, Billing.PerMinuteRateCents, 1000)
	mustExec(t, `insert into public.payments (task_id, requester_id, kind, status,
	                                          stripe_payment_intent_id, authorized_cents)
	             values ($1::uuid, $2::uuid, $3, 'authorized', 'pi_promo_edit', $4)`,
		id, w.requesterID, paymentKindTaskPayment, held)

	edit := func(minutes int) (int, map[string]any) {
		gin.SetMode(gin.TestMode)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPatch, "/tasks/"+id, strings.NewReader(
			`{"title":"Coffee run, oat milk","category":"delivery","estimated_minutes":`+itoa(minutes)+`,"is_immediate":true}`))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: id}}
		c.Set("uid", w.requesterID)
		c.Set("email", requesterEmail)
		updateTask(c)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	if code, out := edit(30); code != http.StatusOK {
		t.Errorf("an edit that changes no price was refused under a promo hold: %d (%v)", code, out["error"])
	}
	if code, out := edit(20); code != http.StatusOK {
		t.Errorf("a cheaper edit was refused under a promo hold: %d (%v)", code, out["error"])
	}
	code, out := edit(60)
	if code != http.StatusConflict || out["error"] != "exceeds_authorized_hold" {
		t.Fatalf("an edit that outgrows the promo hold: %d (%v), want 409", code, out["error"])
	}
	// The number named is the discounted one the card would have to carry.
	want := float64(preAuthAfterPromoCents("delivery", 60, 0, Billing.PerMinuteRateCents, 1000))
	if out["required_cents"] != want {
		t.Errorf("required_cents = %v, want %v", out["required_cents"], want)
	}
}
