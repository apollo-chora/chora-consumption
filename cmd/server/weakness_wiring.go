// weakness_wiring.go — composition root for the Epic-1b Growth-Edge explicit
// path's inbound side (W3-decode).
//
// Wires the WeaknessAnalyzedSubscriber + its HTTP push handler so
// /api/internal/pubsub/weakness-analyzed lands registered on Routes(), projecting
// the ai-kernel weakness-analyser crew's chora.consumption.weakness.analyzed.v1
// events into the LearnerWeakness aggregate (source=explicit).
//
// Dependencies (all already bootstrapped by the time main calls this):
//   - pgx pool  → pg.NewLearnerWeaknessRepo (the pgvector dedup-fold repo).
//   - srv.Embedder (the F4 Vertex text-embedding client) → wrapped as the
//     learner_weakness.Embedder so concept labels embed for dedup + topic
//     resolution. When the embedder is absent (CHORA_MODEL_GATEWAY_GRPC_URL
//     unset) the handler is NOT wired — the explicit path cannot upsert without
//     embeddings, so we fail-soft at boot (log + skip) exactly like F4 memory.
//
// Per secrets-and-env the OIDC verifier is wired from the same env vars the
// growth + egg push handlers use (CHORA_PUBSUB_PUSH_AUDIENCE / _TRUSTED_SA).
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/eventbus"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/drillcache"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/adapter/storage"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// learnerWeaknessWriteRepo builds the Growth-Edge WRITE repo: the pg repo,
// wrapped with the W4 drill-atom cache decorator when chora-creation's
// ContentRetrieval gRPC is configured (SVC_CREATION_GRPC_URL; one shared
// client across both upsert paths). No env ⇒ plain repo (W6 topic fallback).
var (
	contentRetrievalOnce   sync.Once
	contentRetrievalClient *clients.ContentRetrievalClient
)

func learnerWeaknessWriteRepo(pool *pgxpool.Pool) lw.Repository {
	repo := pg.NewLearnerWeaknessRepo(pg.NewPgxTxRunner(pool))
	contentRetrievalOnce.Do(func() {
		target := strings.TrimSpace(os.Getenv("SVC_CREATION_GRPC_URL"))
		if target == "" {
			log.Printf("consumption: drill-atom cache NOT wired (SVC_CREATION_GRPC_URL unset) — W6 falls back to topic matching")
			return
		}
		c, err := clients.NewContentRetrievalClient(target)
		if err != nil {
			log.Printf("consumption: drill-atom cache NOT wired (dial %s: %v)", target, err)
			return
		}
		contentRetrievalClient = c
		log.Printf("consumption: drill-atom cache wired (ContentRetrieval.SearchEmbeddings @ %s)", target)
	})
	if contentRetrievalClient == nil {
		return repo
	}
	// CHO-1895 gradability invariant: only cache gradable+answerable drill atoms
	// (atom_index IsMCQ AND topic-tagged). The atom_index projection is the same
	// local chora_consumption read the dose universe uses (RLS rides the upsert
	// ctx). A non-gradable atom cached as a drill is a dead end — the player
	// renders no options and the projector skips it, so the drill→grow loop never
	// closes.
	gradability := drillcache.AtomIndexGradability(pg.NewAtomIndexRepo(pg.NewPgxTxRunner(pool)))
	return drillcache.Wrap(repo, contentRetrievalClient, gradability)
}

// wireWeaknessAnalyzed attaches the Growth-Edge analyzed.v1 inbound adapter to
// the eventbus. MUST run AFTER wireCompanionMemory (which sets srv.Embedder).
func wireWeaknessAnalyzed(ctx context.Context, srv *httpadapter.Server, pool *pgxpool.Pool, bus eventbus.Bus) {
	if pool == nil {
		log.Printf("consumption: weakness-analyzed NOT wired (no pgx pool)")
		return
	}
	if srv.Embedder == nil {
		log.Printf("consumption: weakness-analyzed NOT wired (no embedder, set CHORA_MODEL_GATEWAY_GRPC_URL)")
		return
	}
	if bus == nil {
		log.Printf("consumption: weakness-analyzed NOT wired (no event bus, NATS_URL unset)")
		return
	}

	repo := learnerWeaknessWriteRepo(pool)
	embedder := clients.NewLearnerWeaknessEmbedder(srv.Embedder)
	// W8b-3: on a successful analysis, flip the originating upload job to
	// COMPLETED (with the upserted edge ids) so the FE poll resolves.
	uploadRepo := pg.NewWeaknessUploadRepo(pg.NewPgxTxRunner(pool))
	sub := subscribers.NewWeaknessAnalyzedSubscriber(repo, embedder).WithJobCompleter(uploadRepo)
	// WS-5 (ADR-205 D8 / CHO-1957): when the blob-envelope feature is enabled, a
	// completed diagnosis crypto-shreds the raw upload (tombstone the wrapped DEK
	// + delete the GCS ciphertext + a best-effort TTL sweep). DARK by default —
	// returns sub unchanged. See cmd/server/weakness_crypto_wiring.go.
	sub = maybeWithBlobShredHook(sub, pool)
	// ADR-205 D5 (CHO-1966): when WEAKNESS_COMPANION_RAG_ENABLED, a completed
	// analysis whose learner opted into familiar_coaching writes the diagnosis
	// into their Companion's pgvector memory (ADR-173). DARK by default; soft-fail
	// (best-effort coaching). See cmd/server/weakness_companion_rag_wiring.go.
	// CHO-2012 (ADR-218 D7): the resolver attributes through Goal attachment —
	// a pool-backed goal repo gives it the read (this wiring runs before
	// wireGoalRepo populates ext.Goals; the repo is a stateless view over the
	// same pool).
	sub = maybeWithCompanionRAGHook(sub, srv, pg.NewGoalRepo(pg.NewPgxTxRunner(pool)))

	// ADR-238 M-D2 (goal-scoped Diagnose): resolve each analysed Growth Edge to
	// its goal sub-tree's nearest on-map ConceptNode, stamping TargetConceptID +
	// aligning ConceptKey so both the read-side id-join AND slug-join light the
	// right node. Composes the goal repo + concept node/edge readers + the SAME
	// embedder; the upload→goal lookup is the same uploadRepo (GoalForUpload).
	// Best-effort — resolution errors soft-fail (the edges still persist). Only
	// wired here because srv.Embedder is guaranteed non-nil at this point.
	//
	// The resolver's embedder is CACHED (ADR-238 §5): resolution embeds every
	// in-subtree concept TITLE on every upload, so an uncached resolver fans out N
	// synchronous Vertex round-trips inside a Pub/Sub handler, N growing with the
	// learner's map. Caching is applied HERE, not inside the resolver, so the
	// resolver stays a pure composition of its ports. Only the resolver's embedder
	// is wrapped - per-edge analyser labels are effectively unique per upload, so
	// caching them would evict hot concept titles for single-use entries.
	goalResolver := subscribers.NewGoalConceptResolver(
		pg.NewGoalRepo(pg.NewPgxTxRunner(pool)),
		pg.NewConceptNodeRepo(pg.NewPgxTxRunner(pool)),
		pg.NewEdgeRepo(pg.NewPgxTxRunner(pool)),
		subscribers.NewConceptEmbeddingCache(embedder),
	)
	minCosine := diagnoseConceptMatchMinCosine()
	tieBand := diagnoseConceptMatchTieBand()
	sub = sub.WithGoalConceptResolver(goalResolver, uploadRepo).
		WithConceptMatchMinCosine(minCosine).
		WithConceptMatchTieBand(tieBand)
	// ADR-238 D4: an unmatched weakness becomes a PENDING concept suggestion
	// anchored at the goal root, so it is offered to the learner instead of
	// surfacing nowhere. Wired unconditionally — D4 is a decision, not a flag,
	// and an unwired writer is exactly how the half-built state persisted.
	sub = sub.WithConceptSuggestions(pg.NewSuggestionRepo(pg.NewPgxTxRunner(pool)))
	// Derive the D2 clause from tieBand rather than hand-writing "bias ON": a
	// literal here is a claim about runtime behaviour, and a hand-written one goes
	// stale the moment the knob is turned off.
	d2State := fmt.Sprintf("bias ON tie-band=%.3f", tieBand)
	if tieBand <= 0 {
		d2State = "bias OFF (tie-band=0)"
	}
	log.Printf("consumption: goal-scoped Diagnose resolver wired (ADR-238 M-D2; concept-match min cosine=%.2f; D2 entry-concept %s; title-embedding cache ON; D4 unmatched→concept-suggestion ON)", minCosine, d2State)

	go func() {
		log.Printf("consumption: weakness-analyzed subscriber binding chora.consumption.weakness.analyzed.v1 (Growth-Edge explicit; repo=pg, embedder wraps %T)", srv.Embedder)
		if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.weakness-analyzed", "chora.consumption.weakness.analyzed.v1"), subscribers.WeaknessAnalyzedHandler(sub)); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("ERROR consumption: weakness-analyzed subscriber exited: %v", err)
		}
	}()
}

// diagnoseConceptMatchMinCosine reads the ADR-238 M-D2 goal-scoped concept-match
// cosine floor from CHORA_DIAGNOSE_CONCEPT_MATCH_MIN_COSINE. It is a tuning knob
// with a SAFE default (0.55), applied when the var is unset, unparseable, or out
// of the [0,1] range — a permitted inline default per feedback_no_inline_config
// (no secret, no URL, no adapter target; just a resolver sensitivity dial).
func diagnoseConceptMatchMinCosine() float64 {
	const def = 0.55
	raw := strings.TrimSpace(os.Getenv("CHORA_DIAGNOSE_CONCEPT_MATCH_MIN_COSINE"))
	if raw == "" {
		return def
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || f < 0 || f > 1 {
		log.Printf("consumption: CHORA_DIAGNOSE_CONCEPT_MATCH_MIN_COSINE=%q invalid (want a float in 0..1); keeping default %.2f", raw, def)
		return def
	}
	return f
}

// diagnoseConceptMatchTieBand reads ADR-238 D2's soft-bias width from
// CHORA_DIAGNOSE_CONCEPT_MATCH_TIE_BAND: how close to the cosine argmax a
// candidate inside the ENTRY concept's sub-tree must be to win the tie.
//
// The default (0.03) is deliberately small. Matching an analyser-generated label
// against a bare node title is a THIN signal (ADR-238 §5), so gaps of a few
// hundredths are within its noise - which is exactly the region where "the
// learner opened this concept" is better evidence than the cosine. A wider band
// would let the entry point overrule genuinely better matches and drift toward
// the hard filter D2 rejects.
//
// 0 is the kill-switch: it disables the bias entirely, restoring pre-D2 pure
// argmax without a redeploy. No value can change WHETHER an edge matches - the
// bias is applied after the floor - only which concept it lands on. Same
// permitted-inline-default rules as the floor knob (no secret, no URL, no
// adapter target; a resolver sensitivity dial).
func diagnoseConceptMatchTieBand() float64 {
	const def = 0.03
	raw := strings.TrimSpace(os.Getenv("CHORA_DIAGNOSE_CONCEPT_MATCH_TIE_BAND"))
	if raw == "" {
		return def
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || f < 0 || f > 1 {
		log.Printf("consumption: CHORA_DIAGNOSE_CONCEPT_MATCH_TIE_BAND=%q invalid (want a float in 0..1); keeping default %.3f", raw, def)
		return def
	}
	return f
}

// wireWeaknessReviewPendingPushHandler attaches the bounded HITL review-panel
// bridge (ADR-205 D4 / CHO-1973): the review_pending.v1 inbound adapter that
// parks the upload job in AWAITING_REVIEW with the panel stored for the A+ poll.
//
// DARK by default — wired ONLY when WEAKNESS_REVIEW_PENDING_ENABLED=true (the
// graph crew cutover; no live single-shot job is ever AWAITING_REVIEW). The OIDC
// verifier is wired from the same env vars the analyzed handler uses
// (secrets-and-env). nil pool ⇒ skipped (the parker needs the pg upload repo).
func wireWeaknessReviewPending(ctx context.Context, srv *httpadapter.Server, pool *pgxpool.Pool, bus eventbus.Bus) {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("WEAKNESS_REVIEW_PENDING_ENABLED")), "true") {
		log.Printf("consumption: weakness-review-pending DISABLED (WEAKNESS_REVIEW_PENDING_ENABLED unset/false) — bounded HITL bridge dark [ADR-205 D4; couples to WEAKNESS_CREW_MODE=graph]")
		return
	}
	if pool == nil {
		log.Printf("consumption: weakness-review-pending NOT wired (no pgx pool)")
		return
	}
	if bus == nil {
		log.Printf("consumption: weakness-review-pending NOT wired (no event bus, NATS_URL unset)")
		return
	}

	// The parker is the pg upload-job repo (its MarkAwaitingReview satisfies the
	// subscriber's ReviewParker structurally — NOT the Repository port).
	parker := pg.NewWeaknessUploadRepo(pg.NewPgxTxRunner(pool))
	sub := subscribers.NewWeaknessReviewPendingSubscriber(parker)

	go func() {
		log.Printf("consumption: weakness-review-pending subscriber binding chora.consumption.weakness.review_pending.v1 (bounded HITL review bridge; repo=pg) [ADR-205 D4]")
		if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.weakness-review-pending", subscribers.TopicWeaknessReviewPending), subscribers.WeaknessReviewPendingHandler(sub)); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("ERROR consumption: weakness-review-pending subscriber exited: %v", err)
		}
	}()
}

// wireGrowthEdgeReadRepo attaches the pg LearnerWeakness repo to the ExtServer
// so the A+ Growth-Edges read API (GET/DELETE /v1/me/growth-edges) is live,
// AND to the Phyllis Server so the daily-dose weakness slot reads Growth Edges
// (W6) — same repo instance, read-only on both surfaces.
// Nil pool ⇒ the routes return 503 (fail-soft at boot, fail-loud per request)
// and the dose keeps its legacy topic_accuracy slot.
func wireGrowthEdgeReadRepo(srv *httpadapter.Server, ext *httpadapter.ExtServer, pool *pgxpool.Pool) {
	if pool == nil {
		log.Printf("consumption: growth-edges read API NOT wired (no pgx pool); /v1/me/growth-edges → 503")
		return
	}
	repo := pg.NewLearnerWeaknessRepo(pg.NewPgxTxRunner(pool))
	ext.LearnerWeakness = repo
	// ADR-247 Capability B (CHO-2328): the SAME concrete repo satisfies the narrow
	// WeaknessContextLoader, so the learner-door fog request can carry the focal
	// node's diagnosed weakness. Set here (not on the 13-method Repository port) so
	// the many Repository mocks stay untouched (interface segregation).
	ext.WeaknessLoader = repo
	if srv != nil {
		srv.LearnerWeakness = repo // W6: dose weakness slot reads Growth Edges
		// CHO-2043: the ceremony learning-edges write path mints a backing
		// learner_weakness per remediate edge (map↔list consistency); it needs
		// the same embedder the explicit/derived paths use (set by
		// wireCompanionMemory, which runs earlier in main). Nil embedder ⇒ the
		// handler skips the backing + warns (concepts still mint).
		if srv.Embedder != nil {
			ext.WeaknessEmbedder = clients.NewLearnerWeaknessEmbedder(srv.Embedder)
		} else {
			log.Printf("consumption: ceremony weakness backing NOT wired (no embedder, set CHORA_MODEL_GATEWAY_GRPC_URL); remediate learning-edges will read shaky with no growth-edges list row")
		}
	}
	log.Printf("consumption: growth-edges read API wired (pg LearnerWeakness repo): GET/DELETE /v1/me/growth-edges + dose weakness slot")
}

// wireGrowthEdgeUploads attaches the W8b upload PRODUCER: the pg upload-job repo
// + the GCS blob uploader. The repo is wired whenever a pool exists; the GCS
// uploader needs WEAKNESS_UPLOADS_BUCKET (feedback_no_inline_config) — when unset
// the POST/GET .../uploads routes return 503 (fail-loud, no stub).
func wireGrowthEdgeUploads(ext *httpadapter.ExtServer, pool *pgxpool.Pool) {
	if pool == nil {
		log.Printf("consumption: growth-edge uploads NOT wired (no pgx pool); .../uploads → 503")
		return
	}
	uploadRepo := pg.NewWeaknessUploadRepo(pg.NewPgxTxRunner(pool))
	ext.WeaknessUploads = uploadRepo
	// B6 item 1: the same repo also satisfies the read-model port for the
	// learner's parked diagnoses. Wired from the same pool deliberately, so the
	// list can never be live while the producer is dark or the reverse.
	ext.WeaknessPendingReviews = uploadRepo
	log.Printf("consumption: pending-review read model wired (GET /v1/me/growth-edges/uploads?status=awaiting_review)")

	bucket := strings.TrimSpace(os.Getenv("WEAKNESS_UPLOADS_BUCKET"))
	store, err := storage.NewWeaknessBlobStore(storage.WeaknessBlobStoreConfig{Bucket: bucket})
	if err != nil {
		log.Printf("consumption: growth-edge upload blob store NOT wired (%v); POST .../uploads → 503", err)
		return
	}
	ext.WeaknessBlobs = store
	log.Printf("consumption: growth-edge uploads wired (pg job repo + GCS bucket=%s): POST/GET /v1/me/growth-edges/uploads", bucket)
}
