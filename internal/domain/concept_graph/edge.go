package conceptgraph

import (
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// EdgeClass distinguishes the two relation kinds (ADR-212 D2): hierarchy edges
// are the parent/child rings (the only edges moved on a re-root); lateral edges
// are relates-to cross-links.
type EdgeClass string

const (
	EdgeClassHierarchy EdgeClass = "hierarchy"
	EdgeClassLateral   EdgeClass = "lateral"
)

// Valid reports whether c is a recognised edge class.
func (c EdgeClass) Valid() bool {
	switch c {
	case EdgeClassHierarchy, EdgeClassLateral:
		return true
	}
	return false
}

// MaxHexFaceRelations is the SOFT cap of relations rendered on a concept's hex
// face (ADR-212 D6). Unlike ADR-143's hard exactly-6 rule, extra relations are
// accepted and spill into an overflow drawer — see PartitionByHexFace.
const MaxHexFaceRelations = 6

// Edge is a learner-authored typed relation between two ConceptNodes (ADR-212
// D2). Endpoints are opaque concept UUIDs with NO FK — they are separate
// aggregates, and re-rooting / soft-delete must not be blocked by referential
// integrity (#3).
type Edge struct {
	EdgeID          string
	TenantID        string
	LearnerGCID     string
	SourceConceptID string
	TargetConceptID string
	Class           EdgeClass
	Provenance      Provenance
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
}

// NewEdgeInput is the constructor input for an Edge.
type NewEdgeInput struct {
	TenantID        string
	LearnerGCID     string
	SourceConceptID string
	TargetConceptID string
	Class           EdgeClass
	Provenance      Provenance // defaults to learner_authored when empty
	Now             time.Time
}

// NewEdge mints a learner-owned Edge. A concept cannot relate to itself
// (ErrSelfLoop).
func NewEdge(in NewEdgeInput) (*Edge, error) {
	tenant := strings.TrimSpace(in.TenantID)
	if tenant == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalid)
	}
	learner := strings.TrimSpace(in.LearnerGCID)
	if learner == "" {
		return nil, fmt.Errorf("%w: learner_gcid required", ErrInvalid)
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
	prov := in.Provenance
	if prov == "" {
		prov = ProvenanceLearnerAuthored
	}
	if !prov.Valid() {
		return nil, fmt.Errorf("%w: unknown provenance %q", ErrInvalid, prov)
	}
	now := resolveNow(in.Now)
	return &Edge{
		EdgeID:          domain.NewUUIDv7(),
		TenantID:        tenant,
		LearnerGCID:     learner,
		SourceConceptID: source,
		TargetConceptID: target,
		Class:           in.Class,
		Provenance:      prov,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

// SoftDelete tombstones the edge (idempotent; never hard-deletes, #5).
func (e *Edge) SoftDelete(now time.Time) {
	if e.DeletedAt != nil {
		return
	}
	n := resolveNow(now)
	e.DeletedAt = &n
	e.UpdatedAt = n
}

// PartitionByHexFace splits a concept's relations into the (≤6) shown on its
// hex face and the overflow drawer, order-preserved (ADR-212 D6 soft cap). It
// supersedes ADR-143's hard exactly-6 constraint: a 7th relation is accepted,
// not rejected. Callers order `relations` (e.g. by recency/priority) before
// partitioning.
func PartitionByHexFace(relations []Edge) (onFace, overflow []Edge) {
	if len(relations) <= MaxHexFaceRelations {
		return relations, nil
	}
	return relations[:MaxHexFaceRelations], relations[MaxHexFaceRelations:]
}
