// collection_converted_to_study_list_subscriber_test.go — RED-phase tests for
// the WS-4 study-list subscriber (ADR-233, spec-001 US5).
package subscribers

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

const (
	clTenant  = "01970000-0000-7000-8000-000000000001"
	clLearner = "01970000-0000-7000-9000-000000000001"
	clCollID  = "01970000-0000-7000-b000-000000000001"
	clAtom1   = "01970000-0000-7000-a000-000000000001"
	clAtom2   = "01970000-0000-7000-a000-000000000002"
	clAtom3   = "01970000-0000-7000-a000-000000000003"
	clEvID1   = "01970000-0000-7000-c000-000000000001"
	clEvID2   = "01970000-0000-7000-c000-000000000002"
)

// ctxCapturingPathRepo records the ctx handed to the repo so we can prove the
// RLS tenant was stamped (the CHO-1612 bug: a bare context.Background() yields
// rls.ErrNoTenantContext → EVERY event 500s → DLQ).
type ctxCapturingPathRepo struct {
	*inmem.LearningPathRepo
	saveCtx context.Context
}

func (c *ctxCapturingPathRepo) Save(ctx context.Context, p *learning_path.LearningPath) error {
	c.saveCtx = ctx
	return c.LearningPathRepo.Save(ctx, p)
}

func clEnvelope(eventID string) events.Envelope {
	return events.Envelope{
		EventID:        eventID,
		IdempotencyKey: eventID,
		TenantID:       clTenant,
		GCID:           clLearner,
		OccurredAt:     time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC),
		PublishedAt:    time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC),
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		SourceProject:  "chora-489812",
		SourceService:  "chora-creation",
		SchemaVersion:  1,
	}
}

func clPayload(evID string, atoms ...string) CollectionConvertedToStudyListPayload {
	return CollectionConvertedToStudyListPayload{
		CollectionID:     clCollID,
		OwnerGCID:        clLearner,
		AtomIDs:          atoms,
		StudyListEventID: evID,
	}
}

func newCLSub(paths learning_path.Repo, pub events.Publisher) *CollectionConvertedToStudyListSubscriber {
	return NewCollectionConvertedToStudyListSubscriber(paths, pub)
}

// -----------------------------------------------------------------------------
// Happy path — a spaced, collection-provenanced path + the bootstrapped event
// -----------------------------------------------------------------------------

func TestCollectionConverted_CreatesSpacedCollectionPath(t *testing.T) {
	paths := inmem.NewLearningPathRepo()
	pub := events.NewInMemoryPublisher()
	sub := newCLSub(paths, pub)

	if err := sub.Handle(clEnvelope(clEvID1), clPayload(clEvID1, clAtom1, clAtom2)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	got, err := paths.GetBySourceCollection(context.Background(), clTenant, clLearner, clCollID)
	if err != nil {
		t.Fatalf("GetBySourceCollection: %v", err)
	}
	if got.SourceType != learning_path.SourceTypeCollection {
		t.Errorf("SourceType = %q; want collection", got.SourceType)
	}
	if got.SourceID != clCollID {
		t.Errorf("SourceID = %q; want %q", got.SourceID, clCollID)
	}
	if got.TraversalMode != learning_path.TraversalModeSpaced {
		t.Errorf("TraversalMode = %q; want spaced", got.TraversalMode)
	}
	if got.StudyListEventID != clEvID1 {
		t.Errorf("StudyListEventID = %q; want %q (the durable dedupe anchor)", got.StudyListEventID, clEvID1)
	}
	if len(got.AtomIDs) != 2 {
		t.Errorf("AtomIDs = %v; want 2 atoms from the payload", got.AtomIDs)
	}
}

// 🔴 THE LOAD-BEARING ONE. Without learning_path.bootstrapped.v1 the study list
// never reaches active_path_topics → never reaches the daily dose's CURIOSITY
// slot → the list is INERT and "converted to a study list" is a lie.
//
// The REST learning_path.New() lane does NOT emit this event, so the subscriber
// must emit it itself.
func TestCollectionConverted_PublishesBootstrapped_FeedsCuriositySlot(t *testing.T) {
	paths := inmem.NewLearningPathRepo()
	pub := events.NewInMemoryPublisher()
	sub := newCLSub(paths, pub)

	if err := sub.Handle(clEnvelope(clEvID1), clPayload(clEvID1, clAtom1, clAtom2)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var found *events.Event
	for i, e := range pub.Events() {
		if e.Topic == events.TopicLearningPathBootstrapped {
			found = &pub.Events()[i]
			break
		}
	}
	if found == nil {
		var topics []string
		for _, e := range pub.Events() {
			topics = append(topics, e.Topic)
		}
		t.Fatalf("no %s emitted (got topics %v).\n"+
			"Without it the study list never reaches active_path_topics → never reaches "+
			"the daily dose's CURIOSITY slot → the list is INERT and the conversion is a lie.",
			events.TopicLearningPathBootstrapped, topics)
	}
	if found.Envelope.TenantID != clTenant {
		t.Errorf("bootstrapped envelope tenant = %q; want %q", found.Envelope.TenantID, clTenant)
	}
	if found.Envelope.Traceparent == "" {
		t.Error("bootstrapped envelope lost the W3C traceparent (mandatory)")
	}
	if src, _ := found.Payload["source_event"].(string); !strings.Contains(src, "converted_to_study_list") {
		t.Errorf("payload.source_event = %v; want the convert topic for provenance", found.Payload["source_event"])
	}
}

// -----------------------------------------------------------------------------
// Idempotency — the DURABLE domain anchor, not an in-process tracker
// -----------------------------------------------------------------------------

func TestCollectionConverted_IsIdempotentOnStudyListEventID(t *testing.T) {
	paths := inmem.NewLearningPathRepo()
	pub := events.NewInMemoryPublisher()
	sub := newCLSub(paths, pub)

	env := clEnvelope(clEvID1)
	p := clPayload(clEvID1, clAtom1, clAtom2)

	if err := sub.Handle(env, p); err != nil {
		t.Fatalf("Handle #1: %v", err)
	}
	// Redelivery of the SAME event (Pub/Sub is at-least-once).
	if err := sub.Handle(env, p); err != nil {
		t.Fatalf("Handle #2 (redelivery): %v", err)
	}

	all, err := paths.ListByLearner(context.Background(), clTenant, clLearner, 0)
	if err != nil {
		t.Fatalf("ListByLearner: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("got %d paths; want exactly 1 — a redelivered convert must NOT mint a second study list", len(all))
	}
	n := 0
	for _, e := range pub.Events() {
		if e.Topic == events.TopicLearningPathBootstrapped {
			n++
		}
	}
	if n != 1 {
		t.Errorf("emitted %d bootstrapped events; want 1 (a redelivery must not re-publish)", n)
	}
}

// A FRESH subscriber instance (i.e. a different pod / after a restart) must ALSO
// dedupe — proving the anchor is DURABLE and not an in-process tracker.
func TestCollectionConverted_DedupeSurvivesSubscriberRestart(t *testing.T) {
	paths := inmem.NewLearningPathRepo()
	pub := events.NewInMemoryPublisher()

	if err := newCLSub(paths, pub).Handle(clEnvelope(clEvID1), clPayload(clEvID1, clAtom1)); err != nil {
		t.Fatalf("Handle #1: %v", err)
	}
	// New subscriber = new in-process tracker; only a DURABLE anchor can dedupe.
	if err := newCLSub(paths, pub).Handle(clEnvelope(clEvID1), clPayload(clEvID1, clAtom1)); err != nil {
		t.Fatalf("Handle #2 (post-restart redelivery): %v", err)
	}

	all, _ := paths.ListByLearner(context.Background(), clTenant, clLearner, 0)
	if len(all) != 1 {
		t.Errorf("got %d paths; want 1 — dedupe MUST be anchored on the durable "+
			"study_list_event_id, not an in-process event-id tracker (the R1 lesson)", len(all))
	}
}

// -----------------------------------------------------------------------------
// D4 — re-convert is an ADDITIVE re-sync
// -----------------------------------------------------------------------------

func TestCollectionConverted_ReConvertIsAdditiveReSync(t *testing.T) {
	paths := inmem.NewLearningPathRepo()
	pub := events.NewInMemoryPublisher()
	sub := newCLSub(paths, pub)

	// First convert: {atom1, atom2}
	if err := sub.Handle(clEnvelope(clEvID1), clPayload(clEvID1, clAtom1, clAtom2)); err != nil {
		t.Fatalf("Handle #1: %v", err)
	}
	// Learner has progress.
	existing, _ := paths.GetBySourceCollection(context.Background(), clTenant, clLearner, clCollID)
	existing.CurrentIndex = 1
	_ = paths.Save(context.Background(), existing)

	// Re-convert: the source now holds {atom2, atom3} — atom1 was REMOVED upstream.
	if err := sub.Handle(clEnvelope(clEvID2), clPayload(clEvID2, clAtom2, clAtom3)); err != nil {
		t.Fatalf("Handle #2 (re-convert): %v", err)
	}

	got, err := paths.GetBySourceCollection(context.Background(), clTenant, clLearner, clCollID)
	if err != nil {
		t.Fatalf("GetBySourceCollection: %v", err)
	}
	// Exactly ONE path — a re-convert re-syncs, never forks.
	all, _ := paths.ListByLearner(context.Background(), clTenant, clLearner, 0)
	if len(all) != 1 {
		t.Fatalf("got %d paths; want 1 (re-convert must re-sync the SAME path)", len(all))
	}
	// atom1 must NOT be retracted (ADR-233 D4 — the learner may have progress in it).
	if !clHas(got.AtomIDs, clAtom1) {
		t.Errorf("atom1 was RETRACTED on re-convert; ADR-233 D4 forbids a source ever "+
			"retracting atoms from a derived path. AtomIDs=%v", got.AtomIDs)
	}
	if !clHas(got.AtomIDs, clAtom3) {
		t.Errorf("atom3 (newly added upstream) was not appended; AtomIDs=%v", got.AtomIDs)
	}
	if len(got.AtomIDs) != 3 {
		t.Errorf("AtomIDs = %v; want 3 (atom1 retained + atom2 + atom3 appended)", got.AtomIDs)
	}
	// Progress preserved.
	if got.CurrentIndex != 1 {
		t.Errorf("CurrentIndex = %d; want 1 preserved across the additive re-sync", got.CurrentIndex)
	}
}

// -----------------------------------------------------------------------------
// RLS — the CHO-1612 bug guard
// -----------------------------------------------------------------------------

func TestCollectionConverted_StampsTenantOnContextForRLS(t *testing.T) {
	capturing := &ctxCapturingPathRepo{LearningPathRepo: inmem.NewLearningPathRepo()}
	sub := newCLSub(capturing, events.NewInMemoryPublisher())

	if err := sub.Handle(clEnvelope(clEvID1), clPayload(clEvID1, clAtom1)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if capturing.saveCtx == nil {
		t.Fatal("repo.Save was never called")
	}
	if got := tracing.TenantIDFromContext(capturing.saveCtx); got != clTenant {
		t.Errorf("tenant on repo ctx = %q; want %q.\n"+
			"Push requests carry NO tenant middleware ctx — the subscriber MUST stamp "+
			"tracing.WithTenantID(ctx, env.TenantID) or rls.ApplySession yields "+
			"ErrNoTenantContext and EVERY event 500s → DLQ (the CHO-1612 bug).", got, clTenant)
	}
}

// -----------------------------------------------------------------------------
// Fail-loud
// -----------------------------------------------------------------------------

func TestCollectionConverted_RejectsInvalidEnvelope(t *testing.T) {
	sub := newCLSub(inmem.NewLearningPathRepo(), events.NewInMemoryPublisher())
	env := clEnvelope(clEvID1)
	env.Traceparent = "" // mandatory W3C field

	if err := sub.Handle(env, clPayload(clEvID1, clAtom1)); err == nil {
		t.Fatal("Handle accepted an envelope with no traceparent; want a loud refusal")
	}
}

func TestCollectionConverted_RejectsMissingCollectionID(t *testing.T) {
	sub := newCLSub(inmem.NewLearningPathRepo(), events.NewInMemoryPublisher())
	p := clPayload(clEvID1, clAtom1)
	p.CollectionID = ""

	if err := sub.Handle(clEnvelope(clEvID1), p); err == nil {
		t.Fatal("Handle accepted a payload with no collection_id; want a loud refusal")
	}
}

// A convert whose atom set is empty still records the (empty) study list rather
// than fabricating atoms. Zero ENTITLED atoms is refused UPSTREAM in
// chora-creation (409 CREATION_COLLECTION_NO_ENTITLED_ATOMS, ADR-233 D11) —
// consumption never invents content.
func TestCollectionConverted_EmptyAtomListIsNotFabricated(t *testing.T) {
	paths := inmem.NewLearningPathRepo()
	sub := newCLSub(paths, events.NewInMemoryPublisher())

	if err := sub.Handle(clEnvelope(clEvID1), clPayload(clEvID1)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, err := paths.GetBySourceCollection(context.Background(), clTenant, clLearner, clCollID)
	if err != nil {
		t.Fatalf("GetBySourceCollection: %v", err)
	}
	if len(got.AtomIDs) != 0 {
		t.Errorf("AtomIDs = %v; want empty — consumption must never fabricate atoms", got.AtomIDs)
	}
}

// -----------------------------------------------------------------------------
// ADR-233 rejected-alternative guard — NO sm2_states seeding
// -----------------------------------------------------------------------------

// TestCollectionConverted_DoesNotSeedSM2 locks the rejected alternative
// structurally: the subscriber holds NO retention/SM-2 collaborator at all, so
// it CANNOT seed review history even by accident.
//
// ADR-233 (alternatives, rejected): seeding sm2_states at conversion "fabricates
// review history for atoms the learner has never seen, and corrupts SM-2's
// easiness-factor / interval semantics. New material correctly enters via the
// dose's CURIOSITY slot and earns its SM-2 row on first answer."
func TestCollectionConverted_DoesNotSeedSM2(t *testing.T) {
	forbidden := []string{"sm2", "retention", "review", "schedul"}

	typ := reflect.TypeOf(CollectionConvertedToStudyListSubscriber{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := strings.ToLower(f.Name)
		kind := strings.ToLower(f.Type.String())
		for _, bad := range forbidden {
			if strings.Contains(name, bad) || strings.Contains(kind, bad) {
				t.Errorf("ADR-233 REJECTED ALTERNATIVE: subscriber field %s (%s) looks like an "+
					"SM-2 / review-scheduling collaborator.\n"+
					"Seeding sm2_states at conversion fabricates review history for atoms the "+
					"learner has NEVER SEEN and corrupts SM-2's easiness-factor/interval semantics.\n"+
					"New material enters via the dose's CURIOSITY slot (fed by "+
					"learning_path.bootstrapped.v1 → active_path_topics) and earns its SM-2 row on "+
					"first answer.", f.Name, f.Type)
			}
		}
	}
}

func clHas(list []string, want string) bool {
	for _, a := range list {
		if a == want {
			return true
		}
	}
	return false
}

var _ = errors.New // keep errors import stable across edits
