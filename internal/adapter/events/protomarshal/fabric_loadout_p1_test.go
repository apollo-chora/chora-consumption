// fabric_loadout_p1_test — canonical round-trips for the CHO-2013 P1 loadout
// lifecycle + specialization events. The P0 emitters (growth stage-up slot
// announcements, PathUnlocker mints, equip/unequip) shipped WITHOUT encoder
// cases, so every publish JSON-fell-back and the BINARY Schema Registry
// rejected it (INVALID_BINARY_PROTO_MESSAGE, 6 deadlettered rows on
// 2026-07-03). Same guardrail shape as fabric_canonical_test: representative
// payload with every encoder-read field non-zero → MarshalPayload → decode
// into the generated canonical struct → per-field assertions.
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

func TestFabric_CompanionSkillSlotUnlocked_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	unlockedAt := time.Date(2026, 7, 3, 5, 30, 0, 0, time.UTC)
	payload := map[string]any{
		"companion_id":       "fam-slot-1",
		"owner_gcid":         "gcid-slot-1",
		"stage_from":         1,
		"stage_to":           2,
		"slots_before":       2,
		"slots_after":        3,
		"unlocked_at":        unlockedAt,
		"chora_companion_id": "fam-slot-1",
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.skill_slot_unlocked.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionSkillSlotUnlocked
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen CompanionSkillSlotUnlocked: %v", err)
	}
	if m.GetEnvelope() == nil || m.GetEnvelope().GetTenantId() != env.TenantID {
		t.Errorf("envelope (1) missing or tenant_id drift: %+v", m.GetEnvelope())
	}
	if got := m.GetCompanionId(); got != "fam-slot-1" {
		t.Errorf("companion_id (2): got %q", got)
	}
	if got := m.GetOwnerGcid(); got != "gcid-slot-1" {
		t.Errorf("owner_gcid (3): got %q", got)
	}
	if got := m.GetStageFrom(); got != 1 {
		t.Errorf("stage_from (4): got %d", got)
	}
	if got := m.GetStageTo(); got != 2 {
		t.Errorf("stage_to (5): got %d", got)
	}
	if got := m.GetSlotsBefore(); got != 2 {
		t.Errorf("slots_before (6): got %d", got)
	}
	if got := m.GetSlotsAfter(); got != 3 {
		t.Errorf("slots_after (7): got %d", got)
	}
	if got := m.GetUnlockedAt(); got == nil || !got.AsTime().Equal(unlockedAt) {
		t.Errorf("unlocked_at (8): got %v", got)
	}
}

func TestFabric_CompanionSkillGranted_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	grantedAt := time.Date(2026, 7, 3, 5, 31, 0, 0, time.UTC)
	payload := map[string]any{
		"companion_id":              "fam-grant-1",
		"owner_gcid":                "gcid-grant-1",
		"skill_key":                 "recap_scribe",
		"skill_kind":                "active",
		"unlocked_via":              "species_path",
		"unlocked_at_stage":         2,
		"equipped":                  true,
		"granted_at":                grantedAt,
		"chora_companion_id":        "fam-grant-1",
		"chora_companion_skill_key": "recap_scribe",
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.skill_granted.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionSkillGranted
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen CompanionSkillGranted: %v", err)
	}
	if m.GetEnvelope() == nil || m.GetEnvelope().GetEventId() != env.EventID {
		t.Errorf("envelope (1) missing or event_id drift: %+v", m.GetEnvelope())
	}
	if got := m.GetCompanionId(); got != "fam-grant-1" {
		t.Errorf("companion_id (2): got %q", got)
	}
	if got := m.GetOwnerGcid(); got != "gcid-grant-1" {
		t.Errorf("owner_gcid (3): got %q", got)
	}
	if got := m.GetSkillKey(); got != "recap_scribe" {
		t.Errorf("skill_key (4): got %q", got)
	}
	if got := m.GetSkillKind(); got != "active" {
		t.Errorf("skill_kind (5): got %q", got)
	}
	if got := m.GetUnlockedVia(); got != "species_path" {
		t.Errorf("unlocked_via (6): got %q", got)
	}
	if got := m.GetUnlockedAtStage(); got != 2 {
		t.Errorf("unlocked_at_stage (7): got %d", got)
	}
	if got := m.GetEquipped(); got != true {
		t.Errorf("equipped (8): got %v", got)
	}
	if got := m.GetGrantedAt(); got == nil || !got.AsTime().Equal(grantedAt) {
		t.Errorf("granted_at (9): got %v", got)
	}
}

func TestFabric_CompanionLoadoutChanged_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	changedAt := time.Date(2026, 7, 3, 5, 32, 0, 0, time.UTC)
	payload := map[string]any{
		"companion_id":              "fam-load-1",
		"owner_gcid":                "gcid-load-1",
		"skill_key":                 "explain_anew",
		"action":                    "equip",
		"equipped_skills":           []string{"explain_anew", "progress_mirror"},
		"slots_used":                2,
		"skill_slots_unlocked":      3,
		"changed_at":                changedAt,
		"chora_companion_id":        "fam-load-1",
		"chora_companion_skill_key": "explain_anew",
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.loadout_changed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionLoadoutChanged
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen CompanionLoadoutChanged: %v", err)
	}
	if m.GetEnvelope() == nil || m.GetEnvelope().GetGcid() != env.GCID {
		t.Errorf("envelope (1) missing or gcid drift: %+v", m.GetEnvelope())
	}
	if got := m.GetCompanionId(); got != "fam-load-1" {
		t.Errorf("companion_id (2): got %q", got)
	}
	if got := m.GetOwnerGcid(); got != "gcid-load-1" {
		t.Errorf("owner_gcid (3): got %q", got)
	}
	if got := m.GetSkillKey(); got != "explain_anew" {
		t.Errorf("skill_key (4): got %q", got)
	}
	if got := m.GetAction(); got != "equip" {
		t.Errorf("action (5): got %q", got)
	}
	if got := m.GetEquippedSkills(); len(got) != 2 || got[0] != "explain_anew" || got[1] != "progress_mirror" {
		t.Errorf("equipped_skills (6): got %v", got)
	}
	if got := m.GetSlotsUsed(); got != 2 {
		t.Errorf("slots_used (7): got %d", got)
	}
	if got := m.GetSkillSlotsUnlocked(); got != 3 {
		t.Errorf("skill_slots_unlocked (8): got %d", got)
	}
	if got := m.GetChangedAt(); got == nil || !got.AsTime().Equal(changedAt) {
		t.Errorf("changed_at (9): got %v", got)
	}
}

func TestFabric_CompanionSpecializationChanged_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	changedAt := time.Date(2026, 7, 3, 5, 33, 0, 0, time.UTC)
	payload := map[string]any{
		"companion_id":            "fam-spec-1",
		"owner_gcid":              "gcid-spec-1",
		"previous_specialization": "general",
		"new_specialization":      "Astronomy Foundations",
		"memory_handling":         "carry_over",
		"user_reason":             "bound to goal",
		"changed_at":              changedAt,
		"chora_companion_id":      "fam-spec-1",
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.specialization_changed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionSpecializationChanged
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen CompanionSpecializationChanged: %v", err)
	}
	if m.GetEnvelope() == nil || m.GetEnvelope().GetIdempotencyKey() != env.IdempotencyKey {
		t.Errorf("envelope (1) missing or idempotency_key drift: %+v", m.GetEnvelope())
	}
	if got := m.GetCompanionId(); got != "fam-spec-1" {
		t.Errorf("companion_id (2): got %q", got)
	}
	if got := m.GetOwnerGcid(); got != "gcid-spec-1" {
		t.Errorf("owner_gcid (3): got %q", got)
	}
	if got := m.GetPreviousSpecialization(); got != "general" {
		t.Errorf("previous_specialization (4): got %q", got)
	}
	if got := m.GetNewSpecialization(); got != "Astronomy Foundations" {
		t.Errorf("new_specialization (5): got %q", got)
	}
	if got := m.GetMemoryHandling(); got != "carry_over" {
		t.Errorf("memory_handling (6): got %q", got)
	}
	if got := m.GetUserReason(); got != "bound to goal" {
		t.Errorf("user_reason (7): got %q", got)
	}
	if got := m.GetChangedAt(); got == nil || !got.AsTime().Equal(changedAt) {
		t.Errorf("changed_at (8): got %v", got)
	}
}
