package survey

import (
	"context"

	"github.com/google/uuid"
)

// SurveyTemplateRepository defines the data access interface for SurveyTemplate entities.
type SurveyTemplateRepository interface {
	// Create persists a new survey template.
	Create(ctx context.Context, survey *SurveyTemplate) error

	// GetByID retrieves a survey template by ID within a tenant.
	// Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*SurveyTemplate, error)

	// List returns survey templates for a tenant with cursor-based pagination.
	List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]SurveyTemplate, error)

	// Update saves changes to an existing survey template.
	Update(ctx context.Context, survey *SurveyTemplate) error

	// Delete soft-deletes a survey template.
	Delete(ctx context.Context, id, tenantID uuid.UUID) error
}

// SurveyQuestionRepository defines the data access interface for SurveyQuestion entities.
type SurveyQuestionRepository interface {
	// CreateBatch persists multiple survey questions for a survey.
	CreateBatch(ctx context.Context, questions []SurveyQuestion) error

	// ListBySurvey returns all questions for a survey ordered by order_index.
	ListBySurvey(ctx context.Context, surveyID uuid.UUID) ([]SurveyQuestion, error)

	// DeleteBySurvey removes all questions for a survey (used on update).
	DeleteBySurvey(ctx context.Context, surveyID uuid.UUID) error
}

// SurveyResponseRepository defines the data access interface for SurveyResponse entities.
type SurveyResponseRepository interface {
	// Create persists a new survey response (append-only).
	Create(ctx context.Context, response *SurveyResponse) error

	// ListBySurvey returns responses for a survey with cursor-based pagination.
	ListBySurvey(ctx context.Context, surveyID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]SurveyResponse, error)

	// ExistsByGCID checks if a GCID has already responded to a survey.
	ExistsByGCID(ctx context.Context, surveyID, gcid uuid.UUID) (bool, error)

	// CountBySurvey returns the number of responses for a survey.
	CountBySurvey(ctx context.Context, surveyID uuid.UUID) (int, error)
}

// EventPublisher abstracts the event bus (Cloud Pub/Sub in production,
// in-memory or emulator in tests/local dev).
type EventPublisher interface {
	// Publish sends a domain event to the specified topic.
	Publish(ctx context.Context, topic string, event interface{}) error

	// Close releases resources held by the publisher.
	Close() error
}
