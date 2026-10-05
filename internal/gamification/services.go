package gamification

import (
	"context"
	"io"
	"time"

	"github.com/google/uuid"
)

// NewSkinService creates a new SkinService.
func NewSkinService(
	skinRepo DigitalSkinRepository,
	awardRepo SkinAwardRepository,
	equipRepo SkinEquipmentRepository,
	exportRepo SkinExportRepository,
	legendaryRepo LegendarySkinRepository,
	publisher EventPublisher,
) *SkinService {
	return &SkinService{
		skinRepo:      skinRepo,
		awardRepo:     awardRepo,
		equipRepo:     equipRepo,
		exportRepo:    exportRepo,
		legendaryRepo: legendaryRepo,
		publisher:     publisher,
	}
}

// SkinService handles DigitalSkin lifecycle, awards, and equipment.
type SkinService struct {
	skinRepo      DigitalSkinRepository
	awardRepo     SkinAwardRepository
	equipRepo     SkinEquipmentRepository
	exportRepo    SkinExportRepository
	legendaryRepo LegendarySkinRepository
	publisher     EventPublisher
}

// NewCoinService creates a new CoinService.
func NewCoinService(repo CoinAccountRepository, publisher EventPublisher) *CoinService {
	return &CoinService{repo: repo, publisher: publisher}
}

// CoinService handles CoinAccount operations.
type CoinService struct {
	repo      CoinAccountRepository
	publisher EventPublisher
}

// NewLeagueService creates a new LeagueService.
func NewLeagueService(repo LeagueRepository, badgeRepo TopicBadgeRepository, publisher EventPublisher) *LeagueService {
	return &LeagueService{repo: repo, badgeRepo: badgeRepo, publisher: publisher}
}

// LeagueService handles League standings and promotions.
type LeagueService struct {
	repo      LeagueRepository
	badgeRepo TopicBadgeRepository
	publisher EventPublisher
}

// NewEconomyConfigService creates a new EconomyConfigService.
func NewEconomyConfigService(repo EconomyConfigRepository, publisher EventPublisher) *EconomyConfigService {
	return &EconomyConfigService{repo: repo, publisher: publisher}
}

// EconomyConfigService handles EconomyConfig CRUD, history, and rollback.
type EconomyConfigService struct {
	repo      EconomyConfigRepository
	publisher EventPublisher
}

// NewBountyService creates a new BountyService.
func NewBountyService(repo KnowledgeBountyRepository, coinRepo CoinAccountRepository, publisher EventPublisher) *BountyService {
	return &BountyService{repo: repo, coinRepo: coinRepo, publisher: publisher}
}

// BountyService handles KnowledgeBounty lifecycle.
type BountyService struct {
	repo      KnowledgeBountyRepository
	coinRepo  CoinAccountRepository
	publisher EventPublisher
}

// NewCardService creates a new CardService.
func NewCardService(
	cardRepo TradingCardRepository,
	instanceRepo CardInstanceRepository,
	econRepo EconomyConfigRepository,
	publisher EventPublisher,
) *CardService {
	return &CardService{
		cardRepo:     cardRepo,
		instanceRepo: instanceRepo,
		econRepo:     econRepo,
		publisher:    publisher,
	}
}

// CardService handles TradingCard awarding and recycling.
type CardService struct {
	cardRepo     TradingCardRepository
	instanceRepo CardInstanceRepository
	econRepo     EconomyConfigRepository
	publisher    EventPublisher
}

// NewCardTradeService creates a new CardTradeService.
func NewCardTradeService(
	tradeRepo CardTradeRepository,
	instanceRepo CardInstanceRepository,
	historyRepo CardTradeHistoryRepository,
	publisher EventPublisher,
) *CardTradeService {
	return &CardTradeService{
		tradeRepo:    tradeRepo,
		instanceRepo: instanceRepo,
		historyRepo:  historyRepo,
		publisher:    publisher,
	}
}

// CardTradeService handles peer-to-peer card trading.
type CardTradeService struct {
	tradeRepo    CardTradeRepository
	instanceRepo CardInstanceRepository
	historyRepo  CardTradeHistoryRepository
	publisher    EventPublisher
}

// NewCardSetService creates a new CardSetService.
func NewCardSetService(
	setRepo CardSetRepository,
	instanceRepo CardInstanceRepository,
	completionRepo CardSetCompletionRepository,
	publisher EventPublisher,
) *CardSetService {
	return &CardSetService{
		setRepo:        setRepo,
		instanceRepo:   instanceRepo,
		completionRepo: completionRepo,
		publisher:      publisher,
	}
}

// CardSetService handles card set completion detection.
type CardSetService struct {
	setRepo        CardSetRepository
	instanceRepo   CardInstanceRepository
	completionRepo CardSetCompletionRepository
	publisher      EventPublisher
}

// NewBossChallengeService creates a new BossChallengeService.
func NewBossChallengeService(
	challengeRepo BossChallengeRepository,
	participantRepo BossChallengeParticipantRepository,
	publisher EventPublisher,
) *BossChallengeService {
	return &BossChallengeService{
		challengeRepo:   challengeRepo,
		participantRepo: participantRepo,
		publisher:       publisher,
	}
}

// BossChallengeService handles BossChallenge lifecycle and reward distribution.
type BossChallengeService struct {
	challengeRepo   BossChallengeRepository
	participantRepo BossChallengeParticipantRepository
	publisher       EventPublisher
}

// NewCraftingService creates a new CraftingService.
func NewCraftingService(
	materialRepo MaterialRepository,
	inventoryRepo MaterialInventoryRepository,
	recipeRepo CraftingRecipeRepository,
	publisher EventPublisher,
) *CraftingService {
	return &CraftingService{
		materialRepo:  materialRepo,
		inventoryRepo: inventoryRepo,
		recipeRepo:    recipeRepo,
		publisher:     publisher,
	}
}

// CraftingService handles crafting recipe execution.
type CraftingService struct {
	materialRepo  MaterialRepository
	inventoryRepo MaterialInventoryRepository
	recipeRepo    CraftingRecipeRepository
	publisher     EventPublisher
}

// NewLootboxService creates a new LootboxService.
func NewLootboxService(
	lootboxRepo LootboxRepository,
	lootTableRepo LootTableRepository,
	publisher EventPublisher,
) *LootboxService {
	return &LootboxService{
		lootboxRepo:   lootboxRepo,
		lootTableRepo: lootTableRepo,
		publisher:     publisher,
	}
}

// LootboxService handles lootbox granting and opening.
type LootboxService struct {
	lootboxRepo   LootboxRepository
	lootTableRepo LootTableRepository
	publisher     EventPublisher
	// entropy is the source every loot draw reads from. The constructor
	// leaves it nil and entropySource resolves to crypto/rand.Reader; tests
	// inject a reader to exercise the fail-loud path. Mirrors the seam in
	// internal/domain/growth.RollBreedWithRand.
	entropy io.Reader
}

// NewLoginCalendarService creates a new LoginCalendarService.
func NewLoginCalendarService(
	calendarRepo LoginCalendarRepository,
	lootboxSvc *LootboxService,
	publisher EventPublisher,
) *LoginCalendarService {
	return &LoginCalendarService{
		calendarRepo: calendarRepo,
		lootboxSvc:   lootboxSvc,
		publisher:    publisher,
	}
}

// LoginCalendarService handles daily login check-ins and streak tracking.
type LoginCalendarService struct {
	calendarRepo LoginCalendarRepository
	lootboxSvc   *LootboxService
	publisher    EventPublisher
}

// ---------------------------------------------------------------------------
// EconomyDrainService — batch drain mechanics
// ---------------------------------------------------------------------------

// NewEconomyDrainService creates a new EconomyDrainService.
func NewEconomyDrainService(
	coinRepo CoinAccountRepository,
	econRepo EconomyConfigRepository,
	snapRepo EconomySnapshotRepository,
	publisher EventPublisher,
) *EconomyDrainService {
	return &EconomyDrainService{
		coinRepo:  coinRepo,
		econRepo:  econRepo,
		snapRepo:  snapRepo,
		publisher: publisher,
	}
}

// EconomyDrainService handles periodic economy drain operations.
type EconomyDrainService struct {
	coinRepo  CoinAccountRepository
	econRepo  EconomyConfigRepository
	snapRepo  EconomySnapshotRepository
	publisher EventPublisher
}

// ExecuteTerritoryMaintenanceDrain deducts daily maintenance cost from territory
// holders. Skip if balance < cost (never go negative).
func (s *EconomyDrainService) ExecuteTerritoryMaintenanceDrain(
	ctx context.Context,
	tenantID uuid.UUID,
	holders []TerritoryHolder,
) (*DrainResult, error) {
	config, err := s.econRepo.GetByTenantCategoryKey(ctx, nil, ConfigCategoryStoreLimits, "territory_maintenance_cost")
	if err != nil {
		return nil, err
	}

	costPerTerritory := int64(5) // default
	if v, ok := config.ConfigValue["daily_cost_per_territory"].(float64); ok {
		costPerTerritory = int64(v)
	}

	result := &DrainResult{}
	for _, h := range holders {
		totalCost := costPerTerritory * int64(h.TerritoryCount)

		account, err := s.coinRepo.GetByGCID(ctx, tenantID, h.GCID)
		if err != nil {
			continue
		}

		if account.Balance < totalCost {
			result.AccountsSkipped++
			continue
		}

		account.Balance -= totalCost
		account.LifetimeSpent += totalCost
		if err := s.coinRepo.UpdateBalance(ctx, account); err != nil {
			continue
		}

		tx := &CoinTransaction{
			ID:              uuid.Must(uuid.NewV7()),
			TenantID:        tenantID,
			CoinAccountID:   account.ID,
			Amount:          totalCost,
			TransactionType: CoinTransactionTypeSpent,
			Reason:          "territory_maintenance",
			CreatedAt:       time.Now().UTC(),
		}
		_ = s.coinRepo.CreateTransaction(ctx, tx)

		result.AccountsProcessed++
		result.TotalDrained += totalCost
	}

	if result.AccountsProcessed > 0 {
		event := NewDomainEvent(
			EventEconomyDrainExecuted,
			tenantID,
			nil,
			uuid.Must(uuid.NewV7()),
			AggregateEconomySnapshot,
			map[string]interface{}{
				"drain_type":         "territory_maintenance",
				"accounts_processed": result.AccountsProcessed,
				"accounts_skipped":   result.AccountsSkipped,
				"total_drained":      result.TotalDrained,
			},
		)
		_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)
	}

	return result, nil
}

// ExecuteMaterialExpiry marks materials past shelf-life as expired (soft-delete).
func (s *EconomyDrainService) ExecuteMaterialExpiry(
	ctx context.Context,
	tenantID uuid.UUID,
	materials []MaterialInstance,
) (*ExpiryResult, error) {
	config, err := s.econRepo.GetByTenantCategoryKey(ctx, nil, ConfigCategoryMaterialDropRates, "material_shelf_life")
	if err != nil {
		return nil, err
	}

	shelfLifeDays := 30 // default
	if v, ok := config.ConfigValue["shelf_life_days"].(float64); ok {
		shelfLifeDays = int(v)
	}

	now := time.Now().UTC()
	cutoff := now.AddDate(0, 0, -shelfLifeDays)

	result := &ExpiryResult{MaterialsChecked: len(materials)}
	expiredIDs := make([]uuid.UUID, 0)

	for i := range materials {
		if materials[i].AcquiredAt.Before(cutoff) && materials[i].DeletedAt == nil {
			nowCopy := now
			materials[i].DeletedAt = &nowCopy
			expiredIDs = append(expiredIDs, materials[i].ID)
			result.MaterialsExpired++
		}
	}

	if result.MaterialsExpired > 0 {
		event := NewDomainEvent(
			EventMaterialExpired,
			tenantID,
			nil,
			uuid.Must(uuid.NewV7()),
			AggregateEconomySnapshot,
			map[string]interface{}{
				"materials_expired": result.MaterialsExpired,
				"materials_checked": result.MaterialsChecked,
			},
		)
		_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)
	}

	return result, nil
}

// ExecuteELODecay reduces ELO rating for players inactive past the threshold.
// Rating never goes below ELORatingFloor (800).
func (s *EconomyDrainService) ExecuteELODecay(
	ctx context.Context,
	tenantID uuid.UUID,
	players []ELOPlayer,
) (*DecayResult, error) {
	config, err := s.econRepo.GetByTenantCategoryKey(ctx, nil, ConfigCategoryEloSettings, "elo_inactivity_decay")
	if err != nil {
		return nil, err
	}

	inactiveDays := 14 // default
	decayAmount := 25  // default
	if v, ok := config.ConfigValue["inactive_days_threshold"].(float64); ok {
		inactiveDays = int(v)
	}
	if v, ok := config.ConfigValue["decay_amount"].(float64); ok {
		decayAmount = int(v)
	}

	now := time.Now().UTC()
	cutoff := now.AddDate(0, 0, -inactiveDays)

	result := &DecayResult{PlayersChecked: len(players)}

	for i := range players {
		if players[i].LastDuelAt.Before(cutoff) && players[i].Rating > ELORatingFloor {
			actualDecay := decayAmount
			if players[i].Rating-actualDecay < ELORatingFloor {
				actualDecay = players[i].Rating - ELORatingFloor
			}
			if actualDecay > 0 {
				players[i].Rating -= actualDecay
				result.PlayersDecayed++
				result.TotalRatingLost += actualDecay
			}
		}
	}

	if result.PlayersDecayed > 0 {
		event := NewDomainEvent(
			EventELODecayed,
			tenantID,
			nil,
			uuid.Must(uuid.NewV7()),
			AggregateEconomySnapshot,
			map[string]interface{}{
				"players_decayed":   result.PlayersDecayed,
				"players_checked":   result.PlayersChecked,
				"total_rating_lost": result.TotalRatingLost,
			},
		)
		_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// EconomyHealthService — snapshot capture and retrieval
// ---------------------------------------------------------------------------

// NewEconomyHealthService creates a new EconomyHealthService.
func NewEconomyHealthService(
	snapRepo EconomySnapshotRepository,
	coinRepo CoinAccountRepository,
) *EconomyHealthService {
	return &EconomyHealthService{
		snapRepo: snapRepo,
		coinRepo: coinRepo,
	}
}

// EconomyHealthService captures and retrieves economy health snapshots.
type EconomyHealthService struct {
	snapRepo EconomySnapshotRepository
	coinRepo CoinAccountRepository
}

// CaptureSnapshot captures a point-in-time economy health snapshot for a tenant.
func (s *EconomyHealthService) CaptureSnapshot(
	ctx context.Context,
	tenantID uuid.UUID,
	snapshotDate time.Time,
) (*EconomySnapshot, error) {
	totalCoins, err := s.coinRepo.SumBalances(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	activeAccounts, err := s.coinRepo.CountActiveAccounts(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	dailyMinted, err := s.coinRepo.SumDailyMinted(ctx, tenantID, snapshotDate)
	if err != nil {
		return nil, err
	}
	dailyDrained, err := s.coinRepo.SumDailyDrained(ctx, tenantID, snapshotDate)
	if err != nil {
		return nil, err
	}

	inflationRate := s.CalculateInflationRate(dailyMinted, dailyDrained, totalCoins)

	snap := &EconomySnapshot{
		ID:               uuid.Must(uuid.NewV7()),
		TenantID:         tenantID,
		SnapshotDate:     snapshotDate,
		TotalCoins:       totalCoins,
		DailyMinted:      dailyMinted,
		DailyDrained:     dailyDrained,
		ActiveAccounts:   activeAccounts,
		InflationRatePct: inflationRate,
		CreatedAt:        time.Now().UTC(),
	}

	if err := s.snapRepo.Create(ctx, snap); err != nil {
		return nil, err
	}

	return snap, nil
}

// GetLatestSnapshot returns the most recent snapshot for a tenant.
func (s *EconomyHealthService) GetLatestSnapshot(
	ctx context.Context,
	tenantID uuid.UUID,
) (*EconomySnapshot, error) {
	return s.snapRepo.GetLatestByTenant(ctx, tenantID)
}

// CalculateInflationRate computes: (minted - drained) / total * 100.
// Returns 0 if totalCoins is zero to avoid division by zero.
func (s *EconomyHealthService) CalculateInflationRate(minted, drained, totalCoins int64) float64 {
	if totalCoins == 0 {
		return 0
	}
	return float64(minted-drained) / float64(totalCoins) * 100
}
