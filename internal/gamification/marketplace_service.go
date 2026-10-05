package gamification

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// DefaultMarketplaceFeePct is the default marketplace fee percentage.
const DefaultMarketplaceFeePct = 5

// DefaultListingDuration is the default listing expiry duration.
const DefaultListingDuration = 7 * 24 * time.Hour

// NewMarketplaceService creates a new MarketplaceService.
func NewMarketplaceService(
	listingRepo MarketListingRepository,
	orderRepo MarketOrderRepository,
	txRepo MarketTransactionRepository,
	econRepo EconomyConfigRepository,
	publisher EventPublisher,
) *MarketplaceService {
	return &MarketplaceService{
		listingRepo: listingRepo,
		orderRepo:   orderRepo,
		txRepo:      txRepo,
		econRepo:    econRepo,
		publisher:   publisher,
	}
}

// MarketplaceService handles marketplace listing, buying, cancelling, and expiry.
type MarketplaceService struct {
	listingRepo MarketListingRepository
	orderRepo   MarketOrderRepository
	txRepo      MarketTransactionRepository
	econRepo    EconomyConfigRepository
	publisher   EventPublisher
}

// CreateListing creates a new marketplace listing.
func (s *MarketplaceService) CreateListing(
	ctx context.Context,
	tenantID, sellerGCID uuid.UUID,
	itemType MarketItemType,
	itemID uuid.UUID,
	quantity, priceCoins int,
	priceStars *int,
) (*MarketListing, error) {
	if !itemType.IsValid() {
		return nil, ErrValidationFailed
	}

	now := time.Now().UTC()
	listing := &MarketListing{
		ID:         uuid.Must(uuid.NewV7()),
		TenantID:   tenantID,
		SellerGCID: sellerGCID,
		ItemType:   itemType,
		ItemID:     itemID,
		Quantity:   quantity,
		PriceCoins: priceCoins,
		PriceStars: priceStars,
		Status:     ListingStatusActive,
		ListedAt:   now,
		ExpiresAt:  now.Add(DefaultListingDuration),
		CreatedAt:  now,
	}

	if err := s.listingRepo.Create(ctx, listing); err != nil {
		return nil, err
	}

	return listing, nil
}

// BuyListing processes a purchase of a marketplace listing.
func (s *MarketplaceService) BuyListing(
	ctx context.Context,
	tenantID, listingID, buyerGCID uuid.UUID,
	paymentMethod PaymentMethod,
) (*MarketOrder, error) {
	listing, err := s.listingRepo.GetByID(ctx, listingID)
	if err != nil {
		return nil, err
	}

	if listing.Status != ListingStatusActive {
		return nil, ErrListingNotAvailable
	}

	if listing.SellerGCID == buyerGCID {
		return nil, ErrCannotBuyOwnListing
	}

	if time.Now().UTC().After(listing.ExpiresAt) {
		return nil, ErrListingNotAvailable
	}

	// Determine fee
	feePct := DefaultMarketplaceFeePct
	feeConfig, err := s.econRepo.GetByTenantCategoryKey(ctx, nil, ConfigCategoryStoreLimits, "marketplace_fee_pct")
	if err == nil && feeConfig != nil {
		if v, ok := feeConfig.ConfigValue["fee_pct"].(float64); ok {
			feePct = int(v)
		}
	}

	var price int
	switch paymentMethod {
	case PaymentMethodStars:
		if listing.PriceStars != nil {
			price = *listing.PriceStars
		} else {
			return nil, ErrValidationFailed
		}
	default:
		price = listing.PriceCoins
	}

	feeAmount := price * feePct / 100
	netAmount := price - feeAmount

	now := time.Now().UTC()

	// Create order
	order := &MarketOrder{
		ID:            uuid.Must(uuid.NewV7()),
		TenantID:      tenantID,
		ListingID:     listingID,
		BuyerGCID:     buyerGCID,
		PaymentMethod: paymentMethod,
		AmountPaid:    price,
		Status:        OrderStatusCompleted,
		CreatedAt:     now,
		CompletedAt:   &now,
	}

	if err := s.orderRepo.Create(ctx, order); err != nil {
		return nil, err
	}

	// Update listing status
	listing.Status = ListingStatusSold
	listing.SoldAt = &now
	listing.BuyerGCID = &buyerGCID

	if err := s.listingRepo.Update(ctx, listing); err != nil {
		return nil, err
	}

	// Create immutable transaction record
	tx := &MarketTransaction{
		ID:         uuid.Must(uuid.NewV7()),
		TenantID:   tenantID,
		OrderID:    order.ID,
		SellerGCID: listing.SellerGCID,
		BuyerGCID:  buyerGCID,
		ItemType:   string(listing.ItemType),
		ItemID:     listing.ItemID,
		Price:      price,
		FeeAmount:  feeAmount,
		NetAmount:  netAmount,
		CreatedAt:  now,
	}

	if err := s.txRepo.Create(ctx, tx); err != nil {
		return nil, err
	}

	// Publish event
	event := NewDomainEvent(
		EventMarketListingSold,
		tenantID,
		&buyerGCID,
		listing.ID,
		AggregateMarketListing,
		map[string]interface{}{
			"listing_id":  listing.ID.String(),
			"buyer_gcid":  buyerGCID.String(),
			"seller_gcid": listing.SellerGCID.String(),
			"price":       price,
			"fee_amount":  feeAmount,
			"net_amount":  netAmount,
		},
	)
	_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)

	return order, nil
}

// CancelListing cancels an active listing. Only the seller can cancel.
func (s *MarketplaceService) CancelListing(
	ctx context.Context,
	listingID, requesterGCID uuid.UUID,
) error {
	listing, err := s.listingRepo.GetByID(ctx, listingID)
	if err != nil {
		return err
	}

	if listing.SellerGCID != requesterGCID {
		return ErrListingNotOwner
	}

	if listing.Status != ListingStatusActive {
		return ErrListingNotAvailable
	}

	listing.Status = ListingStatusCancelled
	return s.listingRepo.Update(ctx, listing)
}

// ExpireListings marks all expired active listings and returns count.
func (s *MarketplaceService) ExpireListings(
	ctx context.Context,
	tenantID uuid.UUID,
) (int, error) {
	now := time.Now().UTC()

	expired, err := s.listingRepo.ListExpired(ctx, tenantID, now)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, listing := range expired {
		listing.Status = ListingStatusExpired
		if err := s.listingRepo.Update(ctx, listing); err != nil {
			continue
		}

		event := NewDomainEvent(
			EventMarketListingExpired,
			tenantID,
			nil,
			listing.ID,
			AggregateMarketListing,
			map[string]interface{}{
				"listing_id":  listing.ID.String(),
				"seller_gcid": listing.SellerGCID.String(),
				"item_type":   string(listing.ItemType),
			},
		)
		_ = s.publisher.Publish(ctx, TopicGamificationEvents, event)

		count++
	}

	return count, nil
}

// ListActive returns active marketplace listings for a tenant.
func (s *MarketplaceService) ListActive(
	ctx context.Context,
	tenantID uuid.UUID,
	offset, limit int,
) ([]*MarketListing, error) {
	return s.listingRepo.ListActive(ctx, tenantID, offset, limit)
}
