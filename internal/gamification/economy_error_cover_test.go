package gamification

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ===========================================================================
// CurrencyConverterService.Convert — default-rate fallbacks + repo error
// ===========================================================================

func TestCurrencyConverter_DefaultRateFallback(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	t.Run("stars->coins missing config value uses default 10", func(t *testing.T) {
		d := newTestCurrencyConverterService()
		// rateConfig present but WITHOUT the stars_to_coins key → default 10 applies.
		cfg := &EconomyConfig{ID: uuid.New(), ConfigValue: map[string]interface{}{}}
		d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "currency_exchange_rate").Return(cfg, nil)
		d.conversionR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.CurrencyConversion")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

		conv, err := d.svc.Convert(ctx, tenantID, gcid, CurrencyTypeStars, CurrencyTypeCoins, 3)
		require.NoError(t, err)
		assert.Equal(t, float64(10), conv.ExchangeRate)
		assert.Equal(t, 30, conv.ToAmount)
	})

	t.Run("coins->stars missing config value uses default 0.1", func(t *testing.T) {
		d := newTestCurrencyConverterService()
		cfg := &EconomyConfig{ID: uuid.New(), ConfigValue: map[string]interface{}{}}
		d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "currency_exchange_rate").Return(cfg, nil)
		d.conversionR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.CurrencyConversion")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

		conv, err := d.svc.Convert(ctx, tenantID, gcid, CurrencyTypeCoins, CurrencyTypeStars, 100)
		require.NoError(t, err)
		assert.Equal(t, 0.1, conv.ExchangeRate)
		assert.Equal(t, 10, conv.ToAmount)
	})

	t.Run("config lookup error propagates", func(t *testing.T) {
		d := newTestCurrencyConverterService()
		d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "currency_exchange_rate").Return(nil, errBoom)

		_, err := d.svc.Convert(ctx, tenantID, gcid, CurrencyTypeStars, CurrencyTypeCoins, 5)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("conversion Create error propagates", func(t *testing.T) {
		d := newTestCurrencyConverterService()
		cfg := &EconomyConfig{ID: uuid.New(), ConfigValue: map[string]interface{}{"stars_to_coins": float64(10)}}
		d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "currency_exchange_rate").Return(cfg, nil)
		d.conversionR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.CurrencyConversion")).Return(errBoom)

		_, err := d.svc.Convert(ctx, tenantID, gcid, CurrencyTypeStars, CurrencyTypeCoins, 5)
		assert.ErrorIs(t, err, errBoom)
	})
}

// ===========================================================================
// CurrencyTransferService.Transfer — SumDailyTransferred + Create errors
// ===========================================================================

func TestCurrencyTransfer_RepoErrors(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	sender := uuid.New()
	receiver := uuid.New()

	t.Run("SumDailyTransferred error propagates", func(t *testing.T) {
		d := newTestCurrencyTransferService()
		d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "daily_transfer_limit").Return(nil, errBoom)
		d.transferR.On("SumDailyTransferred", mock.Anything, tenantID, sender, CurrencyTypeCoins, mock.AnythingOfType("time.Time")).Return(0, errBoom)

		_, err := d.svc.Transfer(ctx, tenantID, sender, receiver, CurrencyTypeCoins, 10, nil)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("transfer Create error propagates", func(t *testing.T) {
		d := newTestCurrencyTransferService()
		d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "daily_transfer_limit").Return(nil, errBoom)
		d.transferR.On("SumDailyTransferred", mock.Anything, tenantID, sender, CurrencyTypeCoins, mock.AnythingOfType("time.Time")).Return(0, nil)
		d.transferR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.CurrencyTransfer")).Return(errBoom)

		_, err := d.svc.Transfer(ctx, tenantID, sender, receiver, CurrencyTypeCoins, 10, nil)
		assert.ErrorIs(t, err, errBoom)
	})
}

// ===========================================================================
// EconomyConfigService — UpdateConfig / RollbackConfig / DeleteConfig errors
// ===========================================================================

func TestEconomyConfig_UpdateConfig_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	configID := uuid.New()
	gcid := uuid.New()
	newVal := map[string]interface{}{"k": "v"}

	t.Run("GetByID error propagates", func(t *testing.T) {
		d := newTestEconomyService()
		d.repo.On("GetByID", mock.Anything, configID).Return(nil, errBoom)

		_, err := d.svc.UpdateConfig(ctx, configID, newVal, gcid, "r")
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("history Create error propagates", func(t *testing.T) {
		d := newTestEconomyService()
		existing := &EconomyConfig{ID: configID, ConfigCategory: ConfigCategoryStoreLimits, ConfigKey: "k"}
		d.repo.On("GetByID", mock.Anything, configID).Return(existing, nil)
		d.repo.On("CreateHistoryEntry", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfigHistory")).Return(errBoom)

		_, err := d.svc.UpdateConfig(ctx, configID, newVal, gcid, "r")
		assert.ErrorIs(t, err, errBoom)
		d.repo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
	})

	t.Run("Update error propagates", func(t *testing.T) {
		d := newTestEconomyService()
		existing := &EconomyConfig{ID: configID, ConfigCategory: ConfigCategoryStoreLimits, ConfigKey: "k"}
		d.repo.On("GetByID", mock.Anything, configID).Return(existing, nil)
		d.repo.On("CreateHistoryEntry", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfigHistory")).Return(nil)
		d.repo.On("Update", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfig")).Return(errBoom)

		_, err := d.svc.UpdateConfig(ctx, configID, newVal, gcid, "r")
		assert.ErrorIs(t, err, errBoom)
		d.publisher.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("publish error propagates and tenant-id taken from config", func(t *testing.T) {
		d := newTestEconomyService()
		tenantID := uuid.New()
		existing := &EconomyConfig{ID: configID, TenantID: &tenantID, ConfigCategory: ConfigCategoryStoreLimits, ConfigKey: "k"}
		d.repo.On("GetByID", mock.Anything, configID).Return(existing, nil)
		d.repo.On("CreateHistoryEntry", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfigHistory")).Return(nil)
		d.repo.On("Update", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfig")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errBoom)

		_, err := d.svc.UpdateConfig(ctx, configID, newVal, gcid, "r")
		assert.ErrorIs(t, err, errBoom)
	})
}

func TestEconomyConfig_RollbackConfig_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	configID := uuid.New()
	gcid := uuid.New()

	t.Run("GetByID error propagates", func(t *testing.T) {
		d := newTestEconomyService()
		d.repo.On("GetByID", mock.Anything, configID).Return(nil, errBoom)

		_, err := d.svc.RollbackConfig(ctx, configID, gcid)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("GetHistory error propagates", func(t *testing.T) {
		d := newTestEconomyService()
		existing := &EconomyConfig{ID: configID, ConfigCategory: ConfigCategoryStoreLimits, ConfigKey: "k"}
		d.repo.On("GetByID", mock.Anything, configID).Return(existing, nil)
		d.repo.On("GetHistory", mock.Anything, configID, 1).Return(nil, errBoom)

		_, err := d.svc.RollbackConfig(ctx, configID, gcid)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("history Create error propagates", func(t *testing.T) {
		d := newTestEconomyService()
		existing := &EconomyConfig{ID: configID, ConfigCategory: ConfigCategoryStoreLimits, ConfigKey: "k"}
		hist := []*EconomyConfigHistory{{ID: uuid.New(), OldValue: map[string]interface{}{"old": 1}}}
		d.repo.On("GetByID", mock.Anything, configID).Return(existing, nil)
		d.repo.On("GetHistory", mock.Anything, configID, 1).Return(hist, nil)
		d.repo.On("CreateHistoryEntry", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfigHistory")).Return(errBoom)

		_, err := d.svc.RollbackConfig(ctx, configID, gcid)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("Update error propagates", func(t *testing.T) {
		d := newTestEconomyService()
		existing := &EconomyConfig{ID: configID, ConfigCategory: ConfigCategoryStoreLimits, ConfigKey: "k"}
		hist := []*EconomyConfigHistory{{ID: uuid.New(), OldValue: map[string]interface{}{"old": 1}}}
		d.repo.On("GetByID", mock.Anything, configID).Return(existing, nil)
		d.repo.On("GetHistory", mock.Anything, configID, 1).Return(hist, nil)
		d.repo.On("CreateHistoryEntry", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfigHistory")).Return(nil)
		d.repo.On("Update", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfig")).Return(errBoom)

		_, err := d.svc.RollbackConfig(ctx, configID, gcid)
		assert.ErrorIs(t, err, errBoom)
		d.publisher.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("publish error propagates", func(t *testing.T) {
		d := newTestEconomyService()
		existing := &EconomyConfig{ID: configID, ConfigCategory: ConfigCategoryStoreLimits, ConfigKey: "k"}
		hist := []*EconomyConfigHistory{{ID: uuid.New(), OldValue: map[string]interface{}{"old": 1}}}
		d.repo.On("GetByID", mock.Anything, configID).Return(existing, nil)
		d.repo.On("GetHistory", mock.Anything, configID, 1).Return(hist, nil)
		d.repo.On("CreateHistoryEntry", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfigHistory")).Return(nil)
		d.repo.On("Update", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfig")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errBoom)

		_, err := d.svc.RollbackConfig(ctx, configID, gcid)
		assert.ErrorIs(t, err, errBoom)
	})
}

func TestEconomyConfig_DeleteConfig_GetByIDError(t *testing.T) {
	ctx := context.Background()
	configID := uuid.New()

	d := newTestEconomyService()
	d.repo.On("GetByID", mock.Anything, configID).Return(nil, errBoom)

	err := d.svc.DeleteConfig(ctx, configID)
	assert.ErrorIs(t, err, errBoom)
	d.repo.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything)
}
