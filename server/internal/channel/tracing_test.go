package channel

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"hora-auth/internal/channel/llm"
	"hora-auth/internal/tracing"
)

// One inbound message is one trace (D-22): its steps are children of the
// message's span, the session is the conversation, and neither the sender's
// number nor one in the text is exported.
func TestOneTracePerInboundMessage(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	stop := tracing.StartWithExporter(exp)
	defer func() { _ = stop(context.Background()) }()

	g := newRig(llm.IntentNewTask)
	h := newHandler(g.ad, g.st, g.r)
	h.route(Inbound{Channel: IMessage, ProviderMessageID: "pm-77", From: me,
		Text: "laundry pickup at 12 Bedford Ave, call me 212-555-1234", ReceivedAt: time.Now()})
	if g.ad.last() == "" {
		t.Fatal("no reply")
	}
	id, err := g.ids.UpsertIdentity(context.Background(), IMessage, me)
	if err != nil {
		t.Fatal(err)
	}

	spans := exp.GetSpans()
	var root tracetest.SpanStub
	names := map[string]bool{}
	for _, s := range spans {
		names[s.Name] = true
		if s.Name == "channel.inbound" {
			root = s
		}
	}
	if !root.SpanContext.IsValid() {
		t.Fatalf("no message span among %v", names)
	}
	if !names["service_area.errand"] || !names["channel.send"] {
		t.Fatalf("steps missing: %v", names)
	}
	digits := strings.Trim(me, "+")
	for _, s := range spans {
		if s.SpanContext.TraceID() != root.SpanContext.TraceID() {
			t.Errorf("span %s is in another trace", s.Name)
		}
		a := map[string]string{}
		for _, kv := range s.Attributes {
			v := kv.Value.Emit()
			a[string(kv.Key)] = v
			// session.id is exempt here only because the in-memory store
			// spells identity ids "id-<number>"; production's are uuids.
			if kv.Key != "session.id" && (strings.Contains(v, digits) || strings.Contains(v, "555-1234")) {
				t.Errorf("span %s attribute %s carries a phone number: %q", s.Name, kv.Key, v)
			}
		}
		if a["session.id"] != id.ID || a["hora.inbound_message_id"] != "pm-77" {
			t.Errorf("span %s: session %q message %q, want %q / pm-77", s.Name, a["session.id"], a["hora.inbound_message_id"], id.ID)
		}
		if s.Name == "channel.inbound" {
			if a["input.value"] != "laundry pickup at 12 Bedford Ave, call me [phone]" || a["output.value"] == "" {
				t.Errorf("message span input %q output %q", a["input.value"], a["output.value"])
			}
		}
	}
}
