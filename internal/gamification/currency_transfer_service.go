package gamification

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// DefaultDailyTransferLimit is the default max coins that can be transferred per day.
const DefaultDailyTransferLimit = 1000

// NewCurrencyTransferService creates a new CurrencyTransferService.
func NewCurrencyTransferService(
	transferRepo CurrencyTransferRepository,
	econRepo EconomyConfigRepository,
	publisher EventPublisher,
) *CurrencyTransferService {
	return &CurrencyTransferService{
		transferRepo: transferRepo,
		econRepo:     econRepo,
		publisher:    publisher,
	}
}

// CurrencyTransferService handles P2P currency gifting with social gate validation.
type CurrencyTransferService struct {
	transferRepo CurrencyTransferRepository
	econRepo     EconomyConfigRepository
	publisher    EventPublisher
}

// Transfer sends currency from one GCID to another with daily limit enforcement.
func (s *CurrencyTransferService) Transfer(
	ctx context.Context,
	tenantID, senderGCID, receiverGCID uuid.UUID,
	currency CurrencyType,
	amount int,
	message *string,
) (*CurrencyTransfer, error) {
	// Self-transfer blocked
	if senderGCID == receiverGCID {
		return nil, ErrSelfTransferBlocked
	}

	if amount <= 0 {
		return nil, ErrValidationFailed
	}

	// Fetch daily transfer limit from EconomyConfig
	dailyLimit := DefaultDailyTransferLimit
	limitConfig, err := s.econRepo.GetByTenantCategoryKey(ctx, nil, ConfigCategoryStoreLimits, "daily_transfer_limit")
	if err == nil && limitConfig != nil {
		if v, ok := limitConfig.ConfigValue["max_daily_coins"].(float64); ok {
			dailyLimit = int(v)
		}
	}

	// Check daily transferred amount
	today := time.Now().UTC().Truncate(24 * time.Hour)
	alreadyTransferred, err := s.transferRepo.SumDailyTransferred(ctx, tenantID, senderGCID, currency, today)
	if err != nil {
		return nil, err
	}

	if alreadyTransferred+amount > dailyLimit {
		return nil, ErrDailyTransferLimitExceeded
	}

	now := time.Now().UTC()
	transfer := &CurrencyTransfer{
		ID:           uuid.Must(uuid.NewV7()),
		TenantID:     tenantID,
		SenderGCID:   senderGCID,
		ReceiverGCID: receiverGCID,
		Currency:     currency,
		Amount:       amount,
		Message:      message,
		Status:       TransferStatusCompleted,
		CreatedAt:    now,
		CompletedAt:  &now,
	}

	if err := s.transferRepo.Create(ctx, transfer); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventCurrencyTransferred,
		tenantID,
		&senderGCID,
		transfer.ID,
		AggregateCurrencyTransfer,
		map[string]interface{}{
			"sender_gcid":   senderGCID.String(),
			"receiver_gcid": receiverGCID.String(),
			"currency":      string(currency),
			"amount":        amount,
		},
	)
	_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)

	return transfer, nil
}
