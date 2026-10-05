package gamification

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// NewCurrencyConverterService creates a new CurrencyConverterService.
func NewCurrencyConverterService(
	conversionRepo CurrencyConversionRepository,
	econRepo EconomyConfigRepository,
	publisher EventPublisher,
) *CurrencyConverterService {
	return &CurrencyConverterService{
		conversionRepo: conversionRepo,
		econRepo:       econRepo,
		publisher:      publisher,
	}
}

// CurrencyConverterService handles stars-to-coins and coins-to-stars conversion.
type CurrencyConverterService struct {
	conversionRepo CurrencyConversionRepository
	econRepo       EconomyConfigRepository
	publisher      EventPublisher
}

// Convert performs a currency conversion between coins and stars.
// Exchange rate is fetched from EconomyConfig (category: store_limits, key: currency_exchange_rate).
func (s *CurrencyConverterService) Convert(
	ctx context.Context,
	tenantID, gcid uuid.UUID,
	fromCurrency, toCurrency CurrencyType,
	fromAmount int,
) (*CurrencyConversion, error) {
	if fromCurrency == toCurrency {
		return nil, ErrSameCurrencyConversion
	}
	if fromAmount <= 0 {
		return nil, ErrValidationFailed
	}

	rateConfig, err := s.econRepo.GetByTenantCategoryKey(ctx, nil, ConfigCategoryStoreLimits, "currency_exchange_rate")
	if err != nil {
		return nil, err
	}

	var rate float64
	switch {
	case fromCurrency == CurrencyTypeStars && toCurrency == CurrencyTypeCoins:
		if v, ok := rateConfig.ConfigValue["stars_to_coins"].(float64); ok {
			rate = v
		} else {
			rate = 10 // default: 1 star = 10 coins
		}
	case fromCurrency == CurrencyTypeCoins && toCurrency == CurrencyTypeStars:
		if v, ok := rateConfig.ConfigValue["coins_to_stars"].(float64); ok {
			rate = v
		} else {
			rate = 0.1 // default: 10 coins = 1 star
		}
	default:
		return nil, ErrValidationFailed
	}

	toAmount := int(float64(fromAmount) * rate)

	now := time.Now().UTC()
	conversion := &CurrencyConversion{
		ID:           uuid.Must(uuid.NewV7()),
		TenantID:     tenantID,
		GCID:         gcid,
		FromCurrency: fromCurrency,
		ToCurrency:   toCurrency,
		FromAmount:   fromAmount,
		ToAmount:     toAmount,
		ExchangeRate: rate,
		CreatedAt:    now,
	}

	if err := s.conversionRepo.Create(ctx, conversion); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventCurrencyConverted,
		tenantID,
		&gcid,
		conversion.ID,
		AggregateCurrencyConversion,
		map[string]interface{}{
			"from_currency": string(fromCurrency),
			"to_currency":   string(toCurrency),
			"from_amount":   fromAmount,
			"to_amount":     toAmount,
			"exchange_rate": rate,
		},
	)
	_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)

	return conversion, nil
}
