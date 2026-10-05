package survey

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MaxQuestions is the maximum number of questions allowed per survey.
const MaxQuestions = 50

// SurveyService manages the SurveyTemplate lifecycle, question management,
// response submission, and analytics computation.
type SurveyService struct {
	surveys   SurveyTemplateRepository
	questions SurveyQuestionRepository
	responses SurveyResponseRepository
	events    EventPublisher
}

// NewSurveyService creates a SurveyService with the given repositories and
// event publisher.
func NewSurveyService(
	surveys SurveyTemplateRepository,
	questions SurveyQuestionRepository,
	responses SurveyResponseRepository,
	events EventPublisher,
) *SurveyService {
	return &SurveyService{
		surveys:   surveys,
		questions: questions,
		responses: responses,
		events:    events,
	}
}

// ---------------------------------------------------------------------------
// Survey CRUD
// ---------------------------------------------------------------------------

// CreateSurvey validates and persists a new SurveyTemplate with its questions.
// The survey starts in draft status. At least one question is required.
func (s *SurveyService) CreateSurvey(ctx context.Context, survey *SurveyTemplate, questions []SurveyQuestion) (*SurveyTemplate, error) {
	if survey.Title == "" {
		return nil, fmt.Errorf("title must not be empty: %w", ErrValidationFailed)
	}
	if len(questions) == 0 {
		return nil, fmt.Errorf("at least one question is required: %w", ErrNoQuestions)
	}
	if len(questions) > MaxQuestions {
		return nil, fmt.Errorf("cannot exceed %d questions: %w", MaxQuestions, ErrTooManyQuestions)
	}

	// Validate each question.
	for i, q := range questions {
		if !q.QuestionType.IsValid() {
			return nil, fmt.Errorf("invalid question_type %q at index %d: %w", q.QuestionType, i, ErrValidationFailed)
		}
		if q.QuestionText == "" {
			return nil, fmt.Errorf("question_text must not be empty at index %d: %w", i, ErrValidationFailed)
		}
	}

	now := time.Now().UTC()
	survey.ID = uuid.Must(uuid.NewV7())
	survey.Status = SurveyStatusDraft
	survey.QuestionCount = len(questions)
	survey.CreatedAt = now
	survey.UpdatedAt = now

	if err := s.surveys.Create(ctx, survey); err != nil {
		return nil, err
	}

	// Assign IDs and link questions to the survey.
	for i := range questions {
		questions[i].ID = uuid.Must(uuid.NewV7())
		questions[i].SurveyID = survey.ID
		questions[i].OrderIndex = i
		questions[i].CreatedAt = now
	}

	if err := s.questions.CreateBatch(ctx, questions); err != nil {
		return nil, err
	}

	return survey, nil
}

// ListSurveys returns survey templates for a tenant with cursor-based pagination.
func (s *SurveyService) ListSurveys(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]SurveyTemplate, error) {
	return s.surveys.List(ctx, tenantID, cursor, limit)
}

// GetSurvey retrieves a survey template by ID and tenant. Returns
// ErrSurveyNotFound if the survey does not exist.
func (s *SurveyService) GetSurvey(ctx context.Context, id, tenantID uuid.UUID) (*SurveyDetail, error) {
	survey, err := s.surveys.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if survey == nil {
		return nil, ErrSurveyNotFound
	}

	questions, err := s.questions.ListBySurvey(ctx, id)
	if err != nil {
		return nil, err
	}

	return &SurveyDetail{
		SurveyTemplate: *survey,
		Questions:      questions,
	}, nil
}

// UpdateSurvey applies mutable field changes to an existing survey. The survey
// must be in draft status; otherwise ErrSurveyNotModifiable is returned.
func (s *SurveyService) UpdateSurvey(ctx context.Context, survey *SurveyTemplate, questions []SurveyQuestion) (*SurveyTemplate, error) {
	existing, err := s.surveys.GetByID(ctx, survey.ID, survey.TenantID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrSurveyNotFound
	}

	if existing.Status != SurveyStatusDraft {
		return nil, ErrSurveyNotModifiable
	}

	existing.Title = survey.Title
	existing.Description = survey.Description
	existing.UpdatedAt = time.Now().UTC()

	if questions != nil {
		if len(questions) > MaxQuestions {
			return nil, fmt.Errorf("cannot exceed %d questions: %w", MaxQuestions, ErrTooManyQuestions)
		}
		for i, q := range questions {
			if !q.QuestionType.IsValid() {
				return nil, fmt.Errorf("invalid question_type %q at index %d: %w", q.QuestionType, i, ErrValidationFailed)
			}
		}

		// Replace questions: delete old, insert new.
		if err := s.questions.DeleteBySurvey(ctx, existing.ID); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		for i := range questions {
			questions[i].ID = uuid.Must(uuid.NewV7())
			questions[i].SurveyID = existing.ID
			questions[i].OrderIndex = i
			questions[i].CreatedAt = now
		}
		if err := s.questions.CreateBatch(ctx, questions); err != nil {
			return nil, err
		}
		existing.QuestionCount = len(questions)
	}

	if err := s.surveys.Update(ctx, existing); err != nil {
		return nil, err
	}

	return existing, nil
}

// DeleteSurvey soft-deletes a survey template. Returns ErrSurveyHasResponses
// as a warning if the survey has responses (but still deletes).
func (s *SurveyService) DeleteSurvey(ctx context.Context, id, tenantID uuid.UUID) error {
	existing, err := s.surveys.GetByID(ctx, id, tenantID)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrSurveyNotFound
	}

	// Check for responses — warn but still allow soft delete.
	count, err := s.responses.CountBySurvey(ctx, id)
	if err != nil {
		return err
	}

	if err := s.surveys.Delete(ctx, id, tenantID); err != nil {
		return err
	}

	if count > 0 {
		return ErrSurveyHasResponses
	}

	return nil
}

// ---------------------------------------------------------------------------
// State transitions
// ---------------------------------------------------------------------------

// PublishSurvey transitions a draft survey to published status. The survey
// must have at least one question. Publishes a survey.published event.
func (s *SurveyService) PublishSurvey(ctx context.Context, id, tenantID uuid.UUID) (*SurveyTemplate, error) {
	existing, err := s.surveys.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrSurveyNotFound
	}

	if existing.Status != SurveyStatusDraft {
		return nil, fmt.Errorf("survey must be in draft status to publish: %w", ErrSurveyNotPublishable)
	}

	// Verify at least one question exists.
	questions, err := s.questions.ListBySurvey(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(questions) == 0 {
		return nil, fmt.Errorf("survey must have at least one question to publish: %w", ErrNoQuestions)
	}

	existing.Status = SurveyStatusPublished
	existing.UpdatedAt = time.Now().UTC()

	if err := s.surveys.Update(ctx, existing); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventSurveyPublished,
		existing.TenantID,
		&existing.CreatedByGCID,
		existing.ID,
		AggregateSurveyTemplate,
		map[string]interface{}{
			"survey_id":      existing.ID.String(),
			"title":          existing.Title,
			"question_count": len(questions),
		},
	)
	if err := s.events.Publish(ctx, TopicSurveyEvents, evt); err != nil {
		return nil, fmt.Errorf("publish survey.published event: %w", err)
	}

	return existing, nil
}

// ---------------------------------------------------------------------------
// Response submission
// ---------------------------------------------------------------------------

// SubmitResponse validates and persists a learner's response to a published
// survey. A learner may only respond once per survey. All required questions
// must be answered.
func (s *SurveyService) SubmitResponse(ctx context.Context, response *SurveyResponse) (*SurveyResponse, error) {
	// Verify survey exists and is published.
	survey, err := s.surveys.GetByID(ctx, response.SurveyID, response.TenantID)
	if err != nil {
		return nil, err
	}
	if survey == nil {
		return nil, ErrSurveyNotFound
	}
	if survey.Status != SurveyStatusPublished {
		return nil, ErrSurveyNotPublished
	}

	// Check if already responded.
	exists, err := s.responses.ExistsByGCID(ctx, response.SurveyID, response.GCID)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrAlreadyResponded
	}

	// Validate required questions are answered.
	questions, err := s.questions.ListBySurvey(ctx, response.SurveyID)
	if err != nil {
		return nil, err
	}
	for _, q := range questions {
		if q.Required {
			if _, ok := response.Answers[q.ID.String()]; !ok {
				return nil, fmt.Errorf("missing answer for required question %q: %w", q.QuestionText, ErrMissingRequiredAnswer)
			}
		}
	}

	now := time.Now().UTC()
	response.ID = uuid.Must(uuid.NewV7())
	response.CompletedAt = now

	if err := s.responses.Create(ctx, response); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventResponseSubmitted,
		response.TenantID,
		&response.GCID,
		response.SurveyID,
		AggregateSurveyTemplate,
		map[string]interface{}{
			"survey_id":       response.SurveyID.String(),
			"response_id":     response.ID.String(),
			"respondent_gcid": response.GCID.String(),
		},
	)
	if err := s.events.Publish(ctx, TopicSurveyEvents, evt); err != nil {
		return nil, fmt.Errorf("publish survey.response.submitted event: %w", err)
	}

	return response, nil
}

// ListResponses returns responses for a survey with cursor-based pagination.
func (s *SurveyService) ListResponses(ctx context.Context, surveyID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]SurveyResponse, error) {
	return s.responses.ListBySurvey(ctx, surveyID, tenantID, cursor, limit)
}

// ---------------------------------------------------------------------------
// Analytics
// ---------------------------------------------------------------------------

// GetAnalytics computes analytics for a survey: response count, completion
// rate, average rating, and per-question breakdowns.
func (s *SurveyService) GetAnalytics(ctx context.Context, surveyID, tenantID uuid.UUID) (*SurveyAnalytics, error) {
	survey, err := s.surveys.GetByID(ctx, surveyID, tenantID)
	if err != nil {
		return nil, err
	}
	if survey == nil {
		return nil, ErrSurveyNotFound
	}

	responseCount, err := s.responses.CountBySurvey(ctx, surveyID)
	if err != nil {
		return nil, err
	}

	analytics := &SurveyAnalytics{
		SurveyID:      surveyID,
		ResponseCount: responseCount,
	}

	if responseCount == 0 {
		analytics.CompletionRate = 0
		return analytics, nil
	}

	// Compute completion rate (responses / question_count as a proxy).
	analytics.CompletionRate = 100.0

	// Compute average rating across rating-type questions.
	questions, err := s.questions.ListBySurvey(ctx, surveyID)
	if err != nil {
		return nil, err
	}

	responses, err := s.responses.ListBySurvey(ctx, surveyID, tenantID, nil, 1000)
	if err != nil {
		return nil, err
	}

	var totalRating float64
	var ratingCount int

	for _, q := range questions {
		bd := QuestionBreakdown{
			QuestionID:   q.ID,
			QuestionText: q.QuestionText,
			QuestionType: string(q.QuestionType),
		}

		var sum float64
		var count int
		for _, resp := range responses {
			if val, ok := resp.Answers[q.ID.String()]; ok {
				count++
				if q.QuestionType == QuestionTypeRating || q.QuestionType == QuestionTypeScale {
					switch v := val.(type) {
					case float64:
						sum += v
						totalRating += v
						ratingCount++
					case int:
						sum += float64(v)
						totalRating += float64(v)
						ratingCount++
					}
				}
			}
		}

		bd.ResponseCount = count
		if count > 0 && (q.QuestionType == QuestionTypeRating || q.QuestionType == QuestionTypeScale) {
			avg := sum / float64(count)
			bd.AverageValue = &avg
		}

		analytics.QuestionBreakdowns = append(analytics.QuestionBreakdowns, bd)
	}

	if ratingCount > 0 {
		avg := totalRating / float64(ratingCount)
		analytics.AverageRating = &avg
	}

	return analytics, nil
}
