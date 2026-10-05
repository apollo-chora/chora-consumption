package engagement

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestXPService_GetXP(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("returns existing ledger and entries", func(t *testing.T) {
		t.Parallel()

		repo := new(mockXPLedgerRepo)
		publisher := new(mockEventPublisher)

		ledger := &XPLedger{
			ID:        uuid.Must(uuid.NewV7()),
			GCID:      gcid,
			TenantID:  tenantID,
			TotalXP:   250,
			Level:     2,
			ComboTier: ComboTierBronze,
		}
		entries := []XPEntry{
			{ID: uuid.Must(uuid.NewV7()), XPEarned: 10, Source: XPSourceAtomCorrect},
			{ID: uuid.Must(uuid.NewV7()), XPEarned: 50, Source: XPSourceDailyDoseBonus},
		}

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(ledger, nil)
		repo.On("ListEntries", mock.Anything, gcid, tenantID, 10).Return(entries, nil)

		svc := NewXPService(repo, publisher)
		got, gotEntries, err := svc.GetXP(context.Background(), gcid, tenantID, 10)

		require.NoError(t, err)
		assert.Equal(t, 250, got.TotalXP)
		assert.Equal(t, 2, got.Level)
		assert.Len(t, gotEntries, 2)
	})

	t.Run("returns default ledger when not found", func(t *testing.T) {
		t.Parallel()

		repo := new(mockXPLedgerRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
		repo.On("ListEntries", mock.Anything, gcid, tenantID, 5).Return([]XPEntry{}, nil)

		svc := NewXPService(repo, publisher)
		got, _, err := svc.GetXP(context.Background(), gcid, tenantID, 5)

		require.NoError(t, err)
		assert.Equal(t, 0, got.TotalXP)
		assert.Equal(t, 1, got.Level)
		assert.Equal(t, ComboTierBase, got.ComboTier)
	})

	t.Run("propagates repository error", func(t *testing.T) {
		t.Parallel()

		repo := new(mockXPLedgerRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, errors.New("db error"))

		svc := NewXPService(repo, publisher)
		_, _, err := svc.GetXP(context.Background(), gcid, tenantID, 5)

		require.Error(t, err)
		assert.ErrorContains(t, err, "db error")
	})
}

func TestXPService_AwardXP(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("increments total XP and recalculates level", func(t *testing.T) {
		t.Parallel()

		repo := new(mockXPLedgerRepo)
		publisher := new(mockEventPublisher)

		ledger := &XPLedger{
			ID:        uuid.Must(uuid.NewV7()),
			GCID:      gcid,
			TenantID:  tenantID,
			TotalXP:   90,
			Level:     1,
			ComboTier: ComboTierBase,
		}

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(ledger, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.XPLedger")).Return(nil)
		repo.On("AppendEntry", mock.Anything, mock.AnythingOfType("*engagement.XPEntry")).Return(nil)
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).Return(nil)

		svc := NewXPService(repo, publisher)
		got, err := svc.AwardXP(context.Background(), gcid, tenantID, 20, XPSourceAtomCorrect, nil, 2)

		require.NoError(t, err)
		assert.Equal(t, 110, got.TotalXP)
		assert.Equal(t, 2, got.Level) // 100 XP → level 2
	})

	t.Run("creates default ledger when none exists", func(t *testing.T) {
		t.Parallel()

		repo := new(mockXPLedgerRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.XPLedger")).Return(nil)
		repo.On("AppendEntry", mock.Anything, mock.AnythingOfType("*engagement.XPEntry")).Return(nil)
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).Return(nil)

		svc := NewXPService(repo, publisher)
		got, err := svc.AwardXP(context.Background(), gcid, tenantID, 10, XPSourceAtomCorrect, nil, 1)

		require.NoError(t, err)
		assert.Equal(t, 10, got.TotalXP)
		assert.Equal(t, 1, got.Level)
	})

	t.Run("appends XP entry with correct fields", func(t *testing.T) {
		t.Parallel()

		repo := new(mockXPLedgerRepo)
		publisher := new(mockEventPublisher)

		atomID := uuid.Must(uuid.NewV7())
		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.XPLedger")).Return(nil)

		var capturedEntry *XPEntry
		repo.On("AppendEntry", mock.Anything, mock.AnythingOfType("*engagement.XPEntry")).
			Run(func(args mock.Arguments) {
				capturedEntry = args.Get(1).(*XPEntry)
			}).Return(nil)
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).Return(nil)

		svc := NewXPService(repo, publisher)
		_, err := svc.AwardXP(context.Background(), gcid, tenantID, 20, XPSourceAtomCorrect, &atomID, 2)

		require.NoError(t, err)
		require.NotNil(t, capturedEntry)
		assert.Equal(t, 20, capturedEntry.XPEarned)
		assert.Equal(t, XPSourceAtomCorrect, capturedEntry.Source)
		assert.Equal(t, &atomID, capturedEntry.AtomID)
		assert.Equal(t, 2, capturedEntry.ComboMultiplier)
	})

	t.Run("publishes XPAwarded event with correct payload", func(t *testing.T) {
		t.Parallel()

		repo := new(mockXPLedgerRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.XPLedger")).Return(nil)
		repo.On("AppendEntry", mock.Anything, mock.AnythingOfType("*engagement.XPEntry")).Return(nil)

		var capturedEvent DomainEvent
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).
			Run(func(args mock.Arguments) {
				capturedEvent = args.Get(2).(DomainEvent)
			}).Return(nil)

		svc := NewXPService(repo, publisher)
		_, err := svc.AwardXP(context.Background(), gcid, tenantID, 10, XPSourceAtomCorrect, nil, 1)

		require.NoError(t, err)
		assert.Equal(t, EventXPAwarded, capturedEvent.EventType)
		assert.Equal(t, "XPLedger", capturedEvent.AggregateType)
		// AsyncAPI contract: required payload fields for engagement.xp.awarded
		assert.Contains(t, capturedEvent.Payload, "gcid")
		assert.Contains(t, capturedEvent.Payload, "xp_amount")
		assert.Contains(t, capturedEvent.Payload, "source")
		assert.Contains(t, capturedEvent.Payload, "new_total")
		assert.Contains(t, capturedEvent.Payload, "new_level")
		assert.Equal(t, 10, capturedEvent.Payload["xp_amount"])
		assert.Equal(t, "atom_correct", capturedEvent.Payload["source"])
	})

	t.Run("publishes LevelUp event when level increases", func(t *testing.T) {
		t.Parallel()

		repo := new(mockXPLedgerRepo)
		publisher := new(mockEventPublisher)

		ledger := &XPLedger{
			ID:        uuid.Must(uuid.NewV7()),
			GCID:      gcid,
			TenantID:  tenantID,
			TotalXP:   90,
			Level:     1,
			ComboTier: ComboTierBase,
		}

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(ledger, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.XPLedger")).Return(nil)
		repo.On("AppendEntry", mock.Anything, mock.AnythingOfType("*engagement.XPEntry")).Return(nil)

		var events []DomainEvent
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).
			Run(func(args mock.Arguments) {
				events = append(events, args.Get(2).(DomainEvent))
			}).Return(nil)

		svc := NewXPService(repo, publisher)
		_, err := svc.AwardXP(context.Background(), gcid, tenantID, 20, XPSourceAtomCorrect, nil, 1)

		require.NoError(t, err)
		require.Len(t, events, 2, "should publish XPAwarded + LevelUp")

		lvlEvt := events[1]
		assert.Equal(t, EventLevelUp, lvlEvt.EventType)
		assert.Equal(t, "XPLedger", lvlEvt.AggregateType)
		// AsyncAPI contract: required payload fields for engagement.level.up
		assert.Contains(t, lvlEvt.Payload, "gcid")
		assert.Contains(t, lvlEvt.Payload, "new_level")
		assert.Contains(t, lvlEvt.Payload, "previous_level")
		assert.Contains(t, lvlEvt.Payload, "total_xp")
		assert.Equal(t, 2, lvlEvt.Payload["new_level"])
		assert.Equal(t, 1, lvlEvt.Payload["previous_level"])
	})

	t.Run("does not publish LevelUp when level unchanged", func(t *testing.T) {
		t.Parallel()

		repo := new(mockXPLedgerRepo)
		publisher := new(mockEventPublisher)

		ledger := &XPLedger{
			ID:        uuid.Must(uuid.NewV7()),
			GCID:      gcid,
			TenantID:  tenantID,
			TotalXP:   50,
			Level:     1,
			ComboTier: ComboTierBase,
		}

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(ledger, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.XPLedger")).Return(nil)
		repo.On("AppendEntry", mock.Anything, mock.AnythingOfType("*engagement.XPEntry")).Return(nil)
		publisher.On("Publish", mock.Anything, TopicEngagementEvents, mock.AnythingOfType("engagement.DomainEvent")).Return(nil)

		svc := NewXPService(repo, publisher)
		_, err := svc.AwardXP(context.Background(), gcid, tenantID, 10, XPSourceAtomCorrect, nil, 1)

		require.NoError(t, err)
		publisher.AssertNumberOfCalls(t, "Publish", 1) // only XPAwarded, no LevelUp
	})

	t.Run("propagates save error", func(t *testing.T) {
		t.Parallel()

		repo := new(mockXPLedgerRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.XPLedger")).Return(errors.New("save failed"))

		svc := NewXPService(repo, publisher)
		_, err := svc.AwardXP(context.Background(), gcid, tenantID, 10, XPSourceAtomCorrect, nil, 1)

		require.Error(t, err)
		assert.ErrorContains(t, err, "save failed")
	})
}

func TestXPService_UpdateCombo(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("correct answer advances tier", func(t *testing.T) {
		t.Parallel()

		repo := new(mockXPLedgerRepo)
		publisher := new(mockEventPublisher)

		ledger := &XPLedger{
			ID:        uuid.Must(uuid.NewV7()),
			GCID:      gcid,
			TenantID:  tenantID,
			ComboTier: ComboTierBase,
		}

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(ledger, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.XPLedger")).Return(nil)

		svc := NewXPService(repo, publisher)
		tier, err := svc.UpdateCombo(context.Background(), gcid, tenantID, true)

		require.NoError(t, err)
		assert.Equal(t, ComboTierBronze, tier)
	})

	t.Run("incorrect answer resets to base", func(t *testing.T) {
		t.Parallel()

		repo := new(mockXPLedgerRepo)
		publisher := new(mockEventPublisher)

		ledger := &XPLedger{
			ID:        uuid.Must(uuid.NewV7()),
			GCID:      gcid,
			TenantID:  tenantID,
			ComboTier: ComboTierGold,
		}

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(ledger, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.XPLedger")).Return(nil)

		svc := NewXPService(repo, publisher)
		tier, err := svc.UpdateCombo(context.Background(), gcid, tenantID, false)

		require.NoError(t, err)
		assert.Equal(t, ComboTierBase, tier)
	})

	t.Run("gold stays gold on correct", func(t *testing.T) {
		t.Parallel()

		repo := new(mockXPLedgerRepo)
		publisher := new(mockEventPublisher)

		ledger := &XPLedger{
			ID:        uuid.Must(uuid.NewV7()),
			GCID:      gcid,
			TenantID:  tenantID,
			ComboTier: ComboTierGold,
		}

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(ledger, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.XPLedger")).Return(nil)

		svc := NewXPService(repo, publisher)
		tier, err := svc.UpdateCombo(context.Background(), gcid, tenantID, true)

		require.NoError(t, err)
		assert.Equal(t, ComboTierGold, tier)
	})

	t.Run("returns base when no ledger exists", func(t *testing.T) {
		t.Parallel()

		repo := new(mockXPLedgerRepo)
		publisher := new(mockEventPublisher)

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)

		svc := NewXPService(repo, publisher)
		tier, err := svc.UpdateCombo(context.Background(), gcid, tenantID, true)

		require.NoError(t, err)
		assert.Equal(t, ComboTierBase, tier)
	})

	t.Run("full escalation path: base → bronze → silver → gold", func(t *testing.T) {
		t.Parallel()

		repo := new(mockXPLedgerRepo)
		publisher := new(mockEventPublisher)

		ledger := &XPLedger{
			ID:        uuid.Must(uuid.NewV7()),
			GCID:      gcid,
			TenantID:  tenantID,
			ComboTier: ComboTierBase,
			UpdatedAt: time.Now().UTC(),
		}

		repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(ledger, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.XPLedger")).Return(nil)

		svc := NewXPService(repo, publisher)

		// base → bronze
		tier, err := svc.UpdateCombo(context.Background(), gcid, tenantID, true)
		require.NoError(t, err)
		assert.Equal(t, ComboTierBronze, tier)

		// bronze → silver
		tier, err = svc.UpdateCombo(context.Background(), gcid, tenantID, true)
		require.NoError(t, err)
		assert.Equal(t, ComboTierSilver, tier)

		// silver → gold
		tier, err = svc.UpdateCombo(context.Background(), gcid, tenantID, true)
		require.NoError(t, err)
		assert.Equal(t, ComboTierGold, tier)
	})
}
