package channel

import (
	"context"
	"testing"
)

// The offer round-trips (ids only) and clears; the daily count is atomic,
// stops at the cap and starts again on a new day.
func TestPGDiscoveryStore(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	id, err := s.UpsertIdentity(ctx, IMessage, "+15550001111")
	if err != nil {
		t.Fatal(err)
	}
	if o, err := s.GetDiscovery(ctx, id.ID); err != nil || o != nil {
		t.Fatalf("empty: %+v %v", o, err)
	}
	want := &DiscoveryOffer{Kind: offerOptions, Words: []string{"laundromat near 14th"}, Options: [][]string{{"gA"}, {"gB", "gC"}}, Check: true, AtUnix: 42}
	if err := s.SaveDiscovery(ctx, id.ID, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDiscovery(ctx, id.ID)
	if err != nil || got == nil || len(got.Options) != 2 || got.Options[1][1] != "gC" || !got.Check || got.Words[0] != "laundromat near 14th" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	for i := 1; i <= 3; i++ {
		ok, err := s.TakePlacesCall(ctx, id.ID, "2026-11-04", 2)
		if err != nil || ok != (i <= 2) {
			t.Fatalf("call %d: %v %v", i, ok, err)
		}
	}
	if ok, err := s.TakePlacesCall(ctx, id.ID, "2026-11-05", 2); err != nil || !ok {
		t.Fatalf("new day: %v %v", ok, err)
	}
	// The count never touched the offer, and clearing it keeps the count.
	if got, _ := s.GetDiscovery(ctx, id.ID); got == nil {
		t.Fatal("offer lost by the count")
	}
	if err := s.SaveDiscovery(ctx, id.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetDiscovery(ctx, id.ID); got != nil {
		t.Fatalf("not cleared: %+v", got)
	}
	if ok, _ := s.TakePlacesCall(ctx, id.ID, "2026-11-05", 2); !ok {
		t.Fatal("second call of the new day refused")
	}
	if ok, _ := s.TakePlacesCall(ctx, id.ID, "2026-11-05", 2); ok {
		t.Fatal("third call of the new day allowed")
	}
	// The peek counts nothing.
	for day, want := range map[string]int{"2026-11-05": 0, "2026-11-04": 2, "2026-11-06": 2} {
		if n, err := s.PlacesCallsLeft(ctx, id.ID, day, 2); err != nil || n != want {
			t.Fatalf("left %s: %d %v, want %d", day, n, err, want)
		}
	}
	if n, _ := s.PlacesCallsLeft(ctx, "00000000-0000-0000-0000-000000000000", "2026-11-05", 2); n != 2 {
		t.Fatalf("no row: %d", n)
	}
}

// The web search count is its own: atomic, capped, new each day, and it
// leaves the Places count and the offer alone.
func TestPGWebSearchQuota(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	id, err := s.UpsertIdentity(ctx, IMessage, "+15550002222")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveDiscovery(ctx, id.ID, &DiscoveryOffer{Kind: offerOptions, Options: [][]string{{"gA"}}, AtUnix: 1}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TakePlacesCall(ctx, id.ID, "2026-11-06", 5); err != nil || !ok {
		t.Fatal(ok, err)
	}
	for i := 1; i <= 3; i++ {
		ok, err := s.TakeWebSearch(ctx, id.ID, "2026-11-06", 2)
		if err != nil || ok != (i <= 2) {
			t.Fatalf("search %d: %v %v", i, ok, err)
		}
	}
	if ok, err := s.TakeWebSearch(ctx, id.ID, "2026-11-07", 2); err != nil || !ok {
		t.Fatalf("new day: %v %v", ok, err)
	}
	if left, err := s.PlacesCallsLeft(ctx, id.ID, "2026-11-06", 5); err != nil || left != 4 {
		t.Fatalf("places count touched: %d %v", left, err)
	}
	if o, _ := s.GetDiscovery(ctx, id.ID); o == nil || len(o.Options) != 1 {
		t.Fatal("offer lost")
	}
}
