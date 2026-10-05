package survey

import (
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

// SurveyStatus represents the lifecycle state of a survey template.
type SurveyStatus string

const (
	SurveyStatusDraft     SurveyStatus = "draft"
	SurveyStatusPublished SurveyStatus = "published"
	SurveyStatusArchived  SurveyStatus = "archived"
)

// IsValid checks whether the SurveyStatus value is a known enum member.
func (s SurveyStatus) IsValid() bool {
	switch s {
	case SurveyStatusDraft, SurveyStatusPublished, SurveyStatusArchived:
		return true
	}
	return false
}

// QuestionType represents the type of survey question.
type QuestionType string

const (
	QuestionTypeRating         QuestionType = "rating"
	QuestionTypeText           QuestionType = "text"
	QuestionTypeMultipleChoice QuestionType = "multiple_choice"
	QuestionTypeScale          QuestionType = "scale"
)

// IsValid checks whether the QuestionType value is a known enum member.
func (q QuestionType) IsValid() bool {
	switch q {
	case QuestionTypeRating, QuestionTypeText, QuestionTypeMultipleChoice,
		QuestionTypeScale:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Domain Entities
// ---------------------------------------------------------------------------

// SurveyTemplate is the aggregate root for surveys.
type SurveyTemplate struct {
	ID              uuid.UUID    `json:"id"`
	TenantID        uuid.UUID    `json:"tenant_id"`
	Title           string       `json:"title"`
	Description     string       `json:"description"`
	Status          SurveyStatus `json:"status"`
	LinkedSessionID *uuid.UUID   `json:"linked_session_id,omitempty"`
	QuestionCount   int          `json:"question_count"`
	CreatedByGCID   uuid.UUID    `json:"created_by_gcid"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
	DeletedAt       *time.Time   `json:"deleted_at,omitempty"`
}

// SurveyQuestion is a child entity of SurveyTemplate, accessed only
// through the aggregate root.
type SurveyQuestion struct {
	ID           uuid.UUID      `json:"id"`
	SurveyID     uuid.UUID      `json:"survey_id"`
	QuestionText string         `json:"question_text"`
	QuestionType QuestionType   `json:"question_type"`
	Options      map[string]any `json:"options,omitempty"`
	OrderIndex   int            `json:"order_index"`
	Required     bool           `json:"required"`
	CreatedAt    time.Time      `json:"created_at"`
}

// SurveyResponse is an append-only record of a learner's response to a survey.
type SurveyResponse struct {
	ID          uuid.UUID      `json:"id"`
	TenantID    uuid.UUID      `json:"tenant_id"`
	SurveyID    uuid.UUID      `json:"survey_id"`
	GCID        uuid.UUID      `json:"gcid"`
	Answers     map[string]any `json:"answers"`
	CompletedAt time.Time      `json:"completed_at"`
}

// SurveyAnalytics is a read model for survey analytics.
type SurveyAnalytics struct {
	SurveyID           uuid.UUID           `json:"survey_id"`
	ResponseCount      int                 `json:"response_count"`
	CompletionRate     float64             `json:"completion_rate"`
	AverageRating      *float64            `json:"average_rating,omitempty"`
	QuestionBreakdowns []QuestionBreakdown `json:"question_breakdowns,omitempty"`
}

// QuestionBreakdown provides per-question analytics.
type QuestionBreakdown struct {
	QuestionID    uuid.UUID `json:"question_id"`
	QuestionText  string    `json:"question_text"`
	QuestionType  string    `json:"question_type"`
	AverageValue  *float64  `json:"average_value,omitempty"`
	ResponseCount int       `json:"response_count"`
}

// SurveyDetail is a read model that includes the template and its questions.
type SurveyDetail struct {
	SurveyTemplate
	Questions []SurveyQuestion `json:"questions"`
}

// PageInfo contains cursor-based pagination metadata.
type PageInfo struct {
	NextCursor *string `json:"next_cursor,omitempty"`
	HasNext    bool    `json:"has_next"`
}
