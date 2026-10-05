// Package subscribers wires the cross-domain Pub/Sub subscribers in
// chora-consumption.
//
// Per ddd-enforcement HARD RULE chora-consumption never queries
// chora_creation OR chora_delivery directly. Subscribers populate local
// projections so reads stay inside chora_consumption DB.
//
// Subscribers landed (S4.2):
//
//   - AtomCreatedSubscriber  consumes chora.creation.atom.created.v1
//     and upserts the local atom_index projection (skinny — no full
//     content). Enables server-side MCQ grading + topic_retention
//     attribution.
//
//   - EnrollmentCreatedSubscriber consumes
//     chora.delivery.enrollment.created.v1 and bootstraps a Straight-Up
//     LearningPath for (course_id, learner_gcid). Idempotent — duplicate
//     event_ids return successfully without reissuing the path. Emits
//     `chora.consumption.learning_path.bootstrapped.v1` on first bootstrap.
//
// Production wiring (M12+) replaces in-memory delivery with a Cloud
// Pub/Sub Push subscriber that decodes the Protobuf payload + verifies the
// envelope mandatory fields against the Schema Registry.
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/course_content"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// inboxTTL is the default dedupe-key retention window. 24h covers Pub/Sub
// max redelivery for cross-domain events. Production may tune per
// subscriber via newIdempotencyTrackerWithStore.
const inboxTTL = 24 * time.Hour

// ----------------- shared envelope validation -----------------

// validateInboundEnvelope checks the mandatory fields per
// .claude/rules/ddd-enforcement.md. Subscribers reject events whose
// envelope is incomplete (consistent with the producer-side
// validateEnvelope in adapter/events/publisher.go).
func validateInboundEnvelope(env events.Envelope) error {
	if env.EventID == "" {
		return errors.New("subscribers: envelope.event_id required")
	}
	if env.IdempotencyKey == "" {
		return errors.New("subscribers: envelope.idempotency_key required")
	}
	if env.TenantID == "" {
		return errors.New("subscribers: envelope.tenant_id required")
	}
	if env.OccurredAt.IsZero() {
		return errors.New("subscribers: envelope.occurred_at required")
	}
	if env.Traceparent == "" {
		return errors.New("subscribers: envelope.traceparent required (W3C trace)")
	}
	if env.SourceProject == "" {
		return errors.New("subscribers: envelope.source_project required")
	}
	if env.SourceService == "" {
		return errors.New("subscribers: envelope.source_service required")
	}
	if env.SchemaVersion < 1 {
		return errors.New("subscribers: envelope.schema_version >= 1 required")
	}
	return nil
}

// idempotencyTracker records seen event_ids for at-least-once Pub/Sub
// retry handling.
//
// W1.8 (2026-05-12): the in-process `map[string]time.Time` + manual GC has
// been swapped for `libs/chora-go-common/idempotent.Store`. The wrapper
// preserves the original markSeen(id) bool API while gaining:
//   - race-safe seen+mark via Store.Process (no Seen-then-Mark window)
//   - PostgresStore swap-in for production (shared across replicas, survives
//     pod-death — the previous in-process map did NOT)
//   - TTL-based cleanup via Cloud Run Job (no per-insert GC)
//
// Production wires PostgresStore against chora_consumption idempotency_keys.
// Dev / tests use MemoryStore which is goroutine-safe within a single
// process.
type idempotencyTracker struct {
	store idempotent.Store
	ttl   time.Duration
}

// newIdempotencyTracker constructs a tracker backed by an in-memory Store.
// Production wiring SHOULD call newIdempotencyTrackerWithStore to inject a
// PostgresStore so dedup survives pod restart + works across replicas.
func newIdempotencyTracker() *idempotencyTracker {
	return newIdempotencyTrackerWithStore(idempotent.NewMemoryStore(), inboxTTL)
}

// newIdempotencyTrackerWithStore constructs a tracker around the supplied
// Store + TTL. nil store triggers a MemoryStore fallback; non-positive ttl
// defaults to inboxTTL.
func newIdempotencyTrackerWithStore(store idempotent.Store, ttl time.Duration) *idempotencyTracker {
	if store == nil {
		store = idempotent.NewMemoryStore()
	}
	if ttl <= 0 {
		ttl = inboxTTL
	}
	return &idempotencyTracker{store: store, ttl: ttl}
}

// markSeen returns true the FIRST time this eventID is observed (within
// TTL) and false on every subsequent call. Race-safe across goroutines and
// across replicas when backed by PostgresStore.
//
// Empty eventID short-circuits to true (no dedup possible without a key —
// acceptable for raw fixtures that pre-date the envelope contract).
func (t *idempotencyTracker) markSeen(eventID string) bool {
	if eventID == "" {
		return true
	}
	ran := false
	err := t.store.Process(context.Background(), "envID:"+eventID, t.ttl, func() error {
		ran = true
		return nil
	})
	if err != nil {
		// Store error → conservative no-op (treat as already-seen so we
		// don't double-process on transient infra failures). Production
		// surfaces this via Cloud Logging; tests do not exercise this
		// path because MemoryStore.Process never errors on non-empty key.
		return false
	}
	return ran
}

// seen reports whether eventID has already been fully processed (within TTL)
// WITHOUT claiming the key — the non-marking read half of the
// process-then-mark discipline (CHO-2107): check seen at entry, process, and
// only mark(...) after the side effect succeeded. markSeen's claim-first
// semantics burned the dedupe key BEFORE processing, so a failure after the
// claim swallowed the Pub/Sub redelivery as a duplicate and lost the event
// for the tracker's lifetime.
//
// Shares markSeen's "envID:" key-space, so seen/mark interoperate with
// markSeen against the same Store. Race-safety is delegated to the Store
// (MemoryStore mutex / PostgresStore SQL); the wrapper holds no state.
//
// Empty eventID reports false (no dedup possible without a key). A Store
// read error also reports false — bias toward REPROCESSING: the durable
// per-path DB anchor (e.g. companion_growth_events UNIQUE (companion_id,
// source, idempotency_key)) absorbs a duplicate, while skipping would lose
// the event.
func (t *idempotencyTracker) seen(eventID string) bool {
	if eventID == "" {
		return false
	}
	ok, err := t.store.Seen(context.Background(), "envID:"+eventID)
	if err != nil {
		return false
	}
	return ok
}

// mark records eventID as fully processed (within TTL). Call ONLY after the
// handler's side effect durably succeeded. Best-effort by design: the side
// effect has already committed, so failing the handler on a Store write
// error would NACK a successfully processed event; leaving the key unmarked
// merely lets a duplicate delivery re-process, which the durable DB anchor
// dedupes — duplicates are absorbed, loss is impossible.
func (t *idempotencyTracker) mark(eventID string) {
	if eventID == "" {
		return
	}
	_ = t.store.Mark(context.Background(), "envID:"+eventID, t.ttl)
}

// ----------------- AtomCreated subscriber -----------------

// AtomCreatedPayload mirrors the Protobuf-encoded body of
// `chora.creation.atom.created.v1`. The full schema lives at
// chora-contracts/proto/events/creation/atom.proto; we mirror only the
// fields the projection needs.
type AtomCreatedPayload struct {
	AtomID          string
	TenantID        string
	CourseID        string
	Title           string
	AtomType        string
	Difficulty      int
	TopicTags       []string
	CorrectOptionID string // stable MCQ answer-key option id; "" = unset/non-MCQ
	AnswerCount     int
	PublishedAt     time.Time
}

// AtomCreatedSubscriber upserts the atom_index projection for every
// `chora.creation.atom.created.v1` event AND retroactively appends the atom
// to every course-bound LearningPath that has already been bootstrapped for
// the atom's course (R2). paths may be nil (atom_index-only mode).
type AtomCreatedSubscriber struct {
	repo    atom_index.Repo
	paths   learning_path.Repo
	tracker *idempotencyTracker
}

// NewAtomCreatedSubscriber constructs the subscriber. paths is the
// LearningPath repo used for retroactive append (R2); pass nil to upsert the
// atom_index projection only.
func NewAtomCreatedSubscriber(repo atom_index.Repo, paths learning_path.Repo) *AtomCreatedSubscriber {
	return &AtomCreatedSubscriber{repo: repo, paths: paths, tracker: newIdempotencyTracker()}
}

// Handle upserts the atom_index projection then appends the atom to any
// existing path for its course. Idempotent — duplicate event_ids are silently
// dropped (atom_index.Save is an upsert and AppendAtom skips duplicates, so a
// replay would be harmless even without the tracker). ctx carries the RLS
// tenant session for the pg-backed repo (set by the dispatcher from the event
// payload tenant).
//
// Process-then-mark (CHO-2130, pattern per CHO-2107): non-marking seen() peek
// at entry; the key is recorded ONLY after the projection landed. The previous
// claim-first markSeen burned the dedupe key before Save, so a transient
// failure NACKed the event and its redelivery was ack-dropped as a duplicate —
// atom.created is once-only, so the atom_index row was lost permanently (atom
// invisible to grading + daily dose). Errors return UNMARKED (NACK →
// redelivery); the upsert absorbs the rare concurrent double-process.
func (s *AtomCreatedSubscriber) Handle(ctx context.Context, env events.Envelope, p AtomCreatedPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	if s.tracker.seen(env.EventID) {
		return nil // duplicate — already projected
	}
	if p.PublishedAt.IsZero() {
		p.PublishedAt = env.OccurredAt
	}
	a, err := atom_index.New(atom_index.NewParams{
		AtomID:          p.AtomID,
		TenantID:        p.TenantID,
		CourseID:        p.CourseID,
		Title:           p.Title,
		AtomType:        p.AtomType,
		Difficulty:      p.Difficulty,
		TopicTags:       p.TopicTags,
		CorrectOptionID: p.CorrectOptionID,
		AnswerCount:     p.AnswerCount,
		PublishedAt:     p.PublishedAt,
	})
	if err != nil {
		return fmt.Errorf("atom_created subscriber: %w", err)
	}
	if err := s.repo.Save(ctx, a); err != nil {
		return fmt.Errorf("atom_created subscriber: save: %w", err)
	}
	// R2 retroactive append: an atom authored after a learner enrolled into a
	// (then content-empty) course extends that learner's existing path rather
	// than being lost. No-op when paths is nil, the course has no atom, or the
	// atom is already in the path (AppendAtom is idempotent).
	if s.paths != nil && p.CourseID != "" {
		paths, lerr := s.paths.ListByCourse(ctx, p.TenantID, p.CourseID)
		if lerr != nil {
			return fmt.Errorf("atom_created subscriber: list paths by course: %w", lerr)
		}
		for _, path := range paths {
			if path.AppendAtom(p.AtomID, p.PublishedAt) {
				if serr := s.paths.Save(ctx, path); serr != nil {
					return fmt.Errorf("atom_created subscriber: save path: %w", serr)
				}
			}
		}
	}
	s.tracker.mark(env.EventID)
	return nil
}

// ----------------- EnrollmentCreated subscriber -----------------

// EnrollmentCreatedPayload mirrors `chora.delivery.enrollment.created.v1`.
// See chora-contracts/asyncapi/delivery/enrollment.created.v1.yaml.
type EnrollmentCreatedPayload struct {
	EnrollmentID string
	CourseID     string
	LearnerGCID  string
	TenantID     string
	EnrolledAt   time.Time
}

// EnrollmentCreatedSubscriber bootstraps a LearningPath on enrollment.
//
// 🔴 CHO-2169. The atom list comes from the LOCAL course-content projection —
// chora-delivery's curriculum, arriving via course.content_composed.v1 — and
// NEVER cross-DB.
//
// It used to come from `atom_index.course_id`, which is projected from
// chora-creation's atom.created.v1, i.e. an atom SELF-DECLARING its course. But
// R+ composes a curriculum in chora_delivery, and that never writes back to the
// atom in chora_creation. So the tag was NULL for 122 of 128 live atoms, the
// lookup returned nothing, and EVERY course-bootstrapped path in production held
// zero atoms — while the correct curriculum sat in this very service, projected
// and unread.
//
// The two questions look identical and are not:
//
//	"which course was this atom authored for?"  → chora_creation (an atom property)
//	"which atoms are in this course?"           → chora_delivery (a curriculum)
//
// Only the second one builds a learner's path.
type EnrollmentCreatedSubscriber struct {
	paths     learning_path.Repo
	content   course_content.ProjectionRepo
	publisher events.Publisher
}

// NewEnrollmentCreatedSubscriber constructs the subscriber. Idempotency is
// the durable (tenant, course, learner) path-existence check inside Handle —
// no event_id tracker (see Handle R1 note).
func NewEnrollmentCreatedSubscriber(paths learning_path.Repo, content course_content.ProjectionRepo, publisher events.Publisher) *EnrollmentCreatedSubscriber {
	return &EnrollmentCreatedSubscriber{
		paths:     paths,
		content:   content,
		publisher: publisher,
	}
}

// atomRefsInCurriculumOrder extracts the playable atoms from a course's
// curriculum, in the INSTRUCTOR'S order.
//
// ListByCourse already returns items ordered by `position`, so no sort is needed
// — and none is wanted. The previous implementation sorted by atom_id ("a safe
// MVP order"), which is a lexicographic accident, not a teaching order. `position`
// IS the order; carrying one is the whole point of a curriculum.
//
// Only kind=atom is playable. A video, a PDF, a graded assessment and a live
// classroom are all legitimate curriculum items, and none of them is a
// LearningAtom — admitting their refs would put a GCS object path where an
// atom_id belongs.
func atomRefsInCurriculumOrder(items []*course_content.Item) []string {
	refs := make([]string, 0, len(items))
	for _, it := range items {
		if it == nil || it.Kind != course_content.KindAtom {
			continue
		}
		if ref := strings.TrimSpace(it.Ref); ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

// Handle bootstraps a LearningPath for the (tenant, course, learner) trio.
//
// Idempotency (R1): anchored on the DURABLE (tenant, course, learner)
// existence check below — NOT on env.EventID. The previous code marked the
// event_id "seen" in an in-process tracker BEFORE the path was durably
// created; when the bootstrap then deferred (empty course) or failed, the
// dedupe key was already burned, so any redrive (fresh or same event_id) was
// silently skipped forever — stranding the enrolment. We removed that guard:
// GetByCourseAndGCID is the authoritative, replay-safe idempotency key and
// also gates the outbound bootstrapped event so it is never double-published.
//
// Content tolerance (OPEN-1): a PUBLISHED course with no atoms yet (test-set-
// only, or atoms not yet projected into atom_index) STILL bootstraps a 0-atom
// path so the enrolled learner is not shown "not enrolled". Atoms arriving
// later are appended retroactively by AtomCreatedSubscriber (R2).
func (s *EnrollmentCreatedSubscriber) Handle(ctx context.Context, env events.Envelope, p EnrollmentCreatedPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	// Durable, replay-safe idempotency: one path per (tenant, course, learner).
	if existing, err := s.paths.GetByCourseAndGCID(ctx, p.TenantID, p.CourseID, p.LearnerGCID); err == nil && existing != nil {
		return nil
	}
	// The CURRICULUM (chora-delivery's truth), not atom_index.course_id — see the
	// type doc. Already ordered by the instructor's `position`.
	items, err := s.content.ListByCourse(ctx, p.TenantID, p.CourseID)
	if err != nil {
		return fmt.Errorf("enrollment_created subscriber: list curriculum: %w", err)
	}
	atomIDs := atomRefsInCurriculumOrder(items)

	path, err := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID:     p.TenantID,
		LearnerGCID:  p.LearnerGCID,
		CourseID:     p.CourseID,
		EnrollmentID: p.EnrollmentID,
		AtomIDs:      atomIDs,
		Now:          env.OccurredAt,
	})
	if err != nil {
		return fmt.Errorf("enrollment_created subscriber: %w", err)
	}
	if err := s.paths.Save(ctx, path); err != nil {
		return fmt.Errorf("enrollment_created subscriber: save path: %w", err)
	}

	// Emit chora.consumption.learning_path.bootstrapped.v1 — tells
	// downstream observability + IMDA evidence about the new path.
	outbound := events.NewEnvelope(
		p.TenantID,
		p.LearnerGCID,
		env.Traceparent,
		env.Tracestate,
		"learning_path:"+path.PathID,
	)
	return s.publisher.Publish(events.TopicLearningPathBootstrapped, outbound, map[string]any{
		"path_id":       path.PathID,
		"course_id":     p.CourseID,
		"learner_gcid":  p.LearnerGCID,
		"enrollment_id": p.EnrollmentID,
		// atom_ids — REQUIRED by the sole consumer. ActivePathTopicsSubscriber's
		// LearningPathBootstrappedPayload declares `AtomIDs []string` and resolves
		// each atom's topic_tags from atom_index to build the dose's CURIOSITY
		// topic-set. This payload only ever carried atom_count (an int), so the
		// consumer always decoded an EMPTY atom list, resolved zero topics and
		// wrote zero rows. The contract never matched. atom_count is retained —
		// it is additive and other readers may rely on it.
		"atom_ids":          atomIDs,
		"atom_count":        len(atomIDs),
		"upstream_event_id": env.EventID,
		"source_event":      "chora.delivery.enrollment.created.v1",
	})
}

// NB: sortStrings (an insertion sort over atom_ids) lived here and is GONE. It
// existed only to impose "a safe MVP order" on a set of atoms that arrived with
// no order at all — a lexicographic accident standing in for a teaching order.
// The curriculum carries `position`, which is the instructor's actual order, so
// there is nothing left to sort.
