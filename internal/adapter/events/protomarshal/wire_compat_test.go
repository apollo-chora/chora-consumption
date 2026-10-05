// wire_compat_test verifies the binary bytes emitted by MarshalPayload parse
// cleanly into the generated proto types from chora-contracts. This is the
// load-bearing assertion — if these tests pass, the BINARY schema encoding
// will accept the bytes.
//
// CompanionChatTurnCompleted is not yet in the generated Go bindings (BSR
// regen rate-limited at landing time); the field-tag-walk test in
// protomarshal_test.go covers its wire compatibility. DailyDoseServed IS
// generated, so we round-trip it through proto.Unmarshal here.
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

func TestWireCompat_DailyDoseServed_DecodesIntoGeneratedType(t *testing.T) {
	t0 := time.Date(2026, 5, 16, 9, 30, 0, 0, time.UTC)
	env := protomarshal.Envelope{
		EventID:        "01971a90-0000-7000-8000-000000000abc",
		IdempotencyKey: "idemp-wire-1",
		TenantID:       "tenant-acme",
		GCID:           "gcid-phyllis",
		OccurredAt:     t0,
		PublishedAt:    t0.Add(time.Millisecond),
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		Tracestate:     "vendor=value",
		SourceProject:  "chora-content",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
	payload := map[string]any{
		"learner_gcid":         "gcid-phyllis",
		"atom_ids":             []string{"atom-A", "atom-B"},
		"size":                 2,
		"served_at":            t0,
		"chora_imda_dimension": "accountability",
		"imda_lifecycle_stage": "runtime",
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.daily_dose.served.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var msg consumptionv1.DailyDoseServed
	if err := proto.Unmarshal(bz, &msg); err != nil {
		t.Fatalf("proto.Unmarshal into DailyDoseServed: %v", err)
	}

	if msg.GetEnvelope() == nil {
		t.Fatal("envelope not decoded")
	}
	if got := msg.GetEnvelope().GetEventId(); got != env.EventID {
		t.Fatalf("envelope.event_id: got %q want %q", got, env.EventID)
	}
	if got := msg.GetEnvelope().GetTenantId(); got != env.TenantID {
		t.Fatalf("envelope.tenant_id: got %q want %q", got, env.TenantID)
	}
	if got := msg.GetEnvelope().GetGcid(); got != env.GCID {
		t.Fatalf("envelope.gcid: got %q want %q", got, env.GCID)
	}
	if got := msg.GetEnvelope().GetTraceparent(); got != env.Traceparent {
		t.Fatalf("envelope.traceparent: got %q want %q", got, env.Traceparent)
	}
	if got := msg.GetEnvelope().GetSchemaVersion(); got != env.SchemaVersion {
		t.Fatalf("envelope.schema_version: got %d want %d", got, env.SchemaVersion)
	}
	if got := msg.GetEnvelope().GetSourceService(); got != env.SourceService {
		t.Fatalf("envelope.source_service: got %q want %q", got, env.SourceService)
	}
	if got := msg.GetEnvelope().GetOccurredAt(); got == nil || got.AsTime().Unix() != t0.Unix() {
		t.Fatalf("envelope.occurred_at not round-tripped: %+v", got)
	}

	if got := msg.GetLearnerGcid(); got != "gcid-phyllis" {
		t.Fatalf("learner_gcid: got %q", got)
	}
	if got := msg.GetSize(); got != 2 {
		t.Fatalf("size: got %d", got)
	}
	if got := msg.GetEntries(); len(got) != 2 || got[0].GetAtomId() != "atom-A" || got[1].GetAtomId() != "atom-B" {
		t.Fatalf("entries: got %+v", got)
	}
	if got := msg.GetServedAt(); got == nil || got.AsTime().Unix() != t0.Unix() {
		t.Fatalf("served_at not round-tripped: %+v", got)
	}
	// IMDA evidence projected into envelope.chora_imda_dimension / imda_lifecycle_stage.
	if got := msg.GetEnvelope().GetChoraImdaDimension(); got != "accountability" {
		t.Fatalf("envelope.chora_imda_dimension: got %q", got)
	}
	if got := msg.GetEnvelope().GetImdaLifecycleStage(); got != "runtime" {
		t.Fatalf("envelope.imda_lifecycle_stage: got %q", got)
	}
}

// -----------------------------------------------------------------------------
// 4 companion milestone topics — protobuf binary encoder coverage.
// Producers in services/chora-consumption/internal/domain/growth/service.go
// emit these via PublishGrowthEvent → outbox encodeOutboxPayload, which now
// MUST resolve to MarshalPayload (not JSON fallback) for Schema Registry GA.
// -----------------------------------------------------------------------------

func milestoneEnvelope() protomarshal.Envelope {
	t0 := time.Date(2026, 5, 16, 9, 0, 0, 0, time.UTC)
	return protomarshal.Envelope{
		EventID:        "01971a90-0000-7000-8000-000000000m1",
		IdempotencyKey: "milestone-1",
		TenantID:       "tenant-acme",
		GCID:           "gcid-phyllis",
		OccurredAt:     t0,
		PublishedAt:    t0.Add(time.Millisecond),
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		SourceProject:  "chora-content",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
}

func TestWireCompat_CompanionStageUp_DecodesIntoGeneratedType(t *testing.T) {
	env := milestoneEnvelope()
	now := env.OccurredAt
	payload := map[string]any{
		"companion_id":                "fam-1",
		"owner_gcid":                  env.GCID,
		"stage_from":                  2,
		"stage_to":                    3,
		"stage_from_name":             "fledgling",
		"stage_to_name":               "awakened",
		"newly_unlocked_tools":        []string{"semantic_search", "kg_traverse"},
		"newly_revealed_kg_neighbors": []string{},
		"new_llm_tier":                "flash-reasoning",
		"new_memory_mode":             "rolling-3",
		"stage_up_at":                 now,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.stage_up.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var msg consumptionv1.CompanionStageUp
	if err := proto.Unmarshal(bz, &msg); err != nil {
		t.Fatalf("proto.Unmarshal CompanionStageUp: %v", err)
	}
	if msg.GetEnvelope().GetEventId() != env.EventID {
		t.Fatalf("envelope.event_id: got %q want %q", msg.GetEnvelope().GetEventId(), env.EventID)
	}
	if msg.GetCompanionId() != "fam-1" {
		t.Fatalf("companion_id: got %q", msg.GetCompanionId())
	}
	if msg.GetStageFrom() != 2 || msg.GetStageTo() != 3 {
		t.Fatalf("stage_from/to: got %d/%d", msg.GetStageFrom(), msg.GetStageTo())
	}
	if msg.GetStageFromName() != "fledgling" || msg.GetStageToName() != "awakened" {
		t.Fatalf("stage_*_name: got %q/%q", msg.GetStageFromName(), msg.GetStageToName())
	}
	if got := msg.GetNewlyUnlockedTools(); len(got) != 2 || got[0] != "semantic_search" {
		t.Fatalf("newly_unlocked_tools: got %+v", got)
	}
	if msg.GetNewLlmTier() != "flash-reasoning" {
		t.Fatalf("new_llm_tier: got %q", msg.GetNewLlmTier())
	}
	if msg.GetNewMemoryMode() != "rolling-3" {
		t.Fatalf("new_memory_mode: got %q", msg.GetNewMemoryMode())
	}
	if got := msg.GetStageUpAt(); got == nil || got.AsTime().Unix() != now.Unix() {
		t.Fatalf("stage_up_at not round-tripped: %+v", got)
	}
}

func TestWireCompat_CompanionBreedRevealed_DecodesIntoGeneratedType(t *testing.T) {
	env := milestoneEnvelope()
	now := env.OccurredAt
	payload := map[string]any{
		"companion_id":       "fam-1",
		"owner_gcid":         env.GCID,
		"species":            "dragon",
		"shiny_variant":      true,
		"rarity":             "legendary",
		"egg_sku":            "egg.standard.v1",
		"rolled_probability": 3.5,
		"revealed_at":        now,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.breed_revealed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var msg consumptionv1.CompanionBreedRevealed
	if err := proto.Unmarshal(bz, &msg); err != nil {
		t.Fatalf("proto.Unmarshal CompanionBreedRevealed: %v", err)
	}
	if msg.GetCompanionId() != "fam-1" {
		t.Fatalf("companion_id: got %q", msg.GetCompanionId())
	}
	if got := msg.GetSpecies(); got != consumptionv1.CompanionSpecies_COMPANION_SPECIES_DRAGON {
		t.Fatalf("species: got %v, want DRAGON", got)
	}
	if !msg.GetShinyVariant() {
		t.Fatalf("shiny_variant: expected true")
	}
	if msg.GetRarity() != "legendary" {
		t.Fatalf("rarity: got %q", msg.GetRarity())
	}
	if msg.GetEggSku() != "egg.standard.v1" {
		t.Fatalf("egg_sku: got %q", msg.GetEggSku())
	}
	if got := msg.GetRolledProbability(); got < 3.49 || got > 3.51 {
		t.Fatalf("rolled_probability: got %f, want ~3.5", got)
	}
	if got := msg.GetRevealedAt(); got == nil || got.AsTime().Unix() != now.Unix() {
		t.Fatalf("revealed_at not round-tripped: %+v", got)
	}
}

func TestWireCompat_CompanionHatched_DecodesIntoGeneratedType(t *testing.T) {
	env := milestoneEnvelope()
	now := env.OccurredAt
	payload := map[string]any{
		"companion_id":     "fam-1",
		"owner_gcid":       env.GCID,
		"display_name":     "Sparkle",
		"tone":             "socratic",
		"learner_persona":  "curious-explorer",
		"resonant_atom_id": "atom-math-derivatives",
		"specialization":   "math",
		"species":          "owl",
		"shiny_variant":    false,
		"hatched_at":       now,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.hatched.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var msg consumptionv1.CompanionHatched
	if err := proto.Unmarshal(bz, &msg); err != nil {
		t.Fatalf("proto.Unmarshal CompanionHatched: %v", err)
	}
	if msg.GetCompanionId() != "fam-1" {
		t.Fatalf("companion_id: got %q", msg.GetCompanionId())
	}
	if msg.GetDisplayName() != "Sparkle" {
		t.Fatalf("display_name: got %q", msg.GetDisplayName())
	}
	if msg.GetTone() != "socratic" {
		t.Fatalf("tone: got %q", msg.GetTone())
	}
	if msg.GetLearnerPersona() != "curious-explorer" {
		t.Fatalf("learner_persona: got %q", msg.GetLearnerPersona())
	}
	if msg.GetResonantAtomId() != "atom-math-derivatives" {
		t.Fatalf("resonant_atom_id: got %q", msg.GetResonantAtomId())
	}
	if msg.GetSpecialization() != "math" {
		t.Fatalf("specialization: got %q", msg.GetSpecialization())
	}
	if got := msg.GetSpecies(); got != consumptionv1.CompanionSpecies_COMPANION_SPECIES_OWL {
		t.Fatalf("species: got %v, want OWL", got)
	}
	if got := msg.GetHatchedAt(); got == nil || got.AsTime().Unix() != now.Unix() {
		t.Fatalf("hatched_at not round-tripped: %+v", got)
	}
}

func TestWireCompat_CompanionSourceRevelation_DecodesIntoGeneratedType(t *testing.T) {
	env := milestoneEnvelope()
	now := env.OccurredAt
	expires := now.Add(24 * time.Hour)
	payload := map[string]any{
		"companion_id":            "fam-1",
		"owner_gcid":              env.GCID,
		"preview_llm_tier":        "pro",
		"preview_tools":           []string{"kg_traverse", "atom_compose", "semantic_search"},
		"revelation_at":           now,
		"window_expires_at":       expires,
		"window_duration_seconds": 86400,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.source_revelation.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var msg consumptionv1.CompanionSourceRevelation
	if err := proto.Unmarshal(bz, &msg); err != nil {
		t.Fatalf("proto.Unmarshal CompanionSourceRevelation: %v", err)
	}
	if msg.GetCompanionId() != "fam-1" {
		t.Fatalf("companion_id: got %q", msg.GetCompanionId())
	}
	if msg.GetPreviewLlmTier() != "pro" {
		t.Fatalf("preview_llm_tier: got %q", msg.GetPreviewLlmTier())
	}
	if got := msg.GetPreviewTools(); len(got) != 3 || got[0] != "kg_traverse" {
		t.Fatalf("preview_tools: got %+v", got)
	}
	if msg.GetWindowDurationSeconds() != 86400 {
		t.Fatalf("window_duration_seconds: got %d", msg.GetWindowDurationSeconds())
	}
	if got := msg.GetRevelationAt(); got == nil || got.AsTime().Unix() != now.Unix() {
		t.Fatalf("revelation_at not round-tripped: %+v", got)
	}
	if got := msg.GetWindowExpiresAt(); got == nil || got.AsTime().Unix() != expires.Unix() {
		t.Fatalf("window_expires_at not round-tripped: %+v", got)
	}
}

func TestWireCompat_CompanionBreedRevealed_RejectsUnknownSpecies(t *testing.T) {
	env := milestoneEnvelope()
	payload := map[string]any{
		"companion_id": "fam-1",
		"owner_gcid":   env.GCID,
		"species":      "unicorn", // not in canonical enum
	}
	_, err := protomarshal.MarshalPayload("chora.consumption.companion.breed_revealed.v1", env, payload)
	if err == nil {
		t.Fatal("expected error for unknown species, got nil")
	}
}

// TestWireCompat_CompanionExpAwarded_DecodesIntoGeneratedType pins the
// exp_awarded encoder to its Schema Registry schema. The topic is BINARY-bound
// live — without an encoder the outbox JSON-fell-through and every growth-EXP
// publish dead-lettered (valuestreams §7.4 row "companion.exp_awarded.v1 fails
// Schema Registry validation"). Payload keys mirror growth.Service.AwardExp
// exactly, including the chora_* observability extras the encoder must ignore.
func TestWireCompat_CompanionExpAwarded_DecodesIntoGeneratedType(t *testing.T) {
	t0 := time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC)
	env := protomarshal.Envelope{
		EventID:        "01971a90-0000-7000-8000-00000000d00d",
		IdempotencyKey: "exp:01971a90-aaaa-7000-8000-000000000001",
		TenantID:       "11111111-1111-7111-8111-111111111111",
		GCID:           "gcid-phyllis",
		OccurredAt:     t0,
		PublishedAt:    t0.Add(time.Millisecond),
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		Tracestate:     "vendor=value",
		SourceProject:  "chora-489812",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
	payload := map[string]any{
		"companion_id":      "fam-0001",
		"owner_gcid":        "gcid-phyllis",
		"exp_delta":         15, // plain int, as AwardExp emits
		"exp_total_after":   115,
		"source":            "atom_session",
		"source_event_id":   "01971a90-aaaa-7000-8000-000000000001",
		"source_topic":      "chora.consumption.atom_session.completed.v1",
		"source_session_id": "sess-42",
		"source_turn_seq":   3,
		"daily_cap_hit":     true,
		"triggers_stage_up": true,
		"awarded_at":        t0,
		// Observability extras AwardExp stamps alongside the schema fields —
		// unknown to the schema, must be skipped without erroring.
		"chora_companion_id":        "fam-0001",
		"chora_growth_source":       "atom_session",
		"chora_growth_stage_before": 1,
		"chora_growth_stage_after":  2,
		"chora_exp_delta":           15,
		"chora_daily_cap_hit":       true,
		"chora_duplicate":           false,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.exp_awarded.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var msg consumptionv1.CompanionExpAwarded
	if err := proto.Unmarshal(bz, &msg); err != nil {
		t.Fatalf("proto.Unmarshal into CompanionExpAwarded: %v", err)
	}

	if msg.GetEnvelope() == nil {
		t.Fatal("envelope not decoded")
	}
	if got := msg.GetEnvelope().GetEventId(); got != env.EventID {
		t.Errorf("envelope.event_id = %q, want %q", got, env.EventID)
	}
	if got := msg.GetEnvelope().GetTenantId(); got != env.TenantID {
		t.Errorf("envelope.tenant_id = %q, want %q", got, env.TenantID)
	}
	if got := msg.GetEnvelope().GetTraceparent(); got != env.Traceparent {
		t.Errorf("envelope.traceparent = %q, want %q", got, env.Traceparent)
	}
	if msg.GetCompanionId() != "fam-0001" {
		t.Errorf("companion_id = %q", msg.GetCompanionId())
	}
	if msg.GetOwnerGcid() != "gcid-phyllis" {
		t.Errorf("owner_gcid = %q", msg.GetOwnerGcid())
	}
	if msg.GetExpDelta() != 15 {
		t.Errorf("exp_delta = %d, want 15", msg.GetExpDelta())
	}
	if msg.GetExpTotalAfter() != 115 {
		t.Errorf("exp_total_after = %d, want 115", msg.GetExpTotalAfter())
	}
	if msg.GetSource() != "atom_session" {
		t.Errorf("source = %q", msg.GetSource())
	}
	if msg.GetSourceEventId() != "01971a90-aaaa-7000-8000-000000000001" {
		t.Errorf("source_event_id = %q", msg.GetSourceEventId())
	}
	if msg.GetSourceTopic() != "chora.consumption.atom_session.completed.v1" {
		t.Errorf("source_topic = %q", msg.GetSourceTopic())
	}
	if msg.GetSourceSessionId() != "sess-42" {
		t.Errorf("source_session_id = %q", msg.GetSourceSessionId())
	}
	if msg.GetSourceTurnSeq() != 3 {
		t.Errorf("source_turn_seq = %d, want 3", msg.GetSourceTurnSeq())
	}
	if !msg.GetDailyCapHit() {
		t.Error("daily_cap_hit = false, want true")
	}
	if !msg.GetTriggersStageUp() {
		t.Error("triggers_stage_up = false, want true")
	}
	if got := msg.GetAwardedAt(); got == nil || !got.AsTime().Equal(t0) {
		t.Errorf("awarded_at not round-tripped: %+v", got)
	}
}

// TestWireCompat_CompanionKgNeighborRevealed_DecodesIntoGeneratedType pins the
// kg_neighbor_revealed encoder to its BINARY-bound schema (CHO-1729 outbox
// audit: 4 failed rows — JSON fell through with no encoder). Payload keys
// mirror growth.Service.PickKgNeighbor exactly.
func TestWireCompat_CompanionKgNeighborRevealed_DecodesIntoGeneratedType(t *testing.T) {
	t0 := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	env := milestoneEnvelope()
	payload := map[string]any{
		"companion_id":       "fam-0001",
		"owner_gcid":         env.GCID,
		"atom_id":            "019eb05f-d556-7284-b643-a7cc50932f8c",
		"revealed_via":       "user_pick",
		"graph_distance":     1,
		"revealed_at":        t0,
		"chora_companion_id": "fam-0001", // observability extra — must be ignored
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.kg_neighbor_revealed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var msg consumptionv1.CompanionKgNeighborRevealed
	if err := proto.Unmarshal(bz, &msg); err != nil {
		t.Fatalf("proto.Unmarshal into CompanionKgNeighborRevealed: %v", err)
	}
	if msg.GetEnvelope() == nil || msg.GetEnvelope().GetEventId() != env.EventID {
		t.Fatalf("envelope not round-tripped: %+v", msg.GetEnvelope())
	}
	if msg.GetCompanionId() != "fam-0001" {
		t.Errorf("companion_id = %q", msg.GetCompanionId())
	}
	if msg.GetOwnerGcid() != env.GCID {
		t.Errorf("owner_gcid = %q", msg.GetOwnerGcid())
	}
	if msg.GetAtomId() != "019eb05f-d556-7284-b643-a7cc50932f8c" {
		t.Errorf("atom_id = %q", msg.GetAtomId())
	}
	if msg.GetRevealedVia() != "user_pick" {
		t.Errorf("revealed_via = %q", msg.GetRevealedVia())
	}
	if msg.GetGraphDistance() != 1 {
		t.Errorf("graph_distance = %d, want 1", msg.GetGraphDistance())
	}
	if got := msg.GetRevealedAt(); got == nil || !got.AsTime().Equal(t0) {
		t.Errorf("revealed_at not round-tripped: %+v", got)
	}
}

// TestWireCompat_CompanionCreated_DecodesIntoGeneratedType pins the
// companion.created encoder to its BINARY-bound schema (CHO-1729 outbox audit:
// 1 failed row). Payload keys mirror companion_instance_handlers.createCompanion
// — note it emits only fields 2-5 + 8; species/age_stage/roster_position/
// created_at stay at proto3 defaults (elided on the wire).
func TestWireCompat_CompanionCreated_DecodesIntoGeneratedType(t *testing.T) {
	env := milestoneEnvelope()
	payload := map[string]any{
		"companion_id":              "fam-0002",
		"owner_gcid":                env.GCID,
		"name":                      "Ignis",
		"specialization":            "structural",
		"evolution_tier":            "scholar",
		"chora_companion_id":        "fam-0002",
		"chora_companion_class":     "structural",
		"chora_companion_evol_tier": "scholar",
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var msg consumptionv1.CompanionCreated
	if err := proto.Unmarshal(bz, &msg); err != nil {
		t.Fatalf("proto.Unmarshal into CompanionCreated: %v", err)
	}
	if msg.GetEnvelope() == nil || msg.GetEnvelope().GetEventId() != env.EventID {
		t.Fatalf("envelope not round-tripped: %+v", msg.GetEnvelope())
	}
	if msg.GetCompanionId() != "fam-0002" {
		t.Errorf("companion_id = %q", msg.GetCompanionId())
	}
	if msg.GetOwnerGcid() != env.GCID {
		t.Errorf("owner_gcid = %q", msg.GetOwnerGcid())
	}
	if msg.GetName() != "Ignis" {
		t.Errorf("name = %q", msg.GetName())
	}
	if msg.GetSpecialization() != "structural" {
		t.Errorf("specialization = %q", msg.GetSpecialization())
	}
	if msg.GetEvolutionTier() != "scholar" {
		t.Errorf("evolution_tier = %q", msg.GetEvolutionTier())
	}
}

// TestWireCompat_CompanionCreated_SpeciesEnumWhenPresent asserts the optional
// species slot (field 6, enum) round-trips when a future call site supplies it.
func TestWireCompat_CompanionCreated_SpeciesEnumWhenPresent(t *testing.T) {
	env := milestoneEnvelope()
	payload := map[string]any{
		"companion_id": "fam-0003",
		"species":      "phoenix",
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var msg consumptionv1.CompanionCreated
	if err := proto.Unmarshal(bz, &msg); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	if got := msg.GetSpecies(); got.String() != "COMPANION_SPECIES_PHOENIX" {
		t.Errorf("species = %v, want COMPANION_SPECIES_PHOENIX", got)
	}
}

// TestWireCompat_WeaknessGrown_DecodesIntoGeneratedType pins the weakness.grown
// encoder to its BINARY-bound Schema Registry schema (ADR-196 reward spine —
// the Curiosity-REWARD trigger emitted when a Growth Edge transitions to
// "grown"). Without an encoder the outbox JSON-falls-through and every grow
// publish dead-letters. final_strength is a proto `float` (wire type I32 /
// Fixed32) — the first non-double fixed-width slot in this package. Payload keys
// mirror the chora-consumption grow-transition emit exactly.
func TestWireCompat_WeaknessGrown_DecodesIntoGeneratedType(t *testing.T) {
	t0 := time.Date(2026, 6, 28, 11, 0, 0, 0, time.UTC)
	env := protomarshal.Envelope{
		EventID:        "01971a90-0000-7000-8000-000000000a01",
		IdempotencyKey: "grown:01971a90-aaaa-7000-8000-000000000007",
		TenantID:       "11111111-1111-7111-8111-111111111111",
		GCID:           "gcid-phyllis",
		OccurredAt:     t0,
		PublishedAt:    t0.Add(time.Millisecond),
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		Tracestate:     "vendor=value",
		SourceProject:  "chora-489812",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
	payload := map[string]any{
		"growth_edge_id":  "019eb05f-d556-7284-b643-a7cc50932f8c",
		"tenant_id":       "11111111-1111-7111-8111-111111111111",
		"learner_gcid":    "gcid-phyllis",
		"concept_label":   "Single Responsibility Principle",
		"concept_key":     "single-responsibility-principle",
		"final_strength":  0.05,
		"recovery_source": "drill_atom",
		"tags":            []string{"solid", "oop"},
		"grown_at":        t0,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.weakness.grown.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var msg consumptionv1.WeaknessGrown
	if err := proto.Unmarshal(bz, &msg); err != nil {
		t.Fatalf("proto.Unmarshal into WeaknessGrown: %v", err)
	}

	if msg.GetEnvelope() == nil {
		t.Fatal("envelope not decoded")
	}
	if got := msg.GetEnvelope().GetEventId(); got != env.EventID {
		t.Errorf("envelope.event_id = %q, want %q", got, env.EventID)
	}
	if got := msg.GetGrowthEdgeId(); got != "019eb05f-d556-7284-b643-a7cc50932f8c" {
		t.Errorf("growth_edge_id = %q", got)
	}
	if got := msg.GetTenantId(); got != "11111111-1111-7111-8111-111111111111" {
		t.Errorf("tenant_id = %q", got)
	}
	if got := msg.GetLearnerGcid(); got != "gcid-phyllis" {
		t.Errorf("learner_gcid = %q", got)
	}
	if got := msg.GetConceptLabel(); got != "Single Responsibility Principle" {
		t.Errorf("concept_label = %q", got)
	}
	if got := msg.GetConceptKey(); got != "single-responsibility-principle" {
		t.Errorf("concept_key = %q", got)
	}
	if got := msg.GetFinalStrength(); got != float32(0.05) {
		t.Errorf("final_strength = %v, want %v", got, float32(0.05))
	}
	if got := msg.GetRecoverySource(); got != "drill_atom" {
		t.Errorf("recovery_source = %q", got)
	}
	if got := msg.GetTags(); len(got) != 2 || got[0] != "solid" || got[1] != "oop" {
		t.Errorf("tags = %+v", got)
	}
	if got := msg.GetGrownAt(); got == nil || !got.AsTime().Equal(t0) {
		t.Errorf("grown_at not round-tripped: %+v", got)
	}
}
