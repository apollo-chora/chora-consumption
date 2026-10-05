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

// ---------------------------------------------------------------------------
// StarAccountService Tests (CHO-359)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// StarTier.IsValid
// ---------------------------------------------------------------------------

func TestStarTier_IsValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tier StarTier
		want bool
	}{
		{StarTierBronze, true},
		{StarTierSilver, true},
		{StarTierGold, true},
		{StarTierPlatinum, true},
		{StarTierDiamond, true},
		{StarTier("invalid"), false},
		{StarTier(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.tier), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.tier.IsValid())
		})
	}
}

// ---------------------------------------------------------------------------
// StarTierForLevel
// ---------------------------------------------------------------------------

func TestStarTierForLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		level int
		want  StarTier
	}{
		{"level 1 is bronze", 1, StarTierBronze},
		{"level 4 is bronze", 4, StarTierBronze},
		{"level 5 is silver", 5, StarTierSilver},
		{"level 9 is silver", 9, StarTierSilver},
		{"level 10 is gold", 10, StarTierGold},
		{"level 14 is gold", 14, StarTierGold},
		{"level 15 is platinum", 15, StarTierPlatinum},
		{"level 19 is platinum", 19, StarTierPlatinum},
		{"level 20 is diamond", 20, StarTierDiamond},
		{"level 50 is diamond", 50, StarTierDiamond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, StarTierForLevel(tt.level))
		})
	}
}

// ---------------------------------------------------------------------------
// StarAccountService.GetStarAccount
// ---------------------------------------------------------------------------

func TestStarAccountService_GetStarAccount(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("returns existing star account", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStarAccountRepo)
		publisher := new(mockEventPublisher)

		now := time.Now().UTC()
		account := &StarAccount{
			ID:              uuid.Must(uuid.NewV7()),
			TenantID:        tenantID,
			GCID:            gcid,
			TotalXP:         500,
			CurrentLevel:    3,
			CurrentStarTier: StarTierBronze,
			LevelAchievedAt: &now,
			CreatedAt:       now,
			UpdatedAt:       now,
		}

		repo.On("GetByGCID", mock.Anything, gcid, tenantID).Return(account, nil)

		svc := NewStarAccountService(repo, publisher)
		got, err := svc.GetStarAccount(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.Equal(t, int64(500), got.TotalXP)
		assert.Equal(t, 3, got.CurrentLevel)
		assert.Equal(t, StarTierBronze, got.CurrentStarTier)
	})

	t.Run("creates default account when not found", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStarAccountRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCID", mock.Anything, gcid, tenantID).Return(nil, nil)
		repo.On("Create", mock.Anything, mock.AnythingOfType("*engagement.StarAccount")).Return(nil)

		svc := NewStarAccountService(repo, publisher)
		got, err := svc.GetStarAccount(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.Equal(t, int64(0), got.TotalXP)
		assert.Equal(t, 1, got.CurrentLevel)
		assert.Equal(t, StarTierBronze, got.CurrentStarTier)
		assert.Equal(t, gcid, got.GCID)
		assert.Equal(t, tenantID, got.TenantID)
	})

	t.Run("propagates repository error", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStarAccountRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCID", mock.Anything, gcid, tenantID).Return(nil, errors.New("db error"))

		svc := NewStarAccountService(repo, publisher)
		_, err := svc.GetStarAccount(context.Background(), gcid, tenantID)

		require.Error(t, err)
		assert.ErrorContains(t, err, "db error")
	})
}

// ---------------------------------------------------------------------------
// StarAccountService.AddXP
// ---------------------------------------------------------------------------

func TestStarAccountService_AddXP(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("adds XP to existing account without level change", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStarAccountRepo)
		publisher := new(mockEventPublisher)

		account := &StarAccount{
			ID:              uuid.Must(uuid.NewV7()),
			TenantID:        tenantID,
			GCID:            gcid,
			TotalXP:         50,
			CurrentLevel:    1,
			CurrentStarTier: StarTierBronze,
		}

		repo.On("GetByGCID", mock.Anything, gcid, tenantID).Return(account, nil)
		repo.On("Update", mock.Anything, mock.AnythingOfType("*engagement.StarAccount")).Return(nil)

		svc := NewStarAccountService(repo, publisher)
		got, err := svc.AddXP(context.Background(), gcid, tenantID, 20)

		require.NoError(t, err)
		assert.Equal(t, int64(70), got.TotalXP)
		assert.Equal(t, 1, got.CurrentLevel) // still level 1 (need 100)
		// No event should be published for same-level XP addition
		publisher.AssertNotCalled(t, "Publish")
	})

	t.Run("level up triggers event publication", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStarAccountRepo)
		publisher := new(mockEventPublisher)

		account := &StarAccount{
			ID:              uuid.Must(uuid.NewV7()),
			TenantID:        tenantID,
			GCID:            gcid,
			TotalXP:         90,
			CurrentLevel:    1,
			CurrentStarTier: StarTierBronze,
		}

		repo.On("GetByGCID", mock.Anything, gcid, tenantID).Return(account, nil)
		repo.On("Update", mock.Anything, mock.AnythingOfType("*engagement.StarAccount")).Return(nil)

		var capturedEvent DomainEvent
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).
			Run(func(args mock.Arguments) {
				capturedEvent = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewStarAccountService(repo, publisher)
		got, err := svc.AddXP(context.Background(), gcid, tenantID, 20)

		require.NoError(t, err)
		assert.Equal(t, int64(110), got.TotalXP)
		assert.Equal(t, 2, got.CurrentLevel)

		// Verify level-up event
		assert.Equal(t, EventStarAccountLevelUp, capturedEvent.EventType)
		assert.Equal(t, AggregateStarAccount, capturedEvent.AggregateType)
		assert.Contains(t, capturedEvent.Payload, "gcid")
		assert.Contains(t, capturedEvent.Payload, "new_level")
		assert.Contains(t, capturedEvent.Payload, "previous_level")
		assert.Contains(t, capturedEvent.Payload, "total_xp")
		assert.Contains(t, capturedEvent.Payload, "new_star_tier")
		assert.Contains(t, capturedEvent.Payload, "previous_star_tier")
		assert.Equal(t, 2, capturedEvent.Payload["new_level"])
		assert.Equal(t, 1, capturedEvent.Payload["previous_level"])
	})

	t.Run("star tier upgrade on level crossing", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStarAccountRepo)
		publisher := new(mockEventPublisher)

		// At level 4 with enough XP to jump to level 5 = silver tier
		account := &StarAccount{
			ID:              uuid.Must(uuid.NewV7()),
			TenantID:        tenantID,
			GCID:            gcid,
			TotalXP:         990,
			CurrentLevel:    4,
			CurrentStarTier: StarTierBronze,
		}

		repo.On("GetByGCID", mock.Anything, gcid, tenantID).Return(account, nil)
		repo.On("Update", mock.Anything, mock.AnythingOfType("*engagement.StarAccount")).Return(nil)

		var capturedEvent DomainEvent
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).
			Run(func(args mock.Arguments) {
				capturedEvent = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewStarAccountService(repo, publisher)
		got, err := svc.AddXP(context.Background(), gcid, tenantID, 100)

		require.NoError(t, err)
		assert.Equal(t, StarTierSilver, got.CurrentStarTier)
		assert.Equal(t, "silver_star", capturedEvent.Payload["new_star_tier"])
		assert.Equal(t, "bronze_star", capturedEvent.Payload["previous_star_tier"])
	})

	t.Run("creates account if not found then adds XP", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStarAccountRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCID", mock.Anything, gcid, tenantID).Return(nil, nil)
		repo.On("Create", mock.Anything, mock.AnythingOfType("*engagement.StarAccount")).Return(nil)
		repo.On("Update", mock.Anything, mock.AnythingOfType("*engagement.StarAccount")).Return(nil)

		svc := NewStarAccountService(repo, publisher)
		got, err := svc.AddXP(context.Background(), gcid, tenantID, 50)

		require.NoError(t, err)
		assert.Equal(t, int64(50), got.TotalXP)
		assert.Equal(t, 1, got.CurrentLevel)
	})

	t.Run("propagates update error", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStarAccountRepo)
		publisher := new(mockEventPublisher)

		account := &StarAccount{
			ID:              uuid.Must(uuid.NewV7()),
			TenantID:        tenantID,
			GCID:            gcid,
			TotalXP:         50,
			CurrentLevel:    1,
			CurrentStarTier: StarTierBronze,
		}

		repo.On("GetByGCID", mock.Anything, gcid, tenantID).Return(account, nil)
		repo.On("Update", mock.Anything, mock.AnythingOfType("*engagement.StarAccount")).Return(errors.New("update failed"))

		svc := NewStarAccountService(repo, publisher)
		_, err := svc.AddXP(context.Background(), gcid, tenantID, 10)

		require.Error(t, err)
		assert.ErrorContains(t, err, "update failed")
	})

	t.Run("propagates event publish error", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStarAccountRepo)
		publisher := new(mockEventPublisher)

		account := &StarAccount{
			ID:              uuid.Must(uuid.NewV7()),
			TenantID:        tenantID,
			GCID:            gcid,
			TotalXP:         90,
			CurrentLevel:    1,
			CurrentStarTier: StarTierBronze,
		}

		repo.On("GetByGCID", mock.Anything, gcid, tenantID).Return(account, nil)
		repo.On("Update", mock.Anything, mock.AnythingOfType("*engagement.StarAccount")).Return(nil)
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).Return(errors.New("publish failed"))

		svc := NewStarAccountService(repo, publisher)
		_, err := svc.AddXP(context.Background(), gcid, tenantID, 20)

		require.Error(t, err)
		assert.ErrorContains(t, err, "publish failed")
	})
}

// ---------------------------------------------------------------------------
// StarAccountService.GetLevelProgress
// ---------------------------------------------------------------------------

func TestStarAccountService_GetLevelProgress(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("returns progress for level 1 learner", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStarAccountRepo)
		publisher := new(mockEventPublisher)

		account := &StarAccount{
			ID:              uuid.Must(uuid.NewV7()),
			TenantID:        tenantID,
			GCID:            gcid,
			TotalXP:         50,
			CurrentLevel:    1,
			CurrentStarTier: StarTierBronze,
		}

		repo.On("GetByGCID", mock.Anything, gcid, tenantID).Return(account, nil)

		svc := NewStarAccountService(repo, publisher)
		progress, err := svc.GetLevelProgress(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.Equal(t, 1, progress.CurrentLevel)
		assert.Equal(t, int64(50), progress.CurrentXP)
		assert.Equal(t, int64(0), progress.XPForCurrentLevel)
		assert.Equal(t, int64(100), progress.XPForNextLevel)
		assert.Equal(t, int64(50), progress.XPRemaining)
		assert.InDelta(t, 0.5, progress.ProgressPct, 0.01)
		assert.Equal(t, StarTierBronze, progress.CurrentStarTier)
	})

	t.Run("returns progress for higher level learner", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStarAccountRepo)
		publisher := new(mockEventPublisher)

		account := &StarAccount{
			ID:              uuid.Must(uuid.NewV7()),
			TenantID:        tenantID,
			GCID:            gcid,
			TotalXP:         250,
			CurrentLevel:    2,
			CurrentStarTier: StarTierBronze,
		}

		repo.On("GetByGCID", mock.Anything, gcid, tenantID).Return(account, nil)

		svc := NewStarAccountService(repo, publisher)
		progress, err := svc.GetLevelProgress(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.Equal(t, 2, progress.CurrentLevel)
		assert.Equal(t, int64(250), progress.CurrentXP)
		assert.Equal(t, int64(100), progress.XPForCurrentLevel) // Level 2 starts at 100 XP
		assert.Equal(t, int64(300), progress.XPForNextLevel)    // Level 3 starts at 300 XP
		assert.Equal(t, int64(50), progress.XPRemaining)
		assert.InDelta(t, 0.75, progress.ProgressPct, 0.01)
	})

	t.Run("returns next star tier info", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStarAccountRepo)
		publisher := new(mockEventPublisher)

		account := &StarAccount{
			ID:              uuid.Must(uuid.NewV7()),
			TenantID:        tenantID,
			GCID:            gcid,
			TotalXP:         500,
			CurrentLevel:    3,
			CurrentStarTier: StarTierBronze,
		}

		repo.On("GetByGCID", mock.Anything, gcid, tenantID).Return(account, nil)

		svc := NewStarAccountService(repo, publisher)
		progress, err := svc.GetLevelProgress(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.Equal(t, StarTierBronze, progress.CurrentStarTier)
		require.NotNil(t, progress.NextStarTier)
		assert.Equal(t, StarTierSilver, *progress.NextStarTier)
		require.NotNil(t, progress.NextStarTierLevel)
		assert.Equal(t, 5, *progress.NextStarTierLevel)
	})

	t.Run("diamond tier has no next tier", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStarAccountRepo)
		publisher := new(mockEventPublisher)

		account := &StarAccount{
			ID:              uuid.Must(uuid.NewV7()),
			TenantID:        tenantID,
			GCID:            gcid,
			TotalXP:         100000,
			CurrentLevel:    25,
			CurrentStarTier: StarTierDiamond,
		}

		repo.On("GetByGCID", mock.Anything, gcid, tenantID).Return(account, nil)

		svc := NewStarAccountService(repo, publisher)
		progress, err := svc.GetLevelProgress(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.Equal(t, StarTierDiamond, progress.CurrentStarTier)
		assert.Nil(t, progress.NextStarTier)
		assert.Nil(t, progress.NextStarTierLevel)
	})

	t.Run("creates default account when not found", func(t *testing.T) {
		t.Parallel()

		repo := new(mockStarAccountRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCID", mock.Anything, gcid, tenantID).Return(nil, nil)
		repo.On("Create", mock.Anything, mock.AnythingOfType("*engagement.StarAccount")).Return(nil)

		svc := NewStarAccountService(repo, publisher)
		progress, err := svc.GetLevelProgress(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.Equal(t, 1, progress.CurrentLevel)
		assert.Equal(t, int64(0), progress.CurrentXP)
		assert.Equal(t, int64(100), progress.XPRemaining)
	})
}

// ---------------------------------------------------------------------------
// Level threshold constants
// ---------------------------------------------------------------------------

func TestLevelThresholdConstants(t *testing.T) {
	t.Parallel()

	// Level thresholds use the existing LevelForXP function.
	// Verify key thresholds mentioned in the spec.
	assert.Equal(t, 1, LevelForXP(0))
	assert.Equal(t, 2, LevelForXP(100))
	assert.Equal(t, 5, LevelForXP(1000))
}

// ---------------------------------------------------------------------------
// mockStarAccountRepo — mock for StarAccountRepository
// ---------------------------------------------------------------------------

type mockStarAccountRepo struct{ mock.Mock }

func (m *mockStarAccountRepo) GetByGCID(ctx context.Context, gcid, tenantID uuid.UUID) (*StarAccount, error) {
	args := m.Called(ctx, gcid, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*StarAccount), args.Error(1)
}

func (m *mockStarAccountRepo) Create(ctx context.Context, account *StarAccount) error {
	return m.Called(ctx, account).Error(0)
}

func (m *mockStarAccountRepo) Update(ctx context.Context, account *StarAccount) error {
	return m.Called(ctx, account).Error(0)
}

// Compile-time interface check.
var _ StarAccountRepository = (*mockStarAccountRepo)(nil)
