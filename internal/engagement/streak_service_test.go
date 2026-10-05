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

func TestStreakService_GetStreak(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name           string
		setupRepo      func(*mockStreakRepo)
		wantErr        bool
		wantStatus     StreakStatus
		wantMilestones int
	}{
		{
			name: "returns existing streak with milestones",
			setupRepo: func(r *mockStreakRepo) {
				r.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(&Streak{
					ID:            uuid.Must(uuid.NewV7()),
					GCID:          gcid,
					TenantID:      tenantID,
					CurrentDays:   15,
					LongestStreak: 15,
					Status:        StreakStatusActive,
				}, nil)
				r.On("ListMilestones", mock.Anything, gcid, tenantID).Return([]StreakMilestone{
					{Days: 7, ReachedAt: time.Now(), RewardType: "xp_bonus"},
				}, nil)
			},
			wantStatus:     StreakStatusActive,
			wantMilestones: 1,
		},
		{
			name: "returns zero-value streak when not found",
			setupRepo: func(r *mockStreakRepo) {
				r.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
				r.On("ListMilestones", mock.Anything, gcid, tenantID).Return([]StreakMilestone{}, nil)
			},
			wantStatus:     StreakStatusBroken,
			wantMilestones: 0,
		},
		{
			name: "propagates repository error",
			setupRepo: func(r *mockStreakRepo) {
				r.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, errors.New("db error"))
			},
			wantErr: true,
		},
		{
			name: "propagates milestone list error",
			setupRepo: func(r *mockStreakRepo) {
				r.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(&Streak{
					Status: StreakStatusActive,
				}, nil)
				r.On("ListMilestones", mock.Anything, gcid, tenantID).Return(nil, errors.New("milestone error"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := new(mockStreakRepo)
			publisher := new(mockEventPublisher)
			tt.setupRepo(repo)

			svc := NewStreakService(repo, publisher)
			streak, milestones, err := svc.GetStreak(context.Background(), gcid, tenantID)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, streak.Status)
			assert.Len(t, milestones, tt.wantMilestones)
			repo.AssertExpectations(t)
		})
	}
}

func TestStreakService_RecordActivity(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("first activity creates streak with day 1", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStreakRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.Streak")).Return(nil)
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.Anything).Return(nil)

		svc := NewStreakService(repo, publisher)
		streak, err := svc.RecordActivity(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.Equal(t, 1, streak.CurrentDays)
		assert.Equal(t, 1, streak.LongestStreak)
		assert.Equal(t, StreakStatusActive, streak.Status)
		repo.AssertExpectations(t)
	})

	t.Run("new-day activity increments CurrentDays", func(t *testing.T) {
		t.Parallel()

		yesterday := time.Now().UTC().Add(-24 * time.Hour)
		existingStreak := &Streak{
			ID:             uuid.Must(uuid.NewV7()),
			GCID:           gcid,
			TenantID:       tenantID,
			CurrentDays:    5,
			LongestStreak:  10,
			Status:         StreakStatusActive,
			LastActivityAt: yesterday,
		}

		repo := new(mockStreakRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(existingStreak, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.Streak")).Return(nil)
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.Anything).Return(nil)

		svc := NewStreakService(repo, publisher)
		streak, err := svc.RecordActivity(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		// Streak should increment from 5 to 6 because this is a new day.
		assert.Equal(t, 6, streak.CurrentDays, "CurrentDays should increment on a new day")
	})

	t.Run("same-day activity does not increment CurrentDays", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		existingStreak := &Streak{
			ID:             uuid.Must(uuid.NewV7()),
			GCID:           gcid,
			TenantID:       tenantID,
			CurrentDays:    5,
			LongestStreak:  10,
			Status:         StreakStatusActive,
			LastActivityAt: now,
		}

		repo := new(mockStreakRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(existingStreak, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.Streak")).Return(nil)
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.Anything).Return(nil)

		svc := NewStreakService(repo, publisher)
		streak, err := svc.RecordActivity(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.Equal(t, 5, streak.CurrentDays, "CurrentDays should NOT increment on same day")
	})

	t.Run("updates longest streak when current exceeds it", func(t *testing.T) {
		t.Parallel()

		existingStreak := &Streak{
			ID:             uuid.Must(uuid.NewV7()),
			GCID:           gcid,
			TenantID:       tenantID,
			CurrentDays:    0,
			LongestStreak:  0,
			Status:         StreakStatusBroken,
			LastActivityAt: time.Time{},
		}

		repo := new(mockStreakRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(existingStreak, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.Streak")).Return(nil)
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.Anything).Return(nil)

		svc := NewStreakService(repo, publisher)
		streak, err := svc.RecordActivity(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.Equal(t, 1, streak.LongestStreak)
	})

	t.Run("publishes StreakUpdated event to correct topic", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStreakRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.Streak")).Return(nil)
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).Return(nil)

		svc := NewStreakService(repo, publisher)
		_, err := svc.RecordActivity(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		publisher.AssertCalled(t, "Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent"))
	})

	t.Run("StreakUpdated event has correct type and aggregate", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStreakRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.Streak")).Return(nil)

		var capturedEvent DomainEvent
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).
			Run(func(args mock.Arguments) {
				capturedEvent = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewStreakService(repo, publisher)
		_, err := svc.RecordActivity(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.Equal(t, EventStreakUpdated, capturedEvent.EventType)
		assert.Equal(t, "Streak", capturedEvent.AggregateType)
		assert.Equal(t, tenantID, capturedEvent.TenantID)
	})

	t.Run("StreakUpdated event payload has required fields per contract", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStreakRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.Streak")).Return(nil)

		var capturedEvent DomainEvent
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).
			Run(func(args mock.Arguments) {
				capturedEvent = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewStreakService(repo, publisher)
		_, err := svc.RecordActivity(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		// AsyncAPI contract: required payload fields for engagement.streak.updated
		assert.Contains(t, capturedEvent.Payload, "gcid")
		assert.Contains(t, capturedEvent.Payload, "streak_days")
		assert.Contains(t, capturedEvent.Payload, "status")
		assert.Contains(t, capturedEvent.Payload, "previous_status")
	})

	t.Run("triggers milestone and publishes event at day 7", func(t *testing.T) {
		t.Parallel()

		yesterday := time.Now().UTC().Add(-24 * time.Hour)
		existingStreak := &Streak{
			ID:             uuid.Must(uuid.NewV7()),
			GCID:           gcid,
			TenantID:       tenantID,
			CurrentDays:    6,
			LongestStreak:  6,
			Status:         StreakStatusActive,
			LastActivityAt: yesterday,
		}

		repo := new(mockStreakRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(existingStreak, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.Streak")).Return(nil)
		repo.On("SaveMilestone", mock.Anything, gcid, tenantID, mock.AnythingOfType("engagement.StreakMilestone")).Return(nil)
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).Return(nil)

		svc := NewStreakService(repo, publisher)
		streak, err := svc.RecordActivity(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.Equal(t, 7, streak.CurrentDays)
		repo.AssertCalled(t, "SaveMilestone", mock.Anything, gcid, tenantID, mock.AnythingOfType("engagement.StreakMilestone"))
		// Two events: StreakUpdated + StreakMilestoneReached
		publisher.AssertNumberOfCalls(t, "Publish", 2)
	})

	t.Run("milestone event has correct type and payload", func(t *testing.T) {
		t.Parallel()

		yesterday := time.Now().UTC().Add(-24 * time.Hour)
		existingStreak := &Streak{
			ID:             uuid.Must(uuid.NewV7()),
			GCID:           gcid,
			TenantID:       tenantID,
			CurrentDays:    6,
			LongestStreak:  6,
			Status:         StreakStatusActive,
			LastActivityAt: yesterday,
		}

		repo := new(mockStreakRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(existingStreak, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.Streak")).Return(nil)
		repo.On("SaveMilestone", mock.Anything, gcid, tenantID, mock.AnythingOfType("engagement.StreakMilestone")).Return(nil)

		var events []DomainEvent
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).
			Run(func(args mock.Arguments) {
				events = append(events, args.Get(2).(DomainEvent))
			}).Return(nil)

		svc := NewStreakService(repo, publisher)
		_, err := svc.RecordActivity(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		require.Len(t, events, 2)

		milestoneEvt := events[1]
		assert.Equal(t, EventStreakMilestoneReached, milestoneEvt.EventType)
		assert.Equal(t, "Streak", milestoneEvt.AggregateType)
		// AsyncAPI contract: required payload fields for engagement.streak.milestone_reached
		assert.Contains(t, milestoneEvt.Payload, "gcid")
		assert.Contains(t, milestoneEvt.Payload, "streak_days")
		assert.Contains(t, milestoneEvt.Payload, "milestone")
		assert.Equal(t, 7, milestoneEvt.Payload["milestone"])
	})

	t.Run("propagates save error", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStreakRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.Streak")).Return(errors.New("save failed"))

		svc := NewStreakService(repo, publisher)
		_, err := svc.RecordActivity(context.Background(), gcid, tenantID)

		require.Error(t, err)
		assert.ErrorContains(t, err, "save failed")
	})
}
