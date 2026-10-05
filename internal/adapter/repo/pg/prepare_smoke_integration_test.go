//go:build integration

package pg

// prepare_smoke_integration_test.go — PREPARE every pg SQL const against a
// REAL PostgreSQL so server-side parse analysis runs (parameter type
// deduction, column existence, cast validity).
//
// Why this exists (CHO-2012 live-fire lesson, 2026-07-03): the unit suite
// stubs Exec, so `updateGrowthExpAndStageSQL` shipped with $2 deduced as
// BOTH smallint (growth_stage assignment) and integer (LEAST/CASE
// arithmetic) — SQLSTATE 42P08 on every stage-transition award in
// production while all unit tests stayed green. PREPARE reproduces that
// class of failure with zero fixtures and zero writes: PostgreSQL runs the
// full parse analysis at PREPARE time, and privileges are only checked at
// EXECUTE, so a read-only DSN suffices.
//
// Run (same harness contract as integration_test.go):
//
//	export CHORA_TEST_DSN=postgres://...           # any role; parse-only
//	go test -tags integration -run TestPrepareSmoke ./internal/adapter/repo/pg/
import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// prepareSmokeStatements enumerates every SQL const this package executes.
// ADD NEW CONSTS HERE — a const missing from this list is a statement that
// ships without parse-analysis coverage.
var prepareSmokeStatements = map[string]string{
	// campaign.go (ADR-227 WS-C1, CHO-2080)
	"upsertCampaignProgressSQL": upsertCampaignProgressSQL,
	"getCampaignProgressSQL":    getCampaignProgressSQL,
	"listCampaignProgressSQL":   listCampaignProgressSQL,
	// campaign_lineage_history.go (ADR-227 D14 WS-C6, CHO-2085)
	"priorLadderStateSQL": priorLadderStateSQL,
	// concept_merge_split.go (ADR-227 D14 WS-C6, CHO-2085)
	"tombstoneCampaignProgressSQL":     tombstoneCampaignProgressSQL,
	"insertConceptLineageSQL":          insertConceptLineageSQL,
	"repointPendingSuggestionFocalSQL": repointPendingSuggestionFocalSQL,
	"listConceptLineageSQL":            listConceptLineageSQL,
	// goal.go (ADR-204; campaign columns focus_concept_id + campaign_sealed_at
	// joined in WS-C1 — these consts were previously unregistered, a gap)
	"insertGoalSQL": insertGoalSQL,
	"updateGoalSQL": updateGoalSQL,
	"getGoalSQL":    getGoalSQL,
	"listGoalsSQL":  listGoalsSQL,
	// atom_attempt.go (CHO-2029 durable sessions)
	"upsertAtomSessionSQL": upsertAtomSessionSQL,
	"loadAtomSessionSQL":   loadAtomSessionSQL,
	// active_path_topics.go + topic_accuracy.go — the two daily-dose projections
	// (CHO-2167, closes CHO-1471). These consts predate this ticket but were
	// NEVER registered here, because until now they were never executed in
	// production: the composition root held the concrete *inmem.* types, so the
	// pg adapters could not be bound. They are load-bearing from this commit on
	// (dose CURIOSITY + WEAKNESS slots), so they get parse-analysis coverage.
	"upsertActivePathTopicsSQL":       upsertActivePathTopicsSQL,
	"markActivePathTopicsAdvancedSQL": markActivePathTopicsAdvancedSQL,
	"getActivePathTopicsSQL":          getActivePathTopicsSQL,
	"recordTopicAccuracyAttemptSQL":   recordTopicAccuracyAttemptSQL,
	"getTopicAccuracyByLearnerSQL":    getTopicAccuracyByLearnerSQL,
	"markSessionSeenSQL":              markSessionSeenSQL,
	// companion_loadout.go (ADR-218 P0)
	"listSkillCatalogSQL":  listSkillCatalogSQL,
	"activeSpeciesPathSQL": activeSpeciesPathSQL,
	// ADR-254 D4/D8 companion_turns (0111): the bus turn store.
	"insertCompanionTurnSQL":          insertCompanionTurnSQL,
	"selectCompanionTurnSQL":          selectCompanionTurnSQL,
	"selectCompanionTurnForUpdateSQL": selectCompanionTurnForUpdateSQL,
	"updateCompanionTurnTerminalSQL":  updateCompanionTurnTerminalSQL,
	"timeoutCompanionTurnSQL":         timeoutCompanionTurnSQL,
	"listGrantsSQL":                   listGrantsSQL,
	"listGrantedKeysSQL":              listGrantedKeysSQL,
	"mintGrantSQL":                    mintGrantSQL,
	"resolveSkillIDSQL":               resolveSkillIDSQL,
	"setGrantEquippedSQL":             setGrantEquippedSQL,

	// companion_instance.go
	"insertCompanionInstanceSQL":        insertCompanionInstanceSQL,
	"upsertCompanionInstanceSQL":        upsertCompanionInstanceSQL,
	"loadCompanionInstanceSQL":          loadCompanionInstanceSQL,
	"listCompanionInstancesByOwnerSQL":  listCompanionInstancesByOwnerSQL,
	"listCompanionRosterByOwnerSQL":     listCompanionRosterByOwnerSQL,
	"countCompanionInstancesByOwnerSQL": countCompanionInstancesByOwnerSQL,
	"softDeleteCompanionInstanceSQL":    softDeleteCompanionInstanceSQL,

	// growth.go (award tx + hatch + provisioning)
	"selectGrowthRowSQL":              selectGrowthRowSQL,
	"setResonantConceptSQL":           setResonantConceptSQL,
	"commitBornHatchedSQL":            commitBornHatchedSQL,
	"selectGrowthRowForUpdateSQL":     selectGrowthRowForUpdateSQL,
	"selectGrowthDailyCounterSQL":     selectGrowthDailyCounterSQL,
	"upsertGrowthDailyCounterSQL":     upsertGrowthDailyCounterSQL,
	"insertGrowthEventSQL":            insertGrowthEventSQL,
	"selectExistingGrowthEventSQL":    selectExistingGrowthEventSQL,
	"selectLastGrowthAwardAtSQL":      selectLastGrowthAwardAtSQL,
	"updateGrowthExpAndStageSQL":      updateGrowthExpAndStageSQL,
	"commitHatchSQL":                  commitHatchSQL,
	"commitRevealSQL":                 commitRevealSQL,
	"markAhaMomentSQL":                markAhaMomentSQL,
	"countCompanionsOfSpeciesSQL":     countCompanionsOfSpeciesSQL,
	"listGrowthEventsSQL":             listGrowthEventsSQL,
	"provisionEggInsertSQL":           provisionEggInsertSQL,
	"provisionEggLookupByPurchaseSQL": provisionEggLookupByPurchaseSQL,

	// concept_graph (CHO-2038 learning-edges intent + ADR-212 WS-1). The
	// learning-edges applier reuses insertConceptNodeSQL + insertConceptEdgeSQL,
	// so registering the concept_nodes/edges consts covers the whole write path.
	"insertConceptNodeSQL": insertConceptNodeSQL,
	"updateConceptNodeSQL": updateConceptNodeSQL,
	"getConceptNodeSQL":    getConceptNodeSQL,
	"listConceptNodesSQL":  listConceptNodesSQL,
	"insertConceptEdgeSQL": insertConceptEdgeSQL,

	// ceremony_edge_scout_runs.go (CHO-2040 R8-1 first-run ledger, migration 0067)
	"ceremonyEdgeScoutHasRunSQL":    ceremonyEdgeScoutHasRunSQL,
	"ceremonyEdgeScoutRecordRunSQL": ceremonyEdgeScoutRecordRunSQL,

	// campaign_question.go (ADR-227 WS-C3, CHO-2082)
	"upsertCampaignQuestionSetSQL":      upsertCampaignQuestionSetSQL,
	"getCampaignQuestionSetSQL":         getCampaignQuestionSetSQL,
	"getCampaignQuestionSetByAssistSQL": getCampaignQuestionSetByAssistSQL,
	"countCampaignQuestionRequestsSQL":  countCampaignQuestionRequestsSQL,

	// companion_memory_recall.go (CHO-2096 — retire-time memory purge)
	"softDeleteMemoryByCompanionSQL": softDeleteMemoryByCompanionSQL,
	// companion_memory_recall.go — the INSERT/RECALL pair was NEVER parse-analysed
	// (found while adding source_metadata, CHO-2179). Adding a column + a bind
	// param to an INSERT is precisely the 42P08-class failure this list exists to
	// catch: the unit suite stubs Exec, so a column that does not exist or a param
	// whose type cannot be deduced stays green until it 500s in production.
	"insertCompanionMemoryRecallSQL": insertCompanionMemoryRecallSQL,
	"recallCompanionMemoryRecallSQL": recallCompanionMemoryRecallSQL,
	// CHO-2185 — the learner-panel read. It had NEVER been parse-analysed, and it
	// now projects source_metadata and binds a text[] filter referenced twice, which
	// is precisely the 42P08 param-type-deduction shape this list exists to catch.
	"recentCompanionMemoriesSQL": recentCompanionMemoriesSQL,

	// campaign_reveal.go (ADR-227 WS-C4, CHO-2083 — reveal ledger claim-then-mark)
	"claimRevealSQL":         claimRevealSQL,
	"getRevealByIdentitySQL": getRevealByIdentitySQL,
	"markRevealPublishedSQL": markRevealPublishedSQL,

	// proofingtest.go (CHO-2040 Virgin Proofing Test composed runner)
	"insertProofingTestSQL":      insertProofingTestSQL,
	"updateProofingTestSQL":      updateProofingTestSQL,
	"getProofingTestSQL":         getProofingTestSQL,
	"getProofingTestByAssistSQL": getProofingTestByAssistSQL,
	"listProofingTestsSQL":       listProofingTestsSQL,

	// companion_goal_knowledge.go (CHO-2118 tier-2 goal-memory cache, migration
	// 0092). The upsert's ON CONFLICT arbiter is a PARTIAL unique index, so it is
	// specifically 42P10 (arbiter inference) that PREPARE catches here; the two
	// nullable UUID binds and the two enum casts are the 22P02/42P08 surface.
	"upsertGoalKnowledgeSQL":                upsertGoalKnowledgeSQL,
	"findGoalKnowledgeByGoalSQL":            findGoalKnowledgeByGoalSQL,
	"listGoalKnowledgeByGoalSQL":            listGoalKnowledgeByGoalSQL,
	"listGoalKnowledgeByCompanionSQL":       listGoalKnowledgeByCompanionSQL,
	"softDeleteGoalKnowledgeByCompanionSQL": softDeleteGoalKnowledgeByCompanionSQL,
	"softDeleteGoalKnowledgeByLearnerSQL":   softDeleteGoalKnowledgeByLearnerSQL,

	// learning_path.go (ADR-233 study-list provenance, migration 0093). The
	// upsert grew 4 columns + 4 params and the shared learningPathCols
	// projection grew 4 columns, so EVERY statement below is re-parsed here —
	// a column/param mismatch in any of them is a 42703/42P08 at runtime while
	// the stub-Exec unit suite stays green (the CHO-2012 lesson).
	"upsertLearningPathSQL":                 upsertLearningPathSQL,
	"loadLearningPathSQL":                   loadLearningPathSQL,
	"getLearningPathByCourseSQL":            getLearningPathByCourseSQL,
	"listLearningPathsByLearnerSQL":         listLearningPathsByLearnerSQL,
	"listLearningPathsByLearnerWithAtomSQL": listLearningPathsByLearnerWithAtomSQL,
	"listLearningPathsByCourseSQL":          listLearningPathsByCourseSQL,
	"getLearningPathBySourceCollectionSQL":  getLearningPathBySourceCollectionSQL,
	"getLearningPathByStudyListEventSQL":    getLearningPathByStudyListEventSQL,
}

// TestPrepareSmoke_AllStatementsParse PREPAREs every statement on one
// connection. Any 42P08 / 42703 / 42P01-class defect fails loud with the
// const name so the broken SQL is one grep away.
func TestPrepareSmoke_AllStatementsParse(t *testing.T) {
	dsn := os.Getenv("CHORA_TEST_DSN")
	if dsn == "" {
		t.Skip("set CHORA_TEST_DSN to run prepare-smoke integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	i := 0
	for name, sql := range prepareSmokeStatements {
		i++
		if _, err := conn.Prepare(ctx, fmt.Sprintf("smoke_%d", i), sql); err != nil {
			t.Errorf("PREPARE %s failed: %v", name, err)
		}
	}
}
