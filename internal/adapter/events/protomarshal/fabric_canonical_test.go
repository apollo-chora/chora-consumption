// fabric_canonical_test — golden guardrail for the chora-consumption hand-rolled
// outbox encoders that the 2026-07-01 event-fabric audit found deadlettering with
// INVALID_BINARY_PROTO_MESSAGE / "Could not parse binary message". Each case
// builds a representative payload (ALL fields the encoder reads set to non-zero
// values), marshals via MarshalPayload, and decodes the bytes into the generated
// canonical struct (chora-contracts/gen/go/chora/consumption/v1 — the registered
// Pub/Sub schema shape). A field-number or wire-type drift makes proto.Unmarshal
// fail (proto3 string UTF-8 validation) or lands a value in the wrong field; the
// per-field assertions — weighted to the high field numbers (timestamps + tail
// fields, where drift hides) — catch a silent mis-field. This is the durable
// regression gate: every encoded consumption event MUST round-trip through its
// registered schema shape.
//
// Mirrors services/chora-delivery/.../protomarshal/fabric_canonical_test.go
// (the submission.released fabric fix, origin/main d1ac3fd93).
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

func TestFabric_CompanionExpAwarded_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	awardedAt := time.Date(2026, 6, 30, 9, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"companion_id":      "fam-e1",
		"owner_gcid":        "gcid-e1",
		"exp_delta":         15,
		"exp_total_after":   115,
		"source":            "atom_session",
		"source_event_id":   "evt-e1",
		"source_topic":      "chora.consumption.atom_session.completed.v1",
		"source_session_id": "sess-e1",
		"source_turn_seq":   3,
		"daily_cap_hit":     true,
		"triggers_stage_up": true,
		"awarded_at":        awardedAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.exp_awarded.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionExpAwarded
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen CompanionExpAwarded: %v", err)
	}
	if m.GetEnvelope() == nil || m.GetEnvelope().GetTenantId() == "" {
		t.Errorf("envelope (1) missing/empty")
	}
	if m.GetExpDelta() != 15 {
		t.Errorf("exp_delta (4) = %d", m.GetExpDelta())
	}
	if m.GetExpTotalAfter() != 115 {
		t.Errorf("exp_total_after (5) = %d", m.GetExpTotalAfter())
	}
	if m.GetSourceSessionId() != "sess-e1" {
		t.Errorf("source_session_id (9) = %q", m.GetSourceSessionId())
	}
	if m.GetSourceTurnSeq() != 3 {
		t.Errorf("source_turn_seq (10) = %d", m.GetSourceTurnSeq())
	}
	if !m.GetDailyCapHit() {
		t.Errorf("daily_cap_hit (11) = false")
	}
	if !m.GetTriggersStageUp() {
		t.Errorf("triggers_stage_up (12) = false")
	}
	if m.GetAwardedAt() == nil || !m.GetAwardedAt().AsTime().Equal(awardedAt) {
		t.Errorf("awarded_at (13) = %v want %v", m.GetAwardedAt(), awardedAt)
	}
}

func TestFabric_DailyDoseServed_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	servedAt := time.Date(2026, 6, 30, 6, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"learner_gcid": "gcid-d1",
		"entries": []map[string]any{
			{"atom_id": "atom-1", "topic": "algebra", "title": "Quadratics", "dose_reason": int32(2)},
		},
		"size":      1,
		"message":   "Your dose is ready",
		"served_at": servedAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.daily_dose.served.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.DailyDoseServed
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen DailyDoseServed: %v", err)
	}
	if m.GetLearnerGcid() != "gcid-d1" {
		t.Errorf("learner_gcid (2) = %q", m.GetLearnerGcid())
	}
	if len(m.GetEntries()) != 1 {
		t.Fatalf("entries (3) len = %d want 1", len(m.GetEntries()))
	}
	if got := m.GetEntries()[0]; got.GetAtomId() != "atom-1" || got.GetTitle() != "Quadratics" ||
		got.GetDoseReason() != consumptionv1.DoseReason_DOSE_REASON_FRESH {
		t.Errorf("entry[0] mis-decoded: %+v", got)
	}
	if m.GetSize() != 1 {
		t.Errorf("size (4) = %d", m.GetSize())
	}
	if m.GetMessage() != "Your dose is ready" {
		t.Errorf("message (5) = %q", m.GetMessage())
	}
	if m.GetServedAt() == nil || !m.GetServedAt().AsTime().Equal(servedAt) {
		t.Errorf("served_at (6) = %v want %v", m.GetServedAt(), servedAt)
	}
}

// TestFabric_HexagonFogGenerated_CanonicalRoundTrip is the RED-driving case:
// chora.consumption.kg_hexagon_fog.generated.v1 had NO encoder in MarshalPayload
// so it JSON-fell-back and Schema Registry rejected it. Uses the exact payload
// keys the sole producer emits (kg_publisher.PublishHexagonFogGenerated:
// generated_by_run_id / generated_by_model_id), plus the full schema tail
// (user_gcid / neighbors / generated_at) to prove every field number + wire type.
func TestFabric_HexagonFogGenerated_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	generatedAt := time.Date(2026, 6, 30, 8, 30, 0, 0, time.UTC)
	payload := map[string]any{
		"generated_by_run_id":   "run-f1",   // -> run_id (2)
		"cluster_id":            "clu-f1",   // -> cluster_id (3)
		"exploration_id":        "exp-f1",   // -> exploration_id (4)
		"user_gcid":             "gcid-f1",  // -> user_gcid (5)
		"focal_atom_id":         "atom-f1",  // -> focal_atom_id (6)
		"generated_by_model_id": "gemini-x", // -> model_id (8)
		"total_cost_micros":     int64(123456),
		"input_tokens":          int64(800),
		"output_tokens":         int64(200),
		"generated_at":          generatedAt,
		"neighbors": []map[string]any{
			{
				"atom_id":     "atom-n1",
				"relation":    "prerequisite_of",
				"confidence":  float32(0.75),
				"fog_label":   "Before you can grasp this...",
				"is_junction": true,
			},
		},
		// Envelope IMDA evidence extras the producer stamps (fields 14/15 of
		// the nested envelope); must be tolerated, not mis-fielded.
		"chora_imda_dimension": "safety_and_robustness",
		"imda_lifecycle_stage": "runtime",
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.kg_hexagon_fog.generated.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.HexagonFogGenerated
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen HexagonFogGenerated: %v", err)
	}
	if m.GetEnvelope() == nil || m.GetEnvelope().GetTenantId() == "" {
		t.Errorf("envelope (1) missing/empty")
	}
	if m.GetRunId() != "run-f1" {
		t.Errorf("run_id (2) = %q", m.GetRunId())
	}
	if m.GetClusterId() != "clu-f1" {
		t.Errorf("cluster_id (3) = %q", m.GetClusterId())
	}
	if m.GetExplorationId() != "exp-f1" {
		t.Errorf("exploration_id (4) = %q", m.GetExplorationId())
	}
	if m.GetUserGcid() != "gcid-f1" {
		t.Errorf("user_gcid (5) = %q", m.GetUserGcid())
	}
	if m.GetFocalAtomId() != "atom-f1" {
		t.Errorf("focal_atom_id (6) = %q", m.GetFocalAtomId())
	}
	if len(m.GetNeighbors()) != 1 {
		t.Fatalf("neighbors (7) len = %d want 1", len(m.GetNeighbors()))
	}
	if n := m.GetNeighbors()[0]; n.GetAtomId() != "atom-n1" ||
		n.GetRelation() != consumptionv1.NeighborRelation_NEIGHBOR_RELATION_PREREQUISITE_OF ||
		n.GetConfidence() != 0.75 || n.GetFogLabel() != "Before you can grasp this..." || !n.GetIsJunction() {
		t.Errorf("neighbor[0] mis-decoded: %+v", n)
	}
	if m.GetModelId() != "gemini-x" {
		t.Errorf("model_id (8) = %q", m.GetModelId())
	}
	if m.GetTotalCostMicros() != 123456 {
		t.Errorf("total_cost_micros (9) = %d", m.GetTotalCostMicros())
	}
	if m.GetInputTokens() != 800 {
		t.Errorf("input_tokens (10) = %d", m.GetInputTokens())
	}
	if m.GetOutputTokens() != 200 {
		t.Errorf("output_tokens (11) = %d", m.GetOutputTokens())
	}
	if m.GetGeneratedAt() == nil || !m.GetGeneratedAt().AsTime().Equal(generatedAt) {
		t.Errorf("generated_at (12) = %v want %v", m.GetGeneratedAt(), generatedAt)
	}
}

// TestFabric_ExplorationFocalChanged_CanonicalRoundTrip is a Class-D-contract
// case: chora.consumption.kg_exploration.focal_changed.v1 was emitted but had
// NO encoder in MarshalPayload (and its topic was never provisioned), so it
// JSON-fell-back + deadlettered NotFound. Sets the exact keys the sole producer
// emits (kg_publisher.PublishExplorationFocalChanged), plus a non-schema
// `trail_id` extra that MUST be tolerated (dropped), and the full schema tail
// (user_gcid / traversed_at) to pin every field number + wire type against the
// registered ExplorationFocalNodeChanged schema shape.
func TestFabric_ExplorationFocalChanged_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	traversedAt := time.Date(2026, 6, 30, 10, 15, 0, 0, time.UTC)
	payload := map[string]any{
		"exploration_id":     "exp-fc1",       // -> exploration_id (2)
		"cluster_id":         "clu-fc1",       // -> cluster_id (3)
		"user_gcid":          "gcid-fc1",      // -> user_gcid (4)
		"from_focal_atom_id": "atom-from-fc1", // -> from_focal_atom_id (5)
		"to_focal_atom_id":   "atom-to-fc1",   // -> to_focal_atom_id (6)
		"relation":           "extends",       // -> relation (7) enum
		"step_index":         4,               // -> step_index (8)
		"traversed_at":       traversedAt,     // -> traversed_at (9)
		// Non-schema key the producer also emits — must be tolerated (dropped),
		// not mis-fielded.
		"trail_id": "trail-fc1",
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.kg_exploration.focal_changed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.ExplorationFocalNodeChanged
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen ExplorationFocalNodeChanged: %v", err)
	}
	if m.GetEnvelope() == nil || m.GetEnvelope().GetTenantId() == "" {
		t.Errorf("envelope (1) missing/empty")
	}
	if m.GetExplorationId() != "exp-fc1" {
		t.Errorf("exploration_id (2) = %q", m.GetExplorationId())
	}
	if m.GetClusterId() != "clu-fc1" {
		t.Errorf("cluster_id (3) = %q", m.GetClusterId())
	}
	if m.GetUserGcid() != "gcid-fc1" {
		t.Errorf("user_gcid (4) = %q", m.GetUserGcid())
	}
	if m.GetFromFocalAtomId() != "atom-from-fc1" {
		t.Errorf("from_focal_atom_id (5) = %q", m.GetFromFocalAtomId())
	}
	if m.GetToFocalAtomId() != "atom-to-fc1" {
		t.Errorf("to_focal_atom_id (6) = %q", m.GetToFocalAtomId())
	}
	if m.GetRelation() != consumptionv1.NeighborRelation_NEIGHBOR_RELATION_EXTENDS {
		t.Errorf("relation (7) = %v want EXTENDS", m.GetRelation())
	}
	if m.GetStepIndex() != 4 {
		t.Errorf("step_index (8) = %d", m.GetStepIndex())
	}
	if m.GetTraversedAt() == nil || !m.GetTraversedAt().AsTime().Equal(traversedAt) {
		t.Errorf("traversed_at (9) = %v want %v", m.GetTraversedAt(), traversedAt)
	}
}

func TestFabric_CompanionKgNeighborRevealed_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	revealedAt := time.Date(2026, 6, 30, 7, 15, 0, 0, time.UTC)
	payload := map[string]any{
		"companion_id":   "fam-k1",
		"owner_gcid":     "gcid-k1",
		"atom_id":        "atom-k1",
		"revealed_via":   "stage_up",
		"graph_distance": 2,
		"revealed_at":    revealedAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.kg_neighbor_revealed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionKgNeighborRevealed
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen CompanionKgNeighborRevealed: %v", err)
	}
	if m.GetAtomId() != "atom-k1" {
		t.Errorf("atom_id (4) = %q", m.GetAtomId())
	}
	if m.GetRevealedVia() != "stage_up" {
		t.Errorf("revealed_via (5) = %q", m.GetRevealedVia())
	}
	if m.GetGraphDistance() != 2 {
		t.Errorf("graph_distance (6) = %d", m.GetGraphDistance())
	}
	if m.GetRevealedAt() == nil || !m.GetRevealedAt().AsTime().Equal(revealedAt) {
		t.Errorf("revealed_at (7) = %v want %v", m.GetRevealedAt(), revealedAt)
	}
}

func TestFabric_CompanionChatTurnCompleted_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	startedAt := time.Date(2026, 6, 30, 9, 30, 0, 0, time.UTC)
	completedAt := startedAt.Add(2 * time.Second)
	payload := map[string]any{
		"session_id":        "sess-c1",
		"turn_id":           "turn-c1",
		"gcid":              "gcid-c1",
		"tenant_id":         "tenant-c1",
		"companion_id":      "fam-c1",
		"engine_session_id": "eng-c1",
		"model":             "gemini-2.5",
		"output_tokens":     42,
		"mana_charged":      7,
		"mana_tier":         "standard",
		"tool_calls_count":  2,
		"finish_reason":     "stop",
		"started_at":        startedAt,
		"completed_at":      completedAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.chat_turn_completed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionChatTurnCompleted
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen CompanionChatTurnCompleted: %v", err)
	}
	if m.GetOutputTokens() != 42 {
		t.Errorf("output_tokens (9) = %d", m.GetOutputTokens())
	}
	if m.GetManaCharged() != 7 {
		t.Errorf("mana_charged (10) = %d", m.GetManaCharged())
	}
	if m.GetManaTier() != "standard" {
		t.Errorf("mana_tier (11) = %q", m.GetManaTier())
	}
	if m.GetToolCallsCount() != 2 {
		t.Errorf("tool_calls_count (12) = %d", m.GetToolCallsCount())
	}
	if m.GetFinishReason() != "stop" {
		t.Errorf("finish_reason (13) = %q", m.GetFinishReason())
	}
	if m.GetStartedAt() == nil || !m.GetStartedAt().AsTime().Equal(startedAt) {
		t.Errorf("started_at (14) = %v want %v", m.GetStartedAt(), startedAt)
	}
	if m.GetCompletedAt() == nil || !m.GetCompletedAt().AsTime().Equal(completedAt) {
		t.Errorf("completed_at (15) = %v want %v", m.GetCompletedAt(), completedAt)
	}
}

func TestFabric_CompanionCreated_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	createdAt := time.Date(2026, 6, 30, 5, 45, 0, 0, time.UTC)
	payload := map[string]any{
		"companion_id":    "fam-n1",
		"owner_gcid":      "gcid-n1",
		"name":            "Emberwing",
		"specialization":  "history",
		"species":         "dragon",
		"age_stage":       "hatchling",
		"evolution_tier":  "tier_1",
		"roster_position": 3,
		"created_at":      createdAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionCreated
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen CompanionCreated: %v", err)
	}
	if m.GetSpecies() != consumptionv1.CompanionSpecies_COMPANION_SPECIES_DRAGON {
		t.Errorf("species (6) = %v want DRAGON", m.GetSpecies())
	}
	if m.GetAgeStage() != "hatchling" {
		t.Errorf("age_stage (7) = %q", m.GetAgeStage())
	}
	if m.GetEvolutionTier() != "tier_1" {
		t.Errorf("evolution_tier (8) = %q", m.GetEvolutionTier())
	}
	if m.GetRosterPosition() != 3 {
		t.Errorf("roster_position (9) = %d", m.GetRosterPosition())
	}
	if m.GetCreatedAt() == nil || !m.GetCreatedAt().AsTime().Equal(createdAt) {
		t.Errorf("created_at (10) = %v want %v", m.GetCreatedAt(), createdAt)
	}
}

func TestFabric_CompanionBonded_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	bondedAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"companion_id": "comp-b1",
		"owner_gcid":   "gcid-b1",
		"species":      "owl",
		"level":        3,
		"bonded_at":    bondedAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.bonded.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionBonded
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen CompanionBonded: %v", err)
	}
	if m.GetEnvelope() == nil || m.GetEnvelope().GetTenantId() == "" {
		t.Errorf("envelope (1) missing/empty")
	}
	if m.GetCompanionId() != "comp-b1" {
		t.Errorf("companion_id (2) = %q", m.GetCompanionId())
	}
	if m.GetOwnerGcid() != "gcid-b1" {
		t.Errorf("owner_gcid (3) = %q", m.GetOwnerGcid())
	}
	if m.GetSpecies() != consumptionv1.CompanionSpecies_COMPANION_SPECIES_OWL {
		t.Errorf("species (4) = %v want OWL", m.GetSpecies())
	}
	if m.GetLevel() != 3 {
		t.Errorf("level (5) = %d", m.GetLevel())
	}
	if m.GetBondedAt() == nil || !m.GetBondedAt().AsTime().Equal(bondedAt) {
		t.Errorf("bonded_at (6) = %v want %v", m.GetBondedAt(), bondedAt)
	}
}

func TestFabric_CompanionExposureGranted_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	grantedAt := time.Date(2026, 8, 1, 10, 5, 0, 0, time.UTC)
	expiresAt := grantedAt.Add(24 * time.Hour)
	payload := map[string]any{
		"companion_id":      "comp-e1",
		"exposure_grant_id": "grant-e1",
		"learner_gcid":      "gcid-e1",
		"content_atom_id":   "atom-e1",
		"exposure_scope":    "knowledge-map",
		"granted_at":        grantedAt,
		"expires_at":        expiresAt,
		"grant_reason":      "goal-attached",
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.exposure_granted.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionExposureGranted
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen CompanionExposureGranted: %v", err)
	}
	if m.GetExposureGrantId() != "grant-e1" {
		t.Errorf("exposure_grant_id (3) = %q", m.GetExposureGrantId())
	}
	if m.GetLearnerGcid() != "gcid-e1" {
		t.Errorf("learner_gcid (4) = %q", m.GetLearnerGcid())
	}
	if m.GetContentAtomId() != "atom-e1" {
		t.Errorf("content_atom_id (5) = %q", m.GetContentAtomId())
	}
	if m.GetExposureScope() != "knowledge-map" {
		t.Errorf("exposure_scope (6) = %q", m.GetExposureScope())
	}
	if m.GetGrantedAt() == nil || !m.GetGrantedAt().AsTime().Equal(grantedAt) {
		t.Errorf("granted_at (7) = %v want %v", m.GetGrantedAt(), grantedAt)
	}
	if m.GetExpiresAt() == nil || !m.GetExpiresAt().AsTime().Equal(expiresAt) {
		t.Errorf("expires_at (8) = %v want %v", m.GetExpiresAt(), expiresAt)
	}
	if m.GetGrantReason() != "goal-attached" {
		t.Errorf("grant_reason (9) = %q", m.GetGrantReason())
	}
}

func TestFabric_CompanionLeveledUp_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	leveledUpAt := time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"companion_id":   "comp-l1",
		"owner_gcid":     "gcid-l1",
		"species":        "fox",
		"previous_level": 4,
		"level":          5,
		"leveled_up_at":  leveledUpAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.leveled_up.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionLeveledUp
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen CompanionLeveledUp: %v", err)
	}
	if m.GetSpecies() != consumptionv1.CompanionSpecies_COMPANION_SPECIES_FOX {
		t.Errorf("species (4) = %v want FOX", m.GetSpecies())
	}
	if m.GetPreviousLevel() != 4 {
		t.Errorf("previous_level (5) = %d", m.GetPreviousLevel())
	}
	if m.GetLevel() != 5 {
		t.Errorf("level (6) = %d", m.GetLevel())
	}
	if m.GetLeveledUpAt() == nil || !m.GetLeveledUpAt().AsTime().Equal(leveledUpAt) {
		t.Errorf("leveled_up_at (7) = %v want %v", m.GetLeveledUpAt(), leveledUpAt)
	}
}

func TestFabric_CompanionMemoryEviction_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	oldestAt := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	evictedAt := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"companion_id":         "comp-m1",
		"owner_gcid":           "gcid-m1",
		"memory_bank_app_name": "familiar:comp-m1",
		"batch_count":          12,
		"reason":               "capacity",
		"capacity":             200,
		"retained_count":       188,
		"oldest_retained_at":   oldestAt,
		"evicted_at":           evictedAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.memory_eviction.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionMemoryEviction
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen CompanionMemoryEviction: %v", err)
	}
	if m.GetMemoryBankAppName() != "familiar:comp-m1" {
		t.Errorf("memory_bank_app_name (4) = %q", m.GetMemoryBankAppName())
	}
	if m.GetBatchCount() != 12 {
		t.Errorf("batch_count (5) = %d", m.GetBatchCount())
	}
	if m.GetReason() != "capacity" {
		t.Errorf("reason (6) = %q", m.GetReason())
	}
	if m.GetCapacity() != 200 {
		t.Errorf("capacity (7) = %d", m.GetCapacity())
	}
	if m.GetRetainedCount() != 188 {
		t.Errorf("retained_count (8) = %d", m.GetRetainedCount())
	}
	if m.GetOldestRetainedAt() == nil || !m.GetOldestRetainedAt().AsTime().Equal(oldestAt) {
		t.Errorf("oldest_retained_at (9) = %v want %v", m.GetOldestRetainedAt(), oldestAt)
	}
	if m.GetEvictedAt() == nil || !m.GetEvictedAt().AsTime().Equal(evictedAt) {
		t.Errorf("evicted_at (10) = %v want %v", m.GetEvictedAt(), evictedAt)
	}
}

func TestFabric_CompanionSessionStarted_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	startedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"companion_id":    "comp-s1",
		"session_id":      "sess-s1",
		"learner_gcid":    "gcid-s1",
		"session_type":    "chat",
		"context_atom_id": "atom-s1",
		"started_at":      startedAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.session_started.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionSessionStarted
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen CompanionSessionStarted: %v", err)
	}
	if m.GetSessionId() != "sess-s1" {
		t.Errorf("session_id (3) = %q", m.GetSessionId())
	}
	if m.GetLearnerGcid() != "gcid-s1" {
		t.Errorf("learner_gcid (4) = %q", m.GetLearnerGcid())
	}
	if m.GetSessionType() != "chat" {
		t.Errorf("session_type (5) = %q", m.GetSessionType())
	}
	if m.GetContextAtomId() != "atom-s1" {
		t.Errorf("context_atom_id (6) = %q", m.GetContextAtomId())
	}
	if m.GetStartedAt() == nil || !m.GetStartedAt().AsTime().Equal(startedAt) {
		t.Errorf("started_at (7) = %v want %v", m.GetStartedAt(), startedAt)
	}
}

func TestFabric_CompanionSkinEquipped_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	equippedAt := time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"companion_id":     "comp-k1",
		"owner_gcid":       "gcid-k1",
		"digital_skin_id":  "skin-k1",
		"previous_skin_id": "skin-k0",
		"equipped_at":      equippedAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.skin_equipped.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionSkinEquipped
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen CompanionSkinEquipped: %v", err)
	}
	if m.GetCompanionId() != "comp-k1" {
		t.Errorf("companion_id (2) = %q", m.GetCompanionId())
	}
	if m.GetOwnerGcid() != "gcid-k1" {
		t.Errorf("owner_gcid (3) = %q", m.GetOwnerGcid())
	}
	if m.GetDigitalSkinId() != "skin-k1" {
		t.Errorf("digital_skin_id (4) = %q", m.GetDigitalSkinId())
	}
	if m.GetPreviousSkinId() != "skin-k0" {
		t.Errorf("previous_skin_id (5) = %q", m.GetPreviousSkinId())
	}
	if m.GetEquippedAt() == nil || !m.GetEquippedAt().AsTime().Equal(equippedAt) {
		t.Errorf("equipped_at (6) = %v want %v", m.GetEquippedAt(), equippedAt)
	}
}
