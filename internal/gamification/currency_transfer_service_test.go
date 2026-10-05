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

type testCurrencyTransferDeps struct {
	svc       *CurrencyTransferService
	transferR *mockCurrencyTransferRepo
	econR     *mockEconomyConfigRepo
	publisher *mockEventPublisher
}

func newTestCurrencyTransferService() testCurrencyTransferDeps {
	tr := &mockCurrencyTransferRepo{}
	er := &mockEconomyConfigRepo{}
	ep := &mockEventPublisher{}
	return testCurrencyTransferDeps{
		svc:       NewCurrencyTransferService(tr, er, ep),
		transferR: tr,
		econR:     er,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// Tests — Transfer
// ---------------------------------------------------------------------------

func TestCurrencyTransfer_Success(t *testing.T) {
	t.Parallel()
	d := newTestCurrencyTransferService()
	ctx := context.Background()
	tenantID := uuid.New()
	senderGCID := uuid.New()
	receiverGCID := uuid.New()

	limitConfig := &EconomyConfig{
		ID:             uuid.New(),
		ConfigCategory: ConfigCategoryStoreLimits,
		ConfigKey:      "daily_transfer_limit",
		ConfigValue:    map[string]interface{}{"max_daily_coins": float64(1000)},
	}

	d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "daily_transfer_limit").
		Return(limitConfig, nil)
	d.transferR.On("SumDailyTransferred", mock.Anything, tenantID, senderGCID, CurrencyTypeCoins, mock.AnythingOfType("time.Time")).
		Return(0, nil)
	d.transferR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.CurrencyTransfer")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	transfer, err := d.svc.Transfer(ctx, tenantID, senderGCID, receiverGCID, CurrencyTypeCoins, 100, nil)
	assert.NoError(t, err)
	assert.NotNil(t, transfer)
	assert.Equal(t, senderGCID, transfer.SenderGCID)
	assert.Equal(t, receiverGCID, transfer.ReceiverGCID)
	assert.Equal(t, CurrencyTypeCoins, transfer.Currency)
	assert.Equal(t, 100, transfer.Amount)
	assert.Equal(t, TransferStatusCompleted, transfer.Status)
	d.transferR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestCurrencyTransfer_SelfBlocked(t *testing.T) {
	t.Parallel()
	d := newTestCurrencyTransferService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	_, err := d.svc.Transfer(ctx, tenantID, gcid, gcid, CurrencyTypeCoins, 100, nil)
	assert.ErrorIs(t, err, ErrSelfTransferBlocked)
}

func TestCurrencyTransfer_DailyLimitExceeded(t *testing.T) {
	t.Parallel()
	d := newTestCurrencyTransferService()
	ctx := context.Background()
	tenantID := uuid.New()
	senderGCID := uuid.New()
	receiverGCID := uuid.New()

	limitConfig := &EconomyConfig{
		ID:             uuid.New(),
		ConfigCategory: ConfigCategoryStoreLimits,
		ConfigKey:      "daily_transfer_limit",
		ConfigValue:    map[string]interface{}{"max_daily_coins": float64(1000)},
	}

	d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "daily_transfer_limit").
		Return(limitConfig, nil)
	d.transferR.On("SumDailyTransferred", mock.Anything, tenantID, senderGCID, CurrencyTypeCoins, mock.AnythingOfType("time.Time")).
		Return(950, nil)

	_, err := d.svc.Transfer(ctx, tenantID, senderGCID, receiverGCID, CurrencyTypeCoins, 100, nil)
	assert.ErrorIs(t, err, ErrDailyTransferLimitExceeded)
}

func TestCurrencyTransfer_ZeroAmount_Error(t *testing.T) {
	t.Parallel()
	d := newTestCurrencyTransferService()
	ctx := context.Background()
	tenantID := uuid.New()
	senderGCID := uuid.New()
	receiverGCID := uuid.New()

	_, err := d.svc.Transfer(ctx, tenantID, senderGCID, receiverGCID, CurrencyTypeCoins, 0, nil)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

func TestCurrencyTransfer_WithMessage(t *testing.T) {
	t.Parallel()
	d := newTestCurrencyTransferService()
	ctx := context.Background()
	tenantID := uuid.New()
	senderGCID := uuid.New()
	receiverGCID := uuid.New()

	limitConfig := &EconomyConfig{
		ID:             uuid.New(),
		ConfigCategory: ConfigCategoryStoreLimits,
		ConfigKey:      "daily_transfer_limit",
		ConfigValue:    map[string]interface{}{"max_daily_coins": float64(1000)},
	}

	msg := "Happy birthday!"
	d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "daily_transfer_limit").
		Return(limitConfig, nil)
	d.transferR.On("SumDailyTransferred", mock.Anything, tenantID, senderGCID, CurrencyTypeCoins, mock.AnythingOfType("time.Time")).
		Return(0, nil)
	d.transferR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.CurrencyTransfer")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	transfer, err := d.svc.Transfer(ctx, tenantID, senderGCID, receiverGCID, CurrencyTypeCoins, 50, &msg)
	assert.NoError(t, err)
	assert.NotNil(t, transfer.Message)
	assert.Equal(t, "Happy birthday!", *transfer.Message)
}

// ---------------------------------------------------------------------------
// Tests — TransferStatus Enum
// ---------------------------------------------------------------------------

func TestTransferStatus_IsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, TransferStatusPending.IsValid())
	assert.True(t, TransferStatusCompleted.IsValid())
	assert.True(t, TransferStatusRejected.IsValid())
	assert.False(t, TransferStatus("invalid").IsValid())
}
