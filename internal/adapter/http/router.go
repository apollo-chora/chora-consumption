// Package http exposes the consumption service over HTTP REST.
//
// Endpoint set (per Phase D — domain track briefing):
//
//	GET    /healthz, /health              liveness (/health alias for cross-service convention)
//	GET    /readyz                        readiness
//	POST   /api/learning-paths            create LearningPath
//	GET    /api/learning-paths/{id}       fetch LearningPath
//	GET    /api/learning-paths            list LearningPaths (tenant filter)
//	POST   /api/sessions                  start AtomAttempt
//	PATCH  /api/sessions/{id}/complete    complete AtomAttempt
//	GET    /api/companions/{gcid}          fetch the learner's Companion
//	POST   /api/companions/{gcid}/xp       award XP, level up if threshold crossed
//
// Middleware: logRequest + extractContext (gcid + X-Tenant-Id headers).
package http

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	consumptionmodelarmor "github.com/apollo-chora/chora-consumption/internal/adapter/modelarmor"
	"github.com/apollo-chora/chora-consumption/internal/domain/active_path_topics"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_attempt"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion/edgescout"
	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
	"github.com/apollo-chora/chora-consumption/internal/domain/dose_pref"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
	"github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
	"github.com/apollo-chora/chora-consumption/internal/domain/proofingtest"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_accuracy"
)

// osGetenv is the production env lookup, exposed via the getenv var so
// tests can stub it. Per feedback_no_inline_config, env reads happen ONLY
// at construction time (cmd/server boot) — no env reads in handlers.
var osGetenv = os.Getenv

// Server holds the wired adapters + config.
type Server struct {
	Paths *inmem.PathRepo
	// Sessions persists AtomAttempt aggregates (CHO-2029): the port is
	// pg-backed in production (atomic_sessions; RLS tenant-scoped) so
	// start/submit survive pod hops; in-memory in unit servers.
	Sessions      atom_attempt.Repository
	Companions    *inmem.CompanionRepo
	Atoms         *inmem.AtomCatalogue
	ExamPrepGoals *inmem.ExamPrepGoalRepo

	// SM2 persists the Maya retention loop's spaced-repetition state
	// (companion.SM2Store port). In-memory by default (NewServer); swapped
	// for the pg-backed adapter at cmd/server boot (migration 0048) so the
	// Ebbinghaus state survives pod restarts. Per the no-debt directive
	// 2026-06-10 handlers FAIL LOUD (500) on storage errors.
	SM2 companion.SM2Store

	// M12.2.B Companion consolidation — multi-Companion (1:N) repo. Owns
	// the `companions` aggregate per ADR-116 amendment + ADR-147 §7 +
	// docs/architecture/multi-companion-per-user-2026-05-11.md. Production
	// (M14.1) replaces this with the pg.CompanionInstanceRepo backed by
	// chora_consumption.companion_instances + family_skill_grants tables.
	CompanionInstances companion.InstanceRepository

	// CompanionMaps clears a retired companion's bond from every map (Goal) that
	// designates it, so a retired companion never lingers on a live map (map↔
	// roster consistency). Wired at cmd/server boot over the same pg goal repo
	// ExtServer.Goals uses; nil ⇒ retire skips the detach with a loud log (the
	// roster slot is still freed).
	CompanionMaps CompanionMapDetacher

	// ResonanceTitles resolves the learner-facing TITLE behind a resonant
	// concept or atom id, so the character sheet can NAME what a companion is
	// tuned to instead of only pointing at the map (D3, CHO-2047).
	//
	// It is a server-side read on purpose. The browser holds two ids and needs
	// two names; resolving them client-side would mean a learner-wide concept
	// read from a screen that wants two rows, which is a cross-map leak waiting
	// to happen. Here the lookup rides the SAME RLS-scoped context as the read
	// that produced the ids.
	//
	// Nil is a legitimate state (unit servers, any deployment that has not
	// wired it) and so is an error: a name lookup must never fail a growth
	// read, because the stage, EXP and skills the screen exists for are all
	// still true. Both degrade to an ABSENT title, never an empty one, which
	// is what lets the profile say a concept is gone rather than invent a name.
	ResonanceTitles ResonanceTitleReader

	// AtomTitles names an atom for readers that never ask about a concept
	// (today: the ritual run story's sources, D2 tail a).
	//
	// Separate from ResonanceTitles because that one binds only when BOTH
	// projections are live. That rule protects the profile and would otherwise
	// leave ritual sources nameless wherever the concept graph is absent,
	// which is a liveness fact about concepts being used to gate atoms.
	// Nil is legitimate and degrades to an ABSENT name, never a failed read.
	AtomTitles AtomTitleReader

	// CHO-2012 loadout seams (ADR-218 D3/D4): the grants bridge with its
	// equipped/unlocked_via columns, the platform Skill catalogue, and the
	// loadout-lifecycle event publisher (skill_granted/loadout_changed).
	// In-memory defaults seed from seedspec (the 0061 seed mirror);
	// cmd/server swaps the pg + outbox adapters at boot.
	Loadouts      companion.LoadoutRepository
	SkillCatalog  companion.CatalogReader
	LoadoutEvents companion.LoadoutOutbox

	// CHO-2016 Grimoire Rituals v1 (ADR-219). The publish/enable service, the
	// deterministic runner, the aggregate + run repos, and the capability
	// resolver. All nil in unit servers ⇒ the rituals subtree returns 503
	// RITUALS_NOT_WIRED; cmd/server wires the pg + adapter set at boot.
	RitualPublisher *companion.RitualPublisher
	RitualRunner    *companion.RitualRunner
	// RitualSinks answers "what can this deployment write?" for the composer
	// (B3b). Nil until the rituals lane is wired, which the list handler serves
	// as an EMPTY array so the FE greys everything rather than guessing.
	RitualSinks companion.SinkRegistry
	Rituals     companion.RitualRepository
	RitualRuns  companion.RunRepository
	RitualCaps  companion.RitualCapabilityResolver

	// CHO-2015 Persona sheet (ADR-219 D2). Persona is the load/apply/persist
	// service over companion_instances; PersonaGuardrail screens the free-text
	// guidance note at SAVE time via Cloud Model Armor (agent_id
	// companion_persona → tier strict). Persona nil ⇒ /v1/me/companions/{id}/
	// persona → 503 PERSONA_NOT_WIRED. PersonaGuardrail nil + a NON-EMPTY note
	// ⇒ 503 PERSONA_GUARDRAIL_NOT_WIRED — a note that would enter the kid-
	// facing Companion system prompt is NEVER persisted unscreened (an empty
	// note is trivially safe and skips screening). cmd/server wires both.
	Persona          *companion.PersonaService
	PersonaGuardrail consumptionmodelarmor.GuardrailPort

	// SpeciesPaths reads the ACTIVE per-species unlock Path for the
	// CHO-2030 next-unlock preview (R3-9 named-tease). Same store as the
	// loadout family (pg CompanionLoadoutRepo in prod; seedspec-seeded
	// in-memory by default). Nil ⇒ growth reads omit the preview
	// (pre-CHO-2030 unit servers).
	SpeciesPaths companion.SpeciesPathReader

	// CompanionConfigJSONResolver resolves a Companion's session-bootstrap
	// config (base instance + ADR-149 growth axis) to its protojson string,
	// for injection into the ADK CreateSession state (bug-3 mesh-sidestep,
	// 2026-06-02): the sidecar-less Companion agent reads its config from
	// session state instead of calling back over consumption's STRICT-mTLS
	// gRPC. Set at cmd/server boot (closure over grpcadapter
	// .ResolveCompanionInstanceConfig + the growth.Service + CompanionInstances).
	// Nil ⇒ chat falls back to no injected config (agent stub/registry path).
	CompanionConfigJSONResolver func(ctx context.Context, tenantID, companionID, callerGCID string) (string, error)

	// S5.1 Maya retention loop + 40/30/30 dose composer support. All four are
	// domain ports, pg-backed in production, in-memory in tests/dev.
	//
	// CHO-2167 closed the NAMED platform debt that used to sit on the last two:
	// they were held as CONCRETE *inmem.* types, so the pg adapters that had
	// existed since CHO-1471 could never be bound and the dose's CURIOSITY +
	// WEAKNESS slots ran off per-pod memory. cmd/server (wireDosingProjections)
	// now binds pg whenever a pool is available and logs which adapter is live.
	Streaks          companion.StreakStore
	XP               companion.XPStore
	TopicAccuracy    topic_accuracy.Repository
	ActivePathTopics active_path_topics.Repository

	// LearnerWeakness is the Growth-Edge read repo (W6, Epic-1b). When wired
	// (cmd/server derived_weakness_wiring) the daily-dose weakness slot reads
	// the learner's Growth Edges (cached drill atoms preferred) instead of
	// the raw topic_accuracy pool. Nil ⇒ legacy accuracy-only slot. Also backs
	// the CHO-2014 weakness_sight invoke runner (weakness.read).
	LearnerWeakness learner_weakness.Repository

	// KGMap assembles the ring-scoped per-user concept map (kg.read_map) for the
	// CHO-2014 map_sight invoke runner. Nil ⇒ map_sight returns 503. Wired at
	// cmd/server boot from the pg concept repos (kgmapread.NewReader).
	KGMap KGMapReader

	// ComposerV2Enabled gates the ADR-224 (CHO-2045) dose selection v2 —
	// subject/KG-balanced allocation + seeded weighted sampling on the weakness +
	// curiosity slots. Set at cmd/server boot from DOSE_COMPOSER_V2_ENABLED (dark
	// by default). False ⇒ the v1 strict global-shakiest fill (byte-identical).
	ComposerV2Enabled bool

	// DoseKGPrefs is the learner's per-KG (per-map) daily-dose include/exclude
	// preference store (ADR-224 #3). It backs GET/PUT /v1/me/dose-preferences and
	// the weakness-pool filter in doseGrowthEdges (excluded maps' concepts are
	// dropped, resolved via Goals.ConceptSet). Wired at cmd/server boot from the
	// pg repo. Nil ⇒ the endpoint 503s + the filter is a no-op (default all-
	// included) — the dose is unaffected.
	DoseKGPrefs dose_pref.Repository

	// CampaignDose is the ADR-227 D12 campaign axis (CHO-2081): the daily
	// dose elects ONE campaign among the learner's focused goals and serves
	// its focus node ahead of the other slots. Shared (same pointer) with
	// ExtServer so the compose + answer-fold sides always elect identically.
	// Nil ⇒ no campaign axis; the dose composes exactly as before. Wired at
	// cmd/server boot (campaign_wiring.go).
	CampaignDose *CampaignDose

	// ExamPrepCoach is optional. When set (env CHORA_AI_KERNEL_ORCHESTRATOR_BASE_URL
	// is configured at boot), the daily-dose handler enriches the dose with
	// weakness-drill picks for learners who have an active exam-prep goal
	// (BE-EP1). Nil ⇒ vanilla 40/30/30 dose composition (S5.1).
	ExamPrepCoach companion.ExamPrepCoachPort
	// ManaQuoter is optional. When set (env CHORA_IDENTITY_MANA_GRPC_URL is
	// configured at boot per BE-USR-2), the daily-dose handler debits 10 mana
	// per LLM-backed coach call BEFORE invoking the coach. Nil ⇒ fail-open
	// (medium-tier action backstop per ADR-142 §8). The service-unreachable
	// audit-log entry lives at the chora-model-broker-gateway side, so the
	// consumption-side fail-open is silent.
	ManaQuoter companion.ManaQuoter
	// CompanionSuspension is the ADR-252 / ADR-254 D11 ADVISORY read of the
	// operator containment state, projected locally from
	// chora.governance.audit.companion_suspension_changed.v1 (the tables live
	// in chora_observability and cross-DB reads are forbidden). It lets the
	// chat turn refuse honestly with 403 COMPANION_SUSPENDED before any mana
	// pre-check or SSE header, and the profile render companion_status.
	//
	// NOT the control: chora-model-gateway is (ADR-252 D1, uncached,
	// fail-closed, deny-before-debit). A read error here means UNKNOWN, so the
	// caller logs loud and proceeds; it must never become a second, local,
	// fail-closed gate. Nil in unit servers; cmd/server always wires it (pg on
	// a pool, in-memory otherwise).
	CompanionSuspension companion.SuspensionAdvisor
	// GreetingPort is optional. When wired (S5.1+) the Companion daily-dose
	// surface fetches an LLM-personalised greeting line from the Model
	// Broker Gateway via this port; per ADR-142 §4 the action costs 50 mana.
	// Nil ⇒ deterministic DefaultGreeting fallback.
	GreetingPort companion.GreetingPort

	// Lane markers for the two Companion bus lanes (ADR-254). NOT env vars
	// and NOT Agent Engine resources: Agent Engine was decommissioned by
	// ADR-169, and nothing reads COMPANION_ENGINE_RESOURCE or
	// RECOMMENDER_ENGINE_RESOURCE anywhere in Go. cmd/server sets both
	// unconditionally in companion_bus_wiring.go as "bus:" + the request
	// topic, so in a wired server they are never empty.
	//
	// They are half of the readiness guard, alongside the ports below, and
	// the two consumers treat an unwired lane differently ON PURPOSE:
	//   - chat / skill-invoke / edge-scout: 503 ENGINE_NOT_CONFIGURED,
	//     fail-loud, no stub fallback.
	//   - daily dose: 200 DEGRADED with deterministic SM-2 + Ebbinghaus
	//     picks and a templated greeting (companion_handlers.go:506), the
	//     F4 exception to the fail-loud directive. Degraded, not absent.
	CompanionEngineResource   string
	RecommenderEngineResource string

	// CompanionEngine + RecommenderEngine are constructed at boot by
	// companion_bus_wiring.go: NewBusTurnEngine and NewBusDoseRecommender,
	// which publish onto the lanes named above. Nil in tests unless
	// explicitly wired via seedDailyDoseEngines + fake clients. (consumption
	// still imports libs/.../agentengine, but only for its error sentinels
	// in gateway_embedding_client.go, never for a client.)
	CompanionEngine   clients.CompanionEnginePort
	RecommenderEngine clients.RecommenderEnginePort

	// AtomIndex is the local atom_index projection repo (CHO-1662 session-state-
	// injection RAG). When wired (cmd/server threads the SAME pg repo the gRPC
	// ConsumptionServer uses), the daily-dose/ai handler pre-fetches candidate
	// atoms in-process and injects them into the recommender session state — the
	// sidecar-less crew CANNOT dial consumption's STRICT-mTLS :9090 back, and
	// consumption already owns atom_index + initiates the Recommend call. Nil ⇒
	// the pre-fetch is skipped (the recommender degrades to its stub searcher);
	// the daily-dose path never 5xx's on a nil/erroring AtomIndex.
	AtomIndex atom_index.Repo

	// LearningPaths is the course-bound LearningPath repo (L4 Fix-1,
	// CHO-1702). When wired (cmd/server threads the SAME pg repo the
	// ExtServer uses), the daily-dose handler sources its atom UNIVERSE from
	// the learner's real enrolled-course paths (ListByLearner → AtomIndex.Get)
	// instead of the synthetic NewAtomCatalogue. Nil ⇒ synthetic catalogue
	// fallback (tests / dev without pg); the dose path never 5xx's on a
	// nil/erroring repo. NOTE: distinct from the LEGACY `Paths` inmem repo
	// above (no ListByLearner).
	LearningPaths learning_path.Repo

	Publisher events.Publisher

	// ADR-149 Companion Growth axis (Iter G.2). Nil ⇒ /v1/me/companions/{id}/
	// growth subpaths return 503. Wired at cmd/server boot when a real
	// Repository + OutboxPort + BreedDistributionProvider are available.
	Growth GrowthServer

	// HatchExpThreshold is the resolved incubation gate (ADR-228 D2,
	// COMPANION_HATCH_EXP_THRESHOLD), carried here so the ROSTER read can warm a
	// Stage-0 pod towards the same denominator the per-companion growth read
	// surfaces (D3). Set at cmd/server boot beside Growth; unset resolves to
	// growth.DefaultHatchExpThreshold via hatchExpThreshold(), never to 0.
	HatchExpThreshold int

	// ADR-154 — Companion conversational chat surface. Nil ⇒
	// POST /v1/me/companions/{id}/chat skips session persistence (engine
	// session is single-turn only). Production wires the pg-backed
	// CompanionChatSessionRepo (see cmd/server/chat_wiring.go).
	CompanionChatSessions companion.ChatSessionRepository

	// F5 — OPTIONAL Vertex AI Memory Bank summary resolver for the per-
	// instance profile's `memory_summary` field. Nil ⇒ the field is OMITTED
	// (Memory Bank not wired). F4 (Memory Bank) is designed separately by a
	// peer; this service deliberately ships ONLY the nil-safe port. The
	// concrete adapter (scoped app_name="companion:{companion_id}") lands with
	// F4 and is attached at cmd/server boot when configured. A resolver error
	// is non-fatal — the profile omits the field rather than 5xx.
	MemorySummaries companion.MemorySummaryResolver

	// F4 (ADR-173) — per-Companion pgvector RAG conversational memory. BOTH
	// Embedder and CompanionMemory must be non-nil for memory to be active; if
	// either is nil the chat handler skips recall + record and chat is
	// unchanged (memory disabled gracefully). Wired at cmd/server boot:
	// CompanionMemory from the pg pool (always when a pool exists); Embedder
	// only when CHORA_MODEL_GATEWAY_GRPC_URL is set (feedback_no_inline_config).
	Embedder        companion.Embedder
	CompanionMemory companion.CompanionMemory

	// CHO-2096 — retire-time memory purge (soft-delete of the companion's
	// retained memory rows). Nil ⇒ purge skipped with a loud log (dev without
	// pg: nothing was ever recorded). Wired beside CompanionMemory at boot.
	CompanionMemoryRetire companion.MemoryRetirer

	// CompanionMemoryRecallTopK caps how many prior memories are recalled per
	// chat turn. <=0 ⇒ defaultMemoryRecallTopK (5). Sourced from
	// COMPANION_MEMORY_RECALL_TOPK at boot.
	CompanionMemoryRecallTopK int

	// CompanionEmbeddingModelID is the embedding model identifier stamped into
	// the persisted companion_memory_recall.model_id column (e.g.
	// "text-embedding-004"). Derived from the embedding model resource at boot.
	CompanionEmbeddingModelID string

	// DefaultManaTier is the per-request mana-tier fallback used when no
	// per-user subscription lookup is wired (Task 2(b)). Sourced from
	// COMPANION_DEFAULT_MANA_TIER at boot (NewServer) per
	// feedback_no_inline_config — NEVER read in a handler. Empty means the
	// canonical safe default ("basic"). Validated at boot; invalid values
	// fail loud (see NewServer). Replaces the previously-hardcoded "basic"
	// literal in resolveManaTier so per-environment tier policy is
	// config-driven (env sourced from Terraform / Secret Manager) rather than
	// an inline source constant.
	DefaultManaTier string

	// ADR-215 WS-1 (CHO-1997) — learner-facing Companion memory READ handler,
	// serving GET /v1/me/companions/{id}/memory. The {id}/memory subpath is
	// dispatched here from handleCompanionInstanceByID (the legacy Server owns
	// the /v1/me/companions/ subtree). nil ⇒ the subpath falls through to the
	// growth-route 404; set at cmd/server boot via wireCompanionMemoryRead (the
	// handler itself 503s per-request when the pgx pool is absent).
	CompanionMemoryRead http.Handler

	// Register 6.4 R6: the learner's own delete over one remembered thing,
	// serving DELETE /v1/me/companions/{id}/memory/{memoryId}. Dispatched from
	// handleCompanionInstanceByID alongside the READ subpath above (same reason:
	// the legacy Server owns the /v1/me/companions/ subtree). nil ⇒ the subpath
	// falls through to the growth-route 404; set at cmd/server boot via
	// wireCompanionMemoryDelete (the handler itself 503s per-request when the pgx
	// pool is absent, never a 204 for an erasure that did not happen).
	CompanionMemoryDelete http.Handler

	// Owner ruling 2026-08-07 (effective per-learner odds): the handler
	// serving GET /v1/me/companions/{id}/egg-odds. Publishes the declared SKU
	// table alongside the EFFECTIVE pool the learner's next reveal rolls
	// (growth.ExcludeOwnedSpecies), so declared == observed for the IMDA D2
	// fairness audit. chora-tenancy cannot compute this: the owned set lives in
	// chora_consumption and cross-DB queries are forbidden.
	//
	// Registered as the more-specific `GET /v1/me/companions/{id}/egg-odds`
	// pattern, which wins over the `/v1/me/companions/` subtree. nil ⇒ the route
	// answers 503; set at cmd/server boot via wireCompanionEggOdds.
	CompanionEggOdds http.Handler

	// CHO-2013 P1.B (R4-4) — the single-step Skill invoke runner's read
	// ports (POST /v1/me/companions/{id}/skills/{key}/invoke). The sidecar-less
	// companion agent cannot dial consumption's STRICT-mTLS surfaces, so the
	// runner pre-fetches each Skill's read context locally and injects it
	// into the one driven turn; sinks are runner-written (spec-§6 Ritual
	// semantics at N=1).
	//
	//   - LearnerProfiles: ADR-200 verified-profile projection
	//     (progress_mirror). Same pg repo the gRPC ReadLearnerProfile uses.
	//   - CompanionMemoryView: recency memory read over companion_memory_recall
	//     (recap_scribe session log). Same port the ADR-215 memory view uses.
	//   - ConceptNodes: learner-scoped ConceptNode resolve for explain_anew
	//     targets (AtomIndex above covers atom targets).
	//
	// nil ports ⇒ the Skill(s) needing them refuse 503 SKILL_INVOKE_NOT_WIRED
	// (fail-loud, never a stubbed context). Wired at cmd/server boot.
	LearnerProfiles     LearnerProfileReader
	CompanionMemoryView companionmind.MemoryReader
	ConceptNodes        ConceptNodeReader

	// kg_explore (Weaver, st4) GROUNDED-RECONCILE suggestion scout ports
	// (companion_skill_invoke_scout.go). KgExploreMap lists the learner's live
	// ConceptNodes (the grounded pool source); Suggestions writes the reconciled
	// pool-grounded concepts into the WS-4 curation inbox. AtomIndex (above) is
	// shared. Both satisfied by the SAME pg repos the concept-graph API uses; a nil
	// port ⇒ kg_explore refuses 503 SKILL_INVOKE_NOT_WIRED (fail-loud, never a
	// silent no-write). Wired at cmd/server boot (wireSkillInvokeRunner).
	KgExploreMap KgExploreMapReader
	Suggestions  KgExploreSuggestionWriter
	// CHO-2117 goal scope: KgExploreGoals resolves the invoking Companion's bound
	// Goal(s) (AttachedCompanionID) and KgExploreEdges feeds the ADR-214 subtree
	// walk, so the fog pool is scoped to the goal's map BEFORE the LLM turn.
	// Either nil ⇒ kg_explore 503 (fail-closed — an unscoped run could leak
	// off-goal topics into the inbox).
	KgExploreGoals KgExploreGoalLister
	KgExploreEdges KgExploreEdgeReader

	// P5 Far Sight (CHO-2017, ADR-220) — the Seeker family's gateway
	// grounded-search seam (companion_skill_invoke_seeker.go). fact_check reaches
	// the web ONLY through this port onto chora-model-gateway's grounded-search
	// surface (the single egress; Armor + mana metering central). The real
	// adapter (grounding config on the gateway Invoke path + citation plumbing)
	// is the P5 gateway-vertical checkpoint; a nil port ⇒ Seeker Skills refuse
	// 503 SKILL_INVOKE_NOT_WIRED (fail-loud — never a silent ungrounded turn).
	GroundedSearch GroundedSearchPort

	// CHO-2059 — OPTIONAL course_id → title resolver (course_directory
	// projection). progress_mirror stitches real course NAMES onto the
	// LearnerProfile facts so the Companion says "Algebra I" instead of "a
	// course". nil ⇒ byte-identical leak-free generic-noun output (the
	// projection is purely additive). Wired at cmd/server boot when a pgx pool
	// is available.
	CourseDirectory CourseDirectoryReader

	// CHO-2040 (CR §8 R7-3) — the ceremony edge-scout propose runner's read
	// ports (POST /v1/me/companions/{id}/ceremony/edge-scout). A COMPOSED
	// runner next to the Skill-invoke machinery (no grant/equip/stage gate —
	// the binding ceremony is the acquisition moment):
	//
	//   - Goals: READ-ONLY goal ownership gate + ConceptSet/RootConceptID
	//     anchor (same pg repo the /v1/me/goals CRUD uses; never mutated).
	//   - FogSuggestions: pending WS-4 concept suggestions (the demoted
	//     fog's output store), focal-scoped to the goal's root concept.
	//   - GradedComments: chora-delivery graded-assessment overall comments
	//     via Delivery.ListLearnerGradedSubmissions (typed gRPC — the
	//     runner's ONLY cross-domain read; cross-DB forbidden).
	//
	// LearnerWeakness (above) + ConceptNodes + Embedder + CompanionEngine +
	// ManaQuoter are shared with the existing runners. Any nil port ⇒ the
	// runner refuses 503 EDGE_SCOUT_NOT_WIRED (fail-loud, never a silently
	// narrowed crawl). Wired at cmd/server boot (wireCeremonyEdgeScout).
	Goals          GoalReader
	FogSuggestions FogSuggestionReader
	GradedComments GradedCommentsReader

	// CeremonyRuns is the R8-1 first-run ledger (CHO-2040): the FIRST
	// edge-scout run per (tenant, goal, learner) stamps the zero-cost
	// companion_ceremony_edge_scout_first code; re-runs pay the locked 25.
	// pg-backed over ceremony_edge_scout_runs (migration 0067) in production
	// (wireCeremonyEdgeScout); in-memory in tests. nil ⇒ the runner refuses
	// 503 EDGE_SCOUT_NOT_WIRED — silently treating every run as paid (or
	// free) would misprice the owner ruling.
	CeremonyRuns edgescout.RunStore

	// CHO-2040 (R8-6/R8-7) — the Virgin Proofing Test composed runner's
	// ports (POST /v1/me/companions/{id}/proofing-test + GET
	// /v1/me/proofing-tests). A composed goal-driven runner next to the
	// ceremony edge-scout (no catalogue row, no grant/equip/stage gate):
	//
	//   - ProofingTests: the consumption-owned ProofingTest aggregate repo
	//     (pg in prod; inmem in unit servers).
	//   - ProofingTickedEdges: READ-ONLY view of the goal's intent-tagged
	//     ceremony learning-edges inside the root subtree (composed over the
	//     concept_graph read repos — never a write path).
	//   - ProofingMana: the reserve→settle/refund economy port
	//     (proofing_test_gen; identity ManaService via the ADR-178 resolver).
	//   - ProofingPublisher: the outbox publisher the runner emits the ONE
	//     chora.creation.ai_assist.started.v2 batch request through (the
	//     allowlisted cross-domain request lane; same events.Publisher
	//     interface as Publisher — a DEDICATED field so unit servers can
	//     capture without disturbing the legacy in-memory stream).
	//
	// Goals (above) is shared with the edge-scout runner. Any nil port ⇒ the
	// runner refuses 503 PROOFING_TEST_NOT_WIRED (fail-soft at boot,
	// fail-loud per request). Wired at cmd/server boot (wireProofingTest).
	ProofingTests       proofingtest.Repository
	ProofingTickedEdges proofingtest.TickedEdgeReader
	ProofingMana        proofingtest.ManaReserver
	ProofingPublisher   events.Publisher
}

// NewServer wires the in-memory repos + the in-memory event publisher.
//
// Phyllis MVP wiring: a channel-based publisher + atom seed catalogue + SM-2
// state repo are added alongside the existing skeleton repos. Real Pub/Sub
// + PostgreSQL adapters land in M12.
//
// BE-EP1 wiring: the exam-prep coach port is resolved from the environment
// variable CHORA_AI_KERNEL_ORCHESTRATOR_BASE_URL (per
// feedback_no_inline_config — the constructor is the ONLY env-aware layer).
// When the variable is unset, ExamPrepCoach is nil and the daily-dose
// handler falls back to the vanilla 2-1-2 mix for every learner.
func NewServer() *Server {
	srv := &Server{
		Paths:              inmem.NewPathRepo(),
		Sessions:           inmem.NewSessionRepo(),
		Companions:         inmem.NewCompanionRepo(),
		CompanionInstances: inmem.NewCompanionInstanceRepo(),
		Atoms:              inmem.NewAtomCatalogue(),
		SM2:                inmem.NewSM2Repo(),
		ExamPrepGoals:      inmem.NewExamPrepGoalRepo(),
		Streaks:            inmem.NewStreakRepo(),
		XP:                 inmem.NewXPRepo(),
		TopicAccuracy:      inmem.NewTopicAccuracyRepo(),
		ActivePathTopics:   inmem.NewActivePathTopicsRepo(),
		Publisher:          events.NewInMemoryPublisher(),
	}
	// CHO-2012 loadout defaults: seedspec-seeded catalogue + in-memory
	// grants bridge + publisher-backed loadout events (prod swaps the pg
	// repo + outbox bridge at cmd/server boot).
	catalog := inmem.NewSkillCatalog()
	srv.SkillCatalog = catalog
	srv.Loadouts = inmem.NewCompanionLoadoutRepo(catalog)
	srv.LoadoutEvents = events.NewLoadoutPublisherBridge(srv.Publisher)
	srv.SpeciesPaths = inmem.NewSpeciesPathRepo()
	// Task 2(b) — config-driven per-tenant default mana tier (replaces the
	// hardcoded "basic" literal in resolveManaTier). Per
	// feedback_no_inline_config + secrets-and-env, the env read happens ONLY
	// here at the composition root. Unset ⇒ canonical growth.DefaultManaTier
	// ("basic"). Set-but-invalid ⇒ fail loud (log.Fatal) so a typo in the
	// Terraform/Secret-Manager-sourced value CrashLoopBackOffs rather than
	// silently degrading every learner's Companion to basic.
	srv.DefaultManaTier = growth.DefaultManaTier
	if v := strings.TrimSpace(getenv("COMPANION_DEFAULT_MANA_TIER")); v != "" {
		if !growth.IsValidManaTier(v) {
			log.Fatalf("consumption: COMPANION_DEFAULT_MANA_TIER=%q invalid (want one of basic|standard|premium) — fail-loud per feedback_no_inline_config", v)
		}
		srv.DefaultManaTier = v
	}
	// BE-USR-2 — wire the mana quoter (gRPC → chora-identity ManaService :9090)
	// when chora-identity is reachable. Per feedback_no_inline_config the
	// constructor is the ONLY env-aware layer. When the variable is unset,
	// ManaQuoter is nil and the daily-dose handler falls back to fail-open
	// (medium-tier action backstop per ADR-142 §8). A set-but-undiallable target
	// fails loud (CrashLoopBackOff) rather than silently degrading every learner.
	if base := strings.TrimSpace(getenv("CHORA_IDENTITY_MANA_GRPC_URL")); base != "" {
		mc, err := clients.NewManaClient(base, 5*time.Second)
		if err != nil {
			log.Fatalf("consumption: mana client dial %q failed: %v — fail-loud per feedback_no_inline_config", base, err)
		}
		srv.ManaQuoter = mc
	}
	// P5 Far Sight (ADR-231 D7) — wire the REAL grounded-search port (gRPC →
	// chora-model-gateway GroundedSearch :9090) when the gateway is reachable.
	// This replaces the P5 nil-seam: the two Seeker skills (fact_check,
	// web_research) stay DARK (activation migs 0087/0088 held), but the seam is
	// now REAL — a Seeker invoke reaches the gateway (which fail-closes with
	// external_egress_disabled until Phase 4 entitles a tenant) instead of a nil
	// 503 SKILL_INVOKE_NOT_WIRED. A NEW gRPC method needs a mesh AuthorizationPolicy
	// allow-list entry for the consumption principal + this caller restart. Per
	// feedback_no_inline_config the constructor is the only env-aware layer; unset
	// ⇒ the port stays nil and Seeker invokes 503 fail-loud (never a silent
	// ungrounded turn). A set-but-undiallable target fails loud (CrashLoopBackOff).
	// Timeout 0 ⇒ the client's generous grounded-egress default (the Vertex web
	// search + synthesis is slower than a plain RPC).
	// EVAL-ONLY seam (build tag `eval_fixture`, ADR-174 §8 Seeker groundedness
	// gate): the Seekers' grounding is gateway-sourced (not a DB pool), so the
	// gate needs a PINNED source set. When this binary was built with
	// -tags eval_fixture AND CHORA_GROUNDED_SEARCH_EVAL_FIXTURE_PATH is set, wire a
	// deterministic test-double GroundedSearchPort returning each fixture row's
	// sources. In the PRODUCTION build (no tag) evalGroundedSearchOverride is a
	// compiled-out no-op returning nil, so this branch is IMPOSSIBLE — the
	// test-double can never enter a prod image ("no stub in prod").
	if fx, err := evalGroundedSearchOverride(getenv); err != nil {
		log.Fatalf("consumption: grounded-search eval fixture load failed: %v — fail-loud", err)
	} else if fx != nil {
		srv.GroundedSearch = fx
		log.Printf("consumption: grounded-search port = EVAL FIXTURE (build tag eval_fixture) — NOT FOR PRODUCTION")
	} else if base := strings.TrimSpace(getenv("CHORA_MODEL_GATEWAY_GRPC_URL")); base != "" {
		gsc, err := clients.NewGroundedSearchGatewayClient(base, 0)
		if err != nil {
			log.Fatalf("consumption: grounded-search gateway client dial %q failed: %v — fail-loud per feedback_no_inline_config", base, err)
		}
		srv.GroundedSearch = gsc
	}
	return srv
}

// getenv is a tiny indirection over os.Getenv so tests can stub the
// boot-time env lookup without touching real process env. Per
// feedback_no_inline_config, ONLY the constructor reads env vars.
var getenv = func(key string) string {
	return osGetenv(key)
}

// Routes builds the http.Handler with all routes + middleware.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Health probes — also under /healthz/ to dodge Cloud Run GFE quirk.
	// /health is the cross-service convention alias (debt sweep 2026-05-13);
	// /healthz retained because k8s manifests already reference it.
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/healthz/", s.healthz)
	mux.HandleFunc("/health", s.healthz)
	mux.HandleFunc("/health/", s.healthz)
	mux.HandleFunc("/readyz", s.readyz)
	mux.HandleFunc("/readyz/", s.readyz)

	// LearningPath endpoints — /api/learning-paths/{id} or /api/learning-paths.
	mux.HandleFunc("/api/learning-paths", s.handleLearningPaths)
	mux.HandleFunc("/api/learning-paths/", s.handleLearningPathByID)

	// Session endpoints. Registered through withRLSContext: the pg
	// atomic_sessions repo is RLS-bound (CHO-2029) and rejects a bare
	// request ctx — the identity wrap happens once at this seam (mirror
	// of the EXT server's withExtRLSContext).
	mux.HandleFunc("/api/sessions", withRLSContext(s.handleSessions))
	mux.HandleFunc("/api/sessions/", withRLSContext(s.handleSessionByID))

	// Companion endpoints — /api/companions/{gcid} and /api/companions/{gcid}/xp.
	mux.HandleFunc("/api/companions/", s.handleCompanionByGCID)

	// Phyllis MVP — Companion + Daily Dose endpoints (Comic Ch6 P14, Ch7 P16).
	// Mounted at root path per phyllis-mvp-2026-05-08.md §5.4 contract.
	mux.HandleFunc("/companion/me", s.handleCompanionMe)
	mux.HandleFunc("/companion/summon", s.handleCompanionSummon)
	// Async AI enrichment for the dose (Companion greeting + Recommender picks)
	// — generous timeout, runs the gateway LLM calls to completion + meters
	// (mana umbrella). Registered as a distinct exact path; the FE fetches it
	// after the deterministic /companion/daily-dose renders.
	mux.HandleFunc("/companion/daily-dose/ai", s.handleDailyDoseAI)
	mux.HandleFunc("/companion/daily-dose", s.handleDailyDose)
	mux.HandleFunc("/atoms/", s.handleAtomsRoot)

	// S5.1 Maya retention loop endpoints. Mounted under /v1/me/* per
	// the consistent learner-self surface convention (matches the EXT
	// `/v1/me/learning-paths` + `/v1/me/topic-retention` mounts).
	mux.HandleFunc("/v1/me/streak", s.handleMeStreak)
	mux.HandleFunc("/v1/me/xp", s.handleMeXP)

	// M12.2.B Companion consolidation — multi-Companion (1:N) endpoints.
	// Migrated from chora-companion service-side per 2026-05-12 decision.
	// Mounted under /v1/me/companions (learner-self collection + by-ID).
	mux.HandleFunc("/v1/me/companions", s.handleCompanionInstances)
	mux.HandleFunc("/v1/me/companions/", s.handleCompanionInstanceByID)

	// Owner ruling 2026-08-07: effective per-learner egg odds. Registered as a
	// method+wildcard pattern so it beats the `/v1/me/companions/` subtree above
	// on this exact path (ServeMux prefers the more specific pattern) without
	// threading another case through the by-ID dispatcher. Always registered:
	// the handler itself answers 503 when its collaborators are absent, so a
	// dev server reports "not wired" instead of a misleading 404.
	mux.HandleFunc("GET /v1/me/companions/{id}/egg-odds", func(w http.ResponseWriter, r *http.Request) {
		if s.CompanionEggOdds == nil {
			writeError(w, http.StatusServiceUnavailable, "EGG_ODDS_NOT_WIRED",
				"effective egg odds unavailable: handler not wired")
			return
		}
		s.CompanionEggOdds.ServeHTTP(w, r)
	})

	// CHO-2040 — the learner's proofing-test read surface (the POST runner
	// rides the /v1/me/companions/{id}/proofing-test subtree above).
	mux.HandleFunc("/v1/me/proofing-tests", s.handleMeProofingTests)

	// CHO-2045 / ADR-224 #3 — per-KG daily-dose preferences (GET excluded set,
	// PUT include/exclude one map). Tenant+gcid scoped (RLS rides repoCtx).
	mux.HandleFunc("/v1/me/dose-preferences", s.handleDosePreferences)

	return s.middleware(mux)
}

// middleware is the legacy per-route wrapper.
//
// Its logfmt-to-stderr access line was REMOVED 2026-08-07 (FINDING-02). That
// line was the bulk of the 1,274-of-1,472 entries the GKE agent stamped ERROR
// on a served 200, and being unstructured text it could carry neither a
// severity nor `logging.googleapis.com/trace`. The single canonical access
// entry is now emitted as Cloud Logging structured JSON on stdout by
// observability.AccessLogMiddleware, mounted once around the composite
// handler in cmd/server. Logging here as well would double-count every
// request and re-introduce the stderr line.
//
// The wrapper is kept so the composition point survives for future
// per-route concerns.
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

// ----- Health -----

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) readyz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ready",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

// ----- LearningPath -----

type createPathReq struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	AtomIDs     []string `json:"atom_ids"`
}

type pathResp struct {
	PathID      string     `json:"path_id"`
	TenantID    string     `json:"tenant_id"`
	OwnerGCID   string     `json:"owner_gcid"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	AtomIDs     []string   `json:"atom_ids"`
	Completed   bool       `json:"completed"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}

func toPathResp(p *learning_path.LearningPath) pathResp {
	return pathResp{
		PathID:      p.PathID,
		TenantID:    p.TenantID,
		OwnerGCID:   p.OwnerGCID,
		Name:        p.Title,
		Description: "",
		AtomIDs:     p.AtomIDs,
		Completed:   p.IsCompleted(),
		CompletedAt: p.CompletedAt,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
		DeletedAt:   p.DeletedAt,
	}
}

func (s *Server) handleLearningPaths(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.createPath(w, r)
	case http.MethodGet:
		s.listPaths(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
	}
}

func (s *Server) createPath(w http.ResponseWriter, r *http.Request) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	var req createPathReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}

	// Legacy /api/learning-paths uses Name (legacy field) in the body —
	// map it through to the canonical Title in learning_path.New. The
	// description field is dropped (legacy `domain/path` retained it,
	// but `domain/learning_path` does not — per S4.2 cleanup the
	// surviving canonical aggregate excludes it).
	_ = req.Description
	p, err := learning_path.New(tenantID, gcid, req.Name, req.AtomIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PATH", err.Error())
		return
	}
	s.Paths.Save(p)
	writeJSON(w, http.StatusCreated, toPathResp(p))
}

func (s *Server) listPaths(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Tenant-Id")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "MISSING_TENANT", "X-Tenant-Id required")
		return
	}
	limit := 50
	items := s.Paths.List(tenantID, limit)
	resp := make([]pathResp, 0, len(items))
	for _, p := range items {
		resp = append(resp, toPathResp(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": resp,
	})
}

func (s *Server) handleLearningPathByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/learning-paths/")
	id = strings.TrimSuffix(id, "/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "MISSING_ID", "path id required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		p, err := s.Paths.Get(id)
		if err != nil {
			writeError(w, http.StatusNotFound, "PATH_NOT_FOUND", "")
			return
		}
		writeJSON(w, http.StatusOK, toPathResp(p))
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
	}
}

// ----- AtomAttempt -----

type startSessionReq struct {
	AtomID string `json:"atom_id"`
	PathID string `json:"path_id"`
}

type completeSessionReq struct {
	Score float32 `json:"score"`
}

type sessionResp struct {
	SessionID    string     `json:"session_id"`
	TenantID     string     `json:"tenant_id"`
	LearnerGCID  string     `json:"learner_gcid"`
	AtomID       string     `json:"atom_id"`
	PathID       string     `json:"path_id,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	Score        float32    `json:"score"`
	TimeSpentSec int64      `json:"time_spent_sec"`
}

// completePassThreshold maps the legacy float `score` 0..1 onto the
// state-machine's binary correct/incorrect — score >= 0.5 = correct.
// This preserves the legacy `/api/sessions/{id}/complete` response shape
// while delegating to atom_attempt.SubmitAnswer under the hood.
const completePassThreshold = 0.5

func toLegacySessionResp(s *atom_attempt.AtomAttempt) sessionResp {
	r := sessionResp{
		SessionID:   s.SessionID,
		TenantID:    s.TenantID,
		LearnerGCID: s.LearnerGCID,
		AtomID:      s.AtomID,
		StartedAt:   s.StartedAt,
		CompletedAt: s.CompletedAt,
	}
	if s.CompletedAt != nil {
		r.Score = 1.0
		r.TimeSpentSec = int64(s.CompletedAt.Sub(s.StartedAt).Seconds())
		if r.TimeSpentSec < 0 {
			r.TimeSpentSec = 0
		}
	}
	return r
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	var req startSessionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	// Legacy /api/sessions handler — note PathID is dropped per S4.2
	// cleanup. The new state-machine atom_session does not encode an
	// optional path_id (paths are tracked via LearningPath.Advance
	// in the SessionCompletionHandler instead).
	_ = req.PathID
	sess, err := atom_attempt.Start(tenantID, gcid, req.AtomID, atom_attempt.SystemClock)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_SESSION", err.Error())
		return
	}
	if err := s.Sessions.Save(r.Context(), sess); err != nil {
		writeError(w, http.StatusInternalServerError, "SESSION_PERSIST_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toLegacySessionResp(sess))
}

func (s *Server) handleSessionByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, "MISSING_ID", "")
		return
	}
	id := parts[0]
	if len(parts) >= 2 && parts[1] == "complete" {
		s.completeSession(w, r, id)
		return
	}
	writeError(w, http.StatusNotFound, "NOT_FOUND", "")
}

func (s *Server) completeSession(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	sess, err := s.Sessions.Get(r.Context(), id)
	if errors.Is(err, atom_attempt.ErrNotFound) {
		writeError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SESSION_READ_FAILED", err.Error())
		return
	}
	var req completeSessionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if req.Score < 0 || req.Score > 1.0 {
		writeError(w, http.StatusBadRequest, "COMPLETE_FAILED", "score must be in [0.0, 1.0]")
		return
	}
	// Legacy float-score → binary correct/incorrect via threshold.
	correct := req.Score >= completePassThreshold
	if _, err := sess.SubmitAnswer("legacy:"+id, false, correct, atom_attempt.SystemClock); err != nil {
		writeError(w, http.StatusBadRequest, "COMPLETE_FAILED", err.Error())
		return
	}
	if err := s.Sessions.Save(r.Context(), sess); err != nil {
		writeError(w, http.StatusInternalServerError, "SESSION_PERSIST_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toLegacySessionResp(sess))
}

// ----- Companion -----

type companionResp struct {
	CompanionID string    `json:"companion_id"`
	TenantID    string    `json:"tenant_id"`
	OwnerGCID   string    `json:"owner_gcid"`
	Name        string    `json:"name"`
	Level       int       `json:"level"`
	XP          int       `json:"xp"`
	Traits      []string  `json:"traits"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type awardXPReq struct {
	Delta int `json:"delta"`
}

type awardXPResp struct {
	Companion companionResp `json:"companion"`
	LeveledUp bool          `json:"leveled_up"`
}

func toCompanionResp(f *companion.Companion) companionResp {
	traits := f.Traits
	if traits == nil {
		traits = []string{}
	}
	return companionResp{
		CompanionID: f.CompanionID,
		TenantID:    f.TenantID,
		OwnerGCID:   f.OwnerGCID,
		Name:        f.Name,
		Level:       f.Level,
		XP:          f.XP,
		Traits:      traits,
		CreatedAt:   f.CreatedAt,
		UpdatedAt:   f.UpdatedAt,
	}
}

// handleCompanionByGCID dispatches GET /api/companions/{gcid} and
// POST /api/companions/{gcid}/xp.
func (s *Server) handleCompanionByGCID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/companions/")
	rest = strings.TrimSuffix(rest, "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, "MISSING_GCID", "")
		return
	}
	gcid := parts[0]
	tenantID := r.Header.Get("X-Tenant-Id")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "MISSING_TENANT", "X-Tenant-Id required")
		return
	}

	if len(parts) == 1 {
		s.getCompanion(w, r, tenantID, gcid)
		return
	}
	if len(parts) == 2 && parts[1] == "xp" {
		s.awardXP(w, r, tenantID, gcid)
		return
	}
	writeError(w, http.StatusNotFound, "NOT_FOUND", "")
}

func (s *Server) getCompanion(w http.ResponseWriter, r *http.Request, tenantID, gcid string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	f, err := s.Companions.GetByGCID(tenantID, gcid)
	if err != nil {
		// Auto-bond a fresh Companion on first read (skeleton convenience —
		// production flow has explicit POST /bond per Tier 5 D20 ritual).
		f, err = companion.New(tenantID, gcid, "Companion")
		if err != nil {
			writeError(w, http.StatusBadRequest, "AUTO_BOND_FAILED", err.Error())
			return
		}
		s.Companions.Save(f)
	}
	writeJSON(w, http.StatusOK, toCompanionResp(f))
}

func (s *Server) awardXP(w http.ResponseWriter, r *http.Request, tenantID, gcid string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	f, err := s.Companions.GetByGCID(tenantID, gcid)
	if err != nil {
		f, _ = companion.New(tenantID, gcid, "Companion")
		s.Companions.Save(f)
	}
	var req awardXPReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	leveled := f.AwardXP(req.Delta)
	s.Companions.Save(f)
	writeJSON(w, http.StatusOK, awardXPResp{
		Companion: toCompanionResp(f),
		LeveledUp: leveled,
	})
}

// ----- helpers -----

func requireContext(r *http.Request) (tenantID, gcid string, err error) {
	tenantID = r.Header.Get("X-Tenant-Id")
	gcid = r.Header.Get("gcid")
	if tenantID == "" {
		return "", "", errors.New("X-Tenant-Id required")
	}
	if gcid == "" {
		return "", "", errors.New("gcid required")
	}
	return tenantID, gcid, nil
}

// withRLSContext wraps a route handler so r.Context() carries the RLS
// identity (tracing tenant + gcid) BEFORE any handler/repo code runs —
// the RLS-bound pg adapters (atomic_sessions, CHO-2029) reject a bare
// request ctx ("rls: tenant_id missing on context"). Internal-lane
// mirror of the EXT server's withExtRLSContext. Requests without the
// identity headers pass through unwrapped; the handlers' own
// requireContext then renders the 400.
func withRLSContext(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tenantID, gcid, err := requireContext(r); err == nil {
			ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
			r = r.WithContext(ctx)
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode error: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{
		"code":    code,
		"message": message,
	})
}
