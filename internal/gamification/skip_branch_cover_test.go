package gamification

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// CurrencyConverter.Convert default branch: a currency pair that is neither
// stars->coins nor coins->stars (e.g. an unrecognised CurrencyType on one
// side) but still passes the "different + positive" guards falls through to
// ErrValidationFailed.
func TestCurrencyConverter_UnsupportedPair(t *testing.T) {
	d := newTestCurrencyConverterService()
	ctx := context.Background()
	cfg := &EconomyConfig{ID: uuid.New(), ConfigValue: map[string]interface{}{}}
	d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "currency_exchange_rate").Return(cfg, nil)

	// from = coins, to = an unknown currency type → not coins->stars, not
	// stars->coins → default branch.
	_, err := d.svc.Convert(ctx, uuid.New(), uuid.New(), CurrencyTypeCoins, CurrencyType("gems"), 100)
	assert.ErrorIs(t, err, ErrValidationFailed)
	d.conversionR.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
}

// TerritoryRewardService.DistributeDaily skip branches:
//   - a holder with TerritoryCount <= 0 is skipped (not counted as processed)
//   - a CalculateIncome result <= 0 (zero BaseAmount) is skipped
//   - a CreateLog error skips that log without aborting the run
func TestTerritoryReward_DistributeDaily_SkipBranches(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()

	t.Run("zero-territory holder skipped; positive holder processed", func(t *testing.T) {
		d := newTestTerritoryRewardService()
		cfgs := []*TerritoryIncomeConfig{
			{ID: uuid.New(), TenantID: tenantID, IncomeType: IncomeTypeCoins, BaseAmount: 100, MultiplierPerTerritory: 0, Frequency: IncomeFrequencyDaily},
		}
		d.incomeR.On("ListConfigsByTenant", mock.Anything, tenantID, IncomeFrequencyDaily).Return(cfgs, nil)
		d.incomeR.On("CreateLog", mock.Anything, mock.AnythingOfType("*gamification.TerritoryIncomeLog")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

		holders := []TerritoryHolder{
			{GCID: uuid.New(), TerritoryCount: 0}, // skipped
			{GCID: uuid.New(), TerritoryCount: 3}, // processed
		}
		result, err := d.svc.DistributeDaily(ctx, tenantID, holders)
		require.NoError(t, err)
		assert.Equal(t, 1, result.HoldersProcessed)
		assert.Equal(t, 100, result.TotalDistributed)
	})

	t.Run("zero-income config produces no distribution", func(t *testing.T) {
		d := newTestTerritoryRewardService()
		// BaseAmount 0 → CalculateIncome returns 0 → amount<=0 continue branch.
		cfgs := []*TerritoryIncomeConfig{
			{ID: uuid.New(), TenantID: tenantID, IncomeType: IncomeTypeCoins, BaseAmount: 0, MultiplierPerTerritory: 0, Frequency: IncomeFrequencyDaily},
		}
		d.incomeR.On("ListConfigsByTenant", mock.Anything, tenantID, IncomeFrequencyDaily).Return(cfgs, nil)
		d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

		result, err := d.svc.DistributeDaily(ctx, tenantID, []TerritoryHolder{{GCID: uuid.New(), TerritoryCount: 2}})
		require.NoError(t, err)
		// Holder still counted as processed, but nothing distributed and no log written.
		assert.Equal(t, 1, result.HoldersProcessed)
		assert.Equal(t, 0, result.TotalDistributed)
		d.incomeR.AssertNotCalled(t, "CreateLog", mock.Anything, mock.Anything)
	})

	t.Run("CreateLog error skips the log but completes the run", func(t *testing.T) {
		d := newTestTerritoryRewardService()
		cfgs := []*TerritoryIncomeConfig{
			{ID: uuid.New(), TenantID: tenantID, IncomeType: IncomeTypeCoins, BaseAmount: 100, MultiplierPerTerritory: 0, Frequency: IncomeFrequencyDaily},
		}
		d.incomeR.On("ListConfigsByTenant", mock.Anything, tenantID, IncomeFrequencyDaily).Return(cfgs, nil)
		d.incomeR.On("CreateLog", mock.Anything, mock.AnythingOfType("*gamification.TerritoryIncomeLog")).Return(errBoom)
		d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

		result, err := d.svc.DistributeDaily(ctx, tenantID, []TerritoryHolder{{GCID: uuid.New(), TerritoryCount: 1}})
		require.NoError(t, err)
		// Holder counted as processed; the failed log contributes nothing to the total.
		assert.Equal(t, 1, result.HoldersProcessed)
		assert.Equal(t, 0, result.TotalDistributed)
	})
}

// EconomyDrainService.ExecuteTerritoryMaintenanceDrain: an UpdateBalance error
// skips that holder (no transaction, not counted as processed).
func TestDrain_TerritoryMaintenance_UpdateBalanceErrorSkipsHolder(t *testing.T) {
	d := newTestDrainService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	cfg := &EconomyConfig{ID: uuid.New(), ConfigValue: map[string]interface{}{"daily_cost_per_territory": float64(5)}}
	account := &CoinAccount{ID: uuid.New(), TenantID: tenantID, GCID: gcid, Balance: 1000}

	d.econRepo.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "territory_maintenance_cost").Return(cfg, nil)
	d.coinRepo.On("GetByGCID", mock.Anything, tenantID, gcid).Return(account, nil)
	d.coinRepo.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(errBoom)

	result, err := d.svc.ExecuteTerritoryMaintenanceDrain(ctx, tenantID, []TerritoryHolder{{GCID: gcid, TerritoryCount: 2}})
	require.NoError(t, err)
	assert.Equal(t, 0, result.AccountsProcessed)
	assert.Equal(t, int64(0), result.TotalDrained)
	// Transaction must not be created when the balance update fails.
	d.coinRepo.AssertNotCalled(t, "CreateTransaction", mock.Anything, mock.Anything)
}
