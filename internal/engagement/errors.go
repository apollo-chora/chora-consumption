package engagement

import "errors"

// Sentinel errors for the engagement domain.
// Error codes use ENGAGEMENT_ prefix per error-handling conventions.
var (
	// ErrDashboardNotFound is returned when no engagement data exists for a learner.
	ErrDashboardNotFound = errors.New("ENGAGEMENT_DASHBOARD_NOT_FOUND")

	// ErrDailyDoseNotAvailable is returned when no atoms are available for DailyDose.
	ErrDailyDoseNotAvailable = errors.New("ENGAGEMENT_DAILY_DOSE_NOT_AVAILABLE")

	// ErrDailyDoseAlreadyCompleted is returned when the session was already completed.
	ErrDailyDoseAlreadyCompleted = errors.New("ENGAGEMENT_DAILY_DOSE_ALREADY_COMPLETED")

	// ErrDailyDoseSessionNotFound is returned when a DailyDose session cannot be found.
	ErrDailyDoseSessionNotFound = errors.New("ENGAGEMENT_DAILY_DOSE_SESSION_NOT_FOUND")

	// ErrAtomNotFound is returned when a LearningAtom cannot be found
	// (cross-context UUID reference to chora-atomic).
	ErrAtomNotFound = errors.New("ENGAGEMENT_ATOM_NOT_FOUND")

	// ErrGoalNotFound is returned when a GoalChallenge cannot be found.
	ErrGoalNotFound = errors.New("ENGAGEMENT_GOAL_NOT_FOUND")

	// ErrGoalSlotsFull is returned when a learner already has MaxConcurrentGoals active goals.
	ErrGoalSlotsFull = errors.New("ENGAGEMENT_GOAL_SLOTS_FULL")

	// ErrGoalAlreadyAccepted is returned when attempting to accept an already-accepted goal.
	ErrGoalAlreadyAccepted = errors.New("ENGAGEMENT_GOAL_ALREADY_ACCEPTED")

	// ErrGoalExpired is returned when attempting to act on an expired goal.
	ErrGoalExpired = errors.New("ENGAGEMENT_GOAL_EXPIRED")

	// ErrGoalAlreadyDeclined is returned when attempting to decline an already-declined goal.
	ErrGoalAlreadyDeclined = errors.New("ENGAGEMENT_GOAL_ALREADY_DECLINED")

	// ErrNotificationNotFound is returned when an engagement notification cannot be found.
	ErrNotificationNotFound = errors.New("ENGAGEMENT_NOTIFICATION_NOT_FOUND")

	// ErrValidationFailed is returned when request validation fails.
	ErrValidationFailed = errors.New("ENGAGEMENT_VALIDATION_FAILED")

	// ErrDiscoveryStateNotFound is returned when a UIDiscoveryState cannot be found.
	ErrDiscoveryStateNotFound = errors.New("ENGAGEMENT_DISCOVERY_STATE_NOT_FOUND")

	// ErrFogAlreadyRevealed is returned when attempting to reveal an already-revealed node.
	ErrFogAlreadyRevealed = errors.New("ENGAGEMENT_FOG_ALREADY_REVEALED")

	// ErrFogAlreadyConquered is returned when attempting to conquer an already-conquered node.
	ErrFogAlreadyConquered = errors.New("ENGAGEMENT_FOG_ALREADY_CONQUERED")

	// ErrFogThresholdInvalid is returned when a fog threshold is outside the 0.1-1.0 range.
	ErrFogThresholdInvalid = errors.New("ENGAGEMENT_FOG_THRESHOLD_INVALID")

	// ErrStarAccountNotFound is returned when a StarAccount cannot be found.
	ErrStarAccountNotFound = errors.New("ENGAGEMENT_STAR_ACCOUNT_NOT_FOUND")

	// ErrStarAccountAlreadyExists is returned when a StarAccount already exists for a GCID+tenant.
	ErrStarAccountAlreadyExists = errors.New("ENGAGEMENT_STAR_ACCOUNT_ALREADY_EXISTS")
)
