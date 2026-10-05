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
// LootboxService.Grant / Open — repo error branches
// ===========================================================================

func TestLootbox_Grant_CreateError(t *testing.T) {
	d := newTestLootboxService()
	ctx := context.Background()
	d.lootR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.Lootbox")).Return(errBoom)

	_, err := d.svc.Grant(ctx, uuid.New(), uuid.New(), LootboxTierStandard, LootboxSourceLevelUp)
	assert.ErrorIs(t, err, errBoom)
}

func TestLootbox_Open_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	lootboxID := uuid.New()
	tenantID := uuid.New()
	gcid := uuid.New()

	t.Run("GetByID error propagates", func(t *testing.T) {
		d := newTestLootboxService()
		d.lootR.On("GetByID", mock.Anything, lootboxID).Return(nil, errBoom)

		_, err := d.svc.Open(ctx, lootboxID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("loot table lookup error propagates", func(t *testing.T) {
		d := newTestLootboxService()
		lootbox := &Lootbox{ID: lootboxID, TenantID: tenantID, GCID: gcid, LootboxTier: LootboxTierStandard}
		d.lootR.On("GetByID", mock.Anything, lootboxID).Return(lootbox, nil)
		d.tableR.On("ListByTier", mock.Anything, tenantID, "standard").Return(nil, errBoom)

		_, err := d.svc.Open(ctx, lootboxID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("empty loot table is rejected", func(t *testing.T) {
		d := newTestLootboxService()
		lootbox := &Lootbox{ID: lootboxID, TenantID: tenantID, GCID: gcid, LootboxTier: LootboxTierStandard}
		d.lootR.On("GetByID", mock.Anything, lootboxID).Return(lootbox, nil)
		d.tableR.On("ListByTier", mock.Anything, tenantID, "standard").Return([]*LootTable{}, nil)

		_, err := d.svc.Open(ctx, lootboxID)
		assert.ErrorIs(t, err, ErrLootTableEmpty)
	})

	t.Run("lootbox Update error propagates", func(t *testing.T) {
		d := newTestLootboxService()
		lootbox := &Lootbox{ID: lootboxID, TenantID: tenantID, GCID: gcid, LootboxTier: LootboxTierStandard}
		entries := []*LootTable{{ID: uuid.New(), Tier: "standard", ItemType: LootItemCoins, QuantityMin: 5, QuantityMax: 5, Weight: 100}}
		d.lootR.On("GetByID", mock.Anything, lootboxID).Return(lootbox, nil)
		d.tableR.On("ListByTier", mock.Anything, tenantID, "standard").Return(entries, nil)
		d.lootR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.Lootbox")).Return(errBoom)

		_, err := d.svc.Open(ctx, lootboxID)
		assert.ErrorIs(t, err, errBoom)
		d.publisher.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("single fixed-quantity entry yields exact quantity", func(t *testing.T) {
		d := newTestLootboxService()
		lootbox := &Lootbox{ID: lootboxID, TenantID: tenantID, GCID: gcid, LootboxTier: LootboxTierStandard}
		// QuantityMax == QuantityMin → no random range, quantity is deterministic.
		entries := []*LootTable{{ID: uuid.New(), Tier: "standard", ItemType: LootItemCoins, QuantityMin: 42, QuantityMax: 42, Weight: 100}}
		d.lootR.On("GetByID", mock.Anything, lootboxID).Return(lootbox, nil)
		d.tableR.On("ListByTier", mock.Anything, tenantID, "standard").Return(entries, nil)
		d.lootR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.Lootbox")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

		result, err := d.svc.Open(ctx, lootboxID)
		require.NoError(t, err)
		assert.Equal(t, LootItemCoins, result.ItemType)
		assert.Equal(t, 42, result.Quantity)
	})
}

// ===========================================================================
// LoginCalendarService.DailyCheckin — error branches + reward cap
// ===========================================================================

func TestLoginCalendar_DailyCheckin_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	yesterday := today.AddDate(0, 0, -1)

	t.Run("today lookup error propagates", func(t *testing.T) {
		d := newTestLoginCalendarService()
		d.calR.On("GetByDate", mock.Anything, tenantID, gcid, today).Return(nil, errBoom)

		_, err := d.svc.DailyCheckin(ctx, tenantID, gcid)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("yesterday lookup error propagates", func(t *testing.T) {
		d := newTestLoginCalendarService()
		d.calR.On("GetByDate", mock.Anything, tenantID, gcid, today).Return(nil, nil)
		d.calR.On("GetByDate", mock.Anything, tenantID, gcid, yesterday).Return(nil, errBoom)

		_, err := d.svc.DailyCheckin(ctx, tenantID, gcid)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("calendar Create error propagates", func(t *testing.T) {
		d := newTestLoginCalendarService()
		d.calR.On("GetByDate", mock.Anything, tenantID, gcid, today).Return(nil, nil)
		d.calR.On("GetByDate", mock.Anything, tenantID, gcid, yesterday).Return(nil, nil)
		d.calR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.LoginCalendar")).Return(errBoom)

		_, err := d.svc.DailyCheckin(ctx, tenantID, gcid)
		assert.ErrorIs(t, err, errBoom)
		d.publisher.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("reward value capped at 100 for long non-weekly streaks", func(t *testing.T) {
		d := newTestLoginCalendarService()
		// yesterday streak 10 → today 11; 11 is not a multiple of 7, and
		// 10*11 = 110 → capped to 100.
		ye := &LoginCalendar{ID: uuid.New(), TenantID: tenantID, GCID: gcid, StreakDay: 10}
		d.calR.On("GetByDate", mock.Anything, tenantID, gcid, today).Return(nil, nil)
		d.calR.On("GetByDate", mock.Anything, tenantID, gcid, yesterday).Return(ye, nil)
		d.calR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.LoginCalendar")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

		got, err := d.svc.DailyCheckin(ctx, tenantID, gcid)
		require.NoError(t, err)
		assert.Equal(t, 11, got.StreakDay)
		assert.Equal(t, "coins", got.RewardType)
		assert.Equal(t, 100, got.RewardValue)
	})

	t.Run("seven-day lootbox grant error propagates", func(t *testing.T) {
		d := newTestLoginCalendarService()
		// yesterday streak 6 → today 7 → lootbox grant; force the grant to fail.
		ye := &LoginCalendar{ID: uuid.New(), TenantID: tenantID, GCID: gcid, StreakDay: 6}
		d.calR.On("GetByDate", mock.Anything, tenantID, gcid, today).Return(nil, nil)
		d.calR.On("GetByDate", mock.Anything, tenantID, gcid, yesterday).Return(ye, nil)
		d.lootR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.Lootbox")).Return(errBoom)

		_, err := d.svc.DailyCheckin(ctx, tenantID, gcid)
		assert.ErrorIs(t, err, errBoom)
		// Calendar entry must not be persisted when the embedded grant fails.
		d.calR.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	})
}

// ===========================================================================
// GroupFundService — CreatePool / Contribute / DistributePool error branches
// ===========================================================================

func TestGroupFund_CreatePool_CreateError(t *testing.T) {
	d := newTestGroupFundService()
	ctx := context.Background()
	d.poolR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.GroupFundPool")).Return(errBoom)

	_, err := d.svc.CreatePool(ctx, uuid.New(), "P", PoolTypeBossChallenge, 100)
	assert.ErrorIs(t, err, errBoom)
}

func TestGroupFund_Contribute_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	poolID := uuid.New()
	gcid := uuid.New()

	t.Run("pool lookup error propagates", func(t *testing.T) {
		d := newTestGroupFundService()
		d.poolR.On("GetByID", mock.Anything, poolID).Return(nil, errBoom)

		_, err := d.svc.Contribute(ctx, tenantID, poolID, gcid, 50)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("contribution Create error propagates", func(t *testing.T) {
		d := newTestGroupFundService()
		pool := &GroupFundPool{ID: poolID, TenantID: tenantID, Status: PoolStatusCollecting}
		d.poolR.On("GetByID", mock.Anything, poolID).Return(pool, nil)
		d.contributionR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.GroupFundContribution")).Return(errBoom)

		_, err := d.svc.Contribute(ctx, tenantID, poolID, gcid, 50)
		assert.ErrorIs(t, err, errBoom)
		d.poolR.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
	})

	t.Run("pool Update error propagates", func(t *testing.T) {
		d := newTestGroupFundService()
		pool := &GroupFundPool{ID: poolID, TenantID: tenantID, Status: PoolStatusCollecting}
		d.poolR.On("GetByID", mock.Anything, poolID).Return(pool, nil)
		d.contributionR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.GroupFundContribution")).Return(nil)
		d.poolR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.GroupFundPool")).Return(errBoom)

		_, err := d.svc.Contribute(ctx, tenantID, poolID, gcid, 50)
		assert.ErrorIs(t, err, errBoom)
	})
}

func TestGroupFund_DistributePool_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	poolID := uuid.New()

	t.Run("pool lookup error propagates", func(t *testing.T) {
		d := newTestGroupFundService()
		d.poolR.On("GetByID", mock.Anything, poolID).Return(nil, errBoom)

		err := d.svc.DistributePool(ctx, poolID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("pool Update error propagates", func(t *testing.T) {
		d := newTestGroupFundService()
		pool := &GroupFundPool{ID: poolID, TenantID: uuid.New(), Status: PoolStatusCollecting}
		d.poolR.On("GetByID", mock.Anything, poolID).Return(pool, nil)
		d.poolR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.GroupFundPool")).Return(errBoom)

		err := d.svc.DistributePool(ctx, poolID)
		assert.ErrorIs(t, err, errBoom)
		d.publisher.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything, mock.Anything)
	})
}

// ===========================================================================
// CoinService.ListTransactions — account lookup error branch
// ===========================================================================

func TestCoinService_ListTransactions_GetByGCIDError(t *testing.T) {
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(nil, errBoom)

	_, err := d.svc.ListTransactions(ctx, tenantID, gcid, 0, 20)
	assert.ErrorIs(t, err, errBoom)
}
