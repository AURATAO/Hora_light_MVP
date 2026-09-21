package main

// Earnings pagination.
//
// The earnings strip shows three transfers and a "See all (N)", and "See all"
// opens a paginated list — so the endpoint that used to hand back a fixed
// twenty now takes limit/offset and reports a total. OFFSET rather than a
// keyset, deliberately: payouts.created_at is not unique (two transfers from
// one settlement share it), so a keyset on it alone can skip or repeat a row
// at a page boundary. The shared-timestamp case is seeded below on purpose.
//
// No Stripe: readConnectStatus returns before any network call when the
// supporter has no stripe_account_id, which is the fixture's default.
//
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55440/horatest' \
//	  go test ./ -run EarningsPagination -v

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

type earningsEnvelope struct {
	LifetimeEarnedCents int `json:"lifetime_earned_cents"`
	Total               int `json:"total"`
	Transfers           []struct {
		TaskID      string `json:"task_id"`
		AmountCents int    `json:"amount_cents"`
		Status      string `json:"status"`
	} `json:"transfers"`
}

func earningsPage(t *testing.T, uid string, query string) earningsEnvelope {
	t.Helper()
	code, rec := callTaskHandler(t, earningsHandler, http.MethodGet, "/payments/earnings"+query,
		"", uid, oldSupporterEmail, "", nil)
	if code != http.StatusOK {
		t.Fatalf("earnings%s: %d (%s)", query, code, rec.Body.String())
	}
	var out earningsEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return out
}

// seedPayoutFor writes one finished task with a captured payment and a payout
// of `cents` to the world's supporter. `minutesAgo` orders the rows; two
// calls with the SAME value share a created_at, which is the case offset
// pagination exists to survive.
func seedPayoutFor(t *testing.T, w opsWorld, i, cents int, status string, minutesAgo int) string {
	t.Helper()
	ctx := context.Background()
	var taskID string
	if err := db.QueryRow(ctx, `
		insert into public.tasks
			(title, description, category, location_text, estimated_minutes,
			 requester, requester_id, assigned_to, assigned_to_id, status)
		values ($1, 'seeded', 'quick_errand', 'Somewhere | NYC', 30,
			 $2, $3::uuid, $4, $5::uuid, 'completed')
		returning id::text`,
		fmt.Sprintf("Paid task %d", i), requesterEmail, w.requesterID, oldSupporterEmail, w.supporterID,
	).Scan(&taskID); err != nil {
		t.Fatalf("seed paid task: %v", err)
	}
	paymentID := seedPayment(t, taskID, w.requesterID, fmt.Sprintf("pi_earn_%d", i), paymentStatusCaptured)
	var payoutID string
	if err := db.QueryRow(ctx, `
		insert into public.payouts (task_id, supporter_id, payment_id, amount_cents, status, created_at)
		values ($1::uuid, $2::uuid, $3::uuid, $4, $5, now() - make_interval(mins => $6))
		returning id::text`,
		taskID, w.supporterID, paymentID, cents, status, minutesAgo,
	).Scan(&payoutID); err != nil {
		t.Fatalf("seed payout: %v", err)
	}
	return taskID
}

// Seven transfers, walked three at a time: every row exactly once, the total
// constant, and nothing lost at the boundary where two rows share a timestamp.
func TestEarningsPaginationWalksEveryTransferOnce(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")

	// Rows 2 and 3 share created_at — the boundary a page of three lands on.
	ages := []int{10, 20, 20, 40, 50, 60, 70}
	for i, age := range ages {
		seedPayoutFor(t, w, i, 1000+i, payoutStatusPaid, age)
	}

	seen := map[string]int{}
	for offset := 0; offset < 12; offset += 3 {
		page := earningsPage(t, w.supporterID, fmt.Sprintf("?limit=3&offset=%d", offset))
		if page.Total != 7 {
			t.Fatalf("total = %d at offset %d, want 7 — the count followed the page", page.Total, offset)
		}
		for _, tr := range page.Transfers {
			seen[tr.TaskID]++
		}
		if len(page.Transfers) == 0 {
			break
		}
	}
	if len(seen) != 7 {
		t.Errorf("walked %d distinct transfers, want 7 — a page boundary skipped a row", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("transfer for task %s came back %d times — a page boundary repeated a row", id, n)
		}
	}
}

// The strip's own call: no params, a default page, and the total it needs to
// say "See all (N)".
func TestEarningsPaginationDefaultPageCarriesTotal(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	for i := 0; i < 5; i++ {
		seedPayoutFor(t, w, i, 1500, payoutStatusPaid, (i+1)*10)
	}
	page := earningsPage(t, w.supporterID, "")
	if page.Total != 5 {
		t.Errorf("total = %d, want 5", page.Total)
	}
	if len(page.Transfers) != 5 {
		t.Errorf("%d transfers on the default page, want all 5 (under the default limit)", len(page.Transfers))
	}
	// Past the end: nothing, but the total still says how many exist.
	tail := earningsPage(t, w.supporterID, "?limit=3&offset=50")
	if len(tail.Transfers) != 0 || tail.Total != 5 {
		t.Errorf("offset past the end: %d transfers, total %d — want 0 and 5", len(tail.Transfers), tail.Total)
	}
}

// Lifetime counts PAID only — pending and failed money is not in anyone's
// bank yet — while total counts every transfer, because "See all" lists
// every transfer.
func TestEarningsPaginationLifetimeIsPaidOnlyButTotalIsEverything(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedPayoutFor(t, w, 0, 1200, payoutStatusPaid, 10)
	seedPayoutFor(t, w, 1, 2500, payoutStatusPaid, 20)
	seedPayoutFor(t, w, 2, 999, payoutStatusPending, 30)
	seedPayoutFor(t, w, 3, 777, payoutStatusFailed, 40)

	page := earningsPage(t, w.supporterID, "?limit=50")
	if page.LifetimeEarnedCents != 3700 {
		t.Errorf("lifetime = %s, want $37.00 (paid rows only)", formatCentsUSD(page.LifetimeEarnedCents))
	}
	if page.Total != 4 {
		t.Errorf("total = %d, want 4 (every transfer, whatever its status)", page.Total)
	}
}

// Garbage in the query is the first page, not an error and not a crash: this
// is a screen about somebody's money.
func TestEarningsPaginationMalformedParamsAreTheFirstPage(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedPayoutFor(t, w, 0, 1200, payoutStatusPaid, 10)
	for _, q := range []string{"?limit=abc", "?offset=-5", "?limit=-1&offset=xyz", "?limit=999999"} {
		page := earningsPage(t, w.supporterID, q)
		if len(page.Transfers) != 1 || page.Total != 1 {
			t.Errorf("%s: %d transfers, total %d — want the first page (1, 1)", q, len(page.Transfers), page.Total)
		}
	}
}
