// derived_weakness_projector_test.go — W3-derived of the 1b Growth-Edge track.
//
// The projector turns performance evidence into derived/classroom Growth Edges:
//   - chora.delivery.live_quiz_session.score_awarded.v1 (classroom)
//   - chora.consumption.atom_session.completed.v1 (derived)
//
// Both paths record the attempt into the durable topic_accuracy projection,
// then either Upsert a weakness (below the mastery threshold; strength is the
// DerivedStrength accuracy×retention blend) or Recover the matching edge
// (at/above threshold — the Ebbinghaus auto-recovery path; only ever lowers).
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// --- fakes ---

type dwRecoverCall struct {
	GCID       string
	ConceptKey string
	Strength   float64
	CtxTenant  string
}

type dwFakeRepo struct {
	upserts         []lw.UpsertInput
	tenants         []string
	recovers        []dwRecoverCall
	drillRecovers   []dwDrillCall
	upsertErr       error
	recoverErr      error
	drillRecoverErr error
	// grownOnRecover / grownOnDrill let a test simulate an active→grown
	// transition on the respective recover path (ADR-196 B1 emit tests).
	grownOnRecover []lw.GrownEdge
	grownOnDrill   []lw.GrownEdge
}

func (r *dwFakeRepo) Upsert(ctx context.Context, in lw.UpsertInput) (lw.UpsertResult, error) {
	r.upserts = append(r.upserts, in)
	r.tenants = append(r.tenants, tracing.TenantIDFromContext(ctx))
	if r.upsertErr != nil {
		return lw.UpsertResult{}, r.upsertErr
	}
	return lw.UpsertResult{ID: "ge-" + in.ConceptKey}, nil
}
func (r *dwFakeRepo) List(context.Context, lw.ListQuery) (lw.ListResult, error) {
	return lw.ListResult{}, nil
}
func (r *dwFakeRepo) ListAll(context.Context, lw.ListQuery) ([]lw.LearnerWeakness, error) {
	return nil, nil
}
func (r *dwFakeRepo) Get(context.Context, string, string) (*lw.LearnerWeakness, error) {
	return nil, nil
}
func (r *dwFakeRepo) SoftDelete(context.Context, string, string, time.Time) error { return nil }
func (r *dwFakeRepo) RecoverByConceptKey(ctx context.Context, gcid, conceptKey string, strength float64, _ time.Time) ([]lw.GrownEdge, error) {
	if r.recoverErr != nil {
		return nil, r.recoverErr
	}
	r.recovers = append(r.recovers, dwRecoverCall{
		GCID: gcid, ConceptKey: conceptKey, Strength: strength,
		CtxTenant: tracing.TenantIDFromContext(ctx),
	})
	return r.grownOnRecover, nil
}
func (r *dwFakeRepo) SetCachedDrillAtoms(context.Context, string, string, []string, time.Time) error {
	return nil
}

type dwAttempt struct {
	Topic   string
	Correct bool
}

// dwFakeAccuracy implements AccuracyRecorder with a settable accuracy map.
type dwFakeAccuracy struct {
	attempts     []dwAttempt
	accuracy     map[string]float64
	seenSessions map[string]bool
	recordErr    error
	getErr       error
	markErr      error
}

func (a *dwFakeAccuracy) RecordAttempt(_ context.Context, _, _, topicTag string, isCorrect bool) error {
	if a.recordErr != nil {
		return a.recordErr
	}
	a.attempts = append(a.attempts, dwAttempt{Topic: topicTag, Correct: isCorrect})
	return nil
}
func (a *dwFakeAccuracy) GetByLearner(context.Context, string, string) (map[string]float64, error) {
	if a.getErr != nil {
		return nil, a.getErr
	}
	if a.accuracy == nil {
		return map[string]float64{}, nil
	}
	return a.accuracy, nil
}
func (a *dwFakeAccuracy) MarkSessionSeen(_ context.Context, tenantID, gcid, sessionID string) (bool, error) {
	if a.markErr != nil {
		return false, a.markErr
	}
	if a.seenSessions == nil {
		a.seenSessions = map[string]bool{}
	}
	k := tenantID + "|" + gcid + "|" + sessionID
	if a.seenSessions[k] {
		return false, nil
	}
	a.seenSessions[k] = true
	return true, nil
}

// dwFakeRetention implements RetentionReader.
type dwFakeRetention struct {
	byTopic map[string]float64
}

func (r *dwFakeRetention) RetentionAt(_ context.Context, _, _, topic string, _ time.Time) (float64, bool) {
	v, ok := r.byTopic[topic]
	return v, ok
}

// dwFakeAtoms is a minimal atom_index.Repo. Missing atoms return the canonical
// atom_index.ErrNotFound (the contract BOTH real adapters honour) so the
// projector's transient-vs-notfound discrimination is exercised honestly;
// getErr arms a TRANSIENT lookup failure.
type dwFakeAtoms struct {
	atoms  map[string]*atom_index.AtomIndex
	getErr error
}

func (f *dwFakeAtoms) Save(context.Context, *atom_index.AtomIndex) error { return nil }
func (f *dwFakeAtoms) Get(_ context.Context, atomID string) (*atom_index.AtomIndex, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if a, ok := f.atoms[atomID]; ok {
		return a, nil
	}
	return nil, atom_index.ErrNotFound
}
func (f *dwFakeAtoms) ListByCourse(context.Context, string, string) ([]*atom_index.AtomIndex, error) {
	return nil, nil
}
func (f *dwFakeAtoms) SearchForLearner(context.Context, string, string, int) ([]*atom_index.AtomIndex, error) {
	return nil, nil
}
func (f *dwFakeAtoms) MarkPublished(context.Context, string, atom_index.Status, string, string, int, string) error {
	return nil
}

// --- helpers ---

const (
	dwTenant = "01970000-0000-7000-8000-000000000001"
	dwGCID   = "01970000-0000-7000-9000-000000000001"
)

func dwEnv(eventID string) events.Envelope {
	return events.Envelope{
		EventID:        eventID,
		IdempotencyKey: "lq-1|q-1",
		TenantID:       dwTenant,
		GCID:           dwGCID,
		OccurredAt:     time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		SourceProject:  "chora-489812",
		SourceService:  "chora-delivery",
		SchemaVersion:  1,
	}
}

func dwScorePayload(correct bool) LiveQuizScoreAwardedPayload {
	return LiveQuizScoreAwardedPayload{
		SessionID:   "lqs-1",
		LiveQuizID:  "lq-1",
		QuestionID:  "q-1",
		AtomID:      "atom-1",
		TopicTags:   []string{"multiplication tables", "arithmetic"},
		Correct:     correct,
		TenantID:    dwTenant,
		LearnerGCID: dwGCID,
	}
}

func newDWProjector(repo *dwFakeRepo, acc *dwFakeAccuracy) *DerivedWeaknessProjector {
	return NewDerivedWeaknessProjector(repo, &lwFakeEmbedder{vec: []float32{0.1, 0.2}}, acc)
}

// --- live-quiz (classroom) path ---

func TestLiveQuiz_WrongBelowThreshold_UpsertsClassroomEdge(t *testing.T) {
	repo := &dwFakeRepo{}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"multiplication tables": 0.25}}
	p := newDWProjector(repo, acc)

	err := p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("ev-1"), dwScorePayload(false))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// Every denormalised tag records an attempt.
	if len(acc.attempts) != 2 || acc.attempts[0].Topic != "multiplication tables" || acc.attempts[1].Topic != "arithmetic" {
		t.Fatalf("attempts = %+v", acc.attempts)
	}
	if acc.attempts[0].Correct {
		t.Fatal("attempt must record incorrect")
	}

	if len(repo.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(repo.upserts))
	}
	up := repo.upserts[0]
	if up.Source != lw.SourceClassroom {
		t.Fatalf("source = %q, want classroom", up.Source)
	}
	if up.ConceptLabel != "multiplication tables" {
		t.Fatalf("concept label = %q", up.ConceptLabel)
	}
	if want := lw.DerivedStrength(0.25, 0, false); up.Strength != want {
		t.Fatalf("strength = %v, want %v", up.Strength, want)
	}
	if len(up.Tags) != 2 {
		t.Fatalf("tags = %v", up.Tags)
	}
	if up.Descriptor.Summary == "" {
		t.Fatal("descriptor summary must explain the classroom signal")
	}
	if repo.tenants[0] != dwTenant {
		t.Fatal("Upsert ctx must carry the tenant for RLS")
	}
	if len(repo.recovers) != 0 {
		t.Fatal("below threshold must not recover")
	}
}

func TestLiveQuiz_CorrectAboveThreshold_Recovers(t *testing.T) {
	repo := &dwFakeRepo{}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"multiplication tables": 0.9}}
	p := newDWProjector(repo, acc)

	if err := p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("ev-2"), dwScorePayload(true)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.upserts) != 0 {
		t.Fatalf("upserts = %d, want 0", len(repo.upserts))
	}
	if len(repo.recovers) != 1 {
		t.Fatalf("recovers = %d, want 1", len(repo.recovers))
	}
	rec := repo.recovers[0]
	if rec.ConceptKey != "multiplication-tables" {
		t.Fatalf("recover concept_key = %q", rec.ConceptKey)
	}
	if want := lw.DerivedStrength(0.9, 0, false); rec.Strength != want {
		t.Fatalf("recover strength = %v, want %v", rec.Strength, want)
	}
	if rec.CtxTenant != dwTenant {
		t.Fatal("Recover ctx must carry the tenant for RLS")
	}
}

func TestLiveQuiz_RetentionBlendsIntoStrength(t *testing.T) {
	repo := &dwFakeRepo{}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"multiplication tables": 0.5}}
	p := newDWProjector(repo, acc).WithRetention(&dwFakeRetention{
		byTopic: map[string]float64{"multiplication tables": 0.4},
	})

	if err := p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("ev-3"), dwScorePayload(false)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(repo.upserts))
	}
	if want := lw.DerivedStrength(0.5, 0.4, true); repo.upserts[0].Strength != want {
		t.Fatalf("strength = %v, want blended %v", repo.upserts[0].Strength, want)
	}
}

func TestLiveQuiz_AdHocQuestionSkipped(t *testing.T) {
	repo := &dwFakeRepo{}
	acc := &dwFakeAccuracy{}
	p := newDWProjector(repo, acc)

	payload := dwScorePayload(false)
	payload.AtomID = "" // ad-hoc question — no atom mastery signal
	if err := p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("ev-4"), payload); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(acc.attempts) != 0 || len(repo.upserts) != 0 || len(repo.recovers) != 0 {
		t.Fatal("ad-hoc question must be a no-op")
	}
}

func TestLiveQuiz_NoTags_FallsBackToAtomIndex(t *testing.T) {
	repo := &dwFakeRepo{}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"fractions": 0.2}}
	atoms := &dwFakeAtoms{atoms: map[string]*atom_index.AtomIndex{
		"atom-1": {AtomID: "atom-1", AtomType: "mcq", CorrectOptionID: "b", TopicTags: []string{"fractions"}},
	}}
	p := newDWProjector(repo, acc).WithAtomIndex(atoms)

	payload := dwScorePayload(false)
	payload.TopicTags = nil
	if err := p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("ev-5"), payload); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.upserts) != 1 || repo.upserts[0].ConceptLabel != "fractions" {
		t.Fatalf("upserts = %+v", repo.upserts)
	}
}

func TestLiveQuiz_NoTagsNoIndex_Acked(t *testing.T) {
	repo := &dwFakeRepo{}
	acc := &dwFakeAccuracy{}
	p := newDWProjector(repo, acc)

	payload := dwScorePayload(false)
	payload.TopicTags = nil
	if err := p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("ev-6"), payload); err != nil {
		t.Fatalf("unmappable evidence must ack, got %v", err)
	}
	if len(acc.attempts) != 0 {
		t.Fatal("no topic — no attempt recorded")
	}
}

func TestLiveQuiz_DedupByEventID_AndByAward(t *testing.T) {
	repo := &dwFakeRepo{}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"multiplication tables": 0.2}}
	p := newDWProjector(repo, acc)

	if err := p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("ev-7"), dwScorePayload(false)); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Same event_id redelivered.
	if err := p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("ev-7"), dwScorePayload(false)); err != nil {
		t.Fatalf("dup event_id: %v", err)
	}
	// Fresh event_id, same (session, question) award.
	if err := p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("ev-8"), dwScorePayload(false)); err != nil {
		t.Fatalf("dup award: %v", err)
	}
	if len(acc.attempts) != 2 { // one per tag, exactly once
		t.Fatalf("attempts = %+v, want exactly one processing", acc.attempts)
	}
}

func TestLiveQuiz_EnvelopeTenantFallback(t *testing.T) {
	repo := &dwFakeRepo{}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"multiplication tables": 0.2}}
	p := newDWProjector(repo, acc)

	payload := dwScorePayload(false)
	payload.TenantID = ""
	payload.LearnerGCID = ""
	if err := p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("ev-9"), payload); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.upserts) != 1 || repo.upserts[0].TenantID != dwTenant || repo.upserts[0].LearnerGCID != dwGCID {
		t.Fatalf("envelope fallback failed: %+v", repo.upserts)
	}
}

func TestLiveQuiz_InvalidEnvelopeRejected(t *testing.T) {
	p := newDWProjector(&dwFakeRepo{}, &dwFakeAccuracy{})
	env := dwEnv("ev-10")
	env.TenantID = ""
	if err := p.HandleLiveQuizScoreAwarded(context.Background(), env, dwScorePayload(false)); err == nil {
		t.Fatal("invalid envelope must error")
	}
}

// --- atom-session (derived) path ---

func dwSessionPayload(correct bool) AtomSessionCompletedPayload {
	return AtomSessionCompletedPayload{
		SessionID:   "as-1",
		AtomID:      "atom-1",
		LearnerGCID: dwGCID,
		TenantID:    dwTenant,
		IsCorrect:   correct,
	}
}

func dwMCQAtoms() *dwFakeAtoms {
	return &dwFakeAtoms{atoms: map[string]*atom_index.AtomIndex{
		"atom-1": {AtomID: "atom-1", AtomType: "mcq", CorrectOptionID: "b", TopicTags: []string{"fractions", "arithmetic"}},
		"atom-2": {AtomID: "atom-2", AtomType: "text", TopicTags: []string{"fractions"}},
	}}
}

func TestAtomSession_WrongBelowThreshold_UpsertsDerivedEdge(t *testing.T) {
	repo := &dwFakeRepo{}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"fractions": 0.4}}
	p := newDWProjector(repo, acc).WithAtomIndex(dwMCQAtoms())

	if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-20"), dwSessionPayload(false)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(acc.attempts) != 1 || acc.attempts[0].Topic != "fractions" {
		t.Fatalf("attempts = %+v (primary topic only)", acc.attempts)
	}
	if len(repo.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(repo.upserts))
	}
	if repo.upserts[0].Source != lw.SourceDerived {
		t.Fatalf("source = %q, want derived", repo.upserts[0].Source)
	}
}

func TestAtomSession_CorrectAboveThreshold_Recovers(t *testing.T) {
	repo := &dwFakeRepo{}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"fractions": 0.85}}
	p := newDWProjector(repo, acc).WithAtomIndex(dwMCQAtoms())

	if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-21"), dwSessionPayload(true)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.recovers) != 1 || repo.recovers[0].ConceptKey != "fractions" {
		t.Fatalf("recovers = %+v", repo.recovers)
	}
}

func TestAtomSession_NonMCQSkipped(t *testing.T) {
	repo := &dwFakeRepo{}
	acc := &dwFakeAccuracy{}
	p := newDWProjector(repo, acc).WithAtomIndex(dwMCQAtoms())

	payload := dwSessionPayload(false)
	payload.AtomID = "atom-2"
	if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-22"), payload); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(acc.attempts) != 0 || len(repo.upserts) != 0 {
		t.Fatal("non-MCQ atoms are not gradable")
	}
}

func TestAtomSession_MissingAtomAcked(t *testing.T) {
	repo := &dwFakeRepo{}
	acc := &dwFakeAccuracy{}
	p := newDWProjector(repo, acc).WithAtomIndex(dwMCQAtoms())

	payload := dwSessionPayload(false)
	payload.AtomID = "atom-unknown"
	if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-23"), payload); err != nil {
		t.Fatalf("out-of-order projection must ack, got %v", err)
	}
	if len(acc.attempts) != 0 {
		t.Fatal("missing atom — nothing recorded")
	}
}

func TestAtomSession_NoAtomIndexWired_Acked(t *testing.T) {
	repo := &dwFakeRepo{}
	acc := &dwFakeAccuracy{}
	p := newDWProjector(repo, acc) // no atom index

	if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-24"), dwSessionPayload(false)); err != nil {
		t.Fatalf("no index wired must ack, got %v", err)
	}
	if len(acc.attempts) != 0 {
		t.Fatal("nothing should be recorded without the index")
	}
}

// --- error propagation (NACK paths) ---

func TestProjector_ErrorPathsNack(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name string
		mut  func(repo *dwFakeRepo, acc *dwFakeAccuracy, emb *lwFakeEmbedder)
	}{
		{"record attempt fails", func(_ *dwFakeRepo, acc *dwFakeAccuracy, _ *lwFakeEmbedder) { acc.recordErr = boom }},
		{"accuracy read fails", func(_ *dwFakeRepo, acc *dwFakeAccuracy, _ *lwFakeEmbedder) { acc.getErr = boom }},
		{"embed fails", func(_ *dwFakeRepo, _ *dwFakeAccuracy, emb *lwFakeEmbedder) { emb.err = boom }},
		{"upsert fails", func(repo *dwFakeRepo, _ *dwFakeAccuracy, _ *lwFakeEmbedder) { repo.upsertErr = boom }},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &dwFakeRepo{}
			acc := &dwFakeAccuracy{accuracy: map[string]float64{"multiplication tables": 0.2}}
			emb := &lwFakeEmbedder{vec: []float32{0.1}}
			tc.mut(repo, acc, emb)
			p := NewDerivedWeaknessProjector(repo, emb, acc)
			err := p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv(fmt.Sprintf("ev-err-%d", i)), dwScorePayload(false))
			if err == nil {
				t.Fatal("transient failure must surface (Pub/Sub NACK)")
			}
		})
	}
}

func TestLiveQuiz_RecoverErrorNacks(t *testing.T) {
	repo := &dwFakeRepo{recoverErr: errors.New("boom")}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"multiplication tables": 0.95}}
	p := newDWProjector(repo, acc)
	if err := p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("ev-err-rec"), dwScorePayload(true)); err == nil {
		t.Fatal("recover failure must surface")
	}
}

func TestAtomSession_ErrorPathsNack(t *testing.T) {
	boom := errors.New("boom")
	t.Run("invalid envelope", func(t *testing.T) {
		p := newDWProjector(&dwFakeRepo{}, &dwFakeAccuracy{}).WithAtomIndex(dwMCQAtoms())
		env := dwEnv("ev-as-bad")
		env.Traceparent = ""
		if err := p.HandleAtomSessionCompleted(context.Background(), env, dwSessionPayload(false)); err == nil {
			t.Fatal("invalid envelope must error")
		}
	})
	t.Run("session dedup store fails", func(t *testing.T) {
		acc := &dwFakeAccuracy{markErr: boom}
		p := newDWProjector(&dwFakeRepo{}, acc).WithAtomIndex(dwMCQAtoms())
		if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-as-mark"), dwSessionPayload(false)); err == nil {
			t.Fatal("dedup store failure must surface")
		}
	})
	t.Run("record fails", func(t *testing.T) {
		acc := &dwFakeAccuracy{recordErr: boom}
		p := newDWProjector(&dwFakeRepo{}, acc).WithAtomIndex(dwMCQAtoms())
		if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-as-rec"), dwSessionPayload(false)); err == nil {
			t.Fatal("record failure must surface")
		}
	})
	t.Run("duplicate event id acks", func(t *testing.T) {
		acc := &dwFakeAccuracy{accuracy: map[string]float64{"fractions": 0.2}}
		p := newDWProjector(&dwFakeRepo{}, acc).WithAtomIndex(dwMCQAtoms())
		if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-as-dup"), dwSessionPayload(false)); err != nil {
			t.Fatalf("first: %v", err)
		}
		if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-as-dup"), dwSessionPayload(false)); err != nil {
			t.Fatalf("dup: %v", err)
		}
		if len(acc.attempts) != 1 {
			t.Fatalf("attempts = %d, want 1", len(acc.attempts))
		}
	})
	t.Run("unclassified atom acks", func(t *testing.T) {
		atoms := &dwFakeAtoms{atoms: map[string]*atom_index.AtomIndex{
			"atom-1": {AtomID: "atom-1", AtomType: "mcq", CorrectOptionID: "b"}, // no topic tags
		}}
		acc := &dwFakeAccuracy{}
		p := newDWProjector(&dwFakeRepo{}, acc).WithAtomIndex(atoms)
		if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-as-topicless"), dwSessionPayload(false)); err != nil {
			t.Fatalf("unclassified atom must ack, got %v", err)
		}
		if len(acc.attempts) != 0 {
			t.Fatal("nothing should record for a topicless atom")
		}
	})
	t.Run("retention blends on derived path", func(t *testing.T) {
		repo := &dwFakeRepo{}
		acc := &dwFakeAccuracy{accuracy: map[string]float64{"fractions": 0.4}}
		p := newDWProjector(repo, acc).WithAtomIndex(dwMCQAtoms()).
			WithRetention(&dwFakeRetention{byTopic: map[string]float64{"fractions": 0.3}})
		if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-as-ret"), dwSessionPayload(false)); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		if want := lw.DerivedStrength(0.4, 0.3, true); len(repo.upserts) != 1 || repo.upserts[0].Strength != want {
			t.Fatalf("upserts = %+v, want strength %v", repo.upserts, want)
		}
	})
}

// The pg dedup column (migrations/0004 topic_accuracy_session_dedup.
// atom_session_id) is UUID-typed — the derived projector's namespaced dedup
// key MUST therefore be a valid RFC-4122 UUID, deterministic across
// redeliveries, and distinct from the raw session id (which
// TopicAccuracySubscriber dedups in the same table for the same completion).
// A bare "derived:"+id string 22P02s at the pg layer (caught live 2026-06-10).
func TestAtomSession_DedupKeyIsNamespacedUUID(t *testing.T) {
	repo := &dwFakeRepo{}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"fractions": 0.2}}
	p := newDWProjector(repo, acc).WithAtomIndex(dwMCQAtoms())

	if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-27"), dwSessionPayload(false)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	raw := dwSessionPayload(false).SessionID
	if len(acc.seenSessions) != 1 {
		t.Fatalf("seenSessions = %d, want 1", len(acc.seenSessions))
	}
	var got string
	for k := range acc.seenSessions {
		parts := strings.Split(k, "|")
		got = parts[len(parts)-1]
	}
	if _, err := uuid.Parse(got); err != nil {
		t.Fatalf("dedup key %q is not a valid uuid (pg column is UUID-typed): %v", got, err)
	}
	if got == raw {
		t.Fatalf("dedup key must be namespaced away from the raw session id %q", raw)
	}
	if got != derivedSessionDedupID(raw) {
		t.Fatalf("dedup key %q not deterministic (want %q)", got, derivedSessionDedupID(raw))
	}
}

func TestAtomSession_SessionDedupViaMarkSessionSeen(t *testing.T) {
	repo := &dwFakeRepo{}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"fractions": 0.2}}
	p := newDWProjector(repo, acc).WithAtomIndex(dwMCQAtoms())

	if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-25"), dwSessionPayload(false)); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Republished with a fresh event_id but the same session — must not double-count.
	if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-26"), dwSessionPayload(false)); err != nil {
		t.Fatalf("dup session: %v", err)
	}
	if len(acc.attempts) != 1 {
		t.Fatalf("attempts = %+v, want 1", acc.attempts)
	}
}
