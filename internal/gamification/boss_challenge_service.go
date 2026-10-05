package gamification

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Create schedules a new boss challenge.
func (s *BossChallengeService) Create(
	ctx context.Context,
	tenantID uuid.UUID,
	bossName string,
	topicNodeID uuid.UUID,
	difficulty BossDifficultyTier,
	requiredScore, rewardXP, rewardCoins int,
	rewardCardID *uuid.UUID,
	startsAt, endsAt time.Time,
) (*BossChallenge, error) {
	if !difficulty.IsValid() {
		return nil, ErrValidationFailed
	}

	now := time.Now().UTC()
	challenge := &BossChallenge{
		ID:             uuid.Must(uuid.NewV7()),
		TenantID:       tenantID,
		BossName:       bossName,
		TopicNodeID:    topicNodeID,
		DifficultyTier: difficulty,
		RequiredScore:  requiredScore,
		RewardXP:       rewardXP,
		RewardCoins:    rewardCoins,
		RewardCardID:   rewardCardID,
		Status:         BossChallengeStatusScheduled,
		ScheduledAt:    now,
		StartsAt:       startsAt,
		EndsAt:         endsAt,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := s.challengeRepo.Create(ctx, challenge); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventBossChallengeCreated,
		tenantID,
		nil,
		challenge.ID,
		AggregateBossChallenge,
		map[string]interface{}{
			"boss_name":       bossName,
			"difficulty_tier": string(difficulty),
		},
	)
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, event); err != nil {
		return nil, err
	}

	return challenge, nil
}

// Start transitions a scheduled boss challenge to active.
func (s *BossChallengeService) Start(ctx context.Context, challengeID uuid.UUID) (*BossChallenge, error) {
	challenge, err := s.challengeRepo.GetByID(ctx, challengeID)
	if err != nil {
		return nil, err
	}
	if challenge == nil {
		return nil, ErrBossChallengeNotFound
	}
	if challenge.Status != BossChallengeStatusScheduled {
		return nil, ErrBossChallengeNotScheduled
	}

	challenge.Status = BossChallengeStatusActive
	challenge.UpdatedAt = time.Now().UTC()

	if err := s.challengeRepo.Update(ctx, challenge); err != nil {
		return nil, err
	}

	return challenge, nil
}

// RecordParticipation records or updates a learner's score in an active challenge.
func (s *BossChallengeService) RecordParticipation(
	ctx context.Context,
	challengeID, gcid uuid.UUID,
	score int,
) (*BossChallengeParticipant, error) {
	challenge, err := s.challengeRepo.GetByID(ctx, challengeID)
	if err != nil {
		return nil, err
	}
	if challenge == nil {
		return nil, ErrBossChallengeNotFound
	}
	if challenge.Status != BossChallengeStatusActive {
		return nil, ErrBossChallengeNotActive
	}

	participant := &BossChallengeParticipant{
		ID:          uuid.Must(uuid.NewV7()),
		TenantID:    challenge.TenantID,
		ChallengeID: challengeID,
		GCID:        gcid,
		Score:       score,
		CreatedAt:   time.Now().UTC(),
	}

	if err := s.participantRepo.Upsert(ctx, participant); err != nil {
		return nil, err
	}

	return participant, nil
}

// Complete ends an active challenge, distributes rewards to qualifying participants.
func (s *BossChallengeService) Complete(ctx context.Context, challengeID uuid.UUID) (*BossChallenge, error) {
	challenge, err := s.challengeRepo.GetByID(ctx, challengeID)
	if err != nil {
		return nil, err
	}
	if challenge == nil {
		return nil, ErrBossChallengeNotFound
	}
	if challenge.Status != BossChallengeStatusActive {
		return nil, ErrBossChallengeNotActive
	}

	participants, err := s.participantRepo.ListByChallenge(ctx, challengeID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	// Distribute rewards to qualifying participants.
	for _, p := range participants {
		if p.Score >= challenge.RequiredScore {
			p.RewardClaimed = true
			p.CompletedAt = &now
			if err := s.participantRepo.Upsert(ctx, p); err != nil {
				return nil, err
			}
		}
	}

	challenge.Status = BossChallengeStatusCompleted
	challenge.UpdatedAt = now

	if err := s.challengeRepo.Update(ctx, challenge); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventBossChallengeCompleted,
		challenge.TenantID,
		nil,
		challenge.ID,
		AggregateBossChallenge,
		map[string]interface{}{
			"boss_name":         challenge.BossName,
			"participant_count": len(participants),
		},
	)
	_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)

	return challenge, nil
}

// ListActive returns active boss challenges for a tenant.
func (s *BossChallengeService) ListActive(ctx context.Context, tenantID uuid.UUID, offset, limit int) ([]*BossChallenge, error) {
	return s.challengeRepo.ListByStatus(ctx, tenantID, BossChallengeStatusActive, offset, limit)
}
