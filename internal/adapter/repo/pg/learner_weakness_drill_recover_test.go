// learner_weakness_drill_recover_test.go — W4 drill-completion recovery
// (RecoverByDrillAtomID): SQL-shape + RLS contract + only-lower + multi-edge
// semantics, verified against the in-memory lwStub* (no live pgvector). This is
// the path that closes the upload→practice→grow loop for EXPLICIT edges, whose
// minted concept_key never equals a drilled atom's broad primary topic.
//
// ADR-196 B1: RecoverByDrillAtomID returns the edges that crossed active→grown
// (0..N; empty when it recovered-but-did-not-grow or no-op). "How many recovered"
// is asserted via the recovery UPDATE count, not the return.
package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
)

// lwDrillRow builds the row scanLearnerWeakness expects for an edge whose drill
// cache contains drillAtomID, with the supplied strength + status. The trailing
// "" is the ADR-238 target_concept_id (UNMATCHED for a recovery-path row).
func lwDrillRow(id, drillAtomID string, strength float64, status string) []any {
	return []any{
		id,                                // id
		"single-responsibility-principle", // concept_key (analyser-minted)
		"Single Responsibility Principle", // concept_label
		"",                                // category
		"",                                // topic_id
		[]string{"solid"},                 // tags
		strength,                          // strength
		[]string{"explicit"},              // sources
		`{}`,                              // descriptor
		[]string{drillAtomID},             // cached_drill_atom_ids
		status,                            // status
		time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC), // first_seen_at
		time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC), // last_evidenced_at
		"", // target_concept_id (ADR-238)
	}
}

const drillAtomID = "019e8167-cbcd-7576-addf-6759b22125fc"

// recoveryUpdateCount counts the only-lower recovery UPDATEs issued.
func recoveryUpdateCount(q *lwStubQuerier) int {
	n := 0
	for _, sql := range q.execCalls {
		if contains(sql, "UPDATE learner_weakness") && contains(sql, "strength = $2") {
			n++
		}
	}
	return n
}

func TestLWRecoverByDrillAtom_LowersAllMatchingEdges(t *testing.T) {
	q := &lwStubQuerier{rows: &lwStubRows{rows: [][]any{
		lwDrillRow("edge-a", drillAtomID, 0.8, "active"),
		lwDrillRow("edge-b", drillAtomID, 0.6, "active"),
	}}}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: q})
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)

	grown, err := repo.RecoverByDrillAtomID(withCtx(), pgUserGCID, drillAtomID, 0.2, now)
	if err != nil {
		t.Fatalf("RecoverByDrillAtomID: %v", err)
	}
	// Both lowered to 0.2 (> mastered threshold) — neither grew.
	if len(grown) != 0 {
		t.Fatalf("recovered-but-not-grown edges must report no grown transitions; got %+v", grown)
	}
	// The drill load keys off cached_drill_atom_ids membership, per learner, live rows only.
	if len(q.queryCalls) != 1 ||
		!contains(q.queryCalls[0], "cached_drill_atom_ids @>") ||
		!contains(q.queryCalls[0], "learner_gcid = $1") ||
		!contains(q.queryCalls[0], "deleted_at IS NULL") {
		t.Fatalf("unexpected drill-load SQL: %v", q.queryCalls)
	}
	// One only-lower recovery UPDATE per matched edge.
	if got := recoveryUpdateCount(q); got != 2 {
		t.Fatalf("recovery UPDATEs = %d, want 2", got)
	}
}

func TestLWRecoverByDrillAtom_OnlyLowers(t *testing.T) {
	q := &lwStubQuerier{rows: &lwStubRows{rows: [][]any{lwDrillRow("edge-a", drillAtomID, 0.3, "active")}}}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: q})

	grown, err := repo.RecoverByDrillAtomID(withCtx(), pgUserGCID, drillAtomID, 0.9, time.Now().UTC())
	if err != nil {
		t.Fatalf("RecoverByDrillAtomID: %v", err)
	}
	if len(grown) != 0 {
		t.Fatalf("a would-be raise must recover (and grow) nothing; got %+v", grown)
	}
	if recoveryUpdateCount(q) != 0 {
		t.Fatalf("only-lower violated: issued %d recovery UPDATEs", recoveryUpdateCount(q))
	}
}

// ADR-196 B1: a drilled edge recovered across the mastery threshold is reported
// as a GrownEdge (recovery_source = drill_atom at the projector).
func TestLWRecoverByDrillAtom_MasteredFlipsGrown(t *testing.T) {
	q := &lwStubQuerier{rows: &lwStubRows{rows: [][]any{lwDrillRow("edge-a", drillAtomID, 0.5, "active")}}}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: q})
	now := time.Date(2026, 6, 28, 10, 30, 0, 0, time.UTC)

	grown, err := repo.RecoverByDrillAtomID(withCtx(), pgUserGCID, drillAtomID, 0.05, now)
	if err != nil {
		t.Fatalf("RecoverByDrillAtomID: %v", err)
	}
	if len(grown) != 1 {
		t.Fatalf("an active→grown drill recovery must report one grown edge; got %+v", grown)
	}
	g := grown[0]
	if g.ID != "edge-a" {
		t.Fatalf("grown id = %q, want edge-a", g.ID)
	}
	if g.ConceptKey != "single-responsibility-principle" || g.ConceptLabel != "Single Responsibility Principle" {
		t.Fatalf("grown concept = %q/%q", g.ConceptKey, g.ConceptLabel)
	}
	if g.FinalStrength != 0.05 {
		t.Fatalf("grown final_strength = %v, want 0.05", g.FinalStrength)
	}
	if len(g.Tags) != 1 || g.Tags[0] != "solid" {
		t.Fatalf("grown tags = %v", g.Tags)
	}
	if !g.GrownAt.Equal(now) {
		t.Fatalf("grown_at = %v, want %v", g.GrownAt, now)
	}
}

// Multi-edge: when the target strength is below the mastery threshold, every
// matched active edge that it lowers crosses to grown — one GrownEdge each.
func TestLWRecoverByDrillAtom_MultipleEdgesGrow(t *testing.T) {
	q := &lwStubQuerier{rows: &lwStubRows{rows: [][]any{
		lwDrillRow("edge-a", drillAtomID, 0.12, "active"),
		lwDrillRow("edge-b", drillAtomID, 0.8, "active"),
	}}}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: q})

	grown, err := repo.RecoverByDrillAtomID(withCtx(), pgUserGCID, drillAtomID, 0.09, time.Now().UTC())
	if err != nil {
		t.Fatalf("RecoverByDrillAtomID: %v", err)
	}
	if len(grown) != 2 {
		t.Fatalf("both edges recovered below the threshold must grow; got %+v", grown)
	}
	ids := map[string]bool{grown[0].ID: true, grown[1].ID: true}
	if !ids["edge-a"] || !ids["edge-b"] {
		t.Fatalf("grown ids = %v, want edge-a + edge-b", ids)
	}
	if recoveryUpdateCount(q) != 2 {
		t.Fatalf("recovery UPDATEs = %d, want 2", recoveryUpdateCount(q))
	}
}

func TestLWRecoverByDrillAtom_AlreadyGrown_NoTransition(t *testing.T) {
	q := &lwStubQuerier{rows: &lwStubRows{rows: [][]any{lwDrillRow("edge-a", drillAtomID, 0.05, "grown")}}}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: q})

	grown, err := repo.RecoverByDrillAtomID(withCtx(), pgUserGCID, drillAtomID, 0.02, time.Now().UTC())
	if err != nil {
		t.Fatalf("RecoverByDrillAtomID: %v", err)
	}
	if len(grown) != 0 {
		t.Fatalf("an already-grown edge must not re-report a transition; got %+v", grown)
	}
}

func TestLWRecoverByDrillAtom_NoMatchNoOp(t *testing.T) {
	q := &lwStubQuerier{} // Query default -> empty rows
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: q})

	grown, err := repo.RecoverByDrillAtomID(withCtx(), pgUserGCID, drillAtomID, 0.2, time.Now().UTC())
	if err != nil {
		t.Fatalf("RecoverByDrillAtomID: %v", err)
	}
	if len(grown) != 0 {
		t.Fatalf("no matching edge must grow nothing; got %+v", grown)
	}
	if recoveryUpdateCount(q) != 0 {
		t.Fatalf("no recovery UPDATE expected; execs: %v", q.execCalls)
	}
}

func TestLWRecoverByDrillAtom_RequiresAtomID(t *testing.T) {
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: &lwStubQuerier{}})
	if _, err := repo.RecoverByDrillAtomID(withCtx(), pgUserGCID, "   ", 0.2, time.Now().UTC()); err == nil {
		t.Fatal("a blank atom id must error")
	}
}

func TestLWRecoverByDrillAtom_FailsLoudOnMissingTenant(t *testing.T) {
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: &lwStubQuerier{}})
	if _, err := repo.RecoverByDrillAtomID(context.Background(), pgUserGCID, drillAtomID, 0.2, time.Now().UTC()); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("err = %v; want ErrNoTenantContext", err)
	}
}

func TestLWRecoverByDrillAtom_PropagatesQueryError(t *testing.T) {
	q := &lwStubQuerier{queryErr: errors.New("drill boom")}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: q})
	if _, err := repo.RecoverByDrillAtomID(withCtx(), pgUserGCID, drillAtomID, 0.2, time.Now().UTC()); err == nil {
		t.Fatal("drill-load query error must propagate")
	}
}

func TestLWRecoverByDrillAtom_PropagatesUpdateError(t *testing.T) {
	q := &lwStubQuerier{
		rows:     &lwStubRows{rows: [][]any{lwDrillRow("edge-a", drillAtomID, 0.8, "active")}},
		failExec: "status = $3", // recoverLearnerWeaknessSQL binds (id, strength, status, now)
	}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: q})
	if _, err := repo.RecoverByDrillAtomID(withCtx(), pgUserGCID, drillAtomID, 0.2, time.Now().UTC()); err == nil {
		t.Fatal("recovery UPDATE failure must propagate")
	}
}
