package channel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// DB-backed tests for PGStore. They run only when CHANNEL_TEST_DATABASE_URL
// points at a THROWAWAY database: each run drops and recreates the channel
// tables plus minimal users/tasks/extension_requests stubs, then applies the
// real migration file. Never point this at Supabase.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("CHANNEL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CHANNEL_TEST_DATABASE_URL not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	mig, err := os.ReadFile(filepath.Join("..", "..", "..", "supabase", "migrations", "20261019120000_messaging_channel.sql"))
	if err != nil {
		t.Fatal(err)
	}
	reset := `
		DROP TABLE IF EXISTS channel_setup_tokens, channel_conversations, channel_messages, channel_identities CASCADE;
		DROP TABLE IF EXISTS extension_requests, tasks, users CASCADE;
		DO $$ BEGIN
		  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='anon') THEN CREATE ROLE anon; END IF;
		  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='authenticated') THEN CREATE ROLE authenticated; END IF;
		END $$;
		CREATE TABLE users (id uuid PRIMARY KEY DEFAULT gen_random_uuid());
		CREATE TABLE tasks (id uuid PRIMARY KEY DEFAULT gen_random_uuid());
		CREATE TABLE extension_requests (id uuid PRIMARY KEY DEFAULT gen_random_uuid());`
	if _, err := db.Exec(reset); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(mig)); err != nil {
		t.Fatalf("migration: %v", err)
	}
	mig2, err := os.ReadFile(filepath.Join("..", "..", "..", "supabase", "migrations", "20261020120000_channel_messages_address_e164.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(mig2)); err != nil {
		t.Fatalf("migration e164: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func newUser(t *testing.T, db *sql.DB) string {
	var id string
	if err := db.QueryRow(`INSERT INTO users DEFAULT VALUES RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPGIdentityLifecycle(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()

	a, err := s.UpsertIdentity(ctx, IMessage, "+15551234567")
	if err != nil || a.Linked() {
		t.Fatalf("first upsert: %+v %v", a, err)
	}
	b, err := s.UpsertIdentity(ctx, IMessage, "+15551234567")
	if err != nil || b.ID != a.ID {
		t.Fatalf("upsert not idempotent: %v vs %v (%v)", a.ID, b.ID, err)
	}
	if _, err := s.UpsertIdentity(ctx, IMessage, "5551234567"); err == nil {
		t.Fatal("non-E.164 address accepted by the table")
	}

	// Inbound row gets attached; conversation touched twice keeps the latest.
	in := Inbound{Channel: IMessage, ProviderMessageID: "m1", From: a.Address, Text: "hi", ReceivedAt: time.Now()}
	if isNew, err := s.RecordInbound(ctx, in); err != nil || !isNew {
		t.Fatalf("record: %v %v", isNew, err)
	}
	if err := s.AttachMessage(ctx, IMessage, "m1", a.ID); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := s.TouchConversation(ctx, a.ID, later); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchConversation(ctx, a.ID, later.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	var last time.Time
	if err := db.QueryRow(`SELECT last_inbound_at FROM channel_conversations WHERE identity_id=$1`, a.ID).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if !last.Equal(later.Truncate(time.Microsecond)) && last.Sub(later).Abs() > time.Millisecond {
		t.Fatalf("last_inbound_at went backwards: %v", last)
	}

	// Token: peek does not consume; consume links once.
	tok, hash, err := NewSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSetupToken(ctx, a.ID, hash, time.Now().Add(SetupTokenTTL)); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SetupTokenIssuedSince(ctx, a.ID, time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatal("fresh token not seen")
	}
	if id, _, err := s.PeekSetupToken(ctx, HashSetupToken(tok)); err != nil || id.ID != a.ID {
		t.Fatalf("peek: %+v %v", id, err)
	}
	user := newUser(t, db)
	linked, err := s.ConsumeSetupTokenAndLink(ctx, hash, user)
	if err != nil || linked.UserID != user {
		t.Fatalf("link: %+v %v", linked, err)
	}
	if _, err := s.ConsumeSetupTokenAndLink(ctx, hash, user); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("second use of the same link: %v", err)
	}
	if _, _, err := s.PeekSetupToken(ctx, hash); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("peek after use: %v", err)
	}
	var verified sql.NullTime
	if err := db.QueryRow(`SELECT verified_at FROM channel_identities WHERE id=$1`, a.ID).Scan(&verified); err != nil {
		t.Fatal(err)
	}
	if !verified.Valid {
		t.Fatal("verified_at not set on link")
	}

	// The same number can't be taken over by a different account, and the
	// failed attempt leaves its token unused (transaction rolled back).
	_, hash2, err := NewSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSetupToken(ctx, a.ID, hash2, time.Now().Add(SetupTokenTTL)); err != nil {
		t.Fatal(err)
	}
	other := newUser(t, db)
	if _, err := s.ConsumeSetupTokenAndLink(ctx, hash2, other); !errors.Is(err, ErrIdentityTaken) {
		t.Fatalf("takeover: %v", err)
	}
	if _, _, err := s.PeekSetupToken(ctx, hash2); err != nil {
		t.Fatalf("rolled-back token should still be usable by the owner: %v", err)
	}
	// Re-linking to the same account is allowed.
	if _, err := s.ConsumeSetupTokenAndLink(ctx, hash2, user); err != nil {
		t.Fatalf("relink same user: %v", err)
	}
}

func TestPGExpiredTokenRejected(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	a, err := s.UpsertIdentity(ctx, IMessage, "+15559876543")
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := NewSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSetupToken(ctx, a.ID, hash, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PeekSetupToken(ctx, hash); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("peek expired: %v", err)
	}
	if _, err := s.ConsumeSetupTokenAndLink(ctx, hash, newUser(t, db)); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("consume expired: %v", err)
	}
}

func TestPGConcurrentLinkOnlyOneWins(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	a, err := s.UpsertIdentity(ctx, IMessage, "+15550001111")
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := NewSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSetupToken(ctx, a.ID, hash, time.Now().Add(SetupTokenTTL)); err != nil {
		t.Fatal(err)
	}
	user := newUser(t, db)

	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ConsumeSetupTokenAndLink(ctx, hash, user)
			switch {
			case err == nil:
				mu.Lock()
				wins++
				mu.Unlock()
			case errors.Is(err, ErrTokenInvalid):
				// the expected loss: the link was already used
			default:
				t.Errorf("unexpected error from a losing racer: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d concurrent requests used the same link", wins)
	}
}

func TestPGConversationRoundTripAndClaim(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	a, err := s.UpsertIdentity(ctx, IMessage, "+15552223333")
	if err != nil {
		t.Fatal(err)
	}

	// Fresh identity reads as idle with nothing pending.
	c, err := s.GetConversation(ctx, a.ID)
	if err != nil || c.State != StateIdle || c.Pending != nil {
		t.Fatalf("fresh: %+v %v", c, err)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	c.State = StateAwaitingConfirm
	c.PendingName = "Aura"
	c.TermsAcceptedAt = &now
	c.Pending = &PendingDraft{Source: "laundry", QuoteCents: 1200, QuoteMinutes: 30}
	c.Pending.Draft.Title = "Laundry pickup"
	if err := s.SaveConversation(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetConversation(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateAwaitingConfirm || got.PendingName != "Aura" || got.Pending == nil ||
		got.Pending.Draft.Title != "Laundry pickup" || got.Pending.QuoteCents != 1200 || got.TermsAcceptedAt == nil {
		t.Fatalf("round trip: %+v", got)
	}

	// Consent is never cleared by a later save.
	got.TermsAcceptedAt = nil
	if err := s.SaveConversation(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, err := s.GetConversation(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.TermsAcceptedAt == nil {
		t.Fatal("terms_accepted_at was cleared")
	}

	// Eight racers, one claim.
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, ok, err := s.ClaimForPosting(ctx, a.ID, StateAwaitingConfirm)
			if err != nil {
				t.Errorf("claim racer: %v", err)
				return
			}
			if ok && p.Draft.Title == "Laundry pickup" {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d claims won", wins)
	}
	if c2, err := s.GetConversation(ctx, a.ID); err != nil || c2.State != StatePosting {
		t.Fatalf("state after claim: %s", c2.State)
	}
	// A fresh claim is not stealable; a stale one (crash mid-post) is.
	if _, ok, err := s.ClaimForPosting(ctx, a.ID, StateAwaitingConfirm); err != nil || ok {
		t.Fatal("claimed a conversation that is mid-post")
	}
	if _, err := db.Exec(`UPDATE channel_conversations SET updated_at = now() - interval '4 minutes' WHERE identity_id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimForPosting(ctx, a.ID, StateAwaitingConfirm); err != nil || !ok {
		t.Fatal("a stale claim should be recoverable")
	}

	// ConversationsForUser finds it once linked.
	user := newUser(t, db)
	_, hash, err := NewSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSetupToken(ctx, a.ID, hash, time.Now().Add(SetupTokenTTL)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeSetupTokenAndLink(ctx, hash, user); err != nil {
		t.Fatal(err)
	}
	convs, ids, err := s.ConversationsForUser(ctx, user)
	if err != nil || len(convs) != 1 || ids[0].ID != a.ID || convs[0].Pending == nil {
		t.Fatalf("for user: %d %v %v", len(convs), ids, err)
	}
}

func TestPGStatusTargetPrefersTheBookingThread(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	user := newUser(t, db)
	var task string
	if err := db.QueryRow(`INSERT INTO tasks DEFAULT VALUES RETURNING id`).Scan(&task); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := s.StatusTargetFor(ctx, user, task); err != nil || ok {
		t.Fatalf("app-only user must have no target: ok=%v err=%v", ok, err)
	}

	im, err := s.UpsertIdentity(ctx, IMessage, "+15550000001")
	if err != nil {
		t.Fatal(err)
	}
	wa, err := s.UpsertIdentity(ctx, WhatsApp, "+15550000002")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE channel_identities SET user_id = $1, verified_at = now()`, user); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.TouchConversation(ctx, im.ID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchConversation(ctx, wa.ID, now); err != nil {
		t.Fatal(err)
	} // WhatsApp is the most recent

	got, ok, err := s.StatusTargetFor(ctx, user, task)
	if err != nil || !ok || got.Identity.Channel != WhatsApp || got.LastInboundAt == nil {
		t.Fatalf("no booking thread yet → most recent number: %+v ok=%v err=%v", got, ok, err)
	}

	// The task was booked over iMessage → updates go there.
	if err := s.RecordOutbound(ctx, Outbound{Channel: IMessage, To: "+15550000001", Text: "Booked."}, "out-1", task); err != nil {
		t.Fatal(err)
	}
	got, ok, err = s.StatusTargetFor(ctx, user, task)
	if err != nil || !ok || got.Identity.Channel != IMessage || got.Identity.Address != "+15550000001" {
		t.Fatalf("booking thread should win: %+v ok=%v err=%v", got, ok, err)
	}
}

// D-16 R-3: account deletion removes the person's numbers and everything said
// over them — and nobody else's.
func TestPGPurgeUserRemovesOnlyThatPersonsChatData(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	me, other := newUser(t, db), newUser(t, db)

	mine, err := s.UpsertIdentity(ctx, IMessage, "+15550000101")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := s.UpsertIdentity(ctx, IMessage, "+15550000202")
	if err != nil {
		t.Fatal(err)
	}
	for id, u := range map[string]string{mine.ID: me, theirs.ID: other} {
		if _, err := db.Exec(`UPDATE channel_identities SET user_id = $2, verified_at = now() WHERE id = $1`, id, u); err != nil {
			t.Fatal(err)
		}
		if err := s.TouchConversation(ctx, id, time.Now()); err != nil {
			t.Fatal(err)
		}
		_, hash, err := NewSetupToken()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CreateSetupToken(ctx, id, hash, time.Now().Add(SetupTokenTTL)); err != nil {
			t.Fatal(err)
		}
	}
	// One inbound attached to my identity, one outbound to my number with no
	// identity_id (how outbound rows are stored), one for the other person.
	for _, m := range []Inbound{
		{Channel: IMessage, ProviderMessageID: "in-mine", From: "+15550000101", Text: "hi", ReceivedAt: time.Now()},
		{Channel: IMessage, ProviderMessageID: "in-theirs", From: "+15550000202", Text: "hi", ReceivedAt: time.Now()},
	} {
		if _, err := s.RecordInbound(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AttachMessage(ctx, IMessage, "in-mine", mine.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordOutbound(ctx, Outbound{Channel: IMessage, To: "+15550000101", Text: "Booked."}, "out-mine", ""); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := PurgeUserTx(ctx, tx, me)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("numbers purged = %d, want 1", n)
	}
	count := func(q string, args ...any) int {
		var c int
		if err := db.QueryRow(q, args...).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := count(`SELECT count(*) FROM channel_messages WHERE address = '+15550000101'`); c != 0 {
		t.Fatalf("%d of my messages survived", c)
	}
	if c := count(`SELECT count(*) FROM channel_identities WHERE user_id = $1`, me); c != 0 {
		t.Fatal("my number survived")
	}
	if c := count(`SELECT count(*) FROM channel_conversations WHERE identity_id = $1`, mine.ID); c != 0 {
		t.Fatal("my conversation survived")
	}
	if c := count(`SELECT count(*) FROM channel_setup_tokens WHERE identity_id = $1`, mine.ID); c != 0 {
		t.Fatal("my setup link survived")
	}
	if c := count(`SELECT count(*) FROM channel_messages WHERE address = '+15550000202'`); c != 1 {
		t.Fatal("someone else's message was deleted")
	}
	if c := count(`SELECT count(*) FROM channel_identities WHERE user_id = $1`, other); c != 1 {
		t.Fatal("someone else's number was deleted")
	}
}

// Account deletion must keep working on a database that predates the channel
// tables (production before go-live).
func TestPGPurgeUserWithoutChannelTablesIsANoop(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`DROP TABLE channel_setup_tokens, channel_conversations, channel_messages, channel_identities`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Error(err)
		}
	}()
	if n, err := PurgeUserTx(context.Background(), tx, newUser(t, db)); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

// The stored body is StoredText when it is set (setup links), Text otherwise.
func TestPGRecordOutboundStoresTheRedactedBody(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	out := Outbound{Channel: IMessage, To: "+15550000303", Text: "link https://x.test/channel/setup#t=SECRETTOKEN", StoredText: "link " + SetupLinkPlaceholder}
	if err := s.RecordOutbound(ctx, out, "out-redact", ""); err != nil {
		t.Fatal(err)
	}
	var body string
	if err := db.QueryRow(`SELECT body FROM channel_messages WHERE provider_message_id = 'out-redact'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "SECRETTOKEN") || body != "link "+SetupLinkPlaceholder {
		t.Fatalf("stored %q", body)
	}
}

// M3: the pending approval round-trips, and clearing it stores NULL (the
// column is a FK, so an empty string must never reach it).
func TestPGPendingExtensionRoundTrip(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	a, err := s.UpsertIdentity(ctx, IMessage, "+15550000404")
	if err != nil {
		t.Fatal(err)
	}
	var ext string
	if err := db.QueryRow(`INSERT INTO extension_requests DEFAULT VALUES RETURNING id`).Scan(&ext); err != nil {
		t.Fatal(err)
	}
	c := Conversation{IdentityID: a.ID, State: StateAwaitingApproval, PendingExtensionID: ext}
	if err := s.SaveConversation(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetConversation(ctx, a.ID)
	if err != nil || got.State != StateAwaitingApproval || got.PendingExtensionID != ext {
		t.Fatalf("%+v %v", got, err)
	}
	got.PendingExtensionID = ""
	got.State = got.ResumeState()
	if err := s.SaveConversation(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, err := s.GetConversation(ctx, a.ID)
	if err != nil || again.State != StateIdle || again.PendingExtensionID != "" {
		t.Fatalf("%+v %v", again, err)
	}
}

// Handoff reads: the thread comes back oldest first and bounded; held
// conversations are listed with a hint, never the number.
func TestPGThreadAndHandedOff(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	a, err := s.UpsertIdentity(ctx, WhatsApp, "+15550000505")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	for i, text := range []string{"hi", "laundry please", "talk to a human"} {
		if _, err := s.RecordInbound(ctx, Inbound{Channel: WhatsApp, ProviderMessageID: fmt.Sprintf("in-%d", i), From: "+15550000505", Text: text, ReceivedAt: base.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordOutbound(ctx, Outbound{Channel: WhatsApp, To: "+15550000505", Text: "Let me get a teammate"}, "out-1", ""); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.RecentMessages(ctx, WhatsApp, "+15550000505", 2)
	if err != nil || len(msgs) != 2 || msgs[0].Text != "talk to a human" || msgs[1].Direction != "outbound" {
		t.Fatalf("%+v %v", msgs, err)
	}
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateHandoff}); err != nil {
		t.Fatal(err)
	}
	held, err := s.HandedOff(ctx)
	if err != nil || len(held) != 1 || held[0].IdentityID != a.ID || held[0].PhoneHint != "•••• 0505" {
		t.Fatalf("%+v %v", held, err)
	}
	got, err := s.IdentityByID(ctx, a.ID)
	if err != nil || got.Address != "+15550000505" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.IdentityByID(ctx, "00000000-0000-0000-0000-000000000009"); !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("want ErrIdentityNotFound, got %v", err)
	}
}

// M3.2: the task list and the held message round-trip, clear to NULL, and
// an inbound message can be tagged with its task so an inline reply finds it.
func TestPGTaskChoiceAndMessageTaskRoundTrip(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	a, err := s.UpsertIdentity(ctx, IMessage, "+15550000505")
	if err != nil {
		t.Fatal(err)
	}
	var t1, t2 string
	if err := db.QueryRow(`INSERT INTO tasks DEFAULT VALUES RETURNING id`).Scan(&t1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO tasks DEFAULT VALUES RETURNING id`).Scan(&t2); err != nil {
		t.Fatal(err)
	}
	c := Conversation{IdentityID: a.ID, State: StateAwaitingTaskChoice, PendingTaskChoices: []string{t1, t2}, PendingRelayText: "leave it at the door"}
	if err := s.SaveConversation(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetConversation(ctx, a.ID)
	if err != nil || got.State != StateAwaitingTaskChoice || len(got.PendingTaskChoices) != 2 ||
		got.PendingTaskChoices[0] != t1 || got.PendingTaskChoices[1] != t2 || got.PendingRelayText != "leave it at the door" {
		t.Fatalf("%+v %v", got, err)
	}
	got.State, got.PendingTaskChoices, got.PendingRelayText = StateIdle, nil, ""
	if err := s.SaveConversation(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, err := s.GetConversation(ctx, a.ID)
	if err != nil || again.State != StateIdle || len(again.PendingTaskChoices) != 0 || again.PendingRelayText != "" {
		t.Fatalf("%+v %v", again, err)
	}
	var nulls int
	if err := db.QueryRow(`SELECT count(*) FROM channel_conversations WHERE identity_id = $1 AND pending_task_choices IS NULL AND pending_relay_text IS NULL`, a.ID).Scan(&nulls); err != nil || nulls != 1 {
		t.Fatalf("cleared fields must be NULL: %d %v", nulls, err)
	}

	// Inbound message → task tag → lookup.
	in := Inbound{Channel: IMessage, ProviderMessageID: "in-relay-1", From: "+15550000505", Text: "ok", ReceivedAt: time.Now()}
	if _, err := s.RecordInbound(ctx, in); err != nil {
		t.Fatal(err)
	}
	if tid, _, known, err := s.TaskForProviderMessage(ctx, IMessage, "+15550000505", "in-relay-1"); err != nil || tid != "" || !known {
		t.Fatalf("untagged message: %q %v", tid, err)
	}
	if err := s.SetMessageTask(ctx, IMessage, "in-relay-1", t2); err != nil {
		t.Fatal(err)
	}
	if tid, rl, known, err := s.TaskForProviderMessage(ctx, IMessage, "+15550000505", "in-relay-1"); err != nil || tid != t2 || rl || !known {
		t.Fatalf("%q %v", tid, err)
	}
	if tid, _, known, err := s.TaskForProviderMessage(ctx, IMessage, "+15550000505", "never-seen"); err != nil || tid != "" || known {
		t.Fatalf("unknown message: %q %v %v", tid, known, err)
	}
	// Someone else's thread: the same provider id is unknown there.
	if _, _, known, err := s.TaskForProviderMessage(ctx, IMessage, "+15550000999", "in-relay-1"); err != nil || known {
		t.Fatalf("foreign thread must not see the message: %v %v", known, err)
	}
	// A relayed supporter message is remembered as such; our own prompt is not.
	if err := s.RecordOutbound(ctx, Outbound{Channel: IMessage, To: "+15550000505", Text: "Maya · x: hi", Relay: true}, "out-relay", t2); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordOutbound(ctx, Outbound{Channel: IMessage, To: "+15550000505", Text: "Reply YES to approve"}, "out-prompt", t2); err != nil {
		t.Fatal(err)
	}
	if tid, rl, _, err := s.TaskForProviderMessage(ctx, IMessage, "+15550000505", "out-relay"); err != nil || tid != t2 || !rl {
		t.Fatalf("relay bubble: %q %v %v", tid, rl, err)
	}
	if tid, rl, _, err := s.TaskForProviderMessage(ctx, IMessage, "+15550000505", "out-prompt"); err != nil || tid != t2 || rl {
		t.Fatalf("prompt bubble: %q %v %v", tid, rl, err)
	}
}
