package gamification

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// DigitalSkinRepository defines data access for DigitalSkin entities.
type DigitalSkinRepository interface {
	Create(ctx context.Context, skin *DigitalSkin) error
	GetByID(ctx context.Context, id uuid.UUID) (*DigitalSkin, error)
	List(ctx context.Context, tenantID *uuid.UUID, rarity *SkinRarity, category *string, offset, limit int) ([]*DigitalSkin, error)
	Update(ctx context.Context, skin *DigitalSkin) error
	Delete(ctx context.Context, id uuid.UUID) error
}

// SkinAwardRepository defines data access for SkinAward entities.
type SkinAwardRepository interface {
	Create(ctx context.Context, award *SkinAward) error
	GetByID(ctx context.Context, id uuid.UUID) (*SkinAward, error)
	ListByGCID(ctx context.Context, tenantID, gcid uuid.UUID, offset, limit int) ([]*SkinAward, error)
	ListBySkin(ctx context.Context, tenantID, skinID uuid.UUID, offset, limit int) ([]*SkinAward, error)
}

// SkinEquipmentRepository defines data access for SkinEquipment entities.
type SkinEquipmentRepository interface {
	GetByGCID(ctx context.Context, tenantID, gcid uuid.UUID) ([]*SkinEquipment, error)
	EquipSlot(ctx context.Context, equipment *SkinEquipment) error
	UnequipSlot(ctx context.Context, tenantID, gcid uuid.UUID, slot EquipmentSlot) error
}

// SkinExportRepository defines data access for SkinExport entities.
type SkinExportRepository interface {
	Create(ctx context.Context, export *SkinExport) error
	ListByGCID(ctx context.Context, tenantID, gcid uuid.UUID, offset, limit int) ([]*SkinExport, error)
}

// LegendarySkinRepository defines data access for LegendarySkin entities.
type LegendarySkinRepository interface {
	GetBySkinID(ctx context.Context, tenantID, skinID uuid.UUID) (*LegendarySkin, error)
	List(ctx context.Context, tenantID uuid.UUID, offset, limit int) ([]*LegendarySkin, error)
	TransferHolder(ctx context.Context, legendary *LegendarySkin) error
}

// CoinAccountRepository defines data access for CoinAccount and CoinTransaction entities.
type CoinAccountRepository interface {
	GetByGCID(ctx context.Context, tenantID, gcid uuid.UUID) (*CoinAccount, error)
	CreateIfNotExists(ctx context.Context, account *CoinAccount) error
	UpdateBalance(ctx context.Context, account *CoinAccount) error
	CreateTransaction(ctx context.Context, tx *CoinTransaction) error
	ListTransactions(ctx context.Context, tenantID, coinAccountID uuid.UUID, offset, limit int) ([]*CoinTransaction, error)
	SumBalances(ctx context.Context, tenantID uuid.UUID) (int64, error)
	CountActiveAccounts(ctx context.Context, tenantID uuid.UUID) (int, error)
	SumDailyMinted(ctx context.Context, tenantID uuid.UUID, day time.Time) (int64, error)
	SumDailyDrained(ctx context.Context, tenantID uuid.UUID, day time.Time) (int64, error)
}

// LeagueRepository defines data access for League entities.
type LeagueRepository interface {
	GetByGCID(ctx context.Context, tenantID, gcid uuid.UUID, seasonWeek string) (*League, error)
	ListStandings(ctx context.Context, tenantID uuid.UUID, tier LeagueTier, seasonWeek string, offset, limit int) ([]*League, error)
	Upsert(ctx context.Context, league *League) error
}

// TopicBadgeRepository defines data access for TopicBadge entities.
type TopicBadgeRepository interface {
	GetByGCIDAndTopic(ctx context.Context, tenantID, gcid, topicID uuid.UUID) (*TopicBadge, error)
	ListByGCID(ctx context.Context, tenantID, gcid uuid.UUID, offset, limit int) ([]*TopicBadge, error)
	Create(ctx context.Context, badge *TopicBadge) error
}

// KnowledgeBountyRepository defines data access for KnowledgeBounty entities.
type KnowledgeBountyRepository interface {
	Create(ctx context.Context, bounty *KnowledgeBounty) error
	GetByID(ctx context.Context, id uuid.UUID) (*KnowledgeBounty, error)
	List(ctx context.Context, tenantID uuid.UUID, status *BountyStatus, offset, limit int) ([]*KnowledgeBounty, error)
	Update(ctx context.Context, bounty *KnowledgeBounty) error
	Delete(ctx context.Context, id uuid.UUID) error
}

// EconomyConfigRepository defines data access for EconomyConfig and EconomyConfigHistory entities.
type EconomyConfigRepository interface {
	GetByID(ctx context.Context, id uuid.UUID) (*EconomyConfig, error)
	GetEffective(ctx context.Context, tenantID uuid.UUID, category ConfigCategory) ([]*EconomyConfig, error)
	GetByTenantCategoryKey(ctx context.Context, tenantID *uuid.UUID, category ConfigCategory, key string) (*EconomyConfig, error)
	Create(ctx context.Context, config *EconomyConfig) error
	Update(ctx context.Context, config *EconomyConfig) error
	Delete(ctx context.Context, id uuid.UUID) error
	GetHistory(ctx context.Context, configID uuid.UUID, limit int) ([]*EconomyConfigHistory, error)
	CreateHistoryEntry(ctx context.Context, entry *EconomyConfigHistory) error
}

// EconomySnapshotRepository defines data access for EconomySnapshot entities.
type EconomySnapshotRepository interface {
	Create(ctx context.Context, snapshot *EconomySnapshot) error
	GetLatestByTenant(ctx context.Context, tenantID uuid.UUID) (*EconomySnapshot, error)
}

// TradingCardRepository defines data access for TradingCard entities.
type TradingCardRepository interface {
	Create(ctx context.Context, card *TradingCard) error
	GetByID(ctx context.Context, id uuid.UUID) (*TradingCard, error)
	List(ctx context.Context, tenantID uuid.UUID, rarity *CardRarity, archetype *CardArchetype, setID *uuid.UUID, offset, limit int) ([]*TradingCard, error)
	Update(ctx context.Context, card *TradingCard) error
	Delete(ctx context.Context, id uuid.UUID) error
}

// CardInstanceRepository defines data access for TradingCardInstance entities.
type CardInstanceRepository interface {
	Create(ctx context.Context, instance *TradingCardInstance) error
	GetByID(ctx context.Context, id uuid.UUID) (*TradingCardInstance, error)
	ListByOwner(ctx context.Context, tenantID, gcid uuid.UUID, cursor *uuid.UUID, limit int) ([]*TradingCardInstance, error)
	Update(ctx context.Context, instance *TradingCardInstance) error
	NextSerialNumber(ctx context.Context, cardID uuid.UUID) (int, error)
	CountDistinctCardsInSet(ctx context.Context, tenantID, gcid uuid.UUID, setID uuid.UUID) (int, error)
	SwapOwnership(ctx context.Context, instanceID, newOwnerGCID uuid.UUID) error
	ReturnToCollection(ctx context.Context, instanceID uuid.UUID) error
}

// CardSetRepository defines data access for CardSet entities.
type CardSetRepository interface {
	Create(ctx context.Context, set *CardSet) error
	GetByID(ctx context.Context, id uuid.UUID) (*CardSet, error)
	List(ctx context.Context, tenantID uuid.UUID, offset, limit int) ([]*CardSet, error)
}

// CardTradeRepository defines data access for CardTrade entities.
type CardTradeRepository interface {
	Create(ctx context.Context, trade *CardTrade) error
	GetByID(ctx context.Context, id uuid.UUID) (*CardTrade, error)
	ListByGCID(ctx context.Context, tenantID, gcid uuid.UUID, status *TradeStatus, offset, limit int) ([]*CardTrade, error)
	Update(ctx context.Context, trade *CardTrade) error
}

// CardTradeHistoryRepository defines data access for CardTradeHistory (append-only).
type CardTradeHistoryRepository interface {
	Create(ctx context.Context, entry *CardTradeHistory) error
	ListByTrade(ctx context.Context, tradeID uuid.UUID) ([]*CardTradeHistory, error)
}

// CardSetCompletionRepository defines data access for CardSetCompletion entities.
type CardSetCompletionRepository interface {
	Create(ctx context.Context, completion *CardSetCompletion) error
	GetByGCIDAndSet(ctx context.Context, tenantID, gcid, setID uuid.UUID) (*CardSetCompletion, error)
}

// BossChallengeRepository defines data access for BossChallenge entities.
type BossChallengeRepository interface {
	Create(ctx context.Context, challenge *BossChallenge) error
	GetByID(ctx context.Context, id uuid.UUID) (*BossChallenge, error)
	Update(ctx context.Context, challenge *BossChallenge) error
	ListByStatus(ctx context.Context, tenantID uuid.UUID, status BossChallengeStatus, offset, limit int) ([]*BossChallenge, error)
}

// BossChallengeParticipantRepository defines data access for BossChallengeParticipant entities.
type BossChallengeParticipantRepository interface {
	Upsert(ctx context.Context, participant *BossChallengeParticipant) error
	ListByChallenge(ctx context.Context, challengeID uuid.UUID) ([]*BossChallengeParticipant, error)
}

// MaterialRepository defines data access for Material entities.
type MaterialRepository interface {
	Create(ctx context.Context, material *Material) error
	GetByID(ctx context.Context, id uuid.UUID) (*Material, error)
	List(ctx context.Context, tenantID uuid.UUID, offset, limit int) ([]*Material, error)
}

// MaterialInventoryRepository defines data access for MaterialInventory entities.
type MaterialInventoryRepository interface {
	GetByGCIDAndMaterial(ctx context.Context, tenantID, gcid, materialID uuid.UUID) (*MaterialInventory, error)
	Update(ctx context.Context, inventory *MaterialInventory) error
	Upsert(ctx context.Context, inventory *MaterialInventory) error
}

// CraftingRecipeRepository defines data access for CraftingRecipe entities.
type CraftingRecipeRepository interface {
	Create(ctx context.Context, recipe *CraftingRecipe) error
	GetByID(ctx context.Context, id uuid.UUID) (*CraftingRecipe, error)
	List(ctx context.Context, tenantID uuid.UUID, offset, limit int) ([]*CraftingRecipe, error)
}

// LootboxRepository defines data access for Lootbox entities.
type LootboxRepository interface {
	Create(ctx context.Context, lootbox *Lootbox) error
	GetByID(ctx context.Context, id uuid.UUID) (*Lootbox, error)
	Update(ctx context.Context, lootbox *Lootbox) error
	ListByGCID(ctx context.Context, tenantID, gcid uuid.UUID, offset, limit int) ([]*Lootbox, error)
}

// LootTableRepository defines data access for LootTable entries.
type LootTableRepository interface {
	ListByTier(ctx context.Context, tenantID uuid.UUID, tier string) ([]*LootTable, error)
}

// LoginCalendarRepository defines data access for LoginCalendar entities.
type LoginCalendarRepository interface {
	Create(ctx context.Context, entry *LoginCalendar) error
	GetByDate(ctx context.Context, tenantID, gcid uuid.UUID, date time.Time) (*LoginCalendar, error)
}

// ---------------------------------------------------------------------------
// Territory Rewards Repositories (Phase 60.5)
// ---------------------------------------------------------------------------

// TerritoryIncomeRepository defines data access for TerritoryIncomeConfig and TerritoryIncomeLog entities.
type TerritoryIncomeRepository interface {
	CreateConfig(ctx context.Context, config *TerritoryIncomeConfig) error
	ListConfigsByTenant(ctx context.Context, tenantID uuid.UUID, frequency IncomeFrequency) ([]*TerritoryIncomeConfig, error)
	CreateLog(ctx context.Context, log *TerritoryIncomeLog) error
}

// SeasonalRewardRepository defines data access for SeasonalLeaderboardReward entities.
type SeasonalRewardRepository interface {
	ListBySeason(ctx context.Context, tenantID uuid.UUID, seasonID string) ([]*SeasonalLeaderboardReward, error)
	Create(ctx context.Context, reward *SeasonalLeaderboardReward) error
}

// ---------------------------------------------------------------------------
// Marketplace Repositories (Phase 60.8)
// ---------------------------------------------------------------------------

// MarketListingRepository defines data access for MarketListing entities.
type MarketListingRepository interface {
	Create(ctx context.Context, listing *MarketListing) error
	GetByID(ctx context.Context, id uuid.UUID) (*MarketListing, error)
	Update(ctx context.Context, listing *MarketListing) error
	ListActive(ctx context.Context, tenantID uuid.UUID, offset, limit int) ([]*MarketListing, error)
	ListExpired(ctx context.Context, tenantID uuid.UUID, now time.Time) ([]*MarketListing, error)
}

// MarketOrderRepository defines data access for MarketOrder entities.
type MarketOrderRepository interface {
	Create(ctx context.Context, order *MarketOrder) error
	GetByID(ctx context.Context, id uuid.UUID) (*MarketOrder, error)
}

// MarketTransactionRepository defines data access for MarketTransaction (append-only).
type MarketTransactionRepository interface {
	Create(ctx context.Context, tx *MarketTransaction) error
	ListByOrder(ctx context.Context, orderID uuid.UUID) ([]*MarketTransaction, error)
}

// ---------------------------------------------------------------------------
// Currency & Social Economy Repositories (Phase 60.8)
// ---------------------------------------------------------------------------

// CurrencyConversionRepository defines data access for CurrencyConversion entities.
type CurrencyConversionRepository interface {
	Create(ctx context.Context, conversion *CurrencyConversion) error
}

// CurrencyTransferRepository defines data access for CurrencyTransfer entities.
type CurrencyTransferRepository interface {
	Create(ctx context.Context, transfer *CurrencyTransfer) error
	SumDailyTransferred(ctx context.Context, tenantID, senderGCID uuid.UUID, currency CurrencyType, day time.Time) (int, error)
}

// GroupFundPoolRepository defines data access for GroupFundPool entities.
type GroupFundPoolRepository interface {
	Create(ctx context.Context, pool *GroupFundPool) error
	GetByID(ctx context.Context, id uuid.UUID) (*GroupFundPool, error)
	Update(ctx context.Context, pool *GroupFundPool) error
}

// GroupFundContributionRepository defines data access for GroupFundContribution entities.
type GroupFundContributionRepository interface {
	Create(ctx context.Context, contribution *GroupFundContribution) error
	ListByPool(ctx context.Context, poolID uuid.UUID) ([]*GroupFundContribution, error)
}

// EventPublisher defines the interface for publishing domain events.
type EventPublisher interface {
	Publish(ctx context.Context, topic string, event interface{}) error
	Close() error
}
