// course_content_subscriber.go — projects chora.delivery.course.content_composed.v1
// into the local course-content read projection (CHO-1612).
//
// The event carries the FULL ordered curriculum for a course; the subscriber
// REPLACES the projection for that course (idempotent — duplicate event_ids
// are deduped; out-of-order replays converge because each event is a complete
// snapshot). Invalid items (e.g. unknown kind) are skipped, not fatal, so one
// bad row never blocks the whole curriculum.
package subscribers

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/course_content"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// CourseContentItemPayload mirrors one item in the JSON content_composed event.
type CourseContentItemPayload struct {
	ItemID   string
	Kind     string
	Ref      string
	Title    string
	Position int
}

// CourseContentComposedPayload mirrors chora.delivery.course.content_composed.v1.
type CourseContentComposedPayload struct {
	CourseID string
	Items    []CourseContentItemPayload
}

// CourseContentComposedSubscriber upserts the course-content projection and
// back-fills the curriculum's atoms into already-enrolled learners' paths.
//
// 🔴 CHO-2169. The retroactive append used to live on AtomCreatedSubscriber, i.e.
// on `atom.created.v1` — an atom being AUTHORED. But an atom joins a COURSE by
// being composed into its curriculum, which is a chora-delivery act and fires
// THIS event. Authoring an atom in A+ puts it in nobody's course, and composing
// an existing atom into a course does not re-author it. So the append hung off an
// event that can never fire for an R+-composed course, and never ran.
type CourseContentComposedSubscriber struct {
	repo    course_content.ProjectionRepo
	paths   learning_path.Repo // MAY be nil (no pg pool in dev) — back-fill is then skipped LOUDLY at wiring.
	tracker *idempotencyTracker
}

// NewCourseContentComposedSubscriber constructs the subscriber. `paths` may be
// nil, in which case the projection still lands and the back-fill is skipped.
func NewCourseContentComposedSubscriber(repo course_content.ProjectionRepo, paths learning_path.Repo) *CourseContentComposedSubscriber {
	return &CourseContentComposedSubscriber{repo: repo, paths: paths, tracker: newIdempotencyTracker()}
}

// Handle replaces the course's projected curriculum with the event's items.
//
// Process-then-mark (CHO-2130): seen() peek at entry, mark() only after the
// replace landed — claim-first markSeen swallowed the redelivery after a
// transient ReplaceByCourse failure, losing the snapshot until the NEXT
// compose. Errors return UNMARKED (NACK); the whole-course replace is
// naturally idempotent on a duplicate.
func (s *CourseContentComposedSubscriber) Handle(env events.Envelope, p CourseContentComposedPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	if s.tracker.seen(env.EventID) {
		return nil // duplicate — already projected
	}
	items := make([]*course_content.Item, 0, len(p.Items))
	for _, ip := range p.Items {
		it, err := course_content.New(course_content.NewParams{
			ItemID:   ip.ItemID,
			TenantID: env.TenantID,
			CourseID: p.CourseID,
			Kind:     course_content.Kind(ip.Kind),
			Ref:      ip.Ref,
			Title:    ip.Title,
			Position: ip.Position,
		})
		if err != nil {
			log.Printf("course_content projection: skipping invalid item %q in course %s: %v", ip.ItemID, p.CourseID, err)
			continue
		}
		items = append(items, it)
	}
	// RLS: the pg ProjectionRepo runs rls.ApplySession, which reads the tenant
	// from the CONTEXT (not the explicit tenantID arg). A bare context.Background()
	// yields rls.ErrNoTenantContext, so the projection errored on EVERY event
	// (→ 500 → Pub/Sub retry → DLQ; CHO-1612 curriculum never persisted). Stamp
	// the envelope tenant onto the ctx so `SET LOCAL chora.tenant_id` lands.
	ctx := tracing.WithTenantID(context.Background(), env.TenantID)
	if err := s.repo.ReplaceByCourse(ctx, env.TenantID, p.CourseID, items); err != nil {
		return err
	}

	// R2 — retroactive append into ALREADY-ENROLLED learners' paths, so an atom
	// composed into the course after they enrolled still reaches them.
	//
	// Strictly ADDITIVE (ADR-233 D4): an atom REMOVED from the curriculum stays in
	// the learner's path. They may already have progress in it, and a source may
	// never retract atoms from a derived path. That is why this appends from the
	// snapshot and never reconciles against it — the whole-course replace above is
	// a projection of the course, not of anybody's learning.
	//
	// Runs BEFORE tracker.mark: a back-fill failure must NACK and redeliver, or the
	// projection would land while the learners it exists to serve never got the atom.
	if err := s.backfillEnrolledPaths(ctx, env.TenantID, p.CourseID, atomRefsInCurriculumOrder(items)); err != nil {
		return err
	}

	s.tracker.mark(env.EventID)
	return nil
}

// backfillEnrolledPaths appends the curriculum's atoms to every existing path for
// the course. AppendAtom is idempotent, so a re-compose (or a Pub/Sub redelivery)
// adds nothing and saves nothing.
func (s *CourseContentComposedSubscriber) backfillEnrolledPaths(
	ctx context.Context, tenantID, courseID string, atomIDs []string,
) error {
	if s.paths == nil || len(atomIDs) == 0 {
		return nil
	}
	paths, err := s.paths.ListByCourse(ctx, tenantID, courseID)
	if err != nil {
		return fmt.Errorf("course_content subscriber: list paths by course: %w", err)
	}
	now := time.Now().UTC()
	for _, path := range paths {
		changed := false
		for _, atomID := range atomIDs {
			if path.AppendAtom(atomID, now) {
				changed = true
			}
		}
		if !changed {
			continue
		}
		if err := s.paths.Save(ctx, path); err != nil {
			return fmt.Errorf("course_content subscriber: save path %s: %w", path.PathID, err)
		}
	}
	return nil
}
