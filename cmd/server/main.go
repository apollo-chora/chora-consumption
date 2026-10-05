// Package main is the entry point for chora-consumption — the Content
// Consumption domain service.
//
// Cloud-neutral: runs anywhere a container runtime does (docker compose,
// Kubernetes, a bare host). Talks to Postgres, NATS, and an S3-compatible
// object store — no cloud-provider SDK.
//
// Aligned with Architecture Review locked 2026-05-07 — Tier 1 D1, Tier 2 D7,
// Tier 4 D16, Tier 5 D20.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	stdhttp "net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	consumptionpg "github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"

	// pgx stdlib driver — registered for sql.Open("pgx", dsn) used by the
	// per-domain outbox PostgresStore in bootstrap_outbox.go.
	_ "github.com/jackc/pgx/v5/stdlib"

	"google.golang.org/grpc"
	healthgrpc "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/encoding/protojson"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"

	"github.com/apollo-chora/chora-common/durabilityguard"
	"github.com/apollo-chora/chora-common/eventbus"
	commontracing "github.com/apollo-chora/chora-common/tracing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	grpcadapter "github.com/apollo-chora/chora-consumption/internal/adapter/grpc"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/kgmapread"
	consumptionoutbox "github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
	"github.com/apollo-chora/chora-consumption/internal/domain/doseclock"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
	"github.com/apollo-chora/chora-consumption/internal/observability"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// CHORA_DOSE_DAY_SECONDS shrinks the dose-day bucket (qgen budget, D7
	// rung pacing, dose election seed, stale-request supersede) for
	// accelerated E2E testing — owner-directed knob, unset = product
	// per-UTC-day. Invalid values refuse boot (fail-loud, never a silent
	// fallback to a clock the operator didn't ask for).
	if raw := strings.TrimSpace(os.Getenv("CHORA_DOSE_DAY_SECONDS")); raw != "" {
		secs, err := strconv.Atoi(raw)
		if err != nil {
			log.Fatalf("CHORA_DOSE_DAY_SECONDS not an integer: %q", raw)
		}
		if err := doseclock.Init(secs); err != nil {
			log.Fatalf("CHORA_DOSE_DAY_SECONDS rejected: %v", err)
		}
		log.Printf("⚠ doseclock: dose day = %ds (ACCELERATED TEST CLOCK — unset for product per-day pacing)", secs)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// OTLP wiring per Tier 3 D13 — direct to Cloud Trace in prod.
	//
	// Per C(a).S1 path (b) — tracker #151 — OTLP init runs in its own
	// goroutine with its own (env-tunable, default 15s) deadline + fail-
	// soft semantics. Timeout / init-error degrade to a no-op shutdown,
	// so the rest of bootstrap (pgx pool, event bus) gets the FULL
	// CHORA_BOOTSTRAP_TIMEOUT_SECONDS budget. Previously a slow Cloud
	// Trace TLS handshake could swallow the shared budget and crash-
	// loop the pod under PgBouncer 4-container cold-start.
	otlpHandle := observability.SetupAsync(ctx)

	// ----------------------------------------------------------------------
	// pgxpool + event-bus wiring (Wave-B service-wiring per
	// feedback_resilience_priority).
	// ----------------------------------------------------------------------
	pool, poolShutdown := bootstrapDBPool(ctx)
	if poolShutdown != nil {
		defer poolShutdown()
	}
	bus, busShutdown := bootstrapBus(ctx)
	if busShutdown != nil {
		defer busShutdown()
	}

	srv := httpadapter.NewServer()
	ext := httpadapter.NewExtServer(nil)

	// ADR-224 (CHO-2045) — dose composer v2 flag (subject/KG balance + seeded
	// sampling). Dark by default; env-gated at the composition root.
	wireDoseComposerV2(srv)

	// ADR-254 D4/D8: every Companion turn + the daily-dose recommendation
	// ride the bus (companion_turn / dose_recommendation lanes): turn store
	// + same-tx outbox request + pull consumers on the *-completed
	// subscriptions. Replaces the direct engine endpoints (deleted, ADR-254
	// D12/D13) and the boot-time engine warmup (a warmup turn would now be a
	// real dispatch).
	wireCompanionBus(ctx, srv, pool, bus)
	// ADR-254 D4: the diagnosis crew's generated artifacts (incl. the new
	// companion_voice kind) arrive on the PULL subscription the coordinator
	// provisioned; until now the projector had no transport.
	wireWeaknessOutputsConsumer(ctx, pool, bus)
	// ADR-252 / ADR-254 D11: the ADVISORY containment projection behind the
	// chat 403 COMPANION_SUSPENDED, the Q5 reflection skip and the profile's
	// companion_status. One projection, both servers, so the two reads cannot
	// disagree. chora-model-gateway stays the control.
	wireCompanionSuspension(ctx, srv, ext, pool, bus)

	// ADR-154 — Companion conversational chat surface. Attach the
	// per-domain ChatSessionRepository when the pgx pool is available.
	wireChatSessions(srv, pool)

	// ADR-173 / F4 — per-Companion pgvector RAG memory. Wires the pg-backed
	// CompanionMemory store + (when CHORA_MODEL_GATEWAY_GRPC_URL is set) the
	// Vertex embedding client. Memory is active only when BOTH land; otherwise
	// the chat handler skips recall+record (chat unchanged).
	wireCompanionMemory(ctx, srv, pool)

	// Epic-1b — Growth-Edge explicit path inbound. MUST follow wireCompanionMemory
	// (reuses srv.Embedder). Registers /api/internal/pubsub/weakness-analyzed,
	// projecting the ai-kernel analyser's weakness.analyzed.v1 into LearnerWeakness.
	wireWeaknessAnalyzed(ctx, srv, pool, bus)

	// ADR-212 WS-4 — concept-suggestion inbound: registers
	// /api/internal/pubsub/concept-suggested, persisting the fog orchestrator's
	// proposed concepts/edges as pending Suggestions.
	wireConceptSuggestionEmitted(ctx, srv, pool, bus)

	// ADR-205 D4 (CHO-1973) — bounded HITL review-panel bridge. DARK behind
	// WEAKNESS_REVIEW_PENDING_ENABLED (graph crew cutover). When enabled, registers
	// /api/internal/pubsub/weakness-review-pending, parking the upload job in
	// AWAITING_REVIEW with the panel from weakness.review_pending.v1.
	wireWeaknessReviewPending(ctx, srv, pool, bus)

	// M14.1 paydown #M14.1-famrepo-scan (2026-05-16) — swap the in-memory
	// CompanionInstanceRepo for the pgx-backed one once the pool is up. The
	// in-memory adapter remains the fallback when pool is nil (tests / dev
	// without DB). Closes the demo-blocking empty `items[]` defect from
	// `GET /v1/me/companions` surfaced in Phyllis Round-15.
	wireCompanionInstanceRepo(srv, pool)

	// CHO-2015 (ADR-219 D2) — Persona sheet: the load/apply/persist service
	// over CompanionInstances (wired above) + the save-time Cloud Model Armor
	// guardrail for the free-text guidance note. MUST run after
	// wireCompanionInstanceRepo (it reads srv.CompanionInstances). The returned
	// closer tears down the Model Armor SDK client on graceful shutdown.
	if personaClose := wirePersona(ctx, srv); personaClose != nil {
		defer func() {
			if cerr := personaClose(); cerr != nil {
				log.Printf("consumption: persona guardrail screener close error: %v", cerr)
			}
		}()
	}

	// ADR-215 WS-1 (CHO-1997) — learner-facing Companion memory READ handler,
	// serving GET /v1/me/companions/{id}/memory (dispatched from
	// handleCompanionInstanceByID). Always non-nil (503-on-request when the pool
	// is absent); builds its own RLS-bound repos from the pool.
	srv.CompanionMemoryRead = wireCompanionMemoryRead(pool)

	// Register 6.4 R6: the learner's own delete over one remembered thing,
	// serving DELETE /v1/me/companions/{id}/memory/{memoryId} (dispatched from the
	// same handleCompanionInstanceByID subtree as the read above).
	srv.CompanionMemoryDelete = wireCompanionMemoryDelete(pool)

	// Owner ruling 2026-08-07: effective per-learner egg odds, serving
	// GET /v1/me/companions/{id}/egg-odds. Publishes the declared SKU table
	// alongside the pool this learner's next reveal actually rolls, so declared
	// == observed for the IMDA D2 fairness audit. Shares the growth service's
	// breed-distribution provider (see wireCompanionEggOdds) so both read one
	// cached declared table. Always non-nil (503-on-request without a pool).
	srv.CompanionEggOdds = wireCompanionEggOdds(pool)

	// CHO-2013 P1.B (R4-4) — the single-step Skill invoke runner's read ports
	// (learner profile / memory recency view / concept nodes). Engine + mana +
	// embedder + memory-store deps are the chat handler's, wired elsewhere.
	wireSkillInvokeRunner(srv, pool)

	// CHO-2040 (CR §8 R7-3) — the ceremony edge-scout PROPOSE runner's read
	// ports (goal / fog suggestions / delivery graded-comments seam). The
	// runner additionally shares srv.ConceptNodes (wireSkillInvokeRunner,
	// above), srv.LearnerWeakness (wireGrowthEdgeReadRepo, below) and
	// srv.Embedder (wireCompanionMemory, above) — all attached before serving,
	// so wiring order here is presentation-only.
	wireCeremonyEdgeScout(srv, pool)

	// KG-hexagon fold-in (ADR-143 + ADR-169) — pg-durable canvas repos.
	// MUST precede serving: the canvas clusters/explorations/hexagon-fog/
	// trail/junctions survive pod rolls only via the pg adapters.
	wireUserKGPg(ext, pool)

	// ----------------------------------------------------------------------
	// D6.2 producer-side outbox wiring (M12.3 W1b + W1.5b bootstrap).
	//
	// Per `feedback_d6_resilience_first_class` + `agentic-resilience-d6`
	// skill Pillar 2. The per-domain Publisher writes events to
	// outbox_events; the Dispatcher drains to the NATS event bus on a background
	// goroutine.
	//
	// Fallback ladder (same as chora-creation W1.5a):
	//   1. pool nil                   → default in-mem publisher (NewServer)
	//   2. pool set + outboxDB nil    → outbox.InMemoryStore   (dev w/ pool)
	//   3. pool set + outboxDB set    → outbox.PostgresStore   (prod)
	// Dispatcher goroutine only starts when the bus + outboxDB are both
	// wired. Final synchronous DrainOnce on shutdown flushes in-flight rows.
	// ----------------------------------------------------------------------
	var outboxStore consumptionoutbox.Store
	var outboxDispatcher *consumptionoutbox.Dispatcher
	var dispatcherDone chan struct{}
	// outerGrowthSvc is hoisted out of the pool-conditional block below so
	// the gRPC bootstrap can bind the CompanionGrowthServer against the
	// production-wired growth.Service (when wired; nil otherwise — in
	// which case CompanionGrowth is NOT registered, per
	// feedback_no_stubs_real_wiring: don't bind a server that would
	// nil-panic on first non-validation call).
	var outerGrowthSvc *growth.Service

	outboxDB, outboxDBShutdown := bootstrapOutboxDB(ctx)
	if outboxDBShutdown != nil {
		defer outboxDBShutdown()
	}

	if pool != nil {
		if outboxDB != nil {
			outboxStore = consumptionoutbox.NewPostgresStore(
				sqlDBAdapter{db: outboxDB},
				consumptionoutbox.PostgresStoreOptions{WorkerID: outboxWorkerID()},
			)
			log.Printf("consumption: outbox PostgresStore wired (worker_id=%s)", outboxWorkerID())
		} else {
			outboxStore = consumptionoutbox.NewInMemoryStore()
			log.Printf("consumption: outbox InMemoryStore wired (CHORA_OUTBOX_DSN unset; NOT durable across restart)")
		}
		srv.Publisher = consumptionoutbox.NewPublisher(consumptionoutbox.PublisherConfig{
			Store:         outboxStore,
			AggregateType: "atom_session",
		})

		// Epic-1b W8b FIX (2026-06-10) — the A+ Growth-Edge upload PRODUCER
		// (handleMeGrowthEdgeUploads) lives on the ExtServer, whose Publisher
		// defaults to NewInMemoryPublisher() in NewExtServer. Left unwired, every
		// real upload's chora.consumption.weakness_doc.uploaded.v1 was written to
		// that in-memory buffer and silently DROPPED — the POST still 202'd and
		// the object-store blob + QUEUED job were created, but the ai-kernel weakness-
		// analyser never fired (zero topic publishes) so the job stuck QUEUED
		// forever. The prior synthetic-event "e2e proof" published straight to
		// Pub/Sub and masked this. Wire the durable outbox publisher (the same
		// outboxStore the dispatcher drains) so uploaded.v1 reaches the bus as
		// JSON (no Schema Registry schema on that topic → JSON fallback is fine).
		ext.Publisher = consumptionoutbox.NewPublisher(consumptionoutbox.PublisherConfig{
			Store:         outboxStore,
			AggregateType: "learner_weakness",
		})

		// ADR-204 Slice F.2 — KGEvents was built in NewExtServer over the
		// in-memory publisher; the swap above only rebound ext.Publisher, so
		// every KG mutation event (the 11 chora.consumption.kg_* topics) was
		// still buffered in process and silently dropped. Rebind KGEvents (and
		// the junction detector) to the SAME durable outbox publisher so KG
		// mutations land in the outbox the dispatcher already drains — exactly
		// like weakness.grown. MUST follow wireUserKGPg (above) so the detector
		// rebuild binds the pg-backed repos.
		ext.RewireKGEvents(events.NewKGPublisher(ext.Publisher))

		// ADR-149 / Iter G.5 PROD-B — wire the Companion Growth axis. Requires
		// a real pgx pool + outbox publisher. Without both, /v1/me/companions/{id}/
		// growth* endpoints fall back to a 503 GROWTH_NOT_WIRED response.
		//
		// A dedicated publisher with AggregateType="companion" is used so audit
		// + replay queries on aggregate_id surface meaningful values. Per
		// publisher.Publish, the payload's "companion_id" key overrides the
		// AggregateType default — but having the right default is still
		// useful for events whose payload predates the override path.
		growthPublisher := consumptionoutbox.NewPublisher(consumptionoutbox.PublisherConfig{
			Store:         outboxStore,
			AggregateType: "companion",
		})
		growthSvc := wireGrowthService(srv, pool, growthPublisher)

		// ADR-149 / Iter G.8 — the growth push handlers are wired LATER (after
		// wireAtomIndexRepo) so the W3-derived weakness projector can ride the
		// same companion-growth-source-events delivery with a pg-backed
		// atom_index — see the wireGrowthPushHandlers call below.
		if growthSvc != nil {
			// bug-3 mesh-sidestep (2026-06-02): resolve the Companion
			// session-bootstrap config LOCALLY + hand it to the chat handler
			// for injection into the ADK CreateSession state. The sidecar-less
			// Companion agent reads its config from session state, so it never
			// calls back over consumption's STRICT-mTLS gRPC (which it can't
			// satisfy without a sidecar). Same resolver the gRPC
			// ResolveCompanionConfig RPC uses — single source of truth.
			srv.CompanionConfigJSONResolver = func(ctx context.Context, tenantID, companionID, callerGCID string) (string, error) {
				// CHO-2012 (ADR-218 D3): srv.Loadouts supplies the equipped
				// allowlist — same reader the gRPC surface uses.
				cfg, rerr := grpcadapter.ResolveCompanionInstanceConfig(ctx, srv.CompanionInstances, growthSvc, srv.Loadouts, srv.SkillCatalog, tenantID, companionID, callerGCID)
				if rerr != nil {
					return "", rerr
				}
				b, merr := protojson.Marshal(cfg)
				if merr != nil {
					return "", fmt.Errorf("marshal companion config: %w", merr)
				}
				return string(b), nil
			}
		}
		// Hand the growthSvc out to the gRPC bootstrap below.
		outerGrowthSvc = growthSvc

		// CHO-2040 (R8-6/R8-7) — the Virgin Proofing Test composed runner.
		// MUST follow the outbox bootstrap above (the runner publishes its
		// chora.creation.ai_assist.started.v2 batch request through the SAME
		// durable outboxStore the dispatcher drains) and NewServer's
		// ManaQuoter construction (the reserve→refund lane type-asserts the
		// concrete chora-identity ManaClient).
		wireProofingTest(ctx, srv, pool, outboxStore, bus)

		// CHO-2016 Grimoire Rituals v1 (ADR-219). MUST follow growth_wiring
		// (srv.Loadouts/SkillCatalog/Growth), NewServer's ManaQuoter, the
		// engine bootstrap (srv.CompanionEngine), and the outbox bootstrap. The
		// designer wires on the pool; the RUN lane additionally needs the
		// concrete ManaClient + engine (else run → 503 RITUALS_RUN_NOT_WIRED).
		wireRituals(srv, pool, outboxStore)

		if bus != nil && outboxDB != nil {
			// The outbox dispatcher's Bus interface is structurally identical
			// to eventbus.Publisher — pass the bus directly, no adapter.
			var outboxBus consumptionoutbox.Bus = bus
			outboxDispatcher = consumptionoutbox.NewDispatcher(consumptionoutbox.DispatcherConfig{
				Store:    outboxStore,
				Bus:      outboxBus,
				WorkerID: outboxWorkerID(),
			})
			dispatcherDone = make(chan struct{})
			go func() {
				defer close(dispatcherDone)
				if err := outboxDispatcher.Run(ctx, 100); err != nil &&
					!errors.Is(err, context.Canceled) &&
					!errors.Is(err, context.DeadlineExceeded) {
					log.Printf("consumption: outbox dispatcher exited: %v", err)
				}
			}()
			log.Printf("consumption: outbox dispatcher goroutine started (batch=100)")
		} else if bus == nil {
			log.Printf("consumption: NATS_URL unset — outbox dispatcher NOT started; rows accumulate")
		}
	}

	// Federated closure-saga subscriber (CHO-1719 / Tier 3 D11): consumes
	// chora.consumption.pii.pseudonymise.requested.v1, applies the
	// per-domain PII_Closure_Map.yaml duty, and acks on
	// chora.consumption.account.pseudonymised.v1.
	//
	// Repo seam (CHO-2198, W0-F1 durability + W0-F5 error-honesty): pg on a
	// healthy pool (durable ack/dedup — migration 0097,
	// closure_pseudonymisation_state), in-memory ONLY when the pool is
	// absent, mirroring the chora-payments / chora-notifications else-branch
	// shape. Before this fix the repo was UNGATED — gated on the bus
	// only, never on pool health — so the ack/dedup state was lost on every
	// pod restart even with a healthy chora_consumption pool. Real
	// per-table pg tokenisation (actually redacting atomic_session /
	// learning_path / ... columns) remains separate, deeper M12+ debt —
	// this fix is durability of the ack/dedup SIGNAL only, not the
	// redaction itself. The pull subscription is bound on the eventbus;
	// override the name via env.
	// closureRepo is hoisted to function scope so the CHO-2198 W0-F1 durability
	// guard (below, after the stores are wired) can classify it alongside the
	// other repos. It stays nil when the bus is absent (closure subscriber
	// unwired) — the guard reports nil as UNKNOWN, never a false violation.
	var closureRepo events.ClosureRepository
	if bus != nil {
		piiPath := os.Getenv("CHORA_PII_CLOSURE_MAP_PATH")
		if piiPath == "" {
			piiPath = "config/PII_Closure_Map.yaml"
		}
		closureAckPub := eventbus.NewClosureAckPublisher(
			bus,
			os.Getenv("CHORA_SOURCE_PROJECT"),
			"chora-consumption",
		)
		if pool != nil {
			closureRepo = consumptionpg.NewClosureRepository(consumptionpg.NewPgxTxRunner(pool))
			log.Printf("consumption: pg ClosureRepository wired (table=closure_pseudonymisation_state)")
		} else {
			closureRepo = events.NewInMemoryClosureRepo()
			log.Printf("consumption: CHORA_DB_DSN unset — closure repo uses in-memory store (NOT durable across restart)")
		}
		if closureSub, err := events.BootstrapClosureSubscriber(piiPath, closureRepo, closureAckPub, nil); err != nil {
			log.Printf("consumption: closure subscriber DISABLED (PII map load: %v)", err)
		} else {
			closureSubName := os.Getenv("CHORA_CLOSURE_SUBSCRIPTION")
			if closureSubName == "" {
				closureSubName = "chora-consumption.closure-pseudonymise"
			}
			go func() {
				log.Printf("consumption: closure subscriber binding %s -> %s", closureSubName, events.TopicPseudonymiseRequested)
				if err := bus.Subscribe(ctx, consumerConfig(closureSubName, events.TopicPseudonymiseRequested), events.ClosurePullHandler(closureSub)); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("consumption: closure subscriber exited: %v", err)
				}
			}()
		}
	}

	// CHO-1612 D2 — swap the in-memory atom_index projection for the pg-backed
	// (durable) one when a pool is available. MUST precede ShareDosingProjections
	// (which rebuilds the TopicAccuracy + ActivePathTopics subs from
	// ext.AtomIndex) and wireSubscriberPushHandlers (binds AtomCreated/
	// EnrollmentCreated). In-memory retained as the no-pool fallback.
	atomIndexRepo := wireAtomIndexRepo(ext, pool)
	// CHO-1662 session-state-injection RAG: thread the SAME atom_index repo the
	// gRPC ConsumptionServer uses into the Phyllis-MVP daily-dose/ai handler, so
	// it can pre-fetch candidate atoms in-process and inject them into the
	// recommender session state (the sidecar-less crew can't dial consumption's
	// STRICT-mTLS :9090 back). Nil-safe: pg-backed when the pool is up, in-memory
	// otherwise; either way the handler degrades gracefully.
	srv.AtomIndex = atomIndexRepo
	// R3 (OPEN-1 durability): swap the in-memory LearningPath repo for the
	// pg-backed one so a bootstrapped path survives pod restart + is replica-
	// consistent (the volatile in-memory repo 404'd /v1/me/learning-paths after
	// every restart). MUST follow wireAtomIndexRepo (rebinds path subscribers
	// to the now-pg atom_index) and precede wireSubscriberPushHandlers.
	// L4 Fix-1 (CHO-1702): ALSO thread the SAME pg repo onto the Phyllis
	// Server so the daily-dose atom universe comes from the learner's real
	// enrolled-course paths (nil ⇒ synthetic catalogue fallback).
	srv.LearningPaths = wireLearningPathRepo(ext, pool)
	// CHO-1949 — durable per-(tenant, gcid, topic) Ebbinghaus TopicRetention.
	// Swap the in-memory Retention repo (the last in-memory learner-state) for
	// the pg-backed RLS-bound one so the score survives pod restarts and
	// /v1/me/topic-retention is cross-tenant-safe. MUST follow wireAtomIndexRepo
	// + wireLearningPathRepo (the rebuilt SessionCompletion binds the already-pg
	// AtomIndex + LearningPath) and precede wireDerivedWeakness (so its retention
	// reader captures the pg-backed repo).
	wireTopicRetentionRepo(ext, pool)
	// L4 no-debt directive (2026-06-10): durable Maya retention loop — swap
	// the in-memory SM-2 / streak / XP stores for the pg-backed adapters
	// (migration 0048) so the Ebbinghaus state survives pod restarts.
	wireRetentionStores(srv, ext, pool)
	// Epic-1b W8a — Growth-Edges read API on the pg LearnerWeakness repo
	// (+ W6: the same repo feeds the daily-dose weakness slot).
	wireGrowthEdgeReadRepo(srv, ext, pool)
	// ADR-204 §2 — learner-owned Goal aggregate CRUD on the pg Goal repo.
	wireGoalRepo(ext, pool)
	// Roster-side map↔companion consistency (CHO-2033): the retire path clears a
	// retired companion's Goal.AttachedCompanionID via the SAME pg goal repo.
	if ext.Goals != nil {
		srv.CompanionMaps = httpadapter.NewGoalCompanionDetacher(ext.Goals)
	}
	// ADR-224 #3 (CHO-2045) — per-KG dose-preference store on the Server (backs
	// /v1/me/dose-preferences + the weakness-pool exclude filter). srv.Goals is
	// already wired by wireCeremonyEdgeScout above, so the filter can resolve
	// excluded maps → concept_sets.
	wireDoseKGPrefs(srv, pool)
	// ADR-212 WS-2 (CHO-1995) — learner-sovereign concept-graph read + re-root
	// (pg ConceptNode + Edge repos + ReRootApplier).
	wireConceptGraphReRoot(ext, pool)
	// D3 slice 1b (CHO-2047): name what the companion is tuned to. MUST follow
	// wireConceptGraphReRoot (ext.Concepts) and wireAtomIndexRepo
	// (srv.AtomIndex): both are read here, and either absent leaves the reader
	// nil rather than half-built. Without this call the port shipped in slice 1
	// stays nil and the character sheet tells every learner their resonant
	// concept is gone.
	wireResonanceTitles(srv, ext)
	// WS-A3 (CHO-2005, ADR-214 D1) — Companion acquisition for a map (pg Instance
	// repo; the map↔Companion link is the Goal), POST /v1/me/companions/acquire.
	wireCompanionAcquisition(ext, pool, outboxStore)
	// CHO-2013 P1 (R3-7 + dev_hatched honest fix): acquire births companions
	// hatched via the growth service. Unwired growth ⇒ acquire refuses 503.
	if outerGrowthSvc != nil {
		ext.GrowthInit = outerGrowthSvc
	}
	// ADR-212 WS-4 (CHO-2001) — Companion suggestion curation (pg Suggestion repo
	// + accepter), GET /v1/me/concept-graph/suggestions + accept/dismiss.
	wireConceptSuggestions(ext, pool)
	// Epic-1b W8b — Growth-Edge upload producer (pg job repo + S3 uploader).
	wireGrowthEdgeUploads(ext, pool)
	// WS-4 (CHO-1956 / ADR-205 D6) — upfront mana gate at the upload door.
	// DARK unless WEAKNESS_UPFRONT_MANA_ENABLED=true (couples to the crew
	// cutover). MUST follow NewServer (srv.ManaQuoter) + wireGrowthEdgeUploads.
	wireGrowthEdgeUpfrontMana(srv, ext, pool)
	// WS-5 (CHO-1957 / ADR-205 D8) — source_material consent gate + per-blob
	// envelope encrypt-on-upload. DARK unless WEAKNESS_BLOB_ENVELOPE_ENABLED /
	// WEAKNESS_SOURCE_MATERIAL_ENABLED. MUST follow wireGrowthEdgeUploads
	// (ext.WeaknessBlobs); the shred-on-analyse side is wired onto the subscriber
	// by maybeWithBlobShredHook (see wireWeaknessAnalyzedPushHandler).
	wireWeaknessBlobCryptoShred(ext, pool)
	// F-I3 (CHO-2090, ADR-228 D4 Wave 1) — the Wave-1 companion-EXP consumer:
	// weakness_grown + module_completed get their own inboxes here; the
	// submission_graded leg rides the derived dispatch below. MUST follow
	// wireGrowthService (the award port) + precede wireDerivedWeakness (which
	// tees the subscriber onto submission-graded-derived).
	waveOneXP := wireWaveOneXP(ctx, srv, pool, outerGrowthSvc, bus)
	// Epic-1b W3-derived — performance → Growth-Edge projector. MUST follow
	// wireAtomIndexRepo (binds the pg-backed atom_index) + wireCompanionMemory
	// (srv.Embedder). Mounts /api/internal/pubsub/live-quiz-score-awarded.
	derivedProjector := wireDerivedWeakness(ctx, srv, ext, pool, waveOneXP, bus)
	// ADR-149 / Iter G.8 — Pub/Sub push HTTP handlers fronting the
	// CompanionGrowthSubscriber + ProvisionEggSubscriber, with the W3-derived
	// projector fanned into the atom_session.completed dispatch.
	if outerGrowthSvc != nil {
		// CHO-2012 (ADR-218 D7): ext.Goals (wired above by wireGoalRepo)
		// gives the resolver its Goal-attachment read — EXP accrues to the
		// goal-bonded companion (most recent summon) with the legacy
		// oldest-roster read as terminal fallback.
		wireGrowthPushHandlers(ctx, srv, outerGrowthSvc, derivedProjector, ext.Goals, bus)
	}
	// ADR-227 WS-C5 (CHO-2084) — campaign conquest XP: ONE push endpoint
	// consuming the three XP-bearing campaign.* topics into goal-routed
	// AwardExp calls (ResolveForGoal, addendum #6). MUST follow
	// wireGrowthService (the award port); the resolver + the seal-spacing
	// ledger read compose straight off the pool.
	wireCampaignXP(ctx, srv, pool, outerGrowthSvc, bus)
	// §3 Goal graduation (CHO-1962) — consume the consumption-emitted
	// weakness.grown.v1 and reconcile the learner's Goals (graduate at 100% /
	// progress below). MUST follow wireGoalRepo (ext.Goals) +
	// wireGrowthEdgeReadRepo (ext.LearnerWeakness); reuses the durable outboxStore
	// via a dedicated "goal" aggregate publisher.
	wireGoalGraduation(ctx, srv, ext, pool, outboxStore, bus)
	// CHO-2118 sub-phase B — the goal-knowledge cache invalidation lane: the
	// six-topic push inbox + the in-handler goal re-root (which has no event).
	// MUST follow wireGoalRepo (ext.Goals) — the weakness events are
	// concept-scoped and resolve to goals through the Goal repo.
	wireGoalKnowledgeInvalidation(ctx, srv, ext, pool, bus)
	// CHO-2118 sub-phase B — the goal-knowledge COMPLETION inbox (the fog
	// orchestrator's synthesized.v1), drift-guarded before caching.
	//
	// ⚠ The assembler is nil until the read path (CHO-2116) lands its goal-scoped
	// view service: the content-hash drift guard is computed from it, and it must
	// be the SAME assembly the request was built from. Boot logs the lane as NOT
	// wired until then — pass the read-path assembler here to light it up. No
	// completions exist before the read path anyway (it is what emits requests).
	// CHO-2118 sub-phase B — the goal-knowledge READ path: GET
	// /v1/me/goals/{id}/knowledge (tier-1 block + cached reflection) + the
	// lazy-regen request emitter. MUST follow wireGoalRepo (it reads ext.Goals).
	// It returns the goal-scoped view assembler, which is ALSO the completion
	// lane's drift guard — one assembly, so the hash the request advertises and
	// the hash the completion re-checks are computed by the same code.
	goalKnowledgeViews := wireGoalKnowledgeRead(ext, pool)
	wireGoalKnowledgeCompletion(ctx, srv, ext, pool, goalKnowledgeViews, bus)
	// ADR-227 WS-C1 (CHO-2080) — the goal campaign doors (focus + seal) —
	// + WS-C2 (CHO-2081) — the campaign dose axis (grader + shared
	// CampaignDose for the compose + answers-fold sides). MUST follow
	// wireGoalRepo (ext.Goals), wireTopicRetentionRepo (ext.Retention pg),
	// wireConceptGraphReRoot (ext.Concepts), wireDoseKGPrefs
	// (srv.DoseKGPrefs) and the outbox bootstrap; rides a dedicated
	// "campaign" aggregate publisher.
	wireCampaign(ctx, srv, ext, pool, outboxStore, bus)
	// CHO-2167 (closes CHO-1471) — swap the in-memory dose projections for the
	// pg-backed (durable, RLS-bound) adapters. This IS the "M12+ production
	// wiring" the S6.4 note below promised and never received: active_path_topics
	// had 0 rows in chora_consumption because nothing could bind its pg adapter,
	// and topic_accuracy was a split-brain (DerivedWeaknessProjector wrote pg;
	// the dose composer read a heap map that started empty every pod restart).
	//
	// MUST precede ShareDosingProjections — that call copies these repos onto the
	// ExtServer and rebuilds both subscribers from them. Swap after it and the pg
	// repos are overwritten by the in-memory ones NewServer built, leaving the
	// "pg-backed" boot log a lie.
	wireDosingProjections(srv, pool)
	// S6.4 — re-bind the EXT-scope topic_accuracy + active_path_topics
	// subscribers to write into the Phyllis MVP server's repos so the
	// daily-dose composer reads from the same projections the subscribers
	// populate. Subscribers and the dose composer now share ONE adapter
	// instance — pg in production, in-memory only in dev.
	ext.ShareDosingProjections(srv.TopicAccuracy, srv.ActivePathTopics)
	// CHO-1612 D2 — swap the in-memory course-content projection for the
	// pg-backed (durable) one when a pool is available. MUST precede
	// wireSubscriberPushHandlers so the course-content push handler binds the
	// pg-backed subscriber.
	wireCourseContentRepo(ext, pool)
	// ADR-200 WS1.c3 — LearnerProfile projection (pg-only). Before
	// wireSubscriberPushHandlers so its per-topic push endpoints mount.
	wireLearnerProfileRepo(ext, pool)
	// W6 Slice 1 (Four-Mode plan "Outcome spine") — StudentTranscript
	// projection (pg-only). MUST precede wireSubscriberPushHandlers so
	// LearnerProfilePushDeps.Transcript picks up ext.TranscriptSub and the
	// cert-issued/submission-graded fan-out mounts.
	wireTranscriptRepo(ext, pool)
	// CHO-2059 — course_directory projection (course_id → title, pg-only).
	// Before wireSubscriberPushHandlers so the course-metadata push endpoint
	// mounts; also injects the reader onto srv for progress_mirror.
	wireCourseDirectoryRepo(ext, srv, pool)
	// Foundational cross-domain Pub/Sub push handlers (2026-05-26).
	// Mounts /internal/pubsub/enrollment-created + /internal/pubsub/atom-
	// created so chora-delivery's enrollment.created.v1 + chora-creation's
	// atom.created.v1 events flow into the LearningPath bootstrap +
	// atom_index projection subscribers (previously dormant in prod).
	// Closes the course journey: clicking "Open course" → /v1/me/learning-
	// paths?course_id=X returns the hydrated atom list instead of 404.
	// ADR-244 D5 — build the standing atom-refresh trigger FIRST so
	// wireSubscriberPushHandlers can fan it out from the atom-published inbox
	// and front the new atom-updated inbox. Needs the durable outbox (above)
	// + the pg graph/campaign/atom_index repos.
	wireAtomRefresh(ext, pool, outboxStore, ext.Publisher)
	wireSubscriberBusBindings(ctx, srv, ext, bus)
	// ADR-233 / spec-001 US5 (WS-4) — mount the collection→study-list inbound.
	// MUST follow the ext.Publisher swap (durable outbox) + wireLearningPathRepo
	// (pg paths): binding either in-memory dependency would silently produce an
	// INERT study list (built, but never reaching the dose's curiosity slot) or
	// one lost on pod restart. See study_list_wiring.go for the ordering note.
	wireCollectionConvertedToStudyList(ctx, srv, ext, bus)
	// CHO-2324 — mount the concept-delete → edge-cleanup cascade. Needs only the
	// Edge repo (wired early); it self-consumes chora.consumption.concept.deleted.v1
	// to soft-delete a removed concept's incident edges.
	wireConceptDeleted(ctx, srv, ext, bus)
	// ADR-143 / ADR-204 Slice F.1 — mount the KG fog-cache invalidation push
	// handler (atom.published + atom.updated + user_retention.shifted). The
	// subscribers + handler existed + were tested but were never wired, so
	// stale fog was never invalidated. MUST follow wireUserKGPg (pg hexagon
	// repo) + the F.2 RewireKGEvents above (durable publisher for the
	// subscribers' own invalidated.v1 emits).
	wireKGInvalidation(ctx, srv, ext, bus)

	// CHO-2198 W0-F1 — report-only runtime durability guard over the composition
	// root. Classifies each wired repository by SHAPE (holds a live *pgxpool.Pool
	// ⇒ DURABLE; a data map ⇒ IN_MEMORY; nil ⇒ UNKNOWN) and logs a structured,
	// greppable report at boot. Report-only unless CHORA_DURABILITY_GUARD=enforce
	// AND the binding is allow-listed — the nil allow-list matches the
	// chora-delivery / chora-payments / chora-identity wirings. Bindings are the
	// resolved persistence stores on the Server + ExtServer (pg-backed on a
	// healthy pool, in-memory fallback otherwise); clients / publishers / engines
	// / push handlers are intentionally excluded.
	durabilityguard.Guard("chora-consumption", []durabilityguard.Binding{
		// AtomAttempt + LearningPath + atom_index (the playback spine).
		{Port: "sessions", Adapter: srv.Sessions},
		{Port: "learning_paths", Adapter: srv.LearningPaths},
		{Port: "atom_index", Adapter: atomIndexRepo},
		// Maya retention loop — Ebbinghaus SM-2 / streak / lifetime XP (mig 0048).
		{Port: "sm2", Adapter: srv.SM2},
		{Port: "streaks", Adapter: srv.Streaks},
		{Port: "xp", Adapter: srv.XP},
		// Daily-dose projections + per-KG dose preferences + topic retention.
		{Port: "topic_accuracy", Adapter: srv.TopicAccuracy},
		{Port: "active_path_topics", Adapter: srv.ActivePathTopics},
		{Port: "topic_retention", Adapter: ext.Retention},
		{Port: "dose_kg_prefs", Adapter: srv.DoseKGPrefs},
		// Companion (1:N) aggregate + pgvector memory + chat sessions + loadouts.
		{Port: "companion_instances", Adapter: srv.CompanionInstances},
		{Port: "companion_memory", Adapter: srv.CompanionMemory},
		{Port: "companion_chat_sessions", Adapter: srv.CompanionChatSessions},
		{Port: "loadouts", Adapter: srv.Loadouts},
		{Port: "skill_catalog", Adapter: srv.SkillCatalog},
		// ADR-252 advisory containment projection (mig 0112). IN_MEMORY here
		// means every operator pause is forgotten on restart until re-emitted.
		{Port: "companion_suspension", Adapter: srv.CompanionSuspension},
		// Rituals / ceremony edge-scout / proofing-test aggregate stores.
		{Port: "rituals", Adapter: srv.Rituals},
		{Port: "ritual_runs", Adapter: srv.RitualRuns},
		{Port: "ceremony_runs", Adapter: srv.CeremonyRuns},
		{Port: "proofing_tests", Adapter: srv.ProofingTests},
		// Per-user Knowledge Graph canvas (ADR-143) + concept graph (ADR-212).
		{Port: "kg_clusters", Adapter: ext.KGClusters},
		{Port: "kg_explorations", Adapter: ext.KGExplorations},
		{Port: "kg_hexagons", Adapter: ext.KGHexagons},
		{Port: "kg_edges", Adapter: ext.KGEdges},
		{Port: "kg_junctions", Adapter: ext.KGJunctions},
		{Port: "concept_nodes", Adapter: ext.Concepts},
		{Port: "concept_edges", Adapter: ext.ConceptEdges},
		{Port: "concept_suggestions", Adapter: ext.Suggestions},
		// Learner-owned Goals + Growth-Edge (weakness) read + upload job store.
		{Port: "goals", Adapter: ext.Goals},
		{Port: "learner_weakness", Adapter: ext.LearnerWeakness},
		{Port: "weakness_uploads", Adapter: ext.WeaknessUploads},
		// Campaign (ADR-227) progress + question bank.
		{Port: "campaign_progress", Adapter: ext.CampaignProgress},
		{Port: "campaign_question_bank", Adapter: ext.CampaignQuestionBank},
		// Cross-domain read projections (course content / directory / transcript).
		{Port: "course_content", Adapter: ext.CourseContent},
		{Port: "course_directory", Adapter: srv.CourseDirectory},
		{Port: "transcript", Adapter: ext.Transcript},
		// Federated closure-saga ack/dedup (hoisted above; nil ⇒ UNKNOWN).
		{Port: "closure", Adapter: closureRepo},
	}, nil)

	// Composite handler: try /v1/me/* + EXT-scope /learning-paths/* +
	// /sessions/* + /knowledge-graph/* on the EXT server first; fall back
	// to the legacy /api/* + /healthz + /companion/* surfaces on the
	// Phyllis-MVP server. Health probes + the companions family (CHO-2137)
	// are legacy-owned — served directly, no wasted EXT pass (and no
	// spurious ext-side otel 404 line per hit).
	// The two EXT-owned exacts nested inside the companions family stay
	// primary-served via the carve-out list (ext_server.go route audit).
	composite := httpadapter.ComposeHandlers(ext.HTTPHandler(), srv.Routes(),
		[]string{"/v1/me/companions/acquire", "/v1/me/companions/bindings"},
		"/healthz", "/health", "/readyz",
		"/v1/me/companions")
	// Observability chain, outermost first (FINDING-02, 2026-08-07):
	//
	//  1. commontracing.Middleware starts the OTel SERVER span for every
	//     request, continuing an inbound W3C traceparent when the caller is
	//     OTel-aware. chora-consumption had no server span at all before
	//     this, so nothing downstream could correlate.
	//  2. AccessLogMiddleware emits ONE Cloud Logging structured JSON entry
	//     per request on stdout, taking the trace id from the span the layer
	//     above just started. It MUST sit inside the tracing middleware.
	//
	// Order matters both ways: outside the tracing middleware the log loses
	// its trace field; outside the composite it would miss the served status.
	tracedHandler := commontracing.Middleware()(
		observability.AccessLogMiddleware()(composite),
	)

	httpServer := &stdhttp.Server{
		Addr:              ":" + port,
		Handler:           tracedHandler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// WriteTimeout must exceed the async AI-enrichment engine budget on
		// GET /companion/daily-dose/ai (dailyDoseAITimeout=30s — the Recommender
		// + Companion gateway LLM round-trips run to completion there). At 15s the
		// connection write deadline cut the response before the engines returned.
		// 45s covers the gate + headroom; other endpoints are unaffected (SSE
		// companion chat manages its own per-request deadline).
		WriteTimeout: 45 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("service=%s version=%s listening on :%s",
			observability.ServiceName, observability.ServiceVersion, port)
		if err := httpServer.ListenAndServe(); err != nil && err != stdhttp.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	// ----------------------------------------------------------------------
	// gRPC server (Wave-1 CN-FULL — 2026-05-16,
	// docs/m13/grpc-mass-remediation-2026-05-16.md).
	//
	// Closes the systemic ADR-140 violation where chora-gateway BFF dialled
	// chora-consumption over plain HTTP :8080 instead of gRPC :9090. Mirrors
	// the canonical chora-identity ManaService block.
	//
	// Per feedback_no_stubs_real_wiring: NO conditional registration on
	// CHORA_GRPC_PORT env; the server registers unconditionally. The three
	// Wave-1 surfaces that don't yet have backing logic (Consumption /
	// FogOrchestrator / KnowledgeGraphService) are registered with
	// UnimplementedXxxServer-embedded shells so callers receive
	// codes.Unimplemented (the canonical fail-loud signal that the RPC is
	// reachable but not yet wired — NOT a silent stub) until each RPC's
	// domain wiring lands in the Wave-2 GW-SWITCH cutover.
	//
	// CompanionGrowth (ADR-149 Iter G.5) IS fully wired — but only when
	// outerGrowthSvc is non-nil (i.e. when the pgx pool + outbox publisher
	// are wired). In dev (in-memory only) the CompanionGrowthServer is NOT
	// registered to avoid binding a server that would nil-panic on first
	// non-validation call. Health surface stays SERVING for the other 3.
	// ----------------------------------------------------------------------
	grpcPort := strings.TrimSpace(os.Getenv("CHORA_GRPC_PORT"))
	if grpcPort == "" {
		grpcPort = "9090"
	}
	grpcLis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		log.Fatalf("consumption: gRPC net.Listen :%s: %v", grpcPort, err)
	}
	// One structured line per served call, naming the METHOD. Until this,
	// nothing on either side of a consumption gRPC call could say which method
	// served it: no interceptor here, no success-path log in the handlers, and
	// Envoy access logging is OFF on the sidecar. That is the observation both
	// ADR-254 W4 seams needed and neither had. Never logs bodies; see
	// grpc_call_log.go.
	grpcSrv := grpc.NewServer(grpc.UnaryInterceptor(grpcCallLogInterceptor(nil)))

	consumptionv1.RegisterConsumptionServer(grpcSrv, grpcadapter.NewConsumptionServer(atomIndexRepo))
	consumptionv1.RegisterKnowledgeGraphServiceServer(grpcSrv, grpcadapter.NewKnowledgeGraphServer())
	if outerGrowthSvc != nil {
		// WithCompanionInstanceReader wires ResolveCompanionConfig (the AI-Kernel
		// Companion agent's session-bootstrap config source — replaces its POC
		// stub registry). srv.CompanionInstances is the pg-backed repo when the
		// pool is up (wireCompanionInstanceRepo above), in-memory otherwise.
		growthOpts := []grpcadapter.CompanionGrowthOption{
			grpcadapter.WithCompanionInstanceReader(srv.CompanionInstances),
			grpcadapter.WithCompanionLoadoutReader(srv.Loadouts),
		}
		// CHO-2013 P1.B: allowed_tools expansion + the profile.read /
		// memory.note tool RPCs. Each is nil-tolerant (Unimplemented /
		// innate-only when its backing store is down).
		if srv.SkillCatalog != nil {
			growthOpts = append(growthOpts, grpcadapter.WithSkillCatalogueReader(srv.SkillCatalog))
		}
		if pool != nil {
			growthOpts = append(growthOpts, grpcadapter.WithLearnerProfileReader(consumptionpg.NewLearnerProfileRepo(consumptionpg.NewPgxTxRunner(pool))))
			// CHO-2059 — course_id → title resolver for ReadLearnerProfile so a
			// Companion names a real course. Fresh repo over the same pool,
			// mirrors the LearnerProfile reader above.
			growthOpts = append(growthOpts, grpcadapter.WithCourseDirectoryReader(consumptionpg.NewCourseDirectoryRepo(consumptionpg.NewPgxTxRunner(pool))))
			// CHO-2014 — weakness.read tool (weakness_sight): top-N Growth Edges
			// by strength desc. Same pg LearnerWeakness repo the growth-edges read
			// API + dose weakness slot use (List is RLS-scoped by the request ctx).
			growthOpts = append(growthOpts, grpcadapter.WithWeaknessReader(consumptionpg.NewLearnerWeaknessRepo(consumptionpg.NewPgxTxRunner(pool))))
			// CHO-2014 — kg.read_map tool (map_sight): ring-scoped per-user
			// concept map centred on the Companion's resonant concept. Composes the
			// pg concept-node + edge repos (RLS-scoped by the request ctx) with the
			// stage-gated ring BFS.
			growthOpts = append(growthOpts, grpcadapter.WithKGMapReader(kgmapread.NewReader(
				consumptionpg.NewConceptNodeRepo(consumptionpg.NewPgxTxRunner(pool)),
				consumptionpg.NewEdgeRepo(consumptionpg.NewPgxTxRunner(pool)),
			)))
		}
		if srv.CompanionMemory != nil && srv.Embedder != nil {
			growthOpts = append(growthOpts, grpcadapter.WithCompanionMemoryNote(srv.CompanionMemory, srv.Embedder, srv.CompanionEmbeddingModelID))
		}
		growthSrv := grpcadapter.NewCompanionGrowthServer(
			outerGrowthSvc,
			growthOpts...,
		)
		// ADR-254 D9 W4 OVERLAP: the SAME implementation is also served under the
		// pre-rename service name so the live chat binary (built against
		// FamiliarGrowth/ReadWeakness) keeps its weakness.read tool until WP-A sets
		// the rebuilt image; the wire is field-number compatible. The mesh
		// allowlist (authz-allow-consumption-grpc.yaml) carries both path sets.
		// Drop the alias (grpc_legacy_alias.go) + the legacy paths after "chat live".
		registerCompanionGrowthWithLegacyAlias(grpcSrv, growthSrv)
		log.Printf("consumption: gRPC CompanionGrowth bound (+ legacy alias %s for the W4 overlap; instance+loadout+catalogue readers, profile.read=%v, weakness.read=%v, kg.read_map=%v, memory.note=%v — CHO-2013 P1.B / CHO-2014)",
			legacyFamiliarGrowthServiceName, pool != nil, pool != nil, pool != nil, srv.CompanionMemory != nil && srv.Embedder != nil)
	} else {
		log.Printf("consumption: gRPC CompanionGrowth NOT bound (growth.Service nil — pool+outbox required)")
	}

	// gRPC health — required for Cloud Service Mesh probe routing.
	healthSrv := healthgrpc.NewServer()
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("chora.services.consumption.v1.Consumption", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("chora.services.consumption.v1.KnowledgeGraphService", healthpb.HealthCheckResponse_SERVING)
	if outerGrowthSvc != nil {
		healthSrv.SetServingStatus("chora.services.consumption.v1.CompanionGrowth", healthpb.HealthCheckResponse_SERVING)
		healthSrv.SetServingStatus(legacyFamiliarGrowthServiceName, healthpb.HealthCheckResponse_SERVING)
	}
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)

	go func() {
		log.Printf("consumption: gRPC server listening on :%s", grpcPort)
		if err := grpcSrv.Serve(grpcLis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			log.Fatalf("consumption: gRPC Serve: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutdown initiated")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown error: %v", err)
	}

	// gRPC graceful drain (bounded, mirrors chora-identity pattern).
	grpcShutdownDone := make(chan struct{})
	go func() {
		grpcSrv.GracefulStop()
		close(grpcShutdownDone)
	}()
	select {
	case <-grpcShutdownDone:
		log.Printf("consumption: gRPC server drained")
	case <-time.After(10 * time.Second):
		log.Printf("consumption: gRPC graceful-stop deadline exceeded — forcing stop")
		grpcSrv.Stop()
	}

	// Final outbox drain — flush in-flight pending rows before exit.
	if outboxDispatcher != nil {
		finalDrain, fcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer fcancel()
		if n, derr := outboxDispatcher.DrainOnce(finalDrain, 200); derr != nil {
			log.Printf("consumption: final outbox drain error: %v (drained %d)", derr, n)
		} else {
			log.Printf("consumption: final outbox drain published %d rows", n)
		}
	}
	if dispatcherDone != nil {
		select {
		case <-dispatcherDone:
		case <-time.After(5 * time.Second):
			log.Printf("consumption: outbox dispatcher shutdown timed out (5s)")
		}
	}

	// OTLP shutdown — async-handle path. WaitContext blocks until init
	// settles (no-op by shutdown time because pgx-pool + Pub/Sub init
	// above already gave OTLP best-effort wall-clock to land).
	otlpShutdownCtx, otlpCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer otlpCancel()
	res := otlpHandle.WaitContext(otlpShutdownCtx)
	if err := res.Shutdown(otlpShutdownCtx); err != nil {
		log.Printf("otlp shutdown error: %v", err)
	}
	log.Println("shutdown complete")
}
