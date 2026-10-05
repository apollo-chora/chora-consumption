// outbox_in_tx_test.go — CHO-2129 AC4: the same-tx outbox enqueuer.
//
//   - PublishGrownInTx REQUIRES an ambient transaction (fail-loud: a silent
//     own-connection fallback would reintroduce the post-commit loss window);
//   - the row INSERT targets outbox_events with ON CONFLICT DO NOTHING (an
//     in-tx unique violation would poison the whole fold);
//   - the row content comes from outbox.BuildRow — byte-identical to the
//     post-commit Publisher path (binary payload for the schema-bound
//     weakness.grown.v1 topic included).
package pg

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
)

func grownTestEnvelope() events.Envelope {
	return events.Envelope{
		EventID:        "01970000-0000-7000-8000-00000000e001",
		IdempotencyKey: "chora.consumption.weakness.grown:edge-1:2026-06-28T08:30:00Z",
		TenantID:       pgTenantID,
		GCID:           pgUserGCID,
		OccurredAt:     time.Date(2026, 6, 28, 8, 0, 0, 0, time.UTC),
		PublishedAt:    time.Date(2026, 6, 28, 8, 0, 1, 0, time.UTC),
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		SourceProject:  events.SourceProject,
		SourceService:  events.SourceService,
		SchemaVersion:  1,
	}
}

func grownTestPayload() map[string]any {
	return map[string]any{
		"growth_edge_id":  "01970000-0000-7000-8000-00000000edg1",
		"tenant_id":       pgTenantID,
		"learner_gcid":    pgUserGCID,
		"concept_label":   "Multiplication Tables",
		"concept_key":     "multiplication-tables",
		"final_strength":  0.05,
		"recovery_source": "concept_key",
		"tags":            []string{"arithmetic"},
		"grown_at":        time.Date(2026, 6, 28, 8, 30, 0, 0, time.UTC),
	}
}

func TestTxOutbox_RequiresAmbientTransaction(t *testing.T) {
	o := NewTxOutbox("learner_weakness")
	err := o.PublishGrownInTx(context.Background(), events.TopicWeaknessGrown, grownTestEnvelope(), grownTestPayload())
	if err == nil {
		t.Fatal("PublishGrownInTx without an ambient tx must fail loud, got nil")
	}
	if !contains(err.Error(), "ambient transaction") {
		t.Fatalf("error should name the missing ambient tx, got %v", err)
	}
}

func TestTxOutbox_InsertsThroughAmbientQuerier(t *testing.T) {
	q := &stubQuerier{}
	ctx := WithAmbientQuerier(context.Background(), q)

	o := NewTxOutbox("learner_weakness")
	if err := o.PublishGrownInTx(ctx, events.TopicWeaknessGrown, grownTestEnvelope(), grownTestPayload()); err != nil {
		t.Fatalf("PublishGrownInTx: %v", err)
	}
	if len(q.execCalls) != 1 {
		t.Fatalf("exec calls = %d, want exactly 1 (the row INSERT on the ambient querier)", len(q.execCalls))
	}
	sql := q.execCalls[0]
	if !contains(sql, "INSERT INTO outbox_events") {
		t.Errorf("SQL = %q; want INSERT INTO outbox_events", sql)
	}
	if !contains(sql, "ON CONFLICT DO NOTHING") {
		t.Errorf("in-tx insert must be ON CONFLICT DO NOTHING (a raised unique violation poisons the fold): %q", sql)
	}
	if !contains(sql, "'pending'") {
		t.Errorf("row must land pending for the dispatcher: %q", sql)
	}
}

func TestTxOutbox_RowMatchesBuildRow(t *testing.T) {
	q := &stubQuerier{}
	ctx := WithAmbientQuerier(context.Background(), q)
	env, payload := grownTestEnvelope(), grownTestPayload()

	o := NewTxOutbox("learner_weakness")
	if err := o.PublishGrownInTx(ctx, events.TopicWeaknessGrown, env, payload); err != nil {
		t.Fatalf("PublishGrownInTx: %v", err)
	}
	want, err := outbox.BuildRow("learner_weakness", events.TopicWeaknessGrown, env, payload)
	if err != nil {
		t.Fatalf("BuildRow: %v", err)
	}
	args := q.execArgs[0]
	if len(args) != 11 {
		t.Fatalf("args = %d, want 11", len(args))
	}
	if args[0] != want.ID || args[3] != want.AggregateType || args[4] != want.AggregateID ||
		args[5] != want.EventType || args[6] != want.Topic {
		t.Fatalf("row identity mismatch: got id=%v type=%v aggID=%v eventType=%v topic=%v",
			args[0], args[3], args[4], args[5], args[6])
	}
	// The payload is the BuildRow body — binary protobuf for the schema-bound
	// grown topic, NOT a JSON re-marshal.
	gotPayload, ok := args[7].([]byte)
	if !ok {
		t.Fatalf("payload arg type %T, want []byte", args[7])
	}
	if string(gotPayload) != string(want.Payload) {
		t.Fatalf("payload bytes diverge from outbox.BuildRow (len %d vs %d)", len(gotPayload), len(want.Payload))
	}
	if len(want.Payload) == 0 {
		t.Fatal("BuildRow produced an empty payload — the grown topic should binary-encode")
	}
	// An invalid envelope (BuildRow validation) surfaces, no exec issued.
	bad := env
	bad.TenantID = ""
	q2 := &stubQuerier{}
	if err := o.PublishGrownInTx(WithAmbientQuerier(context.Background(), q2), events.TopicWeaknessGrown, bad, payload); err == nil {
		t.Fatal("invalid envelope must fail BuildRow validation")
	}
	if len(q2.execCalls) != 0 {
		t.Fatalf("no INSERT may run on a validation failure, got %d", len(q2.execCalls))
	}
}
