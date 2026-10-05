// ext_server.go — EXT scope HTTP server.
//
// Mounts the M13 EXT-scope endpoints distinct from the older Phyllis-MVP
// router.go (which is owned by the Companion/DailyDose track):
//
//	POST   /learning-paths
//	POST   /learning-paths/{id}/enrollments
//	GET    /learning-paths/{id}/progress?gcid=X
//	POST   /sessions
//	PATCH  /sessions/{id}/answer
//	POST   /sessions/{id}:abandon
//	GET    /sessions/{id}
//	GET    /knowledge-graph/discover?topic=X&depth=N
//	GET    /me/knowledge-graph/heatmap
//
// Hexagonal: domain code never imports this package; adapters wired here.
package http

import (
	"context"
	"net/http"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	extinmem "github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/adapter/storage"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	"github.com/apollo-chora/chora-consumption/internal/domain/active_path_topics"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_attempt"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaignquestion"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/course_content"
	"github.com/apollo-chora/chora-consumption/internal/domain/course_directory"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
	"github.com/apollo-chora/chora-consumption/internal/domain/knowledge_graph_read"
	"github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
	"github.com/apollo-chora/chora-consumption/internal/domain/mergesplit"
	st "github.com/apollo-chora/chora-consumption/internal/domain/student_transcript"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_accuracy"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
	wb "github.com/apollo-chora/chora-consumption/internal/domain/weakness_blob"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

// ExtServer holds the EXT-scope wired adapters.
//
// S4.2 additions: AtomIndex (skinny atom projection from
// chora.creation.atom.created.v1), Retention (TopicRetention scores), and
// the SessionCompletion handler that fans out atom_session.completed.v1
// side-effects (LearningPath.Advance + TopicRetention.Review + IMDA D1
// evidence emit).
type ExtServer struct {
	Paths       learning_path.Repo
	Enrollments *extinmem.EnrollmentRepo
	// Sessions persists AtomAttempt aggregates (CHO-2029): pg-backed in
	// production (atomic_sessions, RLS tenant-scoped) so start/submit
	// survive pod hops; extinmem default for unit servers.
	Sessions atom_attempt.Repository
	// AtomIndex is the Repo port — in-memory by default, swapped for the
	// pg-backed adapter at cmd/server boot via WireAtomIndexPg (CHO-1612 D2).
	AtomIndex atom_index.Repo
	// Retention is the TopicRetention port — in-memory by default, swapped for
	// the pg-backed (durable, RLS-bound) adapter at cmd/server boot via
	// WireTopicRetentionPg (CHO-1949). Without the swap the Ebbinghaus score is
	// wiped on every pod restart.
	Retention topic_retention.Repository
	Publisher events.Publisher
	Graph     knowledge_graph_read.Graph // injected; MAY be nil for tests skipping KG

	// S4.2 subscribers — exposed for cmd/server wiring + tests.
	AtomCreatedSub       *subscribers.AtomCreatedSubscriber
	EnrollmentCreatedSub *subscribers.EnrollmentCreatedSubscriber
	SessionCompletion    *subscribers.SessionCompletionHandler

	// CHO-1968 — atom PLAYABILITY projection: flips atom_index DRAFT→PUBLISHED
	// + stamps the answer key from chora.creation.atom.published.v1 so the daily
	// dose serves published + answerable atoms only. Depends ONLY on AtomIndex
	// (rebuilt by WireAtomIndexPg).
	AtomPublishedSub *subscribers.AtomPublishedSubscriber

	// AtomRefreshSub is the ADR-244 D5 standing refresh trigger (proposals
	// only, zero atom_refs writes). Built by cmd/server atom_refresh_wiring.go
	// over the pg ledger (mig 0108); nil when the pg pool is absent, in which
	// case the atom-published fan-out and the atom-updated inbox stay dark
	// (loudly logged at wire time).
	AtomRefreshSub *subscribers.AtomRefreshSubscriber

	// CHO-1612 — heterogeneous course-content projection (atoms/videos/
	// youtube/documents/live-classrooms/assessments) fed by
	// chora.delivery.course.content_composed.v1.
	// CourseContent is the ProjectionRepo port — in-memory by default
	// (NewExtServer), swapped for the pg-backed adapter at cmd/server boot
	// via WireCourseContentPg when a pgx pool is available (CHO-1612 D2).
	CourseContent    course_content.ProjectionRepo
	CourseContentSub *subscribers.CourseContentComposedSubscriber

	// ADR-200 WS1.c3 — global LearnerProfile projection fed by verified
	// per-learner events (certification.issued / learning_path.completed /
	// enrollment.created [+ submission.graded / enrollment.completed in later
	// phases]). pg-only (the read-model is meaningless without a DB), so it is
	// nil until WireLearnerProfilePg runs at cmd/server boot.
	LearnerProfileSub *subscribers.LearnerProfileSubscriber

	// W6 Slice 1 (Four-Mode plan "Outcome spine") — the StudentTranscript
	// read-model, fanned out from the SAME chora.delivery.submission.
	// graded.v1 / chora.delivery.certification.issued.v1 deliveries the
	// LearnerProfile projection above already consumes (no new Pub/Sub
	// subscriptions). Transcript is the READ port (GET /v1/me/transcript +
	// GET /v1/transcript/by-assessments); TranscriptSub is the projection
	// subscriber wireSubscriberPushHandlers extends LearnerProfilePushDeps
	// with. Both nil until WireTranscriptPg runs at cmd/server boot (pg-only
	// — the read-model is meaningless without a DB); nil Transcript ⇒ the two
	// read routes return 503 (fail-loud, never a silently empty list).
	Transcript    st.Repository
	TranscriptSub *subscribers.TranscriptSubscriber
	// TranscriptSeen is the B6 item 2 write port for the unseen-result flag.
	// Narrow and separate from Transcript above, because three fakes across
	// these tests implement st.Repository and none of them performs this
	// write. nil ⇒ the :seen action returns 503 rather than pretending to
	// have recorded something.
	TranscriptSeen st.SeenMarker

	// CHO-2059 — course_directory projection (course_id → title) fed by
	// chora.delivery.course.{created,updated}.v1. Lets a Companion name a real
	// course instead of "a course". pg-only (a read-model is meaningless
	// without a DB), so it is nil until WireCourseDirectoryPg runs at
	// cmd/server boot.
	CourseMetadataSub *subscribers.CourseMetadataSubscriber

	// CourseDirectory is the READ side of the same CHO-2059 course_directory
	// projection — resolves course_id → real title so the course-learn read
	// (getMeLearningPathByCourse) replaces the "Course <uuid>" bootstrap
	// placeholder with the actual course name. Same pg repo instance as the
	// writer (wired in wireCourseDirectoryRepo); nil ⇒ the path title is kept.
	CourseDirectory CourseDirectoryReader

	// OnUnresolvedCourseTitles observes course ids that the directory had NO row
	// for — i.e. every time a learner is served a raw-UUID placeholder instead of
	// a course name. nil ⇒ defaultUnresolvedCourseTitles (a loud log).
	//
	// CHO-2247 Guard 2. This exists because the gap it guards was SILENT:
	// course_directory sat at 5 rows against 19 courses while lookupCourseTitles
	// logged only on a lookup ERROR — a clean lookup with no row said nothing,
	// so the projection could rot indefinitely behind a green suite.
	//
	// ⚠ THIS IS A SEAM, NOT AN ALERT. The intent was an alertable METRIC, which
	// this platform cannot currently express: there is no metrics lane at all —
	// no /metrics endpoint in any service, no PodMonitoring manifest, GMP
	// installed but scraping nothing, and libs/chora-go-common/tracing is a
	// manual W3C traceparent propagator rather than the OTel SDK, so there is not
	// even a span to hang an attribute on. Meanwhile the buildout-cost-pause
	// _Default sink exclusion has dropped effectively all logs since 2026-07-15,
	// which disarms log-based alerting platform-wide. So the default log below is
	// honest visibility, NOT a guard that can page anyone. Introducing a metrics
	// lane (OTel metrics exporter, or a /metrics endpoint + PodMonitoring) is an
	// ADR-scoped platform decision and is deliberately NOT taken here. Plug the
	// counter in HERE when it lands.
	OnUnresolvedCourseTitles func(ctx context.Context, courseIDs []string)

	// S6.4 subscribers — atom_session.completed.v1 → topic_accuracy +
	// learning_path.bootstrapped.v1 / advanced.v1 → active_path_topics.
	// Both write into the SAME repos that the Phyllis MVP daily-dose
	// composer reads. cmd/server bridges the two via shared repo
	// instances (see ShareDosingProjections).
	//
	// CHO-2167: these are DOMAIN PORTS, not concrete in-memory adapters. Holding
	// the concrete type here is what made the durable pg adapters unbindable —
	// the projections looked durable (table, migration, adapter, subscriber) but
	// lived in the pod's heap.
	TopicAccuracyRepo    topic_accuracy.Repository
	ActivePathTopicsRepo active_path_topics.Repository
	TopicAccuracySub     *subscribers.TopicAccuracySubscriber
	ActivePathTopicsSub  *subscribers.ActivePathTopicsSubscriber

	// S5.2 — Per-User Knowledge Graph (ADR-143). Domain ports: NewExtServer
	// seeds the in-memory adapters (tests / dev without a DB);
	// WireUserKGPg swaps in the RLS-bound pg adapters at boot so canvas
	// state (clusters / explorations / hexagon fog / trail) survives pod
	// rolls.
	KGClusters     userknowledgegraph.MapClusterRepo
	KGExplorations userknowledgegraph.ExplorationRepo
	KGHexagons     userknowledgegraph.HexagonRepo
	KGEdges        userknowledgegraph.AtomSemanticEdgesRepo
	KGJunctions    userknowledgegraph.JunctionRepo

	// ClusterProjector re-projects a retired fog MapCluster into a sovereign
	// Goal (ADR-223); wired at boot beside the pg KG repos. Nil ⇒ the convert
	// endpoint returns 503 (fail-loud, never a silent no-op).
	ClusterProjector clusterProjector

	// LearnerWeakness is the Growth-Edge (Epic-1b W8a) read repo — the pgvector
	// learner_weakness.Repository, wired at cmd/server boot (nil ⇒ the
	// /v1/me/growth-edges routes return 503).
	LearnerWeakness lw.Repository

	// WeaknessLoader is the ADR-247 Capability B (CHO-2328) NARROW weakness read
	// port (LoadWeaknessContextByConceptKey only) used to enrich the fog
	// suggestion request with the focal node's diagnosed weakness (F2). The same
	// concrete *pg.LearnerWeaknessRepo backs it (set alongside LearnerWeakness at
	// boot). Kept separate from the 13-method Repository above on purpose: the many
	// unrelated Repository mocks need not implement it (interface segregation).
	// Nil-tolerant: absent (or a load error) skips the weakness, never fails the
	// request (the enrichment is additive on an already-durable publish).
	WeaknessLoader lw.WeaknessContextLoader

	// WeaknessEmbedder computes concept-label embeddings so the ceremony
	// learning-edges write path can mint a backing learner_weakness for each
	// remediate edge (CHO-2043 — map↔list consistency: the map paints shaky from
	// concept_nodes.intent, the list is backed by learner_weakness, so a
	// remediate tick must seed both). Wired at cmd/server boot beside
	// LearnerWeakness; nil (or nil LearnerWeakness) ⇒ concepts still mint but the
	// weakness backing is skipped with a loud log.
	WeaknessEmbedder lw.Embedder

	// Goals is the learner-owned Goal aggregate repo (ADR-204 §2), wired at
	// cmd/server boot (nil ⇒ the /v1/me/goals routes return 503).
	Goals goal.Repository

	// GoalKnowledge stales the cached per-goal Companion reflections (CHO-2118).
	// It is here because a goal PATCH emits NO event: re-rooting a goal (ADR-214)
	// re-points it at a different concept subtree, which is the strongest
	// invalidation signal there is, and the only place that fact exists is this
	// handler. Every OTHER invalidation arrives as an event (see the six-topic
	// goal-knowledge inbox). Nil ⇒ re-root invalidation is DARK: the reflection
	// would keep describing the old subtree until max-age. Wired at cmd/server
	// boot (see cmd/server/goal_knowledge_wiring.go).
	GoalKnowledge GoalKnowledgeInvalidator

	// GoalKnowledgeRead serves GET /v1/me/goals/{id}/knowledge (CHO-2118 tier-2):
	// the tier-1 deterministic block + the cached Companion reflection, and the
	// lazy-regen request emitter behind it. Nil ⇒ the sub-resource returns 503
	// (the goal CRUD routes are unaffected). Wired at cmd/server boot (see
	// cmd/server/goal_knowledge_read_wiring.go).
	GoalKnowledgeRead *GoalKnowledgeReadService

	// CompanionSuspension is the same ADVISORY containment read Server holds
	// (ADR-254 D11). ADR-252 Q5: while the Companion is contained the
	// reflection read skips the claim AND the publish and renders the `paused`
	// status, because the alternative is a claim / deny / expire / re-claim
	// loop that spends one Pub/Sub message and one unit of orchestrator work
	// per pending-TTL, per learner, per goal, for as long as the suspension
	// lasts. The skip must sit BEFORE the claim: this handler's own rule is
	// "claim fails, do NOT publish", so a pause expressed as a claim failure
	// would be indistinguishable from a broken database. Nil in unit servers.
	CompanionSuspension companion.SuspensionAdvisor

	// ADR-227 WS-C1 (CHO-2080) — the goal campaign doors
	// (/v1/me/goals/{id}/campaign/*). CampaignProgress reads the ladder rows
	// for the seal's frontier walk; CampaignResonance retargets the bound
	// Companion's resonant concept on focus moves (addendum #5);
	// CampaignEvents emits focus_assigned / goal_sealed on the verified
	// stream. Any nil ⇒ the campaign doors return 503 (fail-loud). Wired at
	// cmd/server boot (campaign_wiring.go). CampaignResealMinNewWins
	// resolves CAMPAIGN_RESEAL_MIN_NEW_WINS (<=0 ⇒ domain default).
	CampaignProgress         campaign.ProgressRepository
	CampaignResonance        CampaignResonance
	CampaignEvents           CampaignEvents
	CampaignResealMinNewWins int

	// CampaignDose is the ADR-227 D12 campaign axis (CHO-2081) — the
	// answers door folds server-graded answers on today's campaign material
	// into the campaign Grader. SAME pointer as Server.CampaignDose so both
	// sides elect the same daily march. Nil ⇒ no campaign folding.
	CampaignDose *CampaignDose

	// CampaignQuestionBank is the ADR-227 D13 question-set store (CHO-2082,
	// WS-C3) backing the "generate once, retrieve forever" serve door
	// (GET /v1/me/goals/{id}/campaign/questions): a persisted, servable set
	// is returned verbatim at zero LLM cost. Nil ⇒ the serve door returns 503
	// (fail-loud). Wired at cmd/server boot beside CampaignDose.
	CampaignQuestionBank campaignquestion.Repository

	// PracticeBudget is the B6 item 3 read port for the daily practice-tap
	// allowance. Narrow, like the item 1 and item 2 ports: it only counts.
	// The pg CampaignQuestionSetRepo satisfies it structurally. nil ⇒ the
	// endpoint returns 503 rather than a full budget nobody can spend.
	PracticeBudget PracticeBudgetCounter

	// ADR-212 WS-2 (CHO-1995) — learner-sovereign re-root. Concepts + ConceptEdges
	// load the learner's graph; ConceptReRoot applies the append-only re-parent
	// plan atomically. All nil ⇒ /v1/me/concept-graph* return 503. Wired at
	// cmd/server boot (concept_graph_wiring.go).
	// ConceptEvents makes an atom_refs change observable (ADR-244 D4).
	// Optional: emission is additive telemetry on an already-durable write.
	ConceptEvents ConceptEvents
	Concepts      conceptgraph.ConceptNodeRepository
	ConceptEdges  conceptgraph.EdgeRepository
	ConceptReRoot conceptgraph.ReRootApplier

	// WS-C6 (CHO-2085, ADR-227 addendum #7) — lineage-aware paint: per-concept
	// transitive ancestor concept_keys from concept_node_lineage so slug-axis
	// joins survive merge/split re-keying. Nil ⇒ no ancestor resolution
	// (fail-soft read enrichment); wired at cmd/server boot.
	ConceptLineage ConceptLineageReader

	// WS-C6 (CHO-2085, ADR-227 D14) — the atomic merge/split applier: one
	// chora_consumption tx spanning nodes + edges + lineage + ladder +
	// retention + suggestion repoints + goal repairs. Nil ⇒ the merge/split
	// doors refuse 503 (fail-loud). Wired at cmd/server boot.
	MergeSplit mergesplit.Applier

	// ADR-212 WS-4 — Companion suggestion curation. Suggestions lists/reads the
	// learner's pending suggestions + records Dismiss; SuggestionAccept atomically
	// materialises an accepted one (concept/edge insert + status flip) in one tx.
	// Both nil ⇒ /v1/me/concept-graph/suggestions* return 503. Wired at
	// cmd/server boot (concept_suggestion_wiring.go).
	Suggestions      conceptgraph.SuggestionRepository
	SuggestionAccept conceptgraph.SuggestionAccepter

	// LearningEdges applies a ceremony learning-edges batch (CHO-2038): each
	// ticked selection → a ConceptNode + a hierarchy edge under the goal root,
	// all-or-nothing in one tx. Wired at cmd/server boot (nil ⇒ 503).
	LearningEdges conceptgraph.LearningEdgeApplier

	// Companion acquisition for a map (WS-A3, ADR-214 D1). CompanionInstances
	// persists the minted Instance (roster-cap enforced); the map↔Companion link is
	// the Goal itself (Goal.AttachedCompanionID — the single source of truth), so the
	// theme-keyed CompanionMapBinding repo is RETIRED. nil CompanionInstances/Goals ⇒
	// /v1/me/companions/acquire returns 503. Wired at cmd/server boot
	// (companion_binding_wiring.go).
	CompanionInstances companion.InstanceRepository

	// Summoner applies the companion-side effects of a Goal attach (CHO-2013
	// P1, R3-1): specialization derive + resonant-pick clear +
	// specialization_changed emit. nil ⇒ the goals-PATCH attach door
	// refuses 503 (fail-loud; reads unaffected).
	Summoner GoalSummoner

	// GrowthInit births sovereign companions hatched (stage 1, CHO-2013 P1
	// R3-7 + the dev_hatched honest fix). nil ⇒ acquire refuses 503.
	GrowthInit BornHatcher

	// Epic-1b W8b — Growth-Edge upload PRODUCER. WeaknessUploads is the upload-job
	// repo; WeaknessBlobs streams the doc to GCS. Both wired at cmd/server boot
	// (nil ⇒ POST/GET .../uploads return 503).
	WeaknessUploads wu.Repository
	WeaknessBlobs   storage.BlobUploader

	// B6 item 1: the learner's diagnoses parked at the HITL review interrupt,
	// as a SERVER-side list. Read-model port, separate from the write-side
	// Repository above; the pg repo satisfies both. nil ⇒ the GET arm of
	// .../uploads returns 503 rather than an empty list, because "we cannot
	// look" and "you have nothing pending" are different answers and only one
	// of them is safe to show the learner.
	WeaknessPendingReviews wu.PendingReviewLister

	// WS-4 (CHO-1956 / ADR-205 D6) — upfront mana gate for the premium upload
	// path. nil ⇒ disabled (today's free behaviour, empty reservation_id). Wired
	// ENABLED at boot only when WEAKNESS_UPFRONT_MANA_ENABLED=true (DARK by
	// default; coupled to the crew cutover — see cmd/server/weakness_mana_wiring.go).
	WeaknessMana *wu.ManaGate

	// WS-5 (CHO-1957 / ADR-205 D8) — per-blob envelope crypto-shred + the
	// source_material consent gate. All nil/disabled by default (DARK).
	//
	// BlobEnvelope seals the raw upload with a random per-blob DEK before it hits
	// GCS; nil ⇒ the blob is streamed in plaintext (today's behaviour). BlobDEKs
	// persists the wrapped DEK (MUST be set whenever BlobEnvelope is). SourceMaterial
	// gates the transient-textbook upload kind behind a one-tap upload-rights
	// consent. Wired at boot only when WEAKNESS_BLOB_ENVELOPE_ENABLED /
	// WEAKNESS_SOURCE_MATERIAL_ENABLED are true — see cmd/server/weakness_crypto_wiring.go.
	BlobEnvelope   *wb.Envelope
	BlobDEKs       wb.WrappedDEKStore
	SourceMaterial *wu.SourceMaterialGate

	KGConfig           userknowledgegraph.TenantConfigReader
	KGEvents           userknowledgegraph.EventPublisher
	KGJunctionDetector *subscribers.JunctionDetector
}

// NewExtServer wires the EXT-scope in-memory adapters.
//
// The Graph adapter is injected (production: HTTP client to chora-creation;
// tests: in-memory stub). nil is permitted — the KG endpoints return 503
// when no Graph is wired.

// GoalSummoner is the narrow Summon surface the goals-PATCH door needs
// (CHO-2013 P1). *companion.Summoner satisfies it.
type GoalSummoner interface {
	ValidateOwned(ctx context.Context, tenantID, ownerGCID, companionID string) (*companion.Instance, error)
	OnGoalBound(ctx context.Context, in companion.SummonInput) (bool, error)
}

// BornHatcher is the narrow growth surface sovereign acquisition needs
// (CHO-2013 P1). *growth.Service satisfies it.
type BornHatcher interface {
	InitBornHatched(ctx context.Context, in growth.InitBornHatchedInput) (*growth.InitBornHatchedResponse, error)
}

func NewExtServer(g knowledge_graph_read.Graph) *ExtServer {
	pub := events.NewInMemoryPublisher()
	paths := extinmem.NewLearningPathRepo()
	atomIdx := extinmem.NewAtomIndexRepo()
	retention := extinmem.NewTopicRetentionRepo()

	// S5.2 KG wiring — defaults to in-memory adapters + static tenant
	// config (KG_DEFAULT_MAX_CLUSTERS_PER_USER=3) until the Pub/Sub
	// projection from chora-tenancy lands. cmd/server reads env vars.
	kgConfig, _ := clients.NewStaticKGConfigReader(
		clients.KGDefaultMaxClustersPerUser,
		clients.KGDefaultFogInvalidationGraceS,
	)
	// S6.4 — projections that the Phyllis MVP daily-dose composer
	// reads from. Owned here by default; can be overridden by
	// ShareDosingProjections so a Phyllis Server's repos receive the
	// subscriber writes directly (see cmd/server).
	topicAccRepo := inmem.NewTopicAccuracyRepo()
	activePathRepo := inmem.NewActivePathTopicsRepo()

	// CHO-1612 — course-content projection + its subscriber share one repo.
	courseContentRepo := extinmem.NewCourseContentRepo()

	return &ExtServer{
		Paths:                paths,
		Enrollments:          extinmem.NewEnrollmentRepo(),
		Sessions:             extinmem.NewAtomSessionRepo(),
		AtomIndex:            atomIdx,
		Retention:            retention,
		Publisher:            pub,
		Graph:                g,
		AtomCreatedSub:       subscribers.NewAtomCreatedSubscriber(atomIdx, paths),
		AtomPublishedSub:     subscribers.NewAtomPublishedSubscriber(atomIdx),
		EnrollmentCreatedSub: subscribers.NewEnrollmentCreatedSubscriber(paths, courseContentRepo, pub),
		SessionCompletion:    subscribers.NewSessionCompletionHandler(paths, atomIdx, retention, pub),

		CourseContent:    courseContentRepo,
		CourseContentSub: subscribers.NewCourseContentComposedSubscriber(courseContentRepo, paths),

		TopicAccuracyRepo:    topicAccRepo,
		ActivePathTopicsRepo: activePathRepo,
		TopicAccuracySub:     subscribers.NewTopicAccuracySubscriber(topicAccRepo, atomIdx),
		ActivePathTopicsSub:  subscribers.NewActivePathTopicsSubscriber(activePathRepo, atomIdx),

		KGClusters:     extinmem.NewMapClusterRepo(),
		KGExplorations: extinmem.NewExplorationRepo(),
		KGHexagons:     extinmem.NewHexagonRepo(),
		KGEdges:        extinmem.NewAtomSemanticEdgesRepo(),
		KGJunctions:    extinmem.NewJunctionRepo(),
		KGConfig:       kgConfig,
		KGEvents:       events.NewKGPublisher(pub),
	}
}

// ShareDosingProjections rebinds the TopicAccuracy + ActivePathTopics
// subscribers to write into the supplied repos. Called by cmd/server
// after constructing the Phyllis MVP Server so that the daily-dose
// composer reads from the SAME repo instances the subscribers populate.
//
// MUST be called BEFORE any subscriber Handle() invocations (i.e. at
// boot time only). Idempotent — passing the same repos a second time
// is a no-op.
//
// CHO-2167: takes the PORTS, so cmd/server can hand it the pg adapters. It MUST
// run AFTER wireDosingProjections, or it shares the in-memory repos the Server
// was constructed with and the pg swap is silently undone — the exact shape of
// bug this ticket exists to kill.
func (s *ExtServer) ShareDosingProjections(topicAcc topic_accuracy.Repository, activePath active_path_topics.Repository) {
	if topicAcc == nil || activePath == nil {
		return
	}
	s.TopicAccuracyRepo = topicAcc
	s.ActivePathTopicsRepo = activePath
	s.TopicAccuracySub = subscribers.NewTopicAccuracySubscriber(topicAcc, s.AtomIndex)
	s.ActivePathTopicsSub = subscribers.NewActivePathTopicsSubscriber(activePath, s.AtomIndex)
}

// wireKGDetector lazily constructs the junction detector when the
// caller's KG repos are populated. cmd/server can override this for
// production wiring.
func (s *ExtServer) wireKGDetector() {
	if s.KGJunctionDetector == nil && s.KGClusters != nil && s.KGHexagons != nil && s.KGJunctions != nil && s.KGEvents != nil {
		s.KGJunctionDetector = subscribers.NewJunctionDetector(
			s.KGClusters, s.KGHexagons, s.KGJunctions, s.KGEvents,
			subscribers.DefaultJunctionOverlapThreshold,
		)
	}
}

// RewireKGEvents rebinds the KG EventPublisher and rebuilds the junction
// detector so its PublishJunctionDetected calls route through the new
// publisher too (ADR-204 Slice F.2).
//
// NewExtServer builds KGEvents over the in-memory publisher. cmd/server later
// swaps ext.Publisher to the durable outbox publisher (for weakness), but
// KGEvents — and the junction detector that captured it at WireUserKGPg time —
// kept the in-memory publisher, so every KG mutation event (the 11
// chora.consumption.kg_* topics) was buffered in process and silently dropped.
// Passing the outbox-backed events.NewKGPublisher here makes those events
// durable via the SAME outbox store the dispatcher drains. No-op when pub is
// nil. MUST be called AFTER WireUserKGPg so the detector rebuild binds the
// pg-backed repos.
func (s *ExtServer) RewireKGEvents(pub userknowledgegraph.EventPublisher) {
	if pub == nil {
		return
	}
	s.KGEvents = pub
	// Rebuild the detector against the new publisher so junction-detected
	// events are durable too. Uses whatever repos are currently wired
	// (pg-backed after WireUserKGPg, in-memory otherwise).
	if s.KGClusters != nil && s.KGHexagons != nil && s.KGJunctions != nil {
		s.KGJunctionDetector = subscribers.NewJunctionDetector(
			s.KGClusters, s.KGHexagons, s.KGJunctions, s.KGEvents,
			subscribers.DefaultJunctionOverlapThreshold,
		)
	}
}

// WireUserKGPg swaps the in-memory per-user KG repos for the supplied
// (pg-backed, RLS-bound) adapters and rebuilds the junction detector
// against them. Called by cmd/server at boot when a pgx pool is
// available; no-op when any repo is nil (the in-memory adapters from
// NewExtServer are retained for tests / dev without a database).
//
// Per multi-tenant-rls the pg adapters run rls.ApplySession; the KG
// routes establish the tenant/gcid request context via withExtRLSContext
// at registration (Routes), so every handler call-site — including the
// on-demand fog-generation persist — reaches the repos with the RLS
// identity attached.
func (s *ExtServer) WireUserKGPg(
	clusters userknowledgegraph.MapClusterRepo,
	explorations userknowledgegraph.ExplorationRepo,
	hexagons userknowledgegraph.HexagonRepo,
	junctions userknowledgegraph.JunctionRepo,
) {
	if clusters == nil || explorations == nil || hexagons == nil || junctions == nil {
		return
	}
	s.KGClusters = clusters
	s.KGExplorations = explorations
	s.KGHexagons = hexagons
	s.KGJunctions = junctions
	s.KGJunctionDetector = subscribers.NewJunctionDetector(
		clusters, hexagons, junctions, s.KGEvents,
		subscribers.DefaultJunctionOverlapThreshold,
	)
}

// withExtRLSContext wraps an EXT route handler so r.Context() carries the
// RLS identity (tracing tenant + gcid) BEFORE any handler/repo code
// runs. The RLS-bound pg adapters (KG, atomic_sessions) reject a bare
// request ctx ("rls: tenant_id missing on context") — the same bug class
// as the session-completion side-effect fix (me_handlers.go), solved here
// once at the registration seam instead of per call-site. Requests
// without the identity headers pass through unwrapped; the handlers' own
// extRequireContext then renders the 400.
func withExtRLSContext(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tenantID, gcid, err := extRequireContext(r); err == nil {
			ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
			r = r.WithContext(ctx)
		}
		next(w, r)
	}
}

// WireCourseContentPg swaps the in-memory CourseContent projection for the
// supplied (pg-backed) ProjectionRepo and rebuilds the content_composed
// subscriber to write into it (CHO-1612 D2 durability). Called by cmd/server
// at boot when a pgx pool is available; a no-op when repo is nil (the
// in-memory adapter from NewExtServer is retained for tests / dev without a
// database). MUST be called BEFORE any subscriber Handle() invocations.
//
// Per multi-tenant-rls the pg adapter runs rls.ApplySession internally; tenant
// context flows from the HTTP middleware (tracing.WithTenantID) on the request
// context — this helper configures no tenant context of its own.
func (s *ExtServer) WireCourseContentPg(repo course_content.ProjectionRepo) {
	if repo == nil {
		return
	}
	s.CourseContent = repo
	// s.Paths — the back-fill (CHO-2169) writes learners' paths, so it must bind
	// whatever path repo is current. WireLearningPathPg REBUILDS this subscriber
	// when the pg path repo lands, so the order of the two Wire* calls is
	// immaterial; without that rebuild the back-fill would write into the
	// in-memory repo and vanish on the next pod (the CHO-2167 defect).
	s.CourseContentSub = subscribers.NewCourseContentComposedSubscriber(repo, s.Paths)
	// The enrolment bootstrap reads the curriculum from THIS projection, so it must
	// be rebound too — otherwise it keeps the boot-time in-memory repo and every
	// path bootstraps empty in prod, which is the very defect CHO-2169 fixes.
	s.EnrollmentCreatedSub = subscribers.NewEnrollmentCreatedSubscriber(s.Paths, repo, s.Publisher)
}

// WireLearnerProfilePg constructs the LearnerProfile projection subscriber over
// the supplied (pg-backed) ProjectionRepo (ADR-200 WS1.c3). Called by cmd/server
// at boot when a pgx pool is available; a no-op when repo is nil (no projection
// without a DB). MUST be called BEFORE any subscriber Handle() invocations. The
// pg adapter runs rls.ApplySession internally; tenant context flows from the
// envelope tenant the subscriber stamps onto ctx.
func (s *ExtServer) WireLearnerProfilePg(repo learner_profile.ProjectionRepo) {
	if repo == nil {
		return
	}
	s.LearnerProfileSub = subscribers.NewLearnerProfileSubscriber(repo)
}

// WireTranscriptPg attaches the StudentTranscript read repo + constructs the
// TranscriptSubscriber over the supplied (pg-backed) student_transcript.
// Repository (W6 Slice 1). Called by cmd/server at boot when a pgx pool is
// available; a no-op when repo is nil (no projection without a DB). MUST be
// called BEFORE wireSubscriberPushHandlers so its LearnerProfilePushDeps.
// Transcript fan-out picks up s.TranscriptSub. The pg adapter runs
// rls.ApplySession internally; tenant context flows from the HTTP middleware
// (the two read routes) and the push dispatcher's tracing.WithTenantID
// (the fanned-out subscriber calls).
func (s *ExtServer) WireTranscriptPg(repo st.Repository) {
	if repo == nil {
		return
	}
	s.Transcript = repo
	s.TranscriptSub = subscribers.NewTranscriptSubscriber(repo)
	// B6 item 2: the same pg repo also satisfies the narrow write port for
	// the unseen-result flag. Bound here from the SAME object deliberately,
	// so the read and the mark can never be live independently of each other.
	// The type assertion is the honest shape: st.Repository does not promise
	// SeenMarker, and an in-memory or test repo that does not implement it
	// leaves the :seen action reporting 503 rather than silently doing
	// nothing.
	if marker, ok := repo.(st.SeenMarker); ok {
		s.TranscriptSeen = marker
	}
}

// WireCourseDirectoryPg constructs the CourseMetadataSubscriber over the supplied
// (pg-backed) CourseDirectoryPort (CHO-2059). Called by cmd/server at boot when a
// pgx pool is available; a no-op when repo is nil (no projection without a DB).
// MUST be called BEFORE wireSubscriberPushHandlers so the course-metadata push
// endpoint mounts. The course_directory table is tenant-agnostic (no RLS), so the
// subscriber writes with a bare context — there is no tenant to stamp.
func (s *ExtServer) WireCourseDirectoryPg(repo course_directory.CourseDirectoryPort) {
	if repo == nil {
		return
	}
	s.CourseMetadataSub = subscribers.NewCourseMetadataSubscriber(repo)
}

// WireAtomIndexPg swaps the in-memory atom_index projection for the supplied
// (pg-backed) Repo and rebuilds every subscriber that reads/writes it
// (CHO-1612 D2 durability): AtomCreated, EnrollmentCreated, SessionCompletion,
// TopicAccuracy, ActivePathTopics. No-op when repo is nil (in-memory retained).
//
// MUST be called BEFORE ShareDosingProjections (which rebuilds the
// TopicAccuracy + ActivePathTopics subs from s.AtomIndex) and BEFORE
// wireSubscriberPushHandlers (which binds AtomCreated/EnrollmentCreated). Per
// multi-tenant-rls the pg adapter runs rls.ApplySession; tenant context is set
// by the dispatchers from the event payload tenant (push has no middleware ctx).
func (s *ExtServer) WireAtomIndexPg(repo atom_index.Repo) {
	if repo == nil {
		return
	}
	s.AtomIndex = repo
	s.AtomCreatedSub = subscribers.NewAtomCreatedSubscriber(repo, s.Paths)
	s.AtomPublishedSub = subscribers.NewAtomPublishedSubscriber(repo)
	// NB: the enrolment bootstrap is NOT rebuilt here any more. It no longer reads
	// atom_index at all — it reads the COURSE-CONTENT projection (CHO-2169), so an
	// atom_index swap is none of its business. Binding it to atom_index here is
	// precisely what shipped a subscriber that could never find a course's atoms.
	s.SessionCompletion = subscribers.NewSessionCompletionHandler(s.Paths, repo, s.Retention, s.Publisher)
	s.TopicAccuracySub = subscribers.NewTopicAccuracySubscriber(s.TopicAccuracyRepo, repo)
	s.ActivePathTopicsSub = subscribers.NewActivePathTopicsSubscriber(s.ActivePathTopicsRepo, repo)
}

// WireLearningPathPg swaps the in-memory LearningPath repo for the supplied
// (pg-backed) Repo so bootstrapped paths survive pod restart and are
// consistent across replicas (R3 durability), rebuilding every subscriber +
// handler that reads/writes paths. No-op when repo is nil (in-memory retained).
//
// MUST be called AFTER WireAtomIndexPg so the rebuilt subscribers bind the
// already-swapped (pg) s.AtomIndex. The /v1/me/learning-paths read handlers
// dereference s.Paths at request time, so updating the field is sufficient
// there. Per multi-tenant-rls the pg adapter runs rls.ApplySession; tenant
// context flows from the HTTP middleware (reads) and the push dispatcher's
// tracing.WithTenantID (cross-domain writes).
func (s *ExtServer) WireLearningPathPg(repo learning_path.Repo) {
	if repo == nil {
		return
	}
	s.Paths = repo
	s.AtomCreatedSub = subscribers.NewAtomCreatedSubscriber(s.AtomIndex, repo)
	s.EnrollmentCreatedSub = subscribers.NewEnrollmentCreatedSubscriber(repo, s.CourseContent, s.Publisher)
	s.SessionCompletion = subscribers.NewSessionCompletionHandler(repo, s.AtomIndex, s.Retention, s.Publisher)
	// CHO-2169 — the content_composed subscriber now BACK-FILLS enrolled learners'
	// paths, so it holds a path repo and must be rebuilt on the swap. Miss this and
	// the projection lands in pg while the back-fill writes to the in-memory repo:
	// green in dev, silently inert in prod. That is exactly how the dose's curiosity
	// slot stayed dead for months (CHO-2167).
	s.CourseContentSub = subscribers.NewCourseContentComposedSubscriber(s.CourseContent, repo)
}

// WireTopicRetentionPg swaps the in-memory TopicRetention repo for the supplied
// (pg-backed, RLS-bound) Repository so the per-(tenant, gcid, topic) Ebbinghaus
// score survives pod restarts and is cross-tenant-safe (CHO-1949 — before this
// it was the only learner-state left purely in-memory). Rebuilds the
// SessionCompletion handler so its retention write-through reaches the swapped
// repo. No-op when repo is nil (the in-memory adapter from NewExtServer is
// retained for tests / dev without a database).
//
// MUST be called AFTER WireAtomIndexPg + WireLearningPathPg so the rebuilt
// SessionCompletion binds the already-pg AtomIndex + LearningPath. Per
// multi-tenant-rls the pg adapter runs rls.ApplySession; tenant context flows
// from the HTTP middleware (the /v1/me/topic-retention read) and the
// completion call-site's tracing.WithTenantID wrap (me_handlers submitMeAnswer).
func (s *ExtServer) WireTopicRetentionPg(repo topic_retention.Repository) {
	if repo == nil {
		return
	}
	s.Retention = repo
	s.SessionCompletion = subscribers.NewSessionCompletionHandler(s.Paths, s.AtomIndex, repo, s.Publisher)
}

// Routes builds the http.Handler with EXT-scope routes + middleware.
//
// Note: the standard library's net/http ServeMux uses longest-prefix
// matching, so registering both `/learning-paths` (exact) and
// `/learning-paths/` (subtree) is fine and unambiguous. The same pattern
// is used for `/sessions`.
func (s *ExtServer) Routes() *extMux {
	mux := newExtMux()

	// LearningPath.
	mux.Handle("/learning-paths", s.handleLearningPaths)
	mux.Handle("/learning-paths/", s.handleLearningPathByID)

	// AtomAttempt. Registered through withExtRLSContext: the pg
	// atomic_sessions repo is RLS-bound (CHO-2029) and rejects a bare
	// request ctx — without the seam every start/submit 500s
	// (SESSION_PERSIST_FAILED) while inmem-backed units stay green.
	mux.Handle("/sessions", withExtRLSContext(s.handleSessions))
	mux.Handle("/sessions/", withExtRLSContext(s.handleSessionByID))

	// Knowledge Graph reads.
	mux.Handle("/knowledge-graph/discover", s.handleDiscover)
	mux.Handle("/me/knowledge-graph/heatmap", s.handleHeatmap)

	// S4.2 — /v1/me/* surface area. atom-sessions carry the RLS seam
	// (same CHO-2029 rationale as /sessions above).
	mux.Handle("/v1/me/atom-sessions", withExtRLSContext(s.handleMeAtomSessions))
	mux.Handle("/v1/me/atom-sessions/", withExtRLSContext(s.handleMeAtomSessionsByID))
	mux.Handle("/v1/me/learning-paths", s.handleMeLearningPaths)
	mux.Handle("/v1/me/learning-paths/", s.handleMeLearningPathByID)
	mux.Handle("/v1/me/topic-retention", s.handleMeTopicRetention)

	// W6 Slice 1 (Four-Mode plan "Outcome spine") — StudentTranscript reads.
	// me/transcript is the learner-self view (gcid from AuthCtx, like
	// topic-retention above); by-assessments is the tenant-scoped,
	// instructor/admin role-gated cross-learner view (the R+ per-offering
	// gradebook join) — deliberately NOT under /v1/me/*.
	// B6 item 3: the learner's remaining practice taps for the current UTC day.
	mux.Handle("/v1/me/practice-budget", s.handleMePracticeBudget)
	mux.Handle("/v1/me/transcript", s.handleMeTranscript)
	// B6 item 2: POST /v1/me/transcript/{entryID}:seen. Subtree mount, more
	// specific than the leaf above, so ServeMux longest-match routes the
	// action here and leaves the list untouched.
	mux.Handle("/v1/me/transcript/", s.handleMeTranscriptEntryAction)
	mux.Handle("/v1/transcript/by-assessments", s.handleTranscriptByAssessments)

	// Epic-1b W8a — Growth-Edges read API (list + by-id read/dismiss). The
	// uploads producer + poll (.../uploads, .../uploads/{id}) land in W8b as
	// more-specific mounts (ServeMux longest-match wins over the by-id subtree).
	mux.Handle("/v1/me/growth-edges", s.handleMeGrowthEdges)
	mux.Handle("/v1/me/growth-edges/", s.handleMeGrowthEdgeByID)
	// W8b-2 upload PRODUCER — more-specific than the by-id subtree above
	// (ServeMux longest-match routes /uploads + /uploads/{id} here).
	// B6 item 1: the mount is shared. GET serves the parked-review list, every
	// other method falls through to the POST producer.
	mux.Handle("/v1/me/growth-edges/uploads", s.handleMeGrowthEdgeUploadsDispatch)
	mux.Handle("/v1/me/growth-edges/uploads/", s.handleMeGrowthEdgeUploadByID)

	// ADR-204 §2 — learner-owned Goal aggregate (CRUD). The collection handler
	// owns POST (create) + GET (list, with derived primaryLens); the by-id
	// handler owns PATCH (status / companion attach-detach). ServeMux
	// longest-prefix routes the subtree to the by-id handler.
	mux.Handle("/v1/me/goals", s.handleMeGoals)
	mux.Handle("/v1/me/goals/", s.handleMeGoalByID)

	// My Knowledge map read-model (WS-A2, CHO-2005). A map = a Goal (ADR-214 D1):
	// its graph is the hierarchy sub-tree under Goal.RootConceptID. The collection
	// lists the learner's maps + per-map counts; the by-id subtree serves
	// /{goalId}/graph — the root-scoped painted subgraph (reuses the A1 overlay).
	// ServeMux longest-prefix routes /{goalId}/graph to the by-id handler.
	mux.Handle("/v1/me/maps", s.handleMeMaps)
	mux.Handle("/v1/me/maps/", s.handleMeMapByID)

	// ADR-212 WS-2/WS-3 (CHO-1995) — learner-sovereign concept graph. Registered
	// as EXACT paths (no trailing slash) so extMux exact-match dispatches them;
	// /v1/me/companions/acquire is exact too, so all OTHER /v1/me/companions/*
	// still fall through to the legacy Server (which owns that subtree).
	mux.Handle("/v1/me/concept-graph", s.handleMeConceptGraph)
	mux.Handle("/v1/me/concept-graph/reroot", s.handleMeConceptGraphReroot)
	mux.Handle("/v1/me/companions/acquire", s.handleMeCompanionAcquire)

	// ADR-212 D1/D2 (CJ buildout) — learner AUTHORING surface. Collection paths
	// are exact (POST create); the by-id subtrees are trailing-slash prefixes
	// (PATCH/DELETE) — extMux longest-prefix routes {id} there without shadowing
	// the exact list/reroot/concepts/edges collection paths.
	// ENROLMENT-entitled candidate source for the attach picker (ADR-229 scoped).
	// Under /concept-graph so the gateway subtree proxy + Istio authz wildcard
	// already admit it: no new edge wiring, no new mesh rule.
	mux.Handle("/v1/me/concept-graph/attachable-atoms", s.handleMeAttachableAtoms)
	mux.Handle("/v1/me/concept-graph/concepts", s.handleMeConceptCreate)
	mux.Handle("/v1/me/concept-graph/concepts/", s.handleMeConceptByID)
	mux.Handle("/v1/me/concept-graph/edges", s.handleMeEdgeCreate)
	mux.Handle("/v1/me/concept-graph/edges/", s.handleMeEdgeByID)
	// ADR-212 WS-4 — Companion suggestion curation. List (exact) + generate (exact,
	// publishes the requested event) + Accept/Dismiss under the by-id prefix
	// ({id}/accept|dismiss). `generate` is registered EXACT so extMux's exact-match
	// wins over the by-id prefix (it is not a suggestion id). Nested under
	// /concept-graph so the gateway subtree proxy + Istio authz wildcard admit them.
	mux.Handle("/v1/me/concept-graph/suggestions", s.handleMeSuggestions)
	mux.Handle("/v1/me/concept-graph/suggestions/generate", s.handleMeSuggestionGenerate)
	mux.Handle("/v1/me/concept-graph/suggestions/", s.handleMeSuggestionByID)
	// ADR-212 D5 (CJ buildout) — list the learner's per-map Companion bindings.
	mux.Handle("/v1/me/companions/bindings", s.handleMeCompanionBindings)

	// CHO-1612 — heterogeneous course curriculum (read projection).
	// GET /v1/me/courses/{course_id}/content
	mux.Handle("/v1/me/courses/", s.handleMeCourseContent)

	// S5.2 — Per-User Knowledge Graph (ADR-143). Every KG route is
	// registered through withExtRLSContext: the pg KG repos are RLS-bound
	// and reject a bare request ctx, so the identity wrap happens ONCE at
	// this seam for every current and future KG handler.
	mux.Handle("/v1/me/knowledge-graph/clusters", withExtRLSContext(s.handleKGClusters))

	// E2E-BE-KG-CANVAS (2026-05-16) — FE-facing camelCase + envelope
	// canvas endpoints. The canvas dispatcher delegates to the legacy
	// handleKGClusterByID / handleKGJunctionByID for paths it doesn't
	// own (e.g. v1.0 snake_case .../focal/{atom_id}/fog routes).
	//
	// Mount the canvas dispatchers AT the existing v1.0 prefixes so the
	// mux's longest-prefix matching routes everything through them; the
	// dispatchers themselves fall back to the legacy handlers as needed.
	mux.Handle("/v1/me/knowledge-graph/clusters/", withExtRLSContext(s.handleCanvasClusterSubpath))
	mux.Handle("/v1/me/knowledge-graph/junctions/", withExtRLSContext(s.handleCanvasJunctionSubpath))
	mux.Handle("/v1/tenants/", withExtRLSContext(s.handleCanvasTenantSubpath))

	return mux
}

// HTTPHandler returns an http.Handler with OTel middleware wrapped
// around the EXT mux. cmd/server uses this in production; tests can
// invoke Routes() directly to bypass the middleware noise.
func (s *ExtServer) HTTPHandler() http.Handler {
	return OTelMiddleware(s.Routes())
}
