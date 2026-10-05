// goal_learning_edges_weakness_test.go — CHO-2043: a ceremony REMEDIATE
// learning-edge also mints an unevidenced learner_weakness so the map overlay,
// the growth-edges list, and the drawer diagnosis share one backing. Explore
// selections mint no weakness. RED-first.
package http_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// ceremonyWeaknessRepo captures Upserts; the other Repository methods are inert.
type ceremonyWeaknessRepo struct{ upserts []lw.UpsertInput }

func (f *ceremonyWeaknessRepo) Upsert(_ context.Context, in lw.UpsertInput) (lw.UpsertResult, error) {
	f.upserts = append(f.upserts, in)
	return lw.UpsertResult{}, nil
}
func (f *ceremonyWeaknessRepo) List(context.Context, lw.ListQuery) (lw.ListResult, error) {
	return lw.ListResult{}, nil
}
func (f *ceremonyWeaknessRepo) ListAll(context.Context, lw.ListQuery) ([]lw.LearnerWeakness, error) {
	return nil, nil
}
func (f *ceremonyWeaknessRepo) Get(context.Context, string, string) (*lw.LearnerWeakness, error) {
	return nil, nil
}
func (f *ceremonyWeaknessRepo) SoftDelete(context.Context, string, string, time.Time) error {
	return nil
}
func (f *ceremonyWeaknessRepo) RecoverByConceptKey(context.Context, string, string, float64, time.Time) ([]lw.GrownEdge, error) {
	return nil, nil
}
func (f *ceremonyWeaknessRepo) RecoverByDrillAtomID(context.Context, string, string, float64, time.Time) ([]lw.GrownEdge, error) {
	return nil, nil
}
func (f *ceremonyWeaknessRepo) SetCachedDrillAtoms(context.Context, string, string, []string, time.Time) error {
	return nil
}

// ceremonyEmbedder records the titles it was asked to embed; returns a fixed
// non-empty vector (New rejects an empty embedding).
type ceremonyEmbedder struct{ calls []string }

func (f *ceremonyEmbedder) Embed(_ context.Context, text, _ string) ([]float32, error) {
	f.calls = append(f.calls, text)
	return []float32{0.1, 0.2, 0.3}, nil
}

func TestLearningEdges_RemediateMintsCeremonyWeakness(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, RootConceptID: lePtr(leRoot)})
	srv := goalServer(repo)
	srv.LearningEdges = &fakeLearningEdgeApplier{}
	lwRepo := &ceremonyWeaknessRepo{}
	emb := &ceremonyEmbedder{}
	srv.LearnerWeakness = lwRepo
	srv.WeaknessEmbedder = emb

	body := map[string]any{"edges": []map[string]any{
		{"title": "Adding fractions", "intent": "remediate"},
		{"title": "Continued fractions", "intent": "explore"},
	}}
	w := doGoal(srv, goalReq("POST", learningEdgesPath(g.GoalID), body))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
	}

	// Exactly one weakness upserted — for the REMEDIATE edge only.
	if len(lwRepo.upserts) != 1 {
		t.Fatalf("weakness upserts = %d, want 1 (remediate only)", len(lwRepo.upserts))
	}
	up := lwRepo.upserts[0]
	if up.ConceptLabel != "Adding fractions" {
		t.Fatalf("upsert label = %q, want %q", up.ConceptLabel, "Adding fractions")
	}
	if up.Source != lw.SourceCeremony {
		t.Fatalf("upsert source = %q, want ceremony", up.Source)
	}
	if up.Strength != lw.CeremonySeedStrength || len(up.Embedding) == 0 {
		t.Fatalf("seed mis-set: strength=%v emb_len=%d", up.Strength, len(up.Embedding))
	}
	// The embedder saw only the remediate title (explore is not embedded).
	if len(emb.calls) != 1 || emb.calls[0] != "Adding fractions" {
		t.Fatalf("embedder calls = %v, want [Adding fractions]", emb.calls)
	}
}
