package clients

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

type fakeCompanionEmbedder struct {
	lastIn companion.EmbedInput
	vec    []float32
	err    error
}

func (f *fakeCompanionEmbedder) Embed(_ context.Context, in companion.EmbedInput) ([]float32, error) {
	f.lastIn = in
	return f.vec, f.err
}

func TestLearnerWeaknessEmbedder_DelegatesAsDocument(t *testing.T) {
	fake := &fakeCompanionEmbedder{vec: []float32{0.1, 0.2, 0.3}}
	e := NewLearnerWeaknessEmbedder(fake)

	got, err := e.Embed(context.Background(), "causes of riverine flooding", "tenant-1")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 3 || got[0] != 0.1 {
		t.Errorf("vector not passed through: %v", got)
	}
	if fake.lastIn.Text != "causes of riverine flooding" {
		t.Errorf("text = %q", fake.lastIn.Text)
	}
	if fake.lastIn.TenantID != "tenant-1" {
		t.Errorf("tenant = %q", fake.lastIn.TenantID)
	}
	// Stored concepts embed as RETRIEVAL_DOCUMENT (mirrors atom/memory storage).
	if fake.lastIn.TaskType != companion.EmbedTaskDocument {
		t.Errorf("task type = %q; want %q", fake.lastIn.TaskType, companion.EmbedTaskDocument)
	}
}

func TestLearnerWeaknessEmbedder_PropagatesError(t *testing.T) {
	fake := &fakeCompanionEmbedder{err: errors.New("vertex down")}
	e := NewLearnerWeaknessEmbedder(fake)
	if _, err := e.Embed(context.Background(), "x", "t"); err == nil {
		t.Error("expected embed error to propagate")
	}
}
