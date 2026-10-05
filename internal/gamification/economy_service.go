package gamification

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const maxConfigKeyLength = 64

// GetEffectiveConfig returns merged platform defaults + tenant overrides for a category.
func (s *EconomyConfigService) GetEffectiveConfig(ctx context.Context, tenantID uuid.UUID, category ConfigCategory) ([]*EconomyConfig, error) {
	if !category.IsValid() {
		return nil, ErrEconomyConfigCategoryInvalid
	}
	return s.repo.GetEffective(ctx, tenantID, category)
}

// CreateConfig persists a new EconomyConfig entry.
func (s *EconomyConfigService) CreateConfig(ctx context.Context, config *EconomyConfig) error {
	if !config.ConfigCategory.IsValid() {
		return ErrEconomyConfigCategoryInvalid
	}
	if len(config.ConfigKey) > maxConfigKeyLength {
		return ErrEconomyConfigKeyTooLong
	}

	config.ID = uuid.Must(uuid.NewV7())
	now := time.Now().UTC()
	config.CreatedAt = now
	config.UpdatedAt = now

	return s.repo.Create(ctx, config)
}

// UpdateConfig updates an economy config value, records history, and publishes an event.
func (s *EconomyConfigService) UpdateConfig(ctx context.Context, configID uuid.UUID, newValue map[string]interface{}, updatedByGCID uuid.UUID, reason string) (*EconomyConfig, error) {
	existing, err := s.repo.GetByID(ctx, configID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrEconomyConfigNotFound
	}

	historyEntry := &EconomyConfigHistory{
		ID:              uuid.Must(uuid.NewV7()),
		EconomyConfigID: configID,
		OldValue:        existing.ConfigValue,
		NewValue:        newValue,
		ChangedByGCID:   updatedByGCID,
		ChangeReason:    reason,
		CreatedAt:       time.Now().UTC(),
	}
	if err := s.repo.CreateHistoryEntry(ctx, historyEntry); err != nil {
		return nil, err
	}

	existing.ConfigValue = newValue
	existing.UpdatedByGCID = updatedByGCID
	existing.UpdatedAt = time.Now().UTC()

	if err := s.repo.Update(ctx, existing); err != nil {
		return nil, err
	}

	tenantID := uuid.Nil
	if existing.TenantID != nil {
		tenantID = *existing.TenantID
	}

	event := NewDomainEvent(
		EventEconomyConfigUpdated,
		tenantID,
		&updatedByGCID,
		configID,
		AggregateEconomyConfig,
		map[string]interface{}{
			"category": string(existing.ConfigCategory),
			"key":      existing.ConfigKey,
			"reason":   reason,
		},
	)
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, event); err != nil {
		return nil, err
	}

	return existing, nil
}

// RollbackConfig restores a config to its previous value from history.
func (s *EconomyConfigService) RollbackConfig(ctx context.Context, configID uuid.UUID, rolledBackByGCID uuid.UUID) (*EconomyConfig, error) {
	existing, err := s.repo.GetByID(ctx, configID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrEconomyConfigNotFound
	}

	history, err := s.repo.GetHistory(ctx, configID, 1)
	if err != nil {
		return nil, err
	}
	if len(history) == 0 {
		return nil, ErrEconomyConfigNoHistory
	}

	previousValue := history[0].OldValue

	historyEntry := &EconomyConfigHistory{
		ID:              uuid.Must(uuid.NewV7()),
		EconomyConfigID: configID,
		OldValue:        existing.ConfigValue,
		NewValue:        previousValue,
		ChangedByGCID:   rolledBackByGCID,
		ChangeReason:    "rollback",
		CreatedAt:       time.Now().UTC(),
	}
	if err := s.repo.CreateHistoryEntry(ctx, historyEntry); err != nil {
		return nil, err
	}

	existing.ConfigValue = previousValue
	existing.UpdatedByGCID = rolledBackByGCID
	existing.UpdatedAt = time.Now().UTC()

	if err := s.repo.Update(ctx, existing); err != nil {
		return nil, err
	}

	tenantID := uuid.Nil
	if existing.TenantID != nil {
		tenantID = *existing.TenantID
	}

	event := NewDomainEvent(
		EventEconomyConfigRolledBack,
		tenantID,
		&rolledBackByGCID,
		configID,
		AggregateEconomyConfig,
		map[string]interface{}{
			"category": string(existing.ConfigCategory),
			"key":      existing.ConfigKey,
		},
	)
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, event); err != nil {
		return nil, err
	}

	return existing, nil
}

// GetConfigHistory returns audit history for a config, capped at maxLimit.
func (s *EconomyConfigService) GetConfigHistory(ctx context.Context, configID uuid.UUID, limit int) ([]*EconomyConfigHistory, error) {
	const maxLimit = 50
	if limit <= 0 || limit > maxLimit {
		limit = maxLimit
	}
	return s.repo.GetHistory(ctx, configID, limit)
}

// DeleteConfig soft-deletes an economy config entry.
func (s *EconomyConfigService) DeleteConfig(ctx context.Context, configID uuid.UUID) error {
	existing, err := s.repo.GetByID(ctx, configID)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrEconomyConfigNotFound
	}
	return s.repo.Delete(ctx, configID)
}
