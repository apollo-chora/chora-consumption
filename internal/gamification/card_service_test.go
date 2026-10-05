package gamification

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// CardService Tests
// ---------------------------------------------------------------------------

func TestCardService_AwardCard(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	cardID := uuid.New()

	card := &TradingCard{
		ID:            cardID,
		TenantID:      tenantID,
		CardName:      "Algebra Ace",
		CardArchetype: CardArchetypeAtom,
		Rarity:        CardRarityRare,
		StatPower:     75,
		StatKnowledge: 80,
		IsTradable:    true,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	t.Run("success — awards card with serial number", func(t *testing.T) {
		cardRepo := new(mockTradingCardRepo)
		instanceRepo := new(mockCardInstanceRepo)
		econRepo := new(mockEconomyConfigRepo)
		pub := new(mockEventPublisher)
		svc := NewCardService(cardRepo, instanceRepo, econRepo, pub)

		cardRepo.On("GetByID", ctx, cardID).Return(card, nil)
		instanceRepo.On("NextSerialNumber", ctx, cardID).Return(42, nil)
		instanceRepo.On("Create", ctx, mock.AnythingOfType("*gamification.TradingCardInstance")).Return(nil)
		pub.On("Publish", ctx, TopicGamificationEvents, mock.Anything).Return(nil)

		instance, err := svc.AwardCard(ctx, tenantID, gcid, cardID, AcquiredViaDirectAward, false)
		require.NoError(t, err)
		assert.Equal(t, cardID, instance.CardID)
		assert.Equal(t, gcid, instance.OwnerGCID)
		assert.Equal(t, tenantID, instance.TenantID)
		assert.Equal(t, 42, instance.SerialNumber)
		assert.Equal(t, AcquiredViaDirectAward, instance.AcquiredVia)
		assert.Equal(t, CardTradeStatusInCollection, instance.TradeStatus)
		assert.False(t, instance.IsFoil)

		instanceRepo.AssertExpectations(t)
		pub.AssertExpectations(t)
	})

	t.Run("success — foil card", func(t *testing.T) {
		cardRepo := new(mockTradingCardRepo)
		instanceRepo := new(mockCardInstanceRepo)
		econRepo := new(mockEconomyConfigRepo)
		pub := new(mockEventPublisher)
		svc := NewCardService(cardRepo, instanceRepo, econRepo, pub)

		cardRepo.On("GetByID", ctx, cardID).Return(card, nil)
		instanceRepo.On("NextSerialNumber", ctx, cardID).Return(1, nil)
		instanceRepo.On("Create", ctx, mock.AnythingOfType("*gamification.TradingCardInstance")).Return(nil)
		pub.On("Publish", ctx, TopicGamificationEvents, mock.Anything).Return(nil)

		instance, err := svc.AwardCard(ctx, tenantID, gcid, cardID, AcquiredViaBossReward, true)
		require.NoError(t, err)
		assert.True(t, instance.IsFoil)
		assert.Equal(t, AcquiredViaBossReward, instance.AcquiredVia)
	})

	t.Run("error — card not found", func(t *testing.T) {
		cardRepo := new(mockTradingCardRepo)
		instanceRepo := new(mockCardInstanceRepo)
		econRepo := new(mockEconomyConfigRepo)
		pub := new(mockEventPublisher)
		svc := NewCardService(cardRepo, instanceRepo, econRepo, pub)

		cardRepo.On("GetByID", ctx, cardID).Return(nil, ErrCardNotFound)

		_, err := svc.AwardCard(ctx, tenantID, gcid, cardID, AcquiredViaDirectAward, false)
		assert.ErrorIs(t, err, ErrCardNotFound)
	})
}

func TestCardService_RecycleCard(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	instanceID := uuid.New()
	cardID := uuid.New()

	tests := []struct {
		name   string
		rarity CardRarity
		wantXP int
	}{
		{"common rarity", CardRarityCommon, RecycleXPCommon},
		{"uncommon rarity", CardRarityUncommon, RecycleXPUncommon},
		{"rare rarity", CardRarityRare, RecycleXPRare},
		{"epic rarity", CardRarityEpic, RecycleXPEpic},
		{"legendary rarity", CardRarityLegendary, RecycleXPLegendary},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cardRepo := new(mockTradingCardRepo)
			instanceRepo := new(mockCardInstanceRepo)
			econRepo := new(mockEconomyConfigRepo)
			pub := new(mockEventPublisher)
			svc := NewCardService(cardRepo, instanceRepo, econRepo, pub)

			instance := &TradingCardInstance{
				ID:          instanceID,
				CardID:      cardID,
				OwnerGCID:   gcid,
				TenantID:    tenantID,
				TradeStatus: CardTradeStatusInCollection,
			}
			card := &TradingCard{
				ID:       cardID,
				CardName: "Test Card",
				Rarity:   tt.rarity,
			}

			instanceRepo.On("GetByID", ctx, instanceID).Return(instance, nil)
			cardRepo.On("GetByID", ctx, cardID).Return(card, nil)
			instanceRepo.On("Update", ctx, mock.AnythingOfType("*gamification.TradingCardInstance")).Return(nil)
			pub.On("Publish", ctx, TopicGamificationEvents, mock.Anything).Return(nil)

			result, err := svc.RecycleCard(ctx, tenantID, gcid, instanceID)
			require.NoError(t, err)
			assert.Equal(t, tt.wantXP, result.XPAwarded)
			assert.Equal(t, "Test Card", result.CardName)
		})
	}

	t.Run("error — card not in collection", func(t *testing.T) {
		cardRepo := new(mockTradingCardRepo)
		instanceRepo := new(mockCardInstanceRepo)
		econRepo := new(mockEconomyConfigRepo)
		pub := new(mockEventPublisher)
		svc := NewCardService(cardRepo, instanceRepo, econRepo, pub)

		instance := &TradingCardInstance{
			ID:          instanceID,
			OwnerGCID:   gcid,
			TenantID:    tenantID,
			TradeStatus: CardTradeStatusListedForTrade,
		}
		instanceRepo.On("GetByID", ctx, instanceID).Return(instance, nil)

		_, err := svc.RecycleCard(ctx, tenantID, gcid, instanceID)
		assert.ErrorIs(t, err, ErrCardNotRecyclable)
	})

	t.Run("error — not owner", func(t *testing.T) {
		cardRepo := new(mockTradingCardRepo)
		instanceRepo := new(mockCardInstanceRepo)
		econRepo := new(mockEconomyConfigRepo)
		pub := new(mockEventPublisher)
		svc := NewCardService(cardRepo, instanceRepo, econRepo, pub)

		otherGCID := uuid.New()
		instance := &TradingCardInstance{
			ID:          instanceID,
			OwnerGCID:   otherGCID,
			TenantID:    tenantID,
			TradeStatus: CardTradeStatusInCollection,
		}
		instanceRepo.On("GetByID", ctx, instanceID).Return(instance, nil)

		_, err := svc.RecycleCard(ctx, tenantID, gcid, instanceID)
		assert.ErrorIs(t, err, ErrCardNotOwned)
	})
}

// ---------------------------------------------------------------------------
// CardSetService Tests
// ---------------------------------------------------------------------------

func TestCardSetService_CheckSetCompletion(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	setID := uuid.New()

	t.Run("success — set completed", func(t *testing.T) {
		setRepo := new(mockCardSetRepo)
		instanceRepo := new(mockCardInstanceRepo)
		completionRepo := new(mockCardSetCompletionRepo)
		pub := new(mockEventPublisher)
		svc := NewCardSetService(setRepo, instanceRepo, completionRepo, pub)

		cardSet := &CardSet{
			ID:              setID,
			TenantID:        tenantID,
			SetName:         "Math Mastery",
			TotalCardsInSet: 5,
		}
		setRepo.On("GetByID", ctx, setID).Return(cardSet, nil)
		instanceRepo.On("CountDistinctCardsInSet", ctx, tenantID, gcid, setID).Return(5, nil)
		completionRepo.On("GetByGCIDAndSet", ctx, tenantID, gcid, setID).Return(nil, ErrSetCompletionNotFound)
		completionRepo.On("Create", ctx, mock.AnythingOfType("*gamification.CardSetCompletion")).Return(nil)
		pub.On("Publish", ctx, TopicGamificationEvents, mock.Anything).Return(nil)

		completed, err := svc.CheckSetCompletion(ctx, tenantID, gcid, setID)
		require.NoError(t, err)
		assert.True(t, completed)
		completionRepo.AssertExpectations(t)
	})

	t.Run("not completed — missing cards", func(t *testing.T) {
		setRepo := new(mockCardSetRepo)
		instanceRepo := new(mockCardInstanceRepo)
		completionRepo := new(mockCardSetCompletionRepo)
		pub := new(mockEventPublisher)
		svc := NewCardSetService(setRepo, instanceRepo, completionRepo, pub)

		cardSet := &CardSet{
			ID:              setID,
			TenantID:        tenantID,
			TotalCardsInSet: 5,
		}
		setRepo.On("GetByID", ctx, setID).Return(cardSet, nil)
		instanceRepo.On("CountDistinctCardsInSet", ctx, tenantID, gcid, setID).Return(3, nil)

		completed, err := svc.CheckSetCompletion(ctx, tenantID, gcid, setID)
		require.NoError(t, err)
		assert.False(t, completed)
	})

	t.Run("idempotent — already completed", func(t *testing.T) {
		setRepo := new(mockCardSetRepo)
		instanceRepo := new(mockCardInstanceRepo)
		completionRepo := new(mockCardSetCompletionRepo)
		pub := new(mockEventPublisher)
		svc := NewCardSetService(setRepo, instanceRepo, completionRepo, pub)

		cardSet := &CardSet{
			ID:              setID,
			TenantID:        tenantID,
			TotalCardsInSet: 5,
		}
		existing := &CardSetCompletion{
			ID:       uuid.New(),
			TenantID: tenantID,
			GCID:     gcid,
			SetID:    setID,
		}
		setRepo.On("GetByID", ctx, setID).Return(cardSet, nil)
		instanceRepo.On("CountDistinctCardsInSet", ctx, tenantID, gcid, setID).Return(5, nil)
		completionRepo.On("GetByGCIDAndSet", ctx, tenantID, gcid, setID).Return(existing, nil)

		completed, err := svc.CheckSetCompletion(ctx, tenantID, gcid, setID)
		require.NoError(t, err)
		assert.True(t, completed)
		// No Create or Publish should have been called
		completionRepo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
		pub.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything, mock.Anything)
	})
}

func TestCardSetService_GetProgress(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	setID := uuid.New()

	setRepo := new(mockCardSetRepo)
	instanceRepo := new(mockCardInstanceRepo)
	completionRepo := new(mockCardSetCompletionRepo)
	pub := new(mockEventPublisher)
	svc := NewCardSetService(setRepo, instanceRepo, completionRepo, pub)

	cardSet := &CardSet{
		ID:              setID,
		TenantID:        tenantID,
		TotalCardsInSet: 10,
	}
	setRepo.On("GetByID", ctx, setID).Return(cardSet, nil)
	instanceRepo.On("CountDistinctCardsInSet", ctx, tenantID, gcid, setID).Return(7, nil)
	completionRepo.On("GetByGCIDAndSet", ctx, tenantID, gcid, setID).Return(nil, ErrSetCompletionNotFound)

	progress, err := svc.GetProgress(ctx, tenantID, gcid, setID)
	require.NoError(t, err)
	assert.Equal(t, setID, progress.SetID)
	assert.Equal(t, 7, progress.Collected)
	assert.Equal(t, 10, progress.Total)
	assert.False(t, progress.Completed)
}

// ---------------------------------------------------------------------------
// CardTradeService Tests
// ---------------------------------------------------------------------------

func TestCardTradeService_ProposeTrade(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	offererGCID := uuid.New()
	receiverGCID := uuid.New()

	t.Run("success — trade proposed", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		offeredIDs := []uuid.UUID{uuid.New()}
		requestedIDs := []uuid.UUID{uuid.New()}

		offeredInstance := &TradingCardInstance{
			ID:          offeredIDs[0],
			OwnerGCID:   offererGCID,
			TenantID:    tenantID,
			TradeStatus: CardTradeStatusInCollection,
		}
		instanceRepo.On("GetByID", ctx, offeredIDs[0]).Return(offeredInstance, nil)
		instanceRepo.On("Update", ctx, mock.AnythingOfType("*gamification.TradingCardInstance")).Return(nil)
		tradeRepo.On("Create", ctx, mock.AnythingOfType("*gamification.CardTrade")).Return(nil)
		historyRepo.On("Create", ctx, mock.AnythingOfType("*gamification.CardTradeHistory")).Return(nil)
		pub.On("Publish", ctx, TopicGamificationEvents, mock.Anything).Return(nil)

		trade, err := svc.ProposeTrade(ctx, tenantID, offererGCID, receiverGCID, offeredIDs, requestedIDs)
		require.NoError(t, err)
		assert.Equal(t, TradeStatusProposed, trade.Status)
		assert.Equal(t, offererGCID, trade.OffererGCID)
		assert.Equal(t, receiverGCID, trade.ReceiverGCID)
	})

	t.Run("error — self trade blocked", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		_, err := svc.ProposeTrade(ctx, tenantID, offererGCID, offererGCID, []uuid.UUID{uuid.New()}, []uuid.UUID{uuid.New()})
		assert.ErrorIs(t, err, ErrSelfTradeBlocked)
	})

	t.Run("error — offered card not in collection", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		offeredID := uuid.New()
		instance := &TradingCardInstance{
			ID:          offeredID,
			OwnerGCID:   offererGCID,
			TenantID:    tenantID,
			TradeStatus: CardTradeStatusListedForTrade,
		}
		instanceRepo.On("GetByID", ctx, offeredID).Return(instance, nil)

		_, err := svc.ProposeTrade(ctx, tenantID, offererGCID, receiverGCID, []uuid.UUID{offeredID}, []uuid.UUID{uuid.New()})
		assert.ErrorIs(t, err, ErrCardNotInCollection)
	})

	t.Run("error — offered card not owned by offerer", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		offeredID := uuid.New()
		instance := &TradingCardInstance{
			ID:          offeredID,
			OwnerGCID:   uuid.New(), // different owner
			TenantID:    tenantID,
			TradeStatus: CardTradeStatusInCollection,
		}
		instanceRepo.On("GetByID", ctx, offeredID).Return(instance, nil)

		_, err := svc.ProposeTrade(ctx, tenantID, offererGCID, receiverGCID, []uuid.UUID{offeredID}, []uuid.UUID{uuid.New()})
		assert.ErrorIs(t, err, ErrCardNotOwned)
	})
}

func TestCardTradeService_AcceptTrade(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	offererGCID := uuid.New()
	receiverGCID := uuid.New()
	tradeID := uuid.New()

	t.Run("success — ownership swapped", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		offeredID := uuid.New()
		requestedID := uuid.New()

		trade := &CardTrade{
			ID:                   tradeID,
			TenantID:             tenantID,
			OffererGCID:          offererGCID,
			ReceiverGCID:         receiverGCID,
			OfferedInstanceIDs:   []uuid.UUID{offeredID},
			RequestedInstanceIDs: []uuid.UUID{requestedID},
			Status:               TradeStatusProposed,
		}

		tradeRepo.On("GetByID", ctx, tradeID).Return(trade, nil)
		instanceRepo.On("SwapOwnership", ctx, offeredID, receiverGCID).Return(nil)
		instanceRepo.On("SwapOwnership", ctx, requestedID, offererGCID).Return(nil)
		tradeRepo.On("Update", ctx, mock.AnythingOfType("*gamification.CardTrade")).Return(nil)
		historyRepo.On("Create", ctx, mock.AnythingOfType("*gamification.CardTradeHistory")).Return(nil)
		pub.On("Publish", ctx, TopicGamificationEvents, mock.Anything).Return(nil)

		result, err := svc.AcceptTrade(ctx, tenantID, receiverGCID, tradeID)
		require.NoError(t, err)
		assert.Equal(t, TradeStatusCompleted, result.Status)
	})

	t.Run("error — not receiver", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		trade := &CardTrade{
			ID:           tradeID,
			TenantID:     tenantID,
			OffererGCID:  offererGCID,
			ReceiverGCID: receiverGCID,
			Status:       TradeStatusProposed,
		}
		tradeRepo.On("GetByID", ctx, tradeID).Return(trade, nil)

		wrongGCID := uuid.New()
		_, err := svc.AcceptTrade(ctx, tenantID, wrongGCID, tradeID)
		assert.ErrorIs(t, err, ErrTradeNotReceiver)
	})

	t.Run("error — trade not proposed", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		trade := &CardTrade{
			ID:           tradeID,
			TenantID:     tenantID,
			OffererGCID:  offererGCID,
			ReceiverGCID: receiverGCID,
			Status:       TradeStatusCompleted,
		}
		tradeRepo.On("GetByID", ctx, tradeID).Return(trade, nil)

		_, err := svc.AcceptTrade(ctx, tenantID, receiverGCID, tradeID)
		assert.ErrorIs(t, err, ErrTradeNotProposed)
	})
}

func TestCardTradeService_RejectTrade(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	offererGCID := uuid.New()
	receiverGCID := uuid.New()
	tradeID := uuid.New()

	tradeRepo := new(mockCardTradeRepo)
	instanceRepo := new(mockCardInstanceRepo)
	historyRepo := new(mockCardTradeHistoryRepo)
	pub := new(mockEventPublisher)
	svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

	offeredID := uuid.New()
	trade := &CardTrade{
		ID:                 tradeID,
		TenantID:           tenantID,
		OffererGCID:        offererGCID,
		ReceiverGCID:       receiverGCID,
		OfferedInstanceIDs: []uuid.UUID{offeredID},
		Status:             TradeStatusProposed,
	}

	tradeRepo.On("GetByID", ctx, tradeID).Return(trade, nil)
	instanceRepo.On("ReturnToCollection", ctx, offeredID).Return(nil)
	tradeRepo.On("Update", ctx, mock.AnythingOfType("*gamification.CardTrade")).Return(nil)
	historyRepo.On("Create", ctx, mock.AnythingOfType("*gamification.CardTradeHistory")).Return(nil)
	pub.On("Publish", ctx, TopicGamificationEvents, mock.Anything).Return(nil)

	result, err := svc.RejectTrade(ctx, tenantID, receiverGCID, tradeID)
	require.NoError(t, err)
	assert.Equal(t, TradeStatusRejected, result.Status)
}

func TestCardTradeService_CancelTrade(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	offererGCID := uuid.New()
	receiverGCID := uuid.New()
	tradeID := uuid.New()

	t.Run("success — offerer cancels", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		offeredID := uuid.New()
		trade := &CardTrade{
			ID:                 tradeID,
			TenantID:           tenantID,
			OffererGCID:        offererGCID,
			ReceiverGCID:       receiverGCID,
			OfferedInstanceIDs: []uuid.UUID{offeredID},
			Status:             TradeStatusProposed,
		}

		tradeRepo.On("GetByID", ctx, tradeID).Return(trade, nil)
		instanceRepo.On("ReturnToCollection", ctx, offeredID).Return(nil)
		tradeRepo.On("Update", ctx, mock.AnythingOfType("*gamification.CardTrade")).Return(nil)
		historyRepo.On("Create", ctx, mock.AnythingOfType("*gamification.CardTradeHistory")).Return(nil)
		pub.On("Publish", ctx, TopicGamificationEvents, mock.Anything).Return(nil)

		result, err := svc.CancelTrade(ctx, tenantID, offererGCID, tradeID)
		require.NoError(t, err)
		assert.Equal(t, TradeStatusCancelled, result.Status)
	})

	t.Run("error — not offerer", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		trade := &CardTrade{
			ID:           tradeID,
			TenantID:     tenantID,
			OffererGCID:  offererGCID,
			ReceiverGCID: receiverGCID,
			Status:       TradeStatusProposed,
		}
		tradeRepo.On("GetByID", ctx, tradeID).Return(trade, nil)

		_, err := svc.CancelTrade(ctx, tenantID, receiverGCID, tradeID)
		assert.ErrorIs(t, err, ErrTradeNotOfferer)
	})
}

// ---------------------------------------------------------------------------
// Enum Validation Tests
// ---------------------------------------------------------------------------

func TestCardArchetype_IsValid(t *testing.T) {
	tests := []struct {
		value CardArchetype
		valid bool
	}{
		{CardArchetypeAtom, true},
		{CardArchetypeTopicNode, true},
		{CardArchetypeCompanion, true},
		{CardArchetypeInstructor, true},
		{CardArchetypeLegendary, true},
		{CardArchetype("invalid"), false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.valid, tt.value.IsValid(), "CardArchetype(%q).IsValid()", tt.value)
	}
}

func TestCardRarity_IsValid(t *testing.T) {
	tests := []struct {
		value CardRarity
		valid bool
	}{
		{CardRarityCommon, true},
		{CardRarityUncommon, true},
		{CardRarityRare, true},
		{CardRarityEpic, true},
		{CardRarityLegendary, true},
		{CardRarity("invalid"), false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.valid, tt.value.IsValid(), "CardRarity(%q).IsValid()", tt.value)
	}
}

func TestAcquiredVia_IsValid(t *testing.T) {
	tests := []struct {
		value AcquiredVia
		valid bool
	}{
		{AcquiredViaLootbox, true},
		{AcquiredViaBossReward, true},
		{AcquiredViaCoOpReward, true},
		{AcquiredViaDuelReward, true},
		{AcquiredViaDirectAward, true},
		{AcquiredViaTrade, true},
		{AcquiredVia("invalid"), false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.valid, tt.value.IsValid(), "AcquiredVia(%q).IsValid()", tt.value)
	}
}

func TestCardTradeStatus_IsValid(t *testing.T) {
	tests := []struct {
		value CardTradeStatus
		valid bool
	}{
		{CardTradeStatusInCollection, true},
		{CardTradeStatusListedForTrade, true},
		{CardTradeStatusInTransit, true},
		{CardTradeStatusTradedAway, true},
		{CardTradeStatus("invalid"), false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.valid, tt.value.IsValid(), "CardTradeStatus(%q).IsValid()", tt.value)
	}
}

func TestTradeStatus_IsValid(t *testing.T) {
	tests := []struct {
		value TradeStatus
		valid bool
	}{
		{TradeStatusProposed, true},
		{TradeStatusCompleted, true},
		{TradeStatusRejected, true},
		{TradeStatusCancelled, true},
		{TradeStatus("invalid"), false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.valid, tt.value.IsValid(), "TradeStatus(%q).IsValid()", tt.value)
	}
}
