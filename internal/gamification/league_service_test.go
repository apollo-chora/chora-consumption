package gamification

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

type testLeagueDeps struct {
	svc       *LeagueService
	leagueR   *mockLeagueRepo
	badgeR    *mockBadgeRepo
	publisher *mockEventPublisher
}

func newTestLeagueService() testLeagueDeps {
	lr := &mockLeagueRepo{}
	br := &mockBadgeRepo{}
	ep := &mockEventPublisher{}
	return testLeagueDeps{
		svc:       NewLeagueService(lr, br, ep),
		leagueR:   lr,
		badgeR:    br,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// Tests — RecordPoints
// ---------------------------------------------------------------------------

func TestRecordPoints_Success(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existing := &League{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LeagueTier: LeagueTierSilver, SeasonWeek: "2026-W11",
		Points: 100,
	}

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(existing, nil)
	d.leagueR.On("Upsert", mock.Anything, mock.AnythingOfType("*gamification.League")).Return(nil)

	got, err := d.svc.RecordPoints(ctx, tenantID, gcid, "2026-W11", 50)
	assert.NoError(t, err)
	assert.Equal(t, 150, got.Points)
	d.leagueR.AssertExpectations(t)
}

func TestRecordPoints_NewSeasonWeek(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	// No existing standing for this week.
	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W12").Return(nil, nil)
	d.leagueR.On("Upsert", mock.Anything, mock.AnythingOfType("*gamification.League")).Return(nil)

	got, err := d.svc.RecordPoints(ctx, tenantID, gcid, "2026-W12", 30)
	assert.NoError(t, err)
	assert.Equal(t, 30, got.Points)
	assert.Equal(t, LeagueTierBronze, got.LeagueTier)
	assert.Equal(t, "2026-W12", got.SeasonWeek)
	d.leagueR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// Tests — GetCurrentStanding
// ---------------------------------------------------------------------------

func TestGetCurrentStanding_Success(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existing := &League{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LeagueTier: LeagueTierGold, SeasonWeek: "2026-W11",
		Points: 200, Rank: 5,
	}

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(existing, nil)

	got, err := d.svc.GetCurrentStanding(ctx, tenantID, gcid, "2026-W11")
	assert.NoError(t, err)
	assert.Equal(t, LeagueTierGold, got.LeagueTier)
	assert.Equal(t, 200, got.Points)
	d.leagueR.AssertExpectations(t)
}

func TestGetCurrentStanding_NotFound(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W99").Return(nil, nil)

	_, err := d.svc.GetCurrentStanding(ctx, tenantID, gcid, "2026-W99")
	assert.ErrorIs(t, err, ErrLeagueNotFound)
}

// ---------------------------------------------------------------------------
// Tests — GetStandings
// ---------------------------------------------------------------------------

func TestGetStandings_Success(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()

	standings := []*League{
		{ID: uuid.New(), LeagueTier: LeagueTierGold, Points: 300, Rank: 1},
		{ID: uuid.New(), LeagueTier: LeagueTierGold, Points: 250, Rank: 2},
	}

	d.leagueR.On("ListStandings", mock.Anything, tenantID, LeagueTierGold, "2026-W11", 0, 20).Return(standings, nil)

	got, err := d.svc.GetStandings(ctx, tenantID, LeagueTierGold, "2026-W11", 0, 20)
	assert.NoError(t, err)
	assert.Len(t, got, 2)
	d.leagueR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// Tests — ProcessPromotion
// ---------------------------------------------------------------------------

func TestProcessPromotion_Success(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existing := &League{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LeagueTier: LeagueTierSilver, SeasonWeek: "2026-W11",
		Points: 500, Rank: 2,
	}

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(existing, nil)
	d.leagueR.On("Upsert", mock.Anything, mock.AnythingOfType("*gamification.League")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.ProcessPromotion(ctx, tenantID, gcid, "2026-W11")
	assert.NoError(t, err)
	assert.True(t, got.Promoted)
	assert.Equal(t, LeagueTierGold, got.LeagueTier)
	d.leagueR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestProcessPromotion_NotEligible(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existing := &League{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LeagueTier: LeagueTierSilver, SeasonWeek: "2026-W11",
		Points: 50, Rank: 10,
	}

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(existing, nil)

	got, err := d.svc.ProcessPromotion(ctx, tenantID, gcid, "2026-W11")
	assert.NoError(t, err)
	assert.False(t, got.Promoted)
	assert.Equal(t, LeagueTierSilver, got.LeagueTier)
}

func TestProcessPromotion_NotFound(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(nil, nil)

	_, err := d.svc.ProcessPromotion(ctx, tenantID, gcid, "2026-W11")
	assert.ErrorIs(t, err, ErrLeagueNotFound)
}

// ---------------------------------------------------------------------------
// Tests — ProcessRelegation
// ---------------------------------------------------------------------------

func TestProcessRelegation_Success(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existing := &League{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LeagueTier: LeagueTierGold, SeasonWeek: "2026-W11",
		Points: 10, Rank: 19,
	}

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(existing, nil)
	d.leagueR.On("Upsert", mock.Anything, mock.AnythingOfType("*gamification.League")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.ProcessRelegation(ctx, tenantID, gcid, "2026-W11")
	assert.NoError(t, err)
	assert.True(t, got.Relegated)
	assert.Equal(t, LeagueTierSilver, got.LeagueTier)
	d.leagueR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestProcessRelegation_NotEligible(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	existing := &League{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		LeagueTier: LeagueTierGold, SeasonWeek: "2026-W11",
		Points: 200, Rank: 5,
	}

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(existing, nil)

	got, err := d.svc.ProcessRelegation(ctx, tenantID, gcid, "2026-W11")
	assert.NoError(t, err)
	assert.False(t, got.Relegated)
	assert.Equal(t, LeagueTierGold, got.LeagueTier)
}

func TestProcessRelegation_NotFound(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	d.leagueR.On("GetByGCID", mock.Anything, tenantID, gcid, "2026-W11").Return(nil, nil)

	_, err := d.svc.ProcessRelegation(ctx, tenantID, gcid, "2026-W11")
	assert.ErrorIs(t, err, ErrLeagueNotFound)
}

// ---------------------------------------------------------------------------
// Tests — AwardBadge
// ---------------------------------------------------------------------------

func TestAwardBadge_Success(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	topicID := uuid.New()

	// No existing badge.
	d.badgeR.On("GetByGCIDAndTopic", mock.Anything, tenantID, gcid, topicID).Return(nil, nil)
	d.badgeR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.TopicBadge")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.AwardBadge(ctx, tenantID, gcid, topicID, BadgeTierGold)
	assert.NoError(t, err)
	assert.Equal(t, BadgeTierGold, got.BadgeTier)
	assert.Equal(t, topicID, got.TopicID)
	d.badgeR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestAwardBadge_UpgradeTier(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	topicID := uuid.New()

	// Existing bronze badge — upgrading to gold should succeed.
	existingBadge := &TopicBadge{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		TopicID: topicID, BadgeTier: BadgeTierBronze,
	}

	d.badgeR.On("GetByGCIDAndTopic", mock.Anything, tenantID, gcid, topicID).Return(existingBadge, nil)
	d.badgeR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.TopicBadge")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.AwardBadge(ctx, tenantID, gcid, topicID, BadgeTierGold)
	assert.NoError(t, err)
	assert.Equal(t, BadgeTierGold, got.BadgeTier)
	d.badgeR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestAwardBadge_AlreadyEarned(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	topicID := uuid.New()

	// Existing gold badge — awarding silver should fail.
	existingBadge := &TopicBadge{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		TopicID: topicID, BadgeTier: BadgeTierGold,
	}

	d.badgeR.On("GetByGCIDAndTopic", mock.Anything, tenantID, gcid, topicID).Return(existingBadge, nil)

	_, err := d.svc.AwardBadge(ctx, tenantID, gcid, topicID, BadgeTierSilver)
	assert.ErrorIs(t, err, ErrBadgeAlreadyEarned)
}

func TestAwardBadge_SameTierAlreadyEarned(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	topicID := uuid.New()

	// Existing gold badge — awarding gold again should fail.
	existingBadge := &TopicBadge{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		TopicID: topicID, BadgeTier: BadgeTierGold,
	}

	d.badgeR.On("GetByGCIDAndTopic", mock.Anything, tenantID, gcid, topicID).Return(existingBadge, nil)

	_, err := d.svc.AwardBadge(ctx, tenantID, gcid, topicID, BadgeTierGold)
	assert.ErrorIs(t, err, ErrBadgeAlreadyEarned)
}

// ---------------------------------------------------------------------------
// Tests — ListBadges
// ---------------------------------------------------------------------------

func TestListBadges_Success(t *testing.T) {
	t.Parallel()
	d := newTestLeagueService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()

	badges := []*TopicBadge{
		{ID: uuid.New(), BadgeTier: BadgeTierGold},
		{ID: uuid.New(), BadgeTier: BadgeTierSilver},
	}

	d.badgeR.On("ListByGCID", mock.Anything, tenantID, gcid, 0, 20).Return(badges, nil)

	got, err := d.svc.ListBadges(ctx, tenantID, gcid, 0, 20)
	assert.NoError(t, err)
	assert.Len(t, got, 2)
	d.badgeR.AssertExpectations(t)
}
