// weakness_companion_rag_test.go — specs for the Companion-RAG coaching hook
// (ADR-205 D5 / CHO-1966): when a learner opted into familiar_coaching at the
// bounded HITL review, the analysed diagnosis is written into their Companion's
// pgvector memory (ADR-173). Reuses lwEnv / lwPayload / lwFakeRepo /
// lwFakeEmbedder from weakness_analyzed_subscriber_test.go.
package subscribers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

var errTest = errors.New("subscribers: test error")

// --- fakes for the Companion-RAG ports ---

type fakeCompanionResolver struct {
	id  string
	err error
}

func (f *fakeCompanionResolver) ResolveActiveCompanion(_ context.Context, _, _ string) (string, error) {
	return f.id, f.err
}

type fakeCompanionEmbedder struct {
	gotInput companion.EmbedInput
	vec      []float32
	err      error
	calls    int
}

func (e *fakeCompanionEmbedder) Embed(_ context.Context, in companion.EmbedInput) ([]float32, error) {
	e.calls++
	e.gotInput = in
	return e.vec, e.err
}

type fakeCompanionMemory struct {
	recorded []companion.RecordMemoryInput
	err      error
}

func (m *fakeCompanionMemory) Recall(context.Context, string, []float32, int) ([]companion.MemoryRow, error) {
	return nil, nil
}

func (m *fakeCompanionMemory) Record(_ context.Context, in companion.RecordMemoryInput) error {
	m.recorded = append(m.recorded, in)
	return m.err
}

func fixedClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 6, 9, 12, 0, 6, 0, time.UTC) }
}

// --- the recorder hook (NewCompanionRAGHook) ---

func TestCompanionRAGHook_RecordsDiagnosisIntoCompanionMemory(t *testing.T) {
	res := &fakeCompanionResolver{id: "fam-1"}
	emb := &fakeCompanionEmbedder{vec: []float32{0.1, 0.2}}
	mem := &fakeCompanionMemory{}
	hook := NewCompanionRAGHook(res, emb, mem, "text-embedding-004", fixedClock())

	if err := hook(context.Background(), lwPayload()); err != nil {
		t.Fatalf("hook: %v", err)
	}
	if emb.calls != 1 || emb.gotInput.TaskType != companion.EmbedTaskDocument {
		t.Errorf("embed not called as RETRIEVAL_DOCUMENT: calls=%d type=%q", emb.calls, emb.gotInput.TaskType)
	}
	if !strings.Contains(emb.gotInput.Text, "causes of riverine flooding") ||
		!strings.Contains(emb.gotInput.Text, "tectonic plate boundaries") {
		t.Errorf("composed content missing concept labels: %q", emb.gotInput.Text)
	}
	if len(mem.recorded) != 1 {
		t.Fatalf("got %d memory records; want 1", len(mem.recorded))
	}
	rec := mem.recorded[0]
	if rec.CompanionID != "fam-1" || rec.MemoryType != "weakness_diagnosis" {
		t.Errorf("record routing wrong: companion=%q type=%q", rec.CompanionID, rec.MemoryType)
	}
	if rec.OwnerGCID != lwPayload().LearnerGCID || rec.TenantID != lwPayload().TenantID {
		t.Errorf("record identity wrong: %+v", rec)
	}
	if rec.SourceSessionID != "weakness:upl-1" {
		t.Errorf("provenance source = %q, want weakness:upl-1", rec.SourceSessionID)
	}
	if len(rec.Embedding) != 2 || rec.ModelID != "text-embedding-004" {
		t.Errorf("embedding/model not threaded: %+v", rec)
	}
}

func TestCompanionRAGHook_DropsWhenNoCompanion(t *testing.T) {
	emb := &fakeCompanionEmbedder{vec: []float32{0.1}}
	mem := &fakeCompanionMemory{}
	hook := NewCompanionRAGHook(&fakeCompanionResolver{err: ErrNoCompanion}, emb, mem, "m", fixedClock())

	if err := hook(context.Background(), lwPayload()); err != nil {
		t.Fatalf("ErrNoCompanion must be a silent drop, got %v", err)
	}
	if emb.calls != 0 || len(mem.recorded) != 0 {
		t.Errorf("no companion → must not embed/record: embed=%d record=%d", emb.calls, len(mem.recorded))
	}
}

func TestCompanionRAGHook_PropagatesEmbedError(t *testing.T) {
	emb := &fakeCompanionEmbedder{err: errTest}
	mem := &fakeCompanionMemory{}
	hook := NewCompanionRAGHook(&fakeCompanionResolver{id: "fam-1"}, emb, mem, "m", fixedClock())
	if err := hook(context.Background(), lwPayload()); err == nil {
		t.Fatal("expected embed error to propagate to the caller (subscriber soft-fails it)")
	}
	if len(mem.recorded) != 0 {
		t.Error("must not record when embed failed")
	}
}

// --- subscriber gating + soft-fail ---

// composeWeaknessDiagnosisMemory frames the recall memory by SOURCE: an
// analyser-born analysis references the learner's reviewed upload; a ceremony-
// born (CHO-2040 binding-ceremony remediate ticks) analysis has NO upload —
// every edge is tagged "ceremony" — so the memory must name the ceremony
// instead of falsely claiming a reviewed upload (effect (c) upload-less path).
func TestComposeWeaknessDiagnosisMemory_UploadVsCeremony(t *testing.T) {
	uploadBorn := composeWeaknessDiagnosisMemory([]ExtractedGrowthEdgePayload{
		{ConceptLabel: "Cell membranes", DescriptorJSON: `{"summary":"missed twice in graded work"}`},
	})
	if !strings.Contains(uploadBorn, "reviewed upload") {
		t.Errorf("analyser-born memory should reference the reviewed upload: %q", uploadBorn)
	}
	if !strings.Contains(uploadBorn, "Cell membranes") {
		t.Errorf("memory should carry the concept label: %q", uploadBorn)
	}

	ceremonyBorn := composeWeaknessDiagnosisMemory([]ExtractedGrowthEdgePayload{
		{
			ConceptLabel:   "Cell membranes",
			Tags:           []string{"ceremony"},
			DescriptorJSON: `{"summary":"the learner flagged this to remediate at the Companion binding ceremony"}`,
		},
	})
	if strings.Contains(ceremonyBorn, "reviewed upload") {
		t.Errorf("ceremony-born (upload-less) memory must NOT claim a reviewed upload: %q", ceremonyBorn)
	}
	if !strings.Contains(strings.ToLower(ceremonyBorn), "ceremony") {
		t.Errorf("ceremony-born memory should name the binding ceremony: %q", ceremonyBorn)
	}
	if !strings.Contains(ceremonyBorn, "Cell membranes") {
		t.Errorf("ceremony memory should carry the concept label: %q", ceremonyBorn)
	}
}

func TestWeaknessAnalyzed_CompanionRAG_InvokedOnlyWhenOptedIn(t *testing.T) {
	for _, tc := range []struct {
		name       string
		coaching   bool
		wantCalled bool
	}{
		{"opted-in", true, true},
		{"opted-out", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &lwFakeRepo{}
			emb := &lwFakeEmbedder{vec: []float32{0.1}}
			var calls int
			sub := newLWSub(repo, emb).WithCompanionRAGHook(func(context.Context, WeaknessAnalyzedPayload) error {
				calls++
				return nil
			})
			p := lwPayload()
			p.CompanionCoaching = tc.coaching
			if err := sub.Handle(context.Background(), lwEnv("evt-rag"), p); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if (calls == 1) != tc.wantCalled {
				t.Errorf("hook calls=%d, wantCalled=%v", calls, tc.wantCalled)
			}
		})
	}
}

func TestWeaknessAnalyzed_CompanionRAG_SoftFailsNeverNacksTheAnalysis(t *testing.T) {
	repo := &lwFakeRepo{}
	emb := &lwFakeEmbedder{vec: []float32{0.1}}
	sub := newLWSub(repo, emb).WithCompanionRAGHook(func(context.Context, WeaknessAnalyzedPayload) error {
		return errTest // coaching memory is best-effort (ADR-173 soft-fail)
	})
	p := lwPayload()
	p.CompanionCoaching = true
	if err := sub.Handle(context.Background(), lwEnv("evt-rag2"), p); err != nil {
		t.Fatalf("a Companion-RAG failure must NOT fail the analysis, got %v", err)
	}
	if len(repo.upserts) != 2 {
		t.Errorf("the diagnosis edges must still persist: got %d upserts", len(repo.upserts))
	}
}
