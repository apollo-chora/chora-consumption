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

type testGroupFundDeps struct {
	svc           *GroupFundService
	poolR         *mockGroupFundPoolRepo
	contributionR *mockGroupFundContributionRepo
	publisher     *mockEventPublisher
}

func newTestGroupFundService() testGroupFundDeps {
	pr := &mockGroupFundPoolRepo{}
	cr := &mockGroupFundContributionRepo{}
	ep := &mockEventPublisher{}
	return testGroupFundDeps{
		svc:           NewGroupFundService(pr, cr, ep),
		poolR:         pr,
		contributionR: cr,
		publisher:     ep,
	}
}

// ---------------------------------------------------------------------------
// Tests — CreatePool
// ---------------------------------------------------------------------------

func TestGroupFund_CreatePool_Success(t *testing.T) {
	t.Parallel()
	d := newTestGroupFundService()
	ctx := context.Background()
	tenantID := uuid.New()

	d.poolR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.GroupFundPool")).Return(nil)

	pool, err := d.svc.CreatePool(ctx, tenantID, "Dragon Slayer Fund", PoolTypeBossChallenge, 5000)
	assert.NoError(t, err)
	assert.NotNil(t, pool)
	assert.Equal(t, "Dragon Slayer Fund", pool.PoolName)
	assert.Equal(t, PoolTypeBossChallenge, pool.PoolType)
	assert.Equal(t, 5000, pool.TargetAmount)
	assert.Equal(t, 0, pool.CurrentAmount)
	assert.Equal(t, PoolStatusCollecting, pool.Status)
	d.poolR.AssertExpectations(t)
}

func TestGroupFund_CreatePool_InvalidPoolType(t *testing.T) {
	t.Parallel()
	d := newTestGroupFundService()
	ctx := context.Background()
	tenantID := uuid.New()

	_, err := d.svc.CreatePool(ctx, tenantID, "Bad Pool", PoolType("invalid"), 5000)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

// ---------------------------------------------------------------------------
// Tests — Contribute
// ---------------------------------------------------------------------------

func TestGroupFund_Contribute_Success(t *testing.T) {
	t.Parallel()
	d := newTestGroupFundService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	poolID := uuid.New()

	pool := &GroupFundPool{
		ID:            poolID,
		TenantID:      tenantID,
		PoolName:      "Boss Fund",
		PoolType:      PoolTypeBossChallenge,
		TargetAmount:  5000,
		CurrentAmount: 1000,
		Status:        PoolStatusCollecting,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}

	d.poolR.On("GetByID", mock.Anything, poolID).Return(pool, nil)
	d.poolR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.GroupFundPool")).Return(nil)
	d.contributionR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.GroupFundContribution")).Return(nil)

	contribution, err := d.svc.Contribute(ctx, tenantID, poolID, gcid, 500)
	assert.NoError(t, err)
	assert.NotNil(t, contribution)
	assert.Equal(t, poolID, contribution.PoolID)
	assert.Equal(t, gcid, contribution.GCID)
	assert.Equal(t, 500, contribution.Amount)
	d.poolR.AssertExpectations(t)
	d.contributionR.AssertExpectations(t)
}

func TestGroupFund_Contribute_PoolNotCollecting(t *testing.T) {
	t.Parallel()
	d := newTestGroupFundService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	poolID := uuid.New()

	pool := &GroupFundPool{
		ID:       poolID,
		TenantID: tenantID,
		Status:   PoolStatusDistributed,
	}

	d.poolR.On("GetByID", mock.Anything, poolID).Return(pool, nil)

	_, err := d.svc.Contribute(ctx, tenantID, poolID, gcid, 500)
	assert.ErrorIs(t, err, ErrPoolNotCollecting)
}

func TestGroupFund_Contribute_ZeroAmount(t *testing.T) {
	t.Parallel()
	d := newTestGroupFundService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	poolID := uuid.New()

	_, err := d.svc.Contribute(ctx, tenantID, poolID, gcid, 0)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

// ---------------------------------------------------------------------------
// Tests — DistributePool
// ---------------------------------------------------------------------------

func TestGroupFund_DistributePool_Success(t *testing.T) {
	t.Parallel()
	d := newTestGroupFundService()
	ctx := context.Background()
	poolID := uuid.New()
	tenantID := uuid.New()

	pool := &GroupFundPool{
		ID:            poolID,
		TenantID:      tenantID,
		PoolName:      "Boss Fund",
		PoolType:      PoolTypeBossChallenge,
		TargetAmount:  5000,
		CurrentAmount: 5000,
		Status:        PoolStatusCollecting,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}

	d.poolR.On("GetByID", mock.Anything, poolID).Return(pool, nil)
	d.poolR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.GroupFundPool")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	err := d.svc.DistributePool(ctx, poolID)
	assert.NoError(t, err)
	d.poolR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestGroupFund_DistributePool_AlreadyDistributed(t *testing.T) {
	t.Parallel()
	d := newTestGroupFundService()
	ctx := context.Background()
	poolID := uuid.New()

	pool := &GroupFundPool{
		ID:     poolID,
		Status: PoolStatusDistributed,
	}

	d.poolR.On("GetByID", mock.Anything, poolID).Return(pool, nil)

	err := d.svc.DistributePool(ctx, poolID)
	assert.ErrorIs(t, err, ErrPoolNotCollecting)
}

// ---------------------------------------------------------------------------
// Tests — Pool Enums
// ---------------------------------------------------------------------------

func TestPoolType_IsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, PoolTypeBossChallenge.IsValid())
	assert.True(t, PoolTypeCoOpReward.IsValid())
	assert.True(t, PoolTypeSeasonal.IsValid())
	assert.False(t, PoolType("invalid").IsValid())
}

func TestPoolStatus_IsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, PoolStatusCollecting.IsValid())
	assert.True(t, PoolStatusDistributed.IsValid())
	assert.True(t, PoolStatusCancelled.IsValid())
	assert.False(t, PoolStatus("invalid").IsValid())
}
