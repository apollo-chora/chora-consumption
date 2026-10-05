package gamification

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
)

// Compile-time interface checks.
var (
	_ DigitalSkinRepository              = (*mockSkinRepo)(nil)
	_ SkinAwardRepository                = (*mockAwardRepo)(nil)
	_ SkinEquipmentRepository            = (*mockEquipmentRepo)(nil)
	_ SkinExportRepository               = (*mockExportRepo)(nil)
	_ LegendarySkinRepository            = (*mockLegendaryRepo)(nil)
	_ CoinAccountRepository              = (*mockCoinRepo)(nil)
	_ LeagueRepository                   = (*mockLeagueRepo)(nil)
	_ TopicBadgeRepository               = (*mockBadgeRepo)(nil)
	_ KnowledgeBountyRepository          = (*mockBountyRepo)(nil)
	_ EconomyConfigRepository            = (*mockEconomyConfigRepo)(nil)
	_ EventPublisher                     = (*mockEventPublisher)(nil)
	_ TradingCardRepository              = (*mockTradingCardRepo)(nil)
	_ CardInstanceRepository             = (*mockCardInstanceRepo)(nil)
	_ CardSetRepository                  = (*mockCardSetRepo)(nil)
	_ CardTradeRepository                = (*mockCardTradeRepo)(nil)
	_ CardTradeHistoryRepository         = (*mockCardTradeHistoryRepo)(nil)
	_ CardSetCompletionRepository        = (*mockCardSetCompletionRepo)(nil)
	_ EconomySnapshotRepository          = (*mockEconomySnapshotRepo)(nil)
	_ BossChallengeRepository            = (*mockBossChallengeRepo)(nil)
	_ BossChallengeParticipantRepository = (*mockBossChallengeParticipantRepo)(nil)
	_ MaterialRepository                 = (*mockMaterialRepo)(nil)
	_ MaterialInventoryRepository        = (*mockMaterialInventoryRepo)(nil)
	_ CraftingRecipeRepository           = (*mockCraftingRecipeRepo)(nil)
	_ LootboxRepository                  = (*mockLootboxRepo)(nil)
	_ LootTableRepository                = (*mockLootTableRepo)(nil)
	_ LoginCalendarRepository            = (*mockLoginCalendarRepo)(nil)

	// Phase 60.5 — Territory Rewards
	_ TerritoryIncomeRepository = (*mockTerritoryIncomeRepo)(nil)
	_ SeasonalRewardRepository  = (*mockSeasonalRewardRepo)(nil)

	// Phase 60.8 — Marketplace + Converters + Social Economy
	_ MarketListingRepository         = (*mockMarketListingRepo)(nil)
	_ MarketOrderRepository           = (*mockMarketOrderRepo)(nil)
	_ MarketTransactionRepository     = (*mockMarketTransactionRepo)(nil)
	_ CurrencyConversionRepository    = (*mockCurrencyConversionRepo)(nil)
	_ CurrencyTransferRepository      = (*mockCurrencyTransferRepo)(nil)
	_ GroupFundPoolRepository         = (*mockGroupFundPoolRepo)(nil)
	_ GroupFundContributionRepository = (*mockGroupFundContributionRepo)(nil)
)

// ---------------------------------------------------------------------------
// mockSkinRepo — DigitalSkinRepository
// ---------------------------------------------------------------------------

type mockSkinRepo struct{ mock.Mock }

func (m *mockSkinRepo) Create(ctx context.Context, skin *DigitalSkin) error {
	return m.Called(ctx, skin).Error(0)
}

func (m *mockSkinRepo) GetByID(ctx context.Context, id uuid.UUID) (*DigitalSkin, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*DigitalSkin), args.Error(1)
}

func (m *mockSkinRepo) List(ctx context.Context, tenantID *uuid.UUID, rarity *SkinRarity, category *string, offset, limit int) ([]*DigitalSkin, error) {
	args := m.Called(ctx, tenantID, rarity, category, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*DigitalSkin), args.Error(1)
}

func (m *mockSkinRepo) Update(ctx context.Context, skin *DigitalSkin) error {
	return m.Called(ctx, skin).Error(0)
}

func (m *mockSkinRepo) Delete(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

// ---------------------------------------------------------------------------
// mockAwardRepo — SkinAwardRepository
// ---------------------------------------------------------------------------

type mockAwardRepo struct{ mock.Mock }

func (m *mockAwardRepo) Create(ctx context.Context, award *SkinAward) error {
	return m.Called(ctx, award).Error(0)
}

func (m *mockAwardRepo) GetByID(ctx context.Context, id uuid.UUID) (*SkinAward, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*SkinAward), args.Error(1)
}

func (m *mockAwardRepo) ListByGCID(ctx context.Context, tenantID, gcid uuid.UUID, offset, limit int) ([]*SkinAward, error) {
	args := m.Called(ctx, tenantID, gcid, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*SkinAward), args.Error(1)
}

func (m *mockAwardRepo) ListBySkin(ctx context.Context, tenantID, skinID uuid.UUID, offset, limit int) ([]*SkinAward, error) {
	args := m.Called(ctx, tenantID, skinID, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*SkinAward), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockEquipmentRepo — SkinEquipmentRepository
// ---------------------------------------------------------------------------

type mockEquipmentRepo struct{ mock.Mock }

func (m *mockEquipmentRepo) GetByGCID(ctx context.Context, tenantID, gcid uuid.UUID) ([]*SkinEquipment, error) {
	args := m.Called(ctx, tenantID, gcid)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*SkinEquipment), args.Error(1)
}

func (m *mockEquipmentRepo) EquipSlot(ctx context.Context, equipment *SkinEquipment) error {
	return m.Called(ctx, equipment).Error(0)
}

func (m *mockEquipmentRepo) UnequipSlot(ctx context.Context, tenantID, gcid uuid.UUID, slot EquipmentSlot) error {
	return m.Called(ctx, tenantID, gcid, slot).Error(0)
}

// ---------------------------------------------------------------------------
// mockExportRepo — SkinExportRepository
// ---------------------------------------------------------------------------

type mockExportRepo struct{ mock.Mock }

func (m *mockExportRepo) Create(ctx context.Context, export *SkinExport) error {
	return m.Called(ctx, export).Error(0)
}

func (m *mockExportRepo) ListByGCID(ctx context.Context, tenantID, gcid uuid.UUID, offset, limit int) ([]*SkinExport, error) {
	args := m.Called(ctx, tenantID, gcid, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*SkinExport), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockLegendaryRepo — LegendarySkinRepository
// ---------------------------------------------------------------------------

type mockLegendaryRepo struct{ mock.Mock }

func (m *mockLegendaryRepo) GetBySkinID(ctx context.Context, tenantID, skinID uuid.UUID) (*LegendarySkin, error) {
	args := m.Called(ctx, tenantID, skinID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*LegendarySkin), args.Error(1)
}

func (m *mockLegendaryRepo) List(ctx context.Context, tenantID uuid.UUID, offset, limit int) ([]*LegendarySkin, error) {
	args := m.Called(ctx, tenantID, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*LegendarySkin), args.Error(1)
}

func (m *mockLegendaryRepo) TransferHolder(ctx context.Context, legendary *LegendarySkin) error {
	return m.Called(ctx, legendary).Error(0)
}

// ---------------------------------------------------------------------------
// mockCoinRepo — CoinAccountRepository
// ---------------------------------------------------------------------------

type mockCoinRepo struct{ mock.Mock }

func (m *mockCoinRepo) GetByGCID(ctx context.Context, tenantID, gcid uuid.UUID) (*CoinAccount, error) {
	args := m.Called(ctx, tenantID, gcid)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*CoinAccount), args.Error(1)
}

func (m *mockCoinRepo) CreateIfNotExists(ctx context.Context, account *CoinAccount) error {
	return m.Called(ctx, account).Error(0)
}

func (m *mockCoinRepo) UpdateBalance(ctx context.Context, account *CoinAccount) error {
	return m.Called(ctx, account).Error(0)
}

func (m *mockCoinRepo) CreateTransaction(ctx context.Context, tx *CoinTransaction) error {
	return m.Called(ctx, tx).Error(0)
}

func (m *mockCoinRepo) ListTransactions(ctx context.Context, tenantID, coinAccountID uuid.UUID, offset, limit int) ([]*CoinTransaction, error) {
	args := m.Called(ctx, tenantID, coinAccountID, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*CoinTransaction), args.Error(1)
}

func (m *mockCoinRepo) SumBalances(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	args := m.Called(ctx, tenantID)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockCoinRepo) CountActiveAccounts(ctx context.Context, tenantID uuid.UUID) (int, error) {
	args := m.Called(ctx, tenantID)
	return args.Int(0), args.Error(1)
}

func (m *mockCoinRepo) SumDailyMinted(ctx context.Context, tenantID uuid.UUID, day time.Time) (int64, error) {
	args := m.Called(ctx, tenantID, day)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockCoinRepo) SumDailyDrained(ctx context.Context, tenantID uuid.UUID, day time.Time) (int64, error) {
	args := m.Called(ctx, tenantID, day)
	return args.Get(0).(int64), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockLeagueRepo — LeagueRepository
// ---------------------------------------------------------------------------

type mockLeagueRepo struct{ mock.Mock }

func (m *mockLeagueRepo) GetByGCID(ctx context.Context, tenantID, gcid uuid.UUID, seasonWeek string) (*League, error) {
	args := m.Called(ctx, tenantID, gcid, seasonWeek)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*League), args.Error(1)
}

func (m *mockLeagueRepo) ListStandings(ctx context.Context, tenantID uuid.UUID, tier LeagueTier, seasonWeek string, offset, limit int) ([]*League, error) {
	args := m.Called(ctx, tenantID, tier, seasonWeek, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*League), args.Error(1)
}

func (m *mockLeagueRepo) Upsert(ctx context.Context, league *League) error {
	return m.Called(ctx, league).Error(0)
}

// ---------------------------------------------------------------------------
// mockBadgeRepo — TopicBadgeRepository
// ---------------------------------------------------------------------------

type mockBadgeRepo struct{ mock.Mock }

func (m *mockBadgeRepo) GetByGCIDAndTopic(ctx context.Context, tenantID, gcid, topicID uuid.UUID) (*TopicBadge, error) {
	args := m.Called(ctx, tenantID, gcid, topicID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*TopicBadge), args.Error(1)
}

func (m *mockBadgeRepo) ListByGCID(ctx context.Context, tenantID, gcid uuid.UUID, offset, limit int) ([]*TopicBadge, error) {
	args := m.Called(ctx, tenantID, gcid, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*TopicBadge), args.Error(1)
}

func (m *mockBadgeRepo) Create(ctx context.Context, badge *TopicBadge) error {
	return m.Called(ctx, badge).Error(0)
}

// ---------------------------------------------------------------------------
// mockBountyRepo — KnowledgeBountyRepository
// ---------------------------------------------------------------------------

type mockBountyRepo struct{ mock.Mock }

func (m *mockBountyRepo) Create(ctx context.Context, bounty *KnowledgeBounty) error {
	return m.Called(ctx, bounty).Error(0)
}

func (m *mockBountyRepo) GetByID(ctx context.Context, id uuid.UUID) (*KnowledgeBounty, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*KnowledgeBounty), args.Error(1)
}

func (m *mockBountyRepo) List(ctx context.Context, tenantID uuid.UUID, status *BountyStatus, offset, limit int) ([]*KnowledgeBounty, error) {
	args := m.Called(ctx, tenantID, status, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*KnowledgeBounty), args.Error(1)
}

func (m *mockBountyRepo) Update(ctx context.Context, bounty *KnowledgeBounty) error {
	return m.Called(ctx, bounty).Error(0)
}

func (m *mockBountyRepo) Delete(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

// ---------------------------------------------------------------------------
// mockEconomyConfigRepo — EconomyConfigRepository
// ---------------------------------------------------------------------------

type mockEconomyConfigRepo struct{ mock.Mock }

func (m *mockEconomyConfigRepo) GetByID(ctx context.Context, id uuid.UUID) (*EconomyConfig, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*EconomyConfig), args.Error(1)
}

func (m *mockEconomyConfigRepo) GetEffective(ctx context.Context, tenantID uuid.UUID, category ConfigCategory) ([]*EconomyConfig, error) {
	args := m.Called(ctx, tenantID, category)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*EconomyConfig), args.Error(1)
}

func (m *mockEconomyConfigRepo) GetByTenantCategoryKey(ctx context.Context, tenantID *uuid.UUID, category ConfigCategory, key string) (*EconomyConfig, error) {
	args := m.Called(ctx, tenantID, category, key)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*EconomyConfig), args.Error(1)
}

func (m *mockEconomyConfigRepo) Create(ctx context.Context, config *EconomyConfig) error {
	return m.Called(ctx, config).Error(0)
}

func (m *mockEconomyConfigRepo) Update(ctx context.Context, config *EconomyConfig) error {
	return m.Called(ctx, config).Error(0)
}

func (m *mockEconomyConfigRepo) Delete(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

func (m *mockEconomyConfigRepo) GetHistory(ctx context.Context, configID uuid.UUID, limit int) ([]*EconomyConfigHistory, error) {
	args := m.Called(ctx, configID, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*EconomyConfigHistory), args.Error(1)
}

func (m *mockEconomyConfigRepo) CreateHistoryEntry(ctx context.Context, entry *EconomyConfigHistory) error {
	return m.Called(ctx, entry).Error(0)
}

// ---------------------------------------------------------------------------
// mockEventPublisher — EventPublisher
// ---------------------------------------------------------------------------

type mockEventPublisher struct{ mock.Mock }

func (m *mockEventPublisher) Publish(ctx context.Context, topic string, event interface{}) error {
	return m.Called(ctx, topic, event).Error(0)
}

func (m *mockEventPublisher) Close() error {
	return m.Called().Error(0)
}

// ---------------------------------------------------------------------------
// mockTradingCardRepo — TradingCardRepository
// ---------------------------------------------------------------------------

type mockTradingCardRepo struct{ mock.Mock }

func (m *mockTradingCardRepo) Create(ctx context.Context, card *TradingCard) error {
	return m.Called(ctx, card).Error(0)
}

func (m *mockTradingCardRepo) GetByID(ctx context.Context, id uuid.UUID) (*TradingCard, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*TradingCard), args.Error(1)
}

func (m *mockTradingCardRepo) List(ctx context.Context, tenantID uuid.UUID, rarity *CardRarity, archetype *CardArchetype, setID *uuid.UUID, offset, limit int) ([]*TradingCard, error) {
	args := m.Called(ctx, tenantID, rarity, archetype, setID, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*TradingCard), args.Error(1)
}

func (m *mockTradingCardRepo) Update(ctx context.Context, card *TradingCard) error {
	return m.Called(ctx, card).Error(0)
}

func (m *mockTradingCardRepo) Delete(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

// ---------------------------------------------------------------------------
// mockCardInstanceRepo — CardInstanceRepository
// ---------------------------------------------------------------------------

type mockCardInstanceRepo struct{ mock.Mock }

func (m *mockCardInstanceRepo) Create(ctx context.Context, instance *TradingCardInstance) error {
	return m.Called(ctx, instance).Error(0)
}

func (m *mockCardInstanceRepo) GetByID(ctx context.Context, id uuid.UUID) (*TradingCardInstance, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*TradingCardInstance), args.Error(1)
}

func (m *mockCardInstanceRepo) ListByOwner(ctx context.Context, tenantID, gcid uuid.UUID, cursor *uuid.UUID, limit int) ([]*TradingCardInstance, error) {
	args := m.Called(ctx, tenantID, gcid, cursor, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*TradingCardInstance), args.Error(1)
}

func (m *mockCardInstanceRepo) Update(ctx context.Context, instance *TradingCardInstance) error {
	return m.Called(ctx, instance).Error(0)
}

func (m *mockCardInstanceRepo) NextSerialNumber(ctx context.Context, cardID uuid.UUID) (int, error) {
	args := m.Called(ctx, cardID)
	return args.Int(0), args.Error(1)
}

func (m *mockCardInstanceRepo) CountDistinctCardsInSet(ctx context.Context, tenantID, gcid, setID uuid.UUID) (int, error) {
	args := m.Called(ctx, tenantID, gcid, setID)
	return args.Int(0), args.Error(1)
}

func (m *mockCardInstanceRepo) SwapOwnership(ctx context.Context, instanceID, newOwnerGCID uuid.UUID) error {
	return m.Called(ctx, instanceID, newOwnerGCID).Error(0)
}

func (m *mockCardInstanceRepo) ReturnToCollection(ctx context.Context, instanceID uuid.UUID) error {
	return m.Called(ctx, instanceID).Error(0)
}

// ---------------------------------------------------------------------------
// mockCardSetRepo — CardSetRepository
// ---------------------------------------------------------------------------

type mockCardSetRepo struct{ mock.Mock }

func (m *mockCardSetRepo) Create(ctx context.Context, set *CardSet) error {
	return m.Called(ctx, set).Error(0)
}

func (m *mockCardSetRepo) GetByID(ctx context.Context, id uuid.UUID) (*CardSet, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*CardSet), args.Error(1)
}

func (m *mockCardSetRepo) List(ctx context.Context, tenantID uuid.UUID, offset, limit int) ([]*CardSet, error) {
	args := m.Called(ctx, tenantID, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*CardSet), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockCardTradeRepo — CardTradeRepository
// ---------------------------------------------------------------------------

type mockCardTradeRepo struct{ mock.Mock }

func (m *mockCardTradeRepo) Create(ctx context.Context, trade *CardTrade) error {
	return m.Called(ctx, trade).Error(0)
}

func (m *mockCardTradeRepo) GetByID(ctx context.Context, id uuid.UUID) (*CardTrade, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*CardTrade), args.Error(1)
}

func (m *mockCardTradeRepo) ListByGCID(ctx context.Context, tenantID, gcid uuid.UUID, status *TradeStatus, offset, limit int) ([]*CardTrade, error) {
	args := m.Called(ctx, tenantID, gcid, status, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*CardTrade), args.Error(1)
}

func (m *mockCardTradeRepo) Update(ctx context.Context, trade *CardTrade) error {
	return m.Called(ctx, trade).Error(0)
}

// ---------------------------------------------------------------------------
// mockCardTradeHistoryRepo — CardTradeHistoryRepository
// ---------------------------------------------------------------------------

type mockCardTradeHistoryRepo struct{ mock.Mock }

func (m *mockCardTradeHistoryRepo) Create(ctx context.Context, entry *CardTradeHistory) error {
	return m.Called(ctx, entry).Error(0)
}

func (m *mockCardTradeHistoryRepo) ListByTrade(ctx context.Context, tradeID uuid.UUID) ([]*CardTradeHistory, error) {
	args := m.Called(ctx, tradeID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*CardTradeHistory), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockCardSetCompletionRepo — CardSetCompletionRepository
// ---------------------------------------------------------------------------

type mockCardSetCompletionRepo struct{ mock.Mock }

func (m *mockCardSetCompletionRepo) Create(ctx context.Context, completion *CardSetCompletion) error {
	return m.Called(ctx, completion).Error(0)
}

func (m *mockCardSetCompletionRepo) GetByGCIDAndSet(ctx context.Context, tenantID, gcid, setID uuid.UUID) (*CardSetCompletion, error) {
	args := m.Called(ctx, tenantID, gcid, setID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*CardSetCompletion), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockEconomySnapshotRepo — EconomySnapshotRepository
// ---------------------------------------------------------------------------

type mockEconomySnapshotRepo struct{ mock.Mock }

func (m *mockEconomySnapshotRepo) Create(ctx context.Context, snapshot *EconomySnapshot) error {
	return m.Called(ctx, snapshot).Error(0)
}

func (m *mockEconomySnapshotRepo) GetLatestByTenant(ctx context.Context, tenantID uuid.UUID) (*EconomySnapshot, error) {
	args := m.Called(ctx, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*EconomySnapshot), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockBossChallengeRepo — BossChallengeRepository
// ---------------------------------------------------------------------------

type mockBossChallengeRepo struct{ mock.Mock }

func (m *mockBossChallengeRepo) Create(ctx context.Context, challenge *BossChallenge) error {
	return m.Called(ctx, challenge).Error(0)
}

func (m *mockBossChallengeRepo) GetByID(ctx context.Context, id uuid.UUID) (*BossChallenge, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*BossChallenge), args.Error(1)
}

func (m *mockBossChallengeRepo) Update(ctx context.Context, challenge *BossChallenge) error {
	return m.Called(ctx, challenge).Error(0)
}

func (m *mockBossChallengeRepo) ListByStatus(ctx context.Context, tenantID uuid.UUID, status BossChallengeStatus, offset, limit int) ([]*BossChallenge, error) {
	args := m.Called(ctx, tenantID, status, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*BossChallenge), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockBossChallengeParticipantRepo — BossChallengeParticipantRepository
// ---------------------------------------------------------------------------

type mockBossChallengeParticipantRepo struct{ mock.Mock }

func (m *mockBossChallengeParticipantRepo) Upsert(ctx context.Context, participant *BossChallengeParticipant) error {
	return m.Called(ctx, participant).Error(0)
}

func (m *mockBossChallengeParticipantRepo) ListByChallenge(ctx context.Context, challengeID uuid.UUID) ([]*BossChallengeParticipant, error) {
	args := m.Called(ctx, challengeID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*BossChallengeParticipant), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockMaterialRepo — MaterialRepository
// ---------------------------------------------------------------------------

type mockMaterialRepo struct{ mock.Mock }

func (m *mockMaterialRepo) Create(ctx context.Context, material *Material) error {
	return m.Called(ctx, material).Error(0)
}

func (m *mockMaterialRepo) GetByID(ctx context.Context, id uuid.UUID) (*Material, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*Material), args.Error(1)
}

func (m *mockMaterialRepo) List(ctx context.Context, tenantID uuid.UUID, offset, limit int) ([]*Material, error) {
	args := m.Called(ctx, tenantID, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*Material), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockMaterialInventoryRepo — MaterialInventoryRepository
// ---------------------------------------------------------------------------

type mockMaterialInventoryRepo struct{ mock.Mock }

func (m *mockMaterialInventoryRepo) GetByGCIDAndMaterial(ctx context.Context, tenantID, gcid, materialID uuid.UUID) (*MaterialInventory, error) {
	args := m.Called(ctx, tenantID, gcid, materialID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*MaterialInventory), args.Error(1)
}

func (m *mockMaterialInventoryRepo) Update(ctx context.Context, inventory *MaterialInventory) error {
	return m.Called(ctx, inventory).Error(0)
}

func (m *mockMaterialInventoryRepo) Upsert(ctx context.Context, inventory *MaterialInventory) error {
	return m.Called(ctx, inventory).Error(0)
}

// ---------------------------------------------------------------------------
// mockCraftingRecipeRepo — CraftingRecipeRepository
// ---------------------------------------------------------------------------

type mockCraftingRecipeRepo struct{ mock.Mock }

func (m *mockCraftingRecipeRepo) Create(ctx context.Context, recipe *CraftingRecipe) error {
	return m.Called(ctx, recipe).Error(0)
}

func (m *mockCraftingRecipeRepo) GetByID(ctx context.Context, id uuid.UUID) (*CraftingRecipe, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*CraftingRecipe), args.Error(1)
}

func (m *mockCraftingRecipeRepo) List(ctx context.Context, tenantID uuid.UUID, offset, limit int) ([]*CraftingRecipe, error) {
	args := m.Called(ctx, tenantID, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*CraftingRecipe), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockLootboxRepo — LootboxRepository
// ---------------------------------------------------------------------------

type mockLootboxRepo struct{ mock.Mock }

func (m *mockLootboxRepo) Create(ctx context.Context, lootbox *Lootbox) error {
	return m.Called(ctx, lootbox).Error(0)
}

func (m *mockLootboxRepo) GetByID(ctx context.Context, id uuid.UUID) (*Lootbox, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*Lootbox), args.Error(1)
}

func (m *mockLootboxRepo) Update(ctx context.Context, lootbox *Lootbox) error {
	return m.Called(ctx, lootbox).Error(0)
}

func (m *mockLootboxRepo) ListByGCID(ctx context.Context, tenantID, gcid uuid.UUID, offset, limit int) ([]*Lootbox, error) {
	args := m.Called(ctx, tenantID, gcid, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*Lootbox), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockLootTableRepo — LootTableRepository
// ---------------------------------------------------------------------------

type mockLootTableRepo struct{ mock.Mock }

func (m *mockLootTableRepo) ListByTier(ctx context.Context, tenantID uuid.UUID, tier string) ([]*LootTable, error) {
	args := m.Called(ctx, tenantID, tier)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*LootTable), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockLoginCalendarRepo — LoginCalendarRepository
// ---------------------------------------------------------------------------

type mockLoginCalendarRepo struct{ mock.Mock }

func (m *mockLoginCalendarRepo) Create(ctx context.Context, entry *LoginCalendar) error {
	return m.Called(ctx, entry).Error(0)
}

func (m *mockLoginCalendarRepo) GetByDate(ctx context.Context, tenantID, gcid uuid.UUID, date time.Time) (*LoginCalendar, error) {
	args := m.Called(ctx, tenantID, gcid, date)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*LoginCalendar), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockTerritoryIncomeRepo — TerritoryIncomeRepository
// ---------------------------------------------------------------------------

type mockTerritoryIncomeRepo struct{ mock.Mock }

func (m *mockTerritoryIncomeRepo) CreateConfig(ctx context.Context, config *TerritoryIncomeConfig) error {
	return m.Called(ctx, config).Error(0)
}

func (m *mockTerritoryIncomeRepo) ListConfigsByTenant(ctx context.Context, tenantID uuid.UUID, frequency IncomeFrequency) ([]*TerritoryIncomeConfig, error) {
	args := m.Called(ctx, tenantID, frequency)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*TerritoryIncomeConfig), args.Error(1)
}

func (m *mockTerritoryIncomeRepo) CreateLog(ctx context.Context, log *TerritoryIncomeLog) error {
	return m.Called(ctx, log).Error(0)
}

// ---------------------------------------------------------------------------
// mockSeasonalRewardRepo — SeasonalRewardRepository
// ---------------------------------------------------------------------------

type mockSeasonalRewardRepo struct{ mock.Mock }

func (m *mockSeasonalRewardRepo) ListBySeason(ctx context.Context, tenantID uuid.UUID, seasonID string) ([]*SeasonalLeaderboardReward, error) {
	args := m.Called(ctx, tenantID, seasonID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*SeasonalLeaderboardReward), args.Error(1)
}

func (m *mockSeasonalRewardRepo) Create(ctx context.Context, reward *SeasonalLeaderboardReward) error {
	return m.Called(ctx, reward).Error(0)
}

// ---------------------------------------------------------------------------
// mockMarketListingRepo — MarketListingRepository
// ---------------------------------------------------------------------------

type mockMarketListingRepo struct{ mock.Mock }

func (m *mockMarketListingRepo) Create(ctx context.Context, listing *MarketListing) error {
	return m.Called(ctx, listing).Error(0)
}

func (m *mockMarketListingRepo) GetByID(ctx context.Context, id uuid.UUID) (*MarketListing, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*MarketListing), args.Error(1)
}

func (m *mockMarketListingRepo) Update(ctx context.Context, listing *MarketListing) error {
	return m.Called(ctx, listing).Error(0)
}

func (m *mockMarketListingRepo) ListActive(ctx context.Context, tenantID uuid.UUID, offset, limit int) ([]*MarketListing, error) {
	args := m.Called(ctx, tenantID, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*MarketListing), args.Error(1)
}

func (m *mockMarketListingRepo) ListExpired(ctx context.Context, tenantID uuid.UUID, now time.Time) ([]*MarketListing, error) {
	args := m.Called(ctx, tenantID, now)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*MarketListing), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockMarketOrderRepo — MarketOrderRepository
// ---------------------------------------------------------------------------

type mockMarketOrderRepo struct{ mock.Mock }

func (m *mockMarketOrderRepo) Create(ctx context.Context, order *MarketOrder) error {
	return m.Called(ctx, order).Error(0)
}

func (m *mockMarketOrderRepo) GetByID(ctx context.Context, id uuid.UUID) (*MarketOrder, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*MarketOrder), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockMarketTransactionRepo — MarketTransactionRepository
// ---------------------------------------------------------------------------

type mockMarketTransactionRepo struct{ mock.Mock }

func (m *mockMarketTransactionRepo) Create(ctx context.Context, tx *MarketTransaction) error {
	return m.Called(ctx, tx).Error(0)
}

func (m *mockMarketTransactionRepo) ListByOrder(ctx context.Context, orderID uuid.UUID) ([]*MarketTransaction, error) {
	args := m.Called(ctx, orderID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*MarketTransaction), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockCurrencyConversionRepo — CurrencyConversionRepository
// ---------------------------------------------------------------------------

type mockCurrencyConversionRepo struct{ mock.Mock }

func (m *mockCurrencyConversionRepo) Create(ctx context.Context, conversion *CurrencyConversion) error {
	return m.Called(ctx, conversion).Error(0)
}

// ---------------------------------------------------------------------------
// mockCurrencyTransferRepo — CurrencyTransferRepository
// ---------------------------------------------------------------------------

type mockCurrencyTransferRepo struct{ mock.Mock }

func (m *mockCurrencyTransferRepo) Create(ctx context.Context, transfer *CurrencyTransfer) error {
	return m.Called(ctx, transfer).Error(0)
}

func (m *mockCurrencyTransferRepo) SumDailyTransferred(ctx context.Context, tenantID, senderGCID uuid.UUID, currency CurrencyType, day time.Time) (int, error) {
	args := m.Called(ctx, tenantID, senderGCID, currency, day)
	return args.Int(0), args.Error(1)
}

// ---------------------------------------------------------------------------
// mockGroupFundPoolRepo — GroupFundPoolRepository
// ---------------------------------------------------------------------------

type mockGroupFundPoolRepo struct{ mock.Mock }

func (m *mockGroupFundPoolRepo) Create(ctx context.Context, pool *GroupFundPool) error {
	return m.Called(ctx, pool).Error(0)
}

func (m *mockGroupFundPoolRepo) GetByID(ctx context.Context, id uuid.UUID) (*GroupFundPool, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*GroupFundPool), args.Error(1)
}

func (m *mockGroupFundPoolRepo) Update(ctx context.Context, pool *GroupFundPool) error {
	return m.Called(ctx, pool).Error(0)
}

// ---------------------------------------------------------------------------
// mockGroupFundContributionRepo — GroupFundContributionRepository
// ---------------------------------------------------------------------------

type mockGroupFundContributionRepo struct{ mock.Mock }

func (m *mockGroupFundContributionRepo) Create(ctx context.Context, contribution *GroupFundContribution) error {
	return m.Called(ctx, contribution).Error(0)
}

func (m *mockGroupFundContributionRepo) ListByPool(ctx context.Context, poolID uuid.UUID) ([]*GroupFundContribution, error) {
	args := m.Called(ctx, poolID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*GroupFundContribution), args.Error(1)
}
