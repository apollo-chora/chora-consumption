package http

// resonance_title_resolver.go: the production ResonanceTitleReader (D3 slice
// 1b, CHO-2047).
//
// Slice 1 shipped the port and the enrichment; the field stayed nil in every
// deployment, so the character sheet has been printing "this concept is gone"
// for concepts that are very much alive. This is the wiring that ends that.
//
// Both arms read projections this domain already owns:
//
//	concept → the learner's own ConceptNode (chora_consumption, per-learner,
//	          RLS-enforcing). A concept graph is learner-sovereign, so the read
//	          is scoped by (tenant, learner) as defence in depth on top of RLS.
//	atom    → the local atom_index projection, fed by
//	          chora.creation.atom.created.v1. Atom titles are owned by
//	          chora_creation and a query into that database would be a cross-DB
//	          read, which is forbidden; the projection is the sanctioned copy.
//
// Three answers, and the distinction between the last two is the whole point:
//
//	resolved   → the title
//	absent     → ("", nil), a real state: the concept left the map, or the atom
//	             was never projected here. The profile says so honestly.
//	failed     → ("", err), so the caller LOGS it. An unscoped context and an
//	             unwired arm are failures, NOT absence: answering absence there
//	             would print "gone" to every learner on the platform and never
//	             say why. That is the shape a broken wire hides in, because a
//	             reader that resolves nothing looks exactly like one that is not
//	             there at all.

import (
	"context"
	"errors"
	"fmt"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

// Failure sentinels. They exist so the operator log distinguishes a missing
// wire from a missing row; the learner is owed the same copy either way.
var (
	errTitleConceptArmUnwired = errors.New("resonance titles: concept arm not wired")
	errTitleAtomArmUnwired    = errors.New("resonance titles: atom arm not wired")
	errTitleNoTenantScope     = errors.New("resonance titles: no tenant on context (RLS would read zero rows)")
	errTitleNoLearnerScope    = errors.New("resonance titles: no learner on context (the concept graph is per-learner)")
)

// ResonanceTitleResolver resolves the two resonant ids on a growth read to the
// names a learner would recognise.
//
// Either arm may be nil. That is a half-wired resolver and it reports itself on
// every call rather than degrading to absence, because a Server holding nil is
// already the documented "not wired" state and this must not be mistaken for
// it. One dark arm never disables the other.
type ResonanceTitleResolver struct {
	concepts conceptgraph.ConceptNodeRepository
	atoms    atom_index.Repo
}

// NewResonanceTitleResolver composes the two in-domain projections.
func NewResonanceTitleResolver(concepts conceptgraph.ConceptNodeRepository, atoms atom_index.Repo) *ResonanceTitleResolver {
	return &ResonanceTitleResolver{concepts: concepts, atoms: atoms}
}

var _ ResonanceTitleReader = (*ResonanceTitleResolver)(nil)

// atomTitleResolver is the ATOM arm as its own type, for consumers that never
// ask about a concept.
//
// A SEPARATE type rather than a ResonanceTitleResolver with a nil concept arm,
// at subagent1's constraint and rightly: the profile refuses a half resolver
// because one nil arm looks wired and answers absence to everything, and
// absence is the profile's honest "this concept is gone" copy. A half instance
// existing anywhere is a half instance somebody can wire to the profile later.
// This type has no arm to be nil.
//
// It shares atomTitleFrom with the two-arm resolver, so there is still exactly
// ONE atom-title implementation in this service and the two cannot drift.
type atomTitleResolver struct {
	atoms atom_index.Repo
}

// NewAtomTitleReader builds the atom arm alone.
func NewAtomTitleReader(atoms atom_index.Repo) AtomTitleReader {
	return &atomTitleResolver{atoms: atoms}
}

var _ AtomTitleReader = (*atomTitleResolver)(nil)

func (r *atomTitleResolver) AtomTitle(ctx context.Context, atomID string) (string, error) {
	if r == nil || r.atoms == nil {
		return "", errTitleAtomArmUnwired
	}
	return atomTitleFrom(ctx, r.atoms, atomID)
}

// ConceptTitle names a concept the learner owns, or reports it gone.
//
// The scope comes off the context rather than the signature: the caller has
// already stamped tenant and owner for the RLS session that produced the id,
// and this read must ride exactly that scope. An unscoped context is refused
// BEFORE the repo call, because the pg repo would answer zero rows and the
// absence would be indistinguishable from a departed concept.
func (r *ResonanceTitleResolver) ConceptTitle(ctx context.Context, conceptID string) (string, error) {
	if r == nil || r.concepts == nil {
		return "", errTitleConceptArmUnwired
	}
	tenantID := tracing.TenantIDFromContext(ctx)
	if tenantID == "" {
		return "", errTitleNoTenantScope
	}
	learnerGCID := tracing.GCIDFromContext(ctx)
	if learnerGCID == "" {
		return "", errTitleNoLearnerScope
	}
	node, err := r.concepts.GetByID(ctx, tenantID, learnerGCID, conceptID)
	if err != nil {
		return "", fmt.Errorf("resonance titles: read concept %s: %w", conceptID, err)
	}
	if node == nil {
		// GetByID answers (nil, nil) when nothing live matches: the concept left
		// this learner's map and the companion row now points at a dangling id.
		return "", nil
	}
	return node.Title, nil
}

// AtomTitle names an atom from the local projection, or reports it absent.
//
// Tenant scope only, deliberately: an atom title is tenant-visible content, not
// learner-private, and demanding an owner would assert a scope the projection
// does not carry.
func (r *ResonanceTitleResolver) AtomTitle(ctx context.Context, atomID string) (string, error) {
	if r == nil || r.atoms == nil {
		return "", errTitleAtomArmUnwired
	}
	return atomTitleFrom(ctx, r.atoms, atomID)
}

// atomTitleFrom is the ONE atom-title lookup in this service, shared by the
// two-arm resolver above and the atom-only one. Extracted rather than
// duplicated: two copies would drift, and the drift would be silent, since both
// answer "" for an atom nobody can name.
func atomTitleFrom(ctx context.Context, atoms atom_index.Repo, atomID string) (string, error) {
	if tracing.TenantIDFromContext(ctx) == "" {
		return "", errTitleNoTenantScope
	}
	a, err := atoms.Get(ctx, atomID)
	switch {
	case errors.Is(err, atom_index.ErrNotFound):
		// Never projected here, or soft-deleted. An expected condition, not a
		// storage failure: the sentinel exists so those two stay separable.
		return "", nil
	case err != nil:
		return "", fmt.Errorf("resonance titles: read atom %s: %w", atomID, err)
	case a == nil:
		return "", nil
	}
	return a.Title, nil
}
