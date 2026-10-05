package gamification

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Bounty coin reward limits.
const (
	bountyMinReward = 50
	bountyMaxReward = 500
)

// CreateBounty creates a new knowledge bounty.
func (s *BountyService) CreateBounty(ctx context.Context, tenantID, posterGCID, atomID uuid.UUID, title, description string, coinReward int, repRequirement int, expiresAt time.Time) (*KnowledgeBounty, error) {
	if coinReward < bountyMinReward || coinReward > bountyMaxReward {
		return nil, ErrValidationFailed
	}

	// Check poster has enough coins to escrow.
	posterAccount, err := s.coinRepo.GetByGCID(ctx, tenantID, posterGCID)
	if err != nil {
		return nil, err
	}
	if posterAccount == nil || posterAccount.Balance < int64(coinReward) {
		return nil, ErrInsufficientBalance
	}

	// Deduct coins from poster (escrow).
	posterAccount.Balance -= int64(coinReward)
	posterAccount.LifetimeSpent += int64(coinReward)
	posterAccount.UpdatedAt = time.Now().UTC()
	if err := s.coinRepo.UpdateBalance(ctx, posterAccount); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	bounty := &KnowledgeBounty{
		ID:                    uuid.Must(uuid.NewV7()),
		TenantID:              tenantID,
		PosterGCID:            posterGCID,
		AtomID:                atomID,
		Title:                 title,
		Description:           description,
		CoinReward:            coinReward,
		ReputationRequirement: repRequirement,
		Status:                BountyStatusOpen,
		CreatedAt:             now,
		ExpiresAt:             expiresAt,
	}

	if err := s.repo.Create(ctx, bounty); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventBountyCreated,
		tenantID,
		&posterGCID,
		bounty.ID,
		AggregateKnowledgeBounty,
		map[string]interface{}{
			"title":       title,
			"coin_reward": coinReward,
		},
	)
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, event); err != nil {
		return nil, err
	}

	return bounty, nil
}

// GetBounty retrieves a bounty by ID.
func (s *BountyService) GetBounty(ctx context.Context, id uuid.UUID) (*KnowledgeBounty, error) {
	bounty, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if bounty == nil {
		return nil, ErrBountyNotFound
	}
	return bounty, nil
}

// ListBounties returns bounties matching filters.
func (s *BountyService) ListBounties(ctx context.Context, tenantID uuid.UUID, status *BountyStatus, offset, limit int) ([]*KnowledgeBounty, error) {
	return s.repo.List(ctx, tenantID, status, offset, limit)
}

// SolveBounty submits a solution for a bounty.
func (s *BountyService) SolveBounty(ctx context.Context, bountyID, solverGCID uuid.UUID, solutionText string) (*KnowledgeBounty, error) {
	bounty, err := s.repo.GetByID(ctx, bountyID)
	if err != nil {
		return nil, err
	}
	if bounty == nil {
		return nil, ErrBountyNotFound
	}
	if bounty.Status != BountyStatusOpen {
		return nil, ErrBountyNotOpen
	}
	if bounty.PosterGCID == solverGCID {
		return nil, ErrBountySelfSolve
	}

	now := time.Now().UTC()
	bounty.Status = BountyStatusCompleted
	bounty.SolverGCID = &solverGCID
	bounty.SolutionText = &solutionText
	bounty.CompletedAt = &now

	if err := s.repo.Update(ctx, bounty); err != nil {
		return nil, err
	}

	// Award coins to solver.
	solverAccount, err := s.coinRepo.GetByGCID(ctx, bounty.TenantID, solverGCID)
	if err != nil {
		return nil, err
	}
	if solverAccount == nil {
		solverAccount = &CoinAccount{
			ID:        uuid.Must(uuid.NewV7()),
			TenantID:  bounty.TenantID,
			GCID:      solverGCID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := s.coinRepo.CreateIfNotExists(ctx, solverAccount); err != nil {
			return nil, err
		}
	}
	solverAccount.Balance += int64(bounty.CoinReward)
	solverAccount.LifetimeEarned += int64(bounty.CoinReward)
	solverAccount.UpdatedAt = now
	if err := s.coinRepo.UpdateBalance(ctx, solverAccount); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventBountyCompleted,
		bounty.TenantID,
		&solverGCID,
		bounty.ID,
		AggregateKnowledgeBounty,
		map[string]interface{}{
			"solver_gcid": solverGCID.String(),
			"coin_reward": bounty.CoinReward,
		},
	)
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, event); err != nil {
		return nil, err
	}

	return bounty, nil
}

// CancelBounty cancels a bounty (poster only).
func (s *BountyService) CancelBounty(ctx context.Context, bountyID, posterGCID uuid.UUID) error {
	bounty, err := s.repo.GetByID(ctx, bountyID)
	if err != nil {
		return err
	}
	if bounty == nil {
		return ErrBountyNotFound
	}
	if bounty.PosterGCID != posterGCID {
		return ErrForbidden
	}
	if bounty.Status != BountyStatusOpen {
		return ErrBountyNotOpen
	}

	bounty.Status = BountyStatusCancelled
	if err := s.repo.Update(ctx, bounty); err != nil {
		return err
	}

	// Refund coins to poster.
	posterAccount, err := s.coinRepo.GetByGCID(ctx, bounty.TenantID, posterGCID)
	if err != nil {
		return err
	}
	if posterAccount != nil {
		posterAccount.Balance += int64(bounty.CoinReward)
		posterAccount.UpdatedAt = time.Now().UTC()
		if err := s.coinRepo.UpdateBalance(ctx, posterAccount); err != nil {
			return err
		}
	}

	return nil
}

// ExpireBounties marks expired bounties as expired.
// Batch operation — actual repo query for expired bounties deferred to adapter layer.
func (s *BountyService) ExpireBounties(ctx context.Context, tenantID uuid.UUID) (int, error) {
	return 0, nil
}
