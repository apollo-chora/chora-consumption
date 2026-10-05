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

type testDrainDeps struct {
	svc       *EconomyDrainService
	coinRepo  *mockCoinRepo
	econRepo  *mockEconomyConfigRepo
	snapRepo  *mockEconomySnapshotRepo
	publisher *mockEventPublisher
}

func newTestDrainService() testDrainDeps {
	cr := &mockCoinRepo{}
	er := &mockEconomyConfigRepo{}
	sr := &mockEconomySnapshotRepo{}
	ep := &mockEventPublisher{}
	return testDrainDeps{
		svc:       NewEconomyDrainService(cr, er, sr, ep),
		coinRepo:  cr,
		econRepo:  er,
		snapRepo:  sr,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// Tests — ExecuteTerritoryMaintenanceDrain
// ---------------------------------------------------------------------------

func TestExecuteTerritoryMaintenanceDrain_DeductsCoinsFromHolders(t *testing.T) {
	t.Parallel()
	d := newTestDrainService()
	ctx := context.Background()
	tenantID := uuid.New()

	config := &EconomyConfig{
		ID:             uuid.New(),
		TenantID:       nil,
		ConfigCategory: ConfigCategoryStoreLimits,
		ConfigKey:      "territory_maintenance_cost",
		ConfigValue:    map[string]interface{}{"daily_cost_per_territory": float64(5)},
	}

	gcid1 := uuid.New()
	gcid2 := uuid.New()

	holders := []TerritoryHolder{
		{TenantID: tenantID, GCID: gcid1, TerritoryCount: 3},
		{TenantID: tenantID, GCID: gcid2, TerritoryCount: 1},
	}

	account1 := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid1,
		Balance: 100, LifetimeEarned: 200, LifetimeSpent: 100,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	account2 := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid2,
		Balance: 50, LifetimeEarned: 100, LifetimeSpent: 50,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	d.econRepo.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "territory_maintenance_cost").
		Return(config, nil)
	d.coinRepo.On("GetByGCID", mock.Anything, tenantID, gcid1).Return(account1, nil)
	d.coinRepo.On("GetByGCID", mock.Anything, tenantID, gcid2).Return(account2, nil)
	d.coinRepo.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.coinRepo.On("CreateTransaction", mock.Anything, mock.AnythingOfType("*gamification.CoinTransaction")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	result, err := d.svc.ExecuteTerritoryMaintenanceDrain(ctx, tenantID, holders)
	assert.NoError(t, err)
	// gcid1: 3 territories * 5 = 15 deducted; gcid2: 1 territory * 5 = 5 deducted
	assert.Equal(t, 2, result.AccountsProcessed)
	assert.Equal(t, 0, result.AccountsSkipped)
	assert.Equal(t, int64(20), result.TotalDrained)
	d.coinRepo.AssertExpectations(t)
}

func TestExecuteTerritoryMaintenanceDrain_SkipsInsufficientBalance(t *testing.T) {
	t.Parallel()
	d := newTestDrainService()
	ctx := context.Background()
	tenantID := uuid.New()

	config := &EconomyConfig{
		ID:             uuid.New(),
		ConfigCategory: ConfigCategoryStoreLimits,
		ConfigKey:      "territory_maintenance_cost",
		ConfigValue:    map[string]interface{}{"daily_cost_per_territory": float64(10)},
	}

	gcid1 := uuid.New()
	holders := []TerritoryHolder{
		{TenantID: tenantID, GCID: gcid1, TerritoryCount: 5},
	}

	account1 := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid1,
		Balance: 3, LifetimeEarned: 10, LifetimeSpent: 7,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	d.econRepo.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "territory_maintenance_cost").
		Return(config, nil)
	d.coinRepo.On("GetByGCID", mock.Anything, tenantID, gcid1).Return(account1, nil)

	result, err := d.svc.ExecuteTerritoryMaintenanceDrain(ctx, tenantID, holders)
	assert.NoError(t, err)
	assert.Equal(t, 0, result.AccountsProcessed)
	assert.Equal(t, 1, result.AccountsSkipped)
	assert.Equal(t, int64(0), result.TotalDrained)
}

func TestExecuteTerritoryMaintenanceDrain_NoHolders(t *testing.T) {
	t.Parallel()
	d := newTestDrainService()
	ctx := context.Background()
	tenantID := uuid.New()

	config := &EconomyConfig{
		ID:             uuid.New(),
		ConfigCategory: ConfigCategoryStoreLimits,
		ConfigKey:      "territory_maintenance_cost",
		ConfigValue:    map[string]interface{}{"daily_cost_per_territory": float64(5)},
	}

	d.econRepo.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "territory_maintenance_cost").
		Return(config, nil)

	result, err := d.svc.ExecuteTerritoryMaintenanceDrain(ctx, tenantID, []TerritoryHolder{})
	assert.NoError(t, err)
	assert.Equal(t, 0, result.AccountsProcessed)
	assert.Equal(t, 0, result.AccountsSkipped)
	assert.Equal(t, int64(0), result.TotalDrained)
}

func TestExecuteTerritoryMaintenanceDrain_ZeroBalanceSkipped(t *testing.T) {
	t.Parallel()
	d := newTestDrainService()
	ctx := context.Background()
	tenantID := uuid.New()

	config := &EconomyConfig{
		ID:             uuid.New(),
		ConfigCategory: ConfigCategoryStoreLimits,
		ConfigKey:      "territory_maintenance_cost",
		ConfigValue:    map[string]interface{}{"daily_cost_per_territory": float64(5)},
	}

	gcid1 := uuid.New()
	holders := []TerritoryHolder{
		{TenantID: tenantID, GCID: gcid1, TerritoryCount: 2},
	}

	account := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid1,
		Balance: 0, LifetimeEarned: 0, LifetimeSpent: 0,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	d.econRepo.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "territory_maintenance_cost").
		Return(config, nil)
	d.coinRepo.On("GetByGCID", mock.Anything, tenantID, gcid1).Return(account, nil)

	result, err := d.svc.ExecuteTerritoryMaintenanceDrain(ctx, tenantID, holders)
	assert.NoError(t, err)
	assert.Equal(t, 0, result.AccountsProcessed)
	assert.Equal(t, 1, result.AccountsSkipped)
}

// ---------------------------------------------------------------------------
// Tests — ExecuteMaterialExpiry
// ---------------------------------------------------------------------------

func TestExecuteMaterialExpiry_MarksExpiredMaterials(t *testing.T) {
	t.Parallel()
	d := newTestDrainService()
	ctx := context.Background()
	tenantID := uuid.New()

	config := &EconomyConfig{
		ID:             uuid.New(),
		ConfigCategory: ConfigCategoryMaterialDropRates,
		ConfigKey:      "material_shelf_life",
		ConfigValue:    map[string]interface{}{"shelf_life_days": float64(30)},
	}

	now := time.Now().UTC()
	expiredDate := now.AddDate(0, 0, -31)

	materials := []MaterialInstance{
		{ID: uuid.New(), TenantID: tenantID, AcquiredAt: expiredDate},
		{ID: uuid.New(), TenantID: tenantID, AcquiredAt: now}, // not expired
	}

	d.econRepo.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryMaterialDropRates, "material_shelf_life").
		Return(config, nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	result, err := d.svc.ExecuteMaterialExpiry(ctx, tenantID, materials)
	assert.NoError(t, err)
	assert.Equal(t, 1, result.MaterialsExpired)
	assert.Equal(t, 2, result.MaterialsChecked)
}

func TestExecuteMaterialExpiry_NoExpiredMaterials(t *testing.T) {
	t.Parallel()
	d := newTestDrainService()
	ctx := context.Background()
	tenantID := uuid.New()

	config := &EconomyConfig{
		ID:             uuid.New(),
		ConfigCategory: ConfigCategoryMaterialDropRates,
		ConfigKey:      "material_shelf_life",
		ConfigValue:    map[string]interface{}{"shelf_life_days": float64(30)},
	}

	now := time.Now().UTC()
	materials := []MaterialInstance{
		{ID: uuid.New(), TenantID: tenantID, AcquiredAt: now},
		{ID: uuid.New(), TenantID: tenantID, AcquiredAt: now.AddDate(0, 0, -10)},
	}

	d.econRepo.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryMaterialDropRates, "material_shelf_life").
		Return(config, nil)

	result, err := d.svc.ExecuteMaterialExpiry(ctx, tenantID, materials)
	assert.NoError(t, err)
	assert.Equal(t, 0, result.MaterialsExpired)
	assert.Equal(t, 2, result.MaterialsChecked)
}

func TestExecuteMaterialExpiry_EmptyList(t *testing.T) {
	t.Parallel()
	d := newTestDrainService()
	ctx := context.Background()
	tenantID := uuid.New()

	config := &EconomyConfig{
		ID:             uuid.New(),
		ConfigCategory: ConfigCategoryMaterialDropRates,
		ConfigKey:      "material_shelf_life",
		ConfigValue:    map[string]interface{}{"shelf_life_days": float64(30)},
	}

	d.econRepo.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryMaterialDropRates, "material_shelf_life").
		Return(config, nil)

	result, err := d.svc.ExecuteMaterialExpiry(ctx, tenantID, []MaterialInstance{})
	assert.NoError(t, err)
	assert.Equal(t, 0, result.MaterialsExpired)
	assert.Equal(t, 0, result.MaterialsChecked)
}

// ---------------------------------------------------------------------------
// Tests — ExecuteELODecay
// ---------------------------------------------------------------------------

func TestExecuteELODecay_ReducesInactivePlayerRatings(t *testing.T) {
	t.Parallel()
	d := newTestDrainService()
	ctx := context.Background()
	tenantID := uuid.New()

	config := &EconomyConfig{
		ID:             uuid.New(),
		ConfigCategory: ConfigCategoryEloSettings,
		ConfigKey:      "elo_inactivity_decay",
		ConfigValue:    map[string]interface{}{"inactive_days_threshold": float64(14), "decay_amount": float64(25)},
	}

	now := time.Now().UTC()
	inactiveDate := now.AddDate(0, 0, -20)

	players := []ELOPlayer{
		{GCID: uuid.New(), TenantID: tenantID, Rating: 1200, LastDuelAt: inactiveDate},
		{GCID: uuid.New(), TenantID: tenantID, Rating: 900, LastDuelAt: now}, // active
	}

	d.econRepo.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryEloSettings, "elo_inactivity_decay").
		Return(config, nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	result, err := d.svc.ExecuteELODecay(ctx, tenantID, players)
	assert.NoError(t, err)
	assert.Equal(t, 1, result.PlayersDecayed)
	assert.Equal(t, 2, result.PlayersChecked)
	assert.Equal(t, 25, result.TotalRatingLost)
	// Verify the decayed player's rating was reduced
	assert.Equal(t, 1175, players[0].Rating)
}

func TestExecuteELODecay_FloorAt800(t *testing.T) {
	t.Parallel()
	d := newTestDrainService()
	ctx := context.Background()
	tenantID := uuid.New()

	config := &EconomyConfig{
		ID:             uuid.New(),
		ConfigCategory: ConfigCategoryEloSettings,
		ConfigKey:      "elo_inactivity_decay",
		ConfigValue:    map[string]interface{}{"inactive_days_threshold": float64(14), "decay_amount": float64(25)},
	}

	inactiveDate := time.Now().UTC().AddDate(0, 0, -30)

	players := []ELOPlayer{
		{GCID: uuid.New(), TenantID: tenantID, Rating: 810, LastDuelAt: inactiveDate},
	}

	d.econRepo.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryEloSettings, "elo_inactivity_decay").
		Return(config, nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	result, err := d.svc.ExecuteELODecay(ctx, tenantID, players)
	assert.NoError(t, err)
	assert.Equal(t, 1, result.PlayersDecayed)
	assert.Equal(t, 10, result.TotalRatingLost)
	// Should floor at 800, not go to 785
	assert.Equal(t, 800, players[0].Rating)
}

func TestExecuteELODecay_AlreadyAt800(t *testing.T) {
	t.Parallel()
	d := newTestDrainService()
	ctx := context.Background()
	tenantID := uuid.New()

	config := &EconomyConfig{
		ID:             uuid.New(),
		ConfigCategory: ConfigCategoryEloSettings,
		ConfigKey:      "elo_inactivity_decay",
		ConfigValue:    map[string]interface{}{"inactive_days_threshold": float64(14), "decay_amount": float64(25)},
	}

	inactiveDate := time.Now().UTC().AddDate(0, 0, -30)

	players := []ELOPlayer{
		{GCID: uuid.New(), TenantID: tenantID, Rating: 800, LastDuelAt: inactiveDate},
	}

	d.econRepo.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryEloSettings, "elo_inactivity_decay").
		Return(config, nil)

	result, err := d.svc.ExecuteELODecay(ctx, tenantID, players)
	assert.NoError(t, err)
	assert.Equal(t, 0, result.PlayersDecayed)
	assert.Equal(t, 1, result.PlayersChecked)
	assert.Equal(t, 0, result.TotalRatingLost)
	assert.Equal(t, 800, players[0].Rating)
}

func TestExecuteELODecay_NoInactivePlayers(t *testing.T) {
	t.Parallel()
	d := newTestDrainService()
	ctx := context.Background()
	tenantID := uuid.New()

	config := &EconomyConfig{
		ID:             uuid.New(),
		ConfigCategory: ConfigCategoryEloSettings,
		ConfigKey:      "elo_inactivity_decay",
		ConfigValue:    map[string]interface{}{"inactive_days_threshold": float64(14), "decay_amount": float64(25)},
	}

	now := time.Now().UTC()
	players := []ELOPlayer{
		{GCID: uuid.New(), TenantID: tenantID, Rating: 1200, LastDuelAt: now},
	}

	d.econRepo.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryEloSettings, "elo_inactivity_decay").
		Return(config, nil)

	result, err := d.svc.ExecuteELODecay(ctx, tenantID, players)
	assert.NoError(t, err)
	assert.Equal(t, 0, result.PlayersDecayed)
	assert.Equal(t, 1, result.PlayersChecked)
}

func TestExecuteELODecay_EmptyList(t *testing.T) {
	t.Parallel()
	d := newTestDrainService()
	ctx := context.Background()
	tenantID := uuid.New()

	config := &EconomyConfig{
		ID:             uuid.New(),
		ConfigCategory: ConfigCategoryEloSettings,
		ConfigKey:      "elo_inactivity_decay",
		ConfigValue:    map[string]interface{}{"inactive_days_threshold": float64(14), "decay_amount": float64(25)},
	}

	d.econRepo.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryEloSettings, "elo_inactivity_decay").
		Return(config, nil)

	result, err := d.svc.ExecuteELODecay(ctx, tenantID, []ELOPlayer{})
	assert.NoError(t, err)
	assert.Equal(t, 0, result.PlayersDecayed)
	assert.Equal(t, 0, result.PlayersChecked)
}
