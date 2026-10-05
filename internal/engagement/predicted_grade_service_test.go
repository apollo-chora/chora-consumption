// Package engagement — PredictedGradeService unit tests (M12.2 Batch 3 consolidation).
//
// The PredictedGradeService stores and retrieves ML-predicted grades on behalf
// of the Retention Predictor crew. The crew code itself lives in
// chora-ai-kernel; this service is the pure-domain storage abstraction.
//
// RED → GREEN → REFACTOR per `feedback_strict_tdd`. Re-derived from the
// original chora-engagement test surface (the original predicted grade tests
// lived under `agent_*` test files which were intentionally excluded from
// chora-consumption because they covered LLM-coupled agent code — see report).
package engagement

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// mockPredictedGradeRepo is a testify mock implementing PredictedGradeRepository.
type mockPredictedGradeRepo struct{ mock.Mock }

// Compile-time interface check.
var _ PredictedGradeRepository = (*mockPredictedGradeRepo)(nil)

func (m *mockPredictedGradeRepo) ListByGCID(ctx context.Context, gcid, tenantID uuid.UUID, topicID *uuid.UUID) ([]PredictedGrade, error) {
	args := m.Called(ctx, gcid, tenantID, topicID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]PredictedGrade), args.Error(1)
}

func (m *mockPredictedGradeRepo) Upsert(ctx context.Context, grade *PredictedGrade) error {
	return m.Called(ctx, grade).Error(0)
}

func TestPredictedGradeService_GetPredictions(t *testing.T) {
	ctx := context.Background()
	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("returns predictions for learner", func(t *testing.T) {
		repo := &mockPredictedGradeRepo{}
		svc := NewPredictedGradeService(repo)

		want := []PredictedGrade{{
			GCID:           gcid,
			TenantID:       tenantID,
			PredictedScore: 75,
			Confidence:     0.85,
			ModelVersion:   "v1",
		}}
		repo.On("ListByGCID", ctx, gcid, tenantID, (*uuid.UUID)(nil)).Return(want, nil)

		got, err := svc.GetPredictions(ctx, gcid, tenantID, nil)

		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, 75.0, got[0].PredictedScore)
	})

	t.Run("filters by topic id when provided", func(t *testing.T) {
		repo := &mockPredictedGradeRepo{}
		svc := NewPredictedGradeService(repo)

		topicID := uuid.Must(uuid.NewV7())
		repo.On("ListByGCID", ctx, gcid, tenantID, &topicID).Return([]PredictedGrade{}, nil)

		got, err := svc.GetPredictions(ctx, gcid, tenantID, &topicID)

		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("propagates repo errors", func(t *testing.T) {
		repo := &mockPredictedGradeRepo{}
		svc := NewPredictedGradeService(repo)

		repo.On("ListByGCID", ctx, gcid, tenantID, (*uuid.UUID)(nil)).Return(nil, errors.New("db error"))

		_, err := svc.GetPredictions(ctx, gcid, tenantID, nil)

		require.Error(t, err)
	})
}

func TestPredictedGradeService_StorePrediction(t *testing.T) {
	ctx := context.Background()
	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	topicID := uuid.Must(uuid.NewV7())

	t.Run("upserts a valid prediction", func(t *testing.T) {
		repo := &mockPredictedGradeRepo{}
		svc := NewPredictedGradeService(repo)

		repo.On("Upsert", ctx, mock.AnythingOfType("*engagement.PredictedGrade")).Return(nil)

		got, err := svc.StorePrediction(ctx, gcid, tenantID, topicID, "Algebra", 80.5, 0.92, "v2")

		require.NoError(t, err)
		require.Equal(t, "Algebra", got.TopicName)
		require.Equal(t, 80.5, got.PredictedScore)
		require.Equal(t, 0.92, got.Confidence)
		require.Equal(t, "v2", got.ModelVersion)
		require.NotEqual(t, uuid.Nil, got.ID)
	})

	t.Run("rejects score below 0", func(t *testing.T) {
		repo := &mockPredictedGradeRepo{}
		svc := NewPredictedGradeService(repo)

		_, err := svc.StorePrediction(ctx, gcid, tenantID, topicID, "Algebra", -1, 0.5, "v1")

		require.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("rejects score above 100", func(t *testing.T) {
		repo := &mockPredictedGradeRepo{}
		svc := NewPredictedGradeService(repo)

		_, err := svc.StorePrediction(ctx, gcid, tenantID, topicID, "Algebra", 101, 0.5, "v1")

		require.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("rejects confidence below 0", func(t *testing.T) {
		repo := &mockPredictedGradeRepo{}
		svc := NewPredictedGradeService(repo)

		_, err := svc.StorePrediction(ctx, gcid, tenantID, topicID, "Algebra", 50, -0.1, "v1")

		require.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("rejects confidence above 1", func(t *testing.T) {
		repo := &mockPredictedGradeRepo{}
		svc := NewPredictedGradeService(repo)

		_, err := svc.StorePrediction(ctx, gcid, tenantID, topicID, "Algebra", 50, 1.1, "v1")

		require.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("propagates upsert errors", func(t *testing.T) {
		repo := &mockPredictedGradeRepo{}
		svc := NewPredictedGradeService(repo)

		repo.On("Upsert", ctx, mock.AnythingOfType("*engagement.PredictedGrade")).Return(errors.New("upsert failed"))

		_, err := svc.StorePrediction(ctx, gcid, tenantID, topicID, "Algebra", 50, 0.5, "v1")

		require.Error(t, err)
	})
}
