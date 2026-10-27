package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"hora-auth/internal/channel/llm"
)

// DB-backed tests for PGStore. They run only when CHANNEL_TEST_DATABASE_URL
// points at a THROWAWAY database: each run drops and recreates the channel
// tables plus minimal users/tasks/extension_requests stubs, then applies the
// real migration file. Never point this at Supabase.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("CHANNEL_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("REQUIRE_DB_TESTS") == "1" {
			t.Fatal("CHANNEL_TEST_DATABASE_URL not set (REQUIRE_DB_TESTS=1: a DB-backed test may not skip)")
		}
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
		CREATE TABLE users (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text);
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
	for _, f := range []string{"20261022120000_channel_approval_columns.sql", "20261022130000_channel_approval_question_pid.sql", "20261024120000_channel_cancel_by_chat.sql", "20261024210000_channel_handoff_timeout.sql"} {
		mig, err := os.ReadFile(filepath.Join("..", "..", "..", "supabase", "migrations", f))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(mig)); err != nil {
			t.Fatalf("migration %s: %v", f, err)
		}
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
	// A full-row save cannot end the question (a stale snapshot must not
	// disarm it); ClearApproval does.
	got.PendingExtensionID = ""
	got.State = got.ResumeState()
	if err := s.SaveConversation(ctx, got); err != nil {
		t.Fatal(err)
	}
	if kept, _ := s.GetConversation(ctx, a.ID); kept.State != StateAwaitingApproval || kept.PendingExtensionID != ext {
		t.Fatalf("a full-row save disarmed the question: %+v", kept)
	}
	if ok, err := s.ClearApproval(ctx, a.ID, ext, StateIdle, time.Now()); err != nil || !ok {
		t.Fatalf("clear: %v %v", ok, err)
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

	// The ops Chats ledger: every conversation, state and hint, no number,
	// no text; the linked account's name when there is one.
	convs, err := s.Conversations(ctx, 50)
	if err != nil || len(convs) != 1 {
		t.Fatalf("%+v %v", convs, err)
	}
	if c := convs[0]; c.IdentityID != a.ID || c.State != string(StateHandoff) || c.PhoneHint != "•••• 0505" ||
		c.HandoffActivityAt == nil || c.UserName != "" {
		t.Fatalf("conversation row: %+v", c)
	}
	var uid string
	if err := db.QueryRow(`INSERT INTO users (name) VALUES ('Rita') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE channel_identities SET user_id = $1 WHERE id = $2`, uid, a.ID); err != nil {
		t.Fatal(err)
	}
	convs, err = s.Conversations(ctx, 50)
	if err != nil || len(convs) != 1 || convs[0].UserName != "Rita" || convs[0].UserID != uid {
		t.Fatalf("%+v %v", convs, err)
	}
	if b, _ := json.Marshal(msgs[0]); !strings.Contains(string(b), `"direction":"inbound"`) {
		t.Fatalf("ThreadMessage JSON: %s", b)
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

// D-17: the supporter's last relayed line on the thread, scoped by address
// and time; our own prompts are not it.
func TestPGLastRelayed(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	var t1 string
	if err := db.QueryRow(`INSERT INTO tasks DEFAULT VALUES RETURNING id`).Scan(&t1); err != nil {
		t.Fatal(err)
	}
	addr := "+15550000606"
	if rl, err := s.LastRelayed(ctx, IMessage, addr, time.Now().Add(-time.Hour), "now-msg"); err != nil || rl.OK {
		t.Fatalf("empty thread: %+v %v", rl, err)
	}
	if err := s.RecordOutbound(ctx, Outbound{Channel: IMessage, To: addr, Text: "Reply YES to approve"}, "p1", t1); err != nil {
		t.Fatal(err)
	}
	if rl, _ := s.LastRelayed(ctx, IMessage, addr, time.Now().Add(-time.Hour), "now-msg"); rl.OK {
		t.Fatal("our own prompt is not a supporter line")
	}
	if err := s.RecordOutbound(ctx, Outbound{Channel: IMessage, To: addr, Text: "Sam · Bagel run: what kind?", Relay: true}, "r1", t1); err != nil {
		t.Fatal(err)
	}
	rl, err := s.LastRelayed(ctx, IMessage, addr, time.Now().Add(-time.Hour), "now-msg")
	if err != nil || !rl.OK || rl.Text != "Sam · Bagel run: what kind?" || rl.TaskID != t1 || time.Since(rl.At) > time.Minute || rl.Answered {
		t.Fatalf("%+v %v", rl, err)
	}
	if rl, _ := s.LastRelayed(ctx, IMessage, "+15550000999", time.Now().Add(-time.Hour), "now-msg"); rl.OK {
		t.Fatal("another thread must not see it")
	}
	if rl, _ := s.LastRelayed(ctx, IMessage, addr, time.Now().Add(time.Minute), "now-msg"); rl.OK {
		t.Fatal("outside the window")
	}
	// Once the person has written, the line counts as answered.
	if _, err := s.RecordInbound(ctx, Inbound{Channel: IMessage, ProviderMessageID: "ans-1", From: addr, Text: "everything", ReceivedAt: time.Now().Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if rl, _ := s.LastRelayed(ctx, IMessage, addr, time.Now().Add(-time.Hour), "now-msg"); !rl.OK || !rl.Answered {
		t.Fatalf("answered: %+v", rl)
	}
	// F1: the message being routed is already on record (AttachMessage runs
	// before routing) and must not count as the answer. Fresh thread.
	addr2 := "+15550000607"
	if err := s.RecordOutbound(ctx, Outbound{Channel: IMessage, To: addr2, Text: "Sam · Bagel run: toasted?", Relay: true}, "r2", t1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordInbound(ctx, Inbound{Channel: IMessage, ProviderMessageID: "cur-1", From: addr2, Text: "yes", ReceivedAt: time.Now().Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if rl, _ := s.LastRelayed(ctx, IMessage, addr2, time.Now().Add(-time.Hour), "cur-1"); !rl.OK || rl.Answered || rl.Text != "Sam · Bagel run: toasted?" {
		t.Fatalf("the current message must not count as the answer: %+v", rl)
	}
	if rl, _ := s.LastRelayed(ctx, IMessage, addr2, time.Now().Add(-time.Hour), "other"); !rl.Answered {
		t.Fatalf("any other inbound after it does: %+v", rl)
	}
}

// M3.2c: the held approval question round-trips and clears to NULL.
func TestPGDeferredApprovalRoundTrip(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	a, err := s.UpsertIdentity(ctx, IMessage, "+15550000707")
	if err != nil {
		t.Fatal(err)
	}
	d := DeferredApproval{UserID: "u", TaskID: "t", ExtensionID: "e", Type: evBudgetRequested, Approval: PendingApproval{ID: "e", Kind: "budget", Cents: 1000}, TimeoutMinutes: 5, AtUnix: 1}
	for _, h := range []DeferredApproval{d, d, {UserID: "u", TaskID: "t", ExtensionID: "f", Type: evTimeRequested, AtUnix: 2}} {
		if err := s.HoldApproval(ctx, a.ID, h); err != nil { // the repeat is a no-op
			t.Fatal(err)
		}
	}
	got, err := s.GetConversation(ctx, a.ID)
	if err != nil || len(got.Deferred) != 2 || got.Deferred[0].ExtensionID != "e" || got.Deferred[0].Approval.Cents != 1000 || got.Deferred[1].ExtensionID != "f" {
		t.Fatalf("%+v %v", got, err)
	}
	if err := s.RemoveHold(ctx, a.ID, "f"); err != nil {
		t.Fatal(err)
	}
	// A full-row save — a booking's, say — leaves the hold alone.
	got.Deferred, got.State = nil, StateHandoff
	if err := s.SaveConversation(ctx, got); err != nil {
		t.Fatal(err)
	}
	if got, err = s.GetConversation(ctx, a.ID); err != nil || len(got.Deferred) != 1 || got.State != StateHandoff {
		t.Fatalf("save must not touch the hold: %+v %v", got, err)
	}
	if err := s.RemoveHold(ctx, a.ID, "e"); err != nil {
		t.Fatal(err)
	}
	var isNull bool
	if err := db.QueryRow(`SELECT deferred_approval IS NULL FROM channel_conversations WHERE identity_id = $1`, a.ID).Scan(&isNull); err != nil || !isNull {
		t.Fatalf("cleared field must be NULL: %v %v", isNull, err)
	}
}

// M3.2c rounds 2–3: the claim is one statement — state and request set,
// armed-at = ArmedUnconfirmed, the hold KEPT — only from a state the
// question may interrupt, and only once; ConfirmArmed stamps the time and
// drops the hold; ClearApproval is a compare-and-set on the request. A
// draft underneath survives all three.
func TestPGClaimHold(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	a, err := s.UpsertIdentity(ctx, IMessage, "+15550000708")
	if err != nil {
		t.Fatal(err)
	}
	var ext, ext2 string
	if err := db.QueryRow(`INSERT INTO extension_requests DEFAULT VALUES RETURNING id`).Scan(&ext); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO extension_requests DEFAULT VALUES RETURNING id`).Scan(&ext2); err != nil {
		t.Fatal(err)
	}
	draft := &PendingDraft{Draft: llm.TaskDraft{Title: "Laundry"}, QuoteCents: 2700}
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateHandoff, Pending: draft}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{ext, ext2} {
		if err := s.HoldApproval(ctx, a.ID, DeferredApproval{UserID: "u", TaskID: "t", ExtensionID: e, AtUnix: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// Not from a handoff.
	if ok, err := s.ClaimHold(ctx, a.ID, ext, "", time.Now()); err != nil || ok {
		t.Fatalf("claimed from handoff: %v %v", ok, err)
	}
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateAwaitingConfirm, Pending: draft}); err != nil {
		t.Fatal(err)
	}
	// Eight concurrent claims: one wins.
	var wg sync.WaitGroup
	var wins int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := s.ClaimHold(ctx, a.ID, ext, "", time.Now()); err == nil && ok {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("claims won: %d", wins)
	}
	got, err := s.GetConversation(ctx, a.ID)
	if err != nil || got.State != StateAwaitingApproval || got.PendingExtensionID != ext || !got.Unconfirmed() || got.ApprovalClaimedAt == nil ||
		len(got.Deferred) != 2 || got.Pending == nil || got.Pending.QuoteCents != 2700 {
		t.Fatalf("claimed: %+v %v", got, err)
	}
	// The claimed request's hold cannot be removed (it is the re-send
	// source); the other can.
	if err := s.RemoveHold(ctx, a.ID, ext); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); len(got.Deferred) != 2 {
		t.Fatalf("armed hold removed: %+v", got)
	}
	// A stale full-row save (a handler that read its snapshot before the
	// claim) does not disarm the question — nor restamp it when the
	// snapshot is itself in awaiting_approval; a touch or a hold write
	// leaves the claim time alone.
	claimedAt := *got.ApprovalClaimedAt
	stale := Conversation{IdentityID: a.ID, State: StateAwaitingConfirm, Pending: draft}
	if err := s.SaveConversation(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); got.State != StateAwaitingApproval || got.PendingExtensionID != ext || !got.Unconfirmed() || got.ApprovalClaimedAt == nil {
		t.Fatalf("stale save disarmed the question: %+v", got)
	}
	same := Conversation{IdentityID: a.ID, State: StateAwaitingApproval, PendingExtensionID: ext, Pending: draft} // no stamps in the snapshot
	if err := s.SaveConversation(ctx, same); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchConversation(ctx, a.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.HoldApproval(ctx, a.ID, DeferredApproval{UserID: "u", TaskID: "t", ExtensionID: ext2, AtUnix: 9}); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); !got.Unconfirmed() || got.ApprovalClaimedAt == nil || !got.ApprovalClaimedAt.Equal(claimedAt) {
		t.Fatalf("same-state save / touch / hold changed the claim: %+v", got)
	}
	// The re-claim is a compare-and-set on the claim time.
	if ok, err := s.ReclaimUnconfirmed(ctx, a.ID, ext, ptrTime(claimedAt.Add(time.Second)), time.Now()); err != nil || ok {
		t.Fatalf("reclaim with a stale view: %v %v", ok, err)
	}
	renewed := time.Now().Truncate(time.Microsecond)
	if ok, err := s.ReclaimUnconfirmed(ctx, a.ID, ext, &claimedAt, renewed); err != nil || !ok {
		t.Fatalf("reclaim: %v %v", ok, err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); got.ApprovalClaimedAt == nil || !got.ApprovalClaimedAt.Equal(renewed) {
		t.Fatalf("%+v", got)
	}
	// A handoff may leave awaiting_approval through a full-row save.
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateHandoff, Pending: draft}); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); got.State != StateHandoff || got.PendingExtensionID != "" || got.ApprovalArmedAt != nil || got.ApprovalClaimedAt != nil {
		t.Fatalf("handoff: %+v", got)
	}
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateAwaitingConfirm, Pending: draft}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimHold(ctx, a.ID, ext, "", time.Now()); err != nil || !ok {
		t.Fatalf("re-claim after handoff: %v %v", ok, err)
	}
	// Not while another question is armed.
	if ok, _ := s.ClaimHold(ctx, a.ID, ext2, "", time.Now()); ok {
		t.Fatal("claimed while another question is armed")
	}
	// Confirmed: stamped, hold gone, the other hold and the draft stay.
	at := time.Now().Truncate(time.Microsecond)
	if ok, err := s.ConfirmArmed(ctx, a.ID, ext, at, time.Now(), ""); err != nil || !ok {
		t.Fatalf("confirm: %v %v", ok, err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); got.Unconfirmed() || !got.ApprovalArmedAt.Equal(at) || len(got.Deferred) != 1 || got.Deferred[0].ExtensionID != ext2 || got.Pending == nil {
		t.Fatalf("confirmed: %+v", got)
	}
	// Confirming a request that is not the armed one changes nothing;
	// removing the armed one's hold is refused; re-holding it is a no-op.
	if ok, err := s.ConfirmArmed(ctx, a.ID, ext2, time.Now(), time.Now(), ""); err != nil || ok {
		t.Fatalf("stray confirm: %v %v", ok, err)
	}
	if err := s.HoldApproval(ctx, a.ID, DeferredApproval{UserID: "u", TaskID: "t", ExtensionID: ext, AtUnix: 3}); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); len(got.Deferred) != 1 {
		t.Fatalf("armed request re-held: %+v", got)
	}
	if got, _ = s.GetConversation(ctx, a.ID); !got.ApprovalArmedAt.Equal(at) || len(got.Deferred) != 1 {
		t.Fatalf("stray confirm: %+v", got)
	}
	// Clear: only for the armed request; the draft comes back.
	if ok, err := s.ClearApproval(ctx, a.ID, ext2, StateAwaitingConfirm, time.Now()); err != nil || ok {
		t.Fatalf("cleared the wrong request: %v %v", ok, err)
	}
	if ok, err := s.ClearApproval(ctx, a.ID, ext, StateAwaitingConfirm, time.Now()); err != nil || !ok {
		t.Fatalf("clear: %v %v", ok, err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); got.State != StateAwaitingConfirm || got.PendingExtensionID != "" || got.ApprovalArmedAt != nil || got.ApprovalClaimedAt != nil || got.Pending == nil || len(got.Deferred) != 1 {
		t.Fatalf("cleared: %+v", got)
	}
	// The SQL state list and canArmApproval agree.
	for _, st := range []State{StateIdle, StateAwaitingConfirm, StateAwaitingTaskChoice, StateAwaitingBudget, StateAwaitingName, StateAwaitingApproval, StateHandoff, StatePosting, StateAwaitingSetup} {
		if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: st, Pending: draft, PendingExtensionID: ""}); err != nil {
			t.Fatal(err)
		}
		ok, err := s.ClaimHold(ctx, a.ID, ext2, "", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if ok != canArmApproval(st) {
			t.Fatalf("state %s: SQL %v, canArmApproval %v", st, ok, canArmApproval(st))
		}
		if ok {
			if _, err := s.ClearApproval(ctx, a.ID, ext2, st, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
	}
	// LastInboundAt is read with the row.
	when := time.Now().Truncate(time.Microsecond)
	if err := s.TouchConversation(ctx, a.ID, when); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); got.LastInboundAt == nil || !got.LastInboundAt.Equal(when) {
		t.Fatalf("%+v", got)
	}
}

// M3.2d columns: question_shown_at only moves forward; the armed request's
// task; last_outbound_at, and its value captured when the question is
// confirmed sent; ClearApproval clears the approval ones and moves the
// shown time.
func TestPGM32dColumns(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	a, err := s.UpsertIdentity(ctx, IMessage, "+15550000709")
	if err != nil {
		t.Fatal(err)
	}
	var ext string
	if err := db.QueryRow(`INSERT INTO extension_requests DEFAULT VALUES RETURNING id`).Scan(&ext); err != nil {
		t.Fatal(err)
	}
	t1 := time.Now().Truncate(time.Microsecond)
	t0 := t1.Add(-time.Minute)
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateAwaitingConfirm, QuestionShownAt: &t1}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateAwaitingConfirm, QuestionShownAt: &t0}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetConversation(ctx, a.ID)
	if err != nil || got.QuestionShownAt == nil || !got.QuestionShownAt.Equal(t1) {
		t.Fatalf("shown time moved back: %+v %v", got, err)
	}
	if err := s.HoldApproval(ctx, a.ID, DeferredApproval{UserID: "u", TaskID: "t", ExtensionID: ext, AtUnix: 1}); err != nil {
		t.Fatal(err)
	}
	task := "00000000-0000-0000-0000-0000000000aa"
	if ok, err := s.ClaimHold(ctx, a.ID, ext, task, time.Now()); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	q, err := s.TouchOutbound(ctx, IMessage, a.Address) // the question is sent
	if err != nil || q.IsZero() {
		t.Fatalf("touch: %v %v", q, err)
	}
	if _, err := s.TouchOutbound(ctx, IMessage, a.Address); err != nil { // another message, sent between the question and its confirm
		t.Fatal(err)
	}
	if ok, err := s.ConfirmArmed(ctx, a.ID, ext, time.Now(), q, "pid-q"); err != nil || !ok {
		t.Fatalf("confirm: %v %v", ok, err)
	}
	got, _ = s.GetConversation(ctx, a.ID)
	if got.ApprovalTaskID != task || got.ApprovalQuestionOutAt == nil || !got.ApprovalQuestionOutAt.Equal(q) {
		t.Fatalf("after confirm: %+v", got)
	}
	if questionIsLast(got) {
		t.Fatal("the message sent between the question and its confirm must count (the question's own stamp is kept)")
	}
	// Unknown address: no row, zero stamp, no error.
	if z, err := s.TouchOutbound(ctx, IMessage, "+15550009999"); err != nil || !z.IsZero() {
		t.Fatalf("unknown address: %v %v", z, err)
	}
	// MarkQuestionShown only moves forward.
	before := *got.QuestionShownAt
	if err := s.MarkQuestionShown(ctx, a.ID, before.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); !got.QuestionShownAt.Equal(before) {
		t.Fatalf("mark moved back: %+v", got)
	}
	// A stale same-state save keeps the approval fields.
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateAwaitingApproval, PendingExtensionID: ext}); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); got.ApprovalTaskID != task || got.ApprovalQuestionOutAt == nil {
		t.Fatalf("stale save wiped the approval fields: %+v", got)
	}
	t2 := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	if ok, err := s.ClearApproval(ctx, a.ID, ext, StateAwaitingConfirm, t2); err != nil || !ok {
		t.Fatalf("clear: %v %v", ok, err)
	}
	got, _ = s.GetConversation(ctx, a.ID)
	if got.ApprovalTaskID != "" || got.ApprovalQuestionOutAt != nil || got.QuestionShownAt == nil || !got.QuestionShownAt.Equal(t2) || got.LastOutboundAt == nil {
		t.Fatalf("after clear: %+v", got)
	}
}

// A handoff leaves the approval with its fields; a new claim starts with no
// captured question stamp.
func TestPGHandoffAndReclaimResetApprovalFields(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	a, err := s.UpsertIdentity(ctx, IMessage, "+15550000710")
	if err != nil {
		t.Fatal(err)
	}
	var ext string
	if err := db.QueryRow(`INSERT INTO extension_requests DEFAULT VALUES RETURNING id`).Scan(&ext); err != nil {
		t.Fatal(err)
	}
	task := "00000000-0000-0000-0000-0000000000bb"
	arm := func() {
		t.Helper()
		if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateIdle}); err != nil {
			t.Fatal(err)
		}
		if err := s.HoldApproval(ctx, a.ID, DeferredApproval{UserID: "u", TaskID: "t", ExtensionID: ext, AtUnix: 1}); err != nil {
			t.Fatal(err)
		}
		if ok, err := s.ClaimHold(ctx, a.ID, ext, task, time.Now()); err != nil || !ok {
			t.Fatalf("claim: %v %v", ok, err)
		}
		q, err := s.TouchOutbound(ctx, IMessage, a.Address)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := s.ConfirmArmed(ctx, a.ID, ext, time.Now(), q, "pid-q"); err != nil || !ok {
			t.Fatalf("confirm: %v %v", ok, err)
		}
	}
	arm()
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateHandoff, ApprovalTaskID: task}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetConversation(ctx, a.ID)
	if got.State != StateHandoff || got.ApprovalTaskID != "" || got.ApprovalQuestionOutAt != nil || got.ApprovalArmedAt != nil || got.ApprovalQuestionPID != "" {
		t.Fatalf("handoff kept approval fields: %+v", got)
	}
	arm()
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateIdle}); err != nil { // stale: kept
		t.Fatal(err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); got.ApprovalQuestionPID != "pid-q" || got.ApprovalTaskID != task {
		t.Fatalf("a stale-snapshot save must keep the question's pid: %+v", got)
	}
	if _, err := s.ClearApproval(ctx, a.ID, ext, StateIdle, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.HoldApproval(ctx, a.ID, DeferredApproval{UserID: "u", TaskID: "t", ExtensionID: ext, AtUnix: 2}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimHold(ctx, a.ID, ext, task, time.Now()); err != nil || !ok {
		t.Fatalf("re-claim: %v %v", ok, err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); got.ApprovalQuestionOutAt != nil || got.ApprovalQuestionPID != "" || !got.Unconfirmed() {
		t.Fatalf("a new claim must not carry the old question stamp: %+v", got)
	}
}

// Round 4: the R-1 in-flight mark in SQL — MarkQuestionShown replaces it
// with the delivery time; a full-row save's GREATEST keeps it; the
// question's message id round-trips and is cleared with the approval.
func TestPGShownUnconfirmedAndQuestionPID(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	a, err := s.UpsertIdentity(ctx, IMessage, "+15550000711")
	if err != nil {
		t.Fatal(err)
	}
	mark := ShownUnconfirmed
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateAwaitingConfirm, QuestionShownAt: &mark}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateAwaitingConfirm, QuestionShownAt: &old}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetConversation(ctx, a.ID)
	if got.QuestionShownAt == nil || got.QuestionShownAt.Before(ShownUnconfirmed) {
		t.Fatalf("GREATEST must keep the in-flight mark: %+v", got.QuestionShownAt)
	}
	delivered := time.Now().Truncate(time.Microsecond)
	if err := s.MarkQuestionShown(ctx, a.ID, delivered); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); !got.QuestionShownAt.Equal(delivered) {
		t.Fatalf("the mark must be replaced by the delivery time: %+v", got.QuestionShownAt)
	}
	var ext string
	if err := db.QueryRow(`INSERT INTO extension_requests DEFAULT VALUES RETURNING id`).Scan(&ext); err != nil {
		t.Fatal(err)
	}
	if err := s.HoldApproval(ctx, a.ID, DeferredApproval{UserID: "u", TaskID: "t", ExtensionID: ext, AtUnix: 1}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimHold(ctx, a.ID, ext, "", time.Now()); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if ok, err := s.ConfirmArmed(ctx, a.ID, ext, time.Now(), time.Now(), "wamid.Q"); err != nil || !ok {
		t.Fatalf("confirm: %v %v", ok, err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); got.ApprovalQuestionPID != "wamid.Q" {
		t.Fatalf("pid: %+v", got)
	}
	if _, err := s.ClearApproval(ctx, a.ID, ext, StateIdle, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); got.ApprovalQuestionPID != "" {
		t.Fatalf("pid not cleared: %+v", got)
	}
}

// Cancel by chat: the new state and pending_cancel round-trip; leaving the
// cancel clears it.
func TestPGPendingCancel(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	a, err := s.UpsertIdentity(ctx, IMessage, "+15550000720")
	if err != nil {
		t.Fatal(err)
	}
	pc := &PendingCancel{TaskID: "00000000-0000-0000-0000-0000000000cc", Title: "Laundry pickup", QuotedCents: 1200,
		Choices: []TaskRef{{ID: "x", Title: "A"}}}
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateAwaitingCancel, PendingCancel: pc}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetConversation(ctx, a.ID)
	if err != nil || got.State != StateAwaitingCancel || got.PendingCancel == nil || got.PendingCancel.QuotedCents != 1200 || len(got.PendingCancel.Choices) != 1 {
		t.Fatalf("%+v err %v", got, err)
	}
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateIdle}); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); got.PendingCancel != nil {
		t.Fatalf("not cleared: %+v", got.PendingCancel)
	}
}

// Review 10/24: the cancel claim is compare-and-set (one of two YESes), an
// approval armed over a cancel question drops it, and a held draft is
// written only while that approval is the armed one.
func TestPGCancelClaimAndHoldDraft(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	a, err := s.UpsertIdentity(ctx, IMessage, "+15550000721")
	if err != nil {
		t.Fatal(err)
	}
	task := "00000000-0000-0000-0000-0000000000cd"
	pc := &PendingCancel{TaskID: task, Title: "Laundry pickup", QuotedCents: 1200, AskedAtUnix: time.Now().Unix()}
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateAwaitingCancel, PendingCancel: pc}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimCancel(ctx, a.ID, "00000000-0000-0000-0000-0000000000ee"); err != nil || ok {
		t.Fatalf("another task's claim: %v %v", ok, err)
	}
	if ok, err := s.ClaimCancel(ctx, a.ID, task); err != nil || !ok {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if ok, err := s.ClaimCancel(ctx, a.ID, task); err != nil || ok {
		t.Fatalf("second claim: %v %v", ok, err)
	}
	got, err := s.GetConversation(ctx, a.ID)
	if err != nil || got.State != StateIdle || got.PendingCancel != nil {
		t.Fatalf("after claim: %+v %v", got, err)
	}

	// An approval armed over a cancel question drops it.
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateAwaitingCancel, PendingCancel: pc}); err != nil {
		t.Fatal(err)
	}
	var ext string
	if err := db.QueryRow(`INSERT INTO extension_requests DEFAULT VALUES RETURNING id`).Scan(&ext); err != nil {
		t.Fatal(err)
	}
	if err := s.HoldApproval(ctx, a.ID, DeferredApproval{UserID: "u", TaskID: "t", ExtensionID: ext, AtUnix: 1}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimHold(ctx, a.ID, ext, "", time.Now()); err != nil || !ok {
		t.Fatalf("claim hold over a cancel question: %v %v", ok, err)
	}
	got, _ = s.GetConversation(ctx, a.ID)
	if got.State != StateAwaitingApproval || got.PendingCancel != nil {
		t.Fatalf("arming must drop the cancel: %s %+v", got.State, got.PendingCancel)
	}
	if ok, err := s.ClaimCancel(ctx, a.ID, task); err != nil || ok {
		t.Fatalf("a cancel YES after the approval armed: %v %v", ok, err)
	}

	// HoldDraft: only while that request is armed; nothing else written.
	d := PendingDraft{Source: "buy me a bagel", BudgetAskIndex: -1}
	if ok, err := s.HoldDraft(ctx, a.ID, "00000000-0000-0000-0000-0000000000aa", d); err != nil || ok {
		t.Fatalf("another request: %v %v", ok, err)
	}
	if ok, err := s.HoldDraft(ctx, a.ID, ext, d); err != nil || !ok {
		t.Fatalf("hold draft: %v %v", ok, err)
	}
	got, _ = s.GetConversation(ctx, a.ID)
	if got.State != StateAwaitingApproval || got.PendingExtensionID != ext || got.Pending == nil || got.Pending.Source != "buy me a bagel" {
		t.Fatalf("held draft: %+v", got)
	}
}

// Review 10/24 r3: AskCancel opens the cancel question only from idle or
// the cancel question itself, moves question_shown_at forward (NULL taken
// as unset), and never touches a row an approval holds.
func TestPGAskCancel(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	a, err := s.UpsertIdentity(ctx, IMessage, "+15550000722")
	if err != nil {
		t.Fatal(err)
	}
	pc := PendingCancel{TaskID: "00000000-0000-0000-0000-0000000000c1", Title: "Laundry pickup", QuotedCents: 1200, AskedAtUnix: 1}
	if ok, err := s.AskCancel(ctx, a.ID, pc, ShownUnconfirmed); err != nil || ok {
		t.Fatalf("no row: %v %v", ok, err)
	}
	if err := s.TouchConversation(ctx, a.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.AskCancel(ctx, a.ID, pc, ShownUnconfirmed); err != nil || !ok {
		t.Fatalf("from idle: %v %v", ok, err)
	}
	got, _ := s.GetConversation(ctx, a.ID)
	if got.State != StateAwaitingCancel || got.PendingCancel == nil || got.PendingCancel.QuotedCents != 1200 ||
		got.QuestionShownAt == nil || !got.QuestionShownAt.Equal(ShownUnconfirmed) {
		t.Fatalf("after ask: %+v", got)
	}
	pc.QuotedCents = 1550
	if ok, err := s.AskCancel(ctx, a.ID, pc, time.Now()); err != nil || !ok {
		t.Fatalf("re-ask: %v %v", ok, err)
	}
	if got, _ = s.GetConversation(ctx, a.ID); got.PendingCancel.QuotedCents != 1550 || !got.QuestionShownAt.Equal(ShownUnconfirmed) {
		t.Fatalf("re-ask must keep the later stamp (GREATEST): %+v", got)
	}
	var ext string
	if err := db.QueryRow(`INSERT INTO extension_requests DEFAULT VALUES RETURNING id`).Scan(&ext); err != nil {
		t.Fatal(err)
	}
	if err := s.HoldApproval(ctx, a.ID, DeferredApproval{UserID: "u", TaskID: "t", ExtensionID: ext, AtUnix: 1}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimHold(ctx, a.ID, ext, "", time.Now()); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	before, _ := s.GetConversation(ctx, a.ID)
	if ok, err := s.AskCancel(ctx, a.ID, pc, ShownUnconfirmed); err != nil || ok {
		t.Fatalf("over an armed approval: %v %v", ok, err)
	}
	after, _ := s.GetConversation(ctx, a.ID)
	if after.State != StateAwaitingApproval || after.PendingCancel != nil || after.PendingExtensionID != before.PendingExtensionID {
		t.Fatalf("the approval row was touched: %+v", after)
	}
}

// The hand-off clock: set when handed off, kept while handed off, moved by
// TouchHandoff, cleared on release; release is compare-and-set on it.
func TestPGHandoffActivity(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	a, err := s.UpsertIdentity(ctx, IMessage, "+15550000723")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateHandoff}); err != nil {
		t.Fatal(err)
	}
	c1, _ := s.GetConversation(ctx, a.ID)
	if c1.HandoffActivityAt == nil {
		t.Fatal("not set on hand-off")
	}
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateHandoff}); err != nil {
		t.Fatal(err)
	}
	c2, _ := s.GetConversation(ctx, a.ID)
	if !c2.HandoffActivityAt.Equal(*c1.HandoffActivityAt) {
		t.Fatal("a save while handed off must keep the clock")
	}
	if stale, err := s.StaleHandoffs(ctx, time.Now().Add(time.Minute)); err != nil || len(stale) == 0 {
		t.Fatalf("stale: %v %v", stale, err)
	}
	if ok, err := s.TouchHandoff(ctx, a.ID); err != nil || !ok {
		t.Fatalf("touch: %v %v", ok, err)
	}
	if ok, err := s.ReleaseHandoff(ctx, a.ID, ReleaseOpts{Resume: StateIdle, Seen: c1.HandoffActivityAt}); err != nil || ok {
		t.Fatalf("release with an old clock: %v %v", ok, err)
	}
	c3, _ := s.GetConversation(ctx, a.ID)
	if ok, err := s.ReleaseHandoff(ctx, a.ID, ReleaseOpts{Resume: StateIdle, Seen: c3.HandoffActivityAt}); err != nil || !ok {
		t.Fatalf("release: %v %v", ok, err)
	}
	c4, _ := s.GetConversation(ctx, a.ID)
	if c4.State != StateIdle || c4.HandoffActivityAt != nil {
		t.Fatalf("after release: %+v", c4)
	}
	// Handed off again: a fresh clock, not the old one.
	time.Sleep(10 * time.Millisecond)
	if err := s.SaveConversation(ctx, Conversation{IdentityID: a.ID, State: StateHandoff}); err != nil {
		t.Fatal(err)
	}
	c5, _ := s.GetConversation(ctx, a.ID)
	if c5.HandoffActivityAt == nil || !c5.HandoffActivityAt.After(*c3.HandoffActivityAt) {
		t.Fatalf("re-handoff clock: %v vs %v", c5.HandoffActivityAt, c3.HandoffActivityAt)
	}
	// An ops release goes through whatever the clock (AnyClock).
	if ok, err := s.ReleaseHandoff(ctx, a.ID, ReleaseOpts{Resume: StateIdle, AnyClock: true}); err != nil || !ok {
		t.Fatalf("any-clock release: %v %v", ok, err)
	}
	if ok, err := s.TouchHandoff(ctx, a.ID); err != nil || ok {
		t.Fatalf("touch when not handed off: %v %v", ok, err)
	}
	// No clock (handed off by an older build): started, not listed stale.
	if _, err := db.Exec(`UPDATE channel_conversations SET state = 'handoff', handoff_activity_at = NULL WHERE identity_id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.StartHandoffClocks(ctx); err != nil {
		t.Fatal(err)
	}
	c6, _ := s.GetConversation(ctx, a.ID)
	if c6.HandoffActivityAt == nil {
		t.Fatal("clock not started")
	}
}
