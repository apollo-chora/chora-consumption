package survey

import "errors"

// Sentinel errors for the survey domain.
// Error codes use SURVEY_ prefix per error-handling conventions.
var (
	// ErrSurveyNotFound is returned when a survey template cannot be found.
	ErrSurveyNotFound = errors.New("SURVEY_NOT_FOUND")

	// ErrSurveyNotModifiable is returned when a survey is not in draft status
	// and therefore cannot be modified.
	ErrSurveyNotModifiable = errors.New("SURVEY_NOT_MODIFIABLE")

	// ErrSurveyNotPublishable is returned when a survey cannot be published
	// (e.g., already published or no questions).
	ErrSurveyNotPublishable = errors.New("SURVEY_NOT_PUBLISHABLE")

	// ErrSurveyNotPublished is returned when a response is submitted to a
	// survey that is not in published status.
	ErrSurveyNotPublished = errors.New("SURVEY_NOT_PUBLISHED")

	// ErrAlreadyResponded is returned when a learner has already responded
	// to the survey.
	ErrAlreadyResponded = errors.New("SURVEY_ALREADY_RESPONDED")

	// ErrMissingRequiredAnswer is returned when a required question is not
	// answered in the response.
	ErrMissingRequiredAnswer = errors.New("SURVEY_MISSING_REQUIRED_ANSWER")

	// ErrNoQuestions is returned when a survey has no questions.
	ErrNoQuestions = errors.New("SURVEY_NO_QUESTIONS")

	// ErrTooManyQuestions is returned when the question count exceeds the
	// maximum allowed (50).
	ErrTooManyQuestions = errors.New("SURVEY_TOO_MANY_QUESTIONS")

	// ErrSurveyHasResponses is returned as a warning when deleting a survey
	// that already has responses.
	ErrSurveyHasResponses = errors.New("SURVEY_HAS_RESPONSES")

	// ErrValidationFailed is returned when request validation fails.
	ErrValidationFailed = errors.New("SURVEY_VALIDATION_FAILED")

	// ErrNotFound is a generic not-found error for the survey domain.
	ErrNotFound = errors.New("SURVEY_NOT_FOUND")

	// ErrForbidden is returned when the caller lacks permission.
	ErrForbidden = errors.New("SURVEY_FORBIDDEN")

	// ErrUnauthorized is returned when authentication is required but missing.
	ErrUnauthorized = errors.New("SURVEY_UNAUTHORIZED")
)
