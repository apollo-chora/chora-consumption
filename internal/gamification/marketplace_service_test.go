package gamification

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

type testMarketplaceDeps struct {
	svc       *MarketplaceService
	listingR  *mockMarketListingRepo
	orderR    *mockMarketOrderRepo
	txR       *mockMarketTransactionRepo
	econR     *mockEconomyConfigRepo
	publisher *mockEventPublisher
}

func newTestMarketplaceService() testMarketplaceDeps {
	lr := &mockMarketListingRepo{}
	or := &mockMarketOrderRepo{}
	tr := &mockMarketTransactionRepo{}
	er := &mockEconomyConfigRepo{}
	ep := &mockEventPublisher{}
	return testMarketplaceDeps{
		svc:       NewMarketplaceService(lr, or, tr, er, ep),
		listingR:  lr,
		orderR:    or,
		txR:       tr,
		econR:     er,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// Tests — CreateListing
// ---------------------------------------------------------------------------

func TestMarketplace_CreateListing_Success(t *testing.T) {
	t.Parallel()
	d := newTestMarketplaceService()
	ctx := context.Background()
	tenantID := uuid.New()
	sellerGCID := uuid.New()
	itemID := uuid.New()

	d.listingR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.MarketListing")).Return(nil)

	listing, err := d.svc.CreateListing(ctx, tenantID, sellerGCID, MarketItemTypeCard, itemID, 1, 500, nil)
	assert.NoError(t, err)
	assert.NotNil(t, listing)
	assert.Equal(t, tenantID, listing.TenantID)
	assert.Equal(t, sellerGCID, listing.SellerGCID)
	assert.Equal(t, MarketItemTypeCard, listing.ItemType)
	assert.Equal(t, ListingStatusActive, listing.Status)
	assert.Equal(t, 500, listing.PriceCoins)
	d.listingR.AssertExpectations(t)
}

func TestMarketplace_CreateListing_InvalidItemType(t *testing.T) {
	t.Parallel()
	d := newTestMarketplaceService()
	ctx := context.Background()
	tenantID := uuid.New()
	sellerGCID := uuid.New()
	itemID := uuid.New()

	_, err := d.svc.CreateListing(ctx, tenantID, sellerGCID, MarketItemType("invalid"), itemID, 1, 500, nil)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

// ---------------------------------------------------------------------------
// Tests — BuyListing
// ---------------------------------------------------------------------------

func TestMarketplace_BuyListing_Success(t *testing.T) {
	t.Parallel()
	d := newTestMarketplaceService()
	ctx := context.Background()
	tenantID := uuid.New()
	buyerGCID := uuid.New()
	sellerGCID := uuid.New()
	listingID := uuid.New()
	itemID := uuid.New()

	listing := &MarketListing{
		ID:         listingID,
		TenantID:   tenantID,
		SellerGCID: sellerGCID,
		ItemType:   MarketItemTypeCard,
		ItemID:     itemID,
		Quantity:   1,
		PriceCoins: 500,
		Status:     ListingStatusActive,
		ListedAt:   time.Now().UTC(),
		ExpiresAt:  time.Now().Add(24 * time.Hour),
		CreatedAt:  time.Now().UTC(),
	}

	feeConfig := &EconomyConfig{
		ID:             uuid.New(),
		ConfigCategory: ConfigCategoryStoreLimits,
		ConfigKey:      "marketplace_fee_pct",
		ConfigValue:    map[string]interface{}{"fee_pct": float64(5)},
	}

	d.listingR.On("GetByID", mock.Anything, listingID).Return(listing, nil)
	d.econR.On("GetByTenantCategoryKey", mock.Anything, (*uuid.UUID)(nil), ConfigCategoryStoreLimits, "marketplace_fee_pct").
		Return(feeConfig, nil)
	d.listingR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.MarketListing")).Return(nil)
	d.orderR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.MarketOrder")).Return(nil)
	d.txR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.MarketTransaction")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	order, err := d.svc.BuyListing(ctx, tenantID, listingID, buyerGCID, PaymentMethodCoins)
	assert.NoError(t, err)
	assert.NotNil(t, order)
	assert.Equal(t, OrderStatusCompleted, order.Status)
	assert.Equal(t, buyerGCID, order.BuyerGCID)
	d.listingR.AssertExpectations(t)
	d.orderR.AssertExpectations(t)
	d.txR.AssertExpectations(t)
}

func TestMarketplace_BuyListing_AlreadySold(t *testing.T) {
	t.Parallel()
	d := newTestMarketplaceService()
	ctx := context.Background()
	tenantID := uuid.New()
	buyerGCID := uuid.New()
	listingID := uuid.New()

	listing := &MarketListing{
		ID:       listingID,
		TenantID: tenantID,
		Status:   ListingStatusSold,
	}

	d.listingR.On("GetByID", mock.Anything, listingID).Return(listing, nil)

	_, err := d.svc.BuyListing(ctx, tenantID, listingID, buyerGCID, PaymentMethodCoins)
	assert.ErrorIs(t, err, ErrListingNotAvailable)
}

func TestMarketplace_BuyListing_OwnListing(t *testing.T) {
	t.Parallel()
	d := newTestMarketplaceService()
	ctx := context.Background()
	tenantID := uuid.New()
	sellerGCID := uuid.New()
	listingID := uuid.New()

	listing := &MarketListing{
		ID:         listingID,
		TenantID:   tenantID,
		SellerGCID: sellerGCID,
		Status:     ListingStatusActive,
		ExpiresAt:  time.Now().Add(24 * time.Hour),
	}

	d.listingR.On("GetByID", mock.Anything, listingID).Return(listing, nil)

	_, err := d.svc.BuyListing(ctx, tenantID, listingID, sellerGCID, PaymentMethodCoins)
	assert.ErrorIs(t, err, ErrCannotBuyOwnListing)
}

// ---------------------------------------------------------------------------
// Tests — CancelListing
// ---------------------------------------------------------------------------

func TestMarketplace_CancelListing_Success(t *testing.T) {
	t.Parallel()
	d := newTestMarketplaceService()
	ctx := context.Background()
	sellerGCID := uuid.New()
	listingID := uuid.New()

	listing := &MarketListing{
		ID:         listingID,
		TenantID:   uuid.New(),
		SellerGCID: sellerGCID,
		Status:     ListingStatusActive,
	}

	d.listingR.On("GetByID", mock.Anything, listingID).Return(listing, nil)
	d.listingR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.MarketListing")).Return(nil)

	err := d.svc.CancelListing(ctx, listingID, sellerGCID)
	assert.NoError(t, err)
	d.listingR.AssertExpectations(t)
}

func TestMarketplace_CancelListing_NotOwner(t *testing.T) {
	t.Parallel()
	d := newTestMarketplaceService()
	ctx := context.Background()
	sellerGCID := uuid.New()
	otherGCID := uuid.New()
	listingID := uuid.New()

	listing := &MarketListing{
		ID:         listingID,
		TenantID:   uuid.New(),
		SellerGCID: sellerGCID,
		Status:     ListingStatusActive,
	}

	d.listingR.On("GetByID", mock.Anything, listingID).Return(listing, nil)

	err := d.svc.CancelListing(ctx, listingID, otherGCID)
	assert.ErrorIs(t, err, ErrListingNotOwner)
}

// ---------------------------------------------------------------------------
// Tests — ExpireListings
// ---------------------------------------------------------------------------

func TestMarketplace_ExpireListings_BatchSuccess(t *testing.T) {
	t.Parallel()
	d := newTestMarketplaceService()
	ctx := context.Background()
	tenantID := uuid.New()

	expired := []*MarketListing{
		{ID: uuid.New(), TenantID: tenantID, Status: ListingStatusActive, ExpiresAt: time.Now().Add(-1 * time.Hour)},
		{ID: uuid.New(), TenantID: tenantID, Status: ListingStatusActive, ExpiresAt: time.Now().Add(-2 * time.Hour)},
	}

	d.listingR.On("ListExpired", mock.Anything, tenantID, mock.AnythingOfType("time.Time")).
		Return(expired, nil)
	d.listingR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.MarketListing")).Return(nil).Times(2)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil).Times(2)

	count, err := d.svc.ExpireListings(ctx, tenantID)
	assert.NoError(t, err)
	assert.Equal(t, 2, count)
	d.listingR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// Tests — ListActive
// ---------------------------------------------------------------------------

func TestMarketplace_ListActive_Success(t *testing.T) {
	t.Parallel()
	d := newTestMarketplaceService()
	ctx := context.Background()
	tenantID := uuid.New()

	listings := []*MarketListing{
		{ID: uuid.New(), TenantID: tenantID, Status: ListingStatusActive},
	}

	d.listingR.On("ListActive", mock.Anything, tenantID, 0, 20).
		Return(listings, nil)

	got, err := d.svc.ListActive(ctx, tenantID, 0, 20)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
}

// ---------------------------------------------------------------------------
// Tests — Enum IsValid
// ---------------------------------------------------------------------------

func TestMarketItemType_IsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, MarketItemTypeCard.IsValid())
	assert.True(t, MarketItemTypeMaterial.IsValid())
	assert.True(t, MarketItemTypeLootbox.IsValid())
	assert.True(t, MarketItemTypeSkin.IsValid())
	assert.False(t, MarketItemType("invalid").IsValid())
}

func TestListingStatus_IsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, ListingStatusActive.IsValid())
	assert.True(t, ListingStatusSold.IsValid())
	assert.True(t, ListingStatusExpired.IsValid())
	assert.True(t, ListingStatusCancelled.IsValid())
	assert.False(t, ListingStatus("invalid").IsValid())
}

func TestOrderStatus_IsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, OrderStatusPending.IsValid())
	assert.True(t, OrderStatusCompleted.IsValid())
	assert.True(t, OrderStatusFailed.IsValid())
	assert.True(t, OrderStatusRefunded.IsValid())
	assert.False(t, OrderStatus("invalid").IsValid())
}

func TestPaymentMethod_IsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, PaymentMethodCoins.IsValid())
	assert.True(t, PaymentMethodStars.IsValid())
	assert.False(t, PaymentMethod("invalid").IsValid())
}
