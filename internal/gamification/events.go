package gamification

import (
	"time"

	"github.com/google/uuid"
)

// TopicGamificationEvents is the Cloud Pub/Sub topic for gamification domain events.
//
// M12.3.E (2026-05-12): migrated from "chora.gamification.events" to the
// canonical chora.{domain}.{aggregate}.{event_type}.v{N} form. Gamification
// is hosted under chora-consumption (5-core domain consolidation per M12.2);
// the topic moves under the consumption domain segment.
const TopicGamificationEvents = "chora.consumption.gamification.events.v1"

// Event type constants.
const (
	EventSkinCreated             = "skin.created"
	EventSkinUpdated             = "skin.updated"
	EventSkinDeleted             = "skin.deleted"
	EventSkinAwarded             = "skin.awarded"
	EventSkinEquipped            = "skin.equipped"
	EventSkinUnequipped          = "skin.unequipped"
	EventSkinExported            = "skin.exported"
	EventLegendaryTransferred    = "legendary.transferred"
	EventCoinEarned              = "coin.earned"
	EventCoinSpent               = "coin.spent"
	EventLeaguePromoted          = "league.promoted"
	EventLeagueRelegated         = "league.relegated"
	EventBadgeEarned             = "badge.earned"
	EventBountyCreated           = "bounty.created"
	EventBountyCompleted         = "bounty.completed"
	EventBountyExpired           = "bounty.expired"
	EventEconomyConfigUpdated    = "economy_config.updated"
	EventEconomyConfigRolledBack = "economy_config.rolled_back"
	EventTradingCardEarned       = "trading_card.earned"
	EventTradingCardRecycled     = "trading_card.recycled"
	EventCardTradeProposed       = "card_trade.proposed"
	EventCardTradeCompleted      = "card_trade.completed"
	EventCardTradeRejected       = "card_trade.rejected"
	EventCardTradeCancelled      = "card_trade.cancelled"
	EventCardSetCompleted        = "card_set.completed"
	EventEconomyDrainExecuted    = "economy.drain.executed"
	EventMaterialExpired         = "economy.material.expired"
	EventELODecayed              = "economy.elo.decayed"
	EventBossChallengeCreated    = "boss_challenge.created"
	EventBossChallengeCompleted  = "boss_challenge.completed"
	EventCraftingRecipeCompleted = "crafting.recipe_completed"
	EventLootboxOpened           = "lootbox.opened"
	EventDailyCheckinCompleted   = "daily_checkin.completed"

	// Territory rewards events (Phase 60.5)
	EventTerritoryIncomeDistributed         = "territory.income.distributed"
	EventTerritorySeasonalRewardDistributed = "territory.seasonal_reward.distributed"

	// Marketplace events (Phase 60.8)
	EventMarketListingSold    = "market.listing_sold"
	EventMarketListingExpired = "market.listing_expired"

	// Currency converter events (Phase 60.8)
	EventCurrencyConverted = "currency.converted"

	// Social economy events (Phase 60.8)
	EventCurrencyTransferred  = "currency.transferred"
	EventGroupFundDistributed = "group_fund.distributed"
)

// Aggregate type constants.
const (
	AggregateDigitalSkin         = "DigitalSkin"
	AggregateSkinAward           = "SkinAward"
	AggregateSkinEquipment       = "SkinEquipment"
	AggregateSkinExport          = "SkinExport"
	AggregateLegendarySkin       = "LegendarySkin"
	AggregateCoinAccount         = "CoinAccount"
	AggregateLeague              = "League"
	AggregateTopicBadge          = "TopicBadge"
	AggregateKnowledgeBounty     = "KnowledgeBounty"
	AggregateEconomyConfig       = "EconomyConfig"
	AggregateTradingCard         = "TradingCard"
	AggregateTradingCardInstance = "TradingCardInstance"
	AggregateCardSet             = "CardSet"
	AggregateCardTrade           = "CardTrade"
	AggregateEconomySnapshot     = "EconomySnapshot"
	AggregateBossChallenge       = "BossChallenge"
	AggregateMaterial            = "Material"
	AggregateCraftingRecipe      = "CraftingRecipe"
	AggregateLootbox             = "Lootbox"
	AggregateLoginCalendar       = "LoginCalendar"

	// Territory rewards aggregates (Phase 60.5)
	AggregateTerritoryIncomeConfig     = "TerritoryIncomeConfig"
	AggregateTerritoryIncomeLog        = "TerritoryIncomeLog"
	AggregateSeasonalLeaderboardReward = "SeasonalLeaderboardReward"

	// Marketplace aggregates (Phase 60.8)
	AggregateMarketListing     = "MarketListing"
	AggregateMarketOrder       = "MarketOrder"
	AggregateMarketTransaction = "MarketTransaction"

	// Currency & social economy aggregates (Phase 60.8)
	AggregateCurrencyConversion = "CurrencyConversion"
	AggregateCurrencyTransfer   = "CurrencyTransfer"
	AggregateGroupFundPool      = "GroupFundPool"
)

// DomainEvent represents an immutable event emitted by the gamification domain.
type DomainEvent struct {
	EventID       uuid.UUID              `json:"event_id"`
	EventType     string                 `json:"event_type"`
	Timestamp     time.Time              `json:"timestamp"`
	TenantID      uuid.UUID              `json:"tenant_id"`
	GCID          *uuid.UUID             `json:"gcid,omitempty"`
	AggregateID   uuid.UUID              `json:"aggregate_id"`
	AggregateType string                 `json:"aggregate_type"`
	Payload       map[string]interface{} `json:"payload"`
	CorrelationID *uuid.UUID             `json:"correlation_id,omitempty"`
	CausationID   *uuid.UUID             `json:"causation_id,omitempty"`
}

// NewDomainEvent creates a new DomainEvent with generated ID and current timestamp.
func NewDomainEvent(
	eventType string,
	tenantID uuid.UUID,
	gcid *uuid.UUID,
	aggregateID uuid.UUID,
	aggregateType string,
	payload map[string]interface{},
) DomainEvent {
	return DomainEvent{
		EventID:       uuid.Must(uuid.NewV7()),
		EventType:     eventType,
		Timestamp:     time.Now().UTC(),
		TenantID:      tenantID,
		GCID:          gcid,
		AggregateID:   aggregateID,
		AggregateType: aggregateType,
		Payload:       payload,
	}
}
