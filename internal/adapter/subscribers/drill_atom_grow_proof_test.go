// drill_atom_grow_proof_test.go — CHO-1895 end-to-end proof of the drill-atom→grow
// loop, exercising the REAL domain logic across the serve↔recover seam:
//
//	(1) SERVE  — for an edge with a GRADABLE cached drill atom, the focused Daily
//	             Dose (companion.ComposeDailyDose with FocusEdgeID) serves THAT atom
//	             as the weakness pick (alignment invariant: the focused weakness
//	             slot is a cached drill atom, never a broad topic match).
//	(2) RECOVER— completing that SAME atom correctly drives the projector's
//	             RecoverByDrillAtomID, which runs the real LearnerWeakness.Recover
//	             and flips the edge active→grown, emitting weakness.grown.v1 with
//	             recovery_source=drill_atom.
//
// The proofDrillRepo backs RecoverByDrillAtomID with a REAL aggregate (same
// cache-contains + only-lower + status-flip semantics as the pg adapter), so the
// grow is genuine, not a fixture. This is the test the live walk lacked: the
// served atom is a cached drill atom AND gradable, so the loop actually closes.
package subscribers

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

const proofDrillAtomID = "019e0000-0000-7000-d000-000000000001"

// proofDrillRepo backs RecoverByDrillAtomID with a real LearnerWeakness aggregate,
// mirroring the pg adapter: recover only edges whose cache contains the atom,
// only-lower, report the active→grown transition.
type proofDrillRepo struct {
	edge        *lw.LearnerWeakness
	drillCalled bool
	gotAtomID   string
}

func (r *proofDrillRepo) Upsert(context.Context, lw.UpsertInput) (lw.UpsertResult, error) {
	return lw.UpsertResult{}, nil
}
func (r *proofDrillRepo) List(context.Context, lw.ListQuery) (lw.ListResult, error) {
	return lw.ListResult{}, nil
}
func (r *proofDrillRepo) ListAll(context.Context, lw.ListQuery) ([]lw.LearnerWeakness, error) {
	return nil, nil // unused by this proof (only the RecoverByDrillAtomID path is exercised)
}
func (r *proofDrillRepo) Get(context.Context, string, string) (*lw.LearnerWeakness, error) {
	return nil, nil
}
func (r *proofDrillRepo) SoftDelete(context.Context, string, string, time.Time) error { return nil }
func (r *proofDrillRepo) RecoverByConceptKey(context.Context, string, string, float64, time.Time) ([]lw.GrownEdge, error) {
	return nil, nil // the explicit edge's minted concept_key never equals the atom topic
}
func (r *proofDrillRepo) RecoverByDrillAtomID(_ context.Context, _, atomID string, strength float64, now time.Time) ([]lw.GrownEdge, error) {
	r.drillCalled = true
	r.gotAtomID = atomID
	contains := false
	for _, id := range r.edge.CachedDrillAtomIDs {
		if id == atomID {
			contains = true
			break
		}
	}
	if !contains {
		return nil, nil
	}
	wasActive := r.edge.Status == lw.StatusActive
	if !r.edge.Recover(strength, now) {
		return nil, nil
	}
	if wasActive && r.edge.IsGrown() {
		return []lw.GrownEdge{{
			ID:            r.edge.ID,
			ConceptKey:    r.edge.ConceptKey,
			ConceptLabel:  r.edge.ConceptLabel,
			FinalStrength: r.edge.Strength,
			Tags:          r.edge.Tags,
			GrownAt:       now,
		}}, nil
	}
	return nil, nil
}
func (r *proofDrillRepo) SetCachedDrillAtoms(context.Context, string, string, []string, time.Time) error {
	return nil
}

func TestDrillAtomGrowLoop_ServeThenRecoverToGrown(t *testing.T) {
	// An EXPLICIT (uploaded) edge, active, with the gradable atom cached as a drill.
	edge, err := lw.New(lw.UpsertInput{
		TenantID:     dwTenant,
		LearnerGCID:  dwGCID,
		ConceptLabel: "Binary Search Invariants",
		Embedding:    []float32{0.1, 0.2},
		Strength:     0.9, // active (well above MasteredStrengthThreshold)
		Source:       lw.SourceExplicit,
		Tags:         []string{"algorithms"},
		Now:          time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("edge: %v", err)
	}
	edge.CachedDrillAtomIDs = []string{proofDrillAtomID}
	if edge.Status != lw.StatusActive {
		t.Fatalf("precondition: edge status = %q, want active", edge.Status)
	}

	// ---- (1) SERVE: the focused dose serves the cached drill atom as weakness ----
	gradableSeed := companion.AtomSeed{AtomID: proofDrillAtomID, Topic: "binary-search", Title: "Binary Search Drill"}
	dose := companion.ComposeDailyDose(companion.DailyDoseInput{
		Seeds:       []companion.AtomSeed{gradableSeed},
		States:      map[string]companion.SM2State{},
		FocusEdgeID: edge.ID,
		GrowthEdges: []companion.GrowthEdgeInput{{
			EdgeID:             edge.ID,
			ConceptKey:         edge.ConceptKey,
			Strength:           edge.Strength,
			CachedDrillAtomIDs: []string{proofDrillAtomID},
		}},
		Now: time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC),
	})
	var served []string
	for _, e := range dose.Entries {
		if e.DoseReason == companion.DoseReasonWeakness {
			served = append(served, e.AtomID)
		}
	}
	if len(served) != 1 || served[0] != proofDrillAtomID {
		t.Fatalf("focused dose weakness picks = %v, want [%s] (the gradable cached drill atom)", served, proofDrillAtomID)
	}

	// ---- (2) RECOVER: completing that SAME atom correctly grows the edge ----
	repo := &proofDrillRepo{edge: edge}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"binary-search": 1.0}} // perfect → DerivedStrength 0
	atoms := &dwFakeAtoms{atoms: map[string]*atom_index.AtomIndex{
		proofDrillAtomID: {AtomID: proofDrillAtomID, AtomType: "mcq", CorrectOptionID: "b", TopicTags: []string{"binary-search"}},
	}}
	pub := &dwGrownCapture{}
	p := NewDerivedWeaknessProjector(repo, &lwFakeEmbedder{vec: []float32{0.1, 0.2}}, acc).
		WithAtomIndex(atoms).WithGrownOutbox(pub)

	payload := AtomSessionCompletedPayload{
		SessionID:   "proof-session-1",
		AtomID:      proofDrillAtomID,
		LearnerGCID: dwGCID,
		TenantID:    dwTenant,
		IsCorrect:   true,
	}
	if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-proof-grow"), payload); err != nil {
		t.Fatalf("HandleAtomSessionCompleted: %v", err)
	}

	if !repo.drillCalled || repo.gotAtomID != proofDrillAtomID {
		t.Fatalf("RecoverByDrillAtomID called=%v atom=%q, want called with %s", repo.drillCalled, repo.gotAtomID, proofDrillAtomID)
	}
	if !edge.IsGrown() {
		t.Fatalf("edge status = %q, want grown (completing the cached drill atom recovers it to mastery)", edge.Status)
	}

	grown := grownEvents(pub)
	if len(grown) != 1 {
		t.Fatalf("weakness.grown events = %d, want 1 (the active→grown reward)", len(grown))
	}
	if got := grown[0].Payload["recovery_source"]; got != lw.RecoverySourceDrillAtom {
		t.Fatalf("recovery_source = %v, want drill_atom", got)
	}
	if got := grown[0].Payload["growth_edge_id"]; got != edge.ID {
		t.Fatalf("growth_edge_id = %v, want %s (the edge that cached the served atom)", got, edge.ID)
	}
}
