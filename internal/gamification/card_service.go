package gamification

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// AwardCard creates a new TradingCardInstance for a learner.
// Serial number is atomically incremented via the repository.
func (s *CardService) AwardCard(
	ctx context.Context,
	tenantID, gcid, cardID uuid.UUID,
	acquiredVia AcquiredVia,
	isFoil bool,
) (*TradingCardInstance, error) {
	card, err := s.cardRepo.GetByID(ctx, cardID)
	if err != nil {
		return nil, err
	}

	serial, err := s.instanceRepo.NextSerialNumber(ctx, cardID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	instance := &TradingCardInstance{
		ID:           uuid.Must(uuid.NewV7()),
		CardID:       cardID,
		OwnerGCID:    gcid,
		TenantID:     tenantID,
		SerialNumber: serial,
		AcquiredVia:  acquiredVia,
		AcquiredAt:   now,
		IsFoil:       isFoil,
		TradeStatus:  CardTradeStatusInCollection,
		CreatedAt:    now,
	}

	if err := s.instanceRepo.Create(ctx, instance); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventTradingCardEarned,
		tenantID,
		&gcid,
		instance.ID,
		AggregateTradingCardInstance,
		map[string]interface{}{
			"instance_id":   instance.ID.String(),
			"card_id":       cardID.String(),
			"owner_gcid":    gcid.String(),
			"serial_number": serial,
			"acquired_via":  string(acquiredVia),
			"rarity":        string(card.Rarity),
			"card_name":     card.CardName,
			"is_foil":       isFoil,
		},
	)
	_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)

	return instance, nil
}

// RecycleCard destroys a card instance and returns XP based on rarity.
func (s *CardService) RecycleCard(
	ctx context.Context,
	tenantID, gcid, instanceID uuid.UUID,
) (*RecycleResult, error) {
	instance, err := s.instanceRepo.GetByID(ctx, instanceID)
	if err != nil {
		return nil, err
	}

	if instance.OwnerGCID != gcid {
		return nil, ErrCardNotOwned
	}
	if instance.TradeStatus != CardTradeStatusInCollection {
		return nil, ErrCardNotRecyclable
	}

	card, err := s.cardRepo.GetByID(ctx, instance.CardID)
	if err != nil {
		return nil, err
	}

	xp := recycleXPForRarity(card.Rarity)

	instance.TradeStatus = CardTradeStatusTradedAway
	now := time.Now().UTC()
	instance.DeletedAt = &now

	if err := s.instanceRepo.Update(ctx, instance); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventTradingCardRecycled,
		tenantID,
		&gcid,
		instanceID,
		AggregateTradingCardInstance,
		map[string]interface{}{
			"instance_id": instanceID.String(),
			"card_id":     card.ID.String(),
			"owner_gcid":  gcid.String(),
			"rarity":      string(card.Rarity),
			"xp_awarded":  xp,
		},
	)
	_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)

	return &RecycleResult{XPAwarded: xp, CardName: card.CardName}, nil
}

func recycleXPForRarity(rarity CardRarity) int {
	switch rarity {
	case CardRarityCommon:
		return RecycleXPCommon
	case CardRarityUncommon:
		return RecycleXPUncommon
	case CardRarityRare:
		return RecycleXPRare
	case CardRarityEpic:
		return RecycleXPEpic
	case CardRarityLegendary:
		return RecycleXPLegendary
	default:
		return RecycleXPCommon
	}
}

// ---------------------------------------------------------------------------
// CardSetService
// ---------------------------------------------------------------------------

// CheckSetCompletion checks if a learner has completed a card set.
// Returns true if set is complete. Idempotent — won't re-award.
func (s *CardSetService) CheckSetCompletion(
	ctx context.Context,
	tenantID, gcid, setID uuid.UUID,
) (bool, error) {
	cardSet, err := s.setRepo.GetByID(ctx, setID)
	if err != nil {
		return false, err
	}

	collected, err := s.instanceRepo.CountDistinctCardsInSet(ctx, tenantID, gcid, setID)
	if err != nil {
		return false, err
	}

	if collected < cardSet.TotalCardsInSet {
		return false, nil
	}

	// Check for existing completion (idempotent)
	existing, err := s.completionRepo.GetByGCIDAndSet(ctx, tenantID, gcid, setID)
	if err != nil && !errors.Is(err, ErrSetCompletionNotFound) {
		return false, err
	}
	if existing != nil {
		return true, nil
	}

	xpBonus := setCompletionXPBonus(cardSet.TotalCardsInSet)
	completion := &CardSetCompletion{
		ID:          uuid.Must(uuid.NewV7()),
		TenantID:    tenantID,
		GCID:        gcid,
		SetID:       setID,
		CompletedAt: time.Now().UTC(),
		XPBonus:     xpBonus,
	}

	if err := s.completionRepo.Create(ctx, completion); err != nil {
		return false, err
	}

	event := NewDomainEvent(
		EventCardSetCompleted,
		tenantID,
		&gcid,
		setID,
		AggregateCardSet,
		map[string]interface{}{
			"set_id":      setID.String(),
			"gcid":        gcid.String(),
			"set_name":    cardSet.SetName,
			"total_cards": cardSet.TotalCardsInSet,
			"xp_bonus":    xpBonus,
		},
	)
	_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)

	return true, nil
}

// GetProgress returns the learner's progress toward completing a set.
func (s *CardSetService) GetProgress(
	ctx context.Context,
	tenantID, gcid, setID uuid.UUID,
) (*CardSetProgress, error) {
	cardSet, err := s.setRepo.GetByID(ctx, setID)
	if err != nil {
		return nil, err
	}

	collected, err := s.instanceRepo.CountDistinctCardsInSet(ctx, tenantID, gcid, setID)
	if err != nil {
		return nil, err
	}

	progress := &CardSetProgress{
		SetID:     setID,
		Collected: collected,
		Total:     cardSet.TotalCardsInSet,
		Completed: collected >= cardSet.TotalCardsInSet,
	}

	completion, err := s.completionRepo.GetByGCIDAndSet(ctx, tenantID, gcid, setID)
	if err == nil && completion != nil {
		progress.CompletedAt = &completion.CompletedAt
	}

	return progress, nil
}

func setCompletionXPBonus(totalCards int) int {
	if totalCards >= 20 {
		return 500
	}
	if totalCards >= 10 {
		return 250
	}
	return 100
}

// ---------------------------------------------------------------------------
// CardTradeService
// ---------------------------------------------------------------------------

// ProposeTrade creates a new trade proposal.
func (s *CardTradeService) ProposeTrade(
	ctx context.Context,
	tenantID, offererGCID, receiverGCID uuid.UUID,
	offeredIDs, requestedIDs []uuid.UUID,
) (*CardTrade, error) {
	if offererGCID == receiverGCID {
		return nil, ErrSelfTradeBlocked
	}

	// Validate all offered instances
	for _, id := range offeredIDs {
		instance, err := s.instanceRepo.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if instance.OwnerGCID != offererGCID {
			return nil, ErrCardNotOwned
		}
		if instance.TradeStatus != CardTradeStatusInCollection {
			return nil, ErrCardNotInCollection
		}
	}

	// Mark offered cards as listed
	for _, id := range offeredIDs {
		instance, _ := s.instanceRepo.GetByID(ctx, id)
		instance.TradeStatus = CardTradeStatusListedForTrade
		if err := s.instanceRepo.Update(ctx, instance); err != nil {
			return nil, err
		}
	}

	now := time.Now().UTC()
	trade := &CardTrade{
		ID:                   uuid.Must(uuid.NewV7()),
		TenantID:             tenantID,
		OffererGCID:          offererGCID,
		ReceiverGCID:         receiverGCID,
		OfferedInstanceIDs:   offeredIDs,
		RequestedInstanceIDs: requestedIDs,
		Status:               TradeStatusProposed,
		CreatedAt:            now,
	}

	if err := s.tradeRepo.Create(ctx, trade); err != nil {
		return nil, err
	}

	history := &CardTradeHistory{
		ID:             uuid.Must(uuid.NewV7()),
		TradeID:        trade.ID,
		PreviousStatus: TradeStatusProposed,
		NewStatus:      TradeStatusProposed,
		ChangedByGCID:  offererGCID,
		CreatedAt:      now,
	}
	_ = s.historyRepo.Create(ctx, history)

	event := NewDomainEvent(
		EventCardTradeProposed,
		tenantID,
		&offererGCID,
		trade.ID,
		AggregateCardTrade,
		map[string]interface{}{
			"trade_id":        trade.ID.String(),
			"offerer_gcid":    offererGCID.String(),
			"receiver_gcid":   receiverGCID.String(),
			"offered_count":   len(offeredIDs),
			"requested_count": len(requestedIDs),
		},
	)
	_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)

	return trade, nil
}

// AcceptTrade accepts a trade and swaps card ownership atomically.
func (s *CardTradeService) AcceptTrade(
	ctx context.Context,
	tenantID, receiverGCID, tradeID uuid.UUID,
) (*CardTrade, error) {
	trade, err := s.tradeRepo.GetByID(ctx, tradeID)
	if err != nil {
		return nil, err
	}
	if trade.ReceiverGCID != receiverGCID {
		return nil, ErrTradeNotReceiver
	}
	if trade.Status != TradeStatusProposed {
		return nil, ErrTradeNotProposed
	}

	// Swap offered → receiver
	for _, id := range trade.OfferedInstanceIDs {
		if err := s.instanceRepo.SwapOwnership(ctx, id, receiverGCID); err != nil {
			return nil, err
		}
	}
	// Swap requested → offerer
	for _, id := range trade.RequestedInstanceIDs {
		if err := s.instanceRepo.SwapOwnership(ctx, id, trade.OffererGCID); err != nil {
			return nil, err
		}
	}

	now := time.Now().UTC()
	trade.Status = TradeStatusCompleted
	trade.CompletedAt = &now

	if err := s.tradeRepo.Update(ctx, trade); err != nil {
		return nil, err
	}

	history := &CardTradeHistory{
		ID:             uuid.Must(uuid.NewV7()),
		TradeID:        tradeID,
		PreviousStatus: TradeStatusProposed,
		NewStatus:      TradeStatusCompleted,
		ChangedByGCID:  receiverGCID,
		CreatedAt:      now,
	}
	_ = s.historyRepo.Create(ctx, history)

	event := NewDomainEvent(
		EventCardTradeCompleted,
		tenantID,
		&receiverGCID,
		tradeID,
		AggregateCardTrade,
		map[string]interface{}{
			"trade_id":          tradeID.String(),
			"offerer_gcid":      trade.OffererGCID.String(),
			"receiver_gcid":     receiverGCID.String(),
			"instances_swapped": len(trade.OfferedInstanceIDs) + len(trade.RequestedInstanceIDs),
		},
	)
	_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)

	return trade, nil
}

// RejectTrade rejects a trade proposal and returns offered cards to collection.
func (s *CardTradeService) RejectTrade(
	ctx context.Context,
	tenantID, receiverGCID, tradeID uuid.UUID,
) (*CardTrade, error) {
	trade, err := s.tradeRepo.GetByID(ctx, tradeID)
	if err != nil {
		return nil, err
	}
	if trade.Status != TradeStatusProposed {
		return nil, ErrTradeNotProposed
	}

	for _, id := range trade.OfferedInstanceIDs {
		if err := s.instanceRepo.ReturnToCollection(ctx, id); err != nil {
			return nil, err
		}
	}

	trade.Status = TradeStatusRejected
	if err := s.tradeRepo.Update(ctx, trade); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	history := &CardTradeHistory{
		ID:             uuid.Must(uuid.NewV7()),
		TradeID:        tradeID,
		PreviousStatus: TradeStatusProposed,
		NewStatus:      TradeStatusRejected,
		ChangedByGCID:  receiverGCID,
		CreatedAt:      now,
	}
	_ = s.historyRepo.Create(ctx, history)

	event := NewDomainEvent(
		EventCardTradeRejected,
		tenantID,
		&receiverGCID,
		tradeID,
		AggregateCardTrade,
		map[string]interface{}{
			"trade_id":      tradeID.String(),
			"offerer_gcid":  trade.OffererGCID.String(),
			"receiver_gcid": receiverGCID.String(),
		},
	)
	_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)

	return trade, nil
}

// CancelTrade cancels a trade proposal (only offerer can cancel).
func (s *CardTradeService) CancelTrade(
	ctx context.Context,
	tenantID, offererGCID, tradeID uuid.UUID,
) (*CardTrade, error) {
	trade, err := s.tradeRepo.GetByID(ctx, tradeID)
	if err != nil {
		return nil, err
	}
	if trade.OffererGCID != offererGCID {
		return nil, ErrTradeNotOfferer
	}
	if trade.Status != TradeStatusProposed {
		return nil, ErrTradeNotProposed
	}

	for _, id := range trade.OfferedInstanceIDs {
		if err := s.instanceRepo.ReturnToCollection(ctx, id); err != nil {
			return nil, err
		}
	}

	trade.Status = TradeStatusCancelled
	if err := s.tradeRepo.Update(ctx, trade); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	history := &CardTradeHistory{
		ID:             uuid.Must(uuid.NewV7()),
		TradeID:        tradeID,
		PreviousStatus: TradeStatusProposed,
		NewStatus:      TradeStatusCancelled,
		ChangedByGCID:  offererGCID,
		CreatedAt:      now,
	}
	_ = s.historyRepo.Create(ctx, history)

	event := NewDomainEvent(
		EventCardTradeCancelled,
		tenantID,
		&offererGCID,
		tradeID,
		AggregateCardTrade,
		map[string]interface{}{
			"trade_id":      tradeID.String(),
			"offerer_gcid":  trade.OffererGCID.String(),
			"receiver_gcid": trade.ReceiverGCID.String(),
		},
	)
	_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)

	return trade, nil
}
