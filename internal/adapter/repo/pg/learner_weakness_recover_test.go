// learner_weakness_recover_test.go — W3-derived recovery path: RLS contract +
// SQL-shape + only-lower semantics for RecoverByConceptKey, verified against the
// lwStub* in-memory stubs from learner_weakness_test.go.
//
// ADR-196 B1: RecoverByConceptKey returns the edges that crossed active→grown
// (one per real transition; empty when it recovered-but-did-not-grow or no-op).
// "Did it apply" is now asserted via the recovery UPDATE execArgs, not a bool.
package pg

import (
	"testing"
	"time"
)

// lwConceptRow builds the row scanLearnerWeakness expects, with the supplied
// strength + status. The trailing "" is the ADR-238 target_concept_id (UNMATCHED
// for a recovery-path row).
func lwConceptRow(strength float64, status string) []any {
	return []any{
		"0197aaaa-0000-7000-8000-00000000aaaa", // id
		"multiplication-tables",                // concept_key
		"Multiplication Tables",                // concept_label
		"",                                     // category
		"",                                     // topic_id
		[]string{"multiplication-tables"},      // tags
		strength,                               // strength
		[]string{"derived"},                    // sources
		`{}`,                                   // descriptor
		[]string{},                             // cached_drill_atom_ids
		status,                                 // status
		time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC), // first_seen_at
		time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC), // last_evidenced_at
		"", // target_concept_id (ADR-238)
	}
}

// recoveryUpdateArgs returns the bound args of the recovery UPDATE (id, strength,
// status, now), or nil when no recovery UPDATE was issued.
func recoveryUpdateArgs(q *lwStubQuerier) []any {
	for i, sql := range q.execCalls {
		if contains(sql, "UPDATE learner_weakness") && contains(sql, "strength = $2") {
			return q.execArgs[i]
		}
	}
	return nil
}

func TestLWRecover_NoActiveRow_NoOp(t *testing.T) {
	q := &lwStubQuerier{} // default QueryRow -> ErrNoRows
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: q})

	grown, err := repo.RecoverByConceptKey(withCtx(), pgUserGCID, "multiplication-tables", 0.2, time.Now().UTC())
	if err != nil {
		t.Fatalf("RecoverByConceptKey: %v", err)
	}
	if len(grown) != 0 {
		t.Fatalf("no active row must grow nothing; got %+v", grown)
	}
	if recoveryUpdateArgs(q) != nil {
		t.Fatalf("no UPDATE expected; execs: %v", q.execCalls)
	}
}

func TestLWRecover_LowersStrengthAndStatus(t *testing.T) {
	q := &lwStubQuerier{row: &lwStubRow{vals: lwConceptRow(0.8, "active")}}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: q})
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	grown, err := repo.RecoverByConceptKey(withCtx(), pgUserGCID, "Multiplication Tables", 0.4, now)
	if err != nil {
		t.Fatalf("RecoverByConceptKey: %v", err)
	}
	// Recovered to 0.4 (> mastered threshold) — lowered but NOT grown.
	if len(grown) != 0 {
		t.Fatalf("a recovered-but-not-grown edge must report no grown transitions; got %+v", grown)
	}

	// Load SQL selects by (learner_gcid, concept_key) on live rows only.
	if len(q.rowCalls) != 1 || !contains(q.rowCalls[0], "concept_key = $2") || !contains(q.rowCalls[0], "deleted_at IS NULL") {
		t.Fatalf("unexpected load SQL: %v", q.rowCalls)
	}

	// The recovery UPDATE binds (id, strength, status, now) — proves it applied.
	updateArgs := recoveryUpdateArgs(q)
	if updateArgs == nil {
		t.Fatalf("recovery UPDATE not issued; execs: %v", q.execCalls)
	}
	if updateArgs[0] != "0197aaaa-0000-7000-8000-00000000aaaa" {
		t.Fatalf("update id = %v", updateArgs[0])
	}
	if s, ok := updateArgs[1].(float64); !ok || s != 0.4 {
		t.Fatalf("update strength = %v, want 0.4", updateArgs[1])
	}
	if st, ok := updateArgs[2].(string); !ok || st != "active" {
		t.Fatalf("update status = %v, want active", updateArgs[2])
	}
	if ts, ok := updateArgs[3].(time.Time); !ok || !ts.Equal(now) {
		t.Fatalf("update timestamp = %v, want %v", updateArgs[3], now)
	}
}

func TestLWRecover_HigherStrengthDoesNotApply(t *testing.T) {
	q := &lwStubQuerier{row: &lwStubRow{vals: lwConceptRow(0.3, "active")}}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: q})

	grown, err := repo.RecoverByConceptKey(withCtx(), pgUserGCID, "multiplication-tables", 0.9, time.Now().UTC())
	if err != nil {
		t.Fatalf("RecoverByConceptKey: %v", err)
	}
	if len(grown) != 0 {
		t.Fatalf("Recover must never raise strength (and never grow); got %+v", grown)
	}
	if recoveryUpdateArgs(q) != nil {
		t.Fatalf("no UPDATE expected; execs: %v", q.execCalls)
	}
}

func TestLWRecover_EmptyConceptKeyErrors(t *testing.T) {
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: &lwStubQuerier{}})
	if _, err := repo.RecoverByConceptKey(withCtx(), pgUserGCID, "  --  ", 0.2, time.Now().UTC()); err == nil {
		t.Fatal("an un-keyable concept must error")
	}
}

func TestLWRecover_UpdateFailureSurfaces(t *testing.T) {
	q := &lwStubQuerier{
		row:      &lwStubRow{vals: lwConceptRow(0.8, "active")},
		failExec: "strength = $2",
	}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: q})
	if _, err := repo.RecoverByConceptKey(withCtx(), pgUserGCID, "multiplication-tables", 0.2, time.Now().UTC()); err == nil {
		t.Fatal("UPDATE failure must surface")
	}
}

// ADR-196 B1: an edge recovered across the mastery threshold (active→grown) is
// reported as a GrownEdge so the projector can publish weakness.grown.v1.
func TestLWRecover_MasteredFlipsGrown(t *testing.T) {
	q := &lwStubQuerier{row: &lwStubRow{vals: lwConceptRow(0.5, "active")}}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: q})
	now := time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC)

	grown, err := repo.RecoverByConceptKey(withCtx(), pgUserGCID, "multiplication-tables", 0.05, now)
	if err != nil {
		t.Fatalf("RecoverByConceptKey: %v", err)
	}
	if len(grown) != 1 {
		t.Fatalf("an active→grown recovery must report exactly one grown edge; got %+v", grown)
	}
	g := grown[0]
	if g.ID != "0197aaaa-0000-7000-8000-00000000aaaa" {
		t.Fatalf("grown id = %q", g.ID)
	}
	if g.ConceptKey != "multiplication-tables" || g.ConceptLabel != "Multiplication Tables" {
		t.Fatalf("grown concept = %q/%q", g.ConceptKey, g.ConceptLabel)
	}
	if g.FinalStrength != 0.05 {
		t.Fatalf("grown final_strength = %v, want 0.05", g.FinalStrength)
	}
	if len(g.Tags) != 1 || g.Tags[0] != "multiplication-tables" {
		t.Fatalf("grown tags = %v", g.Tags)
	}
	if !g.GrownAt.Equal(now) {
		t.Fatalf("grown_at = %v, want %v", g.GrownAt, now)
	}

	// The persisted UPDATE must carry status=grown.
	updateArgs := recoveryUpdateArgs(q)
	if updateArgs == nil {
		t.Fatal("recovery UPDATE not issued")
	}
	if st, _ := updateArgs[2].(string); st != "grown" {
		t.Fatalf("status arg = %v, want grown", updateArgs[2])
	}
}

// An ALREADY-grown edge recovered further (e.g. 0.05 → 0.02) must NOT re-report
// a transition — exactly one reward per real grow (ADR-196 idempotency at source).
func TestLWRecover_AlreadyGrown_NoTransition(t *testing.T) {
	q := &lwStubQuerier{row: &lwStubRow{vals: lwConceptRow(0.05, "grown")}}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: q})

	grown, err := repo.RecoverByConceptKey(withCtx(), pgUserGCID, "multiplication-tables", 0.02, time.Now().UTC())
	if err != nil {
		t.Fatalf("RecoverByConceptKey: %v", err)
	}
	if len(grown) != 0 {
		t.Fatalf("an already-grown edge must not re-report a transition; got %+v", grown)
	}
	// It still lowered (only-lower) — the UPDATE is issued, status stays grown.
	updateArgs := recoveryUpdateArgs(q)
	if updateArgs == nil {
		t.Fatal("only-lower recovery UPDATE should still be issued for a grown edge")
	}
	if st, _ := updateArgs[2].(string); st != "grown" {
		t.Fatalf("status arg = %v, want grown", updateArgs[2])
	}
}

// --- W4: SetCachedDrillAtoms ---

func TestLWSetCachedDrillAtoms_SQLShape(t *testing.T) {
	q := &lwStubQuerier{}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: q})
	now := time.Date(2026, 6, 10, 13, 0, 0, 0, time.UTC)

	err := repo.SetCachedDrillAtoms(withCtx(), pgUserGCID, "edge-1", []string{"a-1", "a-2"}, now)
	if err != nil {
		t.Fatalf("SetCachedDrillAtoms: %v", err)
	}
	var args []any
	for i, sql := range q.execCalls {
		if contains(sql, "cached_drill_atom_ids") {
			args = q.execArgs[i]
			if !contains(sql, "deleted_at IS NULL") {
				t.Fatalf("update must scope live rows: %q", sql)
			}
		}
	}
	if args == nil {
		t.Fatalf("cache UPDATE not issued; execs: %v", q.execCalls)
	}
	if args[0] != "edge-1" || args[1] != pgUserGCID {
		t.Fatalf("args = %v", args)
	}
	if ids, ok := args[2].([]string); !ok || len(ids) != 2 {
		t.Fatalf("atom ids arg = %v", args[2])
	}
}

func TestLWSetCachedDrillAtoms_RequiresID(t *testing.T) {
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: &lwStubQuerier{}})
	if err := repo.SetCachedDrillAtoms(withCtx(), pgUserGCID, " ", []string{"a"}, time.Now().UTC()); err == nil {
		t.Fatal("blank id must error")
	}
}
