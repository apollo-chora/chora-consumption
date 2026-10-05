package gamification

import "errors"

// Sentinel errors for the gamification domain.
// All error codes use the GAMIFICATION_ prefix.

// Skin errors.
var (
	ErrSkinNotFound        = errors.New("GAMIFICATION_SKIN_NOT_FOUND")
	ErrSkinAlreadyOwned    = errors.New("GAMIFICATION_SKIN_ALREADY_OWNED")
	ErrSkinNotEquippable   = errors.New("GAMIFICATION_SKIN_NOT_EQUIPPABLE")
	ErrSlotAlreadyEquipped = errors.New("GAMIFICATION_SLOT_ALREADY_EQUIPPED")
)

// Coin errors.
var (
	ErrInsufficientBalance    = errors.New("GAMIFICATION_INSUFFICIENT_BALANCE")
	ErrInsufficientReputation = errors.New("GAMIFICATION_INSUFFICIENT_REPUTATION")
)

// Legendary skin errors.
var (
	ErrLegendarySkinOccupied   = errors.New("GAMIFICATION_LEGENDARY_SKIN_OCCUPIED")
	ErrLegendaryTransferFailed = errors.New("GAMIFICATION_LEGENDARY_TRANSFER_FAILED")
)

// Bounty errors.
var (
	ErrBountyNotFound         = errors.New("GAMIFICATION_BOUNTY_NOT_FOUND")
	ErrBountyNotOpen          = errors.New("GAMIFICATION_BOUNTY_NOT_OPEN")
	ErrBountyAlreadyCompleted = errors.New("GAMIFICATION_BOUNTY_ALREADY_COMPLETED")
	ErrBountySelfSolve        = errors.New("GAMIFICATION_BOUNTY_SELF_SOLVE")
)

// League errors.
var ErrLeagueNotFound = errors.New("GAMIFICATION_LEAGUE_NOT_FOUND")

// Badge errors.
var ErrBadgeAlreadyEarned = errors.New("GAMIFICATION_BADGE_ALREADY_EARNED")

// Economy config errors.
var (
	ErrEconomyConfigNotFound        = errors.New("GAMIFICATION_ECONOMY_CONFIG_NOT_FOUND")
	ErrEconomyConfigCategoryInvalid = errors.New("GAMIFICATION_ECONOMY_CONFIG_CATEGORY_INVALID")
	ErrEconomyConfigKeyTooLong      = errors.New("GAMIFICATION_ECONOMY_CONFIG_KEY_TOO_LONG")
	ErrEconomyConfigNoHistory       = errors.New("GAMIFICATION_ECONOMY_CONFIG_NO_HISTORY")
)

// Economy snapshot errors.
var (
	ErrSnapshotNotFound      = errors.New("GAMIFICATION_SNAPSHOT_NOT_FOUND")
	ErrSnapshotAlreadyExists = errors.New("GAMIFICATION_SNAPSHOT_ALREADY_EXISTS")
)

// Trading card errors.
var (
	ErrCardNotFound          = errors.New("GAMIFICATION_CARD_NOT_FOUND")
	ErrCardNotRecyclable     = errors.New("GAMIFICATION_CARD_NOT_RECYCLABLE")
	ErrCardNotOwned          = errors.New("GAMIFICATION_CARD_NOT_OWNED")
	ErrCardNotInCollection   = errors.New("GAMIFICATION_CARD_NOT_IN_COLLECTION")
	ErrCardSetNotFound       = errors.New("GAMIFICATION_CARD_SET_NOT_FOUND")
	ErrSetCompletionNotFound = errors.New("GAMIFICATION_SET_COMPLETION_NOT_FOUND")
)

// Trade errors.
var (
	ErrTradeNotFound    = errors.New("GAMIFICATION_TRADE_NOT_FOUND")
	ErrTradeNotProposed = errors.New("GAMIFICATION_TRADE_NOT_PROPOSED")
	ErrTradeNotReceiver = errors.New("GAMIFICATION_TRADE_NOT_RECEIVER")
	ErrTradeNotOfferer  = errors.New("GAMIFICATION_TRADE_NOT_OFFERER")
	ErrSelfTradeBlocked = errors.New("GAMIFICATION_SELF_TRADE_BLOCKED")
)

// Boss challenge errors.
var (
	ErrBossChallengeNotFound     = errors.New("GAMIFICATION_BOSS_CHALLENGE_NOT_FOUND")
	ErrBossChallengeNotScheduled = errors.New("GAMIFICATION_BOSS_CHALLENGE_NOT_SCHEDULED")
	ErrBossChallengeNotActive    = errors.New("GAMIFICATION_BOSS_CHALLENGE_NOT_ACTIVE")
)

// Crafting errors.
var (
	ErrCraftingRecipeNotFound = errors.New("GAMIFICATION_CRAFTING_RECIPE_NOT_FOUND")
	ErrInsufficientMaterials  = errors.New("GAMIFICATION_INSUFFICIENT_MATERIALS")
)

// Lootbox errors.
var (
	ErrLootboxNotFound      = errors.New("GAMIFICATION_LOOTBOX_NOT_FOUND")
	ErrLootboxAlreadyOpened = errors.New("GAMIFICATION_LOOTBOX_ALREADY_OPENED")
	ErrLootTableEmpty       = errors.New("GAMIFICATION_LOOT_TABLE_EMPTY")
	// ErrLootTableWeightsInvalid marks a loot table that carries entries but
	// no usable draw range (every weight zero, or a negative weight). It is a
	// tenant misconfiguration, distinct from an empty table, and it used to
	// reach rand.Intn(0) and panic.
	ErrLootTableWeightsInvalid = errors.New("GAMIFICATION_LOOT_TABLE_WEIGHTS_INVALID")
)

// Login calendar errors.
var ErrDailyCheckinAlreadyClaimed = errors.New("GAMIFICATION_DAILY_CHECKIN_ALREADY_CLAIMED")

// Marketplace errors.
var (
	ErrListingNotFound     = errors.New("GAMIFICATION_LISTING_NOT_FOUND")
	ErrListingNotAvailable = errors.New("GAMIFICATION_LISTING_NOT_AVAILABLE")
	ErrListingNotOwner     = errors.New("GAMIFICATION_LISTING_NOT_OWNER")
	ErrCannotBuyOwnListing = errors.New("GAMIFICATION_CANNOT_BUY_OWN_LISTING")
)

// Currency conversion errors.
var ErrSameCurrencyConversion = errors.New("GAMIFICATION_SAME_CURRENCY_CONVERSION")

// Currency transfer errors.
var (
	ErrSelfTransferBlocked        = errors.New("GAMIFICATION_SELF_TRANSFER_BLOCKED")
	ErrDailyTransferLimitExceeded = errors.New("GAMIFICATION_DAILY_TRANSFER_LIMIT_EXCEEDED")
)

// Group fund errors.
var (
	ErrPoolNotFound      = errors.New("GAMIFICATION_POOL_NOT_FOUND")
	ErrPoolNotCollecting = errors.New("GAMIFICATION_POOL_NOT_COLLECTING")
)

// Generic errors.
var (
	ErrNotFound         = errors.New("GAMIFICATION_NOT_FOUND")
	ErrForbidden        = errors.New("GAMIFICATION_FORBIDDEN")
	ErrUnauthorized     = errors.New("GAMIFICATION_UNAUTHORIZED")
	ErrValidationFailed = errors.New("GAMIFICATION_VALIDATION_FAILED")
)
