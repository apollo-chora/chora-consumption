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

type testHealthDeps struct {
	svc      *EconomyHealthService
	snapRepo *mockEconomySnapshotRepo
	coinRepo *mockCoinRepo
}

func newTestHealthService() testHealthDeps {
	sr := &mockEconomySnapshotRepo{}
	cr := &mockCoinRepo{}
	return testHealthDeps{
		svc:      NewEconomyHealthService(sr, cr),
		snapRepo: sr,
		coinRepo: cr,
	}
}

// ---------------------------------------------------------------------------
// Tests — CaptureSnapshot
// ---------------------------------------------------------------------------

func TestCaptureSnapshot_Success(t *testing.T) {
	t.Parallel()
	d := newTestHealthService()
	ctx := context.Background()
	tenantID := uuid.New()
	today := time.Now().UTC().Truncate(24 * time.Hour)

	d.coinRepo.On("SumBalances", mock.Anything, tenantID).Return(int64(50000), nil)
	d.coinRepo.On("CountActiveAccounts", mock.Anything, tenantID).Return(120, nil)
	d.coinRepo.On("SumDailyMinted", mock.Anything, tenantID, today).Return(int64(500), nil)
	d.coinRepo.On("SumDailyDrained", mock.Anything, tenantID, today).Return(int64(200), nil)
	d.snapRepo.On("Create", mock.Anything, mock.AnythingOfType("*gamification.EconomySnapshot")).Return(nil)

	snap, err := d.svc.CaptureSnapshot(ctx, tenantID, today)
	assert.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, snap.ID)
	assert.Equal(t, tenantID, snap.TenantID)
	assert.Equal(t, today, snap.SnapshotDate)
	assert.Equal(t, int64(50000), snap.TotalCoins)
	assert.Equal(t, int64(500), snap.DailyMinted)
	assert.Equal(t, int64(200), snap.DailyDrained)
	assert.Equal(t, 120, snap.ActiveAccounts)
	// inflation_rate_pct = (500 - 200) / 50000 * 100 = 0.6%
	assert.InDelta(t, 0.6, snap.InflationRatePct, 0.01)
	d.snapRepo.AssertExpectations(t)
	d.coinRepo.AssertExpectations(t)
}

func TestCaptureSnapshot_ZeroTotalCoins(t *testing.T) {
	t.Parallel()
	d := newTestHealthService()
	ctx := context.Background()
	tenantID := uuid.New()
	today := time.Now().UTC().Truncate(24 * time.Hour)

	d.coinRepo.On("SumBalances", mock.Anything, tenantID).Return(int64(0), nil)
	d.coinRepo.On("CountActiveAccounts", mock.Anything, tenantID).Return(0, nil)
	d.coinRepo.On("SumDailyMinted", mock.Anything, tenantID, today).Return(int64(0), nil)
	d.coinRepo.On("SumDailyDrained", mock.Anything, tenantID, today).Return(int64(0), nil)
	d.snapRepo.On("Create", mock.Anything, mock.AnythingOfType("*gamification.EconomySnapshot")).Return(nil)

	snap, err := d.svc.CaptureSnapshot(ctx, tenantID, today)
	assert.NoError(t, err)
	assert.Equal(t, float64(0), snap.InflationRatePct)
}

func TestCaptureSnapshot_AlreadyExists(t *testing.T) {
	t.Parallel()
	d := newTestHealthService()
	ctx := context.Background()
	tenantID := uuid.New()
	today := time.Now().UTC().Truncate(24 * time.Hour)

	d.coinRepo.On("SumBalances", mock.Anything, tenantID).Return(int64(1000), nil)
	d.coinRepo.On("CountActiveAccounts", mock.Anything, tenantID).Return(10, nil)
	d.coinRepo.On("SumDailyMinted", mock.Anything, tenantID, today).Return(int64(100), nil)
	d.coinRepo.On("SumDailyDrained", mock.Anything, tenantID, today).Return(int64(50), nil)
	d.snapRepo.On("Create", mock.Anything, mock.AnythingOfType("*gamification.EconomySnapshot")).
		Return(ErrSnapshotAlreadyExists)

	_, err := d.svc.CaptureSnapshot(ctx, tenantID, today)
	assert.ErrorIs(t, err, ErrSnapshotAlreadyExists)
}

// ---------------------------------------------------------------------------
// Tests — GetLatestSnapshot
// ---------------------------------------------------------------------------

func TestGetLatestSnapshot_Success(t *testing.T) {
	t.Parallel()
	d := newTestHealthService()
	ctx := context.Background()
	tenantID := uuid.New()
	today := time.Now().UTC().Truncate(24 * time.Hour)

	expected := &EconomySnapshot{
		ID:               uuid.New(),
		TenantID:         tenantID,
		SnapshotDate:     today,
		TotalCoins:       50000,
		DailyMinted:      500,
		DailyDrained:     200,
		ActiveAccounts:   120,
		InflationRatePct: 0.6,
		CreatedAt:        time.Now().UTC(),
	}

	d.snapRepo.On("GetLatestByTenant", mock.Anything, tenantID).Return(expected, nil)

	got, err := d.svc.GetLatestSnapshot(ctx, tenantID)
	assert.NoError(t, err)
	assert.Equal(t, expected.ID, got.ID)
	assert.Equal(t, expected.TotalCoins, got.TotalCoins)
	d.snapRepo.AssertExpectations(t)
}

func TestGetLatestSnapshot_NotFound(t *testing.T) {
	t.Parallel()
	d := newTestHealthService()
	ctx := context.Background()
	tenantID := uuid.New()

	d.snapRepo.On("GetLatestByTenant", mock.Anything, tenantID).Return(nil, ErrSnapshotNotFound)

	_, err := d.svc.GetLatestSnapshot(ctx, tenantID)
	assert.ErrorIs(t, err, ErrSnapshotNotFound)
}

// ---------------------------------------------------------------------------
// Tests — CalculateInflationRate
// ---------------------------------------------------------------------------

func TestCalculateInflationRate_Normal(t *testing.T) {
	t.Parallel()
	d := newTestHealthService()

	// (500 - 200) / 50000 * 100 = 0.6
	rate := d.svc.CalculateInflationRate(500, 200, 50000)
	assert.InDelta(t, 0.6, rate, 0.01)
}

func TestCalculateInflationRate_ZeroTotal(t *testing.T) {
	t.Parallel()
	d := newTestHealthService()

	rate := d.svc.CalculateInflationRate(100, 50, 0)
	assert.Equal(t, float64(0), rate)
}

func TestCalculateInflationRate_Deflation(t *testing.T) {
	t.Parallel()
	d := newTestHealthService()

	// More drained than minted = negative rate
	rate := d.svc.CalculateInflationRate(100, 300, 10000)
	assert.InDelta(t, -2.0, rate, 0.01)
}

func TestCalculateInflationRate_ZeroActivity(t *testing.T) {
	t.Parallel()
	d := newTestHealthService()

	rate := d.svc.CalculateInflationRate(0, 0, 50000)
	assert.Equal(t, float64(0), rate)
}
