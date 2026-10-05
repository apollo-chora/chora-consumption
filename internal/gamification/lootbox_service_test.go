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

type testLootboxDeps struct {
	svc       *LootboxService
	lootR     *mockLootboxRepo
	tableR    *mockLootTableRepo
	publisher *mockEventPublisher
}

func newTestLootboxService() testLootboxDeps {
	lr := &mockLootboxRepo{}
	tr := &mockLootTableRepo{}
	ep := &mockEventPublisher{}
	return testLootboxDeps{
		svc:       NewLootboxService(lr, tr, ep),
		lootR:     lr,
		tableR:    tr,
		publisher: ep,
	}
}

type testLoginCalendarDeps struct {
	svc       *LoginCalendarService
	calR      *mockLoginCalendarRepo
	lootSvc   *LootboxService
	lootR     *mockLootboxRepo
	tableR    *mockLootTableRepo
	publisher *mockEventPublisher
}

func newTestLoginCalendarService() testLoginCalendarDeps {
	cr := &mockLoginCalendarRepo{}
	lr := &mockLootboxRepo{}
	tr := &mockLootTableRepo{}
	ep := &mockEventPublisher{}
	lootSvc := NewLootboxService(lr, tr, ep)
	return testLoginCalendarDeps{
		svc:       NewLoginCalendarService(cr, lootSvc, ep),
		calR:      cr,
		lootSvc:   lootSvc,
		lootR:     lr,
		tableR:    tr,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// Tests — Grant Lootbox
// ---------------------------------------------------------------------------

func TestLootbox_Grant_Success(t *testing.T) {
	t.Parallel()
	d := newTestLootboxService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.lootR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.Lootbox")).Return(nil)

	got, err := d.svc.Grant(ctx, tenantID, gcid, LootboxTierStandard, LootboxSourceLevelUp)
	assert.NoError(t, err)
	assert.Equal(t, LootboxTierStandard, got.LootboxTier)
	assert.Equal(t, LootboxSourceLevelUp, got.Source)
	assert.False(t, got.IsOpened)
	assert.Equal(t, gcid, got.GCID)
	d.lootR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// Tests — Open Lootbox (weighted random)
// ---------------------------------------------------------------------------

func TestLootbox_Open_Success(t *testing.T) {
	t.Parallel()
	d := newTestLootboxService()
	ctx := context.Background()
	lootboxID := uuid.New()
	tenantID := uuid.New()
	gcid := uuid.New()

	lootbox := &Lootbox{
		ID: lootboxID, TenantID: tenantID, GCID: gcid,
		LootboxTier: LootboxTierStandard, Source: LootboxSourceLevelUp,
		IsOpened: false,
	}

	lootEntries := []*LootTable{
		{ID: uuid.New(), TenantID: tenantID, Tier: "standard", ItemType: LootItemCoins, QuantityMin: 10, QuantityMax: 50, Weight: 70},
		{ID: uuid.New(), TenantID: tenantID, Tier: "standard", ItemType: LootItemXP, QuantityMin: 25, QuantityMax: 100, Weight: 30},
	}

	d.lootR.On("GetByID", mock.Anything, lootboxID).Return(lootbox, nil)
	d.tableR.On("ListByTier", mock.Anything, tenantID, "standard").Return(lootEntries, nil)
	d.lootR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.Lootbox")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	result, err := d.svc.Open(ctx, lootboxID)
	assert.NoError(t, err)
	assert.True(t, lootbox.IsOpened)
	assert.NotNil(t, lootbox.OpenedAt)
	assert.NotNil(t, result)
	// The result should contain one of the possible item types
	assert.True(t, result.ItemType == LootItemCoins || result.ItemType == LootItemXP)
	d.lootR.AssertExpectations(t)
	d.tableR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestLootbox_Open_AlreadyOpened(t *testing.T) {
	t.Parallel()
	d := newTestLootboxService()
	ctx := context.Background()
	lootboxID := uuid.New()

	openedAt := time.Now().UTC()
	lootbox := &Lootbox{
		ID: lootboxID, IsOpened: true, OpenedAt: &openedAt,
		TenantID: uuid.New(), GCID: uuid.New(),
		LootboxTier: LootboxTierStandard,
	}

	d.lootR.On("GetByID", mock.Anything, lootboxID).Return(lootbox, nil)

	_, err := d.svc.Open(ctx, lootboxID)
	assert.ErrorIs(t, err, ErrLootboxAlreadyOpened)
}

func TestLootbox_Open_NotFound(t *testing.T) {
	t.Parallel()
	d := newTestLootboxService()
	ctx := context.Background()
	lootboxID := uuid.New()

	d.lootR.On("GetByID", mock.Anything, lootboxID).Return(nil, nil)

	_, err := d.svc.Open(ctx, lootboxID)
	assert.ErrorIs(t, err, ErrLootboxNotFound)
}

// ---------------------------------------------------------------------------
// Tests — DailyCheckin (new streak)
// ---------------------------------------------------------------------------

func TestLoginCalendar_DailyCheckin_NewStreak(t *testing.T) {
	t.Parallel()
	d := newTestLoginCalendarService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	today := time.Now().UTC().Truncate(24 * time.Hour)

	// No existing entry for today
	d.calR.On("GetByDate", mock.Anything, tenantID, gcid, today).Return(nil, nil)
	// No entry for yesterday either (new streak)
	yesterday := today.AddDate(0, 0, -1)
	d.calR.On("GetByDate", mock.Anything, tenantID, gcid, yesterday).Return(nil, nil)
	d.calR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.LoginCalendar")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.DailyCheckin(ctx, tenantID, gcid)
	assert.NoError(t, err)
	assert.Equal(t, 1, got.StreakDay)
	assert.True(t, got.RewardClaimed)
	d.calR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestLoginCalendar_DailyCheckin_ContinueStreak(t *testing.T) {
	t.Parallel()
	d := newTestLoginCalendarService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	yesterday := today.AddDate(0, 0, -1)

	// No entry for today
	d.calR.On("GetByDate", mock.Anything, tenantID, gcid, today).Return(nil, nil)
	// Yesterday's entry exists with streak 5
	yesterdayEntry := &LoginCalendar{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LoginDate: yesterday, StreakDay: 5, RewardClaimed: true,
	}
	d.calR.On("GetByDate", mock.Anything, tenantID, gcid, yesterday).Return(yesterdayEntry, nil)
	d.calR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.LoginCalendar")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.DailyCheckin(ctx, tenantID, gcid)
	assert.NoError(t, err)
	assert.Equal(t, 6, got.StreakDay)
	assert.True(t, got.RewardClaimed)
	d.calR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestLoginCalendar_DailyCheckin_SevenDayLootbox(t *testing.T) {
	t.Parallel()
	d := newTestLoginCalendarService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	yesterday := today.AddDate(0, 0, -1)

	// No entry for today
	d.calR.On("GetByDate", mock.Anything, tenantID, gcid, today).Return(nil, nil)
	// Yesterday = streak 6, so today = streak 7 → lootbox
	yesterdayEntry := &LoginCalendar{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LoginDate: yesterday, StreakDay: 6, RewardClaimed: true,
	}
	d.calR.On("GetByDate", mock.Anything, tenantID, gcid, yesterday).Return(yesterdayEntry, nil)
	d.calR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.LoginCalendar")).Return(nil)
	// Lootbox grant for 7-day streak
	d.lootR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.Lootbox")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.DailyCheckin(ctx, tenantID, gcid)
	assert.NoError(t, err)
	assert.Equal(t, 7, got.StreakDay)
	assert.Equal(t, "lootbox", got.RewardType)
	d.calR.AssertExpectations(t)
	d.lootR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestLoginCalendar_DailyCheckin_DuplicateSameDay(t *testing.T) {
	t.Parallel()
	d := newTestLoginCalendarService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	today := time.Now().UTC().Truncate(24 * time.Hour)

	existingEntry := &LoginCalendar{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LoginDate: today, StreakDay: 3, RewardClaimed: true,
	}

	d.calR.On("GetByDate", mock.Anything, tenantID, gcid, today).Return(existingEntry, nil)

	_, err := d.svc.DailyCheckin(ctx, tenantID, gcid)
	assert.ErrorIs(t, err, ErrDailyCheckinAlreadyClaimed)
}
