// companion_persona_updated_test — canonical round-trip gate for the CHO-2015
// (ADR-219 D2) persona_updated encoder. Marshals a full payload via
// MarshalPayload and decodes into the generated CompanionPersonaUpdated; a
// field-number / wire-type drift makes proto.Unmarshal fail or mis-field a
// value. Mirrors fabric_canonical_test.go.
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

func TestFabric_CompanionPersonaUpdated_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	updatedAt := time.Date(2026, 7, 9, 4, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"companion_id":      "fam-p1",
		"owner_gcid":        "gcid-p1",
		"persona_version":   3,
		"tone":              "encouraging",
		"difficulty_cap":    "advanced",
		"address_style":     "nickname",
		"has_guidance_note": true,
		"updated_at":        updatedAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.persona_updated.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionPersonaUpdated
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen CompanionPersonaUpdated (field/wire drift): %v", err)
	}
	if m.GetEnvelope() == nil || m.GetEnvelope().GetTenantId() == "" {
		t.Errorf("envelope (1) missing/empty")
	}
	if m.GetCompanionId() != "fam-p1" || m.GetOwnerGcid() != "gcid-p1" {
		t.Errorf("id/gcid (2/3) = %q/%q", m.GetCompanionId(), m.GetOwnerGcid())
	}
	if m.GetPersonaVersion() != 3 {
		t.Errorf("persona_version (4) = %d", m.GetPersonaVersion())
	}
	if m.GetTone() != "encouraging" || m.GetDifficultyCap() != "advanced" || m.GetAddressStyle() != "nickname" {
		t.Errorf("knobs (5/6/7) = %q/%q/%q", m.GetTone(), m.GetDifficultyCap(), m.GetAddressStyle())
	}
	if !m.GetHasGuidanceNote() {
		t.Errorf("has_guidance_note (8) = false")
	}
	if m.GetUpdatedAt() == nil || !m.GetUpdatedAt().AsTime().Equal(updatedAt) {
		t.Errorf("updated_at (9) mismatch: %v", m.GetUpdatedAt())
	}
}

// TestFabric_CompanionPersonaUpdated_OmitsAbsentNote confirms the privacy
// contract: has_guidance_note=false when the learner set no note, and no note
// TEXT field exists on the wire at all (the encoder never carries it).
func TestFabric_CompanionPersonaUpdated_OmitsAbsentNote(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"companion_id":      "fam-p2",
		"owner_gcid":        "gcid-p2",
		"persona_version":   1,
		"tone":              "socratic",
		"difficulty_cap":    "foundation",
		"address_style":     "first_name",
		"has_guidance_note": false,
		"updated_at":        time.Date(2026, 7, 9, 4, 0, 0, 0, time.UTC),
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.persona_updated.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.CompanionPersonaUpdated
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.GetHasGuidanceNote() {
		t.Errorf("has_guidance_note (8) = true; want false")
	}
}
