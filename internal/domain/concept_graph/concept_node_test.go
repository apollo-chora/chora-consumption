package conceptgraph

import (
	"errors"
	"testing"
	"time"
)

// Shared UUIDv7-shaped fixtures (also used by edge_test.go — same package).
const (
	cTenant  = "01970000-0000-7000-8000-0000000000a1"
	cLearner = "01970000-0000-7000-9000-0000000000a1"
	cAtom1   = "01970000-0000-7000-a000-0000000000a1"
	cAtom2   = "01970000-0000-7000-a000-0000000000a2"
	cSource  = "01970000-0000-7000-c000-0000000000b1"
	cTarget  = "01970000-0000-7000-c000-0000000000b2"
)

var cNow = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

func mustConcept(t *testing.T) *ConceptNode {
	t.Helper()
	c, err := NewConceptNode(NewConceptNodeInput{
		TenantID: cTenant, LearnerGCID: cLearner, Title: "story-point calculation", Now: cNow,
	})
	if err != nil {
		t.Fatalf("mustConcept: unexpected error: %v", err)
	}
	return c
}

// ADR-212 D1: a learner concept may be created with ZERO atoms (temporarily
// empty) and defaults to learner_authored provenance.
func TestNewConceptNode_MinimalEmptyAtomsOK(t *testing.T) {
	c, err := NewConceptNode(NewConceptNodeInput{
		TenantID: cTenant, LearnerGCID: cLearner, Title: "  story-point calculation  ", Now: cNow,
	})
	if err != nil {
		t.Fatalf("empty-atom concept must be valid: %v", err)
	}
	if c.ConceptID == "" {
		t.Error("ConceptID must be minted (UUIDv7)")
	}
	if c.Title != "story-point calculation" {
		t.Errorf("Title = %q; want trimmed value", c.Title)
	}
	if c.Provenance != ProvenanceLearnerAuthored {
		t.Errorf("Provenance = %q; want learner_authored (default)", c.Provenance)
	}
	if c.AtomRefs == nil {
		t.Error("AtomRefs must be non-nil (empty slice) for a stable wire shape")
	}
	if len(c.AtomRefs) != 0 {
		t.Errorf("AtomRefs len = %d; want 0", len(c.AtomRefs))
	}
	if !c.CreatedAt.Equal(cNow) || !c.UpdatedAt.Equal(cNow) {
		t.Errorf("timestamps not set to now: created=%v updated=%v", c.CreatedAt, c.UpdatedAt)
	}
	if c.DeletedAt != nil {
		t.Error("a fresh concept must not be soft-deleted")
	}
}

func TestNewConceptNode_TitleRequired(t *testing.T) {
	_, err := NewConceptNode(NewConceptNodeInput{
		TenantID: cTenant, LearnerGCID: cLearner, Title: "   ", Now: cNow,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank title must be ErrInvalid; got %v", err)
	}
}

func TestNewConceptNode_TenantAndLearnerRequired(t *testing.T) {
	if _, err := NewConceptNode(NewConceptNodeInput{TenantID: "", LearnerGCID: cLearner, Title: "x", Now: cNow}); !errors.Is(err, ErrInvalid) {
		t.Errorf("blank tenant must be ErrInvalid; got %v", err)
	}
	if _, err := NewConceptNode(NewConceptNodeInput{TenantID: cTenant, LearnerGCID: "", Title: "x", Now: cNow}); !errors.Is(err, ErrInvalid) {
		t.Errorf("blank learner must be ErrInvalid; got %v", err)
	}
}

// ADR-212 D1: a concept references 0..N atoms by UUID (no FK); refs are
// trimmed + de-duplicated + order-preserved.
func TestNewConceptNode_WithAtomRefsDeduped(t *testing.T) {
	c, err := NewConceptNode(NewConceptNodeInput{
		TenantID: cTenant, LearnerGCID: cLearner, Title: "fibonacci",
		AtomRefs: []string{cAtom1, "  " + cAtom2 + "  ", cAtom1}, Now: cNow,
	})
	if err != nil {
		t.Fatalf("valid atom refs must construct: %v", err)
	}
	if len(c.AtomRefs) != 2 {
		t.Fatalf("AtomRefs = %v; want 2 deduped", c.AtomRefs)
	}
	if c.AtomRefs[0] != cAtom1 || c.AtomRefs[1] != cAtom2 {
		t.Errorf("AtomRefs = %v; want order-preserved [%s %s]", c.AtomRefs, cAtom1, cAtom2)
	}
}

func TestNewConceptNode_InvalidProvenanceRejected(t *testing.T) {
	_, err := NewConceptNode(NewConceptNodeInput{
		TenantID: cTenant, LearnerGCID: cLearner, Title: "x", Provenance: Provenance("hearsay"), Now: cNow,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown provenance must be ErrInvalid; got %v", err)
	}
}

func TestNewConceptNode_MalformedAtomRefRejected(t *testing.T) {
	_, err := NewConceptNode(NewConceptNodeInput{
		TenantID: cTenant, LearnerGCID: cLearner, Title: "x", AtomRefs: []string{"not-a-uuid"}, Now: cNow,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed atom ref must be ErrInvalid; got %v", err)
	}
}

func TestNewConceptNode_CompanionSuggestedProvenanceOK(t *testing.T) {
	c, err := NewConceptNode(NewConceptNodeInput{
		TenantID: cTenant, LearnerGCID: cLearner, Title: "x",
		Provenance: ProvenanceCompanionSuggestedAccepted, Now: cNow,
	})
	if err != nil {
		t.Fatalf("companion_suggested_accepted must be valid: %v", err)
	}
	if c.Provenance != ProvenanceCompanionSuggestedAccepted {
		t.Errorf("Provenance = %q; want companion_suggested_accepted", c.Provenance)
	}
}

func TestConceptNode_AttachAtom(t *testing.T) {
	c := mustConcept(t)
	later := cNow.Add(time.Minute)
	if err := c.AttachAtom(cAtom1, later); err != nil {
		t.Fatalf("AttachAtom: %v", err)
	}
	if err := c.AttachAtom(cAtom1, later); err != nil { // idempotent
		t.Fatalf("AttachAtom (dup) must be idempotent: %v", err)
	}
	if len(c.AtomRefs) != 1 {
		t.Fatalf("AtomRefs = %v; want 1 (deduped)", c.AtomRefs)
	}
	if !c.UpdatedAt.Equal(later) {
		t.Errorf("UpdatedAt = %v; want %v", c.UpdatedAt, later)
	}
}

func TestConceptNode_AttachAtomMalformedRejected(t *testing.T) {
	c := mustConcept(t)
	if err := c.AttachAtom("nope", cNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("AttachAtom malformed must be ErrInvalid; got %v", err)
	}
}

func TestConceptNode_DetachAtom(t *testing.T) {
	c := mustConcept(t)
	_ = c.AttachAtom(cAtom1, cNow)
	_ = c.AttachAtom(cAtom2, cNow)
	if err := c.DetachAtom(cAtom1, cNow.Add(time.Minute)); err != nil {
		t.Fatalf("DetachAtom: %v", err)
	}
	if len(c.AtomRefs) != 1 || c.AtomRefs[0] != cAtom2 {
		t.Errorf("AtomRefs = %v; want [%s]", c.AtomRefs, cAtom2)
	}
}

func TestConceptNode_Rename(t *testing.T) {
	c := mustConcept(t)
	if err := c.Rename("  Scrum  ", cNow.Add(time.Minute)); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if c.Title != "Scrum" {
		t.Errorf("Title = %q; want trimmed 'Scrum'", c.Title)
	}
	if err := c.Rename("   ", cNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("blank rename must be ErrInvalid; got %v", err)
	}
}

func TestConceptNode_SoftDelete(t *testing.T) {
	c := mustConcept(t)
	del := cNow.Add(time.Hour)
	c.SoftDelete(del)
	if c.DeletedAt == nil || !c.DeletedAt.Equal(del) {
		t.Fatalf("DeletedAt = %v; want %v", c.DeletedAt, del)
	}
	// Mutators must refuse a soft-deleted concept (fail-loud, never resurrect silently).
	if err := c.AttachAtom(cAtom1, del); !errors.Is(err, ErrConceptDeleted) {
		t.Errorf("AttachAtom on deleted must be ErrConceptDeleted; got %v", err)
	}
	if err := c.Rename("x", del); !errors.Is(err, ErrConceptDeleted) {
		t.Errorf("Rename on deleted must be ErrConceptDeleted; got %v", err)
	}
}

func TestNewConceptNode_ZeroNowFallsBackToWallClock(t *testing.T) {
	c, err := NewConceptNode(NewConceptNodeInput{TenantID: cTenant, LearnerGCID: cLearner, Title: "x"})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if c.CreatedAt.IsZero() {
		t.Error("zero Now must fall back to wall-clock (CreatedAt non-zero)")
	}
}

func TestProvenance_Valid(t *testing.T) {
	for _, p := range []Provenance{ProvenanceLearnerAuthored, ProvenanceCompanionSuggestedAccepted, ProvenanceSystemDerived} {
		if !p.Valid() {
			t.Errorf("%q must be Valid", p)
		}
	}
	for _, p := range []Provenance{"", "guess", "learner"} {
		if Provenance(p).Valid() {
			t.Errorf("%q must be invalid", p)
		}
	}
}
