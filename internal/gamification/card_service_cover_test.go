package gamification

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// errBoom is a synthetic repository error used to drive error-propagation
// branches that the existing happy-path tests do not reach.
var errBoom = errors.New("boom")

// ---------------------------------------------------------------------------
// CardService.AwardCard — repo error paths beyond "card not found"
// ---------------------------------------------------------------------------

func TestCardService_AwardCard_RepoErrors(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	cardID := uuid.New()
	card := &TradingCard{ID: cardID, TenantID: tenantID, CardName: "X", Rarity: CardRarityRare}

	t.Run("NextSerialNumber error propagates", func(t *testing.T) {
		cardRepo := new(mockTradingCardRepo)
		instanceRepo := new(mockCardInstanceRepo)
		econRepo := new(mockEconomyConfigRepo)
		pub := new(mockEventPublisher)
		svc := NewCardService(cardRepo, instanceRepo, econRepo, pub)

		cardRepo.On("GetByID", ctx, cardID).Return(card, nil)
		instanceRepo.On("NextSerialNumber", ctx, cardID).Return(0, errBoom)

		_, err := svc.AwardCard(ctx, tenantID, gcid, cardID, AcquiredViaDirectAward, false)
		assert.ErrorIs(t, err, errBoom)
		// Create must never be reached when serial allocation fails.
		instanceRepo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	})

	t.Run("instance Create error propagates", func(t *testing.T) {
		cardRepo := new(mockTradingCardRepo)
		instanceRepo := new(mockCardInstanceRepo)
		econRepo := new(mockEconomyConfigRepo)
		pub := new(mockEventPublisher)
		svc := NewCardService(cardRepo, instanceRepo, econRepo, pub)

		cardRepo.On("GetByID", ctx, cardID).Return(card, nil)
		instanceRepo.On("NextSerialNumber", ctx, cardID).Return(7, nil)
		instanceRepo.On("Create", ctx, mock.AnythingOfType("*gamification.TradingCardInstance")).Return(errBoom)

		_, err := svc.AwardCard(ctx, tenantID, gcid, cardID, AcquiredViaDirectAward, false)
		assert.ErrorIs(t, err, errBoom)
		// Publish is best-effort and must not be reached after a create failure.
		pub.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything, mock.Anything)
	})
}

// ---------------------------------------------------------------------------
// CardService.RecycleCard — ownership / status / repo error branches
// ---------------------------------------------------------------------------

func TestCardService_RecycleCard_GuardBranches(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	instanceID := uuid.New()
	cardID := uuid.New()

	t.Run("instance lookup error propagates", func(t *testing.T) {
		cardRepo := new(mockTradingCardRepo)
		instanceRepo := new(mockCardInstanceRepo)
		econRepo := new(mockEconomyConfigRepo)
		pub := new(mockEventPublisher)
		svc := NewCardService(cardRepo, instanceRepo, econRepo, pub)

		instanceRepo.On("GetByID", ctx, instanceID).Return(nil, errBoom)

		_, err := svc.RecycleCard(ctx, tenantID, gcid, instanceID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("not owned by caller", func(t *testing.T) {
		cardRepo := new(mockTradingCardRepo)
		instanceRepo := new(mockCardInstanceRepo)
		econRepo := new(mockEconomyConfigRepo)
		pub := new(mockEventPublisher)
		svc := NewCardService(cardRepo, instanceRepo, econRepo, pub)

		instance := &TradingCardInstance{ID: instanceID, OwnerGCID: uuid.New(), CardID: cardID, TradeStatus: CardTradeStatusInCollection}
		instanceRepo.On("GetByID", ctx, instanceID).Return(instance, nil)

		_, err := svc.RecycleCard(ctx, tenantID, gcid, instanceID)
		assert.ErrorIs(t, err, ErrCardNotOwned)
	})

	t.Run("not in collection is not recyclable", func(t *testing.T) {
		cardRepo := new(mockTradingCardRepo)
		instanceRepo := new(mockCardInstanceRepo)
		econRepo := new(mockEconomyConfigRepo)
		pub := new(mockEventPublisher)
		svc := NewCardService(cardRepo, instanceRepo, econRepo, pub)

		instance := &TradingCardInstance{ID: instanceID, OwnerGCID: gcid, CardID: cardID, TradeStatus: CardTradeStatusListedForTrade}
		instanceRepo.On("GetByID", ctx, instanceID).Return(instance, nil)

		_, err := svc.RecycleCard(ctx, tenantID, gcid, instanceID)
		assert.ErrorIs(t, err, ErrCardNotRecyclable)
	})

	t.Run("card lookup error propagates", func(t *testing.T) {
		cardRepo := new(mockTradingCardRepo)
		instanceRepo := new(mockCardInstanceRepo)
		econRepo := new(mockEconomyConfigRepo)
		pub := new(mockEventPublisher)
		svc := NewCardService(cardRepo, instanceRepo, econRepo, pub)

		instance := &TradingCardInstance{ID: instanceID, OwnerGCID: gcid, CardID: cardID, TradeStatus: CardTradeStatusInCollection}
		instanceRepo.On("GetByID", ctx, instanceID).Return(instance, nil)
		cardRepo.On("GetByID", ctx, cardID).Return(nil, errBoom)

		_, err := svc.RecycleCard(ctx, tenantID, gcid, instanceID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("instance Update error propagates", func(t *testing.T) {
		cardRepo := new(mockTradingCardRepo)
		instanceRepo := new(mockCardInstanceRepo)
		econRepo := new(mockEconomyConfigRepo)
		pub := new(mockEventPublisher)
		svc := NewCardService(cardRepo, instanceRepo, econRepo, pub)

		instance := &TradingCardInstance{ID: instanceID, OwnerGCID: gcid, CardID: cardID, TradeStatus: CardTradeStatusInCollection}
		card := &TradingCard{ID: cardID, Rarity: CardRarityEpic, CardName: "Y"}
		instanceRepo.On("GetByID", ctx, instanceID).Return(instance, nil)
		cardRepo.On("GetByID", ctx, cardID).Return(card, nil)
		instanceRepo.On("Update", ctx, mock.AnythingOfType("*gamification.TradingCardInstance")).Return(errBoom)

		_, err := svc.RecycleCard(ctx, tenantID, gcid, instanceID)
		assert.ErrorIs(t, err, errBoom)
		pub.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything, mock.Anything)
	})
}

// recycleXPForRarity default branch — an unknown rarity maps to the common
// reward (the function's documented fallback).
func TestRecycleXPForRarity_DefaultIsCommon(t *testing.T) {
	t.Parallel()
	assert.Equal(t, RecycleXPCommon, recycleXPForRarity(CardRarity("mythical-unknown")))
}

// ---------------------------------------------------------------------------
// CardSetService.CheckSetCompletion — repo error branches
// ---------------------------------------------------------------------------

func TestCardSetService_CheckSetCompletion_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	setID := uuid.New()

	t.Run("set lookup error propagates", func(t *testing.T) {
		setRepo := new(mockCardSetRepo)
		instanceRepo := new(mockCardInstanceRepo)
		completionRepo := new(mockCardSetCompletionRepo)
		pub := new(mockEventPublisher)
		svc := NewCardSetService(setRepo, instanceRepo, completionRepo, pub)

		setRepo.On("GetByID", ctx, setID).Return(nil, errBoom)

		_, err := svc.CheckSetCompletion(ctx, tenantID, gcid, setID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("count error propagates", func(t *testing.T) {
		setRepo := new(mockCardSetRepo)
		instanceRepo := new(mockCardInstanceRepo)
		completionRepo := new(mockCardSetCompletionRepo)
		pub := new(mockEventPublisher)
		svc := NewCardSetService(setRepo, instanceRepo, completionRepo, pub)

		cardSet := &CardSet{ID: setID, TotalCardsInSet: 5}
		setRepo.On("GetByID", ctx, setID).Return(cardSet, nil)
		instanceRepo.On("CountDistinctCardsInSet", ctx, tenantID, gcid, setID).Return(0, errBoom)

		_, err := svc.CheckSetCompletion(ctx, tenantID, gcid, setID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("completion lookup non-NotFound error propagates", func(t *testing.T) {
		setRepo := new(mockCardSetRepo)
		instanceRepo := new(mockCardInstanceRepo)
		completionRepo := new(mockCardSetCompletionRepo)
		pub := new(mockEventPublisher)
		svc := NewCardSetService(setRepo, instanceRepo, completionRepo, pub)

		cardSet := &CardSet{ID: setID, TotalCardsInSet: 5}
		setRepo.On("GetByID", ctx, setID).Return(cardSet, nil)
		instanceRepo.On("CountDistinctCardsInSet", ctx, tenantID, gcid, setID).Return(5, nil)
		completionRepo.On("GetByGCIDAndSet", ctx, tenantID, gcid, setID).Return(nil, errBoom)

		_, err := svc.CheckSetCompletion(ctx, tenantID, gcid, setID)
		assert.ErrorIs(t, err, errBoom)
		completionRepo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	})

	t.Run("completion Create error propagates", func(t *testing.T) {
		setRepo := new(mockCardSetRepo)
		instanceRepo := new(mockCardInstanceRepo)
		completionRepo := new(mockCardSetCompletionRepo)
		pub := new(mockEventPublisher)
		svc := NewCardSetService(setRepo, instanceRepo, completionRepo, pub)

		cardSet := &CardSet{ID: setID, TotalCardsInSet: 5}
		setRepo.On("GetByID", ctx, setID).Return(cardSet, nil)
		instanceRepo.On("CountDistinctCardsInSet", ctx, tenantID, gcid, setID).Return(5, nil)
		completionRepo.On("GetByGCIDAndSet", ctx, tenantID, gcid, setID).Return(nil, ErrSetCompletionNotFound)
		completionRepo.On("Create", ctx, mock.AnythingOfType("*gamification.CardSetCompletion")).Return(errBoom)

		_, err := svc.CheckSetCompletion(ctx, tenantID, gcid, setID)
		assert.ErrorIs(t, err, errBoom)
		pub.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything, mock.Anything)
	})
}

// ---------------------------------------------------------------------------
// CardSetService.GetProgress — error branches + completed-with-timestamp
// ---------------------------------------------------------------------------

func TestCardSetService_GetProgress_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	setID := uuid.New()

	t.Run("set lookup error propagates", func(t *testing.T) {
		setRepo := new(mockCardSetRepo)
		instanceRepo := new(mockCardInstanceRepo)
		completionRepo := new(mockCardSetCompletionRepo)
		pub := new(mockEventPublisher)
		svc := NewCardSetService(setRepo, instanceRepo, completionRepo, pub)

		setRepo.On("GetByID", ctx, setID).Return(nil, errBoom)

		_, err := svc.GetProgress(ctx, tenantID, gcid, setID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("count error propagates", func(t *testing.T) {
		setRepo := new(mockCardSetRepo)
		instanceRepo := new(mockCardInstanceRepo)
		completionRepo := new(mockCardSetCompletionRepo)
		pub := new(mockEventPublisher)
		svc := NewCardSetService(setRepo, instanceRepo, completionRepo, pub)

		cardSet := &CardSet{ID: setID, TotalCardsInSet: 5}
		setRepo.On("GetByID", ctx, setID).Return(cardSet, nil)
		instanceRepo.On("CountDistinctCardsInSet", ctx, tenantID, gcid, setID).Return(0, errBoom)

		_, err := svc.GetProgress(ctx, tenantID, gcid, setID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("completed set exposes CompletedAt", func(t *testing.T) {
		setRepo := new(mockCardSetRepo)
		instanceRepo := new(mockCardInstanceRepo)
		completionRepo := new(mockCardSetCompletionRepo)
		pub := new(mockEventPublisher)
		svc := NewCardSetService(setRepo, instanceRepo, completionRepo, pub)

		cardSet := &CardSet{ID: setID, TotalCardsInSet: 5}
		completion := &CardSetCompletion{ID: uuid.New(), SetID: setID, GCID: gcid}
		setRepo.On("GetByID", ctx, setID).Return(cardSet, nil)
		instanceRepo.On("CountDistinctCardsInSet", ctx, tenantID, gcid, setID).Return(5, nil)
		completionRepo.On("GetByGCIDAndSet", ctx, tenantID, gcid, setID).Return(completion, nil)

		progress, err := svc.GetProgress(ctx, tenantID, gcid, setID)
		require.NoError(t, err)
		assert.True(t, progress.Completed)
		require.NotNil(t, progress.CompletedAt)
		assert.Equal(t, completion.CompletedAt, *progress.CompletedAt)
	})
}

// setCompletionXPBonus — the three tier branches (10..19 mid-tier is the one
// not exercised by existing tests).
func TestSetCompletionXPBonus_Tiers(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 100, setCompletionXPBonus(5))
	assert.Equal(t, 250, setCompletionXPBonus(10))
	assert.Equal(t, 250, setCompletionXPBonus(19))
	assert.Equal(t, 500, setCompletionXPBonus(20))
}

// ---------------------------------------------------------------------------
// CardTradeService.ProposeTrade — instance lookup error + Update / Create error
// ---------------------------------------------------------------------------

func TestCardTradeService_ProposeTrade_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	offererGCID := uuid.New()
	receiverGCID := uuid.New()

	t.Run("offered instance lookup error propagates", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		offeredID := uuid.New()
		instanceRepo.On("GetByID", ctx, offeredID).Return(nil, errBoom)

		_, err := svc.ProposeTrade(ctx, tenantID, offererGCID, receiverGCID, []uuid.UUID{offeredID}, nil)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("not owned by offerer", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		offeredID := uuid.New()
		instance := &TradingCardInstance{ID: offeredID, OwnerGCID: uuid.New(), TradeStatus: CardTradeStatusInCollection}
		instanceRepo.On("GetByID", ctx, offeredID).Return(instance, nil)

		_, err := svc.ProposeTrade(ctx, tenantID, offererGCID, receiverGCID, []uuid.UUID{offeredID}, nil)
		assert.ErrorIs(t, err, ErrCardNotOwned)
	})

	t.Run("instance Update error during list-for-trade propagates", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		offeredID := uuid.New()
		instance := &TradingCardInstance{ID: offeredID, OwnerGCID: offererGCID, TradeStatus: CardTradeStatusInCollection}
		instanceRepo.On("GetByID", ctx, offeredID).Return(instance, nil)
		instanceRepo.On("Update", ctx, mock.AnythingOfType("*gamification.TradingCardInstance")).Return(errBoom)

		_, err := svc.ProposeTrade(ctx, tenantID, offererGCID, receiverGCID, []uuid.UUID{offeredID}, nil)
		assert.ErrorIs(t, err, errBoom)
		tradeRepo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	})

	t.Run("trade Create error propagates", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		offeredID := uuid.New()
		instance := &TradingCardInstance{ID: offeredID, OwnerGCID: offererGCID, TradeStatus: CardTradeStatusInCollection}
		instanceRepo.On("GetByID", ctx, offeredID).Return(instance, nil)
		instanceRepo.On("Update", ctx, mock.AnythingOfType("*gamification.TradingCardInstance")).Return(nil)
		tradeRepo.On("Create", ctx, mock.AnythingOfType("*gamification.CardTrade")).Return(errBoom)

		_, err := svc.ProposeTrade(ctx, tenantID, offererGCID, receiverGCID, []uuid.UUID{offeredID}, nil)
		assert.ErrorIs(t, err, errBoom)
		historyRepo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	})
}

// ---------------------------------------------------------------------------
// CardTradeService.AcceptTrade — guard + swap + update error branches
// ---------------------------------------------------------------------------

func TestCardTradeService_AcceptTrade_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	offererGCID := uuid.New()
	receiverGCID := uuid.New()
	tradeID := uuid.New()

	newProposed := func() *CardTrade {
		return &CardTrade{
			ID: tradeID, TenantID: tenantID,
			OffererGCID: offererGCID, ReceiverGCID: receiverGCID,
			OfferedInstanceIDs:   []uuid.UUID{uuid.New()},
			RequestedInstanceIDs: []uuid.UUID{uuid.New()},
			Status:               TradeStatusProposed,
		}
	}

	t.Run("trade lookup error propagates", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		tradeRepo.On("GetByID", ctx, tradeID).Return(nil, errBoom)

		_, err := svc.AcceptTrade(ctx, tenantID, receiverGCID, tradeID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("caller is not receiver", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		tradeRepo.On("GetByID", ctx, tradeID).Return(newProposed(), nil)

		_, err := svc.AcceptTrade(ctx, tenantID, uuid.New(), tradeID)
		assert.ErrorIs(t, err, ErrTradeNotReceiver)
	})

	t.Run("trade not in proposed state", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		tr := newProposed()
		tr.Status = TradeStatusCompleted
		tradeRepo.On("GetByID", ctx, tradeID).Return(tr, nil)

		_, err := svc.AcceptTrade(ctx, tenantID, receiverGCID, tradeID)
		assert.ErrorIs(t, err, ErrTradeNotProposed)
	})

	t.Run("offered swap error propagates", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		tr := newProposed()
		tradeRepo.On("GetByID", ctx, tradeID).Return(tr, nil)
		instanceRepo.On("SwapOwnership", ctx, tr.OfferedInstanceIDs[0], receiverGCID).Return(errBoom)

		_, err := svc.AcceptTrade(ctx, tenantID, receiverGCID, tradeID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("requested swap error propagates", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		tr := newProposed()
		tradeRepo.On("GetByID", ctx, tradeID).Return(tr, nil)
		instanceRepo.On("SwapOwnership", ctx, tr.OfferedInstanceIDs[0], receiverGCID).Return(nil)
		instanceRepo.On("SwapOwnership", ctx, tr.RequestedInstanceIDs[0], tr.OffererGCID).Return(errBoom)

		_, err := svc.AcceptTrade(ctx, tenantID, receiverGCID, tradeID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("trade Update error propagates", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		tr := newProposed()
		tradeRepo.On("GetByID", ctx, tradeID).Return(tr, nil)
		instanceRepo.On("SwapOwnership", ctx, tr.OfferedInstanceIDs[0], receiverGCID).Return(nil)
		instanceRepo.On("SwapOwnership", ctx, tr.RequestedInstanceIDs[0], tr.OffererGCID).Return(nil)
		tradeRepo.On("Update", ctx, mock.AnythingOfType("*gamification.CardTrade")).Return(errBoom)

		_, err := svc.AcceptTrade(ctx, tenantID, receiverGCID, tradeID)
		assert.ErrorIs(t, err, errBoom)
		historyRepo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	})
}

// ---------------------------------------------------------------------------
// CardTradeService.RejectTrade — guard + return-to-collection + update errors
// ---------------------------------------------------------------------------

func TestCardTradeService_RejectTrade_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	offererGCID := uuid.New()
	receiverGCID := uuid.New()
	tradeID := uuid.New()

	newProposed := func() *CardTrade {
		return &CardTrade{
			ID: tradeID, TenantID: tenantID,
			OffererGCID: offererGCID, ReceiverGCID: receiverGCID,
			OfferedInstanceIDs: []uuid.UUID{uuid.New()},
			Status:             TradeStatusProposed,
		}
	}

	t.Run("trade lookup error propagates", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		tradeRepo.On("GetByID", ctx, tradeID).Return(nil, errBoom)

		_, err := svc.RejectTrade(ctx, tenantID, receiverGCID, tradeID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("not in proposed state", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		tr := newProposed()
		tr.Status = TradeStatusRejected
		tradeRepo.On("GetByID", ctx, tradeID).Return(tr, nil)

		_, err := svc.RejectTrade(ctx, tenantID, receiverGCID, tradeID)
		assert.ErrorIs(t, err, ErrTradeNotProposed)
	})

	t.Run("return-to-collection error propagates", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		tr := newProposed()
		tradeRepo.On("GetByID", ctx, tradeID).Return(tr, nil)
		instanceRepo.On("ReturnToCollection", ctx, tr.OfferedInstanceIDs[0]).Return(errBoom)

		_, err := svc.RejectTrade(ctx, tenantID, receiverGCID, tradeID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("trade Update error propagates", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		tr := newProposed()
		tradeRepo.On("GetByID", ctx, tradeID).Return(tr, nil)
		instanceRepo.On("ReturnToCollection", ctx, tr.OfferedInstanceIDs[0]).Return(nil)
		tradeRepo.On("Update", ctx, mock.AnythingOfType("*gamification.CardTrade")).Return(errBoom)

		_, err := svc.RejectTrade(ctx, tenantID, receiverGCID, tradeID)
		assert.ErrorIs(t, err, errBoom)
		historyRepo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	})
}

// ---------------------------------------------------------------------------
// CardTradeService.CancelTrade — guard + return-to-collection + update errors
// ---------------------------------------------------------------------------

func TestCardTradeService_CancelTrade_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	offererGCID := uuid.New()
	receiverGCID := uuid.New()
	tradeID := uuid.New()

	newProposed := func() *CardTrade {
		return &CardTrade{
			ID: tradeID, TenantID: tenantID,
			OffererGCID: offererGCID, ReceiverGCID: receiverGCID,
			OfferedInstanceIDs: []uuid.UUID{uuid.New()},
			Status:             TradeStatusProposed,
		}
	}

	t.Run("trade lookup error propagates", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		tradeRepo.On("GetByID", ctx, tradeID).Return(nil, errBoom)

		_, err := svc.CancelTrade(ctx, tenantID, offererGCID, tradeID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("caller is not offerer", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		tradeRepo.On("GetByID", ctx, tradeID).Return(newProposed(), nil)

		_, err := svc.CancelTrade(ctx, tenantID, uuid.New(), tradeID)
		assert.ErrorIs(t, err, ErrTradeNotOfferer)
	})

	t.Run("not in proposed state", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		tr := newProposed()
		tr.Status = TradeStatusCancelled
		tradeRepo.On("GetByID", ctx, tradeID).Return(tr, nil)

		_, err := svc.CancelTrade(ctx, tenantID, offererGCID, tradeID)
		assert.ErrorIs(t, err, ErrTradeNotProposed)
	})

	t.Run("return-to-collection error propagates", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		tr := newProposed()
		tradeRepo.On("GetByID", ctx, tradeID).Return(tr, nil)
		instanceRepo.On("ReturnToCollection", ctx, tr.OfferedInstanceIDs[0]).Return(errBoom)

		_, err := svc.CancelTrade(ctx, tenantID, offererGCID, tradeID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("trade Update error propagates", func(t *testing.T) {
		tradeRepo := new(mockCardTradeRepo)
		instanceRepo := new(mockCardInstanceRepo)
		historyRepo := new(mockCardTradeHistoryRepo)
		pub := new(mockEventPublisher)
		svc := NewCardTradeService(tradeRepo, instanceRepo, historyRepo, pub)

		tr := newProposed()
		tradeRepo.On("GetByID", ctx, tradeID).Return(tr, nil)
		instanceRepo.On("ReturnToCollection", ctx, tr.OfferedInstanceIDs[0]).Return(nil)
		tradeRepo.On("Update", ctx, mock.AnythingOfType("*gamification.CardTrade")).Return(errBoom)

		_, err := svc.CancelTrade(ctx, tenantID, offererGCID, tradeID)
		assert.ErrorIs(t, err, errBoom)
		historyRepo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
	})
}
