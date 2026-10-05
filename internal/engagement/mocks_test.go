package engagement

import (
	"context"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
)

// Compile-time interface checks.
var (
	_ StreakRepository                  = (*mockStreakRepo)(nil)
	_ XPLedgerRepository                = (*mockXPLedgerRepo)(nil)
	_ GoalChallengeRepository           = (*mockGoalChallengeRepo)(nil)
	_ DailyDoseSessionRepository        = (*mockDailyDoseSessionRepo)(nil)
	_ LeaderboardRepository             = (*mockLeaderboardRepo)(nil)
	_ NotificationRepository            = (*mockNotificationRepo)(nil)
	_ NotificationPreferencesRepository = (*mockNotificationPrefsRepo)(nil)
	_ UIDiscoveryStateRepository        = (*mockDiscoveryStateRepo)(nil)
	_ EventPublisher                    = (*mockEventPublisher)(nil)
)

// ---------------------------------------------------------------------------
// mockStreakRepo
// ---------------------------------------------------------------------------

type mockStreakRepo struct{ mock.Mock }

func (m *mockStreakRepo) GetByGCIDAndTenant(ctx context.Context, gcid, tenantID uuid.UUID) (*Streak, error) {
	args := m.Called(ctx, gcid, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*Streak), args.Error(1)
}

func (m *mockStreakRepo) Save(ctx context.Context, streak *Streak) error {
	return m.Called(ctx, streak).Error(0)
}

func (m *mockStreakRepo) ListMilestones(ctx context.Context, gcid, tenantID uuid.UUID) ([]StreakMilestone, error) {
	args := m.Called(ctx, gcid, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]StreakMilestone), args.Error(1)
}

func (m *mockStreakRepo) SaveMilestone(ctx context.Context, gcid, tenantID uuid.UUID, milestone StreakMilestone) error {
	return m.Called(ctx, gcid, tenantID, milestone).Error(0)
}

// ---------------------------------------------------------------------------
// mockXPLedgerRepo
// ---------------------------------------------------------------------------

type mockXPLedgerRepo struct{ mock.Mock }

func (m *mockXPLedgerRepo) GetByGCIDAndTenant(ctx context.Context, gcid, tenantID uuid.UUID) (*XPLedger, error) {
	args := m.Called(ctx, gcid, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*XPLedger), args.Error(1)
}

func (m *mockXPLedgerRepo) Save(ctx context.Context, ledger *XPLedger) error {
	return m.Called(ctx, ledger).Error(0)
}

func (m *mockXPLedgerRepo) AppendEntry(ctx context.Context, entry *XPEntry) error {
	return m.Called(ctx, entry).Error(0)
}

func (m *mockXPLedgerRepo) ListEntries(ctx context.Context, gcid, tenantID uuid.UUID, limit int) ([]XPEntry, error) {
	args := m.Called(ctx, gcid, tenantID, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]XPEntry), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockGoalChallengeRepo
// ---------------------------------------------------------------------------

type mockGoalChallengeRepo struct{ mock.Mock }

func (m *mockGoalChallengeRepo) GetByID(ctx context.Context, id uuid.UUID) (*GoalChallenge, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*GoalChallenge), args.Error(1)
}

func (m *mockGoalChallengeRepo) ListByAssignee(ctx context.Context, gcid, tenantID uuid.UUID, status *GoalStatus, cursor *uuid.UUID, limit int) ([]GoalChallenge, error) {
	args := m.Called(ctx, gcid, tenantID, status, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]GoalChallenge), args.Error(1)
}

func (m *mockGoalChallengeRepo) CountActiveByAssignee(ctx context.Context, gcid, tenantID uuid.UUID) (int, error) {
	args := m.Called(ctx, gcid, tenantID)
	return args.Int(0), args.Error(1)
}

func (m *mockGoalChallengeRepo) Save(ctx context.Context, goal *GoalChallenge) error {
	return m.Called(ctx, goal).Error(0)
}

// ---------------------------------------------------------------------------
// mockDailyDoseSessionRepo
// ---------------------------------------------------------------------------

type mockDailyDoseSessionRepo struct{ mock.Mock }

func (m *mockDailyDoseSessionRepo) GetByID(ctx context.Context, id uuid.UUID) (*DailyDoseSession, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*DailyDoseSession), args.Error(1)
}

func (m *mockDailyDoseSessionRepo) GetTodayByGCID(ctx context.Context, gcid, tenantID uuid.UUID) (*DailyDoseSession, error) {
	args := m.Called(ctx, gcid, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*DailyDoseSession), args.Error(1)
}

func (m *mockDailyDoseSessionRepo) Save(ctx context.Context, session *DailyDoseSession) error {
	return m.Called(ctx, session).Error(0)
}

// ---------------------------------------------------------------------------
// mockLeaderboardRepo
// ---------------------------------------------------------------------------

type mockLeaderboardRepo struct{ mock.Mock }

func (m *mockLeaderboardRepo) GetRanked(ctx context.Context, tenantID uuid.UUID, period LeaderboardPeriod, scope LeaderboardScope, scopeID *uuid.UUID, cursor *string, limit int) ([]LeaderboardEntry, error) {
	args := m.Called(ctx, tenantID, period, scope, scopeID, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]LeaderboardEntry), args.Error(1)
}

func (m *mockLeaderboardRepo) GetLearnerRank(ctx context.Context, gcid, tenantID uuid.UUID, period LeaderboardPeriod, scope LeaderboardScope, scopeID *uuid.UUID) (*LeaderboardEntry, error) {
	args := m.Called(ctx, gcid, tenantID, period, scope, scopeID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*LeaderboardEntry), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockNotificationRepo
// ---------------------------------------------------------------------------

type mockNotificationRepo struct{ mock.Mock }

func (m *mockNotificationRepo) ListByGCID(ctx context.Context, gcid, tenantID uuid.UUID, unreadOnly bool, cursor *uuid.UUID, limit int) ([]EngagementNotification, error) {
	args := m.Called(ctx, gcid, tenantID, unreadOnly, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]EngagementNotification), args.Error(1)
}

func (m *mockNotificationRepo) GetByID(ctx context.Context, id uuid.UUID) (*EngagementNotification, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*EngagementNotification), args.Error(1)
}

func (m *mockNotificationRepo) Save(ctx context.Context, notification *EngagementNotification) error {
	return m.Called(ctx, notification).Error(0)
}

func (m *mockNotificationRepo) MarkRead(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

// ---------------------------------------------------------------------------
// mockNotificationPrefsRepo
// ---------------------------------------------------------------------------

type mockNotificationPrefsRepo struct{ mock.Mock }

func (m *mockNotificationPrefsRepo) GetByGCIDAndTenant(ctx context.Context, gcid, tenantID uuid.UUID) (*NotificationPreferences, error) {
	args := m.Called(ctx, gcid, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*NotificationPreferences), args.Error(1)
}

func (m *mockNotificationPrefsRepo) Save(ctx context.Context, prefs *NotificationPreferences) error {
	return m.Called(ctx, prefs).Error(0)
}

// ---------------------------------------------------------------------------
// mockDiscoveryStateRepo
// ---------------------------------------------------------------------------

type mockDiscoveryStateRepo struct{ mock.Mock }

func (m *mockDiscoveryStateRepo) GetByGCIDAndTopicNode(ctx context.Context, gcid, tenantID, topicNodeID uuid.UUID) (*UIDiscoveryState, error) {
	args := m.Called(ctx, gcid, tenantID, topicNodeID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*UIDiscoveryState), args.Error(1)
}

func (m *mockDiscoveryStateRepo) ListByGCID(ctx context.Context, gcid, tenantID uuid.UUID) ([]UIDiscoveryState, error) {
	args := m.Called(ctx, gcid, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]UIDiscoveryState), args.Error(1)
}

func (m *mockDiscoveryStateRepo) Save(ctx context.Context, state *UIDiscoveryState) error {
	return m.Called(ctx, state).Error(0)
}

// ---------------------------------------------------------------------------
// mockEventPublisher
// ---------------------------------------------------------------------------

type mockEventPublisher struct{ mock.Mock }

func (m *mockEventPublisher) Publish(ctx context.Context, topic string, event interface{}) error {
	return m.Called(ctx, topic, event).Error(0)
}
