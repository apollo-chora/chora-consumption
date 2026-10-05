package survey

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Enum validation tests — SurveyStatus.IsValid, QuestionType.IsValid
// ---------------------------------------------------------------------------

func TestSurveyStatus_IsValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status SurveyStatus
		want   bool
	}{
		{name: "draft is valid", status: SurveyStatusDraft, want: true},
		{name: "published is valid", status: SurveyStatusPublished, want: true},
		{name: "archived is valid", status: SurveyStatusArchived, want: true},
		{name: "empty string is invalid", status: SurveyStatus(""), want: false},
		{name: "unknown value is invalid", status: SurveyStatus("cancelled"), want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.status.IsValid())
		})
	}
}

func TestQuestionType_IsValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		qt   QuestionType
		want bool
	}{
		{name: "rating is valid", qt: QuestionTypeRating, want: true},
		{name: "text is valid", qt: QuestionTypeText, want: true},
		{name: "multiple_choice is valid", qt: QuestionTypeMultipleChoice, want: true},
		{name: "scale is valid", qt: QuestionTypeScale, want: true},
		{name: "empty string is invalid", qt: QuestionType(""), want: false},
		{name: "unknown value is invalid", qt: QuestionType("checkbox"), want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.qt.IsValid())
		})
	}
}

// ---------------------------------------------------------------------------
// NewDomainEvent tests
// ---------------------------------------------------------------------------

func TestNewDomainEvent(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	aggID := uuid.Must(uuid.NewV7())

	t.Run("creates event with all fields populated", func(t *testing.T) {
		t.Parallel()
		evt := NewDomainEvent(
			EventSurveyPublished,
			tenantID,
			&gcid,
			aggID,
			AggregateSurveyTemplate,
			map[string]interface{}{"key": "value"},
		)

		assert.NotEqual(t, uuid.Nil, evt.EventID)
		assert.Equal(t, EventSurveyPublished, evt.EventType)
		assert.Equal(t, tenantID, evt.TenantID)
		require.NotNil(t, evt.GCID)
		assert.Equal(t, gcid, *evt.GCID)
		assert.Equal(t, aggID, evt.AggregateID)
		assert.Equal(t, AggregateSurveyTemplate, evt.AggregateType)
		assert.Equal(t, "value", evt.Payload["key"])
		assert.False(t, evt.Timestamp.IsZero())
	})

	t.Run("creates event with nil GCID", func(t *testing.T) {
		t.Parallel()
		evt := NewDomainEvent(
			EventResponseSubmitted,
			tenantID,
			nil,
			aggID,
			AggregateSurveyTemplate,
			nil,
		)

		assert.Nil(t, evt.GCID)
		assert.Nil(t, evt.Payload)
	})
}

// ---------------------------------------------------------------------------
// ListSurveys tests
// ---------------------------------------------------------------------------

func TestListSurveys(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	cursorID := uuid.Must(uuid.NewV7())

	t.Run("success: returns surveys for tenant", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		expected := []SurveyTemplate{
			{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, Title: "Survey 1"},
			{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, Title: "Survey 2"},
		}
		d.surveyR.On("List", mock.Anything, tenantID, (*uuid.UUID)(nil), 20).Return(expected, nil)

		got, err := d.svc.ListSurveys(context.Background(), tenantID, nil, 20)

		require.NoError(t, err)
		assert.Len(t, got, 2)
		assert.Equal(t, "Survey 1", got[0].Title)
		d.surveyR.AssertExpectations(t)
	})

	t.Run("success: returns surveys with cursor", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.surveyR.On("List", mock.Anything, tenantID, &cursorID, 10).Return([]SurveyTemplate{}, nil)

		got, err := d.svc.ListSurveys(context.Background(), tenantID, &cursorID, 10)

		require.NoError(t, err)
		assert.Empty(t, got)
		d.surveyR.AssertExpectations(t)
	})

	t.Run("fails: repo error propagated", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.surveyR.On("List", mock.Anything, tenantID, (*uuid.UUID)(nil), 20).
			Return(nil, errors.New("db timeout"))

		got, err := d.svc.ListSurveys(context.Background(), tenantID, nil, 20)

		assert.Error(t, err)
		assert.Nil(t, got)
		d.surveyR.AssertExpectations(t)
	})
}

// ---------------------------------------------------------------------------
// GetSurvey tests
// ---------------------------------------------------------------------------

func TestGetSurvey(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())

	t.Run("success: returns survey detail with questions", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
			ID:       surveyID,
			TenantID: tenantID,
			Title:    "My Survey",
			Status:   SurveyStatusDraft,
		}, nil)
		d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{
			{ID: uuid.Must(uuid.NewV7()), QuestionText: "Q1", QuestionType: QuestionTypeText},
		}, nil)

		got, err := d.svc.GetSurvey(context.Background(), surveyID, tenantID)

		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "My Survey", got.Title)
		assert.Len(t, got.Questions, 1)
		d.surveyR.AssertExpectations(t)
		d.questionR.AssertExpectations(t)
	})

	t.Run("fails: survey not found returns ErrSurveyNotFound", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(nil, nil)

		got, err := d.svc.GetSurvey(context.Background(), surveyID, tenantID)

		assert.ErrorIs(t, err, ErrSurveyNotFound)
		assert.Nil(t, got)
		d.surveyR.AssertExpectations(t)
	})

	t.Run("fails: GetByID repo error propagated", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).
			Return(nil, errors.New("db error"))

		got, err := d.svc.GetSurvey(context.Background(), surveyID, tenantID)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "db error")
		assert.Nil(t, got)
	})

	t.Run("fails: ListBySurvey question repo error propagated", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
			ID:       surveyID,
			TenantID: tenantID,
			Title:    "Survey",
			Status:   SurveyStatusDraft,
		}, nil)
		d.questionR.On("ListBySurvey", mock.Anything, surveyID).
			Return(nil, errors.New("question query failed"))

		got, err := d.svc.GetSurvey(context.Background(), surveyID, tenantID)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "question query failed")
		assert.Nil(t, got)
	})
}

// ---------------------------------------------------------------------------
// UpdateSurvey tests
// ---------------------------------------------------------------------------

func TestUpdateSurvey(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())

	t.Run("success: updates title and description without replacing questions", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
			ID:            surveyID,
			TenantID:      tenantID,
			Title:         "Old Title",
			Description:   "Old Desc",
			Status:        SurveyStatusDraft,
			QuestionCount: 2,
		}, nil)
		d.surveyR.On("Update", mock.Anything, mock.AnythingOfType("*survey.SurveyTemplate")).Return(nil)

		got, err := d.svc.UpdateSurvey(context.Background(), &SurveyTemplate{
			ID:          surveyID,
			TenantID:    tenantID,
			Title:       "New Title",
			Description: "New Desc",
		}, nil) // nil questions = no question replacement

		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "New Title", got.Title)
		assert.Equal(t, "New Desc", got.Description)
		assert.Equal(t, 2, got.QuestionCount, "question count should remain unchanged")
		d.surveyR.AssertExpectations(t)
	})

	t.Run("success: updates survey with new questions", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
			ID:            surveyID,
			TenantID:      tenantID,
			Title:         "Old Title",
			Status:        SurveyStatusDraft,
			QuestionCount: 1,
		}, nil)
		d.questionR.On("DeleteBySurvey", mock.Anything, surveyID).Return(nil)
		d.questionR.On("CreateBatch", mock.Anything, mock.AnythingOfType("[]survey.SurveyQuestion")).Return(nil)
		d.surveyR.On("Update", mock.Anything, mock.AnythingOfType("*survey.SurveyTemplate")).Return(nil)

		newQs := []SurveyQuestion{
			{QuestionText: "New Q1", QuestionType: QuestionTypeRating},
			{QuestionText: "New Q2", QuestionType: QuestionTypeText},
			{QuestionText: "New Q3", QuestionType: QuestionTypeScale},
		}
		got, err := d.svc.UpdateSurvey(context.Background(), &SurveyTemplate{
			ID:       surveyID,
			TenantID: tenantID,
			Title:    "Updated Title",
		}, newQs)

		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, 3, got.QuestionCount)
		d.surveyR.AssertExpectations(t)
		d.questionR.AssertExpectations(t)
	})

	t.Run("fails: survey not found", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(nil, nil)

		got, err := d.svc.UpdateSurvey(context.Background(), &SurveyTemplate{
			ID:       surveyID,
			TenantID: tenantID,
			Title:    "Update",
		}, nil)

		assert.ErrorIs(t, err, ErrSurveyNotFound)
		assert.Nil(t, got)
	})

	t.Run("fails: survey not in draft status", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
			ID:       surveyID,
			TenantID: tenantID,
			Status:   SurveyStatusPublished,
		}, nil)

		got, err := d.svc.UpdateSurvey(context.Background(), &SurveyTemplate{
			ID:       surveyID,
			TenantID: tenantID,
			Title:    "Update",
		}, nil)

		assert.ErrorIs(t, err, ErrSurveyNotModifiable)
		assert.Nil(t, got)
	})

	t.Run("fails: too many questions on update", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
			ID:       surveyID,
			TenantID: tenantID,
			Status:   SurveyStatusDraft,
		}, nil)

		qs := make([]SurveyQuestion, 51)
		for i := range qs {
			qs[i] = SurveyQuestion{QuestionText: "Q", QuestionType: QuestionTypeText}
		}

		got, err := d.svc.UpdateSurvey(context.Background(), &SurveyTemplate{
			ID:       surveyID,
			TenantID: tenantID,
			Title:    "Update",
		}, qs)

		assert.ErrorIs(t, err, ErrTooManyQuestions)
		assert.Nil(t, got)
	})

	t.Run("fails: invalid question type on update", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
			ID:       surveyID,
			TenantID: tenantID,
			Status:   SurveyStatusDraft,
		}, nil)

		got, err := d.svc.UpdateSurvey(context.Background(), &SurveyTemplate{
			ID:       surveyID,
			TenantID: tenantID,
			Title:    "Update",
		}, []SurveyQuestion{
			{QuestionText: "Bad Q", QuestionType: QuestionType("invalid_type")},
		})

		assert.ErrorIs(t, err, ErrValidationFailed)
		assert.Nil(t, got)
	})

	t.Run("fails: GetByID repo error propagated", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).
			Return(nil, errors.New("connection refused"))

		got, err := d.svc.UpdateSurvey(context.Background(), &SurveyTemplate{
			ID:       surveyID,
			TenantID: tenantID,
			Title:    "Update",
		}, nil)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "connection refused")
		assert.Nil(t, got)
	})

	t.Run("fails: DeleteBySurvey error propagated", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
			ID:       surveyID,
			TenantID: tenantID,
			Status:   SurveyStatusDraft,
		}, nil)
		d.questionR.On("DeleteBySurvey", mock.Anything, surveyID).
			Return(errors.New("delete failed"))

		got, err := d.svc.UpdateSurvey(context.Background(), &SurveyTemplate{
			ID:       surveyID,
			TenantID: tenantID,
			Title:    "Update",
		}, []SurveyQuestion{
			{QuestionText: "Q1", QuestionType: QuestionTypeText},
		})

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "delete failed")
		assert.Nil(t, got)
	})

	t.Run("fails: CreateBatch error propagated on update", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
			ID:       surveyID,
			TenantID: tenantID,
			Status:   SurveyStatusDraft,
		}, nil)
		d.questionR.On("DeleteBySurvey", mock.Anything, surveyID).Return(nil)
		d.questionR.On("CreateBatch", mock.Anything, mock.AnythingOfType("[]survey.SurveyQuestion")).
			Return(errors.New("batch insert failed"))

		got, err := d.svc.UpdateSurvey(context.Background(), &SurveyTemplate{
			ID:       surveyID,
			TenantID: tenantID,
			Title:    "Update",
		}, []SurveyQuestion{
			{QuestionText: "Q1", QuestionType: QuestionTypeText},
		})

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "batch insert failed")
		assert.Nil(t, got)
	})

	t.Run("fails: Update repo error propagated", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
			ID:       surveyID,
			TenantID: tenantID,
			Status:   SurveyStatusDraft,
		}, nil)
		d.surveyR.On("Update", mock.Anything, mock.AnythingOfType("*survey.SurveyTemplate")).
			Return(errors.New("update failed"))

		got, err := d.svc.UpdateSurvey(context.Background(), &SurveyTemplate{
			ID:       surveyID,
			TenantID: tenantID,
			Title:    "Update",
		}, nil)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "update failed")
		assert.Nil(t, got)
	})
}

// ---------------------------------------------------------------------------
// ListResponses tests
// ---------------------------------------------------------------------------

func TestListResponses(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())
	cursorID := uuid.Must(uuid.NewV7())

	t.Run("success: returns responses", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		expected := []SurveyResponse{
			{ID: uuid.Must(uuid.NewV7()), SurveyID: surveyID},
		}
		d.responseR.On("ListBySurvey", mock.Anything, surveyID, tenantID, (*uuid.UUID)(nil), 20).Return(expected, nil)

		got, err := d.svc.ListResponses(context.Background(), surveyID, tenantID, nil, 20)

		require.NoError(t, err)
		assert.Len(t, got, 1)
		d.responseR.AssertExpectations(t)
	})

	t.Run("success: with cursor pagination", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.responseR.On("ListBySurvey", mock.Anything, surveyID, tenantID, &cursorID, 10).
			Return([]SurveyResponse{}, nil)

		got, err := d.svc.ListResponses(context.Background(), surveyID, tenantID, &cursorID, 10)

		require.NoError(t, err)
		assert.Empty(t, got)
		d.responseR.AssertExpectations(t)
	})

	t.Run("fails: repo error propagated", func(t *testing.T) {
		t.Parallel()
		d := newTestSurveyService()
		d.responseR.On("ListBySurvey", mock.Anything, surveyID, tenantID, (*uuid.UUID)(nil), 20).
			Return(nil, errors.New("db timeout"))

		got, err := d.svc.ListResponses(context.Background(), surveyID, tenantID, nil, 20)

		assert.Error(t, err)
		assert.Nil(t, got)
	})
}

// ---------------------------------------------------------------------------
// CreateSurvey — additional error path coverage
// ---------------------------------------------------------------------------

func TestCreateSurvey_InvalidQuestionType(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())

	got, err := d.svc.CreateSurvey(context.Background(), &SurveyTemplate{
		TenantID:      tenantID,
		Title:         "Survey",
		CreatedByGCID: uuid.Must(uuid.NewV7()),
	}, []SurveyQuestion{
		{QuestionText: "Valid Q", QuestionType: QuestionTypeText},
		{QuestionText: "Invalid Q", QuestionType: QuestionType("unknown")},
	})

	assert.ErrorIs(t, err, ErrValidationFailed)
	assert.Nil(t, got)
}

func TestCreateSurvey_EmptyQuestionText(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())

	got, err := d.svc.CreateSurvey(context.Background(), &SurveyTemplate{
		TenantID:      tenantID,
		Title:         "Survey",
		CreatedByGCID: uuid.Must(uuid.NewV7()),
	}, []SurveyQuestion{
		{QuestionText: "", QuestionType: QuestionTypeText},
	})

	assert.ErrorIs(t, err, ErrValidationFailed)
	assert.Nil(t, got)
}

func TestCreateSurvey_CreateBatchError(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())

	d.surveyR.On("Create", mock.Anything, mock.AnythingOfType("*survey.SurveyTemplate")).Return(nil)
	d.questionR.On("CreateBatch", mock.Anything, mock.AnythingOfType("[]survey.SurveyQuestion")).
		Return(errors.New("batch insert error"))

	got, err := d.svc.CreateSurvey(context.Background(), &SurveyTemplate{
		TenantID:      tenantID,
		Title:         "Survey",
		CreatedByGCID: uuid.Must(uuid.NewV7()),
	}, []SurveyQuestion{
		{QuestionText: "Q1", QuestionType: QuestionTypeRating},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "batch insert error")
	assert.Nil(t, got)
	d.surveyR.AssertExpectations(t)
	d.questionR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// PublishSurvey — additional error path coverage
// ---------------------------------------------------------------------------

func TestPublishSurvey_GetByIDError(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).
		Return(nil, errors.New("db read error"))

	got, err := d.svc.PublishSurvey(context.Background(), surveyID, tenantID)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "db read error")
	assert.Nil(t, got)
}

func TestPublishSurvey_ListBySurveyError(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
		Status:   SurveyStatusDraft,
	}, nil)
	d.questionR.On("ListBySurvey", mock.Anything, surveyID).
		Return(nil, errors.New("question query failed"))

	got, err := d.svc.PublishSurvey(context.Background(), surveyID, tenantID)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "question query failed")
	assert.Nil(t, got)
}

func TestPublishSurvey_UpdateError(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
		Status:   SurveyStatusDraft,
	}, nil)
	d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{
		{ID: uuid.Must(uuid.NewV7()), QuestionText: "Q1", QuestionType: QuestionTypeRating},
	}, nil)
	d.surveyR.On("Update", mock.Anything, mock.AnythingOfType("*survey.SurveyTemplate")).
		Return(errors.New("update failed"))

	got, err := d.svc.PublishSurvey(context.Background(), surveyID, tenantID)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "update failed")
	assert.Nil(t, got)
}

func TestPublishSurvey_EventPublishError(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())
	createdByGCID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:            surveyID,
		TenantID:      tenantID,
		Title:         "Survey",
		Status:        SurveyStatusDraft,
		CreatedByGCID: createdByGCID,
	}, nil)
	d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{
		{ID: uuid.Must(uuid.NewV7()), QuestionText: "Q1", QuestionType: QuestionTypeRating},
	}, nil)
	d.surveyR.On("Update", mock.Anything, mock.AnythingOfType("*survey.SurveyTemplate")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicSurveyEvents, mock.Anything).
		Return(errors.New("pubsub unavailable"))

	got, err := d.svc.PublishSurvey(context.Background(), surveyID, tenantID)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "pubsub unavailable")
	assert.Nil(t, got)
}

func TestPublishSurvey_ArchivedStatus(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
		Status:   SurveyStatusArchived,
	}, nil)

	got, err := d.svc.PublishSurvey(context.Background(), surveyID, tenantID)

	assert.ErrorIs(t, err, ErrSurveyNotPublishable)
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// SubmitResponse — additional error path coverage
// ---------------------------------------------------------------------------

func TestSubmitResponse_SurveyNotFound(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(nil, nil)

	got, err := d.svc.SubmitResponse(context.Background(), &SurveyResponse{
		TenantID: tenantID,
		SurveyID: surveyID,
		GCID:     uuid.Must(uuid.NewV7()),
		Answers:  map[string]any{},
	})

	assert.ErrorIs(t, err, ErrSurveyNotFound)
	assert.Nil(t, got)
}

func TestSubmitResponse_GetByIDError(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).
		Return(nil, errors.New("db error"))

	got, err := d.svc.SubmitResponse(context.Background(), &SurveyResponse{
		TenantID: tenantID,
		SurveyID: surveyID,
		GCID:     uuid.Must(uuid.NewV7()),
		Answers:  map[string]any{},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "db error")
	assert.Nil(t, got)
}

func TestSubmitResponse_ExistsByGCIDError(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
		Status:   SurveyStatusPublished,
	}, nil)
	d.responseR.On("ExistsByGCID", mock.Anything, surveyID, learnerGCID).
		Return(false, errors.New("exists check failed"))

	got, err := d.svc.SubmitResponse(context.Background(), &SurveyResponse{
		TenantID: tenantID,
		SurveyID: surveyID,
		GCID:     learnerGCID,
		Answers:  map[string]any{},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exists check failed")
	assert.Nil(t, got)
}

func TestSubmitResponse_ListBySurveyError(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
		Status:   SurveyStatusPublished,
	}, nil)
	d.responseR.On("ExistsByGCID", mock.Anything, surveyID, learnerGCID).Return(false, nil)
	d.questionR.On("ListBySurvey", mock.Anything, surveyID).
		Return(nil, errors.New("question query failed"))

	got, err := d.svc.SubmitResponse(context.Background(), &SurveyResponse{
		TenantID: tenantID,
		SurveyID: surveyID,
		GCID:     learnerGCID,
		Answers:  map[string]any{},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "question query failed")
	assert.Nil(t, got)
}

func TestSubmitResponse_CreateError(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
		Status:   SurveyStatusPublished,
	}, nil)
	d.responseR.On("ExistsByGCID", mock.Anything, surveyID, learnerGCID).Return(false, nil)
	d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{}, nil)
	d.responseR.On("Create", mock.Anything, mock.AnythingOfType("*survey.SurveyResponse")).
		Return(errors.New("insert failed"))

	got, err := d.svc.SubmitResponse(context.Background(), &SurveyResponse{
		TenantID: tenantID,
		SurveyID: surveyID,
		GCID:     learnerGCID,
		Answers:  map[string]any{},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "insert failed")
	assert.Nil(t, got)
}

func TestSubmitResponse_EventPublishError(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
		Status:   SurveyStatusPublished,
	}, nil)
	d.responseR.On("ExistsByGCID", mock.Anything, surveyID, learnerGCID).Return(false, nil)
	d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{}, nil)
	d.responseR.On("Create", mock.Anything, mock.AnythingOfType("*survey.SurveyResponse")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicSurveyEvents, mock.Anything).
		Return(errors.New("pubsub down"))

	got, err := d.svc.SubmitResponse(context.Background(), &SurveyResponse{
		TenantID: tenantID,
		SurveyID: surveyID,
		GCID:     learnerGCID,
		Answers:  map[string]any{},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "pubsub down")
	assert.Nil(t, got)
}

func TestSubmitResponse_ArchivedSurvey(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
		Status:   SurveyStatusArchived,
	}, nil)

	got, err := d.svc.SubmitResponse(context.Background(), &SurveyResponse{
		TenantID: tenantID,
		SurveyID: surveyID,
		GCID:     uuid.Must(uuid.NewV7()),
		Answers:  map[string]any{},
	})

	assert.ErrorIs(t, err, ErrSurveyNotPublished)
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// DeleteSurvey — additional error path coverage
// ---------------------------------------------------------------------------

func TestDeleteSurvey_GetByIDError(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).
		Return(nil, errors.New("db error"))

	err := d.svc.DeleteSurvey(context.Background(), surveyID, tenantID)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "db error")
}

func TestDeleteSurvey_CountBySurveyError(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
		Status:   SurveyStatusDraft,
	}, nil)
	d.responseR.On("CountBySurvey", mock.Anything, surveyID).
		Return(0, errors.New("count failed"))

	err := d.svc.DeleteSurvey(context.Background(), surveyID, tenantID)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "count failed")
}

func TestDeleteSurvey_DeleteRepoError(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
		Status:   SurveyStatusDraft,
	}, nil)
	d.responseR.On("CountBySurvey", mock.Anything, surveyID).Return(0, nil)
	d.surveyR.On("Delete", mock.Anything, surveyID, tenantID).
		Return(errors.New("delete failed"))

	err := d.svc.DeleteSurvey(context.Background(), surveyID, tenantID)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "delete failed")
}

// ---------------------------------------------------------------------------
// GetAnalytics — additional coverage for int type, scale questions, error paths
// ---------------------------------------------------------------------------

func TestGetAnalytics_WithIntRatings(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())
	q1ID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
	}, nil)
	d.responseR.On("CountBySurvey", mock.Anything, surveyID).Return(2, nil)
	d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{
		{ID: q1ID, QuestionText: "Rate 1-10", QuestionType: QuestionTypeRating},
	}, nil)
	d.responseR.On("ListBySurvey", mock.Anything, surveyID, tenantID, (*uuid.UUID)(nil), 1000).Return([]SurveyResponse{
		{
			ID:       uuid.Must(uuid.NewV7()),
			SurveyID: surveyID,
			GCID:     uuid.Must(uuid.NewV7()),
			Answers:  map[string]any{q1ID.String(): int(3)},
		},
		{
			ID:       uuid.Must(uuid.NewV7()),
			SurveyID: surveyID,
			GCID:     uuid.Must(uuid.NewV7()),
			Answers:  map[string]any{q1ID.String(): int(7)},
		},
	}, nil)

	got, err := d.svc.GetAnalytics(context.Background(), surveyID, tenantID)

	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.AverageRating)
	assert.Equal(t, 5.0, *got.AverageRating)
	require.Len(t, got.QuestionBreakdowns, 1)
	require.NotNil(t, got.QuestionBreakdowns[0].AverageValue)
	assert.Equal(t, 5.0, *got.QuestionBreakdowns[0].AverageValue)
}

func TestGetAnalytics_WithScaleQuestions(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())
	q1ID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
	}, nil)
	d.responseR.On("CountBySurvey", mock.Anything, surveyID).Return(1, nil)
	d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{
		{ID: q1ID, QuestionText: "On a scale of 1-5", QuestionType: QuestionTypeScale},
	}, nil)
	d.responseR.On("ListBySurvey", mock.Anything, surveyID, tenantID, (*uuid.UUID)(nil), 1000).Return([]SurveyResponse{
		{
			ID:       uuid.Must(uuid.NewV7()),
			SurveyID: surveyID,
			GCID:     uuid.Must(uuid.NewV7()),
			Answers:  map[string]any{q1ID.String(): float64(4.5)},
		},
	}, nil)

	got, err := d.svc.GetAnalytics(context.Background(), surveyID, tenantID)

	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.AverageRating)
	assert.Equal(t, 4.5, *got.AverageRating)
	require.Len(t, got.QuestionBreakdowns, 1)
	assert.Equal(t, "scale", got.QuestionBreakdowns[0].QuestionType)
	require.NotNil(t, got.QuestionBreakdowns[0].AverageValue)
	assert.Equal(t, 4.5, *got.QuestionBreakdowns[0].AverageValue)
}

func TestGetAnalytics_TextOnlyNoRating(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())
	q1ID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
	}, nil)
	d.responseR.On("CountBySurvey", mock.Anything, surveyID).Return(1, nil)
	d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{
		{ID: q1ID, QuestionText: "Open comments", QuestionType: QuestionTypeText},
	}, nil)
	d.responseR.On("ListBySurvey", mock.Anything, surveyID, tenantID, (*uuid.UUID)(nil), 1000).Return([]SurveyResponse{
		{
			ID:       uuid.Must(uuid.NewV7()),
			SurveyID: surveyID,
			GCID:     uuid.Must(uuid.NewV7()),
			Answers:  map[string]any{q1ID.String(): "This is feedback"},
		},
	}, nil)

	got, err := d.svc.GetAnalytics(context.Background(), surveyID, tenantID)

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Nil(t, got.AverageRating, "no rating questions = nil average")
	require.Len(t, got.QuestionBreakdowns, 1)
	assert.Equal(t, 1, got.QuestionBreakdowns[0].ResponseCount)
	assert.Nil(t, got.QuestionBreakdowns[0].AverageValue, "text question should have no average value")
}

func TestGetAnalytics_MissingAnswerForQuestion(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())
	q1ID := uuid.Must(uuid.NewV7())
	q2ID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
	}, nil)
	d.responseR.On("CountBySurvey", mock.Anything, surveyID).Return(1, nil)
	d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{
		{ID: q1ID, QuestionText: "Rate", QuestionType: QuestionTypeRating},
		{ID: q2ID, QuestionText: "Scale", QuestionType: QuestionTypeScale},
	}, nil)
	// Response only answers q1, skips q2
	d.responseR.On("ListBySurvey", mock.Anything, surveyID, tenantID, (*uuid.UUID)(nil), 1000).Return([]SurveyResponse{
		{
			ID:       uuid.Must(uuid.NewV7()),
			SurveyID: surveyID,
			GCID:     uuid.Must(uuid.NewV7()),
			Answers:  map[string]any{q1ID.String(): float64(3)},
		},
	}, nil)

	got, err := d.svc.GetAnalytics(context.Background(), surveyID, tenantID)

	require.NoError(t, err)
	require.NotNil(t, got)
	require.Len(t, got.QuestionBreakdowns, 2)
	// q1 was answered
	assert.Equal(t, 1, got.QuestionBreakdowns[0].ResponseCount)
	require.NotNil(t, got.QuestionBreakdowns[0].AverageValue)
	assert.Equal(t, 3.0, *got.QuestionBreakdowns[0].AverageValue)
	// q2 was not answered
	assert.Equal(t, 0, got.QuestionBreakdowns[1].ResponseCount)
	assert.Nil(t, got.QuestionBreakdowns[1].AverageValue)
}

func TestGetAnalytics_GetByIDError(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).
		Return(nil, errors.New("db read error"))

	got, err := d.svc.GetAnalytics(context.Background(), surveyID, tenantID)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "db read error")
	assert.Nil(t, got)
}

func TestGetAnalytics_CountBySurveyError(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
	}, nil)
	d.responseR.On("CountBySurvey", mock.Anything, surveyID).
		Return(0, errors.New("count error"))

	got, err := d.svc.GetAnalytics(context.Background(), surveyID, tenantID)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "count error")
	assert.Nil(t, got)
}

func TestGetAnalytics_ListBySurveyQuestionsError(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
	}, nil)
	d.responseR.On("CountBySurvey", mock.Anything, surveyID).Return(3, nil)
	d.questionR.On("ListBySurvey", mock.Anything, surveyID).
		Return(nil, errors.New("question query failed"))

	got, err := d.svc.GetAnalytics(context.Background(), surveyID, tenantID)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "question query failed")
	assert.Nil(t, got)
}

func TestGetAnalytics_ListResponsesError(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
	}, nil)
	d.responseR.On("CountBySurvey", mock.Anything, surveyID).Return(2, nil)
	d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{
		{ID: uuid.Must(uuid.NewV7()), QuestionText: "Q1", QuestionType: QuestionTypeRating},
	}, nil)
	d.responseR.On("ListBySurvey", mock.Anything, surveyID, tenantID, (*uuid.UUID)(nil), 1000).
		Return(nil, errors.New("response query failed"))

	got, err := d.svc.GetAnalytics(context.Background(), surveyID, tenantID)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "response query failed")
	assert.Nil(t, got)
}

func TestGetAnalytics_MultipleChoiceQuestion(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())
	q1ID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
	}, nil)
	d.responseR.On("CountBySurvey", mock.Anything, surveyID).Return(2, nil)
	d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{
		{ID: q1ID, QuestionText: "Choose option", QuestionType: QuestionTypeMultipleChoice},
	}, nil)
	d.responseR.On("ListBySurvey", mock.Anything, surveyID, tenantID, (*uuid.UUID)(nil), 1000).Return([]SurveyResponse{
		{
			ID:       uuid.Must(uuid.NewV7()),
			SurveyID: surveyID,
			GCID:     uuid.Must(uuid.NewV7()),
			Answers:  map[string]any{q1ID.String(): "option_a"},
		},
		{
			ID:       uuid.Must(uuid.NewV7()),
			SurveyID: surveyID,
			GCID:     uuid.Must(uuid.NewV7()),
			Answers:  map[string]any{q1ID.String(): "option_b"},
		},
	}, nil)

	got, err := d.svc.GetAnalytics(context.Background(), surveyID, tenantID)

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Nil(t, got.AverageRating, "MC questions have no average rating")
	require.Len(t, got.QuestionBreakdowns, 1)
	assert.Equal(t, 2, got.QuestionBreakdowns[0].ResponseCount)
	assert.Nil(t, got.QuestionBreakdowns[0].AverageValue, "MC question has no average value")
}

func TestGetAnalytics_MixedIntAndFloat64Ratings(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())
	q1ID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
	}, nil)
	d.responseR.On("CountBySurvey", mock.Anything, surveyID).Return(3, nil)
	d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{
		{ID: q1ID, QuestionText: "Rate", QuestionType: QuestionTypeRating},
	}, nil)
	d.responseR.On("ListBySurvey", mock.Anything, surveyID, tenantID, (*uuid.UUID)(nil), 1000).Return([]SurveyResponse{
		{
			ID: uuid.Must(uuid.NewV7()), SurveyID: surveyID, GCID: uuid.Must(uuid.NewV7()),
			Answers: map[string]any{q1ID.String(): float64(4)},
		},
		{
			ID: uuid.Must(uuid.NewV7()), SurveyID: surveyID, GCID: uuid.Must(uuid.NewV7()),
			Answers: map[string]any{q1ID.String(): int(5)},
		},
		{
			ID: uuid.Must(uuid.NewV7()), SurveyID: surveyID, GCID: uuid.Must(uuid.NewV7()),
			Answers: map[string]any{q1ID.String(): "not_a_number"}, // string value — should not count
		},
	}, nil)

	got, err := d.svc.GetAnalytics(context.Background(), surveyID, tenantID)

	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.AverageRating)
	// Global average: (4.0 + 5.0) / 2 = 4.5 — only numeric values counted via ratingCount
	assert.Equal(t, 4.5, *got.AverageRating)
	require.Len(t, got.QuestionBreakdowns, 1)
	// All 3 responses have an answer for q1 (response count includes non-numeric)
	assert.Equal(t, 3, got.QuestionBreakdowns[0].ResponseCount)
	require.NotNil(t, got.QuestionBreakdowns[0].AverageValue)
	// Breakdown average: sum(4+5) / count(3) = 3.0 — uses total response count, not just numeric
	assert.Equal(t, 3.0, *got.QuestionBreakdowns[0].AverageValue)
}

func TestGetAnalytics_ScaleWithIntValues(t *testing.T) {
	t.Parallel()

	d := newTestSurveyService()
	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())
	q1ID := uuid.Must(uuid.NewV7())

	d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
		ID:       surveyID,
		TenantID: tenantID,
	}, nil)
	d.responseR.On("CountBySurvey", mock.Anything, surveyID).Return(2, nil)
	d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{
		{ID: q1ID, QuestionText: "Scale 1-10", QuestionType: QuestionTypeScale},
	}, nil)
	d.responseR.On("ListBySurvey", mock.Anything, surveyID, tenantID, (*uuid.UUID)(nil), 1000).Return([]SurveyResponse{
		{
			ID: uuid.Must(uuid.NewV7()), SurveyID: surveyID, GCID: uuid.Must(uuid.NewV7()),
			Answers: map[string]any{q1ID.String(): int(8)},
		},
		{
			ID: uuid.Must(uuid.NewV7()), SurveyID: surveyID, GCID: uuid.Must(uuid.NewV7()),
			Answers: map[string]any{q1ID.String(): int(6)},
		},
	}, nil)

	got, err := d.svc.GetAnalytics(context.Background(), surveyID, tenantID)

	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.AverageRating)
	assert.Equal(t, 7.0, *got.AverageRating)
	require.Len(t, got.QuestionBreakdowns, 1)
	require.NotNil(t, got.QuestionBreakdowns[0].AverageValue)
	assert.Equal(t, 7.0, *got.QuestionBreakdowns[0].AverageValue)
}
