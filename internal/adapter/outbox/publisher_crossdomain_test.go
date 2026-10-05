// publisher_crossdomain_test.go — CHO-2040: the outbox publisher's EXPLICIT
// cross-domain request-topic allowlist.
//
// chora-consumption's outbox is domain-locked to chora.consumption.* — the ONE
// exception is the qgen crew's request lane chora.creation.ai_assist.started.v2
// (terraform m10-data-plane: "the only CROSS-SERVICE topic"). The allowlist is
// explicit + closed: any OTHER chora.creation.* topic keeps failing loud.
package outbox

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
)

func crossDomainEnvelope() events.Envelope {
	now := time.Date(2026, 7, 4, 13, 0, 0, 0, time.UTC)
	return events.Envelope{
		EventID:        "0197a001-aaaa-7bbb-8ccc-00000000000e",
		IdempotencyKey: "proofing_test.requested.0197a001",
		TenantID:       "0197a001-aaaa-7bbb-8ccc-000000000002",
		GCID:           "0197a001-aaaa-7bbb-8ccc-000000000003",
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    "00-11111111111111111111111111111111-2222222222222222-01",
		SourceProject:  "chora-489812",
		SourceService:  "chora-consumption",
		SchemaVersion:  2,
	}
}

func TestPublish_AllowsAiAssistStartedV2CrossDomainRequest(t *testing.T) {
	store := NewInMemoryStore()
	p := NewPublisher(PublisherConfig{Store: store, AggregateType: "proofing_test"})

	payload := map[string]any{
		"assist_id":           "0197a001-aaaa-7bbb-8ccc-000000000001",
		"tenant_id":           "0197a001-aaaa-7bbb-8ccc-000000000002",
		"author_gcid":         "0197a001-aaaa-7bbb-8ccc-000000000003",
		"content_type":        "mixed",
		"prompt":              "compose",
		"started_at":          time.Date(2026, 7, 4, 13, 0, 0, 0, time.UTC),
		"target_growth_edges": []string{"algebra.quadratic_roots"},
		"type_plan": []map[string]any{
			{"question_type": "mcq", "count": 4},
			{"question_type": "oe", "count": 2},
		},
		"operation": "compose", "intent": "new_question", "input_kind": "prompt",
	}
	if err := p.Publish("chora.creation.ai_assist.started.v2", crossDomainEnvelope(), payload); err != nil {
		t.Fatalf("allowlisted cross-domain request topic must publish, got %v", err)
	}

	rows, err := store.FetchPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("FetchPending: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Topic != "chora.creation.ai_assist.started.v2" {
		t.Fatalf("topic = %q", row.Topic)
	}
	// Payload must be the BINARY protobuf encoding (Schema-Registry-bound
	// topic) — a JSON body would deadletter forever. JSON starts with '{'.
	if len(row.Payload) == 0 || row.Payload[0] == '{' {
		t.Fatalf("payload must be binary protobuf, got %q...", string(row.Payload[:min(24, len(row.Payload))]))
	}
	// event_type derives the true domain segment — NOT a hardcoded
	// "consumption." prefix on a creation topic.
	if row.EventType != "creation.ai_assist.started" {
		t.Fatalf("event_type = %q, want creation.ai_assist.started", row.EventType)
	}
	// The batch/aggregate ref is the assist_id so audit/replay queries key
	// on the ai_assist request.
	if row.AggregateID != "0197a001-aaaa-7bbb-8ccc-000000000001" {
		t.Fatalf("aggregate_id = %q, want the assist_id", row.AggregateID)
	}
}

func TestPublish_OtherCreationTopicsStayForbidden(t *testing.T) {
	store := NewInMemoryStore()
	p := NewPublisher(PublisherConfig{Store: store})
	err := p.Publish("chora.creation.ai_assist.completed.v1", crossDomainEnvelope(), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "chora.consumption.") {
		t.Fatalf("non-allowlisted creation topic must fail the domain invariant, got %v", err)
	}
	err = p.Publish("chora.creation.atom.published.v1", crossDomainEnvelope(), map[string]any{})
	if err == nil {
		t.Fatalf("creation atom topic must stay forbidden")
	}
}

func TestDeriveEventType_ConsumptionTopicsUnchanged(t *testing.T) {
	if got := deriveEventType("chora.consumption.daily_dose.served.v1"); got != "consumption.daily_dose.served" {
		t.Fatalf("consumption derivation regressed: %q", got)
	}
}
