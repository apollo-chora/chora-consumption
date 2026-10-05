package conceptgraph

import (
	"errors"
	"testing"
	"time"
)

func mustEdge(t *testing.T, class EdgeClass) *Edge {
	t.Helper()
	e, err := NewEdge(NewEdgeInput{
		TenantID: cTenant, LearnerGCID: cLearner,
		SourceConceptID: cSource, TargetConceptID: cTarget, Class: class, Now: cNow,
	})
	if err != nil {
		t.Fatalf("mustEdge: unexpected error: %v", err)
	}
	return e
}

// ADR-212 D2: two edge classes — hierarchy (rings) + lateral (relates-to).
func TestNewEdge_HierarchyOK(t *testing.T) {
	e := mustEdge(t, EdgeClassHierarchy)
	if e.EdgeID == "" {
		t.Error("EdgeID must be minted (UUIDv7)")
	}
	if e.Class != EdgeClassHierarchy {
		t.Errorf("Class = %q; want hierarchy", e.Class)
	}
	if e.Provenance != ProvenanceLearnerAuthored {
		t.Errorf("Provenance = %q; want learner_authored (default)", e.Provenance)
	}
	if !e.CreatedAt.Equal(cNow) || !e.UpdatedAt.Equal(cNow) {
		t.Errorf("timestamps not set to now: created=%v updated=%v", e.CreatedAt, e.UpdatedAt)
	}
	if e.DeletedAt != nil {
		t.Error("a fresh edge must not be soft-deleted")
	}
}

func TestNewEdge_LateralOK(t *testing.T) {
	e := mustEdge(t, EdgeClassLateral)
	if e.Class != EdgeClassLateral {
		t.Errorf("Class = %q; want lateral", e.Class)
	}
}

// A concept cannot relate to itself (mirrors AtomSemanticEdge's self-loop guard).
func TestNewEdge_SelfLoopRejected(t *testing.T) {
	_, err := NewEdge(NewEdgeInput{
		TenantID: cTenant, LearnerGCID: cLearner,
		SourceConceptID: cSource, TargetConceptID: cSource, Class: EdgeClassHierarchy, Now: cNow,
	})
	if !errors.Is(err, ErrSelfLoop) {
		t.Fatalf("self-loop must be ErrSelfLoop; got %v", err)
	}
}

func TestNewEdge_EndpointsAndScopeRequired(t *testing.T) {
	base := NewEdgeInput{TenantID: cTenant, LearnerGCID: cLearner, SourceConceptID: cSource, TargetConceptID: cTarget, Class: EdgeClassHierarchy, Now: cNow}
	cases := map[string]func(NewEdgeInput) NewEdgeInput{
		"tenant":  func(in NewEdgeInput) NewEdgeInput { in.TenantID = ""; return in },
		"learner": func(in NewEdgeInput) NewEdgeInput { in.LearnerGCID = ""; return in },
		"source":  func(in NewEdgeInput) NewEdgeInput { in.SourceConceptID = " "; return in },
		"target":  func(in NewEdgeInput) NewEdgeInput { in.TargetConceptID = ""; return in },
	}
	for name, mut := range cases {
		if _, err := NewEdge(mut(base)); !errors.Is(err, ErrInvalid) {
			t.Errorf("blank %s must be ErrInvalid; got %v", name, err)
		}
	}
}

func TestNewEdge_InvalidClassRejected(t *testing.T) {
	_, err := NewEdge(NewEdgeInput{
		TenantID: cTenant, LearnerGCID: cLearner,
		SourceConceptID: cSource, TargetConceptID: cTarget, Class: EdgeClass("sideways"), Now: cNow,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown edge class must be ErrInvalid; got %v", err)
	}
}

func TestNewEdge_InvalidProvenanceRejected(t *testing.T) {
	_, err := NewEdge(NewEdgeInput{
		TenantID: cTenant, LearnerGCID: cLearner,
		SourceConceptID: cSource, TargetConceptID: cTarget, Class: EdgeClassLateral,
		Provenance: Provenance("vibes"), Now: cNow,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown provenance must be ErrInvalid; got %v", err)
	}
}

func TestEdge_SoftDelete(t *testing.T) {
	e := mustEdge(t, EdgeClassHierarchy)
	del := cNow.Add(time.Hour)
	e.SoftDelete(del)
	if e.DeletedAt == nil || !e.DeletedAt.Equal(del) {
		t.Fatalf("DeletedAt = %v; want %v", e.DeletedAt, del)
	}
}

func TestNewEdge_ZeroNowFallsBackToWallClock(t *testing.T) {
	e, err := NewEdge(NewEdgeInput{TenantID: cTenant, LearnerGCID: cLearner, SourceConceptID: cSource, TargetConceptID: cTarget, Class: EdgeClassLateral})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if e.CreatedAt.IsZero() {
		t.Error("zero Now must fall back to wall-clock")
	}
}

func TestEdgeClass_Valid(t *testing.T) {
	for _, c := range []EdgeClass{EdgeClassHierarchy, EdgeClassLateral} {
		if !c.Valid() {
			t.Errorf("%q must be Valid", c)
		}
	}
	for _, c := range []EdgeClass{"", "diagonal"} {
		if EdgeClass(c).Valid() {
			t.Errorf("%q must be invalid", c)
		}
	}
}

// ADR-212 D2/D6: the exactly-6 hard cap (ADR-143) becomes a SOFT cap — a 7th
// relation is accepted; the overflow spills into a drawer, it is NOT rejected.
func TestPartitionByHexFace_SoftCap(t *testing.T) {
	seven := make([]Edge, 7)
	onFace, overflow := PartitionByHexFace(seven)
	if len(onFace) != MaxHexFaceRelations {
		t.Errorf("onFace = %d; want %d", len(onFace), MaxHexFaceRelations)
	}
	if len(overflow) != 1 {
		t.Errorf("overflow = %d; want 1 (7th accepted, not rejected)", len(overflow))
	}

	six := make([]Edge, 6)
	onFace, overflow = PartitionByHexFace(six)
	if len(onFace) != 6 || overflow != nil {
		t.Errorf("six relations: onFace=%d overflow=%v; want 6 / nil", len(onFace), overflow)
	}

	if onFace, overflow = PartitionByHexFace(nil); onFace != nil || overflow != nil {
		t.Errorf("nil relations must yield nil/nil; got %v/%v", onFace, overflow)
	}
}

// Sentinel hygiene — every error is pairwise distinct and carries a message
// (so the HTTP/gRPC adapter can map each to its own status code).
func TestErrorsAreDistinct(t *testing.T) {
	sentinels := []error{ErrInvalid, ErrConceptDeleted, ErrEdgeDeleted, ErrSelfLoop}
	for i := range sentinels {
		if sentinels[i].Error() == "" {
			t.Errorf("sentinel %d has empty message", i)
		}
		for j := range sentinels {
			if i != j && errors.Is(sentinels[i], sentinels[j]) {
				t.Errorf("sentinels %d and %d must be distinct", i, j)
			}
		}
	}
}
