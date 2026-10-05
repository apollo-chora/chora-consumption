package gamification

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// NewTerritoryRewardService creates a new TerritoryRewardService.
func NewTerritoryRewardService(
	incomeRepo TerritoryIncomeRepository,
	seasonalRepo SeasonalRewardRepository,
	econRepo EconomyConfigRepository,
	publisher EventPublisher,
) *TerritoryRewardService {
	return &TerritoryRewardService{
		incomeRepo:   incomeRepo,
		seasonalRepo: seasonalRepo,
		econRepo:     econRepo,
		publisher:    publisher,
	}
}

// TerritoryRewardService handles territory-based passive income and seasonal rewards.
type TerritoryRewardService struct {
	incomeRepo   TerritoryIncomeRepository
	seasonalRepo SeasonalRewardRepository
	econRepo     EconomyConfigRepository
	publisher    EventPublisher
}

// CalculateIncome computes the income for a given config and territory count.
// Formula: baseAmount * (1 + (territoryCount-1) * multiplier) for count >= 1, 0 for count 0.
func (s *TerritoryRewardService) CalculateIncome(config *TerritoryIncomeConfig, territoryCount int) int {
	if territoryCount <= 0 {
		return 0
	}
	multiplier := 1.0 + float64(territoryCount-1)*config.MultiplierPerTerritory
	return int(float64(config.BaseAmount) * multiplier)
}

// DistributeDaily distributes daily territory income to all holders for a tenant.
func (s *TerritoryRewardService) DistributeDaily(
	ctx context.Context,
	tenantID uuid.UUID,
	holders []TerritoryHolder,
) (*IncomeDistributeResult, error) {
	configs, err := s.incomeRepo.ListConfigsByTenant(ctx, tenantID, IncomeFrequencyDaily)
	if err != nil {
		return nil, err
	}

	if len(configs) == 0 {
		return &IncomeDistributeResult{}, nil
	}

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	tomorrow := today.AddDate(0, 0, 1)

	result := &IncomeDistributeResult{}

	for _, holder := range holders {
		if holder.TerritoryCount <= 0 {
			continue
		}

		for _, config := range configs {
			amount := s.CalculateIncome(config, holder.TerritoryCount)
			if amount <= 0 {
				continue
			}

			log := &TerritoryIncomeLog{
				ID:             uuid.Must(uuid.NewV7()),
				TenantID:       tenantID,
				GCID:           holder.GCID,
				IncomeType:     config.IncomeType,
				Amount:         amount,
				TerritoryCount: holder.TerritoryCount,
				PeriodStart:    today,
				PeriodEnd:      tomorrow,
				CreatedAt:      now,
			}

			if err := s.incomeRepo.CreateLog(ctx, log); err != nil {
				continue
			}

			result.TotalDistributed += amount
		}

		result.HoldersProcessed++
	}

	if result.HoldersProcessed > 0 {
		event := NewDomainEvent(
			EventTerritoryIncomeDistributed,
			tenantID,
			nil,
			uuid.Must(uuid.NewV7()),
			AggregateTerritoryIncomeLog,
			map[string]interface{}{
				"holders_processed": result.HoldersProcessed,
				"total_distributed": result.TotalDistributed,
				"frequency":         string(IncomeFrequencyDaily),
			},
		)
		_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)
	}

	return result, nil
}

// GetSeasonalRewards returns the seasonal leaderboard rewards for a given season.
func (s *TerritoryRewardService) GetSeasonalRewards(
	ctx context.Context,
	tenantID uuid.UUID,
	seasonID string,
) ([]*SeasonalLeaderboardReward, error) {
	return s.seasonalRepo.ListBySeason(ctx, tenantID, seasonID)
}

// ConfigureIncome creates a new territory income configuration.
func (s *TerritoryRewardService) ConfigureIncome(
	ctx context.Context,
	tenantID uuid.UUID,
	incomeType IncomeType,
	baseAmount int,
	multiplier float64,
	frequency IncomeFrequency,
) (*TerritoryIncomeConfig, error) {
	if !incomeType.IsValid() {
		return nil, ErrValidationFailed
	}
	if !frequency.IsValid() {
		return nil, ErrValidationFailed
	}

	now := time.Now().UTC()
	config := &TerritoryIncomeConfig{
		ID:                     uuid.Must(uuid.NewV7()),
		TenantID:               tenantID,
		IncomeType:             incomeType,
		BaseAmount:             baseAmount,
		MultiplierPerTerritory: multiplier,
		Frequency:              frequency,
		CreatedAt:              now,
		UpdatedAt:              now,
	}

	if err := s.incomeRepo.CreateConfig(ctx, config); err != nil {
		return nil, err
	}

	return config, nil
}
