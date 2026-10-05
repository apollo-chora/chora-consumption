// ceremony_edge_scout_wiring.go — composition root for the CHO-2040
// (CR §8 R7-3) ceremony edge-scout PROPOSE runner
// (POST /v1/me/companions/{id}/ceremony/edge-scout).
//
// Attaches the composed runner's read ports to the Phyllis Server:
//
//   - Goals — the SAME pg Goal repo the /v1/me/goals CRUD uses (ownership
//     gate + ConceptSet/RootConceptID anchor; READ-ONLY here).
//   - FogSuggestions — the SAME pg Suggestion repo the WS-4 curation surface
//     uses (goal-adjacent pending fog concepts).
//   - GradedComments — a NEW thin gRPC client for chora-delivery's
//     Delivery.ListLearnerGradedSubmissions (the runner's ONLY cross-domain
//     read; typed RPC per AP-01, cross-DB forbidden). Target from env
//     CHORA_DELIVERY_GRPC_BASE_URL (mesh DNS, e.g. "chora-delivery:9090"),
//     mirroring CHORA_TENANCY_GRPC_BASE_URL in growth_wiring.go. The conn is
//     process-lifetime (same as the tenancy gRPC client — no shutdown hook).
//
// LearnerWeakness / ConceptNodes / Embedder / CompanionEngine / ManaQuoter are
// shared with the existing runners and wired elsewhere. Any port left nil ⇒
// the runner refuses per-request with 503 EDGE_SCOUT_NOT_WIRED (fail-soft at
// boot, fail-loud per request — the wireSkillInvokeRunner pattern).
//
// ⚠ Deploy-time (NOT applied from code): the new consumption route needs the
// gateway 3-list sync; the new delivery gRPC method needs a mesh-authz
// allowlist entry + caller restart
// ([[feedback_grpc_method_needs_mesh_authz_allowlist]]).
package main

import (
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
)

// wireCeremonyEdgeScout attaches the edge-scout runner's read ports.
func wireCeremonyEdgeScout(srv *httpadapter.Server, pool *pgxpool.Pool) {
	if pool == nil {
		log.Printf("consumption: ceremony edge-scout NOT wired (no pgx pool); POST /v1/me/companions/{id}/ceremony/edge-scout → 503 EDGE_SCOUT_NOT_WIRED")
		return
	}
	tx := pg.NewPgxTxRunner(pool)
	srv.Goals = pg.NewGoalRepo(tx)
	srv.FogSuggestions = pg.NewSuggestionRepo(tx)
	// R8-1 first-run ledger (CHO-2040 Unit B): first edge-scout run per
	// (tenant, goal, learner) is FREE; the ledger row is what flips the
	// pricing. pg-backed over ceremony_edge_scout_runs (migration 0067 —
	// apply BEFORE rolling this build; the runner 500s loudly on a missing
	// table rather than mispricing).
	srv.CeremonyRuns = pg.NewCeremonyEdgeScoutRunRepo(tx)

	target := strings.TrimSpace(os.Getenv("CHORA_DELIVERY_GRPC_BASE_URL"))
	if target == "" {
		log.Printf("consumption: CHORA_DELIVERY_GRPC_BASE_URL unset — graded-comments seam NOT wired; ceremony edge-scout → 503 EDGE_SCOUT_NOT_WIRED (fail-loud, never a silently narrowed crawl)")
		return
	}
	gc, err := clients.NewDeliveryGradedCommentsGRPCClient(clients.DeliveryGradedCommentsGRPCClientOptions{
		Target: target,
	})
	if err != nil {
		log.Printf("consumption: NewDeliveryGradedCommentsGRPCClient(%s) failed: %v — ceremony edge-scout → 503 EDGE_SCOUT_NOT_WIRED", target, err)
		return
	}
	srv.GradedComments = gc
	log.Printf("consumption: ceremony edge-scout wired (CHO-2040 R7-3): POST /v1/me/companions/{id}/ceremony/edge-scout (delivery seam=%s)", target)
}
