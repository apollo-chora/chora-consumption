// weakness_analyzed_subscriber.go — consumes chora.consumption.weakness.analyzed.v1
// (emitted by the ai-kernel weakness-analyser crew) and upserts the extracted
// Growth Edges into the LearnerWeakness aggregate as the EXPLICIT source.
//
// Per ddd-enforcement chora-consumption never reads chora_ai_kernel — this is
// the event seam. The repo's Upsert applies the cos>=0.9 embedding dedup fold;
// this subscriber embeds each concept label (RETRIEVAL_DOCUMENT) and hands the
// vector in.
//
// Idempotency: keyed on (tenant, gcid, upload_id) via idempotent.Store.Process —
// the dedup key is COMMITTED ONLY when the whole analysis upserts successfully,
// so a transient embed / pg failure leaves the key unclaimed and Pub/Sub
// redelivery reprocesses (no lost analysis). A redelivered OR re-analysed event
// for the same upload is skipped. Upsert is merge-idempotent, so the rare
// concurrent-duplicate reprocess is harmless.
package subscribers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

// ExtractedGrowthEdgePayload mirrors one repeated edge of the Protobuf-encoded
// chora.consumption.weakness.analyzed.v1 body (decoded at the push handler).
type ExtractedGrowthEdgePayload struct {
	ConceptLabel   string
	ConceptKey     string
	Category       string
	Tags           []string
	Confidence     float64
	Strength       float64
	DescriptorJSON string
}

// WeaknessAnalyzedPayload mirrors the chora.consumption.weakness.analyzed.v1 body.
type WeaknessAnalyzedPayload struct {
	UploadID    string
	TenantID    string
	LearnerGCID string
	ModelUsed   string
	Edges       []ExtractedGrowthEdgePayload
	// CompanionCoaching is the learner's post-HITL output_selection.familiar_coaching
	// flag (ADR-205 D5 / CHO-1966), decoded from the event's OutputSelection. When
	// true the subscriber writes the diagnosis into the learner's Companion pgvector
	// memory (ADR-173). Absent/false on the live single-shot path ⇒ no write.
	CompanionCoaching bool
}

// JobCompleter marks the originating weakness_doc_uploads job COMPLETED with the
// upserted Growth-Edge ids (W8b-3). Optional — nil ⇒ no upload-job to update
// (e.g. derived/classroom sources, or the W3-decode tests). The pg
// weakness_upload.Repository satisfies it structurally.
type JobCompleter interface {
	MarkCompleted(ctx context.Context, learnerGCID, uploadID string, edgeIDs []string, now time.Time) error
}

// BlobShredHook crypto-shreds the raw upload blob on diagnosis-complete (ADR-205
// D8 / WS-5): tombstone the wrapped per-blob DEK + delete the GCS ciphertext +
// record the audit marker, and (best-effort) sweep the tenant's expired blobs.
// It is composed in cmd/server from the weakness_blob Shredder + the upload repo
// + the TTL Sweeper. nil ⇒ no shred (DARK; the envelope path is off). Fail-loud:
// a non-nil error NACKs the analysis so Pub/Sub redelivers (shred is idempotent).
type BlobShredHook func(ctx context.Context, tenantID, gcid, uploadID string) error

// CompanionRAGHook writes the analysed diagnosis into the learner's Companion
// pgvector memory (ADR-205 D5 / ADR-173, CHO-1966) — invoked ONLY when the
// learner opted into familiar_coaching at the bounded HITL review
// (p.CompanionCoaching). Built in cmd/server from the Companion resolver +
// embedder + memory ports (see NewCompanionRAGHook). nil ⇒ no coaching write
// (DARK; the WEAKNESS_COMPANION_RAG_ENABLED flag is off). SOFT-FAIL: per the
// CompanionMemory port contract this coaching memory is a best-effort
// enhancement, so the subscriber logs a non-nil error LOUDLY but never NACKs the
// analysis (the diagnosis edges already persisted) — the inbox commits once, so
// no duplicate memory on redelivery.
type CompanionRAGHook func(ctx context.Context, p WeaknessAnalyzedPayload) error

// UploadScopeLookup resolves an upload id to the ADR-238 scope it was taken
// under (goal bounds the candidates, entry concept only biases them - see
// weakness_upload.Scope). A zero Scope = a non-goal upload (nothing to scope
// resolution to). The pg weakness_upload.Repository satisfies it structurally
// via ScopeForUpload.
type UploadScopeLookup interface {
	ScopeForUpload(ctx context.Context, learnerGCID, uploadID string) (wu.Scope, error)
}

// WeaknessAnalyzedSubscriber projects analysed Growth Edges (source=explicit)
// into the LearnerWeakness aggregate.
type WeaknessAnalyzedSubscriber struct {
	repo             lw.Repository
	embedder         lw.Embedder
	inbox            idempotent.Store
	ttl              time.Duration
	jobCompleter     JobCompleter     // optional; set via WithJobCompleter
	blobShredHook    BlobShredHook    // optional; set via WithBlobShredHook (WS-5)
	companionRAGHook CompanionRAGHook // optional; set via WithCompanionRAGHook (CHO-1966)
	// ADR-238 M-D2 (goal-scoped Diagnose): when both are set, each analysed edge
	// is resolved to its goal sub-tree's nearest on-map ConceptNode and, on a
	// confident match, stamped with TargetConceptID + the node's ConceptKey.
	// Resolution is a best-effort ENHANCEMENT — any lookup/resolver error is
	// logged loudly but never NACKs (the edges still persist goal-level).
	goalResolver GoalConceptResolver // optional; set via WithGoalConceptResolver
	uploadLookup UploadScopeLookup   // optional; set via WithGoalConceptResolver
	minCosine    float64             // match floor; set via WithConceptMatchMinCosine
	// tieBand is ADR-238 D2's soft-bias width: a candidate inside the ENTRY
	// concept's sub-tree wins only when it is within this much cosine of the
	// argmax. 0 (the zero value, and the kill-switch) disables the bias, leaving
	// pre-D2 pure-argmax behaviour. Set via WithConceptMatchTieBand.
	tieBand float64
	// ADR-238 D4: an UNMATCHED weakness must be SURFACED, not swallowed. The
	// edge is parked goal-level either way, but a goal-level edge paints on no
	// node and WS-4 retired the only browsable edge list, so without this the
	// weakness surfaces nowhere at all.
	suggestions ConceptSuggestionWriter // optional; set via WithConceptSuggestions
}

// ConceptSuggestionWriter is the D4 surfacing port: offer the learner the
// concept an unmatched weakness implies, for them to accept onto their map.
// Deliberately narrow (ADR-212 keeps the map learner-sovereign, so this SUGGESTS
// and never auto-adds). GetBySourceEvent is the idempotent-ingest probe (migration
// 0060): the subscriber's inbox is per-pod in-memory, so a redelivery to a fresh
// pod re-runs the handler, and the suggestion insert is not itself idempotent
// (fresh id, PLAIN source_event index), so the write is guarded by a probe on the
// event id. The pg SuggestionRepo satisfies both methods structurally.
type ConceptSuggestionWriter interface {
	CreateBatch(ctx context.Context, ss []*conceptgraph.Suggestion) error
	GetBySourceEvent(ctx context.Context, tenantID, learnerGCID, sourceEventID string) (*conceptgraph.Suggestion, error)
}

// WithConceptSuggestions wires the ADR-238 D4 surfacing.
func (s *WeaknessAnalyzedSubscriber) WithConceptSuggestions(w ConceptSuggestionWriter) *WeaknessAnalyzedSubscriber {
	s.suggestions = w
	return s
}

// NewWeaknessAnalyzedSubscriber constructs the subscriber with an in-memory
// inbox (production wires a PostgresStore via …WithStore for cross-pod dedup).
func NewWeaknessAnalyzedSubscriber(repo lw.Repository, embedder lw.Embedder) *WeaknessAnalyzedSubscriber {
	return NewWeaknessAnalyzedSubscriberWithStore(repo, embedder, idempotent.NewMemoryStore(), inboxTTL)
}

// NewWeaknessAnalyzedSubscriberWithStore allows injecting a durable inbox + TTL.
func NewWeaknessAnalyzedSubscriberWithStore(repo lw.Repository, embedder lw.Embedder, store idempotent.Store, ttl time.Duration) *WeaknessAnalyzedSubscriber {
	if store == nil {
		store = idempotent.NewMemoryStore()
	}
	if ttl <= 0 {
		ttl = inboxTTL
	}
	return &WeaknessAnalyzedSubscriber{repo: repo, embedder: embedder, inbox: store, ttl: ttl}
}

// WithJobCompleter attaches the upload-job completer (W8b-3) so a successful
// analysis flips the weakness_doc_uploads row to COMPLETED. Builder-style;
// returns the receiver. nil-safe (passing nil leaves the path off).
func (s *WeaknessAnalyzedSubscriber) WithJobCompleter(c JobCompleter) *WeaknessAnalyzedSubscriber {
	s.jobCompleter = c
	return s
}

// WithBlobShredHook attaches the WS-5 crypto-shred hook (ADR-205 D8): a
// successful analysis crypto-shreds the raw upload blob. Builder-style; nil-safe
// (passing nil leaves the shred off — DARK until the envelope path is wired).
func (s *WeaknessAnalyzedSubscriber) WithBlobShredHook(h BlobShredHook) *WeaknessAnalyzedSubscriber {
	s.blobShredHook = h
	return s
}

// WithCompanionRAGHook attaches the ADR-205 D5 Companion-RAG coaching hook
// (CHO-1966): on a completed analysis where the learner opted into
// familiar_coaching, the diagnosis is written into their Companion's pgvector
// memory. Builder-style; nil-safe (passing nil leaves coaching off — DARK until
// WEAKNESS_COMPANION_RAG_ENABLED is set).
func (s *WeaknessAnalyzedSubscriber) WithCompanionRAGHook(h CompanionRAGHook) *WeaknessAnalyzedSubscriber {
	s.companionRAGHook = h
	return s
}

// WithGoalConceptResolver attaches the ADR-238 M-D2 goal-scoped concept resolver
// and the upload→goal lookup it needs (they are a pair — resolution can't happen
// without knowing the upload's goal). Builder-style; nil-safe: leaving EITHER nil
// leaves resolution off, so each edge persists exactly as the analyser produced
// it (ConceptKey unchanged, TargetConceptID "").
func (s *WeaknessAnalyzedSubscriber) WithGoalConceptResolver(r GoalConceptResolver, lookup UploadScopeLookup) *WeaknessAnalyzedSubscriber {
	s.goalResolver = r
	s.uploadLookup = lookup
	return s
}

// WithConceptMatchTieBand sets ADR-238 D2's soft-bias width - how close to the
// cosine argmax a candidate inside the ENTRY concept's sub-tree must be to win
// the tie. Builder-style. 0 disables the bias (kill-switch), restoring pre-D2
// pure argmax. The bias is applied AFTER the floor, so no value of this knob can
// change whether an edge matches - only which concept it lands on.
func (s *WeaknessAnalyzedSubscriber) WithConceptMatchTieBand(f float64) *WeaknessAnalyzedSubscriber {
	s.tieBand = f
	return s
}

// WithConceptMatchMinCosine sets the cosine floor a non-exact candidate must
// clear to resolve an edge (below it the edge stays goal-level). Builder-style.
// The safe default is applied by the composition root (env-tunable); the zero
// value here is only reached when resolution is not wired.
func (s *WeaknessAnalyzedSubscriber) WithConceptMatchMinCosine(f float64) *WeaknessAnalyzedSubscriber {
	s.minCosine = f
	return s
}

// Handle upserts each extracted Growth Edge for the upload. The whole upload is
// one idempotent unit — the dedup key commits only on full success.
func (s *WeaknessAnalyzedSubscriber) Handle(ctx context.Context, env events.Envelope, p WeaknessAnalyzedPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	if strings.TrimSpace(p.UploadID) == "" {
		return errors.New("subscribers: weakness.analyzed payload missing upload_id")
	}
	key := "weakness.analyzed:" + p.TenantID + "|" + p.LearnerGCID + "|" + p.UploadID
	return s.inbox.Process(ctx, key, s.ttl, func() error {
		return s.upsertEdges(ctx, env, p)
	})
}

func (s *WeaknessAnalyzedSubscriber) upsertEdges(ctx context.Context, env events.Envelope, p WeaknessAnalyzedPayload) error {
	// Tenant + GCID on ctx so the pg repo's rls.ApplySession scopes correctly.
	ctx = tracing.WithGCID(tracing.WithTenantID(ctx, p.TenantID), p.LearnerGCID)

	// ADR-238 M-D2: resolve the upload's goal to its on-map concept candidates
	// ONCE, before the edge loop. This is an ENHANCEMENT — a lookup/resolver
	// failure is logged LOUDLY but leaves candidates nil (every edge then
	// persists goal-level), NEVER a NACK (mirrors the CompanionRAGHook soft-fail).
	var candidates []ConceptCandidate
	// The goal's root concept: where an ADR-238 D4 suggestion is anchored.
	rootConceptID := ""
	// Labels of edges that bound to NO on-map concept (D4 subjects).
	var unmatchedLabels []string
	if s.goalResolver != nil && s.uploadLookup != nil {
		up, err := s.uploadLookup.ScopeForUpload(ctx, p.LearnerGCID, p.UploadID)
		if err != nil {
			log.Printf("subscribers: goal-scoped resolve - upload→scope lookup failed (non-fatal) for upload %q: %v", p.UploadID, err)
		} else if goalID := strings.TrimSpace(up.GoalID); goalID != "" {
			// EntryConceptID is the D2 soft hint; "" (a goal-level entry) simply
			// leaves every candidate unbiased.
			scope, err := s.goalResolver.Resolve(ctx, p.TenantID, p.LearnerGCID, goalID, up.EntryConceptID)
			if err != nil {
				log.Printf("subscribers: goal-scoped resolve — candidate build failed (non-fatal) for goal %q: %v", goalID, err)
			} else {
				candidates = scope.Candidates
				rootConceptID = scope.RootConceptID
			}
		}
	}

	edgeIDs := make([]string, 0, len(p.Edges))
	for _, edge := range p.Edges {
		// Skip un-keyable edges (the analyser already drops these, but be
		// defensive so one bad edge can't NACK-loop the whole upload).
		keyHint := edge.ConceptKey
		if strings.TrimSpace(keyHint) == "" {
			keyHint = edge.ConceptLabel
		}
		if lw.NormalizeConceptKey(keyHint) == "" {
			continue
		}

		embedding, err := s.embedder.Embed(ctx, edge.ConceptLabel, p.TenantID)
		if err != nil {
			return fmt.Errorf("subscribers: embed weakness concept %q: %w", edge.ConceptKey, err)
		}

		// ADR-238 M-D2: default to the analyser's own key + no target; on a
		// confident goal-scoped match, stamp the resolved node id AND align the
		// concept_key so both the id-join AND the slug-join light the right node.
		// An unmatched edge is left exactly as the analyser produced it.
		conceptKey := edge.ConceptKey
		targetID := ""
		if len(candidates) > 0 {
			if cid, ckey, ok := matchConcept(edge.ConceptKey, embedding, candidates, s.minCosine, s.tieBand); ok {
				targetID = cid
				conceptKey = ckey
			}
		}
		if targetID == "" {
			// Bound to nothing on the map ⇒ a D4 subject. Collected here and
			// written once after the loop, so one analysis is one batch.
			if lbl := strings.TrimSpace(edge.ConceptLabel); lbl != "" {
				unmatchedLabels = append(unmatchedLabels, lbl)
			}
		}

		res, err := s.repo.Upsert(ctx, lw.UpsertInput{
			TenantID:        p.TenantID,
			LearnerGCID:     p.LearnerGCID,
			ConceptKey:      conceptKey,
			ConceptLabel:    edge.ConceptLabel,
			Embedding:       embedding,
			Category:        edge.Category,
			Tags:            edge.Tags,
			Strength:        edge.Strength,
			Source:          lw.SourceExplicit,
			TargetConceptID: targetID,
			Descriptor:      parseEdgeDescriptor(edge.DescriptorJSON),
			Now:             env.OccurredAt,
		})
		if err != nil {
			return fmt.Errorf("subscribers: upsert weakness %q: %w", edge.ConceptKey, err)
		}
		if res.ID != "" {
			edgeIDs = append(edgeIDs, res.ID)
		}
	}

	// W8b-3: flip the originating upload job to COMPLETED (with the upserted edge
	// ids). Runs inside the same inbox-idempotent unit as the upserts, so a
	// failure NACKs the whole analysis for redelivery (MarkCompleted is itself
	// idempotent). Empty edges (nothing weak) still completes the job. Skipped
	// when no completer is wired (derived/classroom sources have no upload job).
	if s.jobCompleter != nil {
		if err := s.jobCompleter.MarkCompleted(ctx, p.LearnerGCID, p.UploadID, edgeIDs, env.OccurredAt); err != nil {
			return fmt.Errorf("subscribers: mark upload job %q completed: %w", p.UploadID, err)
		}
	}

	// ADR-238 D4: an unmatched weakness is SURFACED, not swallowed. Each edge
	// that bound to no on-map concept becomes a PENDING concept suggestion the
	// learner may accept onto their map (never auto-added — ADR-212 keeps the map
	// learner-sovereign).
	//
	// Anchored at the goal ROOT, never whole-map: concept_suggestion_handler's
	// goal fence keeps only rows whose focal is inside the goal subtree, and a
	// whole-map row is in NO subtree, so it is dropped on every goal-scoped read.
	// An empty focal here would be written and then filtered out forever, which
	// is precisely the invisibility D4 exists to end.
	//
	// FAIL-LOUD on write failure. Swallowing it would leave the learner with no
	// surface AND no signal, which is the D4 failure mode itself; the upserts are
	// idempotent so redelivery is safe.
	if s.suggestions != nil && rootConceptID != "" && len(unmatchedLabels) > 0 {
		// Idempotent ingest (migration 0060; the same probe the sibling
		// ConceptSuggestionEmittedSubscriber uses). The inbox is per-pod in-memory,
		// so a redelivery to a fresh pod (or after the local key's TTL) re-runs
		// this. The edge upserts fold idempotently, but a suggestion insert does not
		// (fresh id, PLAIN source_event index), so probe on the event id first: a
		// live suggestion for this event means the batch already landed, so skip the
		// re-write instead of duplicating the learner's "add this concept?" cards.
		// A probe READ failure is fail-loud (NACK) rather than writing blind.
		existing, err := s.suggestions.GetBySourceEvent(ctx, p.TenantID, p.LearnerGCID, env.EventID)
		if err != nil {
			return fmt.Errorf("subscribers: probe D4 concept suggestions for event %q: %w", env.EventID, err)
		}
		if existing == nil {
			sugs := make([]*conceptgraph.Suggestion, 0, len(unmatchedLabels))
			for _, label := range unmatchedLabels {
				sug, err := conceptgraph.NewConceptSuggestion(conceptgraph.NewConceptSuggestionInput{
					TenantID:       p.TenantID,
					LearnerGCID:    p.LearnerGCID,
					Title:          label,
					Rationale:      d4Rationale(label),
					SourceEventID:  env.EventID,
					FocalConceptID: rootConceptID,
					Now:            env.OccurredAt,
				})
				if err != nil {
					return fmt.Errorf("subscribers: build D4 concept suggestion %q: %w", label, err)
				}
				sugs = append(sugs, sug)
			}
			if err := s.suggestions.CreateBatch(ctx, sugs); err != nil {
				return fmt.Errorf("subscribers: write %d D4 concept suggestion(s): %w", len(sugs), err)
			}
		}
	}

	// WS-5 (ADR-205 D8): the diagnosis is done, so the raw upload blob is no
	// longer needed — crypto-shred it (delete the wrapped DEK + the GCS
	// ciphertext). Runs inside the same idempotent unit as the upserts, so a
	// shred failure NACKs the whole analysis for redelivery (the shred is
	// idempotent). nil hook ⇒ DARK (envelope path off) ⇒ today's behaviour.
	if s.blobShredHook != nil {
		if err := s.blobShredHook(ctx, p.TenantID, p.LearnerGCID, p.UploadID); err != nil {
			return fmt.Errorf("subscribers: crypto-shred upload blob %q: %w", p.UploadID, err)
		}
	}

	// ADR-205 D5 (CHO-1966): when the learner opted into familiar_coaching at the
	// bounded HITL review, write the diagnosis into their Companion's pgvector
	// memory (ADR-173). Runs LAST, only after the load-bearing edges/job/shred
	// succeeded. SOFT-FAIL by the CompanionMemory port contract — coaching memory
	// is a best-effort enhancement, so a failure is logged loudly but NEVER NACKs
	// the analysis (the edges already persisted), and the inbox commits once so a
	// redelivery cannot duplicate the memory. DARK: nil hook (flag off) or
	// familiar_coaching not opted in ⇒ no write.
	if s.companionRAGHook != nil && p.CompanionCoaching {
		if err := s.companionRAGHook(ctx, p); err != nil {
			log.Printf("subscribers: companion-RAG coaching write failed (non-fatal) for upload %q: %v", p.UploadID, err)
		}
	}
	return nil
}

// parseEdgeDescriptor decodes the analyser's descriptor_json into the aggregate
// Descriptor; malformed / empty JSON yields an empty descriptor (fail-soft).
func parseEdgeDescriptor(descriptorJSON string) lw.Descriptor {
	var d lw.Descriptor
	if strings.TrimSpace(descriptorJSON) == "" {
		return d
	}
	_ = json.Unmarshal([]byte(descriptorJSON), &d)
	return d
}

// d4Rationale is the learner-facing "why" on an ADR-238 D4 suggestion (ADR-215
// explainability): plain language, no jargon, and honest that the concept is
// absent from THIS map rather than implying the diagnosis failed.
func d4Rationale(label string) string {
	return "Your upload showed a weak spot in " + label + ", which isn't on this map yet. Add it to track your progress there."
}
