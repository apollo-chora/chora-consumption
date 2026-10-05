package gamification

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// GetBalance returns the coin balance for a learner.
func (s *CoinService) GetBalance(ctx context.Context, tenantID, gcid uuid.UUID) (*CoinAccount, error) {
	account, err := s.repo.GetByGCID(ctx, tenantID, gcid)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrNotFound
	}
	return account, nil
}

// EarnCoins adds coins to a learner's account.
func (s *CoinService) EarnCoins(ctx context.Context, tenantID, gcid uuid.UUID, amount int64, reason string, refID *uuid.UUID, refType *string) (*CoinAccount, error) {
	if amount <= 0 {
		return nil, ErrValidationFailed
	}

	account, err := s.repo.GetByGCID(ctx, tenantID, gcid)
	if err != nil {
		return nil, err
	}
	if account == nil {
		account = &CoinAccount{
			ID:        uuid.Must(uuid.NewV7()),
			TenantID:  tenantID,
			GCID:      gcid,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		if err := s.repo.CreateIfNotExists(ctx, account); err != nil {
			return nil, err
		}
	}

	account.Balance += amount
	account.LifetimeEarned += amount
	account.UpdatedAt = time.Now().UTC()

	if err := s.repo.UpdateBalance(ctx, account); err != nil {
		return nil, err
	}

	tx := &CoinTransaction{
		ID:              uuid.Must(uuid.NewV7()),
		TenantID:        tenantID,
		CoinAccountID:   account.ID,
		Amount:          amount,
		TransactionType: CoinTransactionTypeEarned,
		Reason:          reason,
		ReferenceID:     refID,
		ReferenceType:   refType,
		CreatedAt:       time.Now().UTC(),
	}
	if err := s.repo.CreateTransaction(ctx, tx); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventCoinEarned,
		tenantID,
		&gcid,
		account.ID,
		AggregateCoinAccount,
		map[string]interface{}{
			"amount": amount,
			"reason": reason,
		},
	)
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, event); err != nil {
		return nil, err
	}

	return account, nil
}

// SpendCoins deducts coins from a learner's account.
func (s *CoinService) SpendCoins(ctx context.Context, tenantID, gcid uuid.UUID, amount int64, reason string, refID *uuid.UUID, refType *string) (*CoinAccount, error) {
	if amount <= 0 {
		return nil, ErrValidationFailed
	}

	account, err := s.repo.GetByGCID(ctx, tenantID, gcid)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrNotFound
	}
	if account.Balance < amount {
		return nil, ErrInsufficientBalance
	}

	account.Balance -= amount
	account.LifetimeSpent += amount
	account.UpdatedAt = time.Now().UTC()

	if err := s.repo.UpdateBalance(ctx, account); err != nil {
		return nil, err
	}

	tx := &CoinTransaction{
		ID:              uuid.Must(uuid.NewV7()),
		TenantID:        tenantID,
		CoinAccountID:   account.ID,
		Amount:          -amount,
		TransactionType: CoinTransactionTypeSpent,
		Reason:          reason,
		ReferenceID:     refID,
		ReferenceType:   refType,
		CreatedAt:       time.Now().UTC(),
	}
	if err := s.repo.CreateTransaction(ctx, tx); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventCoinSpent,
		tenantID,
		&gcid,
		account.ID,
		AggregateCoinAccount,
		map[string]interface{}{
			"amount": amount,
			"reason": reason,
		},
	)
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, event); err != nil {
		return nil, err
	}

	return account, nil
}

// RefundCoins returns previously spent coins.
func (s *CoinService) RefundCoins(ctx context.Context, tenantID, gcid uuid.UUID, amount int64, reason string, refID *uuid.UUID, refType *string) (*CoinAccount, error) {
	if amount <= 0 {
		return nil, ErrValidationFailed
	}

	account, err := s.repo.GetByGCID(ctx, tenantID, gcid)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrNotFound
	}

	account.Balance += amount
	account.UpdatedAt = time.Now().UTC()

	if err := s.repo.UpdateBalance(ctx, account); err != nil {
		return nil, err
	}

	tx := &CoinTransaction{
		ID:              uuid.Must(uuid.NewV7()),
		TenantID:        tenantID,
		CoinAccountID:   account.ID,
		Amount:          amount,
		TransactionType: CoinTransactionTypeRefunded,
		Reason:          reason,
		ReferenceID:     refID,
		ReferenceType:   refType,
		CreatedAt:       time.Now().UTC(),
	}
	if err := s.repo.CreateTransaction(ctx, tx); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventCoinEarned,
		tenantID,
		&gcid,
		account.ID,
		AggregateCoinAccount,
		map[string]interface{}{
			"amount": amount,
			"reason": reason,
		},
	)
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, event); err != nil {
		return nil, err
	}

	return account, nil
}

// ListTransactions returns coin transaction history.
func (s *CoinService) ListTransactions(ctx context.Context, tenantID, gcid uuid.UUID, offset, limit int) ([]*CoinTransaction, error) {
	account, err := s.repo.GetByGCID(ctx, tenantID, gcid)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrNotFound
	}
	return s.repo.ListTransactions(ctx, tenantID, account.ID, offset, limit)
}
