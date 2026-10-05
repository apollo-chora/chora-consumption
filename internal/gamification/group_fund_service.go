package gamification

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// NewGroupFundService creates a new GroupFundService.
func NewGroupFundService(
	poolRepo GroupFundPoolRepository,
	contributionRepo GroupFundContributionRepository,
	publisher EventPublisher,
) *GroupFundService {
	return &GroupFundService{
		poolRepo:         poolRepo,
		contributionRepo: contributionRepo,
		publisher:        publisher,
	}
}

// GroupFundService handles boss pool and group fund contributions.
type GroupFundService struct {
	poolRepo         GroupFundPoolRepository
	contributionRepo GroupFundContributionRepository
	publisher        EventPublisher
}

// CreatePool creates a new group fund pool.
func (s *GroupFundService) CreatePool(
	ctx context.Context,
	tenantID uuid.UUID,
	poolName string,
	poolType PoolType,
	targetAmount int,
) (*GroupFundPool, error) {
	if !poolType.IsValid() {
		return nil, ErrValidationFailed
	}

	now := time.Now().UTC()
	pool := &GroupFundPool{
		ID:            uuid.Must(uuid.NewV7()),
		TenantID:      tenantID,
		PoolName:      poolName,
		PoolType:      poolType,
		TargetAmount:  targetAmount,
		CurrentAmount: 0,
		Status:        PoolStatusCollecting,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := s.poolRepo.Create(ctx, pool); err != nil {
		return nil, err
	}

	return pool, nil
}

// Contribute adds funds to a group fund pool.
func (s *GroupFundService) Contribute(
	ctx context.Context,
	tenantID, poolID, gcid uuid.UUID,
	amount int,
) (*GroupFundContribution, error) {
	if amount <= 0 {
		return nil, ErrValidationFailed
	}

	pool, err := s.poolRepo.GetByID(ctx, poolID)
	if err != nil {
		return nil, err
	}

	if pool.Status != PoolStatusCollecting {
		return nil, ErrPoolNotCollecting
	}

	contribution := &GroupFundContribution{
		ID:        uuid.Must(uuid.NewV7()),
		TenantID:  tenantID,
		PoolID:    poolID,
		GCID:      gcid,
		Amount:    amount,
		CreatedAt: time.Now().UTC(),
	}

	if err := s.contributionRepo.Create(ctx, contribution); err != nil {
		return nil, err
	}

	// Update pool current amount
	pool.CurrentAmount += amount
	pool.UpdatedAt = time.Now().UTC()
	if err := s.poolRepo.Update(ctx, pool); err != nil {
		return nil, err
	}

	return contribution, nil
}

// DistributePool marks a pool as distributed and publishes the event.
func (s *GroupFundService) DistributePool(
	ctx context.Context,
	poolID uuid.UUID,
) error {
	pool, err := s.poolRepo.GetByID(ctx, poolID)
	if err != nil {
		return err
	}

	if pool.Status != PoolStatusCollecting {
		return ErrPoolNotCollecting
	}

	pool.Status = PoolStatusDistributed
	pool.UpdatedAt = time.Now().UTC()
	if err := s.poolRepo.Update(ctx, pool); err != nil {
		return err
	}

	event := NewDomainEvent(
		EventGroupFundDistributed,
		pool.TenantID,
		nil,
		pool.ID,
		AggregateGroupFundPool,
		map[string]interface{}{
			"pool_id":         pool.ID.String(),
			"pool_name":       pool.PoolName,
			"pool_type":       string(pool.PoolType),
			"total_collected": pool.CurrentAmount,
			"target_amount":   pool.TargetAmount,
		},
	)
	_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)

	return nil
}
