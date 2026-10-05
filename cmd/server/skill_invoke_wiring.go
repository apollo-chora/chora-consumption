// skill_invoke_wiring.go — composition root for the CHO-2013 P1.B (R4-4)
// single-step Skill invoke runner (POST /v1/me/companions/{id}/skills/{key}/invoke).
//
// Attaches the runner's three read ports to the Phyllis Server:
//
//   - LearnerProfiles — the SAME pg LearnerProfileRepo the gRPC
//     ReadLearnerProfile RPC uses (progress_mirror context).
//   - CompanionMemoryView — the SAME recency reader the ADR-215 memory view
//     uses over companion_memory_recall (recap_scribe session log).
//   - ConceptNodes — the SAME pg ConceptNodeRepo the concept-graph API uses
//     (explain_anew concept targets; atom targets ride srv.AtomIndex).
//
// Nil pool ⇒ the ports stay nil and the runner refuses per-skill with 503
// SKILL_INVOKE_NOT_WIRED (fail-soft at boot, fail-loud per request — the
// wireConceptGraphReRoot pattern). No env reads here (secrets-and-env).
package main

import (
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/kgmapread"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
)

// wireSkillInvokeRunner attaches the invoke runner's read ports.
func wireSkillInvokeRunner(srv *httpadapter.Server, pool *pgxpool.Pool) {
	if pool == nil {
		log.Printf("consumption: skill invoke runner read ports NOT wired (no pgx pool); invokes needing them → 503 SKILL_INVOKE_NOT_WIRED")
		return
	}
	tx := pg.NewPgxTxRunner(pool)
	srv.LearnerProfiles = pg.NewLearnerProfileRepo(tx)
	srv.CompanionMemoryView = pg.NewCompanionMemoryViewRepo(tx)
	srv.ConceptNodes = pg.NewConceptNodeRepo(tx)
	// CHO-2014 map_sight — ring-scoped per-user concept map (kg.read_map) over
	// the same pg concept-node + edge repos. weakness_sight's LearnerWeakness
	// port is wired separately in wireGrowthEdgeReadRepo (W6).
	srv.KGMap = kgmapread.NewReader(pg.NewConceptNodeRepo(tx), pg.NewEdgeRepo(tx))
	// kg_explore GROUNDED-RECONCILE suggestion scout: the map source (learner's
	// ConceptNodes) + the WS-4 suggestion-inbox WRITE port (the SAME pg SuggestionRepo
	// the curation API + edge-scout read through). AtomIndex is threaded elsewhere.
	srv.KgExploreMap = pg.NewConceptNodeRepo(tx)
	srv.Suggestions = pg.NewSuggestionRepo(tx)
	// CHO-2117 goal scope: the goal lister (which Goal designates the invoking
	// companion) + the edge reader (ADR-214 subtree walk) over the SAME pg repos
	// the goals CRUD + concept-graph API use. Either nil ⇒ kg_explore 503
	// (fail-closed — an unscoped run could leak off-goal topics).
	srv.KgExploreGoals = pg.NewGoalRepo(tx)
	srv.KgExploreEdges = pg.NewEdgeRepo(tx)
	log.Printf("consumption: skill invoke runner wired (CHO-2013 P1.B R4-4 + CHO-2014 sight): POST /v1/me/companions/{id}/skills/{key}/invoke")
}
