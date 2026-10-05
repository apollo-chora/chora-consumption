// companion_retired_test — canonical round-trip guard for the CHO-2033 SP2
// retire event encoder (chora.consumption.companion.retired.v1). The topic is
// registered BINARY in the Pub/Sub Schema Registry, so the outbox MUST emit
// canonical protobuf — a JSON fallback dead-letters forever ("Invalid binary
// proto"). Each case builds the payload the retire handler emits, marshals via
// MarshalPayload, and decodes the bytes into the generated CompanionRetired (the
// registered schema shape). RED until the retired case lands in MarshalPayload.
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

// Full payload (every field the encoder reads set non-zero) proves each field
// number + wire type round-trips through the registered schema shape.
func TestFabric_CompanionRetired_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	retiredAt := time.Date(2026, 7, 5, 3, 30, 0, 0, time.UTC)
	payload := map[string]any{
		"companion_id": "fam-r1",
		"owner_gcid":   "gcid-r1",
		"species":      "penguin",
		"level":        7,
		"reason":       "owner_choice",
		"retired_at":   retiredAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.retired.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionRetired
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen CompanionRetired: %v", err)
	}
	if m.GetEnvelope() == nil || m.GetEnvelope().GetTenantId() == "" {
		t.Errorf("envelope (1) missing/empty")
	}
	if m.GetCompanionId() != "fam-r1" {
		t.Errorf("companion_id (2) = %q", m.GetCompanionId())
	}
	if m.GetOwnerGcid() != "gcid-r1" {
		t.Errorf("owner_gcid (3) = %q", m.GetOwnerGcid())
	}
	if m.GetSpecies() != consumptionv1.CompanionSpecies_COMPANION_SPECIES_PENGUIN {
		t.Errorf("species (4) = %v want PENGUIN", m.GetSpecies())
	}
	if m.GetLevel() != 7 {
		t.Errorf("level (5) = %d want 7", m.GetLevel())
	}
	if m.GetReason() != "owner_choice" {
		t.Errorf("reason (6) = %q", m.GetReason())
	}
	if m.GetRetiredAt() == nil || !m.GetRetiredAt().AsTime().Equal(retiredAt) {
		t.Errorf("retired_at (7) = %v want %v", m.GetRetiredAt(), retiredAt)
	}
}

// The SHAPE the live handler actually emits — the consumption Instance carries
// no species enum / int level, so those keys are absent. It MUST still
// round-trip (species=UNSPECIFIED, level=0 are valid proto3 defaults).
func TestFabric_CompanionRetired_MinimalHandlerPayload(t *testing.T) {
	env := fixedEnvelope()
	retiredAt := time.Date(2026, 7, 5, 3, 45, 0, 0, time.UTC)
	payload := map[string]any{
		"companion_id": "fam-r2",
		"owner_gcid":   "gcid-r2",
		"reason":       "owner_choice",
		"retired_at":   retiredAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.retired.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionRetired
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.GetCompanionId() != "fam-r2" || m.GetOwnerGcid() != "gcid-r2" {
		t.Errorf("id/gcid mismatch: %q / %q", m.GetCompanionId(), m.GetOwnerGcid())
	}
	if m.GetSpecies() != consumptionv1.CompanionSpecies_COMPANION_SPECIES_UNSPECIFIED {
		t.Errorf("species should default UNSPECIFIED, got %v", m.GetSpecies())
	}
	if m.GetRetiredAt() == nil || !m.GetRetiredAt().AsTime().Equal(retiredAt) {
		t.Errorf("retired_at (7) = %v", m.GetRetiredAt())
	}
}
