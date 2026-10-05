// Package protomarshal_test verifies binary protobuf wire-format encoding for
// chora-consumption's outbox event payloads. RED tests written BEFORE the
// encoder lands per CLAUDE.md §development-execution + feedback_strict_tdd.
//
// Gap: outbox writer was persisting JSON-marshalled payload bytes that the
// BINARY schema encoding rejects at publish time with
// "Invalid binary proto message". Fix is producer-side: marshal to canonical
// proto wire bytes before the outbox row is written. Dispatcher passes bytes
// through unchanged.
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

// fixedEnvelope returns an envelope with deterministic values for byte-level
// assertions.
func fixedEnvelope() protomarshal.Envelope {
	t := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	return protomarshal.Envelope{
		EventID:        "01971a90-0000-7000-8000-000000000001",
		IdempotencyKey: "idemp-1",
		TenantID:       "tenant-1",
		GCID:           "gcid-phyllis",
		OccurredAt:     t,
		PublishedAt:    t.Add(time.Millisecond),
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		Tracestate:     "",
		SourceProject:  "chora-content",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
}

// TestMarshalDailyDoseServed_RoundTripsBinaryProto asserts the encoder
// produces wire-format protobuf bytes whose nested submessage parses
// without error and whose tag stream is well-formed.
func TestMarshalDailyDoseServed_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"learner_gcid": "gcid-phyllis",
		"atom_ids":     []string{"atom-1", "atom-2", "atom-3"},
		"size":         3,
		"served_at":    env.OccurredAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.daily_dose.served.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("MarshalPayload: empty bytes")
	}

	// Walk the produced wire bytes; every record must be a valid TLV.
	// Field 1 = envelope (length-delimited submessage), 2 = learner_gcid (str),
	// 3 = entries (length-delimited repeated), 4 = size (varint),
	// 5 = message (str), 6 = served_at (length-delimited Timestamp).
	seen := map[protowire.Number]bool{}
	rem := bz
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			t.Fatalf("invalid tag at offset %d", len(bz)-len(rem))
		}
		rem = rem[n:]
		seen[num] = true

		switch typ {
		case protowire.BytesType:
			_, n := protowire.ConsumeBytes(rem)
			if n < 0 {
				t.Fatalf("invalid length-delimited value for field %d", num)
			}
			rem = rem[n:]
		case protowire.VarintType:
			_, n := protowire.ConsumeVarint(rem)
			if n < 0 {
				t.Fatalf("invalid varint value for field %d", num)
			}
			rem = rem[n:]
		default:
			t.Fatalf("unexpected wire type %d for field %d", typ, num)
		}
	}

	// Required: envelope (1) + learner_gcid (2). Entries (3) is repeated and
	// optional in the typed sense, but present here because we set atom_ids.
	if !seen[1] {
		t.Fatal("missing envelope (field 1)")
	}
	if !seen[2] {
		t.Fatal("missing learner_gcid (field 2)")
	}
	if !seen[4] {
		t.Fatal("missing size (field 4)")
	}
}

// TestMarshalDailyDoseServed_EnvelopeIsWireCompatible asserts the nested
// envelope submessage decodes to the canonical chora.common.v1.EventEnvelope
// field layout (event_id=1 ... schema_version=11).
func TestMarshalDailyDoseServed_EnvelopeIsWireCompatible(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{"learner_gcid": env.GCID}

	bz, err := protomarshal.MarshalPayload("chora.consumption.daily_dose.served.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	// Read first top-level field — must be field 1 (envelope) bytes.
	num, typ, n := protowire.ConsumeTag(bz)
	if num != 1 || typ != protowire.BytesType || n < 0 {
		t.Fatalf("expected envelope tag (1, bytes), got num=%d typ=%d", num, typ)
	}
	envBytes, m := protowire.ConsumeBytes(bz[n:])
	if m < 0 {
		t.Fatal("invalid envelope bytes")
	}
	if len(envBytes) == 0 {
		t.Fatal("empty envelope bytes")
	}

	// Walk nested envelope fields; the producer MUST set event_id (1),
	// idempotency_key (2), tenant_id (3), gcid (4), occurred_at (5),
	// published_at (6), traceparent (7), source_project (9), source_service
	// (10), schema_version (11).
	seen := map[protowire.Number]bool{}
	rem := envBytes
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			t.Fatalf("invalid envelope inner tag")
		}
		rem = rem[n:]
		seen[num] = true
		switch typ {
		case protowire.BytesType:
			_, n := protowire.ConsumeBytes(rem)
			if n < 0 {
				t.Fatalf("invalid envelope inner bytes for field %d", num)
			}
			rem = rem[n:]
		case protowire.VarintType:
			_, n := protowire.ConsumeVarint(rem)
			if n < 0 {
				t.Fatalf("invalid envelope inner varint for field %d", num)
			}
			rem = rem[n:]
		}
	}

	required := []protowire.Number{1, 2, 3, 4, 5, 6, 7, 9, 10, 11}
	for _, want := range required {
		if !seen[want] {
			t.Fatalf("envelope: missing required field %d (seen: %v)", want, seen)
		}
	}
}

// TestMarshalCompanionChatTurnCompleted_RoundTripsBinaryProto verifies the
// chat-turn payload encodes to binary protobuf wire format matching the
// CompanionChatTurnCompleted schema (fields 1-15).
func TestMarshalCompanionChatTurnCompleted_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	turnStarted := env.OccurredAt
	completedAt := env.OccurredAt.Add(2 * time.Second)
	payload := map[string]any{
		"session_id":        "01971a90-1111-7000-8000-000000000001",
		"turn_id":           "01971a90-2222-7000-8000-000000000001",
		"gcid":              env.GCID,
		"tenant_id":         env.TenantID,
		"companion_id":      "01971a90-3333-7000-8000-000000000001",
		"engine_session_id": "vertex-abc123",
		"model":             "gemini-2.5-flash-lite",
		"output_tokens":     42,
		"mana_charged":      5,
		"mana_tier":         "basic",
		"tool_calls_count":  1,
		"finish_reason":     "STOP",
		"started_at":        turnStarted,
		"completed_at":      completedAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.chat_turn_completed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("MarshalPayload: empty bytes")
	}

	// Walk all top-level tags + verify field numbers fall in [1..15]
	// (schema for CompanionChatTurnCompleted). field 1 = envelope.
	seen := map[protowire.Number]bool{}
	rem := bz
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			t.Fatalf("invalid tag at offset %d", len(bz)-len(rem))
		}
		if num < 1 || num > 15 {
			t.Fatalf("field number %d out of schema range [1..15]", num)
		}
		rem = rem[n:]
		seen[num] = true
		switch typ {
		case protowire.BytesType:
			_, n := protowire.ConsumeBytes(rem)
			if n < 0 {
				t.Fatalf("invalid bytes for field %d", num)
			}
			rem = rem[n:]
		case protowire.VarintType:
			_, n := protowire.ConsumeVarint(rem)
			if n < 0 {
				t.Fatalf("invalid varint for field %d", num)
			}
			rem = rem[n:]
		}
	}
	required := []protowire.Number{1 /*envelope*/, 2 /*session_id*/, 3 /*turn_id*/, 4 /*gcid*/, 5 /*tenant_id*/, 6 /*companion_id*/, 7 /*engine_session_id*/, 8 /*model*/, 9 /*output_tokens*/, 10 /*mana_charged*/, 11 /*mana_tier*/, 12 /*tool_calls_count*/, 13 /*finish_reason*/, 14 /*started_at*/, 15 /*completed_at*/}
	for _, want := range required {
		if !seen[want] {
			t.Fatalf("CompanionChatTurnCompleted: missing field %d", want)
		}
	}
}

// TestMarshalGoalGraduated_RoundTripsBinaryProto asserts the encoder produces
// canonical wire bytes that decode into the generated GoalGraduated schema with
// every field intact — the strongest guarantee that the Pub/Sub Schema Registry
// (binary encoding) accepts the published payload. RED before the encoder lands.
func TestMarshalGoalGraduated_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	graduatedAt := env.OccurredAt.Add(3 * time.Second)
	payload := map[string]any{
		"goal_id":           "01971a90-9000-7000-8000-000000000001",
		"tenant_id":         env.TenantID,
		"learner_gcid":      env.GCID,
		"kind":              "theme_mastery",
		"chora_target_ref":  "cert-ref-42",
		"mastered_concepts": 5,
		"total_concepts":    5,
		"graduated_at":      graduatedAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.goal.graduated.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var got consumptionv1.GoalGraduated
	if err := proto.Unmarshal(bz, &got); err != nil {
		t.Fatalf("proto.Unmarshal into GoalGraduated: %v", err)
	}

	if got.GetGoalId() != "01971a90-9000-7000-8000-000000000001" {
		t.Errorf("goal_id = %q", got.GetGoalId())
	}
	if got.GetTenantId() != env.TenantID {
		t.Errorf("tenant_id = %q", got.GetTenantId())
	}
	if got.GetLearnerGcid() != env.GCID {
		t.Errorf("learner_gcid = %q", got.GetLearnerGcid())
	}
	if got.GetKind() != "theme_mastery" {
		t.Errorf("kind = %q", got.GetKind())
	}
	if got.GetChoraTargetRef() != "cert-ref-42" {
		t.Errorf("chora_target_ref = %q", got.GetChoraTargetRef())
	}
	if got.GetMasteredConcepts() != 5 {
		t.Errorf("mastered_concepts = %d", got.GetMasteredConcepts())
	}
	if got.GetTotalConcepts() != 5 {
		t.Errorf("total_concepts = %d", got.GetTotalConcepts())
	}
	if !got.GetGraduatedAt().AsTime().Equal(graduatedAt) {
		t.Errorf("graduated_at = %v, want %v", got.GetGraduatedAt().AsTime(), graduatedAt)
	}

	// Envelope must decode to the canonical EventEnvelope layout (field 1).
	gotEnv := got.GetEnvelope()
	if gotEnv == nil {
		t.Fatal("envelope is nil")
	}
	if gotEnv.GetEventId() != env.EventID {
		t.Errorf("envelope.event_id = %q", gotEnv.GetEventId())
	}
	if gotEnv.GetIdempotencyKey() != env.IdempotencyKey {
		t.Errorf("envelope.idempotency_key = %q", gotEnv.GetIdempotencyKey())
	}
	if gotEnv.GetGcid() != env.GCID {
		t.Errorf("envelope.gcid = %q", gotEnv.GetGcid())
	}
	if gotEnv.GetSchemaVersion() != env.SchemaVersion {
		t.Errorf("envelope.schema_version = %d", gotEnv.GetSchemaVersion())
	}
	if !gotEnv.GetOccurredAt().AsTime().Equal(env.OccurredAt) {
		t.Errorf("envelope.occurred_at = %v", gotEnv.GetOccurredAt().AsTime())
	}
}

// TestMarshalCompanionStageUp_CarriesDisplayName (CHO-2266) proves the
// denormalised companion_display_name (field 13) reaches the wire so the C+
// milestone auto-share copy can NAME the Companion. Driven wire-realistically:
// encode via MarshalPayload, then proto.Unmarshal into the generated binding
// and read the getter — a hand-filled struct would pass even if the encoder
// dropped the field.
func TestMarshalCompanionStageUp_CarriesDisplayName(t *testing.T) {
	env := fixedEnvelope()
	stageUpAt := env.OccurredAt.Add(2 * time.Second)
	payload := map[string]any{
		"companion_id":           "01971a90-9000-7000-8000-0000000000f1",
		"owner_gcid":             env.GCID,
		"stage_from":             2,
		"stage_to":               3,
		"stage_from_name":        "fledgling",
		"stage_to_name":          "awakened",
		"new_llm_tier":           "flash",
		"companion_display_name": "Tempo",
		"stage_up_at":            stageUpAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.stage_up.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var got consumptionv1.CompanionStageUp
	if err := proto.Unmarshal(bz, &got); err != nil {
		t.Fatalf("proto.Unmarshal into CompanionStageUp: %v", err)
	}
	if got.GetCompanionDisplayName() != "Tempo" {
		t.Errorf("companion_display_name = %q, want Tempo", got.GetCompanionDisplayName())
	}
	// Guard the field number: the name must NOT collide with an existing field.
	if got.GetStageToName() != "awakened" {
		t.Errorf("stage_to_name = %q, want awakened", got.GetStageToName())
	}
}

// TestMarshalCompanionStageUp_OmitsEmptyDisplayName pins the proto3 omit-zero
// convention: an absent/empty name writes no bytes (the field never leaks a
// blank), so the field number stays reusable and the consumer branch on
// presence works.
func TestMarshalCompanionStageUp_OmitsEmptyDisplayName(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"companion_id":  "01971a90-9000-7000-8000-0000000000f2",
		"owner_gcid":    env.GCID,
		"stage_to_name": "awakened",
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.stage_up.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var got consumptionv1.CompanionStageUp
	if err := proto.Unmarshal(bz, &got); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	if got.GetCompanionDisplayName() != "" {
		t.Errorf("companion_display_name = %q, want empty when unset", got.GetCompanionDisplayName())
	}
}

// TestMarshalCompanionSourceRevelation_CarriesDisplayName (CHO-2266) proves the
// denormalised companion_display_name (field 9) reaches the wire so the Stage-3
// Aha-moment ceremony copy can NAME the Companion.
func TestMarshalCompanionSourceRevelation_CarriesDisplayName(t *testing.T) {
	env := fixedEnvelope()
	revelationAt := env.OccurredAt.Add(time.Second)
	expiresAt := revelationAt.Add(24 * time.Hour)
	payload := map[string]any{
		"companion_id":            "01971a90-9000-7000-8000-0000000000f3",
		"owner_gcid":              env.GCID,
		"preview_llm_tier":        "pro",
		"preview_tools":           []string{"atom_search", "socratic_drill"},
		"revelation_at":           revelationAt,
		"window_expires_at":       expiresAt,
		"window_duration_seconds": 86400,
		"companion_display_name":  "Tempo",
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.source_revelation.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var got consumptionv1.CompanionSourceRevelation
	if err := proto.Unmarshal(bz, &got); err != nil {
		t.Fatalf("proto.Unmarshal into CompanionSourceRevelation: %v", err)
	}
	if got.GetCompanionDisplayName() != "Tempo" {
		t.Errorf("companion_display_name = %q, want Tempo", got.GetCompanionDisplayName())
	}
	if got.GetPreviewLlmTier() != "pro" {
		t.Errorf("preview_llm_tier = %q, want pro", got.GetPreviewLlmTier())
	}
	if got.GetWindowDurationSeconds() != 86400 {
		t.Errorf("window_duration_seconds = %d, want 86400", got.GetWindowDurationSeconds())
	}
}

// TestMarshalGoalProgressUpdated_RoundTripsBinaryProto asserts the progress
// event encodes to canonical wire bytes decoding into the generated schema.
func TestMarshalGoalProgressUpdated_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	occurredAt := env.OccurredAt.Add(1 * time.Second)
	payload := map[string]any{
		"goal_id":           "01971a90-9000-7000-8000-000000000002",
		"tenant_id":         env.TenantID,
		"learner_gcid":      env.GCID,
		"mastered_concepts": 3,
		"total_concepts":    5,
		"progress_percent":  60,
		"occurred_at":       occurredAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.goal.progress_updated.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var got consumptionv1.GoalProgressUpdated
	if err := proto.Unmarshal(bz, &got); err != nil {
		t.Fatalf("proto.Unmarshal into GoalProgressUpdated: %v", err)
	}

	if got.GetGoalId() != "01971a90-9000-7000-8000-000000000002" {
		t.Errorf("goal_id = %q", got.GetGoalId())
	}
	if got.GetTenantId() != env.TenantID {
		t.Errorf("tenant_id = %q", got.GetTenantId())
	}
	if got.GetLearnerGcid() != env.GCID {
		t.Errorf("learner_gcid = %q", got.GetLearnerGcid())
	}
	if got.GetMasteredConcepts() != 3 {
		t.Errorf("mastered_concepts = %d", got.GetMasteredConcepts())
	}
	if got.GetTotalConcepts() != 5 {
		t.Errorf("total_concepts = %d", got.GetTotalConcepts())
	}
	if got.GetProgressPercent() != 60 {
		t.Errorf("progress_percent = %d", got.GetProgressPercent())
	}
	if !got.GetOccurredAt().AsTime().Equal(occurredAt) {
		t.Errorf("occurred_at = %v, want %v", got.GetOccurredAt().AsTime(), occurredAt)
	}
	if got.GetEnvelope().GetEventId() != env.EventID {
		t.Errorf("envelope.event_id = %q", got.GetEnvelope().GetEventId())
	}
}

// TestMarshal_UnknownTopic_FailsLoud asserts unsupported topics return a
// typed error so the dispatcher dead-letters rather than persisting bytes
// the Schema Registry will reject.
func TestMarshal_UnknownTopic_FailsLoud(t *testing.T) {
	env := fixedEnvelope()
	_, err := protomarshal.MarshalPayload("chora.consumption.some.unwired.v1", env, nil)
	if err == nil {
		t.Fatal("expected ErrUnsupportedTopic, got nil")
	}
	if !protomarshal.IsUnsupportedTopic(err) {
		t.Fatalf("expected IsUnsupportedTopic(true), got: %v", err)
	}
}

// TestMarshal_NilPayloadProducesEmptyButValidMessage asserts a nil payload
// still produces a wire-valid message containing just the envelope.
func TestMarshal_NilPayloadProducesEmptyButValidMessage(t *testing.T) {
	env := fixedEnvelope()
	bz, err := protomarshal.MarshalPayload("chora.consumption.daily_dose.served.v1", env, nil)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("nil payload should still produce envelope bytes")
	}
	// First tag must be envelope (field 1).
	num, _, n := protowire.ConsumeTag(bz)
	if n < 0 {
		t.Fatal("invalid leading tag")
	}
	if num != 1 {
		t.Fatalf("expected leading field=1 (envelope), got %d", num)
	}
}

// TestMarshal_RejectsInvalidPayloadType asserts the encoder fails loud when
// payload fields have the wrong Go type for their proto schema slot
// (rather than silently coercing or producing garbage bytes).
func TestMarshal_RejectsInvalidPayloadType(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"output_tokens": "not-a-number", // schema demands int32
	}
	_, err := protomarshal.MarshalPayload("chora.consumption.companion.chat_turn_completed.v1", env, payload)
	if err == nil {
		t.Fatal("expected type error for output_tokens=string")
	}
}

// TestMarshal_DailyDose_AcceptsTypedEntries covers the alternate payload
// shape where the caller has already projected entries to map[string]any.
func TestMarshal_DailyDose_AcceptsTypedEntries(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"learner_gcid": env.GCID,
		"entries": []map[string]any{
			{"atom_id": "a-1", "topic": "fractions", "title": "Halves", "dose_reason": 1},
			{"atom_id": "a-2", "topic": "fractions", "title": "Thirds", "dose_reason": 3},
		},
		"size":    2,
		"message": "Your Daily Dose is ready!",
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.daily_dose.served.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
}

// TestMarshal_ChatTurn_RejectsStartedAtAsString — wrong type on a
// Timestamp slot must fail loud, not silently emit garbage.
func TestMarshal_ChatTurn_RejectsStartedAtAsString(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"session_id": "s-1",
		"started_at": "not-a-time",
	}
	_, err := protomarshal.MarshalPayload("chora.consumption.companion.chat_turn_completed.v1", env, payload)
	if err == nil {
		t.Fatal("expected typed error for started_at=string")
	}
}

// TestMarshal_ChatTurn_StringFieldRejectsNonString — schema-string slot
// must reject non-string Go types.
func TestMarshal_ChatTurn_StringFieldRejectsNonString(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"session_id": 12345, // schema demands string
	}
	_, err := protomarshal.MarshalPayload("chora.consumption.companion.chat_turn_completed.v1", env, payload)
	if err == nil {
		t.Fatal("expected typed error for session_id=int")
	}
}

// TestMarshal_DailyDose_AtomIDsFromInterfaceSlice covers the case where
// the caller passes []any-of-string rather than []string (common when
// payloads are loaded from JSON-decoded responses).
func TestMarshal_DailyDose_AtomIDsFromInterfaceSlice(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"atom_ids": []any{"x", "y", "z"},
		"size":     3,
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.daily_dose.served.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
}

// TestEnvelopePtr ensures asTime accepts *time.Time (call sites may pass
// either form).
func TestEnvelopePtr_TimePointerAccepted(t *testing.T) {
	env := fixedEnvelope()
	now := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"started_at":   &now,
		"completed_at": &now,
		"session_id":   "s-1",
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.chat_turn_completed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload with *time.Time: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
}

// TestMarshal_CompanionExpAwarded_TypeMismatchFailsLoud asserts the exp_awarded
// encoder fails loud (a REAL error, not ErrUnsupportedTopic) when a payload
// value has the wrong Go type for its schema slot — the dispatcher must
// dead-letter the row, never publish corrupt bytes.
func TestMarshal_CompanionExpAwarded_TypeMismatchFailsLoud(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"companion_id": "fam-1",
		"exp_delta":    "fifteen", // schema slot is int32
	}
	_, err := protomarshal.MarshalPayload("chora.consumption.companion.exp_awarded.v1", env, payload)
	if err == nil {
		t.Fatal("expected type-mismatch error, got nil")
	}
	if protomarshal.IsUnsupportedTopic(err) {
		t.Fatalf("expected a real encode error, got ErrUnsupportedTopic: %v", err)
	}
}

// TestMarshalWeaknessGrown_RoundTripsBinaryProto walks the produced wire bytes
// for the weakness.grown encoder. Unlike every other consumption event this one
// carries a proto `float` (final_strength, field 7) → wire type I32 (Fixed32),
// so the tag walk must additionally handle protowire.Fixed32Type. Asserts the
// load-bearing fields (envelope, growth_edge_id, final_strength as I32, tags,
// grown_at) are present + well-formed.
func TestMarshalWeaknessGrown_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"growth_edge_id":  "019eb05f-d556-7284-b643-a7cc50932f8c",
		"tenant_id":       env.TenantID,
		"learner_gcid":    env.GCID,
		"concept_label":   "Single Responsibility Principle",
		"concept_key":     "single-responsibility-principle",
		"final_strength":  0.05,
		"recovery_source": "drill_atom",
		"tags":            []string{"solid", "oop"},
		"grown_at":        env.OccurredAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.weakness.grown.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("MarshalPayload: empty bytes")
	}

	// Walk all top-level tags. Schema is fields [1..10]; final_strength (7) is
	// the sole I32 (Fixed32) slot, the rest are bytes/varint.
	seen := map[protowire.Number]bool{}
	typeOf := map[protowire.Number]protowire.Type{}
	rem := bz
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			t.Fatalf("invalid tag at offset %d", len(bz)-len(rem))
		}
		if num < 1 || num > 10 {
			t.Fatalf("field number %d out of schema range [1..10]", num)
		}
		rem = rem[n:]
		seen[num] = true
		typeOf[num] = typ
		switch typ {
		case protowire.BytesType:
			_, n := protowire.ConsumeBytes(rem)
			if n < 0 {
				t.Fatalf("invalid length-delimited value for field %d", num)
			}
			rem = rem[n:]
		case protowire.VarintType:
			_, n := protowire.ConsumeVarint(rem)
			if n < 0 {
				t.Fatalf("invalid varint value for field %d", num)
			}
			rem = rem[n:]
		case protowire.Fixed32Type:
			_, n := protowire.ConsumeFixed32(rem)
			if n < 0 {
				t.Fatalf("invalid fixed32 value for field %d", num)
			}
			rem = rem[n:]
		default:
			t.Fatalf("unexpected wire type %d for field %d", typ, num)
		}
	}

	for _, want := range []protowire.Number{1 /*envelope*/, 2 /*growth_edge_id*/, 7 /*final_strength*/, 9 /*tags*/, 10 /*grown_at*/} {
		if !seen[want] {
			t.Fatalf("WeaknessGrown: missing field %d", want)
		}
	}
	// final_strength MUST be I32 on the wire (proto `float`), not varint/bytes.
	if typeOf[7] != protowire.Fixed32Type {
		t.Fatalf("final_strength (field 7) wire type = %d, want Fixed32Type (%d)", typeOf[7], protowire.Fixed32Type)
	}
}

// TestMarshalCompanionEggPurchased_RoundTripsBinaryProto — RED for the egg-funnel
// outbox debt (#8, CHO-2028): ProvisionEgg emits egg_purchased.v1 but the topic
// has no binary encoder, so the publisher JSON-falls-back and Schema Registry
// rejects with INVALID_BINARY_PROTO_MESSAGE (row deadletters). The encoder must
// produce wire bytes decodable into the generated CompanionEggPurchased schema.
// Field layout — chora-contracts/proto/events-flat/consumption/companion/egg_purchased.proto.
func TestMarshalCompanionEggPurchased_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	purchasedAt := env.OccurredAt.Add(2 * time.Second)
	soft := purchasedAt.AddDate(0, 0, 30)
	hard := purchasedAt.AddDate(0, 0, 60)
	payload := map[string]any{
		"companion_id":            "01971a90-aaaa-7000-8000-000000000001",
		"owner_gcid":              env.GCID,
		"egg_sku":                 "egg.standard.v1",
		"source":                  "purchase",
		"egg_purchase_id":         "019f23f9-0000-7000-8000-000000000001", // producer-internal; not in the schema
		"stripe_session_id":       "cs_test_123",
		"amount_cents":            int64(1900),
		"currency":                "sgd",
		"suggested_focal_atom_id": "01971a90-bbbb-7000-8000-000000000002",
		"soft_expiry_at":          soft,
		"hard_expiry_at":          hard,
		"purchased_at":            purchasedAt,
		"chora_companion_id":      "01971a90-aaaa-7000-8000-000000000001",
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.egg_purchased.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var got consumptionv1.CompanionEggPurchased
	if err := proto.Unmarshal(bz, &got); err != nil {
		t.Fatalf("proto.Unmarshal into CompanionEggPurchased: %v", err)
	}

	if got.GetCompanionId() != "01971a90-aaaa-7000-8000-000000000001" {
		t.Errorf("companion_id = %q", got.GetCompanionId())
	}
	if got.GetOwnerGcid() != env.GCID {
		t.Errorf("owner_gcid = %q", got.GetOwnerGcid())
	}
	if got.GetEggSku() != "egg.standard.v1" {
		t.Errorf("egg_sku = %q", got.GetEggSku())
	}
	if got.GetSource() != "purchase" {
		t.Errorf("source = %q", got.GetSource())
	}
	if got.GetStripeSessionId() != "cs_test_123" {
		t.Errorf("stripe_session_id = %q", got.GetStripeSessionId())
	}
	if got.GetAmountCents() != 1900 {
		t.Errorf("amount_cents = %d", got.GetAmountCents())
	}
	if got.GetCurrency() != "sgd" {
		t.Errorf("currency = %q", got.GetCurrency())
	}
	if got.GetSuggestedFocalAtomId() != "01971a90-bbbb-7000-8000-000000000002" {
		t.Errorf("suggested_focal_atom_id = %q", got.GetSuggestedFocalAtomId())
	}
	if !got.GetSoftExpiryAt().AsTime().Equal(soft) {
		t.Errorf("soft_expiry_at = %v, want %v", got.GetSoftExpiryAt().AsTime(), soft)
	}
	if !got.GetHardExpiryAt().AsTime().Equal(hard) {
		t.Errorf("hard_expiry_at = %v, want %v", got.GetHardExpiryAt().AsTime(), hard)
	}
	if !got.GetPurchasedAt().AsTime().Equal(purchasedAt) {
		t.Errorf("purchased_at = %v, want %v", got.GetPurchasedAt().AsTime(), purchasedAt)
	}

	gotEnv := got.GetEnvelope()
	if gotEnv == nil {
		t.Fatal("envelope is nil")
	}
	if gotEnv.GetEventId() != env.EventID {
		t.Errorf("envelope.event_id = %q", gotEnv.GetEventId())
	}
	if gotEnv.GetTraceparent() != env.Traceparent {
		t.Errorf("envelope.traceparent = %q", gotEnv.GetTraceparent())
	}
	if gotEnv.GetSchemaVersion() != env.SchemaVersion {
		t.Errorf("envelope.schema_version = %d", gotEnv.GetSchemaVersion())
	}
}
