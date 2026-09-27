package main

import (
	"context"
	"net/http"
	"testing"
)

// Account deletion (App Store 5.1.1(v)) against a real Postgres: what blocks
// it, what it removes, what it keeps, and the audit row. Stripe and Supabase
// are not configured in this environment, so those legs are skipped and the
// audit meta records that they were.

// setupDeletionDB is the Stripe fixture plus the users identity columns the
// anonymiser clears, which the hand-written fixture never carried (the real
// baseline has them: 20260711094158_remote_schema.sql).
func setupDeletionDB(t *testing.T) {
	t.Helper()
	setupStripeWebhookDB(t)
	mustExec(t, `alter table public.users
	               add column if not exists supabase_sub uuid,
	               add column if not exists google_sub text,
	               add column if not exists picture text`)
	mustExec(t, `alter table public.profiles
	               add column if not exists phone text default '',
	               add column if not exists bio text default '',
	               add column if not exists avatar_url text default '',
	               add column if not exists updated_at timestamptz default now()`)
}

func deleteAs(t *testing.T, uid, email string) (int, map[string]any) {
	t.Helper()
	code, w := callTaskHandler(t, deleteMyAccount, http.MethodDelete, "/profile", "", uid, email, "", nil)
	return code, decodeFirstJSON(t, w)
}

func TestAccountDeletionIsRefusedWhileMoneyOrWorkIsOpen(t *testing.T) {
	setupDeletionDB(t)
	w := seedOpsWorld(t, "open") // requester has an open task, supporter holds it

	code, body := deleteAs(t, w.requesterID, requesterEmail)
	if code != http.StatusConflict || body["error"] != "account_not_deletable" {
		t.Fatalf("requester with an open task: %d %v, want 409", code, body)
	}
	code, body = deleteAs(t, w.supporterID, oldSupporterEmail)
	if code != http.StatusConflict {
		t.Fatalf("supporter on an open task: %d %v, want 409", code, body)
	}

	// Close the task; a payout still owed keeps the supporter blocked.
	mustExec(t, `update public.tasks set status = 'completed' where id = $1::uuid`, w.taskID)
	pay := seedCapturedPayment(t, w.taskID, w.requesterID, 2000, "ch_del")
	mustExec(t, `insert into public.payouts (task_id, supporter_id, payment_id, amount_cents, status)
	             values ($1::uuid, $2::uuid, $3::uuid, 1600, $4)`, w.taskID, w.supporterID, pay, payoutStatusPending)
	code, body = deleteAs(t, w.supporterID, oldSupporterEmail)
	if code != http.StatusConflict {
		t.Fatalf("supporter owed a payout: %d %v, want 409", code, body)
	}
	blockers, _ := body["blockers"].([]any)
	if len(blockers) != 1 {
		t.Fatalf("blockers = %v, want exactly the payout", body["blockers"])
	}

	// Nothing was touched by a refused delete.
	var email string
	_ = db.QueryRow(context.Background(), `select email from public.users where id = $1::uuid`, w.supporterID).Scan(&email)
	if email != oldSupporterEmail {
		t.Fatalf("a refused delete anonymised the account: %q", email)
	}
}

func TestAccountDeletionAnonymisesAndKeepsTheLedger(t *testing.T) {
	setupDeletionDB(t)
	w := seedOpsWorld(t, "completed")
	ctx := context.Background()
	pay := seedCapturedPayment(t, w.taskID, w.requesterID, 2000, "ch_del2")
	mustExec(t, `insert into public.payouts (task_id, supporter_id, payment_id, amount_cents, status)
	             values ($1::uuid, $2::uuid, $3::uuid, 1600, $4)`, w.taskID, w.supporterID, pay, payoutStatusPaid)
	seedWorklog(t, w.taskID, 60, false)
	mustExec(t, `insert into public.task_gps_pings (task_id, user_id, lat, lng) values ($1::uuid, $2::uuid, 40.7, -74.0)`, w.taskID, w.supporterID)
	mustExec(t, `insert into public.device_push_tokens (user_id, expo_push_token) values ($1::uuid, 'ExponentPushToken[del]')`, w.supporterID)
	mustExec(t, `update public.users set supabase_sub = gen_random_uuid(), google_sub = 'g-del' where id = $1::uuid`, w.supporterID)

	code, body := deleteAs(t, w.supporterID, oldSupporterEmail)
	if code != http.StatusOK {
		t.Fatalf("delete: %d %v", code, body)
	}

	var email, name string
	var sub, cus *string
	if err := db.QueryRow(ctx, `select email, coalesce(name,''), supabase_sub::text, stripe_customer_id
	                              from public.users where id = $1::uuid`, w.supporterID).Scan(&email, &name, &sub, &cus); err != nil {
		t.Fatalf("users row vanished — it must survive anonymised: %v", err)
	}
	if email != deletedPlaceholderEmail(w.supporterID) || name != "" || sub != nil || cus != nil {
		t.Errorf("users not anonymised: email=%q name=%q sub=%v cus=%v", email, name, sub, cus)
	}
	var pname, pemail string
	var verified bool
	_ = db.QueryRow(ctx, `select coalesce(name,''), email, coalesce(is_verified_supporter,false)
	                       from public.profiles where id = $1::uuid`, w.supporterID).Scan(&pname, &pemail, &verified)
	if pname != "" || pemail != email || verified {
		t.Errorf("profile not anonymised: name=%q email=%q verified=%v", pname, pemail, verified)
	}

	// The legacy email columns follow, so the history reads as a deleted user.
	var assignedTo, wlUser string
	_ = db.QueryRow(ctx, `select assigned_to from public.tasks where id = $1::uuid`, w.taskID).Scan(&assignedTo)
	_ = db.QueryRow(ctx, `select "user" from public.worklogs where task_id = $1::uuid limit 1`, w.taskID).Scan(&wlUser)
	if assignedTo != email || wlUser != email {
		t.Errorf("legacy email columns not anonymised: task=%q worklog=%q", assignedTo, wlUser)
	}

	// Personal data gone; the ledger intact.
	var pings, tokens, payouts, payments int
	_ = db.QueryRow(ctx, `select count(*) from public.task_gps_pings where user_id = $1::uuid`, w.supporterID).Scan(&pings)
	_ = db.QueryRow(ctx, `select count(*) from public.device_push_tokens where user_id = $1::uuid`, w.supporterID).Scan(&tokens)
	_ = db.QueryRow(ctx, `select count(*) from public.payouts where supporter_id = $1::uuid`, w.supporterID).Scan(&payouts)
	_ = db.QueryRow(ctx, `select count(*) from public.payments where task_id = $1::uuid`, w.taskID).Scan(&payments)
	if pings != 0 || tokens != 0 {
		t.Errorf("location/push data survived: pings=%d tokens=%d", pings, tokens)
	}
	if payouts != 1 || payments != 1 {
		t.Errorf("the ledger did not survive: payouts=%d payments=%d", payouts, payments)
	}

	// The audit row, on the system job id since no task is involved.
	if n := len(auditRows(t, systemActorUID, "ACCOUNT_DELETED")); n != 1 {
		t.Errorf("want 1 ACCOUNT_DELETED audit row, got %d", n)
	}

	// The requester, untouched.
	var reqEmail string
	_ = db.QueryRow(ctx, `select email from public.users where id = $1::uuid`, w.requesterID).Scan(&reqEmail)
	if reqEmail != requesterEmail {
		t.Errorf("the other party was touched: %q", reqEmail)
	}
}
