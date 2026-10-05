package engagement

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestLeaderboardService_GetLeaderboard(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("returns entries and learner rank", func(t *testing.T) {
		t.Parallel()

		repo := new(mockLeaderboardRepo)

		entries := []LeaderboardEntry{
			{Rank: 1, GCID: uuid.Must(uuid.NewV7()), DisplayName: "Alice", TotalXP: 500, Level: 3},
			{Rank: 2, GCID: gcid, DisplayName: "Learner", TotalXP: 300, Level: 2},
		}
		learnerRank := &LeaderboardEntry{Rank: 2, GCID: gcid, DisplayName: "Learner", TotalXP: 300, Level: 2}

		repo.On("GetRanked", mock.Anything, tenantID, LeaderboardPeriodWeekly, LeaderboardScopeTenant, (*uuid.UUID)(nil), (*string)(nil), 10).Return(entries, nil)
		repo.On("GetLearnerRank", mock.Anything, gcid, tenantID, LeaderboardPeriodWeekly, LeaderboardScopeTenant, (*uuid.UUID)(nil)).Return(learnerRank, nil)

		svc := NewLeaderboardService(repo)
		gotEntries, gotRank, err := svc.GetLeaderboard(context.Background(), gcid, tenantID, LeaderboardPeriodWeekly, LeaderboardScopeTenant, nil, nil, 10)

		require.NoError(t, err)
		assert.Len(t, gotEntries, 2)
		assert.NotNil(t, gotRank)
		assert.Equal(t, 2, gotRank.Rank)
	})

	t.Run("returns nil learner rank when not ranked", func(t *testing.T) {
		t.Parallel()

		repo := new(mockLeaderboardRepo)

		entries := []LeaderboardEntry{
			{Rank: 1, GCID: uuid.Must(uuid.NewV7()), DisplayName: "Alice", TotalXP: 500},
		}

		repo.On("GetRanked", mock.Anything, tenantID, LeaderboardPeriodMonthly, LeaderboardScopeTenant, (*uuid.UUID)(nil), (*string)(nil), 10).Return(entries, nil)
		repo.On("GetLearnerRank", mock.Anything, gcid, tenantID, LeaderboardPeriodMonthly, LeaderboardScopeTenant, (*uuid.UUID)(nil)).Return(nil, nil)

		svc := NewLeaderboardService(repo)
		_, gotRank, err := svc.GetLeaderboard(context.Background(), gcid, tenantID, LeaderboardPeriodMonthly, LeaderboardScopeTenant, nil, nil, 10)

		require.NoError(t, err)
		assert.Nil(t, gotRank)
	})

	t.Run("propagates repository error", func(t *testing.T) {
		t.Parallel()

		repo := new(mockLeaderboardRepo)

		repo.On("GetRanked", mock.Anything, tenantID, LeaderboardPeriodWeekly, LeaderboardScopeTenant, (*uuid.UUID)(nil), (*string)(nil), 10).Return(nil, errors.New("db error"))

		svc := NewLeaderboardService(repo)
		_, _, err := svc.GetLeaderboard(context.Background(), gcid, tenantID, LeaderboardPeriodWeekly, LeaderboardScopeTenant, nil, nil, 10)

		require.Error(t, err)
		assert.ErrorContains(t, err, "db error")
	})
}
