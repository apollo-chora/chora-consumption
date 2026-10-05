package gamification

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ===========================================================================
// MarketplaceService.CreateListing / BuyListing / CancelListing / ExpireListings
// ===========================================================================

func TestMarketplace_CreateListing_CreateError(t *testing.T) {
	d := newTestMarketplaceService()
	ctx := context.Background()
	d.listingR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.MarketListing")).Return(errBoom)

	_, err := d.svc.CreateListing(ctx, uuid.New(), uuid.New(), MarketItemTypeCard, uuid.New(), 1, 100, nil)
	assert.ErrorIs(t, err, errBoom)
}

func TestMarketplace_BuyListing_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	buyer := uuid.New()
	seller := uuid.New()
	listingID := uuid.New()

	active := func() *MarketListing {
		return &MarketListing{
			ID: listingID, TenantID: tenantID, SellerGCID: seller,
			ItemType: MarketItemTypeCard, ItemID: uuid.New(),
			PriceCoins: 500, Status: ListingStatusActive,
			ExpiresAt: time.Now().Add(24 * time.Hour),
		}
	}

	t.Run("listing lookup error propagates", func(t *testing.T) {
		d := newTestMarketplaceService()
		d.listingR.On("GetByID", mock.Anything, listingID).Return(nil, errBoom)

		_, err := d.svc.BuyListing(ctx, tenantID, listingID, buyer, PaymentMethodCoins)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("expired listing rejected", func(t *testing.T) {
		d := newTestMarketplaceService()
		l := active()
		l.ExpiresAt = time.Now().Add(-time.Hour)
		d.listingR.On("GetByID", mock.Anything, listingID).Return(l, nil)

		_, err := d.svc.BuyListing(ctx, tenantID, listingID, buyer, PaymentMethodCoins)
		assert.ErrorIs(t, err, ErrListingNotAvailable)
	})

	t.Run("stars payment without star price is invalid", func(t *testing.T) {
		d := newTestMarketplaceService()
		l := active() // PriceStars is nil
		d.listingR.On("GetByID", mock.Anything, listingID).Return(l, nil)
		d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "marketplace_fee_pct").Return(nil, errBoom)

		_, err := d.svc.BuyListing(ctx, tenantID, listingID, buyer, PaymentMethodStars)
		assert.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("stars payment with star price uses star amount", func(t *testing.T) {
		d := newTestMarketplaceService()
		stars := 80
		l := active()
		l.PriceStars = &stars
		d.listingR.On("GetByID", mock.Anything, listingID).Return(l, nil)
		d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "marketplace_fee_pct").Return(nil, errBoom)
		d.orderR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.MarketOrder")).Return(nil)
		d.listingR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.MarketListing")).Return(nil)
		d.txR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.MarketTransaction")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

		order, err := d.svc.BuyListing(ctx, tenantID, listingID, buyer, PaymentMethodStars)
		require.NoError(t, err)
		// Fee defaults to 5% when config lookup errored; price is the star amount.
		assert.Equal(t, 80, order.AmountPaid)
	})

	t.Run("order Create error propagates", func(t *testing.T) {
		d := newTestMarketplaceService()
		d.listingR.On("GetByID", mock.Anything, listingID).Return(active(), nil)
		d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "marketplace_fee_pct").Return(nil, errBoom)
		d.orderR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.MarketOrder")).Return(errBoom)

		_, err := d.svc.BuyListing(ctx, tenantID, listingID, buyer, PaymentMethodCoins)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("listing Update error propagates", func(t *testing.T) {
		d := newTestMarketplaceService()
		d.listingR.On("GetByID", mock.Anything, listingID).Return(active(), nil)
		d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "marketplace_fee_pct").Return(nil, errBoom)
		d.orderR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.MarketOrder")).Return(nil)
		d.listingR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.MarketListing")).Return(errBoom)

		_, err := d.svc.BuyListing(ctx, tenantID, listingID, buyer, PaymentMethodCoins)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("transaction Create error propagates", func(t *testing.T) {
		d := newTestMarketplaceService()
		d.listingR.On("GetByID", mock.Anything, listingID).Return(active(), nil)
		d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "marketplace_fee_pct").Return(nil, errBoom)
		d.orderR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.MarketOrder")).Return(nil)
		d.listingR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.MarketListing")).Return(nil)
		d.txR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.MarketTransaction")).Return(errBoom)

		_, err := d.svc.BuyListing(ctx, tenantID, listingID, buyer, PaymentMethodCoins)
		assert.ErrorIs(t, err, errBoom)
		d.publisher.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("fee config present overrides default", func(t *testing.T) {
		d := newTestMarketplaceService()
		feeCfg := &EconomyConfig{ID: uuid.New(), ConfigValue: map[string]interface{}{"fee_pct": float64(10)}}
		d.listingR.On("GetByID", mock.Anything, listingID).Return(active(), nil)
		d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "marketplace_fee_pct").Return(feeCfg, nil)
		d.orderR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.MarketOrder")).Return(nil)
		d.listingR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.MarketListing")).Return(nil)
		var captured *MarketTransaction
		d.txR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.MarketTransaction")).
			Run(func(args mock.Arguments) { captured = args.Get(1).(*MarketTransaction) }).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

		_, err := d.svc.BuyListing(ctx, tenantID, listingID, buyer, PaymentMethodCoins)
		require.NoError(t, err)
		require.NotNil(t, captured)
		// 500 * 10% = 50 fee; net = 450.
		assert.Equal(t, 50, captured.FeeAmount)
		assert.Equal(t, 450, captured.NetAmount)
	})
}

func TestMarketplace_CancelListing_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	listingID := uuid.New()
	owner := uuid.New()

	t.Run("listing lookup error propagates", func(t *testing.T) {
		d := newTestMarketplaceService()
		d.listingR.On("GetByID", mock.Anything, listingID).Return(nil, errBoom)

		err := d.svc.CancelListing(ctx, listingID, owner)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("non-active listing cannot be cancelled", func(t *testing.T) {
		d := newTestMarketplaceService()
		l := &MarketListing{ID: listingID, SellerGCID: owner, Status: ListingStatusSold}
		d.listingR.On("GetByID", mock.Anything, listingID).Return(l, nil)

		err := d.svc.CancelListing(ctx, listingID, owner)
		assert.ErrorIs(t, err, ErrListingNotAvailable)
	})
}

func TestMarketplace_ExpireListings_ListError(t *testing.T) {
	d := newTestMarketplaceService()
	ctx := context.Background()
	tenantID := uuid.New()
	d.listingR.On("ListExpired", mock.Anything, tenantID, mock.AnythingOfType("time.Time")).Return(nil, errBoom)

	count, err := d.svc.ExpireListings(ctx, tenantID)
	assert.ErrorIs(t, err, errBoom)
	assert.Equal(t, 0, count)
}

func TestMarketplace_ExpireListings_UpdateErrorSkipsEntry(t *testing.T) {
	d := newTestMarketplaceService()
	ctx := context.Background()
	tenantID := uuid.New()
	expired := []*MarketListing{
		{ID: uuid.New(), TenantID: tenantID, SellerGCID: uuid.New(), ItemType: MarketItemTypeCard, Status: ListingStatusActive},
		{ID: uuid.New(), TenantID: tenantID, SellerGCID: uuid.New(), ItemType: MarketItemTypeCard, Status: ListingStatusActive},
	}
	d.listingR.On("ListExpired", mock.Anything, tenantID, mock.AnythingOfType("time.Time")).Return(expired, nil)
	// First update fails (entry skipped, no publish), second succeeds (publish + count).
	d.listingR.On("Update", mock.Anything, expired[0]).Return(errBoom)
	d.listingR.On("Update", mock.Anything, expired[1]).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil).Once()

	count, err := d.svc.ExpireListings(ctx, tenantID)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	d.publisher.AssertNumberOfCalls(t, "Publish", 1)
}

// ===========================================================================
// EconomyDrainService — config-lookup error branches
// ===========================================================================

func TestDrain_TerritoryMaintenance_ConfigError(t *testing.T) {
	d := newTestDrainService()
	ctx := context.Background()
	tenantID := uuid.New()
	d.econRepo.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "territory_maintenance_cost").Return(nil, errBoom)

	_, err := d.svc.ExecuteTerritoryMaintenanceDrain(ctx, tenantID, []TerritoryHolder{{GCID: uuid.New(), TerritoryCount: 1}})
	assert.ErrorIs(t, err, errBoom)
}

func TestDrain_TerritoryMaintenance_AccountLookupErrorSkipsHolder(t *testing.T) {
	d := newTestDrainService()
	ctx := context.Background()
	tenantID := uuid.New()
	cfg := &EconomyConfig{ID: uuid.New(), ConfigValue: map[string]interface{}{"daily_cost_per_territory": float64(5)}}
	gcid := uuid.New()
	d.econRepo.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "territory_maintenance_cost").Return(cfg, nil)
	d.coinRepo.On("GetByGCID", mock.Anything, tenantID, gcid).Return(nil, errBoom)

	result, err := d.svc.ExecuteTerritoryMaintenanceDrain(ctx, tenantID, []TerritoryHolder{{GCID: gcid, TerritoryCount: 2}})
	require.NoError(t, err)
	// Holder skipped on lookup error: nothing processed, nothing drained.
	assert.Equal(t, 0, result.AccountsProcessed)
	assert.Equal(t, int64(0), result.TotalDrained)
}

func TestDrain_MaterialExpiry_ConfigError(t *testing.T) {
	d := newTestDrainService()
	ctx := context.Background()
	tenantID := uuid.New()
	d.econRepo.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryMaterialDropRates, "material_shelf_life").Return(nil, errBoom)

	_, err := d.svc.ExecuteMaterialExpiry(ctx, tenantID, nil)
	assert.ErrorIs(t, err, errBoom)
}

func TestDrain_ELODecay_ConfigError(t *testing.T) {
	d := newTestDrainService()
	ctx := context.Background()
	tenantID := uuid.New()
	d.econRepo.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryEloSettings, "elo_inactivity_decay").Return(nil, errBoom)

	_, err := d.svc.ExecuteELODecay(ctx, tenantID, nil)
	assert.ErrorIs(t, err, errBoom)
}

// ===========================================================================
// EconomyHealthService.CaptureSnapshot — each coin-repo aggregate error branch
// ===========================================================================

func TestCaptureSnapshot_AggregateErrorBranches(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	day := time.Now().UTC().Truncate(24 * time.Hour)

	t.Run("SumBalances error propagates", func(t *testing.T) {
		d := newTestHealthService()
		d.coinRepo.On("SumBalances", mock.Anything, tenantID).Return(int64(0), errBoom)

		_, err := d.svc.CaptureSnapshot(ctx, tenantID, day)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("CountActiveAccounts error propagates", func(t *testing.T) {
		d := newTestHealthService()
		d.coinRepo.On("SumBalances", mock.Anything, tenantID).Return(int64(100), nil)
		d.coinRepo.On("CountActiveAccounts", mock.Anything, tenantID).Return(0, errBoom)

		_, err := d.svc.CaptureSnapshot(ctx, tenantID, day)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("SumDailyMinted error propagates", func(t *testing.T) {
		d := newTestHealthService()
		d.coinRepo.On("SumBalances", mock.Anything, tenantID).Return(int64(100), nil)
		d.coinRepo.On("CountActiveAccounts", mock.Anything, tenantID).Return(5, nil)
		d.coinRepo.On("SumDailyMinted", mock.Anything, tenantID, day).Return(int64(0), errBoom)

		_, err := d.svc.CaptureSnapshot(ctx, tenantID, day)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("SumDailyDrained error propagates", func(t *testing.T) {
		d := newTestHealthService()
		d.coinRepo.On("SumBalances", mock.Anything, tenantID).Return(int64(100), nil)
		d.coinRepo.On("CountActiveAccounts", mock.Anything, tenantID).Return(5, nil)
		d.coinRepo.On("SumDailyMinted", mock.Anything, tenantID, day).Return(int64(10), nil)
		d.coinRepo.On("SumDailyDrained", mock.Anything, tenantID, day).Return(int64(0), errBoom)

		_, err := d.svc.CaptureSnapshot(ctx, tenantID, day)
		assert.ErrorIs(t, err, errBoom)
	})
}

// ===========================================================================
// TerritoryRewardService.DistributeDaily — config lookup error
// + ConfigureIncome CreateConfig error
// ===========================================================================

func TestTerritoryReward_DistributeDaily_ConfigError(t *testing.T) {
	d := newTestTerritoryRewardService()
	ctx := context.Background()
	tenantID := uuid.New()
	d.incomeR.On("ListConfigsByTenant", mock.Anything, tenantID, IncomeFrequencyDaily).Return(nil, errBoom)

	_, err := d.svc.DistributeDaily(ctx, tenantID, nil)
	assert.ErrorIs(t, err, errBoom)
}

func TestTerritoryReward_ConfigureIncome_CreateError(t *testing.T) {
	d := newTestTerritoryRewardService()
	ctx := context.Background()
	tenantID := uuid.New()
	d.incomeR.On("CreateConfig", mock.Anything, mock.AnythingOfType("*gamification.TerritoryIncomeConfig")).Return(errBoom)

	_, err := d.svc.ConfigureIncome(ctx, tenantID, IncomeTypeCoins, 100, 0.5, IncomeFrequencyDaily)
	assert.ErrorIs(t, err, errBoom)
}
