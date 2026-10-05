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

func TestGoalService_ListGoals(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("returns goals from repo", func(t *testing.T) {
		t.Parallel()

		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		goals := []GoalChallenge{
			{ID: uuid.Must(uuid.NewV7()), Title: "Complete 10 atoms", Status: GoalStatusActive},
			{ID: uuid.Must(uuid.NewV7()), Title: "80% retention", Status: GoalStatusPending},
		}
		status := GoalStatusActive
		repo.On("ListByAssignee", mock.Anything, gcid, tenantID, &status, (*uuid.UUID)(nil), 10).Return(goals, nil)

		svc := NewGoalService(repo, publisher)
		got, err := svc.ListGoals(context.Background(), gcid, tenantID, &status, nil, 10)

		require.NoError(t, err)
		assert.Len(t, got, 2)
	})

	t.Run("propagates repo error", func(t *testing.T) {
		t.Parallel()

		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		repo.On("ListByAssignee", mock.Anything, gcid, tenantID, (*GoalStatus)(nil), (*uuid.UUID)(nil), 10).Return(nil, errors.New("db error"))

		svc := NewGoalService(repo, publisher)
		_, err := svc.ListGoals(context.Background(), gcid, tenantID, nil, nil, 10)

		require.Error(t, err)
	})
}

func TestGoalService_GetGoal(t *testing.T) {
	t.Parallel()

	t.Run("returns goal by ID", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		goal := &GoalChallenge{ID: goalID, Title: "Complete 10 atoms", Status: GoalStatusActive}
		repo.On("GetByID", mock.Anything, goalID).Return(goal, nil)

		svc := NewGoalService(repo, publisher)
		got, err := svc.GetGoal(context.Background(), goalID)

		require.NoError(t, err)
		assert.Equal(t, goalID, got.ID)
	})

	t.Run("returns ErrGoalNotFound when nil", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByID", mock.Anything, goalID).Return(nil, nil)

		svc := NewGoalService(repo, publisher)
		_, err := svc.GetGoal(context.Background(), goalID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrGoalNotFound)
	})
}

func TestGoalService_CreateGoal(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	createdBy := uuid.Must(uuid.NewV7())
	deadline := time.Now().Add(7 * 24 * time.Hour)
	targetScope := GoalTargetScope{
		Type:        GoalTargetAtomCount,
		TargetValue: 10,
	}

	t.Run("creates goal with pending status", func(t *testing.T) {
		t.Parallel()

		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		repo.On("CountActiveByAssignee", mock.Anything, gcid, tenantID).Return(0, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.GoalChallenge")).Return(nil)

		svc := NewGoalService(repo, publisher)
		got, err := svc.CreateGoal(context.Background(), tenantID, gcid, createdBy, "Complete 10 atoms", "Learn more", targetScope, deadline, 50)

		require.NoError(t, err)
		assert.Equal(t, GoalStatusPending, got.Status)
		assert.Equal(t, "Complete 10 atoms", got.Title)
		assert.Equal(t, 50, got.BountyStarCredits)
	})

	t.Run("returns ErrGoalSlotsFull when 3 active", func(t *testing.T) {
		t.Parallel()

		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		repo.On("CountActiveByAssignee", mock.Anything, gcid, tenantID).Return(MaxConcurrentGoals, nil)

		svc := NewGoalService(repo, publisher)
		_, err := svc.CreateGoal(context.Background(), tenantID, gcid, createdBy, "Another goal", "", targetScope, deadline, 10)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrGoalSlotsFull)
	})

	t.Run("returns ErrValidationFailed when title is empty", func(t *testing.T) {
		t.Parallel()

		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		svc := NewGoalService(repo, publisher)
		_, err := svc.CreateGoal(context.Background(), tenantID, gcid, createdBy, "", "No title", targetScope, deadline, 10)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed, "empty title should fail validation")
	})

	t.Run("returns ErrValidationFailed when deadline is in the past", func(t *testing.T) {
		t.Parallel()

		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		pastDeadline := time.Now().Add(-24 * time.Hour)
		svc := NewGoalService(repo, publisher)
		_, err := svc.CreateGoal(context.Background(), tenantID, gcid, createdBy, "Valid Title", "", targetScope, pastDeadline, 10)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed, "past deadline should fail validation")
	})
}

func TestGoalService_AcceptGoal(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("transitions pending to active", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		goal := &GoalChallenge{
			ID:           goalID,
			TenantID:     tenantID,
			AssigneeGCID: gcid,
			Title:        "Complete 10 atoms",
			Status:       GoalStatusPending,
			Deadline:     time.Now().Add(7 * 24 * time.Hour),
		}
		repo.On("GetByID", mock.Anything, goalID).Return(goal, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.GoalChallenge")).Return(nil)
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).Return(nil)

		svc := NewGoalService(repo, publisher)
		got, err := svc.AcceptGoal(context.Background(), goalID)

		require.NoError(t, err)
		assert.Equal(t, GoalStatusActive, got.Status)
	})

	t.Run("returns ErrGoalNotFound when nil", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByID", mock.Anything, goalID).Return(nil, nil)

		svc := NewGoalService(repo, publisher)
		_, err := svc.AcceptGoal(context.Background(), goalID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrGoalNotFound)
	})

	t.Run("returns ErrGoalExpired when expired", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		goal := &GoalChallenge{ID: goalID, Status: GoalStatusExpired}
		repo.On("GetByID", mock.Anything, goalID).Return(goal, nil)

		svc := NewGoalService(repo, publisher)
		_, err := svc.AcceptGoal(context.Background(), goalID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrGoalExpired)
	})

	t.Run("returns ErrGoalAlreadyAccepted when active", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		goal := &GoalChallenge{ID: goalID, Status: GoalStatusActive}
		repo.On("GetByID", mock.Anything, goalID).Return(goal, nil)

		svc := NewGoalService(repo, publisher)
		_, err := svc.AcceptGoal(context.Background(), goalID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrGoalAlreadyAccepted)
	})

	t.Run("publishes GoalAccepted event with correct payload", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		deadline := time.Now().Add(7 * 24 * time.Hour)
		goal := &GoalChallenge{
			ID:                goalID,
			TenantID:          tenantID,
			AssigneeGCID:      gcid,
			Title:             "Complete 10 atoms",
			Status:            GoalStatusPending,
			Deadline:          deadline,
			BountyStarCredits: 50,
		}
		repo.On("GetByID", mock.Anything, goalID).Return(goal, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.GoalChallenge")).Return(nil)

		var capturedEvent DomainEvent
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).
			Run(func(args mock.Arguments) {
				capturedEvent = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewGoalService(repo, publisher)
		_, err := svc.AcceptGoal(context.Background(), goalID)

		require.NoError(t, err)
		assert.Equal(t, EventGoalAccepted, capturedEvent.EventType)
		assert.Equal(t, "GoalChallenge", capturedEvent.AggregateType)
		// AsyncAPI contract: required payload fields for engagement.goal.accepted
		assert.Contains(t, capturedEvent.Payload, "gcid")
		assert.Contains(t, capturedEvent.Payload, "goal_id")
		assert.Contains(t, capturedEvent.Payload, "title")
		assert.Contains(t, capturedEvent.Payload, "deadline")
		assert.Contains(t, capturedEvent.Payload, "bounty_star_credits")
		assert.Equal(t, 50, capturedEvent.Payload["bounty_star_credits"])
	})
}

func TestGoalService_DeclineGoal(t *testing.T) {
	t.Parallel()

	t.Run("transitions pending to declined", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		goal := &GoalChallenge{ID: goalID, Status: GoalStatusPending}
		repo.On("GetByID", mock.Anything, goalID).Return(goal, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.GoalChallenge")).Return(nil)

		svc := NewGoalService(repo, publisher)
		got, err := svc.DeclineGoal(context.Background(), goalID)

		require.NoError(t, err)
		assert.Equal(t, GoalStatusDeclined, got.Status)
	})

	t.Run("returns ErrGoalNotFound when nil", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByID", mock.Anything, goalID).Return(nil, nil)

		svc := NewGoalService(repo, publisher)
		_, err := svc.DeclineGoal(context.Background(), goalID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrGoalNotFound)
	})

	t.Run("returns ErrGoalExpired when expired", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		goal := &GoalChallenge{ID: goalID, Status: GoalStatusExpired}
		repo.On("GetByID", mock.Anything, goalID).Return(goal, nil)

		svc := NewGoalService(repo, publisher)
		_, err := svc.DeclineGoal(context.Background(), goalID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrGoalExpired)
	})

	t.Run("returns ErrGoalAlreadyDeclined when declined", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		goal := &GoalChallenge{ID: goalID, Status: GoalStatusDeclined}
		repo.On("GetByID", mock.Anything, goalID).Return(goal, nil)

		svc := NewGoalService(repo, publisher)
		_, err := svc.DeclineGoal(context.Background(), goalID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrGoalAlreadyDeclined)
	})

	t.Run("returns ErrGoalAlreadyAccepted when active", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		goal := &GoalChallenge{ID: goalID, Status: GoalStatusActive}
		repo.On("GetByID", mock.Anything, goalID).Return(goal, nil)

		svc := NewGoalService(repo, publisher)
		_, err := svc.DeclineGoal(context.Background(), goalID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrGoalAlreadyAccepted)
	})
}

func TestGoalService_CompleteGoal(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("transitions active to completed with 100 percent progress", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		goal := &GoalChallenge{
			ID:                goalID,
			TenantID:          tenantID,
			AssigneeGCID:      gcid,
			Title:             "Complete 10 atoms",
			Status:            GoalStatusActive,
			BountyStarCredits: 50,
		}
		repo.On("GetByID", mock.Anything, goalID).Return(goal, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.GoalChallenge")).Return(nil)
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).Return(nil)

		svc := NewGoalService(repo, publisher)
		got, err := svc.CompleteGoal(context.Background(), goalID)

		require.NoError(t, err)
		assert.Equal(t, GoalStatusCompleted, got.Status)
		assert.Equal(t, float64(100), got.ProgressPct)
	})

	t.Run("returns ErrGoalNotFound when nil", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByID", mock.Anything, goalID).Return(nil, nil)

		svc := NewGoalService(repo, publisher)
		_, err := svc.CompleteGoal(context.Background(), goalID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrGoalNotFound)
	})

	t.Run("publishes GoalCompleted event with correct payload", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		goal := &GoalChallenge{
			ID:                goalID,
			TenantID:          tenantID,
			AssigneeGCID:      gcid,
			Title:             "Complete 10 atoms",
			Status:            GoalStatusActive,
			BountyStarCredits: 50,
		}
		repo.On("GetByID", mock.Anything, goalID).Return(goal, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.GoalChallenge")).Return(nil)

		var capturedEvent DomainEvent
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).
			Run(func(args mock.Arguments) {
				capturedEvent = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewGoalService(repo, publisher)
		_, err := svc.CompleteGoal(context.Background(), goalID)

		require.NoError(t, err)
		assert.Equal(t, EventGoalCompleted, capturedEvent.EventType)
		assert.Equal(t, "GoalChallenge", capturedEvent.AggregateType)
		// AsyncAPI contract: required payload fields for engagement.goal.completed
		assert.Contains(t, capturedEvent.Payload, "gcid")
		assert.Contains(t, capturedEvent.Payload, "goal_id")
		assert.Contains(t, capturedEvent.Payload, "title")
		assert.Contains(t, capturedEvent.Payload, "star_credits_awarded")
		assert.Equal(t, 50, capturedEvent.Payload["star_credits_awarded"])
	})

	t.Run("returns error when goal is pending", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		goal := &GoalChallenge{ID: goalID, Status: GoalStatusPending}
		repo.On("GetByID", mock.Anything, goalID).Return(goal, nil)

		svc := NewGoalService(repo, publisher)
		_, err := svc.CompleteGoal(context.Background(), goalID)

		require.Error(t, err, "pending goals cannot be completed — must be active first")
		assert.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("returns error when goal is expired", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		goal := &GoalChallenge{ID: goalID, Status: GoalStatusExpired}
		repo.On("GetByID", mock.Anything, goalID).Return(goal, nil)

		svc := NewGoalService(repo, publisher)
		_, err := svc.CompleteGoal(context.Background(), goalID)

		require.Error(t, err, "expired goals cannot be completed")
		assert.ErrorIs(t, err, ErrGoalExpired)
	})

	t.Run("returns error when goal is declined", func(t *testing.T) {
		t.Parallel()

		goalID := uuid.Must(uuid.NewV7())
		repo := new(mockGoalChallengeRepo)
		publisher := new(mockEventPublisher)

		goal := &GoalChallenge{ID: goalID, Status: GoalStatusDeclined}
		repo.On("GetByID", mock.Anything, goalID).Return(goal, nil)

		svc := NewGoalService(repo, publisher)
		_, err := svc.CompleteGoal(context.Background(), goalID)

		require.Error(t, err, "declined goals cannot be completed")
		assert.ErrorIs(t, err, ErrValidationFailed)
	})
}
