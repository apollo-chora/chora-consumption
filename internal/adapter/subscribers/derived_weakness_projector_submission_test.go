// derived_weakness_projector_submission_test.go — WS-6 (ADR-205, CHO-1958):
// the SECOND performance-evidence source folded into the Growth-Edge aggregate.
//
// chora.delivery.submission.graded.v1 carries a whole-submission accuracy
// (points_earned / points_possible) but NO per-atom topic, so this path keys a
// DERIVED edge on the assessment TITLE (the event's only concept-bearing signal)
// rather than on topic_accuracy read-backs. Below the mastery bar → Upsert;
// at/above → RecoverByConceptKey (Ebbinghaus auto-recovery, only ever lowers).
package subscribers

import (
	"context"
	"errors"
	"testing"

	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

func dwSubmission(earned float64, possible int) SubmissionGradedEvidence {
	return SubmissionGradedEvidence{
		SubmissionID:    "sub-1",
		AssessmentID:    "ass-1",
		AssessmentTitle: "Algebra Midterm",
		LearnerGCID:     dwGCID,
		TenantID:        dwTenant,
		PointsEarned:    earned,
		PointsPossible:  possible,
	}
}

func TestSubmission_BelowThreshold_UpsertsDerivedEdge(t *testing.T) {
	repo := &dwFakeRepo{}
	p := newDWProjector(repo, &dwFakeAccuracy{})

	// 4/10 = 0.40 accuracy < DerivedAccuracyThreshold (0.70) → mint/raise edge.
	if err := p.HandleSubmissionGraded(context.Background(), dwEnv("sg-1"), dwSubmission(4, 10)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(repo.upserts))
	}
	up := repo.upserts[0]
	if up.Source != lw.SourceDerived {
		t.Errorf("source = %q, want derived", up.Source)
	}
	if up.ConceptLabel != "Algebra Midterm" {
		t.Errorf("concept label = %q, want the assessment title", up.ConceptLabel)
	}
	if up.ConceptKey != "algebra-midterm" {
		t.Errorf("concept key = %q, want normalised slug", up.ConceptKey)
	}
	if want := lw.DerivedStrength(0.4, 0, false); up.Strength != want {
		t.Errorf("strength = %v, want accuracy-only %v", up.Strength, want)
	}
	if up.Descriptor.Summary == "" {
		t.Error("descriptor summary must explain the assessment signal")
	}
	if repo.tenants[0] != dwTenant {
		t.Error("Upsert ctx must carry the tenant for RLS")
	}
	if len(repo.recovers) != 0 {
		t.Error("below threshold must not recover")
	}
}

func TestSubmission_AtOrAboveThreshold_Recovers(t *testing.T) {
	repo := &dwFakeRepo{}
	p := newDWProjector(repo, &dwFakeAccuracy{})

	// 9/10 = 0.90 ≥ 0.70 → Ebbinghaus recovery of the matching edge.
	if err := p.HandleSubmissionGraded(context.Background(), dwEnv("sg-2"), dwSubmission(9, 10)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.upserts) != 0 {
		t.Fatalf("upserts = %d, want 0", len(repo.upserts))
	}
	if len(repo.recovers) != 1 {
		t.Fatalf("recovers = %d, want 1", len(repo.recovers))
	}
	rec := repo.recovers[0]
	if rec.ConceptKey != "algebra-midterm" {
		t.Errorf("recover concept_key = %q", rec.ConceptKey)
	}
	if want := lw.DerivedStrength(0.9, 0, false); rec.Strength != want {
		t.Errorf("recover strength = %v, want %v", rec.Strength, want)
	}
	if rec.CtxTenant != dwTenant {
		t.Error("Recover ctx must carry the tenant for RLS")
	}
}

func TestSubmission_NoTitle_Acked(t *testing.T) {
	repo := &dwFakeRepo{}
	p := newDWProjector(repo, &dwFakeAccuracy{})

	in := dwSubmission(2, 10)
	in.AssessmentTitle = "   " // no concept-bearing signal
	if err := p.HandleSubmissionGraded(context.Background(), dwEnv("sg-3"), in); err != nil {
		t.Fatalf("unmappable evidence must ack, got %v", err)
	}
	if len(repo.upserts) != 0 || len(repo.recovers) != 0 {
		t.Fatal("no title — nothing to key an edge on")
	}
}

func TestSubmission_ZeroPossible_Acked(t *testing.T) {
	repo := &dwFakeRepo{}
	p := newDWProjector(repo, &dwFakeAccuracy{})

	if err := p.HandleSubmissionGraded(context.Background(), dwEnv("sg-4"), dwSubmission(0, 0)); err != nil {
		t.Fatalf("no denominator must ack, got %v", err)
	}
	if len(repo.upserts) != 0 || len(repo.recovers) != 0 {
		t.Fatal("cannot score a zero-point submission")
	}
}

func TestSubmission_IdempotentReplay(t *testing.T) {
	repo := &dwFakeRepo{}
	p := newDWProjector(repo, &dwFakeAccuracy{})

	// Same event_id redelivered → dedup on envelope event_id.
	if err := p.HandleSubmissionGraded(context.Background(), dwEnv("sg-5"), dwSubmission(3, 10)); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := p.HandleSubmissionGraded(context.Background(), dwEnv("sg-5"), dwSubmission(3, 10)); err != nil {
		t.Fatalf("dup event_id: %v", err)
	}
	// Fresh event_id, SAME submission → domain dedup on submission_id.
	if err := p.HandleSubmissionGraded(context.Background(), dwEnv("sg-6"), dwSubmission(3, 10)); err != nil {
		t.Fatalf("dup submission: %v", err)
	}
	if len(repo.upserts) != 1 {
		t.Fatalf("upserts = %d, want exactly one processing", len(repo.upserts))
	}
}

func TestSubmission_EnvelopeIdentityFallback(t *testing.T) {
	repo := &dwFakeRepo{}
	p := newDWProjector(repo, &dwFakeAccuracy{})

	in := dwSubmission(2, 10)
	in.TenantID = ""
	in.LearnerGCID = ""
	if err := p.HandleSubmissionGraded(context.Background(), dwEnv("sg-7"), in); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.upserts) != 1 || repo.upserts[0].TenantID != dwTenant || repo.upserts[0].LearnerGCID != dwGCID {
		t.Fatalf("envelope identity fallback failed: %+v", repo.upserts)
	}
}

func TestSubmission_InvalidEnvelopeRejected(t *testing.T) {
	p := newDWProjector(&dwFakeRepo{}, &dwFakeAccuracy{})
	env := dwEnv("sg-8")
	env.TenantID = ""
	if err := p.HandleSubmissionGraded(context.Background(), env, dwSubmission(2, 10)); err == nil {
		t.Fatal("invalid envelope must error")
	}
}

func TestSubmission_ErrorPathsNack(t *testing.T) {
	boom := errors.New("boom")

	t.Run("embed fails", func(t *testing.T) {
		emb := &lwFakeEmbedder{err: boom}
		p := NewDerivedWeaknessProjector(&dwFakeRepo{}, emb, &dwFakeAccuracy{})
		if err := p.HandleSubmissionGraded(context.Background(), dwEnv("sg-e1"), dwSubmission(2, 10)); err == nil {
			t.Fatal("embed failure must surface (NACK)")
		}
	})
	t.Run("upsert fails", func(t *testing.T) {
		repo := &dwFakeRepo{upsertErr: boom}
		p := newDWProjector(repo, &dwFakeAccuracy{})
		if err := p.HandleSubmissionGraded(context.Background(), dwEnv("sg-e2"), dwSubmission(2, 10)); err == nil {
			t.Fatal("upsert failure must surface (NACK)")
		}
	})
	t.Run("recover fails", func(t *testing.T) {
		repo := &dwFakeRepo{recoverErr: boom}
		p := newDWProjector(repo, &dwFakeAccuracy{})
		if err := p.HandleSubmissionGraded(context.Background(), dwEnv("sg-e3"), dwSubmission(9, 10)); err == nil {
			t.Fatal("recover failure must surface (NACK)")
		}
	})
}
