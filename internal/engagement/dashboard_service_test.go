package engagement

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestDashboardService_GetDashboard(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("assembles dashboard with all components", func(t *testing.T) {
		t.Parallel()

		streakRepo := new(mockStreakRepo)
		xpRepo := new(mockXPLedgerRepo)
		goalRepo := new(mockGoalChallengeRepo)
		doseRepo := new(mockDailyDoseSessionRepo)
		publisher := new(mockEventPublisher)

		// Streak data
		streakRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(&Streak{
			CurrentDays:    10,
			LongestStreak:  15,
			Status:         StreakStatusActive,
			LastActivityAt: time.Now(),
		}, nil)
		streakRepo.On("ListMilestones", mock.Anything, gcid, tenantID).Return([]StreakMilestone{
			{Days: 7, RewardType: "xp_bonus"},
		}, nil)

		// XP data
		xpRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(&XPLedger{
			TotalXP:   250,
			Level:     2,
			ComboTier: ComboTierBronze,
		}, nil)
		xpRepo.On("ListEntries", mock.Anything, gcid, tenantID, 0).Return([]XPEntry{}, nil)

		// Goals
		activeStatus := GoalStatusActive
		goalRepo.On("ListByAssignee", mock.Anything, gcid, tenantID, &activeStatus, (*uuid.UUID)(nil), MaxConcurrentGoals).Return([]GoalChallenge{
			{Status: GoalStatusActive},
			{Status: GoalStatusActive},
		}, nil)

		// DailyDose — available but not completed
		doseRepo.On("GetTodayByGCID", mock.Anything, gcid, tenantID).Return(&DailyDoseSession{
			ID:        uuid.Must(uuid.NewV7()),
			CreatedAt: time.Now(),
		}, nil)

		streakSvc := NewStreakService(streakRepo, publisher)
		xpSvc := NewXPService(xpRepo, publisher)
		svc := NewDashboardService(streakSvc, xpSvc, goalRepo, doseRepo)

		dash, err := svc.GetDashboard(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.Equal(t, 10, dash.Streak.CurrentDays)
		assert.Equal(t, StreakStatusActive, dash.Streak.Status)
		assert.Equal(t, 250, dash.XP.TotalXP)
		assert.Equal(t, 2, dash.XP.Level)
		assert.Equal(t, 2, dash.XP.ComboMultiplier) // bronze = 2x
		assert.Equal(t, 2, dash.Level)
		assert.Equal(t, DailyDoseStatusAvailable, dash.DailyDoseStatus)
		assert.Equal(t, 2, dash.ActiveGoalsCount)
		assert.NotNil(t, dash.PathProgress) // empty slice, not nil
	})

	t.Run("dose status completed when session has CompletedAt", func(t *testing.T) {
		t.Parallel()

		streakRepo := new(mockStreakRepo)
		xpRepo := new(mockXPLedgerRepo)
		goalRepo := new(mockGoalChallengeRepo)
		doseRepo := new(mockDailyDoseSessionRepo)
		publisher := new(mockEventPublisher)

		streakRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
		streakRepo.On("ListMilestones", mock.Anything, gcid, tenantID).Return([]StreakMilestone{}, nil)
		xpRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
		xpRepo.On("ListEntries", mock.Anything, gcid, tenantID, 0).Return([]XPEntry{}, nil)
		activeStatus := GoalStatusActive
		goalRepo.On("ListByAssignee", mock.Anything, gcid, tenantID, &activeStatus, (*uuid.UUID)(nil), MaxConcurrentGoals).Return([]GoalChallenge{}, nil)

		completed := time.Now()
		doseRepo.On("GetTodayByGCID", mock.Anything, gcid, tenantID).Return(&DailyDoseSession{
			CompletedAt: &completed,
		}, nil)

		streakSvc := NewStreakService(streakRepo, publisher)
		xpSvc := NewXPService(xpRepo, publisher)
		svc := NewDashboardService(streakSvc, xpSvc, goalRepo, doseRepo)

		dash, err := svc.GetDashboard(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.Equal(t, DailyDoseStatusCompleted, dash.DailyDoseStatus)
	})

	t.Run("dose status not_configured when no session exists", func(t *testing.T) {
		t.Parallel()

		streakRepo := new(mockStreakRepo)
		xpRepo := new(mockXPLedgerRepo)
		goalRepo := new(mockGoalChallengeRepo)
		doseRepo := new(mockDailyDoseSessionRepo)
		publisher := new(mockEventPublisher)

		streakRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
		streakRepo.On("ListMilestones", mock.Anything, gcid, tenantID).Return([]StreakMilestone{}, nil)
		xpRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
		xpRepo.On("ListEntries", mock.Anything, gcid, tenantID, 0).Return([]XPEntry{}, nil)
		activeStatus := GoalStatusActive
		goalRepo.On("ListByAssignee", mock.Anything, gcid, tenantID, &activeStatus, (*uuid.UUID)(nil), MaxConcurrentGoals).Return([]GoalChallenge{}, nil)
		doseRepo.On("GetTodayByGCID", mock.Anything, gcid, tenantID).Return(nil, nil)

		streakSvc := NewStreakService(streakRepo, publisher)
		xpSvc := NewXPService(xpRepo, publisher)
		svc := NewDashboardService(streakSvc, xpSvc, goalRepo, doseRepo)

		dash, err := svc.GetDashboard(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.Equal(t, DailyDoseStatusNotConfigured, dash.DailyDoseStatus)
	})

	t.Run("propagates streak error", func(t *testing.T) {
		t.Parallel()

		streakRepo := new(mockStreakRepo)
		xpRepo := new(mockXPLedgerRepo)
		goalRepo := new(mockGoalChallengeRepo)
		doseRepo := new(mockDailyDoseSessionRepo)
		publisher := new(mockEventPublisher)

		streakRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, errors.New("streak error"))

		streakSvc := NewStreakService(streakRepo, publisher)
		xpSvc := NewXPService(xpRepo, publisher)
		svc := NewDashboardService(streakSvc, xpSvc, goalRepo, doseRepo)

		_, err := svc.GetDashboard(context.Background(), gcid, tenantID)

		require.Error(t, err)
		assert.ErrorContains(t, err, "streak error")
	})

	t.Run("propagates XP error", func(t *testing.T) {
		t.Parallel()

		streakRepo := new(mockStreakRepo)
		xpRepo := new(mockXPLedgerRepo)
		goalRepo := new(mockGoalChallengeRepo)
		doseRepo := new(mockDailyDoseSessionRepo)
		publisher := new(mockEventPublisher)

		streakRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
		streakRepo.On("ListMilestones", mock.Anything, gcid, tenantID).Return([]StreakMilestone{}, nil)
		xpRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, errors.New("xp error"))

		streakSvc := NewStreakService(streakRepo, publisher)
		xpSvc := NewXPService(xpRepo, publisher)
		svc := NewDashboardService(streakSvc, xpSvc, goalRepo, doseRepo)

		_, err := svc.GetDashboard(context.Background(), gcid, tenantID)

		require.Error(t, err)
		assert.ErrorContains(t, err, "xp error")
	})
}
