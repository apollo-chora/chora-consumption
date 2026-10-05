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

type testCoinDeps struct {
	svc       *CoinService
	coinR     *mockCoinRepo
	publisher *mockEventPublisher
}

func newTestCoinService() testCoinDeps {
	cr := &mockCoinRepo{}
	ep := &mockEventPublisher{}
	return testCoinDeps{
		svc:       NewCoinService(cr, ep),
		coinR:     cr,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// Tests — EarnCoins
// ---------------------------------------------------------------------------

func TestEarnCoins_Success(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	refID := uuid.New()
	refType := "streak"

	existingAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		Balance: 200, LifetimeEarned: 500, LifetimeSpent: 300,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(existingAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.coinR.On("CreateTransaction", mock.Anything, mock.AnythingOfType("*gamification.CoinTransaction")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.EarnCoins(ctx, tenantID, gcid, 100, "streak_bonus", &refID, &refType)
	assert.NoError(t, err)
	assert.Equal(t, int64(300), got.Balance)
	assert.Equal(t, int64(600), got.LifetimeEarned)
	d.coinR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestEarnCoins_NewAccount(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	// No existing account — returns nil.
	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(nil, nil)
	d.coinR.On("CreateIfNotExists", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.coinR.On("CreateTransaction", mock.Anything, mock.AnythingOfType("*gamification.CoinTransaction")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.EarnCoins(ctx, tenantID, gcid, 50, "welcome_bonus", nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, int64(50), got.Balance)
	assert.Equal(t, int64(50), got.LifetimeEarned)
	d.coinR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestEarnCoins_AmountMustBePositive(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	_, err := d.svc.EarnCoins(ctx, tenantID, gcid, -10, "invalid", nil, nil)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

func TestEarnCoins_ZeroAmount(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	_, err := d.svc.EarnCoins(ctx, tenantID, gcid, 0, "zero", nil, nil)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

// ---------------------------------------------------------------------------
// Tests — SpendCoins
// ---------------------------------------------------------------------------

func TestSpendCoins_Success(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	refID := uuid.New()
	refType := "bounty"

	existingAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		Balance: 200, LifetimeEarned: 500, LifetimeSpent: 300,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(existingAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.coinR.On("CreateTransaction", mock.Anything, mock.AnythingOfType("*gamification.CoinTransaction")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.SpendCoins(ctx, tenantID, gcid, 50, "bounty_post", &refID, &refType)
	assert.NoError(t, err)
	assert.Equal(t, int64(150), got.Balance)
	assert.Equal(t, int64(350), got.LifetimeSpent)
	d.coinR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestSpendCoins_InsufficientBalance(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existingAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		Balance: 10,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(existingAccount, nil)

	_, err := d.svc.SpendCoins(ctx, tenantID, gcid, 9999, "too_much", nil, nil)
	assert.ErrorIs(t, err, ErrInsufficientBalance)
}

func TestSpendCoins_AmountMustBePositive(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	_, err := d.svc.SpendCoins(ctx, tenantID, gcid, 0, "zero", nil, nil)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

func TestSpendCoins_AccountNotFound(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(nil, nil)

	_, err := d.svc.SpendCoins(ctx, tenantID, gcid, 50, "reason", nil, nil)
	assert.ErrorIs(t, err, ErrNotFound)
}

// ---------------------------------------------------------------------------
// Tests — RefundCoins
// ---------------------------------------------------------------------------

func TestRefundCoins_Success(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	refID := uuid.New()
	refType := "bounty_cancel"

	existingAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		Balance: 100, LifetimeEarned: 300, LifetimeSpent: 200,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(existingAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.coinR.On("CreateTransaction", mock.Anything, mock.AnythingOfType("*gamification.CoinTransaction")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.RefundCoins(ctx, tenantID, gcid, 50, "bounty_cancelled", &refID, &refType)
	assert.NoError(t, err)
	assert.Equal(t, int64(150), got.Balance)
	d.coinR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestRefundCoins_AmountMustBePositive(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	_, err := d.svc.RefundCoins(ctx, tenantID, gcid, 0, "zero", nil, nil)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

func TestRefundCoins_AccountNotFound(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(nil, nil)

	_, err := d.svc.RefundCoins(ctx, tenantID, gcid, 50, "reason", nil, nil)
	assert.ErrorIs(t, err, ErrNotFound)
}

// ---------------------------------------------------------------------------
// Tests — GetBalance
// ---------------------------------------------------------------------------

func TestGetBalance_Success(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existingAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		Balance: 250,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(existingAccount, nil)

	got, err := d.svc.GetBalance(ctx, tenantID, gcid)
	assert.NoError(t, err)
	assert.Equal(t, int64(250), got.Balance)
	d.coinR.AssertExpectations(t)
}

func TestGetBalance_NotFound(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(nil, nil)

	_, err := d.svc.GetBalance(ctx, tenantID, gcid)
	assert.ErrorIs(t, err, ErrNotFound)
}

// ---------------------------------------------------------------------------
// Tests — ListTransactions
// ---------------------------------------------------------------------------

func TestListTransactions_Success(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	accountID := uuid.New()

	existingAccount := &CoinAccount{
		ID: accountID, TenantID: tenantID, GCID: gcid,
		Balance: 100,
	}

	txns := []*CoinTransaction{
		{ID: uuid.New(), CoinAccountID: accountID, Amount: 100, TransactionType: CoinTransactionTypeEarned},
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(existingAccount, nil)
	d.coinR.On("ListTransactions", mock.Anything, tenantID, accountID, 0, 20).Return(txns, nil)

	got, err := d.svc.ListTransactions(ctx, tenantID, gcid, 0, 20)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	d.coinR.AssertExpectations(t)
}

func TestListTransactions_AccountNotFound(t *testing.T) {
	t.Parallel()
	d := newTestCoinService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.coinR.On("GetByGCID", mock.Anything, tenantID, gcid).Return(nil, nil)

	_, err := d.svc.ListTransactions(ctx, tenantID, gcid, 0, 20)
	assert.ErrorIs(t, err, ErrNotFound)
}
