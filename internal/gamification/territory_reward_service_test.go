package gamification

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

type testTerritoryRewardDeps struct {
	svc       *TerritoryRewardService
	incomeR   *mockTerritoryIncomeRepo
	seasonalR *mockSeasonalRewardRepo
	econR     *mockEconomyConfigRepo
	publisher *mockEventPublisher
}

func newTestTerritoryRewardService() testTerritoryRewardDeps {
	ir := &mockTerritoryIncomeRepo{}
	sr := &mockSeasonalRewardRepo{}
	er := &mockEconomyConfigRepo{}
	ep := &mockEventPublisher{}
	return testTerritoryRewardDeps{
		svc:       NewTerritoryRewardService(ir, sr, er, ep),
		incomeR:   ir,
		seasonalR: sr,
		econR:     er,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// Tests — CalculateIncome
// ---------------------------------------------------------------------------

func TestTerritoryReward_CalculateIncome_SingleTerritory(t *testing.T) {
	t.Parallel()
	d := newTestTerritoryRewardService()

	config := &TerritoryIncomeConfig{
		ID:                     uuid.New(),
		TenantID:               uuid.New(),
		IncomeType:             IncomeTypeCoins,
		BaseAmount:             100,
		MultiplierPerTerritory: 1.0,
		Frequency:              IncomeFrequencyDaily,
		CreatedAt:              time.Now().UTC(),
		UpdatedAt:              time.Now().UTC(),
	}

	amount := d.svc.CalculateIncome(config, 1)
	assert.Equal(t, 100, amount)
}

func TestTerritoryReward_CalculateIncome_FiveTerritoriesWithMultiplier(t *testing.T) {
	t.Parallel()
	d := newTestTerritoryRewardService()

	config := &TerritoryIncomeConfig{
		ID:                     uuid.New(),
		TenantID:               uuid.New(),
		IncomeType:             IncomeTypeXP,
		BaseAmount:             50,
		MultiplierPerTerritory: 1.5,
		Frequency:              IncomeFrequencyDaily,
		CreatedAt:              time.Now().UTC(),
		UpdatedAt:              time.Now().UTC(),
	}

	// 50 * (1 + (5-1)*1.5) = 50 * (1+6) = 50 * 7 = 350
	amount := d.svc.CalculateIncome(config, 5)
	assert.Equal(t, 350, amount)
}

func TestTerritoryReward_CalculateIncome_ZeroTerritories(t *testing.T) {
	t.Parallel()
	d := newTestTerritoryRewardService()

	config := &TerritoryIncomeConfig{
		ID:                     uuid.New(),
		TenantID:               uuid.New(),
		IncomeType:             IncomeTypeCoins,
		BaseAmount:             100,
		MultiplierPerTerritory: 1.0,
		Frequency:              IncomeFrequencyDaily,
		CreatedAt:              time.Now().UTC(),
		UpdatedAt:              time.Now().UTC(),
	}

	amount := d.svc.CalculateIncome(config, 0)
	assert.Equal(t, 0, amount)
}

// ---------------------------------------------------------------------------
// Tests — DistributeDaily
// ---------------------------------------------------------------------------

func TestTerritoryReward_DistributeDaily_Success(t *testing.T) {
	t.Parallel()
	d := newTestTerritoryRewardService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	config := &TerritoryIncomeConfig{
		ID:                     uuid.New(),
		TenantID:               tenantID,
		IncomeType:             IncomeTypeCoins,
		BaseAmount:             100,
		MultiplierPerTerritory: 1.0,
		Frequency:              IncomeFrequencyDaily,
		CreatedAt:              time.Now().UTC(),
		UpdatedAt:              time.Now().UTC(),
	}

	holders := []TerritoryHolder{
		{TenantID: tenantID, GCID: gcid, TerritoryCount: 3},
	}

	d.incomeR.On("ListConfigsByTenant", mock.Anything, tenantID, IncomeFrequencyDaily).
		Return([]*TerritoryIncomeConfig{config}, nil)
	d.incomeR.On("CreateLog", mock.Anything, mock.AnythingOfType("*gamification.TerritoryIncomeLog")).
		Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).
		Return(nil)

	result, err := d.svc.DistributeDaily(ctx, tenantID, holders)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 1, result.HoldersProcessed)
	assert.Greater(t, result.TotalDistributed, 0)
	d.incomeR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestTerritoryReward_DistributeDaily_NoConfigs(t *testing.T) {
	t.Parallel()
	d := newTestTerritoryRewardService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	holders := []TerritoryHolder{
		{TenantID: tenantID, GCID: gcid, TerritoryCount: 1},
	}

	d.incomeR.On("ListConfigsByTenant", mock.Anything, tenantID, IncomeFrequencyDaily).
		Return([]*TerritoryIncomeConfig{}, nil)

	result, err := d.svc.DistributeDaily(ctx, tenantID, holders)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 0, result.HoldersProcessed)
	assert.Equal(t, 0, result.TotalDistributed)
}

// ---------------------------------------------------------------------------
// Tests — GetSeasonalRewards
// ---------------------------------------------------------------------------

func TestTerritoryReward_GetSeasonalRewards_ByTier(t *testing.T) {
	t.Parallel()
	d := newTestTerritoryRewardService()
	ctx := context.Background()
	tenantID := uuid.New()
	seasonID := "2026-S1"

	rewards := []*SeasonalLeaderboardReward{
		{
			ID:           uuid.New(),
			TenantID:     tenantID,
			SeasonID:     seasonID,
			RankTier:     RankTierTop1,
			RewardType:   "coins",
			RewardAmount: 10000,
			CreatedAt:    time.Now().UTC(),
		},
	}

	d.seasonalR.On("ListBySeason", mock.Anything, tenantID, seasonID).
		Return(rewards, nil)

	got, err := d.svc.GetSeasonalRewards(ctx, tenantID, seasonID)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, RankTierTop1, got[0].RankTier)
	assert.Equal(t, 10000, got[0].RewardAmount)
}

// ---------------------------------------------------------------------------
// Tests — ConfigureIncome
// ---------------------------------------------------------------------------

func TestTerritoryReward_ConfigureIncome_Success(t *testing.T) {
	t.Parallel()
	d := newTestTerritoryRewardService()
	ctx := context.Background()
	tenantID := uuid.New()

	d.incomeR.On("CreateConfig", mock.Anything, mock.AnythingOfType("*gamification.TerritoryIncomeConfig")).
		Return(nil)

	config, err := d.svc.ConfigureIncome(ctx, tenantID, IncomeTypeCoins, 100, 1.5, IncomeFrequencyDaily)
	assert.NoError(t, err)
	assert.NotNil(t, config)
	assert.Equal(t, tenantID, config.TenantID)
	assert.Equal(t, IncomeTypeCoins, config.IncomeType)
	assert.Equal(t, 100, config.BaseAmount)
	assert.Equal(t, 1.5, config.MultiplierPerTerritory)
	assert.Equal(t, IncomeFrequencyDaily, config.Frequency)
	d.incomeR.AssertExpectations(t)
}

func TestTerritoryReward_ConfigureIncome_InvalidIncomeType(t *testing.T) {
	t.Parallel()
	d := newTestTerritoryRewardService()
	ctx := context.Background()
	tenantID := uuid.New()

	_, err := d.svc.ConfigureIncome(ctx, tenantID, IncomeType("invalid"), 100, 1.0, IncomeFrequencyDaily)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

func TestTerritoryReward_ConfigureIncome_InvalidFrequency(t *testing.T) {
	t.Parallel()
	d := newTestTerritoryRewardService()
	ctx := context.Background()
	tenantID := uuid.New()

	_, err := d.svc.ConfigureIncome(ctx, tenantID, IncomeTypeCoins, 100, 1.0, IncomeFrequency("invalid"))
	assert.ErrorIs(t, err, ErrValidationFailed)
}

// ---------------------------------------------------------------------------
// Tests — Enum IsValid
// ---------------------------------------------------------------------------

func TestIncomeType_IsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, IncomeTypeXP.IsValid())
	assert.True(t, IncomeTypeCoins.IsValid())
	assert.True(t, IncomeTypeStars.IsValid())
	assert.False(t, IncomeType("invalid").IsValid())
}

func TestIncomeFrequency_IsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, IncomeFrequencyDaily.IsValid())
	assert.True(t, IncomeFrequencyWeekly.IsValid())
	assert.False(t, IncomeFrequency("invalid").IsValid())
}

func TestRankTier_IsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, RankTierTop1.IsValid())
	assert.True(t, RankTierTop3.IsValid())
	assert.True(t, RankTierTop10.IsValid())
	assert.True(t, RankTierTop25.IsValid())
	assert.True(t, RankTierTop50.IsValid())
	assert.False(t, RankTier("invalid").IsValid())
}
