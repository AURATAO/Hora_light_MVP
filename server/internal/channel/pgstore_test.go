package channel

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
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
	t.Cleanup(func() { db.Close() })
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
	_ = db.QueryRow(`SELECT last_inbound_at FROM channel_conversations WHERE identity_id=$1`, a.ID).Scan(&last)
	if !last.Equal(later.Truncate(time.Microsecond)) && last.Sub(later).Abs() > time.Millisecond {
		t.Fatalf("last_inbound_at went backwards: %v", last)
	}

	// Token: peek does not consume; consume links once.
	tok, hash, _ := NewSetupToken()
	if err := s.CreateSetupToken(ctx, a.ID, hash, time.Now().Add(SetupTokenTTL)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.SetupTokenIssuedSince(ctx, a.ID, time.Now().Add(-time.Minute)); !ok {
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
	_ = db.QueryRow(`SELECT verified_at FROM channel_identities WHERE id=$1`, a.ID).Scan(&verified)
	if !verified.Valid {
		t.Fatal("verified_at not set on link")
	}

	// The same number can't be taken over by a different account, and the
	// failed attempt leaves its token unused (transaction rolled back).
	_, hash2, _ := NewSetupToken()
	_ = s.CreateSetupToken(ctx, a.ID, hash2, time.Now().Add(SetupTokenTTL))
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
	a, _ := s.UpsertIdentity(ctx, IMessage, "+15559876543")
	_, hash, _ := NewSetupToken()
	_ = s.CreateSetupToken(ctx, a.ID, hash, time.Now().Add(-time.Second))
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
	a, _ := s.UpsertIdentity(ctx, IMessage, "+15550001111")
	_, hash, _ := NewSetupToken()
	_ = s.CreateSetupToken(ctx, a.ID, hash, time.Now().Add(SetupTokenTTL))
	user := newUser(t, db)

	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ConsumeSetupTokenAndLink(ctx, hash, user); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
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
	a, _ := s.UpsertIdentity(ctx, IMessage, "+15552223333")

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
	got, _ := s.GetConversation(ctx, a.ID)
	if got.State != StateAwaitingConfirm || got.PendingName != "Aura" || got.Pending == nil ||
		got.Pending.Draft.Title != "Laundry pickup" || got.Pending.QuoteCents != 1200 || got.TermsAcceptedAt == nil {
		t.Fatalf("round trip: %+v", got)
	}

	// Consent is never cleared by a later save.
	got.TermsAcceptedAt = nil
	_ = s.SaveConversation(ctx, got)
	again, _ := s.GetConversation(ctx, a.ID)
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
			if p, ok, err := s.ClaimForPosting(ctx, a.ID, StateAwaitingConfirm); err == nil && ok && p.Draft.Title == "Laundry pickup" {
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
	if c2, _ := s.GetConversation(ctx, a.ID); c2.State != StatePosting {
		t.Fatalf("state after claim: %s", c2.State)
	}
	// A fresh claim is not stealable; a stale one (crash mid-post) is.
	if _, ok, _ := s.ClaimForPosting(ctx, a.ID, StateAwaitingConfirm); ok {
		t.Fatal("claimed a conversation that is mid-post")
	}
	_, _ = db.Exec(`UPDATE channel_conversations SET updated_at = now() - interval '4 minutes' WHERE identity_id=$1`, a.ID)
	if _, ok, _ := s.ClaimForPosting(ctx, a.ID, StateAwaitingConfirm); !ok {
		t.Fatal("a stale claim should be recoverable")
	}

	// ConversationsForUser finds it once linked.
	user := newUser(t, db)
	_, hash, _ := NewSetupToken()
	_ = s.CreateSetupToken(ctx, a.ID, hash, time.Now().Add(SetupTokenTTL))
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

	im, _ := s.UpsertIdentity(ctx, IMessage, "+15550000001")
	wa, _ := s.UpsertIdentity(ctx, WhatsApp, "+15550000002")
	if _, err := db.Exec(`UPDATE channel_identities SET user_id = $1, verified_at = now()`, user); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_ = s.TouchConversation(ctx, im.ID, now.Add(-time.Hour))
	_ = s.TouchConversation(ctx, wa.ID, now) // WhatsApp is the most recent

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
