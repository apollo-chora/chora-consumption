package gamification

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

type testCurrencyConverterDeps struct {
	svc         *CurrencyConverterService
	conversionR *mockCurrencyConversionRepo
	econR       *mockEconomyConfigRepo
	publisher   *mockEventPublisher
}

func newTestCurrencyConverterService() testCurrencyConverterDeps {
	cr := &mockCurrencyConversionRepo{}
	er := &mockEconomyConfigRepo{}
	ep := &mockEventPublisher{}
	return testCurrencyConverterDeps{
		svc:         NewCurrencyConverterService(cr, er, ep),
		conversionR: cr,
		econR:       er,
		publisher:   ep,
	}
}

// ---------------------------------------------------------------------------
// Tests — Convert Stars to Coins
// ---------------------------------------------------------------------------

func TestCurrencyConverter_StarsToCoins_Success(t *testing.T) {
	t.Parallel()
	d := newTestCurrencyConverterService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	rateConfig := &EconomyConfig{
		ID:             uuid.New(),
		ConfigCategory: ConfigCategoryStoreLimits,
		ConfigKey:      "currency_exchange_rate",
		ConfigValue:    map[string]interface{}{"stars_to_coins": float64(10)},
	}

	d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "currency_exchange_rate").
		Return(rateConfig, nil)
	d.conversionR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.CurrencyConversion")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	conversion, err := d.svc.Convert(ctx, tenantID, gcid, CurrencyTypeStars, CurrencyTypeCoins, 5)
	assert.NoError(t, err)
	assert.NotNil(t, conversion)
	assert.Equal(t, CurrencyTypeStars, conversion.FromCurrency)
	assert.Equal(t, CurrencyTypeCoins, conversion.ToCurrency)
	assert.Equal(t, 5, conversion.FromAmount)
	assert.Equal(t, 50, conversion.ToAmount)
	assert.Equal(t, float64(10), conversion.ExchangeRate)
	d.econR.AssertExpectations(t)
	d.conversionR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestCurrencyConverter_CoinsToStars_Success(t *testing.T) {
	t.Parallel()
	d := newTestCurrencyConverterService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	rateConfig := &EconomyConfig{
		ID:             uuid.New(),
		ConfigCategory: ConfigCategoryStoreLimits,
		ConfigKey:      "currency_exchange_rate",
		ConfigValue:    map[string]interface{}{"coins_to_stars": float64(0.1)},
	}

	d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "currency_exchange_rate").
		Return(rateConfig, nil)
	d.conversionR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.CurrencyConversion")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	conversion, err := d.svc.Convert(ctx, tenantID, gcid, CurrencyTypeCoins, CurrencyTypeStars, 100)
	assert.NoError(t, err)
	assert.NotNil(t, conversion)
	assert.Equal(t, CurrencyTypeCoins, conversion.FromCurrency)
	assert.Equal(t, CurrencyTypeStars, conversion.ToCurrency)
	assert.Equal(t, 100, conversion.FromAmount)
	assert.Equal(t, 10, conversion.ToAmount)
	assert.Equal(t, float64(0.1), conversion.ExchangeRate)
}

func TestCurrencyConverter_SameCurrency_Error(t *testing.T) {
	t.Parallel()
	d := newTestCurrencyConverterService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	_, err := d.svc.Convert(ctx, tenantID, gcid, CurrencyTypeCoins, CurrencyTypeCoins, 100)
	assert.ErrorIs(t, err, ErrSameCurrencyConversion)
}

func TestCurrencyConverter_ZeroAmount_Error(t *testing.T) {
	t.Parallel()
	d := newTestCurrencyConverterService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	_, err := d.svc.Convert(ctx, tenantID, gcid, CurrencyTypeStars, CurrencyTypeCoins, 0)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

// ---------------------------------------------------------------------------
// Tests — CurrencyType Enum
// ---------------------------------------------------------------------------

func TestCurrencyType_IsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, CurrencyTypeCoins.IsValid())
	assert.True(t, CurrencyTypeStars.IsValid())
	assert.False(t, CurrencyType("invalid").IsValid())
}
