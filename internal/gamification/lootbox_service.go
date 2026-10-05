package gamification

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"io"
	"math/big"
	"time"

	"github.com/google/uuid"
)

// Grant creates a new unopened lootbox for a learner.
func (s *LootboxService) Grant(
	ctx context.Context,
	tenantID, gcid uuid.UUID,
	tier LootboxTier,
	source LootboxSource,
) (*Lootbox, error) {
	lootbox := &Lootbox{
		ID:          uuid.Must(uuid.NewV7()),
		TenantID:    tenantID,
		GCID:        gcid,
		LootboxTier: tier,
		Source:      source,
		IsOpened:    false,
		CreatedAt:   time.Now().UTC(),
	}

	if err := s.lootboxRepo.Create(ctx, lootbox); err != nil {
		return nil, err
	}

	return lootbox, nil
}

// Open opens a lootbox using weighted random selection from the loot table.
func (s *LootboxService) Open(ctx context.Context, lootboxID uuid.UUID) (*LootResult, error) {
	lootbox, err := s.lootboxRepo.GetByID(ctx, lootboxID)
	if err != nil {
		return nil, err
	}
	if lootbox == nil {
		return nil, ErrLootboxNotFound
	}
	if lootbox.IsOpened {
		return nil, ErrLootboxAlreadyOpened
	}

	entries, err := s.lootTableRepo.ListByTier(ctx, lootbox.TenantID, string(lootbox.LootboxTier))
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, ErrLootTableEmpty
	}

	// Weighted random selection. Both draws come from a cryptographically
	// secure source and both fail loud: an undrawable lootbox stays unopened
	// rather than being burned for a guessed reward.
	selected, err := weightedSelect(entries, s.entropySource())
	if err != nil {
		return nil, err
	}

	// Determine quantity within the min/max range.
	quantity, err := rollQuantity(s.entropySource(), selected.QuantityMin, selected.QuantityMax)
	if err != nil {
		return nil, err
	}

	result := &LootResult{
		ItemType: selected.ItemType,
		ItemID:   selected.ItemID,
		Quantity: quantity,
	}

	now := time.Now().UTC()
	lootbox.IsOpened = true
	lootbox.OpenedAt = &now
	lootbox.RewardSummary = map[string]interface{}{
		"item_type": string(result.ItemType),
		"quantity":  result.Quantity,
	}

	if err := s.lootboxRepo.Update(ctx, lootbox); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventLootboxOpened,
		lootbox.TenantID,
		&lootbox.GCID,
		lootbox.ID,
		AggregateLootbox,
		map[string]interface{}{
			"tier":      string(lootbox.LootboxTier),
			"item_type": string(result.ItemType),
			"quantity":  result.Quantity,
		},
	)
	_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)

	return result, nil
}

// entropySource returns the reader every loot draw reads from. Production
// leaves LootboxService.entropy nil and gets crypto/rand.Reader; tests inject
// their own reader.
func (s *LootboxService) entropySource() io.Reader {
	if s.entropy != nil {
		return s.entropy
	}
	return crand.Reader
}

// weightedSelect picks a LootTable entry with probability proportional to its
// weight, drawing from a cryptographically secure source.
//
// The draw is security relevant, so math/rand is not acceptable here (gosec
// G404). Loot is real economic value in the three-currency economy: the entry
// chosen here becomes XP, Coins, Reputation or an item in the learner's
// wallet. math/rand's global source is a deterministic PRNG whose stream a
// holder of a few observed outcomes can reconstruct, which turns "open a
// lootbox" into a farmable oracle. Unlike the seeded daily-dose samplers in
// internal/domain/companion, nothing here requires reproducibility, so there
// is no reason to keep a weak source.
func weightedSelect(entries []*LootTable, entropy io.Reader) (*LootTable, error) {
	totalWeight := 0
	for _, e := range entries {
		if e.Weight < 0 {
			return nil, fmt.Errorf("%w: entry %s carries weight %d",
				ErrLootTableWeightsInvalid, e.ID, e.Weight)
		}
		totalWeight += e.Weight
	}
	if totalWeight <= 0 {
		return nil, fmt.Errorf("%w: %d entries sum to weight %d",
			ErrLootTableWeightsInvalid, len(entries), totalWeight)
	}

	r, err := secureIntn(entropy, totalWeight)
	if err != nil {
		return nil, fmt.Errorf("gamification: draw loot entry: %w", err)
	}

	cumulative := 0
	for _, e := range entries {
		cumulative += e.Weight
		if r < cumulative {
			return e, nil
		}
	}
	return entries[len(entries)-1], nil
}

// rollQuantity draws an inclusive [minQty, maxQty] reward quantity from the
// same secure source. A fixed range needs no draw at all.
func rollQuantity(entropy io.Reader, minQty, maxQty int) (int, error) {
	if maxQty <= minQty {
		return minQty, nil
	}
	offset, err := secureIntn(entropy, maxQty-minQty+1)
	if err != nil {
		return 0, fmt.Errorf("gamification: draw loot quantity: %w", err)
	}
	return minQty + offset, nil
}

// secureIntn returns a uniform value in [0, n) read from entropy. It is the
// crypto/rand replacement for rand.Intn: crypto/rand.Int does the rejection
// sampling that keeps the draw unbiased, and it reports a starved source
// instead of silently returning a predictable value. Callers must have
// already established n > 0 (crypto/rand.Int panics on a non-positive bound);
// the guard below keeps that contract enforced rather than assumed.
func secureIntn(entropy io.Reader, n int) (int, error) {
	if n <= 0 {
		return 0, fmt.Errorf("gamification: random bound must be positive, got %d", n)
	}
	v, err := crand.Int(entropy, big.NewInt(int64(n)))
	if err != nil {
		return 0, err
	}
	// v is in [0, n) and n came in as an int, so the narrowing is bounded by
	// construction.
	return int(v.Int64()), nil
}

// DailyCheckin records a daily login, tracks streak, and grants a lootbox every 7 days.
func (s *LoginCalendarService) DailyCheckin(
	ctx context.Context,
	tenantID, gcid uuid.UUID,
) (*LoginCalendar, error) {
	today := time.Now().UTC().Truncate(24 * time.Hour)

	// Check if already checked in today.
	existing, err := s.calendarRepo.GetByDate(ctx, tenantID, gcid, today)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, ErrDailyCheckinAlreadyClaimed
	}

	// Check yesterday's entry for streak continuation.
	yesterday := today.AddDate(0, 0, -1)
	yesterdayEntry, err := s.calendarRepo.GetByDate(ctx, tenantID, gcid, yesterday)
	if err != nil {
		return nil, err
	}

	streakDay := 1
	if yesterdayEntry != nil {
		streakDay = yesterdayEntry.StreakDay + 1
	}

	rewardType := "coins"
	rewardValue := 10 * streakDay
	if rewardValue > 100 {
		rewardValue = 100
	}

	// Every 7 days, grant a lootbox instead.
	if streakDay%7 == 0 {
		rewardType = "lootbox"
		rewardValue = 1

		_, err := s.lootboxSvc.Grant(ctx, tenantID, gcid, LootboxTierStandard, LootboxSourceDailyLogin)
		if err != nil {
			return nil, err
		}
	}

	entry := &LoginCalendar{
		ID:            uuid.Must(uuid.NewV7()),
		TenantID:      tenantID,
		GCID:          gcid,
		LoginDate:     today,
		StreakDay:     streakDay,
		RewardClaimed: true,
		RewardType:    rewardType,
		RewardValue:   rewardValue,
		CreatedAt:     time.Now().UTC(),
	}

	if err := s.calendarRepo.Create(ctx, entry); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventDailyCheckinCompleted,
		tenantID,
		&gcid,
		entry.ID,
		AggregateLoginCalendar,
		map[string]interface{}{
			"streak_day":  streakDay,
			"reward_type": rewardType,
		},
	)
	_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)

	return entry, nil
}
