package engagement

import (
	"context"

	"github.com/google/uuid"
)

// StreakRepository defines the data access interface for Streak entities.
type StreakRepository interface {
	// GetByGCIDAndTenant retrieves a streak for the given GCID and tenant.
	// Returns nil if not found.
	GetByGCIDAndTenant(ctx context.Context, gcid, tenantID uuid.UUID) (*Streak, error)

	// Save creates or updates a Streak.
	Save(ctx context.Context, streak *Streak) error

	// ListMilestones returns all streak milestones for a GCID and tenant.
	ListMilestones(ctx context.Context, gcid, tenantID uuid.UUID) ([]StreakMilestone, error)

	// SaveMilestone records a streak milestone.
	SaveMilestone(ctx context.Context, gcid, tenantID uuid.UUID, milestone StreakMilestone) error
}

// XPLedgerRepository defines the data access interface for XP tracking.
type XPLedgerRepository interface {
	// GetByGCIDAndTenant retrieves the XP ledger for the given GCID and tenant.
	// Returns nil if not found.
	GetByGCIDAndTenant(ctx context.Context, gcid, tenantID uuid.UUID) (*XPLedger, error)

	// Save creates or updates an XPLedger.
	Save(ctx context.Context, ledger *XPLedger) error

	// AppendEntry records a new XP earning event (append-only).
	AppendEntry(ctx context.Context, entry *XPEntry) error

	// ListEntries returns recent XP entries for a GCID and tenant.
	ListEntries(ctx context.Context, gcid, tenantID uuid.UUID, limit int) ([]XPEntry, error)
}

// GoalChallengeRepository defines the data access interface for GoalChallenge entities.
type GoalChallengeRepository interface {
	// GetByID retrieves a goal challenge by ID. Returns nil if not found.
	GetByID(ctx context.Context, id uuid.UUID) (*GoalChallenge, error)

	// ListByAssignee returns goal challenges for a learner with optional status filter.
	// Supports cursor-based pagination.
	ListByAssignee(ctx context.Context, gcid, tenantID uuid.UUID, status *GoalStatus, cursor *uuid.UUID, limit int) ([]GoalChallenge, error)

	// CountActiveByAssignee returns the count of active goals for a learner.
	CountActiveByAssignee(ctx context.Context, gcid, tenantID uuid.UUID) (int, error)

	// Save creates or updates a GoalChallenge.
	Save(ctx context.Context, goal *GoalChallenge) error
}

// DailyDoseSessionRepository defines the data access interface for DailyDoseSession entities.
type DailyDoseSessionRepository interface {
	// GetByID retrieves a session by ID. Returns nil if not found.
	GetByID(ctx context.Context, id uuid.UUID) (*DailyDoseSession, error)

	// GetTodayByGCID retrieves today's session for a GCID and tenant.
	// Returns nil if no session exists for today.
	GetTodayByGCID(ctx context.Context, gcid, tenantID uuid.UUID) (*DailyDoseSession, error)

	// Save creates or updates a DailyDoseSession.
	Save(ctx context.Context, session *DailyDoseSession) error
}

// LeaderboardRepository defines the read model for leaderboard queries.
type LeaderboardRepository interface {
	// GetRanked returns ranked leaderboard entries for the given period and scope.
	GetRanked(ctx context.Context, tenantID uuid.UUID, period LeaderboardPeriod, scope LeaderboardScope, scopeID *uuid.UUID, cursor *string, limit int) ([]LeaderboardEntry, error)

	// GetLearnerRank returns the requesting learner's own rank.
	// Returns nil if the learner has no ranking data.
	GetLearnerRank(ctx context.Context, gcid, tenantID uuid.UUID, period LeaderboardPeriod, scope LeaderboardScope, scopeID *uuid.UUID) (*LeaderboardEntry, error)
}

// NotificationRepository defines the data access interface for engagement notifications.
type NotificationRepository interface {
	// ListByGCID returns notifications for a GCID and tenant with optional unread filter.
	ListByGCID(ctx context.Context, gcid, tenantID uuid.UUID, unreadOnly bool, cursor *uuid.UUID, limit int) ([]EngagementNotification, error)

	// GetByID retrieves a notification by ID. Returns nil if not found.
	GetByID(ctx context.Context, id uuid.UUID) (*EngagementNotification, error)

	// Save creates a notification.
	Save(ctx context.Context, notification *EngagementNotification) error

	// MarkRead marks a notification as read.
	MarkRead(ctx context.Context, id uuid.UUID) error
}

// NotificationPreferencesRepository defines the data access interface for notification preferences.
type NotificationPreferencesRepository interface {
	// GetByGCIDAndTenant retrieves preferences for the given GCID and tenant.
	// Returns nil if not found (use defaults).
	GetByGCIDAndTenant(ctx context.Context, gcid, tenantID uuid.UUID) (*NotificationPreferences, error)

	// Save creates or updates notification preferences.
	Save(ctx context.Context, prefs *NotificationPreferences) error
}

// PredictedGradeRepository defines the data access interface for PredictedGrade entities.
type PredictedGradeRepository interface {
	// ListByGCID returns predicted grades for a learner, optionally filtered by topic.
	ListByGCID(ctx context.Context, gcid, tenantID uuid.UUID, topicID *uuid.UUID) ([]PredictedGrade, error)

	// Upsert creates or updates a predicted grade for a learner+topic pair.
	Upsert(ctx context.Context, grade *PredictedGrade) error
}

// UIDiscoveryStateRepository defines the data access interface for fog-of-war state.
type UIDiscoveryStateRepository interface {
	// GetByGCIDAndTopicNode retrieves discovery state for a learner+topic pair.
	// Returns nil if not found (node is hidden by default).
	GetByGCIDAndTopicNode(ctx context.Context, gcid, tenantID, topicNodeID uuid.UUID) (*UIDiscoveryState, error)

	// ListByGCID returns all discovery states for a learner within a tenant.
	ListByGCID(ctx context.Context, gcid, tenantID uuid.UUID) ([]UIDiscoveryState, error)

	// Save creates or updates a UIDiscoveryState.
	Save(ctx context.Context, state *UIDiscoveryState) error
}

// StarAccountRepository defines the data access interface for StarAccount entities.
type StarAccountRepository interface {
	// GetByGCID retrieves a star account for the given GCID and tenant.
	// Returns nil if not found.
	GetByGCID(ctx context.Context, gcid, tenantID uuid.UUID) (*StarAccount, error)

	// Create creates a new StarAccount.
	Create(ctx context.Context, account *StarAccount) error

	// Update updates an existing StarAccount.
	Update(ctx context.Context, account *StarAccount) error
}

// EventPublisher abstracts the event bus (Cloud Pub/Sub in production,
// in-memory or emulator in tests/local dev).
type EventPublisher interface {
	// Publish sends a domain event to the specified topic.
	Publish(ctx context.Context, topic string, event interface{}) error
}
