package main

import (
	"net/http"
	"testing"

	"hora-auth/internal/channel"
	"hora-auth/internal/channel/llm"
)

func TestDraftToCreateInput(t *testing.T) {
	in := draftToCreateInput(llm.TaskDraft{
		Title: "Deliver package", Category: "delivery", Description: "Heavy",
		Location1: "350 5th Ave", Location2: "200 W 79th St",
		Scheduled: true, ScheduledTime: "2026-10-21T14:00:00-04:00",
	}, channel.PostOptions{BudgetCents: 1500, AutoExtend: false})
	if in.LocationText != "350 5th Ave | 200 W 79th St" || in.IsImmediate || in.ScheduledAt != "2026-10-21T14:00:00-04:00" ||
		in.EstimatedMinutes != defaultChatMinutes || in.CreatedVia != "ai_parse" || !validCreatedVia[in.CreatedVia] {
		t.Fatalf("%+v", in)
	}
	// M3.5: the two consents the app collects on its form are sent explicitly,
	// never left to the server default.
	if in.PrepayAmountCents != 1500 || in.AutoExtendConsent == nil || *in.AutoExtendConsent {
		t.Fatalf("consents not carried: %+v", in)
	}
	asap := draftToCreateInput(llm.TaskDraft{Title: "x", Location1: "A", DurationMinutes: 60}, channel.PostOptions{AutoExtend: true})
	if asap.AutoExtendConsent == nil || !*asap.AutoExtendConsent || asap.PrepayAmountCents != 0 {
		t.Fatalf("%+v", asap)
	}
	if !asap.IsImmediate || asap.ScheduledAt != "" || asap.LocationText != "A" || asap.EstimatedMinutes != 60 {
		t.Fatalf("%+v", asap)
	}
}

func TestPostResultFrom(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   channel.PostOutcome
	}{
		{http.StatusCreated, `{"id":"t1"}`, channel.PostBooked},
		{http.StatusPaymentRequired, `{"error":"payment_method_required","message":"Add a card"}`, channel.PostNeedsCard},
		{http.StatusPaymentRequired, `{"error":"payment_authentication_required","task_id":"t2"}`, channel.PostNeeds3DS},
		{http.StatusPaymentRequired, `{"error":"payment_required","message":"declined"}`, channel.PostDeclined},
		{http.StatusForbidden, `{"error":"outstanding_balance","message":"owe"}`, channel.PostOwesBalance},
		{http.StatusServiceUnavailable, `{"error":"payments_unavailable"}`, channel.PostFailed},
		{http.StatusCreated, `{}`, channel.PostFailed}, // 201 without an id is not a booking
		{http.StatusInternalServerError, `not json`, channel.PostFailed},
	}
	for _, c := range cases {
		if got := postResultFrom(c.status, []byte(c.body)); got.Outcome != c.want {
			t.Errorf("%d %s → %v, want %v", c.status, c.body, got.Outcome, c.want)
		}
	}
}
