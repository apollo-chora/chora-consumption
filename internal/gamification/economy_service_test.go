package gamification

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

type testEconomyDeps struct {
	svc       *EconomyConfigService
	repo      *mockEconomyConfigRepo
	publisher *mockEventPublisher
}

func newTestEconomyService() testEconomyDeps {
	r := &mockEconomyConfigRepo{}
	ep := &mockEventPublisher{}
	return testEconomyDeps{
		svc:       NewEconomyConfigService(r, ep),
		repo:      r,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// Tests — GetEffectiveConfig
// ---------------------------------------------------------------------------

func TestGetEffectiveConfig_MergesDefaultsAndOverrides(t *testing.T) {
	t.Parallel()
	d := newTestEconomyService()
	ctx := context.Background()
	tenantID := uuid.New()

	platformDefault := &EconomyConfig{
		ID:               uuid.New(),
		TenantID:         nil,
		ConfigCategory:   ConfigCategoryFogThresholds,
		ConfigKey:        "mastery_threshold",
		ConfigValue:      map[string]interface{}{"value": 0.6},
		IsTenantOverride: false,
		CreatedByGCID:    uuid.New(),
		UpdatedByGCID:    uuid.New(),
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}
	tenantOverride := &EconomyConfig{
		ID:               uuid.New(),
		TenantID:         &tenantID,
		ConfigCategory:   ConfigCategoryFogThresholds,
		ConfigKey:        "reveal_speed",
		ConfigValue:      map[string]interface{}{"value": 1.5},
		IsTenantOverride: true,
		CreatedByGCID:    uuid.New(),
		UpdatedByGCID:    uuid.New(),
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}

	d.repo.On("GetEffective", mock.Anything, tenantID, ConfigCategoryFogThresholds).
		Return([]*EconomyConfig{platformDefault, tenantOverride}, nil)

	got, err := d.svc.GetEffectiveConfig(ctx, tenantID, ConfigCategoryFogThresholds)
	assert.NoError(t, err)
	assert.Len(t, got, 2)
	d.repo.AssertExpectations(t)
}

func TestGetEffectiveConfig_InvalidCategory(t *testing.T) {
	t.Parallel()
	d := newTestEconomyService()
	ctx := context.Background()
	tenantID := uuid.New()

	_, err := d.svc.GetEffectiveConfig(ctx, tenantID, ConfigCategory("invalid"))
	assert.ErrorIs(t, err, ErrEconomyConfigCategoryInvalid)
}

// ---------------------------------------------------------------------------
// Tests — CreateConfig
// ---------------------------------------------------------------------------

func TestCreateConfig_Success(t *testing.T) {
	t.Parallel()
	d := newTestEconomyService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.repo.On("Create", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfig")).Return(nil)

	config := &EconomyConfig{
		TenantID:         &tenantID,
		ConfigCategory:   ConfigCategoryEloSettings,
		ConfigKey:        "k_factor",
		ConfigValue:      map[string]interface{}{"value": 32},
		IsTenantOverride: true,
		CreatedByGCID:    gcid,
		UpdatedByGCID:    gcid,
	}

	err := d.svc.CreateConfig(ctx, config)
	assert.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, config.ID)
	assert.False(t, config.CreatedAt.IsZero())
	d.repo.AssertExpectations(t)
}

func TestCreateConfig_InvalidCategory(t *testing.T) {
	t.Parallel()
	d := newTestEconomyService()
	ctx := context.Background()

	config := &EconomyConfig{
		ConfigCategory: ConfigCategory("bad"),
		ConfigKey:      "test",
		ConfigValue:    map[string]interface{}{"value": 1},
		CreatedByGCID:  uuid.New(),
		UpdatedByGCID:  uuid.New(),
	}

	err := d.svc.CreateConfig(ctx, config)
	assert.ErrorIs(t, err, ErrEconomyConfigCategoryInvalid)
}

func TestCreateConfig_KeyTooLong(t *testing.T) {
	t.Parallel()
	d := newTestEconomyService()
	ctx := context.Background()

	longKey := ""
	for i := 0; i < 65; i++ {
		longKey += "x"
	}

	config := &EconomyConfig{
		ConfigCategory: ConfigCategoryDuelLimits,
		ConfigKey:      longKey,
		ConfigValue:    map[string]interface{}{"value": 1},
		CreatedByGCID:  uuid.New(),
		UpdatedByGCID:  uuid.New(),
	}

	err := d.svc.CreateConfig(ctx, config)
	assert.ErrorIs(t, err, ErrEconomyConfigKeyTooLong)
}

// ---------------------------------------------------------------------------
// Tests — UpdateConfig
// ---------------------------------------------------------------------------

func TestUpdateConfig_Success(t *testing.T) {
	t.Parallel()
	d := newTestEconomyService()
	ctx := context.Background()
	configID := uuid.New()
	gcid := uuid.New()
	tenantID := uuid.New()

	existing := &EconomyConfig{
		ID:               configID,
		TenantID:         &tenantID,
		ConfigCategory:   ConfigCategoryEloSettings,
		ConfigKey:        "k_factor",
		ConfigValue:      map[string]interface{}{"value": float64(32)},
		IsTenantOverride: true,
		CreatedByGCID:    uuid.New(),
		UpdatedByGCID:    uuid.New(),
		CreatedAt:        time.Now().UTC().Add(-time.Hour),
		UpdatedAt:        time.Now().UTC().Add(-time.Hour),
	}

	newValue := map[string]interface{}{"value": float64(24)}

	d.repo.On("GetByID", mock.Anything, configID).Return(existing, nil)
	d.repo.On("CreateHistoryEntry", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfigHistory")).Return(nil)
	d.repo.On("Update", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfig")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.UpdateConfig(ctx, configID, newValue, gcid, "tuning ELO")
	assert.NoError(t, err)
	assert.Equal(t, newValue, got.ConfigValue)
	assert.Equal(t, gcid, got.UpdatedByGCID)
	d.repo.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestUpdateConfig_NotFound(t *testing.T) {
	t.Parallel()
	d := newTestEconomyService()
	ctx := context.Background()
	configID := uuid.New()

	d.repo.On("GetByID", mock.Anything, configID).Return(nil, nil)

	_, err := d.svc.UpdateConfig(ctx, configID, map[string]interface{}{"value": 1}, uuid.New(), "test")
	assert.ErrorIs(t, err, ErrEconomyConfigNotFound)
}

func TestUpdateConfig_PublishesEvent(t *testing.T) {
	t.Parallel()
	d := newTestEconomyService()
	ctx := context.Background()
	configID := uuid.New()
	gcid := uuid.New()
	tenantID := uuid.New()

	existing := &EconomyConfig{
		ID:               configID,
		TenantID:         &tenantID,
		ConfigCategory:   ConfigCategoryMaterialDropRates,
		ConfigKey:        "common_rate",
		ConfigValue:      map[string]interface{}{"value": 0.40},
		IsTenantOverride: true,
		CreatedByGCID:    uuid.New(),
		UpdatedByGCID:    uuid.New(),
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}

	d.repo.On("GetByID", mock.Anything, configID).Return(existing, nil)
	d.repo.On("CreateHistoryEntry", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfigHistory")).Return(nil)
	d.repo.On("Update", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfig")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.MatchedBy(func(e DomainEvent) bool {
		return e.EventType == EventEconomyConfigUpdated &&
			e.AggregateType == AggregateEconomyConfig &&
			e.AggregateID == configID
	})).Return(nil)

	_, err := d.svc.UpdateConfig(ctx, configID, map[string]interface{}{"value": 0.35}, gcid, "balancing")
	assert.NoError(t, err)
	d.publisher.AssertExpectations(t)
}

func TestUpdateConfig_CreatesHistoryEntry(t *testing.T) {
	t.Parallel()
	d := newTestEconomyService()
	ctx := context.Background()
	configID := uuid.New()
	gcid := uuid.New()
	tenantID := uuid.New()
	oldValue := map[string]interface{}{"value": float64(10)}
	newValue := map[string]interface{}{"value": float64(5)}

	existing := &EconomyConfig{
		ID:               configID,
		TenantID:         &tenantID,
		ConfigCategory:   ConfigCategoryDuelLimits,
		ConfigKey:        "max_per_day",
		ConfigValue:      oldValue,
		IsTenantOverride: true,
		CreatedByGCID:    uuid.New(),
		UpdatedByGCID:    uuid.New(),
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}

	d.repo.On("GetByID", mock.Anything, configID).Return(existing, nil)
	d.repo.On("CreateHistoryEntry", mock.Anything, mock.MatchedBy(func(h *EconomyConfigHistory) bool {
		return h.EconomyConfigID == configID &&
			h.ChangedByGCID == gcid &&
			h.ChangeReason == "reducing duel limit"
	})).Return(nil)
	d.repo.On("Update", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfig")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	_, err := d.svc.UpdateConfig(ctx, configID, newValue, gcid, "reducing duel limit")
	assert.NoError(t, err)
	d.repo.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// Tests — RollbackConfig
// ---------------------------------------------------------------------------

func TestRollbackConfig_Success(t *testing.T) {
	t.Parallel()
	d := newTestEconomyService()
	ctx := context.Background()
	configID := uuid.New()
	gcid := uuid.New()
	tenantID := uuid.New()

	existing := &EconomyConfig{
		ID:               configID,
		TenantID:         &tenantID,
		ConfigCategory:   ConfigCategoryEloSettings,
		ConfigKey:        "k_factor",
		ConfigValue:      map[string]interface{}{"value": float64(24)},
		IsTenantOverride: true,
		CreatedByGCID:    uuid.New(),
		UpdatedByGCID:    uuid.New(),
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}

	previousHistory := &EconomyConfigHistory{
		ID:              uuid.New(),
		EconomyConfigID: configID,
		OldValue:        map[string]interface{}{"value": float64(32)},
		NewValue:        map[string]interface{}{"value": float64(24)},
		ChangedByGCID:   uuid.New(),
		ChangeReason:    "previous change",
		CreatedAt:       time.Now().UTC().Add(-time.Hour),
	}

	d.repo.On("GetByID", mock.Anything, configID).Return(existing, nil)
	d.repo.On("GetHistory", mock.Anything, configID, 1).Return([]*EconomyConfigHistory{previousHistory}, nil)
	d.repo.On("CreateHistoryEntry", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfigHistory")).Return(nil)
	d.repo.On("Update", mock.Anything, mock.AnythingOfType("*gamification.EconomyConfig")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.MatchedBy(func(e DomainEvent) bool {
		return e.EventType == EventEconomyConfigRolledBack
	})).Return(nil)

	got, err := d.svc.RollbackConfig(ctx, configID, gcid)
	assert.NoError(t, err)
	// Should have the OLD value from the history entry (rolling back to previous state)
	assert.Equal(t, previousHistory.OldValue, got.ConfigValue)
	d.repo.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestRollbackConfig_NotFound(t *testing.T) {
	t.Parallel()
	d := newTestEconomyService()
	ctx := context.Background()

	d.repo.On("GetByID", mock.Anything, mock.Anything).Return(nil, nil)

	_, err := d.svc.RollbackConfig(ctx, uuid.New(), uuid.New())
	assert.ErrorIs(t, err, ErrEconomyConfigNotFound)
}

func TestRollbackConfig_NoHistory(t *testing.T) {
	t.Parallel()
	d := newTestEconomyService()
	ctx := context.Background()
	configID := uuid.New()
	tenantID := uuid.New()

	existing := &EconomyConfig{
		ID:               configID,
		TenantID:         &tenantID,
		ConfigCategory:   ConfigCategoryEloSettings,
		ConfigKey:        "k_factor",
		ConfigValue:      map[string]interface{}{"value": float64(32)},
		IsTenantOverride: true,
		CreatedByGCID:    uuid.New(),
		UpdatedByGCID:    uuid.New(),
	}

	d.repo.On("GetByID", mock.Anything, configID).Return(existing, nil)
	d.repo.On("GetHistory", mock.Anything, configID, 1).Return([]*EconomyConfigHistory{}, nil)

	_, err := d.svc.RollbackConfig(ctx, configID, uuid.New())
	assert.ErrorIs(t, err, ErrEconomyConfigNoHistory)
}

// ---------------------------------------------------------------------------
// Tests — GetConfigHistory
// ---------------------------------------------------------------------------

func TestGetConfigHistory_Success(t *testing.T) {
	t.Parallel()
	d := newTestEconomyService()
	ctx := context.Background()
	configID := uuid.New()

	entries := []*EconomyConfigHistory{
		{ID: uuid.New(), EconomyConfigID: configID, CreatedAt: time.Now().UTC()},
		{ID: uuid.New(), EconomyConfigID: configID, CreatedAt: time.Now().UTC().Add(-time.Hour)},
	}

	d.repo.On("GetHistory", mock.Anything, configID, 50).Return(entries, nil)

	got, err := d.svc.GetConfigHistory(ctx, configID, 50)
	assert.NoError(t, err)
	assert.Len(t, got, 2)
	d.repo.AssertExpectations(t)
}

func TestGetConfigHistory_LimitCapped(t *testing.T) {
	t.Parallel()
	d := newTestEconomyService()
	ctx := context.Background()
	configID := uuid.New()

	d.repo.On("GetHistory", mock.Anything, configID, 50).Return([]*EconomyConfigHistory{}, nil)

	// Request more than 50 — should be capped to 50
	_, err := d.svc.GetConfigHistory(ctx, configID, 200)
	assert.NoError(t, err)
	d.repo.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// Tests — DeleteConfig
// ---------------------------------------------------------------------------

func TestDeleteConfig_Success(t *testing.T) {
	t.Parallel()
	d := newTestEconomyService()
	ctx := context.Background()
	configID := uuid.New()
	tenantID := uuid.New()

	existing := &EconomyConfig{
		ID:               configID,
		TenantID:         &tenantID,
		ConfigCategory:   ConfigCategoryStoreLimits,
		ConfigKey:        "max_purchase",
		IsTenantOverride: true,
	}

	d.repo.On("GetByID", mock.Anything, configID).Return(existing, nil)
	d.repo.On("Delete", mock.Anything, configID).Return(nil)

	err := d.svc.DeleteConfig(ctx, configID)
	assert.NoError(t, err)
	d.repo.AssertExpectations(t)
}

func TestDeleteConfig_NotFound(t *testing.T) {
	t.Parallel()
	d := newTestEconomyService()
	ctx := context.Background()

	d.repo.On("GetByID", mock.Anything, mock.Anything).Return(nil, nil)

	err := d.svc.DeleteConfig(ctx, uuid.New())
	assert.ErrorIs(t, err, ErrEconomyConfigNotFound)
}
