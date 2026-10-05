package conceptgraph

import (
	"errors"
	"testing"
	"time"
)

var sugNow = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

const (
	sugAtom   = "01970000-0000-7000-a000-0000000000d1"
	sugSource = "01970000-0000-7000-c000-0000000000d2"
	sugTarget = "01970000-0000-7000-c000-0000000000d3"
	sugTenant = "01970000-0000-7000-b000-0000000000d0"
	sugGCID   = "01970000-0000-7000-9000-0000000000d0"
	sugEvent  = "01970000-0000-7000-e000-0000000000d9"
	sugFam    = "01970000-0000-7000-f000-0000000000da"
	sugFocal  = "01970000-0000-7000-c000-0000000000db"
)

func conceptSuggestionFixture(t *testing.T) *Suggestion {
	t.Helper()
	s, err := NewConceptSuggestion(NewConceptSuggestionInput{
		TenantID: sugTenant, LearnerGCID: sugGCID, Title: "Fibonacci sequence",
		AtomRefs: []string{sugAtom}, Rationale: "relates to story-point estimation",
		ModelID: "gemini-2.5-flash", RunID: "run-123", SourceCompanionID: sugFam,
		SourceEventID: sugEvent, FocalConceptID: sugFocal, Now: sugNow,
	})
	if err != nil {
		t.Fatalf("conceptSuggestionFixture: %v", err)
	}
	return s
}

func edgeSuggestionFixture(t *testing.T, class EdgeClass) *Suggestion {
	t.Helper()
	s, err := NewEdgeSuggestion(NewEdgeSuggestionInput{
		TenantID: sugTenant, LearnerGCID: sugGCID,
		SourceConceptID: sugSource, TargetConceptID: sugTarget, Class: class,
		Rationale: "hierarchy suggestion", ModelID: "gemini-2.5-flash", RunID: "run-124",
		SourceCompanionID: sugFam, SourceEventID: sugEvent, FocalConceptID: sugFocal, Now: sugNow,
	})
	if err != nil {
		t.Fatalf("edgeSuggestionFixture: %v", err)
	}
	return s
}

func TestNewConceptSuggestion_OK(t *testing.T) {
	s := conceptSuggestionFixture(t)
	if s.Kind != SuggestionKindConcept {
		t.Errorf("kind = %q; want concept", s.Kind)
	}
	if s.Status != SuggestionStatusPending {
		t.Errorf("status = %q; want pending", s.Status)
	}
	if s.SuggestionID == "" {
		t.Error("want a minted suggestion id")
	}
	if s.Title != "Fibonacci sequence" || len(s.AtomRefs) != 1 || s.AtomRefs[0] != sugAtom {
		t.Errorf("concept payload not preserved: %+v", s)
	}
	if s.ModelID != "gemini-2.5-flash" || s.RunID != "run-123" || s.SourceEventID != sugEvent {
		t.Errorf("provenance stamps not preserved: %+v", s)
	}
	// The focal concept the fog generated this suggestion around (the scoping
	// anchor so a stale prior-generate batch doesn't leak onto other nodes).
	if s.FocalConceptID != sugFocal {
		t.Errorf("focal_concept_id = %q; want %q", s.FocalConceptID, sugFocal)
	}
	if s.DecidedAt != nil || s.DeletedAt != nil {
		t.Error("a fresh suggestion is undecided + live")
	}
}

func TestNewConceptSuggestion_FocalOptional_WholeMap(t *testing.T) {
	// No focal ⇒ a whole-map suggestion (persists as NULL / empty string) so it
	// stays visible on every node (back-compat with the pre-focal generate path).
	s, err := NewConceptSuggestion(NewConceptSuggestionInput{
		TenantID: sugTenant, LearnerGCID: sugGCID, Title: "Whole map concept",
		FocalConceptID: "  ", Now: sugNow,
	})
	if err != nil {
		t.Fatalf("NewConceptSuggestion: %v", err)
	}
	if s.FocalConceptID != "" {
		t.Errorf("focal_concept_id = %q; want empty (whole-map)", s.FocalConceptID)
	}
}

func TestNewConceptSuggestion_RequiresTitle(t *testing.T) {
	_, err := NewConceptSuggestion(NewConceptSuggestionInput{
		TenantID: sugTenant, LearnerGCID: sugGCID, Title: "  ", Now: sugNow,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("empty title: err = %v; want ErrInvalid", err)
	}
}

func TestNewConceptSuggestion_RejectsMalformedAtomRef(t *testing.T) {
	_, err := NewConceptSuggestion(NewConceptSuggestionInput{
		TenantID: sugTenant, LearnerGCID: sugGCID, Title: "X",
		AtomRefs: []string{"not-a-uuid"}, Now: sugNow,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("bad atom ref: err = %v; want ErrInvalid", err)
	}
}

func TestNewEdgeSuggestion_OK(t *testing.T) {
	s := edgeSuggestionFixture(t, EdgeClassHierarchy)
	if s.Kind != SuggestionKindEdge {
		t.Errorf("kind = %q; want edge", s.Kind)
	}
	if s.SourceConceptID != sugSource || s.TargetConceptID != sugTarget || s.EdgeClass != EdgeClassHierarchy {
		t.Errorf("edge payload not preserved: %+v", s)
	}
	if s.FocalConceptID != sugFocal {
		t.Errorf("edge focal_concept_id = %q; want %q", s.FocalConceptID, sugFocal)
	}
	// Regression: edges carry no atoms, but AtomRefs MUST be non-nil so the pg
	// NOT NULL atom_refs column binds '{}' not NULL (SQLSTATE 23502).
	if s.AtomRefs == nil {
		t.Error("edge suggestion AtomRefs must be non-nil (empty), not nil")
	}
	if s.Status != SuggestionStatusPending {
		t.Errorf("status = %q; want pending", s.Status)
	}
}

func TestNewEdgeSuggestion_RejectsSelfLoop(t *testing.T) {
	_, err := NewEdgeSuggestion(NewEdgeSuggestionInput{
		TenantID: sugTenant, LearnerGCID: sugGCID,
		SourceConceptID: sugSource, TargetConceptID: sugSource, Class: EdgeClassLateral, Now: sugNow,
	})
	if !errors.Is(err, ErrSelfLoop) {
		t.Errorf("self-loop: err = %v; want ErrSelfLoop", err)
	}
}

func TestNewEdgeSuggestion_RejectsBadClass(t *testing.T) {
	_, err := NewEdgeSuggestion(NewEdgeSuggestionInput{
		TenantID: sugTenant, LearnerGCID: sugGCID,
		SourceConceptID: sugSource, TargetConceptID: sugTarget, Class: EdgeClass("bogus"), Now: sugNow,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("bad class: err = %v; want ErrInvalid", err)
	}
}

func TestSuggestion_Accept_ConceptMintsNodeAndLinksToFocal(t *testing.T) {
	s := conceptSuggestionFixture(t) // suggested around FocalConceptID: sugFocal
	acceptedAt := sugNow.Add(time.Hour)
	node, edge, err := s.Accept(acceptedAt)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if node == nil {
		t.Fatal("concept suggestion must mint a ConceptNode")
	}
	if node.Provenance != ProvenanceCompanionSuggestedAccepted {
		t.Errorf("node provenance = %q; want companion_suggested_accepted", node.Provenance)
	}
	if node.Title != "Fibonacci sequence" || len(node.AtomRefs) != 1 || node.AtomRefs[0] != sugAtom {
		t.Errorf("minted node payload = %+v", node)
	}
	if node.TenantID != sugTenant || node.LearnerGCID != sugGCID {
		t.Errorf("minted node not scoped to learner/tenant: %+v", node)
	}
	// A concept suggested around a focal must ALSO be LINKED onto the map as a
	// child of that focal — else it lands disconnected and never shows on the
	// goal graph (2026-07-05 KG-walk finding #2: accept was a map no-op).
	if edge == nil {
		t.Fatal("concept accept with a focal must mint a hierarchy edge to place it on the map")
	}
	if edge.SourceConceptID != sugFocal || edge.TargetConceptID != node.ConceptID {
		t.Errorf("link edge = %s→%s; want focal %s → new node %s",
			edge.SourceConceptID, edge.TargetConceptID, sugFocal, node.ConceptID)
	}
	if edge.Class != EdgeClassHierarchy {
		t.Errorf("link edge class = %q; want hierarchy", edge.Class)
	}
	if edge.Provenance != ProvenanceCompanionSuggestedAccepted {
		t.Errorf("link edge provenance = %q; want companion_suggested_accepted", edge.Provenance)
	}
	if s.Status != SuggestionStatusAccepted {
		t.Errorf("suggestion status = %q; want accepted", s.Status)
	}
	if s.DecidedAt == nil || !s.DecidedAt.Equal(acceptedAt) {
		t.Errorf("decided_at = %v; want %v", s.DecidedAt, acceptedAt)
	}
}

func TestSuggestion_Accept_ConceptWholeMap_MintsNodeNoEdge(t *testing.T) {
	// A whole-map concept suggestion (no focal) has nothing to attach to — it
	// mints the node only (a new concept the learner can wire up later).
	s, err := NewConceptSuggestion(NewConceptSuggestionInput{
		TenantID: sugTenant, LearnerGCID: sugGCID, Title: "Orphan concept",
		FocalConceptID: "", Now: sugNow,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	node, edge, err := s.Accept(sugNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if node == nil {
		t.Fatal("whole-map concept accept must still mint the ConceptNode")
	}
	if edge != nil {
		t.Fatalf("no focal ⇒ no link edge; got %+v", edge)
	}
}

func TestSuggestion_Accept_EdgeMintsEdgeWithProvenance(t *testing.T) {
	s := edgeSuggestionFixture(t, EdgeClassHierarchy)
	node, edge, err := s.Accept(sugNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if node != nil {
		t.Fatal("edge suggestion must not mint a concept node")
	}
	if edge == nil {
		t.Fatal("edge suggestion must mint an Edge")
	}
	if edge.Provenance != ProvenanceCompanionSuggestedAccepted {
		t.Errorf("edge provenance = %q; want companion_suggested_accepted", edge.Provenance)
	}
	if edge.SourceConceptID != sugSource || edge.TargetConceptID != sugTarget || edge.Class != EdgeClassHierarchy {
		t.Errorf("minted edge = %+v", edge)
	}
}

func TestSuggestion_Accept_RejectedWhenAlreadyDecided(t *testing.T) {
	s := conceptSuggestionFixture(t)
	if _, _, err := s.Accept(sugNow); err != nil {
		t.Fatalf("first Accept: %v", err)
	}
	if _, _, err := s.Accept(sugNow); !errors.Is(err, ErrSuggestionDecided) {
		t.Errorf("re-accept: err = %v; want ErrSuggestionDecided", err)
	}
}

func TestSuggestion_Dismiss_FlipsStatusNoMaterialization(t *testing.T) {
	s := conceptSuggestionFixture(t)
	dismissedAt := sugNow.Add(2 * time.Hour)
	if err := s.Dismiss(dismissedAt); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	if s.Status != SuggestionStatusDismissed {
		t.Errorf("status = %q; want dismissed", s.Status)
	}
	if s.DecidedAt == nil || !s.DecidedAt.Equal(dismissedAt) {
		t.Errorf("decided_at = %v; want %v", s.DecidedAt, dismissedAt)
	}
}

func TestSuggestion_Dismiss_ThenAcceptRejected(t *testing.T) {
	s := edgeSuggestionFixture(t, EdgeClassLateral)
	if err := s.Dismiss(sugNow); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	if _, _, err := s.Accept(sugNow); !errors.Is(err, ErrSuggestionDecided) {
		t.Errorf("accept after dismiss: err = %v; want ErrSuggestionDecided", err)
	}
}

func TestSuggestionKindAndStatus_Valid(t *testing.T) {
	// Used by the Slice-4 subscriber to reject untrusted event kind/status.
	for _, k := range []SuggestionKind{SuggestionKindConcept, SuggestionKindEdge} {
		if !k.Valid() {
			t.Errorf("kind %q should be valid", k)
		}
	}
	if SuggestionKind("bogus").Valid() {
		t.Error("unknown kind must be invalid")
	}
	for _, s := range []SuggestionStatus{SuggestionStatusPending, SuggestionStatusAccepted, SuggestionStatusDismissed} {
		if !s.Valid() {
			t.Errorf("status %q should be valid", s)
		}
	}
	if SuggestionStatus("bogus").Valid() {
		t.Error("unknown status must be invalid")
	}
}
