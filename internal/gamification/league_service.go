package gamification

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// promotionThreshold defines the maximum rank for promotion (top 3).
const promotionThreshold = 3

// relegationThreshold defines the minimum rank for relegation (bottom 3 means rank >= this).
const relegationThreshold = 18

// nextLeagueTier returns the tier one level above the given tier.
// Returns the same tier if already at the highest.
func nextLeagueTier(t LeagueTier) LeagueTier {
	switch t {
	case LeagueTierBronze:
		return LeagueTierSilver
	case LeagueTierSilver:
		return LeagueTierGold
	case LeagueTierGold:
		return LeagueTierPlatinum
	case LeagueTierPlatinum:
		return LeagueTierDiamond
	default:
		return t
	}
}

// prevLeagueTier returns the tier one level below the given tier.
// Returns the same tier if already at the lowest.
func prevLeagueTier(t LeagueTier) LeagueTier {
	switch t {
	case LeagueTierDiamond:
		return LeagueTierPlatinum
	case LeagueTierPlatinum:
		return LeagueTierGold
	case LeagueTierGold:
		return LeagueTierSilver
	case LeagueTierSilver:
		return LeagueTierBronze
	default:
		return t
	}
}

// GetCurrentStanding returns a learner's league standing for the current week.
func (s *LeagueService) GetCurrentStanding(ctx context.Context, tenantID, gcid uuid.UUID, seasonWeek string) (*League, error) {
	standing, err := s.repo.GetByGCID(ctx, tenantID, gcid, seasonWeek)
	if err != nil {
		return nil, err
	}
	if standing == nil {
		return nil, ErrLeagueNotFound
	}
	return standing, nil
}

// RecordPoints adds points to a learner's league standing.
func (s *LeagueService) RecordPoints(ctx context.Context, tenantID, gcid uuid.UUID, seasonWeek string, points int) (*League, error) {
	standing, err := s.repo.GetByGCID(ctx, tenantID, gcid, seasonWeek)
	if err != nil {
		return nil, err
	}
	if standing == nil {
		standing = &League{
			ID:         uuid.Must(uuid.NewV7()),
			TenantID:   tenantID,
			GCID:       gcid,
			LeagueTier: LeagueTierBronze,
			SeasonWeek: seasonWeek,
			CreatedAt:  time.Now().UTC(),
			UpdatedAt:  time.Now().UTC(),
		}
	}

	standing.Points += points
	standing.UpdatedAt = time.Now().UTC()

	if err := s.repo.Upsert(ctx, standing); err != nil {
		return nil, err
	}

	return standing, nil
}

// GetStandings returns league standings for a tier and week.
func (s *LeagueService) GetStandings(ctx context.Context, tenantID uuid.UUID, tier LeagueTier, seasonWeek string, offset, limit int) ([]*League, error) {
	return s.repo.ListStandings(ctx, tenantID, tier, seasonWeek, offset, limit)
}

// ProcessPromotion checks and applies promotion for top-ranked learners.
func (s *LeagueService) ProcessPromotion(ctx context.Context, tenantID, gcid uuid.UUID, seasonWeek string) (*League, error) {
	standing, err := s.repo.GetByGCID(ctx, tenantID, gcid, seasonWeek)
	if err != nil {
		return nil, err
	}
	if standing == nil {
		return nil, ErrLeagueNotFound
	}

	if standing.Rank <= promotionThreshold {
		standing.Promoted = true
		standing.LeagueTier = nextLeagueTier(standing.LeagueTier)
		standing.UpdatedAt = time.Now().UTC()

		if err := s.repo.Upsert(ctx, standing); err != nil {
			return nil, err
		}

		event := NewDomainEvent(
			EventLeaguePromoted,
			tenantID,
			&gcid,
			standing.ID,
			AggregateLeague,
			map[string]interface{}{
				"new_tier":    string(standing.LeagueTier),
				"season_week": seasonWeek,
				"rank":        standing.Rank,
			},
		)
		if err := s.publisher.Publish(ctx, TopicGamificationEvents, event); err != nil {
			return nil, err
		}
	}

	return standing, nil
}

// ProcessRelegation checks and applies relegation for bottom-ranked learners.
func (s *LeagueService) ProcessRelegation(ctx context.Context, tenantID, gcid uuid.UUID, seasonWeek string) (*League, error) {
	standing, err := s.repo.GetByGCID(ctx, tenantID, gcid, seasonWeek)
	if err != nil {
		return nil, err
	}
	if standing == nil {
		return nil, ErrLeagueNotFound
	}

	if standing.Rank >= relegationThreshold {
		standing.Relegated = true
		standing.LeagueTier = prevLeagueTier(standing.LeagueTier)
		standing.UpdatedAt = time.Now().UTC()

		if err := s.repo.Upsert(ctx, standing); err != nil {
			return nil, err
		}

		event := NewDomainEvent(
			EventLeagueRelegated,
			tenantID,
			&gcid,
			standing.ID,
			AggregateLeague,
			map[string]interface{}{
				"new_tier":    string(standing.LeagueTier),
				"season_week": seasonWeek,
				"rank":        standing.Rank,
			},
		)
		if err := s.publisher.Publish(ctx, TopicGamificationEvents, event); err != nil {
			return nil, err
		}
	}

	return standing, nil
}

// AwardBadge awards a topic badge to a learner.
func (s *LeagueService) AwardBadge(ctx context.Context, tenantID, gcid, topicID uuid.UUID, tier BadgeTier) (*TopicBadge, error) {
	existing, err := s.badgeRepo.GetByGCIDAndTopic(ctx, tenantID, gcid, topicID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		// If they already have the same or higher tier, reject.
		if badgeTierRank(existing.BadgeTier) >= badgeTierRank(tier) {
			return nil, ErrBadgeAlreadyEarned
		}
	}

	badge := &TopicBadge{
		ID:        uuid.Must(uuid.NewV7()),
		TenantID:  tenantID,
		GCID:      gcid,
		TopicID:   topicID,
		BadgeTier: tier,
		EarnedAt:  time.Now().UTC(),
	}

	if err := s.badgeRepo.Create(ctx, badge); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventBadgeEarned,
		tenantID,
		&gcid,
		badge.ID,
		AggregateTopicBadge,
		map[string]interface{}{
			"topic_id":   topicID.String(),
			"badge_tier": string(tier),
		},
	)
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, event); err != nil {
		return nil, err
	}

	return badge, nil
}

// ListBadges returns all badges for a learner.
func (s *LeagueService) ListBadges(ctx context.Context, tenantID, gcid uuid.UUID, offset, limit int) ([]*TopicBadge, error) {
	return s.badgeRepo.ListByGCID(ctx, tenantID, gcid, offset, limit)
}

// badgeTierRank returns a numeric rank for badge tier comparison.
func badgeTierRank(t BadgeTier) int {
	switch t {
	case BadgeTierBronze:
		return 1
	case BadgeTierSilver:
		return 2
	case BadgeTierGold:
		return 3
	default:
		return 0
	}
}
