package http

// resonance_title_resolver_test.go, D3 slice 1b (CHO-2047): the PRODUCTION
// ResonanceTitleReader, over the two projections that already hold the names.
//
// Slice 1 shipped the port and left it nil in every deployment, so the profile
// renders "this concept is gone" everywhere. That resting state is honest but
// it is not finished, and it hides the defect it resembles: a nil reader and a
// wired reader that resolves nothing are INDISTINGUISHABLE from outside,
// because absence is the designed answer for both. Every negative case below is
// therefore preceded by a POSITIVE CONTROL proving a resolvable id comes back
// NAMED through the same code path.
//
// Both sources are in-domain, deliberately:
//
//   - the concept is the learner's own ConceptNode in chora_consumption,
//     scoped by (tenant, learner) on top of RLS;
//   - the atom title is the local atom_index projection, fed by
//     chora.creation.atom.created.v1.
//
// Reading chora_creation directly would be a cross-DB query, which is
// forbidden. Resolving through a partial-coverage source would be worse than
// not resolving at all: a live atom would render to the learner as gone.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

const (
	resolverTenant = "tenant-1"
	resolverGCID   = "gcid-1"
)

// stubConcepts is a ConceptNodeRepository whose GetByID arm is set per test and
// which records the scope it was handed. The other three methods exist only to
// satisfy the port; this resolver reads and never writes.
type stubConcepts struct {
	getFn      func(conceptID string) (*conceptgraph.ConceptNode, error)
	sawTenant  string
	sawGCID    string
	sawConcept string
	calls      int
}

func (s *stubConcepts) GetByID(_ context.Context, tenantID, learnerGCID, conceptID string) (*conceptgraph.ConceptNode, error) {
	s.calls++
	s.sawTenant, s.sawGCID, s.sawConcept = tenantID, learnerGCID, conceptID
	if s.getFn != nil {
		return s.getFn(conceptID)
	}
	return nil, nil
}

func (s *stubConcepts) Create(context.Context, *conceptgraph.ConceptNode) error { return nil }
func (s *stubConcepts) Update(context.Context, *conceptgraph.ConceptNode) error { return nil }
func (s *stubConcepts) ListByLearner(context.Context, string, string) ([]*conceptgraph.ConceptNode, error) {
	return nil, nil
}

// failingAtoms is an atom_index.Repo whose Get always fails, for the unreadable
// case. It embeds the real in-memory repo so the rest of the port is honest.
type failingAtoms struct {
	*inmem.AtomIndexRepo
	err error
}

func (f *failingAtoms) Get(context.Context, string) (*atom_index.AtomIndex, error) {
	return nil, f.err
}

// namedConcept is a live ConceptNode the learner owns.
func namedConcept(title string) *conceptgraph.ConceptNode {
	return &conceptgraph.ConceptNode{
		ConceptID:   resonantConceptID,
		TenantID:    resolverTenant,
		LearnerGCID: resolverGCID,
		Title:       title,
	}
}

// indexedAtom seeds the REAL in-memory atom_index projection with one published
// atom, so the atom arm's positive control runs against a genuine repo rather
// than a hand-rolled stub that can only agree with me.
func indexedAtom(t *testing.T, title string) *inmem.AtomIndexRepo {
	t.Helper()
	repo := inmem.NewAtomIndexRepo()
	a, err := atom_index.New(atom_index.NewParams{
		AtomID:      resonantAtomID,
		TenantID:    resolverTenant,
		CourseID:    "course-1",
		Title:       title,
		AtomType:    "mcq",
		Status:      atom_index.StatusPublished,
		PublishedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("seed atom_index: %v", err)
	}
	if err := repo.Save(context.Background(), a); err != nil {
		t.Fatalf("save atom_index: %v", err)
	}
	return repo
}

// scopedCtx is the context the growth handler hands the reader: tenant and
// owner stamped, which is what the pg repos' rls.ApplySession reads.
func scopedCtx() context.Context {
	ctx := tracing.WithTenantID(context.Background(), resolverTenant)
	return tracing.WithGCID(ctx, resolverGCID)
}

// ---------------------------------------------------------------------------
// Positive controls. These run FIRST on purpose: until a resolvable id comes
// back named, every "absent" assertion below is worthless.
// ---------------------------------------------------------------------------

func TestResonanceTitleResolver_NamesAConceptTheLearnerOwns(t *testing.T) {
	concepts := &stubConcepts{
		getFn: func(string) (*conceptgraph.ConceptNode, error) {
			return namedConcept("Roundabout priority"), nil
		},
	}
	r := NewResonanceTitleResolver(concepts, inmem.NewAtomIndexRepo())

	got, err := r.ConceptTitle(scopedCtx(), resonantConceptID)
	if err != nil {
		t.Fatalf("ConceptTitle: unexpected error %v", err)
	}
	if got != "Roundabout priority" {
		t.Fatalf("ConceptTitle = %q, want %q (the positive control: without this "+
			"every absent-title assertion in this file proves nothing)", got, "Roundabout priority")
	}
}

func TestResonanceTitleResolver_NamesAnAtomInTheIndex(t *testing.T) {
	r := NewResonanceTitleResolver(&stubConcepts{}, indexedAtom(t, "Who yields on entry"))

	got, err := r.AtomTitle(scopedCtx(), resonantAtomID)
	if err != nil {
		t.Fatalf("AtomTitle: unexpected error %v", err)
	}
	if got != "Who yields on entry" {
		t.Fatalf("AtomTitle = %q, want %q (positive control against the real "+
			"in-memory atom_index projection)", got, "Who yields on entry")
	}
}

func TestResonanceTitleResolver_ScopesTheConceptReadToTheCallerTenantAndOwner(t *testing.T) {
	// The concept graph is per-learner and RLS-enforcing. The resolver takes no
	// tenant argument, so it MUST lift the scope off the context the growth read
	// stamped. Passing a bare or wrong scope would either read nothing or,
	// worse, read another learner's map.
	concepts := &stubConcepts{
		getFn: func(string) (*conceptgraph.ConceptNode, error) {
			return namedConcept("Roundabout priority"), nil
		},
	}
	r := NewResonanceTitleResolver(concepts, inmem.NewAtomIndexRepo())

	if _, err := r.ConceptTitle(scopedCtx(), resonantConceptID); err != nil {
		t.Fatalf("ConceptTitle: %v", err)
	}
	if concepts.sawTenant != resolverTenant {
		t.Errorf("tenant = %q, want %q (lifted from the RLS-scoped context)", concepts.sawTenant, resolverTenant)
	}
	if concepts.sawGCID != resolverGCID {
		t.Errorf("learner = %q, want %q (a concept graph is per-learner)", concepts.sawGCID, resolverGCID)
	}
	if concepts.sawConcept != resonantConceptID {
		t.Errorf("concept id = %q, want %q", concepts.sawConcept, resonantConceptID)
	}
}

// ---------------------------------------------------------------------------
// Absence: a real state, reported as an empty title with a NIL error.
// ---------------------------------------------------------------------------

func TestResonanceTitleResolver_AbsentWhenTheConceptLeftTheMap(t *testing.T) {
	// ConceptNodeRepository.GetByID returns (nil, nil) when nothing live
	// matches. The companion row still points at the id; the concept is gone.
	r := NewResonanceTitleResolver(&stubConcepts{}, inmem.NewAtomIndexRepo())

	got, err := r.ConceptTitle(scopedCtx(), resonantConceptID)
	if err != nil {
		t.Fatalf("a departed concept is not an error, got %v", err)
	}
	if got != "" {
		t.Fatalf("ConceptTitle = %q, want empty so the profile can say it is gone", got)
	}
}

func TestResonanceTitleResolver_AbsentWhenTheAtomIsNotIndexed(t *testing.T) {
	// An empty projection: the atom exists in chora_creation but its created
	// event has not landed here, or it was soft-deleted.
	r := NewResonanceTitleResolver(&stubConcepts{}, inmem.NewAtomIndexRepo())

	got, err := r.AtomTitle(scopedCtx(), resonantAtomID)
	if err != nil {
		t.Fatalf("ErrNotFound must translate to absence, not an error, got %v", err)
	}
	if got != "" {
		t.Fatalf("AtomTitle = %q, want empty", got)
	}
}

func TestResonanceTitleResolver_AbsentForAnUntitledRow(t *testing.T) {
	// A projection row can carry an empty title (a pre-title event replay).
	// Absent beats an empty string: the profile prints the honest copy instead
	// of a blank where a name should be.
	concepts := &stubConcepts{
		getFn: func(string) (*conceptgraph.ConceptNode, error) { return namedConcept(""), nil },
	}
	r := NewResonanceTitleResolver(concepts, inmem.NewAtomIndexRepo())

	got, err := r.ConceptTitle(scopedCtx(), resonantConceptID)
	if err != nil || got != "" {
		t.Fatalf("ConceptTitle = (%q, %v), want an absent title and no error", got, err)
	}
}

// ---------------------------------------------------------------------------
// Failure: loud, so the operator can tell a broken wire from a departed concept.
// ---------------------------------------------------------------------------

func TestResonanceTitleResolver_PropagatesAConceptReadFailure(t *testing.T) {
	// The handler omits the title either way, but it LOGS this one. Swallowing
	// the error here is exactly how a broken read hides behind honest copy.
	boom := errors.New("concept store unreachable")
	concepts := &stubConcepts{
		getFn: func(string) (*conceptgraph.ConceptNode, error) { return nil, boom },
	}
	r := NewResonanceTitleResolver(concepts, inmem.NewAtomIndexRepo())

	got, err := r.ConceptTitle(scopedCtx(), resonantConceptID)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the underlying read failure so it reaches the log", err)
	}
	if got != "" {
		t.Fatalf("ConceptTitle = %q, want empty on failure", got)
	}
}

func TestResonanceTitleResolver_PropagatesAnAtomReadFailure(t *testing.T) {
	boom := errors.New("atom index unreachable")
	r := NewResonanceTitleResolver(&stubConcepts{}, &failingAtoms{AtomIndexRepo: inmem.NewAtomIndexRepo(), err: boom})

	got, err := r.AtomTitle(scopedCtx(), resonantAtomID)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the underlying read failure", err)
	}
	if got != "" {
		t.Fatalf("AtomTitle = %q, want empty on failure", got)
	}
}

func TestResonanceTitleResolver_FailsLoudWithoutATenantOnTheContext(t *testing.T) {
	// A missing scope is a WIRING fault, not a departed concept. Returning
	// absence here would print "this concept is gone" to every learner on the
	// platform and never say why.
	concepts := &stubConcepts{
		getFn: func(string) (*conceptgraph.ConceptNode, error) {
			return namedConcept("Roundabout priority"), nil
		},
	}
	r := NewResonanceTitleResolver(concepts, inmem.NewAtomIndexRepo())

	if _, err := r.ConceptTitle(context.Background(), resonantConceptID); err == nil {
		t.Fatal("an unscoped context must be an ERROR: absence would read to the " +
			"learner as a departed concept and hide the broken scope")
	}
	if concepts.calls != 0 {
		t.Fatalf("read the repo with no tenant scope (%d calls); RLS would have "+
			"silently returned zero rows", concepts.calls)
	}
}

func TestResonanceTitleResolver_FailsLoudWithoutALearnerOnTheContext(t *testing.T) {
	concepts := &stubConcepts{}
	r := NewResonanceTitleResolver(concepts, inmem.NewAtomIndexRepo())
	ctx := tracing.WithTenantID(context.Background(), resolverTenant)

	if _, err := r.ConceptTitle(ctx, resonantConceptID); err == nil {
		t.Fatal("a concept graph is per-learner: no owner on the context must be an error")
	}
	if concepts.calls != 0 {
		t.Fatalf("read the repo with no owner scope (%d calls)", concepts.calls)
	}
}

func TestResonanceTitleResolver_AtomArmNeedsOnlyTheTenant(t *testing.T) {
	// An atom title is tenant-visible content, not learner-private. Requiring an
	// owner here would be a scope the data does not have.
	r := NewResonanceTitleResolver(&stubConcepts{}, indexedAtom(t, "Who yields on entry"))
	ctx := tracing.WithTenantID(context.Background(), resolverTenant)

	got, err := r.AtomTitle(ctx, resonantAtomID)
	if err != nil || got != "Who yields on entry" {
		t.Fatalf("AtomTitle = (%q, %v), want the title with tenant scope alone", got, err)
	}
	if _, err := r.AtomTitle(context.Background(), resonantAtomID); err == nil {
		t.Fatal("no tenant on the context must still be an error: the pg projection is RLS-bound")
	}
}

func TestResonanceTitleResolver_FailsLoudWhenAnArmIsNotWired(t *testing.T) {
	// A half-built resolver is worse than a nil one: nil is a documented state
	// the Server understands, whereas this LOOKS wired and answers absence to
	// everything. Each arm names itself so the log says which wire is missing.
	conceptOnly := NewResonanceTitleResolver(&stubConcepts{}, nil)
	if _, err := conceptOnly.AtomTitle(scopedCtx(), resonantAtomID); err == nil {
		t.Fatal("an unwired atom arm must report itself, not answer absence")
	}

	atomOnly := NewResonanceTitleResolver(nil, inmem.NewAtomIndexRepo())
	if _, err := atomOnly.ConceptTitle(scopedCtx(), resonantConceptID); err == nil {
		t.Fatal("an unwired concept arm must report itself, not answer absence")
	}

	// The arm that IS wired keeps working: one missing source must not take the
	// other down with it.
	if _, err := atomOnly.AtomTitle(scopedCtx(), resonantAtomID); err != nil {
		t.Fatalf("a missing concept arm disabled the atom arm: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The composition. The unit tests above prove the resolver; this proves it
// reaches the wire through the handler that slice 1 shipped.
// ---------------------------------------------------------------------------

func TestResonanceTitleResolver_ReachesTheWireThroughTheGrowthRead(t *testing.T) {
	concepts := &stubConcepts{
		getFn: func(string) (*conceptgraph.ConceptNode, error) {
			return namedConcept("Roundabout priority"), nil
		},
	}
	srv := newServerWithGrowth(growthWithResonance())
	srv.ResonanceTitles = NewResonanceTitleResolver(concepts, indexedAtom(t, "Who yields on entry"))

	concept, atom, conceptSet, atomSet := readTitles(t, srv)
	if !conceptSet || concept != "Roundabout priority" {
		t.Fatalf("resonant_concept_title = %q (set=%v), want %q", concept, conceptSet, "Roundabout priority")
	}
	if !atomSet || atom != "Who yields on entry" {
		t.Fatalf("resonant_atom_title = %q (set=%v), want %q", atom, atomSet, "Who yields on entry")
	}
}

func TestResonanceTitleResolver_SatisfiesThePort(t *testing.T) {
	var _ ResonanceTitleReader = (*ResonanceTitleResolver)(nil)
}

// nilAtoms answers (nil, nil): out of contract for atom_index.Repo, which
// promises ErrNotFound for a missing row. The resolver guards it anyway,
// because the alternative is a nil dereference in a boot path a third adapter
// could reach, and a panic here would take down a growth read that is entirely
// healthy apart from a name.
type nilAtoms struct{ *inmem.AtomIndexRepo }

func (nilAtoms) Get(context.Context, string) (*atom_index.AtomIndex, error) { return nil, nil }

func TestResonanceTitleResolver_SurvivesAnOutOfContractNilRow(t *testing.T) {
	r := NewResonanceTitleResolver(&stubConcepts{}, nilAtoms{inmem.NewAtomIndexRepo()})

	got, err := r.AtomTitle(scopedCtx(), resonantAtomID)
	if err != nil || got != "" {
		t.Fatalf("AtomTitle = (%q, %v), want an absent title and no panic", got, err)
	}
}

// The atom-only reader is its OWN type, not a two-arm resolver holding a nil
// concept arm (subagent1's constraint on the per-arm liveness slice).
//
// The profile refuses a half resolver because one nil arm looks wired and
// answers absence to everything, and absence is the profile's honest "this
// concept is gone" copy. A half instance existing anywhere is a half instance
// somebody can wire to the profile later, so none is built.
func TestNewAtomTitleReader_IsNotAHalfTwoArmResolver(t *testing.T) {
	reader := NewAtomTitleReader(nil)
	if _, isTwoArm := reader.(*ResonanceTitleResolver); isTwoArm {
		t.Fatal("the atom-only reader is a ResonanceTitleResolver with a nil concept arm: " +
			"exactly the half resolver the profile refuses to build")
	}
	if _, isTwoArm := reader.(ResonanceTitleReader); isTwoArm {
		t.Error("the atom-only reader satisfies the two-arm port, so it can be wired to the profile")
	}
}
