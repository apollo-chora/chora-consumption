package survey

import (
	"context"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
)

// Compile-time interface checks.
var (
	_ SurveyTemplateRepository = (*mockSurveyRepo)(nil)
	_ SurveyQuestionRepository = (*mockQuestionRepo)(nil)
	_ SurveyResponseRepository = (*mockResponseRepo)(nil)
	_ EventPublisher           = (*mockEventPublisher)(nil)
)

// ---------------------------------------------------------------------------
// mockSurveyRepo — SurveyTemplateRepository
// ---------------------------------------------------------------------------

type mockSurveyRepo struct{ mock.Mock }

func (m *mockSurveyRepo) Create(ctx context.Context, survey *SurveyTemplate) error {
	return m.Called(ctx, survey).Error(0)
}

func (m *mockSurveyRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*SurveyTemplate, error) {
	args := m.Called(ctx, id, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*SurveyTemplate), args.Error(1)
}

func (m *mockSurveyRepo) List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]SurveyTemplate, error) {
	args := m.Called(ctx, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]SurveyTemplate), args.Error(1)
}

func (m *mockSurveyRepo) Update(ctx context.Context, survey *SurveyTemplate) error {
	return m.Called(ctx, survey).Error(0)
}

func (m *mockSurveyRepo) Delete(ctx context.Context, id, tenantID uuid.UUID) error {
	return m.Called(ctx, id, tenantID).Error(0)
}

// ---------------------------------------------------------------------------
// mockQuestionRepo — SurveyQuestionRepository
// ---------------------------------------------------------------------------

type mockQuestionRepo struct{ mock.Mock }

func (m *mockQuestionRepo) CreateBatch(ctx context.Context, questions []SurveyQuestion) error {
	return m.Called(ctx, questions).Error(0)
}

func (m *mockQuestionRepo) ListBySurvey(ctx context.Context, surveyID uuid.UUID) ([]SurveyQuestion, error) {
	args := m.Called(ctx, surveyID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]SurveyQuestion), args.Error(1)
}

func (m *mockQuestionRepo) DeleteBySurvey(ctx context.Context, surveyID uuid.UUID) error {
	return m.Called(ctx, surveyID).Error(0)
}

// ---------------------------------------------------------------------------
// mockResponseRepo — SurveyResponseRepository
// ---------------------------------------------------------------------------

type mockResponseRepo struct{ mock.Mock }

func (m *mockResponseRepo) Create(ctx context.Context, response *SurveyResponse) error {
	return m.Called(ctx, response).Error(0)
}

func (m *mockResponseRepo) ListBySurvey(ctx context.Context, surveyID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]SurveyResponse, error) {
	args := m.Called(ctx, surveyID, tenantID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]SurveyResponse), args.Error(1)
}

func (m *mockResponseRepo) ExistsByGCID(ctx context.Context, surveyID, gcid uuid.UUID) (bool, error) {
	args := m.Called(ctx, surveyID, gcid)
	return args.Bool(0), args.Error(1)
}

func (m *mockResponseRepo) CountBySurvey(ctx context.Context, surveyID uuid.UUID) (int, error) {
	args := m.Called(ctx, surveyID)
	return args.Int(0), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockEventPublisher — EventPublisher
// ---------------------------------------------------------------------------

type mockEventPublisher struct{ mock.Mock }

func (m *mockEventPublisher) Publish(ctx context.Context, topic string, event interface{}) error {
	return m.Called(ctx, topic, event).Error(0)
}

func (m *mockEventPublisher) Close() error {
	return m.Called().Error(0)
}
