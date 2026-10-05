// weakness_analyzed_subscriber_test.go — W3 (explicit source) of the 1b track.
// The subscriber consumes chora.consumption.weakness.analyzed.v1 (emitted by the
// ai-kernel weakness-analyser), embeds each extracted concept, and upserts it
// into the LearnerWeakness aggregate (source=explicit; the repo applies the
// cos>=0.9 dedup fold). Verified with fake repo + fake embedder.
package subscribers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// --- fakes ---

type lwFakeRepo struct {
	upserts   []lw.UpsertInput
	ctxTenant []string // tracing tenant on each Upsert ctx (RLS pre-req)
	err       error
}

func (r *lwFakeRepo) Upsert(ctx context.Context, in lw.UpsertInput) (lw.UpsertResult, error) {
	r.upserts = append(r.upserts, in)
	r.ctxTenant = append(r.ctxTenant, tracing.TenantIDFromContext(ctx))
	if r.err != nil {
		return lw.UpsertResult{}, r.err
	}
	return lw.UpsertResult{ID: "ge-" + in.ConceptKey, Merged: false}, nil
}
func (r *lwFakeRepo) List(context.Context, lw.ListQuery) (lw.ListResult, error) {
	return lw.ListResult{}, nil
}
func (r *lwFakeRepo) ListAll(context.Context, lw.ListQuery) ([]lw.LearnerWeakness, error) {
	return nil, nil
}
func (r *lwFakeRepo) Get(context.Context, string, string) (*lw.LearnerWeakness, error) {
	return nil, nil
}
func (r *lwFakeRepo) SoftDelete(context.Context, string, string, time.Time) error { return nil }
func (r *lwFakeRepo) RecoverByConceptKey(context.Context, string, string, float64, time.Time) ([]lw.GrownEdge, error) {
	return nil, nil
}
func (r *lwFakeRepo) RecoverByDrillAtomID(context.Context, string, string, float64, time.Time) ([]lw.GrownEdge, error) {
	return nil, nil
}
func (r *lwFakeRepo) SetCachedDrillAtoms(context.Context, string, string, []string, time.Time) error {
	return nil
}

type lwFakeEmbedder struct {
	calls []string
	vec   []float32
	err   error
}

func (e *lwFakeEmbedder) Embed(_ context.Context, text, _ string) ([]float32, error) {
	e.calls = append(e.calls, text)
	if e.err != nil {
		return nil, e.err
	}
	return e.vec, e.err
}

func lwEnv(eventID string) events.Envelope {
	return events.Envelope{
		EventID:        eventID,
		IdempotencyKey: "weakness.analyzed.upl-1",
		TenantID:       "01970000-0000-7000-8000-000000000001",
		GCID:           "01970000-0000-7000-9000-000000000001",
		OccurredAt:     time.Date(2026, 6, 9, 12, 0, 5, 0, time.UTC),
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		SourceProject:  "chora-489812",
		SourceService:  "chora-ai-kernel-orchestrator",
		SchemaVersion:  1,
	}
}

func lwPayload() WeaknessAnalyzedPayload {
	return WeaknessAnalyzedPayload{
		UploadID:    "upl-1",
		TenantID:    "01970000-0000-7000-8000-000000000001",
		LearnerGCID: "01970000-0000-7000-9000-000000000001",
		ModelUsed:   "gemini-3-pro",
		Edges: []ExtractedGrowthEdgePayload{
			{
				ConceptLabel:   "causes of riverine flooding",
				ConceptKey:     "causes-of-riverine-flooding",
				Category:       "physical-geography",
				Tags:           []string{"flooding"},
				Confidence:     0.9,
				Strength:       0.8,
				DescriptorJSON: `{"summary":"confuses fluvial vs pluvial","misconceptions":["all flooding is rain"]}`,
			},
			{
				ConceptLabel: "tectonic plate boundaries",
				ConceptKey:   "tectonic-plate-boundaries",
				Strength:     0.6,
			},
		},
	}
}

func newLWSub(repo *lwFakeRepo, emb *lwFakeEmbedder) *WeaknessAnalyzedSubscriber {
	return NewWeaknessAnalyzedSubscriber(repo, emb)
}

func TestWeaknessAnalyzed_UpsertsEachEdgeAsExplicit(t *testing.T) {
	repo := &lwFakeRepo{}
	emb := &lwFakeEmbedder{vec: []float32{0.1, -0.2, 0.3}}
	if err := newLWSub(repo, emb).Handle(context.Background(), lwEnv("evt-1"), lwPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.upserts) != 2 {
		t.Fatalf("got %d upserts; want 2", len(repo.upserts))
	}
	if emb.calls[0] != "causes of riverine flooding" {
		t.Errorf("embedded text[0] = %q", emb.calls[0])
	}
	u0 := repo.upserts[0]
	if u0.Source != lw.SourceExplicit {
		t.Errorf("source = %q; want explicit", u0.Source)
	}
	if u0.TenantID != "01970000-0000-7000-8000-000000000001" || u0.LearnerGCID != "01970000-0000-7000-9000-000000000001" {
		t.Errorf("tenant/gcid not threaded: %+v", u0)
	}
	if len(u0.Embedding) != 3 {
		t.Errorf("embedding not attached: %v", u0.Embedding)
	}
	if u0.ConceptKey != "causes-of-riverine-flooding" || u0.Strength != 0.8 {
		t.Errorf("edge fields wrong: %+v", u0)
	}
	if u0.Descriptor.Summary != "confuses fluvial vs pluvial" || len(u0.Descriptor.Misconceptions) != 1 {
		t.Errorf("descriptor not parsed: %+v", u0.Descriptor)
	}
	if !u0.Now.Equal(lwEnv("evt-1").OccurredAt) {
		t.Errorf("Now not from envelope occurred_at: %v", u0.Now)
	}
	// RLS pre-req: tenant set on the Upsert ctx.
	if repo.ctxTenant[0] != "01970000-0000-7000-8000-000000000001" {
		t.Errorf("tenant not on ctx for RLS: %q", repo.ctxTenant[0])
	}
}

func TestWeaknessAnalyzed_DedupsByEventID(t *testing.T) {
	repo := &lwFakeRepo{}
	emb := &lwFakeEmbedder{vec: []float32{0.1}}
	sub := newLWSub(repo, emb)
	_ = sub.Handle(context.Background(), lwEnv("evt-dup"), lwPayload())
	_ = sub.Handle(context.Background(), lwEnv("evt-dup"), lwPayload()) // same event_id
	if len(repo.upserts) != 2 {
		t.Errorf("redelivery double-processed: %d upserts (want 2)", len(repo.upserts))
	}
}

func TestWeaknessAnalyzed_DedupsByUploadID(t *testing.T) {
	repo := &lwFakeRepo{}
	emb := &lwFakeEmbedder{vec: []float32{0.1}}
	sub := newLWSub(repo, emb)
	_ = sub.Handle(context.Background(), lwEnv("evt-a"), lwPayload())
	_ = sub.Handle(context.Background(), lwEnv("evt-b"), lwPayload()) // fresh event_id, same upload_id
	if len(repo.upserts) != 2 {
		t.Errorf("same upload_id re-analysed double-processed: %d upserts (want 2)", len(repo.upserts))
	}
}

func TestWeaknessAnalyzed_EmptyEdgesNoOp(t *testing.T) {
	repo := &lwFakeRepo{}
	emb := &lwFakeEmbedder{vec: []float32{0.1}}
	p := lwPayload()
	p.Edges = nil
	if err := newLWSub(repo, emb).Handle(context.Background(), lwEnv("evt-empty"), p); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.upserts) != 0 {
		t.Errorf("empty edges produced %d upserts", len(repo.upserts))
	}
}

func TestWeaknessAnalyzed_PropagatesEmbedError(t *testing.T) {
	repo := &lwFakeRepo{}
	emb := &lwFakeEmbedder{err: errors.New("vertex down")}
	if err := newLWSub(repo, emb).Handle(context.Background(), lwEnv("evt-e"), lwPayload()); err == nil {
		t.Error("expected embed error to propagate (NACK -> retry)")
	}
}

func TestWeaknessAnalyzed_PropagatesUpsertError(t *testing.T) {
	repo := &lwFakeRepo{err: errors.New("pg down")}
	emb := &lwFakeEmbedder{vec: []float32{0.1}}
	if err := newLWSub(repo, emb).Handle(context.Background(), lwEnv("evt-u"), lwPayload()); err == nil {
		t.Error("expected upsert error to propagate")
	}
}

func TestWeaknessAnalyzed_RejectsBadEnvelope(t *testing.T) {
	repo := &lwFakeRepo{}
	emb := &lwFakeEmbedder{vec: []float32{0.1}}
	bad := lwEnv("")
	if err := newLWSub(repo, emb).Handle(context.Background(), bad, lwPayload()); err == nil {
		t.Error("expected bad-envelope error")
	}
}

func TestWeaknessAnalyzed_WithStoreDefaults(t *testing.T) {
	// nil store + zero ttl must fall back to safe defaults and still process.
	repo := &lwFakeRepo{}
	sub := NewWeaknessAnalyzedSubscriberWithStore(repo, &lwFakeEmbedder{vec: []float32{0.1}}, nil, 0)
	if err := sub.Handle(context.Background(), lwEnv("evt-def"), lwPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.upserts) != 2 {
		t.Errorf("got %d upserts; want 2", len(repo.upserts))
	}
}

func TestWeaknessAnalyzed_SkipsBlankConceptKey(t *testing.T) {
	repo := &lwFakeRepo{}
	emb := &lwFakeEmbedder{vec: []float32{0.1}}
	p := lwPayload()
	p.Edges = []ExtractedGrowthEdgePayload{{ConceptLabel: "", ConceptKey: "", Strength: 0.5}}
	if err := newLWSub(repo, emb).Handle(context.Background(), lwEnv("evt-blank"), p); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.upserts) != 0 {
		t.Errorf("blank-label edge should be skipped; got %d upserts", len(repo.upserts))
	}
}

// --- W8b-3: upload-job completion ---

type fakeJobCompleter struct {
	uploadID string
	edgeIDs  []string
	called   bool
	err      error
}

func (f *fakeJobCompleter) MarkCompleted(_ context.Context, _, uploadID string, edgeIDs []string, _ time.Time) error {
	f.called = true
	f.uploadID = uploadID
	f.edgeIDs = edgeIDs
	return f.err
}

func TestWeaknessAnalyzed_MarksJobCompletedWithEdgeIDs(t *testing.T) {
	repo := &lwFakeRepo{}
	emb := &lwFakeEmbedder{vec: []float32{0.1}}
	jc := &fakeJobCompleter{}
	sub := newLWSub(repo, emb).WithJobCompleter(jc)

	if err := sub.Handle(context.Background(), lwEnv("evt-jc"), lwPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !jc.called || jc.uploadID != "upl-1" {
		t.Fatalf("MarkCompleted not called for upl-1: %+v", jc)
	}
	// fake repo returns ID "ge-"+ConceptKey for each upserted edge (2 here).
	if len(jc.edgeIDs) != 2 || jc.edgeIDs[0] != "ge-causes-of-riverine-flooding" {
		t.Errorf("edge ids = %v", jc.edgeIDs)
	}
}

func TestWeaknessAnalyzed_MarksJobCompleted_EmptyEdges(t *testing.T) {
	repo := &lwFakeRepo{}
	jc := &fakeJobCompleter{}
	p := lwPayload()
	p.Edges = nil // nothing weak detected — job still completes
	sub := newLWSub(repo, &lwFakeEmbedder{vec: []float32{0.1}}).WithJobCompleter(jc)

	if err := sub.Handle(context.Background(), lwEnv("evt-empty"), p); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !jc.called || len(jc.edgeIDs) != 0 {
		t.Fatalf("empty-edges job must still complete with 0 ids: %+v", jc)
	}
}

func TestWeaknessAnalyzed_JobCompleterError_Nacks(t *testing.T) {
	jc := &fakeJobCompleter{err: errors.New("mark boom")}
	sub := newLWSub(&lwFakeRepo{}, &lwFakeEmbedder{vec: []float32{0.1}}).WithJobCompleter(jc)
	if err := sub.Handle(context.Background(), lwEnv("evt-jcerr"), lwPayload()); err == nil {
		t.Fatal("job-completer error must surface (NACK)")
	}
}
