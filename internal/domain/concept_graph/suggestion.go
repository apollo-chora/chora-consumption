// suggestion.go — the Companion-driven SUGGESTION aggregate (ADR-212 WS-4,
// D4/D9). The LLM "fog" is demoted from an authoritative hexagon generator to a
// suggestion engine: it proposes candidate CONCEPTS and EDGES over the
// learner-sovereign map; the learner curates. A Suggestion is a proposed
// concept or edge awaiting the learner's Accept (→ mints the real ConceptNode /
// Edge with provenance=companion_suggested_accepted) or Dismiss.
//
// HEXAGONAL purity: stdlib-only, reuses this package's helpers (resolveNow,
// normaliseAtomRefs, NewConceptNode, NewEdge). Soft-delete only (#5); ids are
// UUIDv7 (#7); atom + concept references are opaque UUIDs with no FK (#3). The
// model/run stamps (ADR-197) + rationale (ADR-215) travel with the suggestion
// for learner-facing explainability. Slice 1 scope: aggregate + invariants +
// port; the event ingestion + endpoints are later WS-4 slices.
package conceptgraph

import (
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// SuggestionKind is what the Companion proposed: a new concept, or a new edge
// between two existing concepts.
type SuggestionKind string

const (
	SuggestionKindConcept SuggestionKind = "concept"
	SuggestionKindEdge    SuggestionKind = "edge"
)

// Valid reports whether k is a recognised suggestion kind.
func (k SuggestionKind) Valid() bool {
	switch k {
	case SuggestionKindConcept, SuggestionKindEdge:
		return true
	}
	return false
}

// SuggestionStatus is the learner-curation lifecycle: a fresh suggestion is
// pending until the learner Accepts (materialised into the map) or Dismisses.
type SuggestionStatus string

const (
	SuggestionStatusPending   SuggestionStatus = "pending"
	SuggestionStatusAccepted  SuggestionStatus = "accepted"
	SuggestionStatusDismissed SuggestionStatus = "dismissed"
)

// Valid reports whether s is a recognised suggestion status.
func (s SuggestionStatus) Valid() bool {
	switch s {
	case SuggestionStatusPending, SuggestionStatusAccepted, SuggestionStatusDismissed:
		return true
	}
	return false
}

// ErrSuggestionDecided is returned when Accept/Dismiss is called on a
// suggestion the learner has already curated (not pending).
var ErrSuggestionDecided = fmt.Errorf("conceptgraph: suggestion already decided")

// Suggestion is a Companion-proposed concept or edge awaiting learner curation
// (ADR-212 D4). It carries the ADR-197 decision-stamps (model/run) + a
// learner-facing rationale (ADR-215) and the originating event id (idempotent
// ingest). Exactly one payload shape is populated per Kind.
type Suggestion struct {
	SuggestionID string
	TenantID     string
	LearnerGCID  string
	Kind         SuggestionKind
	Status       SuggestionStatus

	// concept payload (Kind == concept)
	Title    string
	AtomRefs []string

	// edge payload (Kind == edge)
	SourceConceptID string
	TargetConceptID string
	EdgeClass       EdgeClass

	// explainability + provenance stamps
	Rationale         string // learner-facing "why" (ADR-215); may be empty
	ModelID           string // ADR-197 decision-stamp; may be empty
	RunID             string // ADR-197 decision-stamp; may be empty
	SourceCompanionID string // which Companion proposed it; may be empty
	SourceEventID     string // originating emitted-event id (idempotent ingest); may be empty

	// focal scope — the concept the fog generated this suggestion around; empty
	// = a whole-map suggestion (visible on every node). Scopes the learner's
	// per-node curation inbox so a stale prior-generate batch (a different focal)
	// doesn't leak onto other concepts (ADR-212 WS-4).
	FocalConceptID string // may be empty (whole-map)

	CreatedAt time.Time
	UpdatedAt time.Time
	DecidedAt *time.Time // set when accepted/dismissed
	DeletedAt *time.Time // soft-delete (#5)
}

// NewConceptSuggestionInput is the constructor input for a concept suggestion.
type NewConceptSuggestionInput struct {
	TenantID          string
	LearnerGCID       string
	Title             string
	AtomRefs          []string
	Rationale         string
	ModelID           string
	RunID             string
	SourceCompanionID string
	SourceEventID     string
	FocalConceptID    string // the focal concept the fog generated around; empty = whole-map
	Now               time.Time
}

// NewConceptSuggestion mints a pending concept suggestion. Title is required (a
// suggested concept is named); AtomRefs may be empty but each present ref must
// be UUID-shaped (fail-loud).
func NewConceptSuggestion(in NewConceptSuggestionInput) (*Suggestion, error) {
	tenant, learner, err := requireTenantLearner(in.TenantID, in.LearnerGCID)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, fmt.Errorf("%w: a suggested concept must be named (title required)", ErrInvalid)
	}
	refs, err := normaliseAtomRefs(in.AtomRefs)
	if err != nil {
		return nil, err
	}
	now := resolveNow(in.Now)
	return &Suggestion{
		SuggestionID:      domain.NewUUIDv7(),
		TenantID:          tenant,
		LearnerGCID:       learner,
		Kind:              SuggestionKindConcept,
		Status:            SuggestionStatusPending,
		Title:             title,
		AtomRefs:          refs,
		Rationale:         strings.TrimSpace(in.Rationale),
		ModelID:           strings.TrimSpace(in.ModelID),
		RunID:             strings.TrimSpace(in.RunID),
		SourceCompanionID: strings.TrimSpace(in.SourceCompanionID),
		SourceEventID:     strings.TrimSpace(in.SourceEventID),
		FocalConceptID:    strings.TrimSpace(in.FocalConceptID),
		CreatedAt:         now,
		UpdatedAt:         now,
	}, nil
}

// NewEdgeSuggestionInput is the constructor input for an edge suggestion.
type NewEdgeSuggestionInput struct {
	TenantID          string
	LearnerGCID       string
	SourceConceptID   string
	TargetConceptID   string
	Class             EdgeClass
	Rationale         string
	ModelID           string
	RunID             string
	SourceCompanionID string
	SourceEventID     string
	FocalConceptID    string // the focal concept the fog generated around; empty = whole-map
	Now               time.Time
}

// NewEdgeSuggestion mints a pending edge suggestion between two existing
// concepts. A concept cannot relate to itself (ErrSelfLoop).
func NewEdgeSuggestion(in NewEdgeSuggestionInput) (*Suggestion, error) {
	tenant, learner, err := requireTenantLearner(in.TenantID, in.LearnerGCID)
	if err != nil {
		return nil, err
	}
	source := strings.TrimSpace(in.SourceConceptID)
	if source == "" {
		return nil, fmt.Errorf("%w: source_concept_id required", ErrInvalid)
	}
	target := strings.TrimSpace(in.TargetConceptID)
	if target == "" {
		return nil, fmt.Errorf("%w: target_concept_id required", ErrInvalid)
	}
	if source == target {
		return nil, fmt.Errorf("%w: source %q == target", ErrSelfLoop, source)
	}
	if !in.Class.Valid() {
		return nil, fmt.Errorf("%w: unknown edge class %q", ErrInvalid, in.Class)
	}
	now := resolveNow(in.Now)
	return &Suggestion{
		SuggestionID:      domain.NewUUIDv7(),
		TenantID:          tenant,
		LearnerGCID:       learner,
		Kind:              SuggestionKindEdge,
		Status:            SuggestionStatusPending,
		AtomRefs:          []string{}, // edges reference no atoms; keep non-nil so the pg NOT NULL atom_refs binds '{}' not NULL
		SourceConceptID:   source,
		TargetConceptID:   target,
		EdgeClass:         in.Class,
		Rationale:         strings.TrimSpace(in.Rationale),
		ModelID:           strings.TrimSpace(in.ModelID),
		RunID:             strings.TrimSpace(in.RunID),
		SourceCompanionID: strings.TrimSpace(in.SourceCompanionID),
		SourceEventID:     strings.TrimSpace(in.SourceEventID),
		FocalConceptID:    strings.TrimSpace(in.FocalConceptID),
		CreatedAt:         now,
		UpdatedAt:         now,
	}, nil
}

// Accept curates a pending suggestion into the learner's map: it flips the
// suggestion to accepted (recording DecidedAt) and MINTS the real aggregate(s)
// with provenance=companion_suggested_accepted. An edge suggestion mints an
// Edge; a concept suggestion mints a ConceptNode AND — when it was suggested
// around a focal — a hierarchy Edge linking the focal to the new node, so the
// concept lands ON the map (a whole-map concept suggestion mints the node
// alone). Accepting an already-decided suggestion is rejected
// (ErrSuggestionDecided) — the caller persists the minted aggregate(s) + the
// flipped suggestion in one transaction.
func (s *Suggestion) Accept(now time.Time) (*ConceptNode, *Edge, error) {
	if err := s.ensurePending(); err != nil {
		return nil, nil, err
	}
	n := resolveNow(now)
	switch s.Kind {
	case SuggestionKindConcept:
		node, err := NewConceptNode(NewConceptNodeInput{
			TenantID: s.TenantID, LearnerGCID: s.LearnerGCID, Title: s.Title,
			AtomRefs: s.AtomRefs, Provenance: ProvenanceCompanionSuggestedAccepted, Now: n,
		})
		if err != nil {
			return nil, nil, err
		}
		// LINK the new concept onto the map as a child of the focal it was
		// suggested around — otherwise it lands disconnected and never appears
		// on the goal graph (2026-07-05 KG-walk finding #2: accept was a map
		// no-op). A whole-map suggestion (no focal) has nothing to attach to, so
		// it mints the node alone. ApplyAccept persists node + edge in one tx.
		var edge *Edge
		if s.FocalConceptID != "" {
			edge, err = NewEdge(NewEdgeInput{
				TenantID: s.TenantID, LearnerGCID: s.LearnerGCID,
				SourceConceptID: s.FocalConceptID, TargetConceptID: node.ConceptID,
				Class: EdgeClassHierarchy, Provenance: ProvenanceCompanionSuggestedAccepted, Now: n,
			})
			if err != nil {
				return nil, nil, err
			}
		}
		s.markDecided(SuggestionStatusAccepted, n)
		return node, edge, nil
	case SuggestionKindEdge:
		edge, err := NewEdge(NewEdgeInput{
			TenantID: s.TenantID, LearnerGCID: s.LearnerGCID,
			SourceConceptID: s.SourceConceptID, TargetConceptID: s.TargetConceptID,
			Class: s.EdgeClass, Provenance: ProvenanceCompanionSuggestedAccepted, Now: n,
		})
		if err != nil {
			return nil, nil, err
		}
		s.markDecided(SuggestionStatusAccepted, n)
		return nil, edge, nil
	default:
		return nil, nil, fmt.Errorf("%w: unknown suggestion kind %q", ErrInvalid, s.Kind)
	}
}

// Dismiss curates a pending suggestion out of the map (learner declines it). It
// flips the status to dismissed and records DecidedAt; nothing is materialised.
// The row is retained (dismissed, not hard-deleted) so the Companion does not
// re-propose it and for audit; closure/pseudonymise handles eventual removal.
func (s *Suggestion) Dismiss(now time.Time) error {
	if err := s.ensurePending(); err != nil {
		return err
	}
	s.markDecided(SuggestionStatusDismissed, resolveNow(now))
	return nil
}

func (s *Suggestion) ensurePending() error {
	if s.DeletedAt != nil {
		return fmt.Errorf("%w: suggestion is soft-deleted", ErrSuggestionDecided)
	}
	if s.Status != SuggestionStatusPending {
		return fmt.Errorf("%w: status is %q", ErrSuggestionDecided, s.Status)
	}
	return nil
}

func (s *Suggestion) markDecided(status SuggestionStatus, now time.Time) {
	s.Status = status
	s.DecidedAt = &now
	s.UpdatedAt = now
}

// requireTenantLearner trims + requires the tenant + learner scope (shared by
// both suggestion constructors).
func requireTenantLearner(tenantID, learnerGCID string) (string, string, error) {
	tenant := strings.TrimSpace(tenantID)
	if tenant == "" {
		return "", "", fmt.Errorf("%w: tenant_id required", ErrInvalid)
	}
	learner := strings.TrimSpace(learnerGCID)
	if learner == "" {
		return "", "", fmt.Errorf("%w: learner_gcid required", ErrInvalid)
	}
	return tenant, learner, nil
}
