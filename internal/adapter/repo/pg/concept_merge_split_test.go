// concept_merge_split_test.go — WS-C6 (CHO-2085, ADR-227 D14) RLS-contract +
// SQL-shape + ordering gates for the atomic merge/split applier. Mirrors
// campaign_repo_test.go: the stub Querier captures Exec SQL so the tests pin
// (a) SET LOCAL lands before any write, (b) every aggregate's statement runs
// inside the ONE tx, (c) the ladder tombstones land BEFORE the fresh inserts
// (the tombstones ARE the D14 XP-refusal history the guard reads). RED-first.
package pg

import (
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	"github.com/apollo-chora/chora-consumption/internal/domain/mergesplit"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

func mergeSplitApplyFixture(t *testing.T) mergesplit.Apply {
	t.Helper()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	absorbed, err := conceptgraph.NewConceptNode(conceptgraph.NewConceptNodeInput{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID, Title: "Roots", Now: now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	absorbed.SoftDelete(now)

	child, err := conceptgraph.NewConceptNode(conceptgraph.NewConceptNodeInput{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID, Title: "Light Reactions", Now: now,
	})
	if err != nil {
		t.Fatalf("node: %v", err)
	}

	oldEdge, err := conceptgraph.NewEdge(conceptgraph.NewEdgeInput{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID,
		SourceConceptID: "01971b00-aaaa-7000-8000-000000000001",
		TargetConceptID: absorbed.ConceptID,
		Class:           conceptgraph.EdgeClassHierarchy, Now: now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("edge: %v", err)
	}
	oldEdge.SoftDelete(now)
	newEdge, err := conceptgraph.NewEdge(conceptgraph.NewEdgeInput{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID,
		SourceConceptID: "01971b00-aaaa-7000-8000-000000000001",
		TargetConceptID: child.ConceptID,
		Class:           conceptgraph.EdgeClassHierarchy, Now: now,
	})
	if err != nil {
		t.Fatalf("edge: %v", err)
	}

	prog, err := campaign.NewNodeProgress(pgTenantID, pgUserGCID, "01971b00-bbbb-7000-8000-000000000009", now)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	score, err := topic_retention.New(pgTenantID, pgUserGCID, "photosynthesis", now, 2)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	g, err := goal.NewGoal(goal.NewGoalInput{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID, Kind: goal.KindCuriosity, Now: now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("goal: %v", err)
	}

	return mergesplit.Apply{
		TenantID:          pgTenantID,
		LearnerGCID:       pgUserGCID,
		Now:               now,
		NodesToTombstone:  []conceptgraph.ConceptNode{*absorbed},
		NodesToCreate:     []conceptgraph.ConceptNode{*child},
		EdgesToSoftDelete: []conceptgraph.Edge{*oldEdge},
		EdgesToCreate:     []conceptgraph.Edge{*newEdge},
		Lineage: []conceptgraph.LineageRecord{{
			Operation:      conceptgraph.LineageOperationMerge,
			FromConceptID:  absorbed.ConceptID,
			ToConceptID:    "01971b00-aaaa-7000-8000-000000000002",
			FromConceptKey: "roots",
			ToConceptKey:   "photosynthesis",
		}},
		ProgressTombstoneConceptIDs: []string{absorbed.ConceptID},
		ProgressToInsert:            []*campaign.NodeProgress{prog},
		RetentionToUpsert:           []*topic_retention.TopicScore{score},
		SuggestionRepoints: []mergesplit.SuggestionRepoint{{
			FromConceptID: absorbed.ConceptID,
			ToConceptID:   "01971b00-aaaa-7000-8000-000000000002",
		}},
		GoalsToUpdate: []*goal.Goal{g},
	}
}

func TestPGMergeSplitRepo_AppliesEveryAxisInOneTx(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewMergeSplitRepo(tx)

	if err := repo.Apply(withCtx(), mergeSplitApplyFixture(t)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	calls := tx.q.execCalls
	if len(calls) < 10 {
		t.Fatalf("want RLS + 9 aggregate writes, got %d calls", len(calls))
	}
	if !contains(calls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("RLS must land before any write: %v", calls[0])
	}
	joined := ""
	for _, c := range calls {
		joined += c + "\n---\n"
	}
	for _, must := range []string{
		"UPDATE concept_nodes",               // tombstone absorbed/parent
		"INSERT INTO concept_nodes",          // split children
		"UPDATE concept_edges",               // soft-delete superseded
		"INSERT INTO concept_edges",          // re-expressed edges
		"INSERT INTO concept_node_lineage",   // dual-axis ancestry
		"UPDATE campaign_node_progress",      // ladder tombstone (refusal history)
		"INSERT INTO campaign_node_progress", // AND/inherited rows
		"INSERT INTO topic_retention",        // weakest/clone re-key
		"UPDATE concept_suggestions",         // pending focal repoint
		"UPDATE goals",                       // concept_set + focus repair
	} {
		if !contains(joined, must) {
			t.Errorf("apply missing statement %q", must)
		}
	}

	// The ladder tombstone MUST land before the fresh insert — the tombstone
	// is the D14 refusal history; inserting first would breach the partial
	// unique index on live rows.
	tombIdx, insIdx := -1, -1
	for i, c := range calls {
		if contains(c, "UPDATE campaign_node_progress") && tombIdx == -1 {
			tombIdx = i
		}
		if contains(c, "INSERT INTO campaign_node_progress") && insIdx == -1 {
			insIdx = i
		}
	}
	if tombIdx == -1 || insIdx == -1 || tombIdx > insIdx {
		t.Errorf("ladder tombstone (%d) must precede the fresh insert (%d)", tombIdx, insIdx)
	}
}

func TestPGMergeSplitRepo_RefusesInvalidApply(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewMergeSplitRepo(tx)
	if err := repo.Apply(withCtx(), mergesplit.Apply{}); err == nil {
		t.Fatalf("want validation error on empty apply, got nil")
	}
	if tx.q != nil && len(tx.q.execCalls) != 0 {
		t.Errorf("invalid apply must not touch the DB: %v", tx.q.execCalls)
	}
}

func TestWalkAncestorKeys_Transitive(t *testing.T) {
	// A merged into B (from=A,to=B); B split into C (from=B,to=C):
	// C's ancestor keys = {b, a}; B's = {a}; unrelated nodes absent.
	rows := []conceptLineageRow{
		{ToConceptID: "B", FromConceptID: "A", FromConceptKey: "a"},
		{ToConceptID: "C", FromConceptID: "B", FromConceptKey: "b"},
	}
	keys := walkAncestorKeys(rows)
	if got := keys["C"]; len(got) != 2 {
		t.Fatalf("C ancestors = %v, want [b a] (transitive)", got)
	}
	if got := keys["B"]; len(got) != 1 || got[0] != "a" {
		t.Fatalf("B ancestors = %v, want [a]", got)
	}
	if _, ok := keys["A"]; ok {
		t.Fatalf("A has no ancestors but appears in the index")
	}
}

func TestWalkAncestorKeys_CycleSafe(t *testing.T) {
	rows := []conceptLineageRow{
		{ToConceptID: "B", FromConceptID: "A", FromConceptKey: "a"},
		{ToConceptID: "A", FromConceptID: "B", FromConceptKey: "b"}, // corrupt cycle
	}
	keys := walkAncestorKeys(rows) // must terminate
	if len(keys["A"]) == 0 || len(keys["B"]) == 0 {
		t.Fatalf("cycle rows still resolve their direct ancestors: %v", keys)
	}
}

// ADR-244 D6 (CHO-2303): the merge survivor's unioned atom_refs must actually
// reach the database. The plan field and the applier loop were both added in
// one change, so without this the whole path was exercised by nothing and a
// green suite proved only that no OTHER test touched it.
func TestPGMergeSplitRepo_PersistsSurvivorAtomUnionWithoutTombstoning(t *testing.T) {
	const (
		atomA = "01971b00-cccc-7000-8000-00000000000a"
		atomB = "01971b00-cccc-7000-8000-00000000000b"
	)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	survivor, err := conceptgraph.NewConceptNode(conceptgraph.NewConceptNodeInput{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID, Title: "Photosynthesis",
		AtomRefs: []string{atomA, atomB}, Now: now,
	})
	if err != nil {
		t.Fatalf("node: %v", err)
	}

	in := mergeSplitApplyFixture(t)
	in.NodesToUpdate = []conceptgraph.ConceptNode{*survivor}

	tx := &stubTxRunner{}
	if err := NewMergeSplitRepo(tx).Apply(withCtx(), in); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Find the UPDATE carrying the survivor's id (the fixture also tombstones
	// an absorbed node through the same SQL, so match on the id argument).
	found := false
	for i, sql := range tx.q.execCalls {
		if !contains(sql, "UPDATE concept_nodes") {
			continue
		}
		args := tx.q.execArgs[i]
		if len(args) < 9 || args[0] != survivor.ConceptID {
			continue
		}
		found = true
		refs, ok := args[4].([]string)
		if !ok || len(refs) != 2 || refs[0] != atomA || refs[1] != atomB {
			t.Fatalf("survivor atom_refs arg = %#v, want [%s %s]", args[4], atomA, atomB)
		}
		// The survivor is LIVE. A deleted_at here would silently delete the
		// concept the learner merged INTO. NB the arg is a *time.Time boxed in
		// an interface, so a plain `!= nil` is TRUE even for a nil pointer.
		if dt, isPtr := args[8].(*time.Time); !isPtr || dt != nil {
			t.Fatalf("survivor deleted_at = %#v, want a nil *time.Time (survivor must stay live)", args[8])
		}
	}
	if !found {
		t.Fatal("no UPDATE concept_nodes carried the survivor id: NodesToUpdate never reached the DB")
	}
}
