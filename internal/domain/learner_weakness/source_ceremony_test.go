// source_ceremony_test.go — CHO-2043: the ceremony source tag + seed. A
// remediate ceremony learning-edge mints an UNEVIDENCED Growth Edge so the map
// overlay, the growth-edges list, and the drawer diagnosis share one backing.
package learner_weakness

import "testing"

func TestSourceCeremony_ValidAndSeed(t *testing.T) {
	if !ValidSource(SourceCeremony) {
		t.Fatalf("SourceCeremony must be a valid source")
	}
	if SourceCeremony != "ceremony" {
		t.Fatalf("SourceCeremony = %q, want %q", SourceCeremony, "ceremony")
	}
	// The seed must read shaky (active) — strictly above the mastered floor so
	// statusFor keeps it active — yet remain a valid [0,1] shakiness.
	if CeremonySeedStrength <= MasteredStrengthThreshold || CeremonySeedStrength > 1 {
		t.Fatalf("CeremonySeedStrength %v must be in (%v, 1]", CeremonySeedStrength, MasteredStrengthThreshold)
	}
}

// TestNew_AcceptsCeremonySource pins that the constructor admits a ceremony-
// sourced seed (real embedding, empty descriptor) as an active edge.
func TestNew_AcceptsCeremonySource(t *testing.T) {
	w, err := New(UpsertInput{
		TenantID:     "11111111-1111-4111-8111-111111111111",
		LearnerGCID:  "22222222-2222-4222-8222-222222222222",
		ConceptLabel: "Food Chains",
		Embedding:    []float32{0.1, 0.2, 0.3},
		Strength:     CeremonySeedStrength,
		Source:       SourceCeremony,
	})
	if err != nil {
		t.Fatalf("New(ceremony): %v", err)
	}
	if w.Status != StatusActive {
		t.Fatalf("ceremony seed status = %q, want active", w.Status)
	}
	if len(w.Sources) != 1 || w.Sources[0] != SourceCeremony {
		t.Fatalf("source not seeded: %v", w.Sources)
	}
}
