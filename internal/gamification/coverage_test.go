package gamification

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// ---------------------------------------------------------------------------
// nextLeagueTier / prevLeagueTier — cover all tier transitions
// ---------------------------------------------------------------------------

func TestNextLeagueTier_AllTiers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input LeagueTier
		want  LeagueTier
	}{
		{LeagueTierBronze, LeagueTierSilver},
		{LeagueTierSilver, LeagueTierGold},
		{LeagueTierGold, LeagueTierPlatinum},
		{LeagueTierPlatinum, LeagueTierDiamond},
		{LeagueTierDiamond, LeagueTierDiamond}, // at max, stays
	}
	for _, tc := range tests {
		t.Run(string(tc.input)+"_to_"+string(tc.want), func(t *testing.T) {
			assert.Equal(t, tc.want, nextLeagueTier(tc.input))
		})
	}
}

func TestPrevLeagueTier_AllTiers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input LeagueTier
		want  LeagueTier
	}{
		{LeagueTierDiamond, LeagueTierPlatinum},
		{LeagueTierPlatinum, LeagueTierGold},
		{LeagueTierGold, LeagueTierSilver},
		{LeagueTierSilver, LeagueTierBronze},
		{LeagueTierBronze, LeagueTierBronze}, // at min, stays
	}
	for _, tc := range tests {
		t.Run(string(tc.input)+"_to_"+string(tc.want), func(t *testing.T) {
			assert.Equal(t, tc.want, prevLeagueTier(tc.input))
		})
	}
}

// ---------------------------------------------------------------------------
// badgeTierRank — cover default case
// ---------------------------------------------------------------------------

func TestBadgeTierRank_AllValues(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 1, badgeTierRank(BadgeTierBronze))
	assert.Equal(t, 2, badgeTierRank(BadgeTierSilver))
	assert.Equal(t, 3, badgeTierRank(BadgeTierGold))
	assert.Equal(t, 0, badgeTierRank(BadgeTier("unknown")))
}

// ---------------------------------------------------------------------------
// ProcessPromotion — Diamond ceiling (already at max tier)
// ---------------------------------------------------------------------------

func TestProcessPromotion_DiamondCeiling(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existing := &League{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LeagueTier: LeagueTierDiamond, SeasonWeek: "2026-W11",
		Points: 999, Rank: 1,
	}

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(existing, nil)
	d.leagueR.On("Upsert", mock.Anything, mock.AnythingOfType("*gamification.League")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.ProcessPromotion(ctx, tenantID, gcid, "2026-W11")
	assert.NoError(t, err)
	assert.True(t, got.Promoted)
	// Should stay Diamond since nextLeagueTier(Diamond) == Diamond.
	assert.Equal(t, LeagueTierDiamond, got.LeagueTier)
	d.leagueR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// ProcessRelegation — Bronze floor (already at lowest tier)
// ---------------------------------------------------------------------------

func TestProcessRelegation_BronzeFloor(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existing := &League{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LeagueTier: LeagueTierBronze, SeasonWeek: "2026-W11",
		Points: 5, Rank: 20,
	}

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(existing, nil)
	d.leagueR.On("Upsert", mock.Anything, mock.AnythingOfType("*gamification.League")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.ProcessRelegation(ctx, tenantID, gcid, "2026-W11")
	assert.NoError(t, err)
	assert.True(t, got.Relegated)
	// Should stay Bronze since prevLeagueTier(Bronze) == Bronze.
	assert.Equal(t, LeagueTierBronze, got.LeagueTier)
	d.leagueR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// ProcessPromotion — exact boundary (rank == 3)
// ---------------------------------------------------------------------------

func TestProcessPromotion_ExactBoundary(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existing := &League{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LeagueTier: LeagueTierGold, SeasonWeek: "2026-W11",
		Points: 300, Rank: 3, // exactly at threshold
	}

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(existing, nil)
	d.leagueR.On("Upsert", mock.Anything, mock.AnythingOfType("*gamification.League")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.ProcessPromotion(ctx, tenantID, gcid, "2026-W11")
	assert.NoError(t, err)
	assert.True(t, got.Promoted)
	assert.Equal(t, LeagueTierPlatinum, got.LeagueTier)
	d.leagueR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// ProcessRelegation — exact boundary (rank == 18)
// ---------------------------------------------------------------------------

func TestProcessRelegation_ExactBoundary(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existing := &League{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LeagueTier: LeagueTierPlatinum, SeasonWeek: "2026-W11",
		Points: 20, Rank: 18, // exactly at threshold
	}

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(existing, nil)
	d.leagueR.On("Upsert", mock.Anything, mock.AnythingOfType("*gamification.League")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.ProcessRelegation(ctx, tenantID, gcid, "2026-W11")
	assert.NoError(t, err)
	assert.True(t, got.Relegated)
	assert.Equal(t, LeagueTierGold, got.LeagueTier)
	d.leagueR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// UpdateSkin — validation: empty skin_code, invalid rarity
// ---------------------------------------------------------------------------

func TestUpdateSkin_EmptySkinCode(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()

	input := &DigitalSkin{
		ID:       uuid.Must(uuid.NewV7()),
		SkinCode: "",
		Rarity:   SkinRarityCommon,
	}

	_, err := d.svc.UpdateSkin(context.Background(), input)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

func TestUpdateSkin_InvalidRarity(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()

	input := &DigitalSkin{
		ID:       uuid.Must(uuid.NewV7()),
		SkinCode: "SKN-TEST-001",
		Rarity:   SkinRarity("mythic"),
	}

	_, err := d.svc.UpdateSkin(context.Background(), input)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

// ---------------------------------------------------------------------------
// UnequipSkin — invalid slot
// ---------------------------------------------------------------------------

func TestUnequipSkin_InvalidSlot(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()

	err := d.svc.UnequipSkin(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), EquipmentSlot("helmet"))
	assert.ErrorIs(t, err, ErrValidationFailed)
}

// ---------------------------------------------------------------------------
// ExportSkin — invalid target
// ---------------------------------------------------------------------------

func TestExportSkin_InvalidTarget(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()

	_, err := d.svc.ExportSkin(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), ExportTarget("tiktok"))
	assert.ErrorIs(t, err, ErrValidationFailed)
}

// ---------------------------------------------------------------------------
// DeleteSkin — repo error
// ---------------------------------------------------------------------------

func TestDeleteSkin_RepoError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	skinID := uuid.Must(uuid.NewV7())

	d.skinR.On("Delete", mock.Anything, skinID).Return(errors.New("db error"))

	err := d.svc.DeleteSkin(context.Background(), skinID)
	assert.Error(t, err)
	d.skinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// EarnCoins — repo error on UpdateBalance
// ---------------------------------------------------------------------------

func TestEarnCoins_UpdateBalanceError(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existingAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		Balance: 100, LifetimeEarned: 200,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(existingAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(errors.New("db write error"))

	_, err := d.svc.EarnCoins(ctx, tenantID, gcid, 50, "test", nil, nil)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// RefundCoins — verify transaction records correct type
// ---------------------------------------------------------------------------

func TestRefundCoins_NegativeAmount(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	_, err := d.svc.RefundCoins(ctx, tenantID, gcid, -50, "invalid", nil, nil)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

// ---------------------------------------------------------------------------
// SpendCoins — exact balance (spend all coins)
// ---------------------------------------------------------------------------

func TestSpendCoins_ExactBalance(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existingAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		Balance: 100, LifetimeEarned: 300, LifetimeSpent: 200,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(existingAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.coinR.On("CreateTransaction", mock.Anything, mock.AnythingOfType("*gamification.CoinTransaction")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.SpendCoins(ctx, tenantID, gcid, 100, "spend_all", nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), got.Balance)
	assert.Equal(t, int64(300), got.LifetimeSpent)
	d.coinR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TransferLegendarySkin — multiple previous holders
// ---------------------------------------------------------------------------

func TestTransferLegendarySkin_AccumulatesPreviousHolders(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()
	tenantID := uuid.Must(uuid.NewV7())
	skinID := uuid.Must(uuid.NewV7())
	holder1 := uuid.Must(uuid.NewV7())
	holder2 := uuid.Must(uuid.NewV7())
	newHolder := uuid.Must(uuid.NewV7())

	// Already has one previous holder.
	legendary := &LegendarySkin{
		ID:                uuid.Must(uuid.NewV7()),
		TenantID:          tenantID,
		SkinID:            skinID,
		CurrentHolderGCID: holder2,
		HeldSince:         time.Now().Add(-3 * 24 * time.Hour),
		TransferReason:    "Second champion",
		PreviousHolders: []map[string]interface{}{
			{"gcid": holder1.String(), "held_since": "2026-01-01T00:00:00Z", "reason": "First champion"},
		},
	}

	d.legendaryR.On("GetBySkinID", mock.Anything, tenantID, skinID).Return(legendary, nil)
	d.legendaryR.On("TransferHolder", mock.Anything, mock.AnythingOfType("*gamification.LegendarySkin")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.TransferLegendarySkin(ctx, tenantID, skinID, newHolder, "Third champion")
	assert.NoError(t, err)
	assert.Equal(t, newHolder, got.CurrentHolderGCID)
	assert.Equal(t, "Third champion", got.TransferReason)
	// Should now have two previous holders.
	assert.Len(t, got.PreviousHolders, 2)
	assert.Equal(t, holder2.String(), got.PreviousHolders[1]["gcid"])
	d.legendaryR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// CancelBounty — poster account nil (no refund needed)
// ---------------------------------------------------------------------------

func TestCancelBounty_PosterAccountNil(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	posterGCID := uuid.New()
	tenantID := uuid.New()

	bounty := &KnowledgeBounty{
		ID: bountyID, TenantID: tenantID, PosterGCID: posterGCID,
		Status: BountyStatusOpen, CoinReward: 100,
	}

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(bounty, nil)
	d.bountyR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.KnowledgeBounty")).Return(nil)
	d.coinR.On("GetByGCID", mock.Anything, tenantID, posterGCID).Return(nil, nil)

	err := d.svc.CancelBounty(ctx, bountyID, posterGCID)
	assert.NoError(t, err)
	assert.Equal(t, BountyStatusCancelled, bounty.Status)
	d.bountyR.AssertExpectations(t)
	d.coinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// CreateBounty — boundary values (min/max reward)
// ---------------------------------------------------------------------------

func TestCreateBounty_MinReward(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	tenantID := uuid.New()
	posterGCID := uuid.New()
	atomID := uuid.New()
	expiresAt := time.Now().Add(72 * time.Hour)

	posterAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: posterGCID,
		Balance: 500, LifetimeEarned: 1000, LifetimeSpent: 500,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, posterGCID).Return(posterAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.bountyR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.KnowledgeBounty")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.CreateBounty(ctx, tenantID, posterGCID, atomID, "Min bounty", "desc", 50, 0, expiresAt)
	assert.NoError(t, err)
	assert.Equal(t, 50, got.CoinReward)
	d.coinR.AssertExpectations(t)
	d.bountyR.AssertExpectations(t)
}

func TestCreateBounty_MaxReward(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	tenantID := uuid.New()
	posterGCID := uuid.New()
	atomID := uuid.New()
	expiresAt := time.Now().Add(72 * time.Hour)

	posterAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: posterGCID,
		Balance: 1000, LifetimeEarned: 2000, LifetimeSpent: 1000,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, posterGCID).Return(posterAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.bountyR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.KnowledgeBounty")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.CreateBounty(ctx, tenantID, posterGCID, atomID, "Max bounty", "desc", 500, 0, expiresAt)
	assert.NoError(t, err)
	assert.Equal(t, 500, got.CoinReward)
	d.coinR.AssertExpectations(t)
	d.bountyR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// RecordPoints — repo error on Upsert
// ---------------------------------------------------------------------------

func TestRecordPoints_UpsertError(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existing := &League{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LeagueTier: LeagueTierSilver, SeasonWeek: "2026-W11",
		Points: 100,
	}

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(existing, nil)
	d.leagueR.On("Upsert", mock.Anything, mock.AnythingOfType("*gamification.League")).Return(errors.New("db write fail"))

	_, err := d.svc.RecordPoints(ctx, tenantID, gcid, "2026-W11", 50)
	assert.Error(t, err)
	d.leagueR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// EarnCoins — repo error on CreateTransaction
// ---------------------------------------------------------------------------

func TestEarnCoins_CreateTransactionError(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existingAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		Balance: 100, LifetimeEarned: 200,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(existingAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.coinR.On("CreateTransaction", mock.Anything, mock.AnythingOfType("*gamification.CoinTransaction")).Return(errors.New("tx error"))

	_, err := d.svc.EarnCoins(ctx, tenantID, gcid, 50, "test", nil, nil)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// EarnCoins — repo error on GetByGCID
// ---------------------------------------------------------------------------

func TestEarnCoins_GetByGCIDError(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(nil, errors.New("db timeout"))

	_, err := d.svc.EarnCoins(ctx, tenantID, gcid, 50, "test", nil, nil)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// EarnCoins — event publish error
// ---------------------------------------------------------------------------

func TestEarnCoins_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existingAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		Balance: 100, LifetimeEarned: 200,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(existingAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.coinR.On("CreateTransaction", mock.Anything, mock.AnythingOfType("*gamification.CoinTransaction")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errors.New("pubsub down"))

	_, err := d.svc.EarnCoins(ctx, tenantID, gcid, 50, "test", nil, nil)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// SpendCoins — repo error on GetByGCID
// ---------------------------------------------------------------------------

func TestSpendCoins_GetByGCIDError(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(nil, errors.New("db timeout"))

	_, err := d.svc.SpendCoins(ctx, tenantID, gcid, 50, "test", nil, nil)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// SpendCoins — repo error on UpdateBalance
// ---------------------------------------------------------------------------

func TestSpendCoins_UpdateBalanceError(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existingAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		Balance: 200, LifetimeEarned: 500, LifetimeSpent: 300,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(existingAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(errors.New("db write fail"))

	_, err := d.svc.SpendCoins(ctx, tenantID, gcid, 50, "test", nil, nil)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// SpendCoins — repo error on CreateTransaction
// ---------------------------------------------------------------------------

func TestSpendCoins_CreateTransactionError(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existingAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		Balance: 200, LifetimeEarned: 500, LifetimeSpent: 300,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(existingAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.coinR.On("CreateTransaction", mock.Anything, mock.AnythingOfType("*gamification.CoinTransaction")).Return(errors.New("tx error"))

	_, err := d.svc.SpendCoins(ctx, tenantID, gcid, 50, "test", nil, nil)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// SpendCoins — event publish error
// ---------------------------------------------------------------------------

func TestSpendCoins_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existingAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		Balance: 200, LifetimeEarned: 500, LifetimeSpent: 300,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(existingAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.coinR.On("CreateTransaction", mock.Anything, mock.AnythingOfType("*gamification.CoinTransaction")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errors.New("pubsub down"))

	_, err := d.svc.SpendCoins(ctx, tenantID, gcid, 50, "test", nil, nil)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// RefundCoins — repo error on GetByGCID
// ---------------------------------------------------------------------------

func TestRefundCoins_GetByGCIDError(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(nil, errors.New("db timeout"))

	_, err := d.svc.RefundCoins(ctx, tenantID, gcid, 50, "test", nil, nil)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// RefundCoins — repo error on UpdateBalance
// ---------------------------------------------------------------------------

func TestRefundCoins_UpdateBalanceError(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existingAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		Balance: 100, LifetimeEarned: 300, LifetimeSpent: 200,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(existingAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(errors.New("db write fail"))

	_, err := d.svc.RefundCoins(ctx, tenantID, gcid, 50, "test", nil, nil)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// RefundCoins — repo error on CreateTransaction
// ---------------------------------------------------------------------------

func TestRefundCoins_CreateTransactionError(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existingAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		Balance: 100, LifetimeEarned: 300, LifetimeSpent: 200,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(existingAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.coinR.On("CreateTransaction", mock.Anything, mock.AnythingOfType("*gamification.CoinTransaction")).Return(errors.New("tx error"))

	_, err := d.svc.RefundCoins(ctx, tenantID, gcid, 50, "test", nil, nil)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// RefundCoins — event publish error
// ---------------------------------------------------------------------------

func TestRefundCoins_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existingAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		Balance: 100, LifetimeEarned: 300, LifetimeSpent: 200,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(existingAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.coinR.On("CreateTransaction", mock.Anything, mock.AnythingOfType("*gamification.CoinTransaction")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errors.New("pubsub down"))

	_, err := d.svc.RefundCoins(ctx, tenantID, gcid, 50, "test", nil, nil)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// ListTransactions — repo error on ListTransactions
// ---------------------------------------------------------------------------

func TestListTransactions_RepoError(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	accountID := uuid.New()

	existingAccount := &CoinAccount{
		ID: accountID, TenantID: tenantID, GCID: gcid,
		Balance: 100,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(existingAccount, nil)
	d.coinR.On("ListTransactions", mock.Anything, tenantID, accountID, 0, 20).Return(nil, errors.New("db error"))

	_, err := d.svc.ListTransactions(ctx, tenantID, gcid, 0, 20)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// GetBalance — repo error
// ---------------------------------------------------------------------------

func TestGetBalance_RepoError(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(nil, errors.New("db timeout"))

	_, err := d.svc.GetBalance(ctx, tenantID, gcid)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// GetCurrentStanding — repo error
// ---------------------------------------------------------------------------

func TestGetCurrentStanding_RepoError(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(nil, errors.New("db error"))

	_, err := d.svc.GetCurrentStanding(ctx, tenantID, gcid, "2026-W11")
	assert.Error(t, err)
	d.leagueR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// AwardBadge — repo error on GetByGCIDAndTopic
// ---------------------------------------------------------------------------

func TestAwardBadge_RepoError(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	topicID := uuid.New()

	d.badgeR.On("GetByGCIDAndTopic", mock.Anything, tenantID, gcid, topicID).Return(nil, errors.New("db error"))

	_, err := d.svc.AwardBadge(ctx, tenantID, gcid, topicID, BadgeTierGold)
	assert.Error(t, err)
	d.badgeR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// AwardBadge — repo error on Create
// ---------------------------------------------------------------------------

func TestAwardBadge_CreateError(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	topicID := uuid.New()

	d.badgeR.On("GetByGCIDAndTopic", mock.Anything, tenantID, gcid, topicID).Return(nil, nil)
	d.badgeR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.TopicBadge")).Return(errors.New("constraint violation"))

	_, err := d.svc.AwardBadge(ctx, tenantID, gcid, topicID, BadgeTierGold)
	assert.Error(t, err)
	d.badgeR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// AwardBadge — event publish error
// ---------------------------------------------------------------------------

func TestAwardBadge_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	topicID := uuid.New()

	d.badgeR.On("GetByGCIDAndTopic", mock.Anything, tenantID, gcid, topicID).Return(nil, nil)
	d.badgeR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.TopicBadge")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errors.New("pubsub error"))

	_, err := d.svc.AwardBadge(ctx, tenantID, gcid, topicID, BadgeTierGold)
	assert.Error(t, err)
	d.badgeR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// ProcessPromotion — repo error on Upsert
// ---------------------------------------------------------------------------

func TestProcessPromotion_UpsertError(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existing := &League{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LeagueTier: LeagueTierSilver, SeasonWeek: "2026-W11",
		Points: 500, Rank: 1,
	}

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(existing, nil)
	d.leagueR.On("Upsert", mock.Anything, mock.AnythingOfType("*gamification.League")).Return(errors.New("db error"))

	_, err := d.svc.ProcessPromotion(ctx, tenantID, gcid, "2026-W11")
	assert.Error(t, err)
	d.leagueR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// ProcessPromotion — event publish error
// ---------------------------------------------------------------------------

func TestProcessPromotion_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existing := &League{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LeagueTier: LeagueTierSilver, SeasonWeek: "2026-W11",
		Points: 500, Rank: 1,
	}

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(existing, nil)
	d.leagueR.On("Upsert", mock.Anything, mock.AnythingOfType("*gamification.League")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errors.New("pubsub error"))

	_, err := d.svc.ProcessPromotion(ctx, tenantID, gcid, "2026-W11")
	assert.Error(t, err)
	d.leagueR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// ProcessRelegation — repo error on Upsert
// ---------------------------------------------------------------------------

func TestProcessRelegation_UpsertError(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existing := &League{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LeagueTier: LeagueTierGold, SeasonWeek: "2026-W11",
		Points: 10, Rank: 19,
	}

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(existing, nil)
	d.leagueR.On("Upsert", mock.Anything, mock.AnythingOfType("*gamification.League")).Return(errors.New("db error"))

	_, err := d.svc.ProcessRelegation(ctx, tenantID, gcid, "2026-W11")
	assert.Error(t, err)
	d.leagueR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// ProcessRelegation — event publish error
// ---------------------------------------------------------------------------

func TestProcessRelegation_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existing := &League{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LeagueTier: LeagueTierGold, SeasonWeek: "2026-W11",
		Points: 10, Rank: 19,
	}

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(existing, nil)
	d.leagueR.On("Upsert", mock.Anything, mock.AnythingOfType("*gamification.League")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errors.New("pubsub error"))

	_, err := d.svc.ProcessRelegation(ctx, tenantID, gcid, "2026-W11")
	assert.Error(t, err)
	d.leagueR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// RecordPoints — repo error on GetByGCID
// ---------------------------------------------------------------------------

func TestRecordPoints_GetByGCIDError(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(nil, errors.New("db timeout"))

	_, err := d.svc.RecordPoints(ctx, tenantID, gcid, "2026-W11", 50)
	assert.Error(t, err)
	d.leagueR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// ProcessPromotion — repo error on GetByGCID
// ---------------------------------------------------------------------------

func TestProcessPromotion_GetByGCIDError(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(nil, errors.New("db timeout"))

	_, err := d.svc.ProcessPromotion(ctx, tenantID, gcid, "2026-W11")
	assert.Error(t, err)
	d.leagueR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// ProcessRelegation — repo error on GetByGCID
// ---------------------------------------------------------------------------

func TestProcessRelegation_GetByGCIDError(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(nil, errors.New("db timeout"))

	_, err := d.svc.ProcessRelegation(ctx, tenantID, gcid, "2026-W11")
	assert.Error(t, err)
	d.leagueR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// SolveBounty — repo errors
// ---------------------------------------------------------------------------

func TestSolveBounty_GetByIDError(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	solverGCID := uuid.New()

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(nil, errors.New("db error"))

	_, err := d.svc.SolveBounty(ctx, bountyID, solverGCID, "Solution")
	assert.Error(t, err)
	d.bountyR.AssertExpectations(t)
}

func TestSolveBounty_UpdateError(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	posterGCID := uuid.New()
	solverGCID := uuid.New()
	tenantID := uuid.New()

	bounty := &KnowledgeBounty{
		ID: bountyID, TenantID: tenantID, PosterGCID: posterGCID,
		Status: BountyStatusOpen, CoinReward: 100,
	}

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(bounty, nil)
	d.bountyR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.KnowledgeBounty")).Return(errors.New("db error"))

	_, err := d.svc.SolveBounty(ctx, bountyID, solverGCID, "Solution")
	assert.Error(t, err)
	d.bountyR.AssertExpectations(t)
}

func TestSolveBounty_CoinRepoError(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	posterGCID := uuid.New()
	solverGCID := uuid.New()
	tenantID := uuid.New()

	bounty := &KnowledgeBounty{
		ID: bountyID, TenantID: tenantID, PosterGCID: posterGCID,
		Status: BountyStatusOpen, CoinReward: 100,
	}

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(bounty, nil)
	d.bountyR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.KnowledgeBounty")).Return(nil)
	d.coinR.On("GetByGCID", mock.Anything, tenantID, solverGCID).Return(nil, errors.New("db error"))

	_, err := d.svc.SolveBounty(ctx, bountyID, solverGCID, "Solution")
	assert.Error(t, err)
	d.bountyR.AssertExpectations(t)
	d.coinR.AssertExpectations(t)
}

func TestSolveBounty_UpdateBalanceError(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	posterGCID := uuid.New()
	solverGCID := uuid.New()
	tenantID := uuid.New()

	bounty := &KnowledgeBounty{
		ID: bountyID, TenantID: tenantID, PosterGCID: posterGCID,
		Status: BountyStatusOpen, CoinReward: 100,
	}

	solverAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: solverGCID,
		Balance: 50, LifetimeEarned: 200,
	}

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(bounty, nil)
	d.bountyR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.KnowledgeBounty")).Return(nil)
	d.coinR.On("GetByGCID", mock.Anything, tenantID, solverGCID).Return(solverAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(errors.New("db error"))

	_, err := d.svc.SolveBounty(ctx, bountyID, solverGCID, "Solution")
	assert.Error(t, err)
	d.bountyR.AssertExpectations(t)
	d.coinR.AssertExpectations(t)
}

func TestSolveBounty_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	posterGCID := uuid.New()
	solverGCID := uuid.New()
	tenantID := uuid.New()

	bounty := &KnowledgeBounty{
		ID: bountyID, TenantID: tenantID, PosterGCID: posterGCID,
		Status: BountyStatusOpen, CoinReward: 100,
	}

	solverAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: solverGCID,
		Balance: 50, LifetimeEarned: 200,
	}

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(bounty, nil)
	d.bountyR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.KnowledgeBounty")).Return(nil)
	d.coinR.On("GetByGCID", mock.Anything, tenantID, solverGCID).Return(solverAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errors.New("pubsub error"))

	_, err := d.svc.SolveBounty(ctx, bountyID, solverGCID, "Solution")
	assert.Error(t, err)
	d.bountyR.AssertExpectations(t)
	d.coinR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// CreateBounty — repo errors
// ---------------------------------------------------------------------------

func TestCreateBounty_GetByGCIDError(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	tenantID := uuid.New()
	posterGCID := uuid.New()
	atomID := uuid.New()
	expiresAt := time.Now().Add(72 * time.Hour)

	d.coinR.On("GetByGCID", mock.Anything, tenantID, posterGCID).Return(nil, errors.New("db error"))

	_, err := d.svc.CreateBounty(ctx, tenantID, posterGCID, atomID, "Title", "desc", 100, 0, expiresAt)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
}

func TestCreateBounty_UpdateBalanceError(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	tenantID := uuid.New()
	posterGCID := uuid.New()
	atomID := uuid.New()
	expiresAt := time.Now().Add(72 * time.Hour)

	posterAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: posterGCID,
		Balance: 500, LifetimeEarned: 1000, LifetimeSpent: 500,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, posterGCID).Return(posterAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(errors.New("db error"))

	_, err := d.svc.CreateBounty(ctx, tenantID, posterGCID, atomID, "Title", "desc", 100, 0, expiresAt)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
}

func TestCreateBounty_RepoCreateError(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	tenantID := uuid.New()
	posterGCID := uuid.New()
	atomID := uuid.New()
	expiresAt := time.Now().Add(72 * time.Hour)

	posterAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: posterGCID,
		Balance: 500, LifetimeEarned: 1000, LifetimeSpent: 500,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, posterGCID).Return(posterAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.bountyR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.KnowledgeBounty")).Return(errors.New("constraint violation"))

	_, err := d.svc.CreateBounty(ctx, tenantID, posterGCID, atomID, "Title", "desc", 100, 0, expiresAt)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
	d.bountyR.AssertExpectations(t)
}

func TestCreateBounty_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	tenantID := uuid.New()
	posterGCID := uuid.New()
	atomID := uuid.New()
	expiresAt := time.Now().Add(72 * time.Hour)

	posterAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: posterGCID,
		Balance: 500, LifetimeEarned: 1000, LifetimeSpent: 500,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, posterGCID).Return(posterAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.bountyR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.KnowledgeBounty")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errors.New("pubsub error"))

	_, err := d.svc.CreateBounty(ctx, tenantID, posterGCID, atomID, "Title", "desc", 100, 0, expiresAt)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
	d.bountyR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// CancelBounty — repo errors
// ---------------------------------------------------------------------------

func TestCancelBounty_GetByIDError(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	posterGCID := uuid.New()

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(nil, errors.New("db error"))

	err := d.svc.CancelBounty(ctx, bountyID, posterGCID)
	assert.Error(t, err)
	d.bountyR.AssertExpectations(t)
}

func TestCancelBounty_UpdateError(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	posterGCID := uuid.New()
	tenantID := uuid.New()

	bounty := &KnowledgeBounty{
		ID: bountyID, TenantID: tenantID, PosterGCID: posterGCID,
		Status: BountyStatusOpen, CoinReward: 100,
	}

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(bounty, nil)
	d.bountyR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.KnowledgeBounty")).Return(errors.New("db error"))

	err := d.svc.CancelBounty(ctx, bountyID, posterGCID)
	assert.Error(t, err)
	d.bountyR.AssertExpectations(t)
}

func TestCancelBounty_CoinGetByGCIDError(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	posterGCID := uuid.New()
	tenantID := uuid.New()

	bounty := &KnowledgeBounty{
		ID: bountyID, TenantID: tenantID, PosterGCID: posterGCID,
		Status: BountyStatusOpen, CoinReward: 100,
	}

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(bounty, nil)
	d.bountyR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.KnowledgeBounty")).Return(nil)
	d.coinR.On("GetByGCID", mock.Anything, tenantID, posterGCID).Return(nil, errors.New("db error"))

	err := d.svc.CancelBounty(ctx, bountyID, posterGCID)
	assert.Error(t, err)
	d.bountyR.AssertExpectations(t)
	d.coinR.AssertExpectations(t)
}

func TestCancelBounty_UpdateBalanceError(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	posterGCID := uuid.New()
	tenantID := uuid.New()

	bounty := &KnowledgeBounty{
		ID: bountyID, TenantID: tenantID, PosterGCID: posterGCID,
		Status: BountyStatusOpen, CoinReward: 100,
	}

	posterAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: posterGCID,
		Balance: 200,
	}

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(bounty, nil)
	d.bountyR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.KnowledgeBounty")).Return(nil)
	d.coinR.On("GetByGCID", mock.Anything, tenantID, posterGCID).Return(posterAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(errors.New("db error"))

	err := d.svc.CancelBounty(ctx, bountyID, posterGCID)
	assert.Error(t, err)
	d.bountyR.AssertExpectations(t)
	d.coinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// GetBounty — repo error
// ---------------------------------------------------------------------------

func TestGetBounty_RepoError(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(nil, errors.New("db error"))

	_, err := d.svc.GetBounty(ctx, bountyID)
	assert.Error(t, err)
	d.bountyR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// EquipSkin — repo error on EquipSlot
// ---------------------------------------------------------------------------

func TestEquipSkin_EquipSlotError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	skinAwardID := uuid.Must(uuid.NewV7())

	d.equipR.On("EquipSlot", mock.Anything, mock.AnythingOfType("*gamification.SkinEquipment")).Return(errors.New("constraint violation"))

	_, err := d.svc.EquipSkin(ctx, tenantID, gcid, EquipmentSlotProfilePhoto, skinAwardID)
	assert.Error(t, err)
	d.equipR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// EquipSkin — event publish error
// ---------------------------------------------------------------------------

func TestEquipSkin_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	skinAwardID := uuid.Must(uuid.NewV7())

	d.equipR.On("EquipSlot", mock.Anything, mock.AnythingOfType("*gamification.SkinEquipment")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errors.New("pubsub error"))

	_, err := d.svc.EquipSkin(ctx, tenantID, gcid, EquipmentSlotProfilePhoto, skinAwardID)
	assert.Error(t, err)
	d.equipR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// UnequipSkin — repo error on UnequipSlot
// ---------------------------------------------------------------------------

func TestUnequipSkin_UnequipSlotError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	d.equipR.On("UnequipSlot", mock.Anything, tenantID, gcid, EquipmentSlotProfilePhoto).Return(errors.New("db error"))

	err := d.svc.UnequipSkin(ctx, tenantID, gcid, EquipmentSlotProfilePhoto)
	assert.Error(t, err)
	d.equipR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// UnequipSkin — event publish error
// ---------------------------------------------------------------------------

func TestUnequipSkin_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	d.equipR.On("UnequipSlot", mock.Anything, tenantID, gcid, EquipmentSlotProfilePhoto).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errors.New("pubsub error"))

	err := d.svc.UnequipSkin(ctx, tenantID, gcid, EquipmentSlotProfilePhoto)
	assert.Error(t, err)
	d.equipR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// ExportSkin — repo error on Create
// ---------------------------------------------------------------------------

func TestExportSkin_CreateError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	skinAwardID := uuid.Must(uuid.NewV7())

	d.exportR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.SkinExport")).Return(errors.New("db error"))

	_, err := d.svc.ExportSkin(ctx, tenantID, gcid, skinAwardID, ExportTargetZoom)
	assert.Error(t, err)
	d.exportR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// ExportSkin — event publish error
// ---------------------------------------------------------------------------

func TestExportSkin_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	skinAwardID := uuid.Must(uuid.NewV7())

	d.exportR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.SkinExport")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errors.New("pubsub error"))

	_, err := d.svc.ExportSkin(ctx, tenantID, gcid, skinAwardID, ExportTargetZoom)
	assert.Error(t, err)
	d.exportR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// CreateSkin — repo error on Create
// ---------------------------------------------------------------------------

func TestCreateSkin_RepoCreateError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()

	input := &DigitalSkin{
		SkinCode: "SKN-ERR-001",
		Rarity:   SkinRarityCommon,
	}

	d.skinR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.DigitalSkin")).Return(errors.New("db error"))

	_, err := d.svc.CreateSkin(ctx, input)
	assert.Error(t, err)
	d.skinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// CreateSkin — event publish error
// ---------------------------------------------------------------------------

func TestCreateSkin_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()

	input := &DigitalSkin{
		SkinCode: "SKN-PUB-001",
		Rarity:   SkinRarityCommon,
	}

	d.skinR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.DigitalSkin")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errors.New("pubsub error"))

	_, err := d.svc.CreateSkin(ctx, input)
	assert.Error(t, err)
	d.skinR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// UpdateSkin — repo error on Update
// ---------------------------------------------------------------------------

func TestUpdateSkin_RepoUpdateError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()

	input := &DigitalSkin{
		ID:       uuid.Must(uuid.NewV7()),
		SkinCode: "SKN-ERR-002",
		Rarity:   SkinRarityCommon,
	}

	d.skinR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.DigitalSkin")).Return(errors.New("db error"))

	_, err := d.svc.UpdateSkin(ctx, input)
	assert.Error(t, err)
	d.skinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// UpdateSkin — event publish error
// ---------------------------------------------------------------------------

func TestUpdateSkin_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()

	input := &DigitalSkin{
		ID:       uuid.Must(uuid.NewV7()),
		SkinCode: "SKN-PUB-002",
		Rarity:   SkinRarityCommon,
	}

	d.skinR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.DigitalSkin")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errors.New("pubsub error"))

	_, err := d.svc.UpdateSkin(ctx, input)
	assert.Error(t, err)
	d.skinR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// DeleteSkin — event publish error
// ---------------------------------------------------------------------------

func TestDeleteSkin_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()
	skinID := uuid.Must(uuid.NewV7())

	d.skinR.On("Delete", mock.Anything, skinID).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errors.New("pubsub error"))

	err := d.svc.DeleteSkin(ctx, skinID)
	assert.Error(t, err)
	d.skinR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// TransferLegendarySkin — repo errors
// ---------------------------------------------------------------------------

func TestTransferLegendarySkin_GetBySkinIDError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()
	tenantID := uuid.Must(uuid.NewV7())
	skinID := uuid.Must(uuid.NewV7())
	newHolder := uuid.Must(uuid.NewV7())

	d.legendaryR.On("GetBySkinID", mock.Anything, tenantID, skinID).Return(nil, errors.New("db error"))

	_, err := d.svc.TransferLegendarySkin(ctx, tenantID, skinID, newHolder, "reason")
	assert.Error(t, err)
	d.legendaryR.AssertExpectations(t)
}

func TestTransferLegendarySkin_TransferHolderError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()
	tenantID := uuid.Must(uuid.NewV7())
	skinID := uuid.Must(uuid.NewV7())
	currentHolder := uuid.Must(uuid.NewV7())
	newHolder := uuid.Must(uuid.NewV7())

	legendary := &LegendarySkin{
		ID:                uuid.Must(uuid.NewV7()),
		TenantID:          tenantID,
		SkinID:            skinID,
		CurrentHolderGCID: currentHolder,
		HeldSince:         time.Now().Add(-7 * 24 * time.Hour),
		TransferReason:    "Previous champion",
		PreviousHolders:   []map[string]interface{}{},
	}

	d.legendaryR.On("GetBySkinID", mock.Anything, tenantID, skinID).Return(legendary, nil)
	d.legendaryR.On("TransferHolder", mock.Anything, mock.AnythingOfType("*gamification.LegendarySkin")).Return(errors.New("db error"))

	_, err := d.svc.TransferLegendarySkin(ctx, tenantID, skinID, newHolder, "reason")
	assert.Error(t, err)
	d.legendaryR.AssertExpectations(t)
}

func TestTransferLegendarySkin_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()
	tenantID := uuid.Must(uuid.NewV7())
	skinID := uuid.Must(uuid.NewV7())
	currentHolder := uuid.Must(uuid.NewV7())
	newHolder := uuid.Must(uuid.NewV7())

	legendary := &LegendarySkin{
		ID:                uuid.Must(uuid.NewV7()),
		TenantID:          tenantID,
		SkinID:            skinID,
		CurrentHolderGCID: currentHolder,
		HeldSince:         time.Now().Add(-7 * 24 * time.Hour),
		TransferReason:    "Previous champion",
		PreviousHolders:   []map[string]interface{}{},
	}

	d.legendaryR.On("GetBySkinID", mock.Anything, tenantID, skinID).Return(legendary, nil)
	d.legendaryR.On("TransferHolder", mock.Anything, mock.AnythingOfType("*gamification.LegendarySkin")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errors.New("pubsub error"))

	_, err := d.svc.TransferLegendarySkin(ctx, tenantID, skinID, newHolder, "reason")
	assert.Error(t, err)
	d.legendaryR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// GetLegendarySkin — repo error
// ---------------------------------------------------------------------------

func TestGetLegendarySkin_RepoError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()
	tenantID := uuid.Must(uuid.NewV7())
	skinID := uuid.Must(uuid.NewV7())

	d.legendaryR.On("GetBySkinID", mock.Anything, tenantID, skinID).Return(nil, errors.New("db error"))

	_, err := d.svc.GetLegendarySkin(ctx, tenantID, skinID)
	assert.Error(t, err)
	d.legendaryR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// GetSkin — repo error
// ---------------------------------------------------------------------------

func TestGetSkin_RepoError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()
	skinID := uuid.Must(uuid.NewV7())

	d.skinR.On("GetByID", mock.Anything, skinID).Return(nil, errors.New("db error"))

	_, err := d.svc.GetSkin(ctx, skinID)
	assert.Error(t, err)
	d.skinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// AwardSkin — repo error on GetByID
// ---------------------------------------------------------------------------

func TestAwardSkin_GetByIDError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	skinID := uuid.Must(uuid.NewV7())

	d.skinR.On("GetByID", mock.Anything, skinID).Return(nil, errors.New("db error"))

	_, err := d.svc.AwardSkin(ctx, tenantID, gcid, skinID, SkinEarnedViaStreak, nil)
	assert.Error(t, err)
	d.skinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// AwardSkin — event publish error
// ---------------------------------------------------------------------------

func TestAwardSkin_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestSkinService()
	ctx := context.Background()
	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	skinID := uuid.Must(uuid.NewV7())

	d.skinR.On("GetByID", mock.Anything, skinID).Return(&DigitalSkin{
		ID: skinID, SkinCode: "SKN-001",
	}, nil)
	d.awardR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.SkinAward")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errors.New("pubsub error"))

	_, err := d.svc.AwardSkin(ctx, tenantID, gcid, skinID, SkinEarnedViaStreak, nil)
	assert.Error(t, err)
	d.skinR.AssertExpectations(t)
	d.awardR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// SolveBounty — CreateIfNotExists error for new solver account
// ---------------------------------------------------------------------------

func TestSolveBounty_CreateIfNotExistsError(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	posterGCID := uuid.New()
	solverGCID := uuid.New()
	tenantID := uuid.New()

	bounty := &KnowledgeBounty{
		ID: bountyID, TenantID: tenantID, PosterGCID: posterGCID,
		Status: BountyStatusOpen, CoinReward: 75,
	}

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(bounty, nil)
	d.bountyR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.KnowledgeBounty")).Return(nil)
	d.coinR.On("GetByGCID", mock.Anything, tenantID, solverGCID).Return(nil, nil)
	d.coinR.On("CreateIfNotExists", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(errors.New("constraint"))

	_, err := d.svc.SolveBounty(ctx, bountyID, solverGCID, "Solution text")
	assert.Error(t, err)
	d.bountyR.AssertExpectations(t)
	d.coinR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// EarnCoins — CreateIfNotExists error for new account
// ---------------------------------------------------------------------------

func TestEarnCoins_CreateIfNotExistsError(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(nil, nil)
	d.coinR.On("CreateIfNotExists", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(errors.New("constraint"))

	_, err := d.svc.EarnCoins(ctx, tenantID, gcid, 50, "test", nil, nil)
	assert.Error(t, err)
	d.coinR.AssertExpectations(t)
}
