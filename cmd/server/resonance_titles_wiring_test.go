// resonance_titles_wiring_test.go: the composition root for the character
// sheet's resonant titles (D3 slice 1b, CHO-2047).
//
// The contract shipped in slice 1 and the field stayed nil in every deployment,
// so the profile has been printing "this concept is gone" for live concepts.
// These tests pin the boot decision that ends it, and the one case that must
// NOT be papered over: with no pool the concept graph is absent, and a resolver
// wired over half a graph would answer absence to every learner while looking
// perfectly healthy. Nil is the honest state there, and the log must say so.
package main

import (
	"context"
	"os"
	"strings"
	"testing"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

// stubConceptNodes stands in for the pg ConceptNode repo. The wiring decision
// under test is presence, not behaviour, so it answers nothing.
type stubConceptNodes struct{}

func (stubConceptNodes) Create(context.Context, *conceptgraph.ConceptNode) error { return nil }
func (stubConceptNodes) Update(context.Context, *conceptgraph.ConceptNode) error { return nil }
func (stubConceptNodes) GetByID(context.Context, string, string, string) (*conceptgraph.ConceptNode, error) {
	return nil, nil
}
func (stubConceptNodes) ListByLearner(context.Context, string, string) ([]*conceptgraph.ConceptNode, error) {
	return nil, nil
}

func TestWireResonanceTitles_BindsTheReaderWhenBothProjectionsAreLive(t *testing.T) {
	srv := &httpadapter.Server{AtomIndex: inmem.NewAtomIndexRepo()}
	ext := &httpadapter.ExtServer{Concepts: stubConceptNodes{}}

	wireResonanceTitles(srv, ext)

	if srv.ResonanceTitles == nil {
		t.Fatal("both projections live and the reader is still nil: the profile " +
			"would keep saying every concept is gone")
	}
}

func TestWireResonanceTitles_LeavesTheReaderNilWithoutAConceptGraph(t *testing.T) {
	// No pgx pool ⇒ wireConceptGraphReRoot never bound ext.Concepts. A resolver
	// built here would report the concept arm unwired on EVERY read. Nil is the
	// documented not-wired state the Server already understands, so leave it.
	srv := &httpadapter.Server{AtomIndex: inmem.NewAtomIndexRepo()}
	ext := &httpadapter.ExtServer{}

	wireResonanceTitles(srv, ext)

	if srv.ResonanceTitles != nil {
		t.Fatal("wired a resolver over an absent concept graph: a half resolver " +
			"looks healthy and answers absence to everything")
	}
}

func TestWireResonanceTitles_LeavesTheReaderNilWithoutAnAtomIndex(t *testing.T) {
	srv := &httpadapter.Server{}
	ext := &httpadapter.ExtServer{Concepts: stubConceptNodes{}}

	wireResonanceTitles(srv, ext)

	if srv.ResonanceTitles != nil {
		t.Fatal("wired a resolver over an absent atom index")
	}
}

func TestWireResonanceTitles_ToleratesANilServerOrExt(t *testing.T) {
	// Boot-order insurance: this helper runs late in main and must not be the
	// thing that panics a pod on a path where an earlier step bailed.
	wireResonanceTitles(nil, &httpadapter.ExtServer{Concepts: stubConceptNodes{}})
	wireResonanceTitles(&httpadapter.Server{AtomIndex: inmem.NewAtomIndexRepo()}, nil)
}

// TestMain_CallsWireResonanceTitlesAfterItsPrerequisites guards the omission
// that created this slice.
//
// Slice 1 shipped a correct port, a correct enrichment and a passing suite, and
// resolved nothing in production for one reason: nothing called the wiring. A
// helper that is never invoked is indistinguishable, from every test above and
// from the screen itself, from one that is invoked and finds nothing. main() is
// not unit-testable here, so the call site is asserted at the source level.
//
// This is a PRESENCE and ORDER check, not an absence check: it says the call is
// there and that it follows the two binds it reads. It cannot prove the boot
// path reaches it, and it is not trying to.
func TestMain_CallsWireResonanceTitlesAfterItsPrerequisites(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	main := string(src)

	call := strings.Index(main, "wireResonanceTitles(srv, ext)")
	if call < 0 {
		t.Fatal("main.go never calls wireResonanceTitles: the reader stays nil and " +
			"the character sheet says every resonant concept is gone, exactly as " +
			"it did between slice 1 and slice 1b")
	}
	concepts := strings.Index(main, "wireConceptGraphReRoot(ext, pool)")
	atoms := strings.Index(main, "srv.AtomIndex = atomIndexRepo")
	if concepts < 0 || atoms < 0 {
		t.Fatalf("prerequisite binds moved (concepts=%d atom_index=%d); this guard "+
			"keys on them and must be updated with them", concepts, atoms)
	}
	if call < concepts {
		t.Error("wireResonanceTitles runs BEFORE wireConceptGraphReRoot: ext.Concepts " +
			"is still nil there, so the reader is left nil at every boot")
	}
	if call < atoms {
		t.Error("wireResonanceTitles runs BEFORE srv.AtomIndex is bound")
	}
}

// D2 tail (a) follow-up: the ATOM arm binds on its own.
//
// The both-arms rule above is right for the PROFILE, which reads a concept and
// an atom and would otherwise look healthy while answering absence to half of
// what it is asked. It is wrong for the ritual source titles, which read the
// atom arm ONLY: under the old rule a deployment with a live atom_index and no
// concept graph left every grounded step nameless for a reason that has
// nothing to do with atoms.
func TestWireResonanceTitles_BindsTheAtomArmWithoutAConceptGraph(t *testing.T) {
	srv := &httpadapter.Server{AtomIndex: inmem.NewAtomIndexRepo()}
	ext := &httpadapter.ExtServer{}

	wireResonanceTitles(srv, ext)

	if srv.ResonanceTitles != nil {
		t.Error("the profile reader must still refuse a half resolver")
	}
	if srv.AtomTitles == nil {
		t.Fatal("the atom index is live and ritual sources are still nameless")
	}
}

func TestWireResonanceTitles_BindsBothReadersWhenBothProjectionsAreLive(t *testing.T) {
	srv := &httpadapter.Server{AtomIndex: inmem.NewAtomIndexRepo()}
	ext := &httpadapter.ExtServer{Concepts: stubConceptNodes{}}

	wireResonanceTitles(srv, ext)

	if srv.ResonanceTitles == nil || srv.AtomTitles == nil {
		t.Fatalf("both projections live; profile reader nil=%t atom reader nil=%t",
			srv.ResonanceTitles == nil, srv.AtomTitles == nil)
	}
}

// No atom index: neither reader binds. There is nothing to read from, and a
// reader over nothing would report its arm unwired on every call.
func TestWireResonanceTitles_LeavesTheAtomReaderNilWithoutAnAtomIndex(t *testing.T) {
	srv := &httpadapter.Server{}
	ext := &httpadapter.ExtServer{Concepts: stubConceptNodes{}}

	wireResonanceTitles(srv, ext)

	if srv.AtomTitles != nil {
		t.Error("wired an atom reader with no atom index behind it")
	}
	if srv.ResonanceTitles != nil {
		t.Error("the profile reader must refuse a half resolver in this direction too")
	}
}
