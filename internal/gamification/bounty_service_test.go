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

type testBountyDeps struct {
	svc       *BountyService
	bountyR   *mockBountyRepo
	coinR     *mockCoinRepo
	publisher *mockEventPublisher
}

func newTestBountyService() testBountyDeps {
	br := &mockBountyRepo{}
	cr := &mockCoinRepo{}
	ep := &mockEventPublisher{}
	return testBountyDeps{
		svc:       NewBountyService(br, cr, ep),
		bountyR:   br,
		coinR:     cr,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// Tests — CreateBounty
// ---------------------------------------------------------------------------

func TestCreateBounty_Success(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	tenantID := uuid.New()
	posterGCID := uuid.New()
	atomID := uuid.New()
	expiresAt := time.Now().Add(72 * time.Hour)

	posterAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: posterGCID,
		Balance: 500, LifetimeEarned: 1000, LifetimeSpent: 500,
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, posterGCID).Return(posterAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.bountyR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.KnowledgeBounty")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.CreateBounty(ctx, tenantID, posterGCID, atomID, "Help with recursion", "Explain tail recursion", 100, 50, expiresAt)
	assert.NoError(t, err)
	assert.Equal(t, "Help with recursion", got.Title)
	assert.Equal(t, BountyStatusOpen, got.Status)
	assert.Equal(t, 100, got.CoinReward)
	// Poster's balance should have been deducted.
	assert.Equal(t, int64(400), posterAccount.Balance)
	d.coinR.AssertExpectations(t)
	d.bountyR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestCreateBounty_RewardBelowMinimum(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	tenantID := uuid.New()
	posterGCID := uuid.New()
	atomID := uuid.New()
	expiresAt := time.Now().Add(72 * time.Hour)

	_, err := d.svc.CreateBounty(ctx, tenantID, posterGCID, atomID, "Too cheap", "desc", 10, 0, expiresAt)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

func TestCreateBounty_RewardAboveMaximum(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	tenantID := uuid.New()
	posterGCID := uuid.New()
	atomID := uuid.New()
	expiresAt := time.Now().Add(72 * time.Hour)

	_, err := d.svc.CreateBounty(ctx, tenantID, posterGCID, atomID, "Too expensive", "desc", 999, 0, expiresAt)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

func TestCreateBounty_InsufficientBalance(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	tenantID := uuid.New()
	posterGCID := uuid.New()
	atomID := uuid.New()
	expiresAt := time.Now().Add(72 * time.Hour)

	posterAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: posterGCID,
		Balance: 50, // Not enough for 200 reward.
	}

	d.coinR.On("GetByGCID", mock.Anything, tenantID, posterGCID).Return(posterAccount, nil)

	_, err := d.svc.CreateBounty(ctx, tenantID, posterGCID, atomID, "No coins", "desc", 200, 0, expiresAt)
	assert.ErrorIs(t, err, ErrInsufficientBalance)
}

func TestCreateBounty_NoAccount(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	tenantID := uuid.New()
	posterGCID := uuid.New()
	atomID := uuid.New()
	expiresAt := time.Now().Add(72 * time.Hour)

	d.coinR.On("GetByGCID", mock.Anything, tenantID, posterGCID).Return(nil, nil)

	_, err := d.svc.CreateBounty(ctx, tenantID, posterGCID, atomID, "No account", "desc", 100, 0, expiresAt)
	assert.ErrorIs(t, err, ErrInsufficientBalance)
}

// ---------------------------------------------------------------------------
// Tests — GetBounty
// ---------------------------------------------------------------------------

func TestGetBounty_Success(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()

	bounty := &KnowledgeBounty{
		ID: bountyID, Title: "Test bounty", Status: BountyStatusOpen,
	}

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(bounty, nil)

	got, err := d.svc.GetBounty(ctx, bountyID)
	assert.NoError(t, err)
	assert.Equal(t, "Test bounty", got.Title)
	d.bountyR.AssertExpectations(t)
}

func TestGetBounty_NotFound(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(nil, nil)

	_, err := d.svc.GetBounty(ctx, bountyID)
	assert.ErrorIs(t, err, ErrBountyNotFound)
}

// ---------------------------------------------------------------------------
// Tests — ListBounties
// ---------------------------------------------------------------------------

func TestListBounties_Success(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	tenantID := uuid.New()
	status := BountyStatusOpen

	bounties := []*KnowledgeBounty{
		{ID: uuid.New(), Status: BountyStatusOpen},
	}

	d.bountyR.On("List", mock.Anything, tenantID, &status, 0, 20).Return(bounties, nil)

	got, err := d.svc.ListBounties(ctx, tenantID, &status, 0, 20)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	d.bountyR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// Tests — SolveBounty
// ---------------------------------------------------------------------------

func TestSolveBounty_Success(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	posterGCID := uuid.New()
	solverGCID := uuid.New()
	tenantID := uuid.New()

	bounty := &KnowledgeBounty{
		ID: bountyID, TenantID: tenantID, PosterGCID: posterGCID,
		Status: BountyStatusOpen, CoinReward: 100,
	}

	solverAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: solverGCID,
		Balance: 50, LifetimeEarned: 200,
	}

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(bounty, nil)
	d.bountyR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.KnowledgeBounty")).Return(nil)
	d.coinR.On("GetByGCID", mock.Anything, tenantID, solverGCID).Return(solverAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.SolveBounty(ctx, bountyID, solverGCID, "Tail recursion reuses the stack frame...")
	assert.NoError(t, err)
	assert.Equal(t, BountyStatusCompleted, got.Status)
	assert.Equal(t, &solverGCID, got.SolverGCID)
	assert.NotNil(t, got.CompletedAt)
	// Solver's balance should have increased.
	assert.Equal(t, int64(150), solverAccount.Balance)
	assert.Equal(t, int64(300), solverAccount.LifetimeEarned)
	d.bountyR.AssertExpectations(t)
	d.coinR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestSolveBounty_NewSolverAccount(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	posterGCID := uuid.New()
	solverGCID := uuid.New()
	tenantID := uuid.New()

	bounty := &KnowledgeBounty{
		ID: bountyID, TenantID: tenantID, PosterGCID: posterGCID,
		Status: BountyStatusOpen, CoinReward: 75,
	}

	// Solver has no account yet.
	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(bounty, nil)
	d.bountyR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.KnowledgeBounty")).Return(nil)
	d.coinR.On("GetByGCID", mock.Anything, tenantID, solverGCID).Return(nil, nil)
	d.coinR.On("CreateIfNotExists", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.SolveBounty(ctx, bountyID, solverGCID, "Solution text")
	assert.NoError(t, err)
	assert.Equal(t, BountyStatusCompleted, got.Status)
	d.coinR.AssertExpectations(t)
	d.bountyR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestSolveBounty_SelfSolve(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	posterGCID := uuid.New()
	tenantID := uuid.New()

	bounty := &KnowledgeBounty{
		ID: bountyID, TenantID: tenantID, PosterGCID: posterGCID,
		Status: BountyStatusOpen, CoinReward: 100,
	}

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(bounty, nil)

	_, err := d.svc.SolveBounty(ctx, bountyID, posterGCID, "I solved my own bounty")
	assert.ErrorIs(t, err, ErrBountySelfSolve)
}

func TestSolveBounty_BountyNotOpen(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	solverGCID := uuid.New()
	tenantID := uuid.New()

	bounty := &KnowledgeBounty{
		ID: bountyID, TenantID: tenantID, PosterGCID: uuid.New(),
		Status: BountyStatusCompleted, CoinReward: 100,
	}

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(bounty, nil)

	_, err := d.svc.SolveBounty(ctx, bountyID, solverGCID, "Too late")
	assert.ErrorIs(t, err, ErrBountyNotOpen)
}

func TestSolveBounty_NotFound(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	solverGCID := uuid.New()

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(nil, nil)

	_, err := d.svc.SolveBounty(ctx, bountyID, solverGCID, "Solution")
	assert.ErrorIs(t, err, ErrBountyNotFound)
}

// ---------------------------------------------------------------------------
// Tests — CancelBounty
// ---------------------------------------------------------------------------

func TestCancelBounty_Success(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	posterGCID := uuid.New()
	tenantID := uuid.New()

	bounty := &KnowledgeBounty{
		ID: bountyID, TenantID: tenantID, PosterGCID: posterGCID,
		Status: BountyStatusOpen, CoinReward: 100,
	}

	posterAccount := &CoinAccount{
		ID: uuid.New(), TenantID: tenantID, GCID: posterGCID,
		Balance: 200,
	}

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(bounty, nil)
	d.bountyR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.KnowledgeBounty")).Return(nil)
	d.coinR.On("GetByGCID", mock.Anything, tenantID, posterGCID).Return(posterAccount, nil)
	d.coinR.On("UpdateBalance", mock.Anything, mock.AnythingOfType("*gamification.CoinAccount")).Return(nil)

	err := d.svc.CancelBounty(ctx, bountyID, posterGCID)
	assert.NoError(t, err)
	assert.Equal(t, BountyStatusCancelled, bounty.Status)
	// Poster's balance should have been refunded.
	assert.Equal(t, int64(300), posterAccount.Balance)
	d.bountyR.AssertExpectations(t)
	d.coinR.AssertExpectations(t)
}

func TestCancelBounty_NotPoster(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	posterGCID := uuid.New()
	otherGCID := uuid.New()
	tenantID := uuid.New()

	bounty := &KnowledgeBounty{
		ID: bountyID, TenantID: tenantID, PosterGCID: posterGCID,
		Status: BountyStatusOpen, CoinReward: 100,
	}

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(bounty, nil)

	err := d.svc.CancelBounty(ctx, bountyID, otherGCID)
	assert.ErrorIs(t, err, ErrForbidden)
}

func TestCancelBounty_NotOpen(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	posterGCID := uuid.New()
	tenantID := uuid.New()

	bounty := &KnowledgeBounty{
		ID: bountyID, TenantID: tenantID, PosterGCID: posterGCID,
		Status: BountyStatusCompleted, CoinReward: 100,
	}

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(bounty, nil)

	err := d.svc.CancelBounty(ctx, bountyID, posterGCID)
	assert.ErrorIs(t, err, ErrBountyNotOpen)
}

func TestCancelBounty_NotFound(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	bountyID := uuid.New()
	posterGCID := uuid.New()

	d.bountyR.On("GetByID", mock.Anything, bountyID).Return(nil, nil)

	err := d.svc.CancelBounty(ctx, bountyID, posterGCID)
	assert.ErrorIs(t, err, ErrBountyNotFound)
}

// ---------------------------------------------------------------------------
// Tests — ExpireBounties
// ---------------------------------------------------------------------------

func TestExpireBounties_ReturnsZero(t *testing.T) {
	t.Parallel()
	d := newTestBountyService()
	ctx := context.Background()
	tenantID := uuid.New()

	count, err := d.svc.ExpireBounties(ctx, tenantID)
	assert.NoError(t, err)
	assert.Equal(t, 0, count)
}
