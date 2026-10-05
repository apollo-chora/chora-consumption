package gamification

import (
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

// SkinRarity represents the rarity level of a DigitalSkin.
type SkinRarity string

const (
	SkinRarityCommon    SkinRarity = "common"
	SkinRarityUncommon  SkinRarity = "uncommon"
	SkinRarityRare      SkinRarity = "rare"
	SkinRarityEpic      SkinRarity = "epic"
	SkinRarityLegendary SkinRarity = "legendary"
)

// IsValid returns true if the SkinRarity value is recognized.
func (r SkinRarity) IsValid() bool {
	switch r {
	case SkinRarityCommon, SkinRarityUncommon, SkinRarityRare, SkinRarityEpic, SkinRarityLegendary:
		return true
	}
	return false
}

// SkinEarnedVia represents how a DigitalSkin was earned.
type SkinEarnedVia string

const (
	SkinEarnedViaStreak            SkinEarnedVia = "streak"
	SkinEarnedViaLeagueWin         SkinEarnedVia = "league_win"
	SkinEarnedViaTopicMastery      SkinEarnedVia = "topic_mastery"
	SkinEarnedViaFeaturedWork      SkinEarnedVia = "featured_work"
	SkinEarnedViaInstructorAward   SkinEarnedVia = "instructor_award"
	SkinEarnedViaCertification     SkinEarnedVia = "certification"
	SkinEarnedViaLegendaryTransfer SkinEarnedVia = "legendary_transfer"
)

// IsValid returns true if the SkinEarnedVia value is recognized.
func (v SkinEarnedVia) IsValid() bool {
	switch v {
	case SkinEarnedViaStreak, SkinEarnedViaLeagueWin, SkinEarnedViaTopicMastery,
		SkinEarnedViaFeaturedWork, SkinEarnedViaInstructorAward,
		SkinEarnedViaCertification, SkinEarnedViaLegendaryTransfer:
		return true
	}
	return false
}

// EquipmentSlot represents where a DigitalSkin can be equipped.
type EquipmentSlot string

const (
	EquipmentSlotProfilePhoto   EquipmentSlot = "profile_photo"
	EquipmentSlotZoomOverlay    EquipmentSlot = "zoom_overlay"
	EquipmentSlotZoomBackground EquipmentSlot = "zoom_background"
	EquipmentSlotNameBadge      EquipmentSlot = "name_badge"
	EquipmentSlotEmailSignature EquipmentSlot = "email_signature"
	EquipmentSlotClassProfile   EquipmentSlot = "class_profile"
)

// IsValid returns true if the EquipmentSlot value is recognized.
func (s EquipmentSlot) IsValid() bool {
	switch s {
	case EquipmentSlotProfilePhoto, EquipmentSlotZoomOverlay, EquipmentSlotZoomBackground,
		EquipmentSlotNameBadge, EquipmentSlotEmailSignature, EquipmentSlotClassProfile:
		return true
	}
	return false
}

// ExportTarget represents external platforms for skin export.
type ExportTarget string

const (
	ExportTargetZoom       ExportTarget = "zoom"
	ExportTargetLinkedIn   ExportTarget = "linkedin"
	ExportTargetEmail      ExportTarget = "email"
	ExportTargetOpenBadges ExportTarget = "open_badges"
)

// IsValid returns true if the ExportTarget value is recognized.
func (t ExportTarget) IsValid() bool {
	switch t {
	case ExportTargetZoom, ExportTargetLinkedIn, ExportTargetEmail, ExportTargetOpenBadges:
		return true
	}
	return false
}

// ExportStatus represents the status of a skin export operation.
type ExportStatus string

const (
	ExportStatusPending   ExportStatus = "pending"
	ExportStatusCompleted ExportStatus = "completed"
	ExportStatusFailed    ExportStatus = "failed"
)

// IsValid returns true if the ExportStatus value is recognized.
func (s ExportStatus) IsValid() bool {
	switch s {
	case ExportStatusPending, ExportStatusCompleted, ExportStatusFailed:
		return true
	}
	return false
}

// LeagueTier represents the competitive league tier.
type LeagueTier string

const (
	LeagueTierBronze   LeagueTier = "bronze"
	LeagueTierSilver   LeagueTier = "silver"
	LeagueTierGold     LeagueTier = "gold"
	LeagueTierPlatinum LeagueTier = "platinum"
	LeagueTierDiamond  LeagueTier = "diamond"
)

// IsValid returns true if the LeagueTier value is recognized.
func (t LeagueTier) IsValid() bool {
	switch t {
	case LeagueTierBronze, LeagueTierSilver, LeagueTierGold, LeagueTierPlatinum, LeagueTierDiamond:
		return true
	}
	return false
}

// BadgeTier represents the tier level of a topic badge.
type BadgeTier string

const (
	BadgeTierBronze BadgeTier = "bronze"
	BadgeTierSilver BadgeTier = "silver"
	BadgeTierGold   BadgeTier = "gold"
)

// IsValid returns true if the BadgeTier value is recognized.
func (t BadgeTier) IsValid() bool {
	switch t {
	case BadgeTierBronze, BadgeTierSilver, BadgeTierGold:
		return true
	}
	return false
}

// BountyStatus represents the lifecycle status of a KnowledgeBounty.
type BountyStatus string

const (
	BountyStatusOpen       BountyStatus = "open"
	BountyStatusInProgress BountyStatus = "in_progress"
	BountyStatusCompleted  BountyStatus = "completed"
	BountyStatusExpired    BountyStatus = "expired"
	BountyStatusCancelled  BountyStatus = "cancelled"
)

// IsValid returns true if the BountyStatus value is recognized.
func (s BountyStatus) IsValid() bool {
	switch s {
	case BountyStatusOpen, BountyStatusInProgress, BountyStatusCompleted,
		BountyStatusExpired, BountyStatusCancelled:
		return true
	}
	return false
}

// CoinTransactionType represents the type of coin transaction.
type CoinTransactionType string

const (
	CoinTransactionTypeEarned   CoinTransactionType = "earned"
	CoinTransactionTypeSpent    CoinTransactionType = "spent"
	CoinTransactionTypeRefunded CoinTransactionType = "refunded"
)

// IsValid returns true if the CoinTransactionType value is recognized.
func (t CoinTransactionType) IsValid() bool {
	switch t {
	case CoinTransactionTypeEarned, CoinTransactionTypeSpent, CoinTransactionTypeRefunded:
		return true
	}
	return false
}

// ConfigCategory represents the economy configuration category.
type ConfigCategory string

const (
	ConfigCategoryFogThresholds        ConfigCategory = "fog_thresholds"
	ConfigCategoryEloSettings          ConfigCategory = "elo_settings"
	ConfigCategoryDuelLimits           ConfigCategory = "duel_limits"
	ConfigCategoryMaterialDropRates    ConfigCategory = "material_drop_rates"
	ConfigCategoryLootboxProbabilities ConfigCategory = "lootbox_probabilities"
	ConfigCategoryCoOpHP               ConfigCategory = "co_op_hp"
	ConfigCategoryBossDifficulty       ConfigCategory = "boss_difficulty"
	ConfigCategoryLoginRewards         ConfigCategory = "login_rewards"
	ConfigCategoryStoreLimits          ConfigCategory = "store_limits"
)

// IsValid returns true if the ConfigCategory value is recognized.
func (c ConfigCategory) IsValid() bool {
	switch c {
	case ConfigCategoryFogThresholds, ConfigCategoryEloSettings, ConfigCategoryDuelLimits,
		ConfigCategoryMaterialDropRates, ConfigCategoryLootboxProbabilities,
		ConfigCategoryCoOpHP, ConfigCategoryBossDifficulty,
		ConfigCategoryLoginRewards, ConfigCategoryStoreLimits:
		return true
	}
	return false
}

// CardArchetype represents the archetype of a TradingCard.
type CardArchetype string

const (
	CardArchetypeAtom       CardArchetype = "atom"
	CardArchetypeTopicNode  CardArchetype = "topic_node"
	CardArchetypeCompanion  CardArchetype = "companion"
	CardArchetypeInstructor CardArchetype = "instructor"
	CardArchetypeLegendary  CardArchetype = "legendary"
)

func (a CardArchetype) IsValid() bool {
	switch a {
	case CardArchetypeAtom, CardArchetypeTopicNode, CardArchetypeCompanion,
		CardArchetypeInstructor, CardArchetypeLegendary:
		return true
	}
	return false
}

// CardRarity represents the rarity level of a TradingCard.
type CardRarity string

const (
	CardRarityCommon    CardRarity = "common"
	CardRarityUncommon  CardRarity = "uncommon"
	CardRarityRare      CardRarity = "rare"
	CardRarityEpic      CardRarity = "epic"
	CardRarityLegendary CardRarity = "legendary"
)

func (r CardRarity) IsValid() bool {
	switch r {
	case CardRarityCommon, CardRarityUncommon, CardRarityRare, CardRarityEpic, CardRarityLegendary:
		return true
	}
	return false
}

// AcquiredVia represents how a TradingCardInstance was acquired.
type AcquiredVia string

const (
	AcquiredViaLootbox     AcquiredVia = "lootbox"
	AcquiredViaBossReward  AcquiredVia = "boss_reward"
	AcquiredViaCoOpReward  AcquiredVia = "co_op_reward"
	AcquiredViaDuelReward  AcquiredVia = "duel_reward"
	AcquiredViaDirectAward AcquiredVia = "direct_award"
	AcquiredViaTrade       AcquiredVia = "trade"
)

func (a AcquiredVia) IsValid() bool {
	switch a {
	case AcquiredViaLootbox, AcquiredViaBossReward, AcquiredViaCoOpReward,
		AcquiredViaDuelReward, AcquiredViaDirectAward, AcquiredViaTrade:
		return true
	}
	return false
}

// CardTradeStatus represents the trade status of a card instance.
type CardTradeStatus string

const (
	CardTradeStatusInCollection   CardTradeStatus = "in_collection"
	CardTradeStatusListedForTrade CardTradeStatus = "listed_for_trade"
	CardTradeStatusInTransit      CardTradeStatus = "in_transit"
	CardTradeStatusTradedAway     CardTradeStatus = "traded_away"
)

func (s CardTradeStatus) IsValid() bool {
	switch s {
	case CardTradeStatusInCollection, CardTradeStatusListedForTrade,
		CardTradeStatusInTransit, CardTradeStatusTradedAway:
		return true
	}
	return false
}

// TradeStatus represents the status of a peer-to-peer card trade.
type TradeStatus string

const (
	TradeStatusProposed  TradeStatus = "proposed"
	TradeStatusCompleted TradeStatus = "completed"
	TradeStatusRejected  TradeStatus = "rejected"
	TradeStatusCancelled TradeStatus = "cancelled"
)

func (s TradeStatus) IsValid() bool {
	switch s {
	case TradeStatusProposed, TradeStatusCompleted, TradeStatusRejected, TradeStatusCancelled:
		return true
	}
	return false
}

// RecycleXP default values by rarity.
const (
	RecycleXPCommon    = 10
	RecycleXPUncommon  = 25
	RecycleXPRare      = 50
	RecycleXPEpic      = 100
	RecycleXPLegendary = 250
)

// ---------------------------------------------------------------------------
// Entities
// ---------------------------------------------------------------------------

// DigitalSkin represents a visual reward item that can be earned (never purchased).
type DigitalSkin struct {
	ID            uuid.UUID              `json:"id"`
	TenantID      *uuid.UUID             `json:"tenant_id"`
	SkinCode      string                 `json:"skin_code"`
	Name          string                 `json:"name"`
	Description   string                 `json:"description"`
	Category      string                 `json:"category"`
	Rarity        SkinRarity             `json:"rarity"`
	AssetURL      string                 `json:"asset_url"`
	AssetVariants map[string]interface{} `json:"asset_variants"`
	EarnCriteria  map[string]interface{} `json:"earn_criteria"`
	IsLegendary   bool                   `json:"is_legendary"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
	DeletedAt     *time.Time             `json:"deleted_at,omitempty"`
}

// SkinAward records a DigitalSkin being awarded to a learner.
type SkinAward struct {
	ID            uuid.UUID              `json:"id"`
	TenantID      uuid.UUID              `json:"tenant_id"`
	GCID          uuid.UUID              `json:"gcid"`
	SkinID        uuid.UUID              `json:"skin_id"`
	EarnedAt      time.Time              `json:"earned_at"`
	EarnedVia     SkinEarnedVia          `json:"earned_via"`
	EarnedContext map[string]interface{} `json:"earned_context"`
}

// SkinEquipment tracks which DigitalSkin is equipped in each slot.
type SkinEquipment struct {
	ID          uuid.UUID     `json:"id"`
	TenantID    uuid.UUID     `json:"tenant_id"`
	GCID        uuid.UUID     `json:"gcid"`
	Slot        EquipmentSlot `json:"slot"`
	SkinAwardID uuid.UUID     `json:"skin_award_id"`
	EquippedAt  time.Time     `json:"equipped_at"`
}

// SkinExport records an export of a DigitalSkin to an external platform.
type SkinExport struct {
	ID           uuid.UUID    `json:"id"`
	TenantID     uuid.UUID    `json:"tenant_id"`
	GCID         uuid.UUID    `json:"gcid"`
	SkinAwardID  uuid.UUID    `json:"skin_award_id"`
	ExportTarget ExportTarget `json:"export_target"`
	ExportedAt   time.Time    `json:"exported_at"`
	ExportStatus ExportStatus `json:"export_status"`
	ExternalRef  *string      `json:"external_ref,omitempty"`
}

// LegendarySkin tracks the unique holder of a legendary DigitalSkin.
type LegendarySkin struct {
	ID                uuid.UUID                `json:"id"`
	TenantID          uuid.UUID                `json:"tenant_id"`
	SkinID            uuid.UUID                `json:"skin_id"`
	CurrentHolderGCID uuid.UUID                `json:"current_holder_gcid"`
	HeldSince         time.Time                `json:"held_since"`
	TransferReason    string                   `json:"transfer_reason"`
	PreviousHolders   []map[string]interface{} `json:"previous_holders"`
}

// CoinAccount tracks a learner's coin balance.
type CoinAccount struct {
	ID             uuid.UUID `json:"id"`
	TenantID       uuid.UUID `json:"tenant_id"`
	GCID           uuid.UUID `json:"gcid"`
	Balance        int64     `json:"balance"`
	LifetimeEarned int64     `json:"lifetime_earned"`
	LifetimeSpent  int64     `json:"lifetime_spent"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CoinTransaction records a single coin balance change.
type CoinTransaction struct {
	ID              uuid.UUID           `json:"id"`
	TenantID        uuid.UUID           `json:"tenant_id"`
	CoinAccountID   uuid.UUID           `json:"coin_account_id"`
	Amount          int64               `json:"amount"`
	TransactionType CoinTransactionType `json:"transaction_type"`
	Reason          string              `json:"reason"`
	ReferenceID     *uuid.UUID          `json:"reference_id,omitempty"`
	ReferenceType   *string             `json:"reference_type,omitempty"`
	CreatedAt       time.Time           `json:"created_at"`
}

// League tracks a learner's position in the competitive league system.
type League struct {
	ID         uuid.UUID  `json:"id"`
	TenantID   uuid.UUID  `json:"tenant_id"`
	GCID       uuid.UUID  `json:"gcid"`
	LeagueTier LeagueTier `json:"league_tier"`
	SeasonWeek string     `json:"season_week"`
	Rank       int        `json:"rank"`
	Points     int        `json:"points"`
	Promoted   bool       `json:"promoted"`
	Relegated  bool       `json:"relegated"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// TopicBadge represents a learner's mastery badge for a specific topic.
type TopicBadge struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	GCID      uuid.UUID `json:"gcid"`
	TopicID   uuid.UUID `json:"topic_id"`
	BadgeTier BadgeTier `json:"badge_tier"`
	EarnedAt  time.Time `json:"earned_at"`
}

// EconomyConfig represents a tenant-configurable economy parameter.
// When TenantID is nil, the record is a platform default.
type EconomyConfig struct {
	ID               uuid.UUID              `json:"id"`
	TenantID         *uuid.UUID             `json:"tenant_id"`
	ConfigCategory   ConfigCategory         `json:"config_category"`
	ConfigKey        string                 `json:"config_key"`
	ConfigValue      map[string]interface{} `json:"config_value"`
	IsTenantOverride bool                   `json:"is_tenant_override"`
	CreatedByGCID    uuid.UUID              `json:"created_by_gcid"`
	UpdatedByGCID    uuid.UUID              `json:"updated_by_gcid"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
	DeletedAt        *time.Time             `json:"deleted_at,omitempty"`
}

// EconomyConfigHistory is an append-only audit record for economy config changes.
type EconomyConfigHistory struct {
	ID              uuid.UUID              `json:"id"`
	EconomyConfigID uuid.UUID              `json:"economy_config_id"`
	OldValue        map[string]interface{} `json:"old_value"`
	NewValue        map[string]interface{} `json:"new_value"`
	ChangedByGCID   uuid.UUID              `json:"changed_by_gcid"`
	ChangeReason    string                 `json:"change_reason"`
	CreatedAt       time.Time              `json:"created_at"`
}

// TradingCard represents a card template in the Choraverse trading card system.
type TradingCard struct {
	ID            uuid.UUID     `json:"id"`
	TenantID      uuid.UUID     `json:"tenant_id"`
	CardName      string        `json:"card_name"`
	CardArchetype CardArchetype `json:"card_archetype"`
	LinkedAtomID  *uuid.UUID    `json:"linked_atom_id,omitempty"`
	LinkedTopicID *uuid.UUID    `json:"linked_topic_id,omitempty"`
	Rarity        CardRarity    `json:"rarity"`
	LoreText      string        `json:"lore_text"`
	StatPower     int           `json:"stat_power"`
	StatKnowledge int           `json:"stat_knowledge"`
	ArtAssetURL   string        `json:"art_asset_url"`
	IsTradable    bool          `json:"is_tradable"`
	SetID         *uuid.UUID    `json:"set_id,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
	DeletedAt     *time.Time    `json:"deleted_at,omitempty"`
}

// CardSet represents a thematic collection of trading cards.
type CardSet struct {
	ID               uuid.UUID  `json:"id"`
	TenantID         uuid.UUID  `json:"tenant_id"`
	SetName          string     `json:"set_name"`
	SetTheme         string     `json:"set_theme"`
	ReleaseDate      *time.Time `json:"release_date,omitempty"`
	IsLimitedEdition bool       `json:"is_limited_edition"`
	TotalCardsInSet  int        `json:"total_cards_in_set"`
	Description      string     `json:"description"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	DeletedAt        *time.Time `json:"deleted_at,omitempty"`
}

// TradingCardInstance represents an owned instance of a TradingCard.
type TradingCardInstance struct {
	ID           uuid.UUID       `json:"id"`
	CardID       uuid.UUID       `json:"card_id"`
	OwnerGCID    uuid.UUID       `json:"owner_gcid"`
	TenantID     uuid.UUID       `json:"tenant_id"`
	SerialNumber int             `json:"serial_number"`
	AcquiredVia  AcquiredVia     `json:"acquired_via"`
	AcquiredAt   time.Time       `json:"acquired_at"`
	IsFoil       bool            `json:"is_foil"`
	TradeStatus  CardTradeStatus `json:"trade_status"`
	CreatedAt    time.Time       `json:"created_at"`
	DeletedAt    *time.Time      `json:"deleted_at,omitempty"`
}

// CardTrade represents a peer-to-peer card trade proposal.
type CardTrade struct {
	ID                   uuid.UUID   `json:"id"`
	TenantID             uuid.UUID   `json:"tenant_id"`
	OffererGCID          uuid.UUID   `json:"offerer_gcid"`
	ReceiverGCID         uuid.UUID   `json:"receiver_gcid"`
	OfferedInstanceIDs   []uuid.UUID `json:"offered_instance_ids"`
	RequestedInstanceIDs []uuid.UUID `json:"requested_instance_ids"`
	Status               TradeStatus `json:"status"`
	CreatedAt            time.Time   `json:"created_at"`
	CompletedAt          *time.Time  `json:"completed_at,omitempty"`
	DeletedAt            *time.Time  `json:"deleted_at,omitempty"`
}

// CardTradeHistory is an append-only audit record for trade status transitions.
type CardTradeHistory struct {
	ID             uuid.UUID   `json:"id"`
	TradeID        uuid.UUID   `json:"trade_id"`
	PreviousStatus TradeStatus `json:"previous_status"`
	NewStatus      TradeStatus `json:"new_status"`
	ChangedByGCID  uuid.UUID   `json:"changed_by_gcid"`
	CreatedAt      time.Time   `json:"created_at"`
}

// CardSetCompletion records a one-time set completion by a learner.
type CardSetCompletion struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	GCID        uuid.UUID `json:"gcid"`
	SetID       uuid.UUID `json:"set_id"`
	CompletedAt time.Time `json:"completed_at"`
	XPBonus     int       `json:"xp_bonus"`
}

// RecycleResult is the response from recycling a card.
type RecycleResult struct {
	XPAwarded int    `json:"xp_awarded"`
	CardName  string `json:"card_name"`
}

// CardSetProgress is the response for set completion progress.
type CardSetProgress struct {
	SetID       uuid.UUID  `json:"set_id"`
	Collected   int        `json:"collected"`
	Total       int        `json:"total"`
	Completed   bool       `json:"completed"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// EconomySnapshot records point-in-time economy health metrics for a tenant.
type EconomySnapshot struct {
	ID               uuid.UUID `json:"id"`
	TenantID         uuid.UUID `json:"tenant_id"`
	SnapshotDate     time.Time `json:"snapshot_date"`
	TotalCoins       int64     `json:"total_coins"`
	DailyMinted      int64     `json:"daily_minted"`
	DailyDrained     int64     `json:"daily_drained"`
	ActiveAccounts   int       `json:"active_accounts"`
	InflationRatePct float64   `json:"inflation_rate_pct"`
	CreatedAt        time.Time `json:"created_at"`
}

// DrainResult is the outcome of a territory maintenance drain batch job.
type DrainResult struct {
	AccountsProcessed int   `json:"accounts_processed"`
	AccountsSkipped   int   `json:"accounts_skipped"`
	TotalDrained      int64 `json:"total_drained"`
}

// ExpiryResult is the outcome of a material shelf-life expiry batch job.
type ExpiryResult struct {
	MaterialsExpired int `json:"materials_expired"`
	MaterialsChecked int `json:"materials_checked"`
}

// DecayResult is the outcome of an ELO inactivity decay batch job.
type DecayResult struct {
	PlayersDecayed  int `json:"players_decayed"`
	PlayersChecked  int `json:"players_checked"`
	TotalRatingLost int `json:"total_rating_lost"`
}

// TerritoryHolder is an input struct for drain processing — each entry
// represents a GCID that holds territories in the Choraverse.
type TerritoryHolder struct {
	TenantID       uuid.UUID `json:"tenant_id"`
	GCID           uuid.UUID `json:"gcid"`
	TerritoryCount int       `json:"territory_count"`
}

// MaterialInstance is an input struct for expiry processing — each entry
// represents a material drop that may expire based on shelf-life.
type MaterialInstance struct {
	ID         uuid.UUID  `json:"id"`
	TenantID   uuid.UUID  `json:"tenant_id"`
	AcquiredAt time.Time  `json:"acquired_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
}

// ELOPlayer is an input struct for decay processing — each entry
// represents a player with an ELO rating and last duel timestamp.
type ELOPlayer struct {
	GCID       uuid.UUID `json:"gcid"`
	TenantID   uuid.UUID `json:"tenant_id"`
	Rating     int       `json:"rating"`
	LastDuelAt time.Time `json:"last_duel_at"`
}

// ELORatingFloor is the minimum ELO rating — decay never reduces below this.
const ELORatingFloor = 800

// ---------------------------------------------------------------------------
// Boss Challenge Enums
// ---------------------------------------------------------------------------

// BossDifficultyTier represents the difficulty of a boss challenge.
type BossDifficultyTier string

const (
	BossDifficultyNormal    BossDifficultyTier = "normal"
	BossDifficultyHard      BossDifficultyTier = "hard"
	BossDifficultyLegendary BossDifficultyTier = "legendary"
)

// IsValid returns true if the BossDifficultyTier value is recognized.
func (d BossDifficultyTier) IsValid() bool {
	switch d {
	case BossDifficultyNormal, BossDifficultyHard, BossDifficultyLegendary:
		return true
	}
	return false
}

// BossChallengeStatus represents the lifecycle status of a BossChallenge.
type BossChallengeStatus string

const (
	BossChallengeStatusScheduled BossChallengeStatus = "scheduled"
	BossChallengeStatusActive    BossChallengeStatus = "active"
	BossChallengeStatusCompleted BossChallengeStatus = "completed"
	BossChallengeStatusCancelled BossChallengeStatus = "cancelled"
)

// IsValid returns true if the BossChallengeStatus value is recognized.
func (s BossChallengeStatus) IsValid() bool {
	switch s {
	case BossChallengeStatusScheduled, BossChallengeStatusActive,
		BossChallengeStatusCompleted, BossChallengeStatusCancelled:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Crafting Enums
// ---------------------------------------------------------------------------

// MaterialType represents the rarity tier of a crafting material.
type MaterialType string

const (
	MaterialTypeCommon   MaterialType = "common"
	MaterialTypeUncommon MaterialType = "uncommon"
	MaterialTypeRare     MaterialType = "rare"
	MaterialTypeEpic     MaterialType = "epic"
)

// IsValid returns true if the MaterialType value is recognized.
func (m MaterialType) IsValid() bool {
	switch m {
	case MaterialTypeCommon, MaterialTypeUncommon, MaterialTypeRare, MaterialTypeEpic:
		return true
	}
	return false
}

// RecipeOutputType represents what a crafting recipe produces.
type RecipeOutputType string

const (
	RecipeOutputMaterial RecipeOutputType = "material"
	RecipeOutputCard     RecipeOutputType = "card"
	RecipeOutputSkin     RecipeOutputType = "skin"
	RecipeOutputCoins    RecipeOutputType = "coins"
	RecipeOutputXP       RecipeOutputType = "xp"
)

// IsValid returns true if the RecipeOutputType value is recognized.
func (r RecipeOutputType) IsValid() bool {
	switch r {
	case RecipeOutputMaterial, RecipeOutputCard, RecipeOutputSkin,
		RecipeOutputCoins, RecipeOutputXP:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Lootbox Enums
// ---------------------------------------------------------------------------

// LootboxTier represents the quality tier of a lootbox.
type LootboxTier string

const (
	LootboxTierStandard  LootboxTier = "standard"
	LootboxTierPremium   LootboxTier = "premium"
	LootboxTierLegendary LootboxTier = "legendary"
)

// IsValid returns true if the LootboxTier value is recognized.
func (l LootboxTier) IsValid() bool {
	switch l {
	case LootboxTierStandard, LootboxTierPremium, LootboxTierLegendary:
		return true
	}
	return false
}

// LootboxSource represents how a lootbox was obtained.
type LootboxSource string

const (
	LootboxSourceLevelUp    LootboxSource = "level_up"
	LootboxSourceBossReward LootboxSource = "boss_reward"
	LootboxSourceDailyLogin LootboxSource = "daily_login"
	LootboxSourcePurchase   LootboxSource = "purchase"
	LootboxSourceEvent      LootboxSource = "event"
)

// IsValid returns true if the LootboxSource value is recognized.
func (l LootboxSource) IsValid() bool {
	switch l {
	case LootboxSourceLevelUp, LootboxSourceBossReward, LootboxSourceDailyLogin,
		LootboxSourcePurchase, LootboxSourceEvent:
		return true
	}
	return false
}

// LootItemType represents the type of item in a loot table.
type LootItemType string

const (
	LootItemCard     LootItemType = "card"
	LootItemMaterial LootItemType = "material"
	LootItemCoins    LootItemType = "coins"
	LootItemXP       LootItemType = "xp"
	LootItemSkin     LootItemType = "skin"
)

// IsValid returns true if the LootItemType value is recognized.
func (l LootItemType) IsValid() bool {
	switch l {
	case LootItemCard, LootItemMaterial, LootItemCoins, LootItemXP, LootItemSkin:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Boss Challenge Entities
// ---------------------------------------------------------------------------

// BossChallenge represents a scheduled or active boss challenge event.
type BossChallenge struct {
	ID             uuid.UUID           `json:"id"`
	TenantID       uuid.UUID           `json:"tenant_id"`
	BossName       string              `json:"boss_name"`
	TopicNodeID    uuid.UUID           `json:"topic_node_id"`
	DifficultyTier BossDifficultyTier  `json:"difficulty_tier"`
	RequiredScore  int                 `json:"required_score"`
	RewardXP       int                 `json:"reward_xp"`
	RewardCoins    int                 `json:"reward_coins"`
	RewardCardID   *uuid.UUID          `json:"reward_card_id,omitempty"`
	Status         BossChallengeStatus `json:"status"`
	ScheduledAt    time.Time           `json:"scheduled_at"`
	StartsAt       time.Time           `json:"starts_at"`
	EndsAt         time.Time           `json:"ends_at"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
	DeletedAt      *time.Time          `json:"deleted_at,omitempty"`
}

// BossChallengeParticipant records a learner's participation in a boss challenge.
type BossChallengeParticipant struct {
	ID            uuid.UUID  `json:"id"`
	TenantID      uuid.UUID  `json:"tenant_id"`
	ChallengeID   uuid.UUID  `json:"challenge_id"`
	GCID          uuid.UUID  `json:"gcid"`
	Score         int        `json:"score"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	RewardClaimed bool       `json:"reward_claimed"`
	CreatedAt     time.Time  `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Crafting Entities
// ---------------------------------------------------------------------------

// Material represents a crafting material definition.
type Material struct {
	ID           uuid.UUID    `json:"id"`
	TenantID     uuid.UUID    `json:"tenant_id"`
	MaterialName string       `json:"material_name"`
	MaterialType MaterialType `json:"material_type"`
	Description  string       `json:"description"`
	IconURL      string       `json:"icon_url"`
	CreatedAt    time.Time    `json:"created_at"`
	DeletedAt    *time.Time   `json:"deleted_at,omitempty"`
}

// MaterialInventory tracks a learner's quantity of a specific material.
type MaterialInventory struct {
	ID         uuid.UUID  `json:"id"`
	TenantID   uuid.UUID  `json:"tenant_id"`
	GCID       uuid.UUID  `json:"gcid"`
	MaterialID uuid.UUID  `json:"material_id"`
	Quantity   int        `json:"quantity"`
	AcquiredAt time.Time  `json:"acquired_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
}

// RecipeInput describes a material requirement for a crafting recipe.
type RecipeInput struct {
	MaterialID uuid.UUID `json:"material_id"`
	Quantity   int       `json:"quantity"`
}

// CraftingRecipe defines what materials are needed and what is produced.
type CraftingRecipe struct {
	ID               uuid.UUID        `json:"id"`
	TenantID         uuid.UUID        `json:"tenant_id"`
	RecipeName       string           `json:"recipe_name"`
	Description      string           `json:"description"`
	InputMaterials   []RecipeInput    `json:"input_materials"`
	OutputType       RecipeOutputType `json:"output_type"`
	OutputID         *uuid.UUID       `json:"output_id,omitempty"`
	OutputQuantity   int              `json:"output_quantity"`
	CraftTimeSeconds int              `json:"craft_time_seconds"`
	CreatedAt        time.Time        `json:"created_at"`
	DeletedAt        *time.Time       `json:"deleted_at,omitempty"`
}

// CraftResult is the response from executing a crafting recipe.
type CraftResult struct {
	RecipeID       uuid.UUID        `json:"recipe_id"`
	OutputType     RecipeOutputType `json:"output_type"`
	OutputID       *uuid.UUID       `json:"output_id,omitempty"`
	OutputQuantity int              `json:"output_quantity"`
}

// ---------------------------------------------------------------------------
// Lootbox Entities
// ---------------------------------------------------------------------------

// Lootbox represents a lootbox that can be opened for rewards.
type Lootbox struct {
	ID            uuid.UUID              `json:"id"`
	TenantID      uuid.UUID              `json:"tenant_id"`
	GCID          uuid.UUID              `json:"gcid"`
	LootboxTier   LootboxTier            `json:"lootbox_tier"`
	Source        LootboxSource          `json:"source"`
	IsOpened      bool                   `json:"is_opened"`
	OpenedAt      *time.Time             `json:"opened_at,omitempty"`
	RewardSummary map[string]interface{} `json:"reward_summary,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
	DeletedAt     *time.Time             `json:"deleted_at,omitempty"`
}

// LootTable defines a weighted entry in a loot table for a given tier.
type LootTable struct {
	ID          uuid.UUID    `json:"id"`
	TenantID    uuid.UUID    `json:"tenant_id"`
	Tier        string       `json:"tier"`
	ItemType    LootItemType `json:"item_type"`
	ItemID      *uuid.UUID   `json:"item_id,omitempty"`
	QuantityMin int          `json:"quantity_min"`
	QuantityMax int          `json:"quantity_max"`
	Weight      int          `json:"weight"`
	CreatedAt   time.Time    `json:"created_at"`
}

// LootResult is the response from opening a lootbox.
type LootResult struct {
	ItemType LootItemType `json:"item_type"`
	ItemID   *uuid.UUID   `json:"item_id,omitempty"`
	Quantity int          `json:"quantity"`
}

// LoginCalendar tracks a learner's daily login streak.
type LoginCalendar struct {
	ID            uuid.UUID `json:"id"`
	TenantID      uuid.UUID `json:"tenant_id"`
	GCID          uuid.UUID `json:"gcid"`
	LoginDate     time.Time `json:"login_date"`
	StreakDay     int       `json:"streak_day"`
	RewardClaimed bool      `json:"reward_claimed"`
	RewardType    string    `json:"reward_type"`
	RewardValue   int       `json:"reward_value"`
	CreatedAt     time.Time `json:"created_at"`
}

// KnowledgeBounty represents a knowledge challenge posted by a learner.
type KnowledgeBounty struct {
	ID                    uuid.UUID    `json:"id"`
	TenantID              uuid.UUID    `json:"tenant_id"`
	PosterGCID            uuid.UUID    `json:"poster_gcid"`
	AtomID                uuid.UUID    `json:"atom_id"`
	Title                 string       `json:"title"`
	Description           string       `json:"description"`
	CoinReward            int          `json:"coin_reward"`
	ReputationRequirement int          `json:"reputation_requirement"`
	Status                BountyStatus `json:"status"`
	SolverGCID            *uuid.UUID   `json:"solver_gcid,omitempty"`
	SolutionText          *string      `json:"solution_text,omitempty"`
	CreatedAt             time.Time    `json:"created_at"`
	CompletedAt           *time.Time   `json:"completed_at,omitempty"`
	ExpiresAt             time.Time    `json:"expires_at"`
	DeletedAt             *time.Time   `json:"deleted_at,omitempty"`
}

// ---------------------------------------------------------------------------
// Territory Rewards Enums (Phase 60.5)
// ---------------------------------------------------------------------------

// IncomeType represents the type of passive territory income.
type IncomeType string

const (
	IncomeTypeXP    IncomeType = "xp"
	IncomeTypeCoins IncomeType = "coins"
	IncomeTypeStars IncomeType = "stars"
)

// IsValid returns true if the IncomeType value is recognized.
func (i IncomeType) IsValid() bool {
	switch i {
	case IncomeTypeXP, IncomeTypeCoins, IncomeTypeStars:
		return true
	}
	return false
}

// IncomeFrequency represents how often territory income is distributed.
type IncomeFrequency string

const (
	IncomeFrequencyDaily  IncomeFrequency = "daily"
	IncomeFrequencyWeekly IncomeFrequency = "weekly"
)

// IsValid returns true if the IncomeFrequency value is recognized.
func (f IncomeFrequency) IsValid() bool {
	switch f {
	case IncomeFrequencyDaily, IncomeFrequencyWeekly:
		return true
	}
	return false
}

// RankTier represents a seasonal leaderboard reward tier.
type RankTier string

const (
	RankTierTop1  RankTier = "top_1"
	RankTierTop3  RankTier = "top_3"
	RankTierTop10 RankTier = "top_10"
	RankTierTop25 RankTier = "top_25"
	RankTierTop50 RankTier = "top_50"
)

// IsValid returns true if the RankTier value is recognized.
func (r RankTier) IsValid() bool {
	switch r {
	case RankTierTop1, RankTierTop3, RankTierTop10, RankTierTop25, RankTierTop50:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Territory Rewards Entities (Phase 60.5)
// ---------------------------------------------------------------------------

// TerritoryIncomeConfig defines the passive income configuration for territories.
type TerritoryIncomeConfig struct {
	ID                     uuid.UUID       `json:"id"`
	TenantID               uuid.UUID       `json:"tenant_id"`
	IncomeType             IncomeType      `json:"income_type"`
	BaseAmount             int             `json:"base_amount"`
	MultiplierPerTerritory float64         `json:"multiplier_per_territory"`
	Frequency              IncomeFrequency `json:"frequency"`
	CreatedAt              time.Time       `json:"created_at"`
	UpdatedAt              time.Time       `json:"updated_at"`
	DeletedAt              *time.Time      `json:"deleted_at,omitempty"`
}

// TerritoryIncomeLog records a territory income distribution event.
type TerritoryIncomeLog struct {
	ID             uuid.UUID  `json:"id"`
	TenantID       uuid.UUID  `json:"tenant_id"`
	GCID           uuid.UUID  `json:"gcid"`
	IncomeType     IncomeType `json:"income_type"`
	Amount         int        `json:"amount"`
	TerritoryCount int        `json:"territory_count"`
	PeriodStart    time.Time  `json:"period_start"`
	PeriodEnd      time.Time  `json:"period_end"`
	CreatedAt      time.Time  `json:"created_at"`
}

// SeasonalLeaderboardReward defines rewards for seasonal territory leaderboard tiers.
type SeasonalLeaderboardReward struct {
	ID           uuid.UUID `json:"id"`
	TenantID     uuid.UUID `json:"tenant_id"`
	SeasonID     string    `json:"season_id"`
	RankTier     RankTier  `json:"rank_tier"`
	RewardType   string    `json:"reward_type"`
	RewardAmount int       `json:"reward_amount"`
	CreatedAt    time.Time `json:"created_at"`
}

// IncomeDistributeResult is the outcome of a territory income distribution run.
type IncomeDistributeResult struct {
	HoldersProcessed int `json:"holders_processed"`
	TotalDistributed int `json:"total_distributed"`
}

// ---------------------------------------------------------------------------
// Marketplace Enums (Phase 60.8)
// ---------------------------------------------------------------------------

// MarketItemType represents what type of item is listed on the marketplace.
type MarketItemType string

const (
	MarketItemTypeCard     MarketItemType = "card"
	MarketItemTypeMaterial MarketItemType = "material"
	MarketItemTypeLootbox  MarketItemType = "lootbox"
	MarketItemTypeSkin     MarketItemType = "skin"
)

// IsValid returns true if the MarketItemType value is recognized.
func (m MarketItemType) IsValid() bool {
	switch m {
	case MarketItemTypeCard, MarketItemTypeMaterial, MarketItemTypeLootbox, MarketItemTypeSkin:
		return true
	}
	return false
}

// ListingStatus represents the lifecycle status of a market listing.
type ListingStatus string

const (
	ListingStatusActive    ListingStatus = "active"
	ListingStatusSold      ListingStatus = "sold"
	ListingStatusExpired   ListingStatus = "expired"
	ListingStatusCancelled ListingStatus = "cancelled"
)

// IsValid returns true if the ListingStatus value is recognized.
func (s ListingStatus) IsValid() bool {
	switch s {
	case ListingStatusActive, ListingStatusSold, ListingStatusExpired, ListingStatusCancelled:
		return true
	}
	return false
}

// OrderStatus represents the status of a market order.
type OrderStatus string

const (
	OrderStatusPending   OrderStatus = "pending"
	OrderStatusCompleted OrderStatus = "completed"
	OrderStatusFailed    OrderStatus = "failed"
	OrderStatusRefunded  OrderStatus = "refunded"
)

// IsValid returns true if the OrderStatus value is recognized.
func (s OrderStatus) IsValid() bool {
	switch s {
	case OrderStatusPending, OrderStatusCompleted, OrderStatusFailed, OrderStatusRefunded:
		return true
	}
	return false
}

// PaymentMethod represents the currency used to pay for a market listing.
type PaymentMethod string

const (
	PaymentMethodCoins PaymentMethod = "coins"
	PaymentMethodStars PaymentMethod = "stars"
)

// IsValid returns true if the PaymentMethod value is recognized.
func (p PaymentMethod) IsValid() bool {
	switch p {
	case PaymentMethodCoins, PaymentMethodStars:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Currency & Social Economy Enums (Phase 60.8)
// ---------------------------------------------------------------------------

// CurrencyType represents the type of currency for conversions and transfers.
type CurrencyType string

const (
	CurrencyTypeCoins CurrencyType = "coins"
	CurrencyTypeStars CurrencyType = "stars"
)

// IsValid returns true if the CurrencyType value is recognized.
func (c CurrencyType) IsValid() bool {
	switch c {
	case CurrencyTypeCoins, CurrencyTypeStars:
		return true
	}
	return false
}

// TransferStatus represents the status of a P2P currency transfer.
type TransferStatus string

const (
	TransferStatusPending   TransferStatus = "pending"
	TransferStatusCompleted TransferStatus = "completed"
	TransferStatusRejected  TransferStatus = "rejected"
)

// IsValid returns true if the TransferStatus value is recognized.
func (s TransferStatus) IsValid() bool {
	switch s {
	case TransferStatusPending, TransferStatusCompleted, TransferStatusRejected:
		return true
	}
	return false
}

// PoolType represents the type of group fund pool.
type PoolType string

const (
	PoolTypeBossChallenge PoolType = "boss_challenge"
	PoolTypeCoOpReward    PoolType = "co_op_reward"
	PoolTypeSeasonal      PoolType = "seasonal"
)

// IsValid returns true if the PoolType value is recognized.
func (p PoolType) IsValid() bool {
	switch p {
	case PoolTypeBossChallenge, PoolTypeCoOpReward, PoolTypeSeasonal:
		return true
	}
	return false
}

// PoolStatus represents the status of a group fund pool.
type PoolStatus string

const (
	PoolStatusCollecting  PoolStatus = "collecting"
	PoolStatusDistributed PoolStatus = "distributed"
	PoolStatusCancelled   PoolStatus = "cancelled"
)

// IsValid returns true if the PoolStatus value is recognized.
func (s PoolStatus) IsValid() bool {
	switch s {
	case PoolStatusCollecting, PoolStatusDistributed, PoolStatusCancelled:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Marketplace Entities (Phase 60.8)
// ---------------------------------------------------------------------------

// MarketListing represents an item listed for sale on the marketplace.
type MarketListing struct {
	ID         uuid.UUID      `json:"id"`
	TenantID   uuid.UUID      `json:"tenant_id"`
	SellerGCID uuid.UUID      `json:"seller_gcid"`
	ItemType   MarketItemType `json:"item_type"`
	ItemID     uuid.UUID      `json:"item_id"`
	Quantity   int            `json:"quantity"`
	PriceCoins int            `json:"price_coins"`
	PriceStars *int           `json:"price_stars,omitempty"`
	Status     ListingStatus  `json:"status"`
	ListedAt   time.Time      `json:"listed_at"`
	ExpiresAt  time.Time      `json:"expires_at"`
	SoldAt     *time.Time     `json:"sold_at,omitempty"`
	BuyerGCID  *uuid.UUID     `json:"buyer_gcid,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	DeletedAt  *time.Time     `json:"deleted_at,omitempty"`
}

// MarketOrder represents a purchase order for a market listing.
type MarketOrder struct {
	ID            uuid.UUID     `json:"id"`
	TenantID      uuid.UUID     `json:"tenant_id"`
	ListingID     uuid.UUID     `json:"listing_id"`
	BuyerGCID     uuid.UUID     `json:"buyer_gcid"`
	PaymentMethod PaymentMethod `json:"payment_method"`
	AmountPaid    int           `json:"amount_paid"`
	Status        OrderStatus   `json:"status"`
	CreatedAt     time.Time     `json:"created_at"`
	CompletedAt   *time.Time    `json:"completed_at,omitempty"`
}

// MarketTransaction is an immutable audit record of a completed market sale.
type MarketTransaction struct {
	ID         uuid.UUID `json:"id"`
	TenantID   uuid.UUID `json:"tenant_id"`
	OrderID    uuid.UUID `json:"order_id"`
	SellerGCID uuid.UUID `json:"seller_gcid"`
	BuyerGCID  uuid.UUID `json:"buyer_gcid"`
	ItemType   string    `json:"item_type"`
	ItemID     uuid.UUID `json:"item_id"`
	Price      int       `json:"price"`
	FeeAmount  int       `json:"fee_amount"`
	NetAmount  int       `json:"net_amount"`
	CreatedAt  time.Time `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Currency Converter Entities (Phase 60.8)
// ---------------------------------------------------------------------------

// CurrencyConversion records a currency exchange operation.
type CurrencyConversion struct {
	ID           uuid.UUID    `json:"id"`
	TenantID     uuid.UUID    `json:"tenant_id"`
	GCID         uuid.UUID    `json:"gcid"`
	FromCurrency CurrencyType `json:"from_currency"`
	ToCurrency   CurrencyType `json:"to_currency"`
	FromAmount   int          `json:"from_amount"`
	ToAmount     int          `json:"to_amount"`
	ExchangeRate float64      `json:"exchange_rate"`
	CreatedAt    time.Time    `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Social Economy Entities (Phase 60.8)
// ---------------------------------------------------------------------------

// CurrencyTransfer records a P2P currency gift/transfer.
type CurrencyTransfer struct {
	ID           uuid.UUID      `json:"id"`
	TenantID     uuid.UUID      `json:"tenant_id"`
	SenderGCID   uuid.UUID      `json:"sender_gcid"`
	ReceiverGCID uuid.UUID      `json:"receiver_gcid"`
	Currency     CurrencyType   `json:"currency"`
	Amount       int            `json:"amount"`
	Message      *string        `json:"message,omitempty"`
	Status       TransferStatus `json:"status"`
	CreatedAt    time.Time      `json:"created_at"`
	CompletedAt  *time.Time     `json:"completed_at,omitempty"`
}

// GroupFundPool represents a shared fund pool for group activities.
type GroupFundPool struct {
	ID            uuid.UUID  `json:"id"`
	TenantID      uuid.UUID  `json:"tenant_id"`
	PoolName      string     `json:"pool_name"`
	PoolType      PoolType   `json:"pool_type"`
	TargetAmount  int        `json:"target_amount"`
	CurrentAmount int        `json:"current_amount"`
	Status        PoolStatus `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// GroupFundContribution records a contribution to a group fund pool.
type GroupFundContribution struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	PoolID    uuid.UUID `json:"pool_id"`
	GCID      uuid.UUID `json:"gcid"`
	Amount    int       `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
}
