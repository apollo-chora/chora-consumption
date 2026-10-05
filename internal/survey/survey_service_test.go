package survey

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// testSurveyDeps holds all mocks wired into a SurveyService.
type testSurveyDeps struct {
	svc       *SurveyService
	surveyR   *mockSurveyRepo
	questionR *mockQuestionRepo
	responseR *mockResponseRepo
	publisher *mockEventPublisher
}

// newTestSurveyService creates a SurveyService with fresh mocks.
func newTestSurveyService() testSurveyDeps {
	sr := &mockSurveyRepo{}
	qr := &mockQuestionRepo{}
	rr := &mockResponseRepo{}
	ep := &mockEventPublisher{}
	return testSurveyDeps{
		svc:       NewSurveyService(sr, qr, rr, ep),
		surveyR:   sr,
		questionR: qr,
		responseR: rr,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// TestCreateSurvey
// ---------------------------------------------------------------------------

func TestCreateSurvey(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	createdByGCID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *SurveyTemplate
		questions    []SurveyQuestion
		setupMocks   func(d testSurveyDeps)
		wantErr      error
		assertResult func(t *testing.T, got *SurveyTemplate)
	}{
		{
			name: "success: creates survey with questions",
			input: &SurveyTemplate{
				TenantID:      tenantID,
				Title:         "Post-Training Feedback",
				Description:   "How was the training?",
				CreatedByGCID: createdByGCID,
			},
			questions: []SurveyQuestion{
				{QuestionText: "Rate the instructor", QuestionType: QuestionTypeRating, Required: true},
				{QuestionText: "Any comments?", QuestionType: QuestionTypeText, Required: false},
			},
			setupMocks: func(d testSurveyDeps) {
				d.surveyR.On("Create", mock.Anything, mock.AnythingOfType("*survey.SurveyTemplate")).Return(nil)
				d.questionR.On("CreateBatch", mock.Anything, mock.AnythingOfType("[]survey.SurveyQuestion")).Return(nil)
			},
			assertResult: func(t *testing.T, got *SurveyTemplate) {
				assert.NotEqual(t, uuid.Nil, got.ID, "should assign UUIDv7")
				assert.Equal(t, SurveyStatusDraft, got.Status, "should default to draft")
				assert.Equal(t, "Post-Training Feedback", got.Title)
				assert.Equal(t, 2, got.QuestionCount)
			},
		},
		{
			name: "fails: empty title returns ErrValidationFailed",
			input: &SurveyTemplate{
				TenantID:      tenantID,
				Title:         "",
				CreatedByGCID: createdByGCID,
			},
			questions: []SurveyQuestion{
				{QuestionText: "Rate the instructor", QuestionType: QuestionTypeRating, Required: true},
			},
			setupMocks: func(d testSurveyDeps) {},
			wantErr:    ErrValidationFailed,
		},
		{
			name: "fails: too many questions",
			input: &SurveyTemplate{
				TenantID:      tenantID,
				Title:         "Too Many",
				CreatedByGCID: createdByGCID,
			},
			questions: func() []SurveyQuestion {
				qs := make([]SurveyQuestion, 51)
				for i := range qs {
					qs[i] = SurveyQuestion{QuestionText: "Q", QuestionType: QuestionTypeText}
				}
				return qs
			}(),
			setupMocks: func(d testSurveyDeps) {},
			wantErr:    ErrTooManyQuestions,
		},
		{
			name: "fails: no questions returns ErrNoQuestions",
			input: &SurveyTemplate{
				TenantID:      tenantID,
				Title:         "No Questions",
				CreatedByGCID: createdByGCID,
			},
			questions:  []SurveyQuestion{},
			setupMocks: func(d testSurveyDeps) {},
			wantErr:    ErrNoQuestions,
		},
		{
			name: "fails: repo error propagated",
			input: &SurveyTemplate{
				TenantID:      tenantID,
				Title:         "Valid Survey",
				CreatedByGCID: createdByGCID,
			},
			questions: []SurveyQuestion{
				{QuestionText: "Rate", QuestionType: QuestionTypeRating, Required: true},
			},
			setupMocks: func(d testSurveyDeps) {
				d.surveyR.On("Create", mock.Anything, mock.AnythingOfType("*survey.SurveyTemplate")).
					Return(errors.New("db connection lost"))
			},
			wantErr: errors.New("db connection lost"),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSurveyService()
			tc.setupMocks(d)

			got, err := d.svc.CreateSurvey(context.Background(), tc.input, tc.questions)

			if tc.wantErr != nil {
				assert.Error(t, err)
				if errors.Is(tc.wantErr, ErrValidationFailed) {
					assert.ErrorIs(t, err, ErrValidationFailed)
				}
				if errors.Is(tc.wantErr, ErrTooManyQuestions) {
					assert.ErrorIs(t, err, ErrTooManyQuestions)
				}
				if errors.Is(tc.wantErr, ErrNoQuestions) {
					assert.ErrorIs(t, err, ErrNoQuestions)
				}
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, got) && tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}

			d.surveyR.AssertExpectations(t)
			d.questionR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestPublishSurvey
// ---------------------------------------------------------------------------

func TestPublishSurvey(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())
	createdByGCID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		id           uuid.UUID
		tenantID     uuid.UUID
		setupMocks   func(d testSurveyDeps)
		wantErr      error
		assertResult func(t *testing.T, got *SurveyTemplate)
	}{
		{
			name:     "success: publishes draft survey with questions",
			id:       surveyID,
			tenantID: tenantID,
			setupMocks: func(d testSurveyDeps) {
				d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
					ID:            surveyID,
					TenantID:      tenantID,
					Title:         "Feedback Survey",
					Status:        SurveyStatusDraft,
					CreatedByGCID: createdByGCID,
				}, nil)
				d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{
					{ID: uuid.Must(uuid.NewV7()), QuestionText: "Rate", QuestionType: QuestionTypeRating},
				}, nil)
				d.surveyR.On("Update", mock.Anything, mock.AnythingOfType("*survey.SurveyTemplate")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicSurveyEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *SurveyTemplate) {
				assert.Equal(t, SurveyStatusPublished, got.Status)
			},
		},
		{
			name:     "fails: already published",
			id:       surveyID,
			tenantID: tenantID,
			setupMocks: func(d testSurveyDeps) {
				d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
					ID:       surveyID,
					TenantID: tenantID,
					Status:   SurveyStatusPublished,
				}, nil)
			},
			wantErr: ErrSurveyNotPublishable,
		},
		{
			name:     "fails: no questions",
			id:       surveyID,
			tenantID: tenantID,
			setupMocks: func(d testSurveyDeps) {
				d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
					ID:       surveyID,
					TenantID: tenantID,
					Status:   SurveyStatusDraft,
				}, nil)
				d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{}, nil)
			},
			wantErr: ErrNoQuestions,
		},
		{
			name:     "fails: survey not found",
			id:       uuid.Must(uuid.NewV7()),
			tenantID: tenantID,
			setupMocks: func(d testSurveyDeps) {
				d.surveyR.On("GetByID", mock.Anything, mock.Anything, tenantID).Return(nil, nil)
			},
			wantErr: ErrSurveyNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSurveyService()
			tc.setupMocks(d)

			got, err := d.svc.PublishSurvey(context.Background(), tc.id, tc.tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				if errors.Is(tc.wantErr, ErrSurveyNotPublishable) {
					assert.ErrorIs(t, err, ErrSurveyNotPublishable)
				}
				if errors.Is(tc.wantErr, ErrNoQuestions) {
					assert.ErrorIs(t, err, ErrNoQuestions)
				}
				if errors.Is(tc.wantErr, ErrSurveyNotFound) {
					assert.ErrorIs(t, err, ErrSurveyNotFound)
				}
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, got) && tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}

			d.surveyR.AssertExpectations(t)
			d.questionR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestSubmitResponse
// ---------------------------------------------------------------------------

func TestSubmitResponse(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())
	learnerGCID := uuid.Must(uuid.NewV7())
	q1ID := uuid.Must(uuid.NewV7())
	q2ID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		input        *SurveyResponse
		setupMocks   func(d testSurveyDeps)
		wantErr      error
		assertResult func(t *testing.T, got *SurveyResponse)
	}{
		{
			name: "success: submits response with all required answers",
			input: &SurveyResponse{
				TenantID: tenantID,
				SurveyID: surveyID,
				GCID:     learnerGCID,
				Answers: map[string]any{
					q1ID.String(): 5,
					q2ID.String(): "Great training!",
				},
			},
			setupMocks: func(d testSurveyDeps) {
				d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
					ID:       surveyID,
					TenantID: tenantID,
					Status:   SurveyStatusPublished,
				}, nil)
				d.responseR.On("ExistsByGCID", mock.Anything, surveyID, learnerGCID).Return(false, nil)
				d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{
					{ID: q1ID, QuestionText: "Rate", QuestionType: QuestionTypeRating, Required: true},
					{ID: q2ID, QuestionText: "Comments", QuestionType: QuestionTypeText, Required: false},
				}, nil)
				d.responseR.On("Create", mock.Anything, mock.AnythingOfType("*survey.SurveyResponse")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicSurveyEvents, mock.Anything).Return(nil)
			},
			assertResult: func(t *testing.T, got *SurveyResponse) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, surveyID, got.SurveyID)
				assert.Equal(t, learnerGCID, got.GCID)
			},
		},
		{
			name: "fails: survey not published",
			input: &SurveyResponse{
				TenantID: tenantID,
				SurveyID: surveyID,
				GCID:     learnerGCID,
				Answers:  map[string]any{q1ID.String(): 5},
			},
			setupMocks: func(d testSurveyDeps) {
				d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
					ID:       surveyID,
					TenantID: tenantID,
					Status:   SurveyStatusDraft,
				}, nil)
			},
			wantErr: ErrSurveyNotPublished,
		},
		{
			name: "fails: already responded",
			input: &SurveyResponse{
				TenantID: tenantID,
				SurveyID: surveyID,
				GCID:     learnerGCID,
				Answers:  map[string]any{q1ID.String(): 5},
			},
			setupMocks: func(d testSurveyDeps) {
				d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
					ID:       surveyID,
					TenantID: tenantID,
					Status:   SurveyStatusPublished,
				}, nil)
				d.responseR.On("ExistsByGCID", mock.Anything, surveyID, learnerGCID).Return(true, nil)
			},
			wantErr: ErrAlreadyResponded,
		},
		{
			name: "fails: missing required question",
			input: &SurveyResponse{
				TenantID: tenantID,
				SurveyID: surveyID,
				GCID:     learnerGCID,
				Answers:  map[string]any{}, // missing q1
			},
			setupMocks: func(d testSurveyDeps) {
				d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
					ID:       surveyID,
					TenantID: tenantID,
					Status:   SurveyStatusPublished,
				}, nil)
				d.responseR.On("ExistsByGCID", mock.Anything, surveyID, learnerGCID).Return(false, nil)
				d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{
					{ID: q1ID, QuestionText: "Rate the instructor", QuestionType: QuestionTypeRating, Required: true},
				}, nil)
			},
			wantErr: ErrMissingRequiredAnswer,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSurveyService()
			tc.setupMocks(d)

			got, err := d.svc.SubmitResponse(context.Background(), tc.input)

			if tc.wantErr != nil {
				assert.Error(t, err)
				if errors.Is(tc.wantErr, ErrSurveyNotPublished) {
					assert.ErrorIs(t, err, ErrSurveyNotPublished)
				}
				if errors.Is(tc.wantErr, ErrAlreadyResponded) {
					assert.ErrorIs(t, err, ErrAlreadyResponded)
				}
				if errors.Is(tc.wantErr, ErrMissingRequiredAnswer) {
					assert.ErrorIs(t, err, ErrMissingRequiredAnswer)
				}
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, got) && tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}

			d.surveyR.AssertExpectations(t)
			d.responseR.AssertExpectations(t)
			d.questionR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetAnalytics
// ---------------------------------------------------------------------------

func TestGetAnalytics(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())
	q1ID := uuid.Must(uuid.NewV7())
	q2ID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name         string
		surveyID     uuid.UUID
		tenantID     uuid.UUID
		setupMocks   func(d testSurveyDeps)
		wantErr      error
		assertResult func(t *testing.T, got *SurveyAnalytics)
	}{
		{
			name:     "success: returns analytics with ratings",
			surveyID: surveyID,
			tenantID: tenantID,
			setupMocks: func(d testSurveyDeps) {
				d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
					ID:       surveyID,
					TenantID: tenantID,
					Status:   SurveyStatusPublished,
				}, nil)
				d.responseR.On("CountBySurvey", mock.Anything, surveyID).Return(2, nil)
				d.questionR.On("ListBySurvey", mock.Anything, surveyID).Return([]SurveyQuestion{
					{ID: q1ID, QuestionText: "Rate instructor", QuestionType: QuestionTypeRating, Required: true},
					{ID: q2ID, QuestionText: "Comments", QuestionType: QuestionTypeText, Required: false},
				}, nil)
				d.responseR.On("ListBySurvey", mock.Anything, surveyID, tenantID, (*uuid.UUID)(nil), 1000).Return([]SurveyResponse{
					{
						ID:       uuid.Must(uuid.NewV7()),
						SurveyID: surveyID,
						GCID:     uuid.Must(uuid.NewV7()),
						Answers:  map[string]any{q1ID.String(): float64(4), q2ID.String(): "Good"},
					},
					{
						ID:       uuid.Must(uuid.NewV7()),
						SurveyID: surveyID,
						GCID:     uuid.Must(uuid.NewV7()),
						Answers:  map[string]any{q1ID.String(): float64(5), q2ID.String(): "Excellent"},
					},
				}, nil)
			},
			assertResult: func(t *testing.T, got *SurveyAnalytics) {
				assert.Equal(t, surveyID, got.SurveyID)
				assert.Equal(t, 2, got.ResponseCount)
				assert.NotNil(t, got.AverageRating)
				assert.Equal(t, 4.5, *got.AverageRating)
				assert.Len(t, got.QuestionBreakdowns, 2)
			},
		},
		{
			name:     "success: no responses returns zero counts",
			surveyID: surveyID,
			tenantID: tenantID,
			setupMocks: func(d testSurveyDeps) {
				d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
					ID:       surveyID,
					TenantID: tenantID,
					Status:   SurveyStatusPublished,
				}, nil)
				d.responseR.On("CountBySurvey", mock.Anything, surveyID).Return(0, nil)
			},
			assertResult: func(t *testing.T, got *SurveyAnalytics) {
				assert.Equal(t, 0, got.ResponseCount)
				assert.Equal(t, float64(0), got.CompletionRate)
				assert.Nil(t, got.AverageRating)
			},
		},
		{
			name:     "fails: survey not found",
			surveyID: uuid.Must(uuid.NewV7()),
			tenantID: tenantID,
			setupMocks: func(d testSurveyDeps) {
				d.surveyR.On("GetByID", mock.Anything, mock.Anything, tenantID).Return(nil, nil)
			},
			wantErr: ErrSurveyNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSurveyService()
			tc.setupMocks(d)

			got, err := d.svc.GetAnalytics(context.Background(), tc.surveyID, tc.tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
			} else {
				assert.NoError(t, err)
				if assert.NotNil(t, got) && tc.assertResult != nil {
					tc.assertResult(t, got)
				}
			}

			d.surveyR.AssertExpectations(t)
			d.responseR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestDeleteSurvey
// ---------------------------------------------------------------------------

func TestDeleteSurvey(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	surveyID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		id         uuid.UUID
		tenantID   uuid.UUID
		setupMocks func(d testSurveyDeps)
		wantErr    error
	}{
		{
			name:     "success: soft-deletes survey without responses",
			id:       surveyID,
			tenantID: tenantID,
			setupMocks: func(d testSurveyDeps) {
				d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
					ID:       surveyID,
					TenantID: tenantID,
					Status:   SurveyStatusDraft,
				}, nil)
				d.responseR.On("CountBySurvey", mock.Anything, surveyID).Return(0, nil)
				d.surveyR.On("Delete", mock.Anything, surveyID, tenantID).Return(nil)
			},
		},
		{
			name:     "warning: deletes survey with responses (returns ErrSurveyHasResponses)",
			id:       surveyID,
			tenantID: tenantID,
			setupMocks: func(d testSurveyDeps) {
				d.surveyR.On("GetByID", mock.Anything, surveyID, tenantID).Return(&SurveyTemplate{
					ID:       surveyID,
					TenantID: tenantID,
					Status:   SurveyStatusPublished,
				}, nil)
				d.responseR.On("CountBySurvey", mock.Anything, surveyID).Return(5, nil)
				d.surveyR.On("Delete", mock.Anything, surveyID, tenantID).Return(nil)
			},
			wantErr: ErrSurveyHasResponses,
		},
		{
			name:     "fails: survey not found",
			id:       uuid.Must(uuid.NewV7()),
			tenantID: tenantID,
			setupMocks: func(d testSurveyDeps) {
				d.surveyR.On("GetByID", mock.Anything, mock.Anything, tenantID).Return(nil, nil)
			},
			wantErr: ErrSurveyNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSurveyService()
			tc.setupMocks(d)

			err := d.svc.DeleteSurvey(context.Background(), tc.id, tc.tenantID)

			if tc.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				assert.NoError(t, err)
			}

			d.surveyR.AssertExpectations(t)
			d.responseR.AssertExpectations(t)
		})
	}
}
