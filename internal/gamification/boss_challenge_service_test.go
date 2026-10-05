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

type testBossChallengeDeps struct {
	svc          *BossChallengeService
	challengeR   *mockBossChallengeRepo
	participantR *mockBossChallengeParticipantRepo
	publisher    *mockEventPublisher
}

func newTestBossChallengeService() testBossChallengeDeps {
	cr := &mockBossChallengeRepo{}
	pr := &mockBossChallengeParticipantRepo{}
	ep := &mockEventPublisher{}
	return testBossChallengeDeps{
		svc:          NewBossChallengeService(cr, pr, ep),
		challengeR:   cr,
		participantR: pr,
		publisher:    ep,
	}
}

// ---------------------------------------------------------------------------
// Tests — Create
// ---------------------------------------------------------------------------

func TestBossChallenge_Create_Success(t *testing.T) {
	t.Parallel()
	d := newTestBossChallengeService()
	ctx := context.Background()
	tenantID := uuid.New()
	topicNodeID := uuid.New()
	startsAt := time.Now().Add(24 * time.Hour)
	endsAt := startsAt.Add(2 * time.Hour)

	d.challengeR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.BossChallenge")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.Create(ctx, tenantID, "Dragon of Calculus", topicNodeID, BossDifficultyNormal, 100, 500, 200, nil, startsAt, endsAt)
	assert.NoError(t, err)
	assert.Equal(t, "Dragon of Calculus", got.BossName)
	assert.Equal(t, BossDifficultyNormal, got.DifficultyTier)
	assert.Equal(t, BossChallengeStatusScheduled, got.Status)
	assert.Equal(t, tenantID, got.TenantID)
	d.challengeR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestBossChallenge_Create_InvalidDifficulty(t *testing.T) {
	t.Parallel()
	d := newTestBossChallengeService()
	ctx := context.Background()
	tenantID := uuid.New()
	topicNodeID := uuid.New()
	startsAt := time.Now().Add(24 * time.Hour)
	endsAt := startsAt.Add(2 * time.Hour)

	_, err := d.svc.Create(ctx, tenantID, "Bad Boss", topicNodeID, BossDifficultyTier("impossible"), 100, 500, 200, nil, startsAt, endsAt)
	assert.ErrorIs(t, err, ErrValidationFailed)
}

// ---------------------------------------------------------------------------
// Tests — Start
// ---------------------------------------------------------------------------

func TestBossChallenge_Start_Success(t *testing.T) {
	t.Parallel()
	d := newTestBossChallengeService()
	ctx := context.Background()
	challengeID := uuid.New()

	challenge := &BossChallenge{
		ID: challengeID, Status: BossChallengeStatusScheduled,
		TenantID: uuid.New(),
	}

	d.challengeR.On("GetByID", mock.Anything, challengeID).Return(challenge, nil)
	d.challengeR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.BossChallenge")).Return(nil)

	got, err := d.svc.Start(ctx, challengeID)
	assert.NoError(t, err)
	assert.Equal(t, BossChallengeStatusActive, got.Status)
	d.challengeR.AssertExpectations(t)
}

func TestBossChallenge_Start_NotFound(t *testing.T) {
	t.Parallel()
	d := newTestBossChallengeService()
	ctx := context.Background()
	challengeID := uuid.New()

	d.challengeR.On("GetByID", mock.Anything, challengeID).Return(nil, nil)

	_, err := d.svc.Start(ctx, challengeID)
	assert.ErrorIs(t, err, ErrBossChallengeNotFound)
}

func TestBossChallenge_Start_NotScheduled(t *testing.T) {
	t.Parallel()
	d := newTestBossChallengeService()
	ctx := context.Background()
	challengeID := uuid.New()

	challenge := &BossChallenge{
		ID: challengeID, Status: BossChallengeStatusCompleted,
		TenantID: uuid.New(),
	}

	d.challengeR.On("GetByID", mock.Anything, challengeID).Return(challenge, nil)

	_, err := d.svc.Start(ctx, challengeID)
	assert.ErrorIs(t, err, ErrBossChallengeNotScheduled)
}

// ---------------------------------------------------------------------------
// Tests — RecordParticipation
// ---------------------------------------------------------------------------

func TestBossChallenge_RecordParticipation_Success(t *testing.T) {
	t.Parallel()
	d := newTestBossChallengeService()
	ctx := context.Background()
	challengeID := uuid.New()
	gcid := uuid.New()
	tenantID := uuid.New()

	challenge := &BossChallenge{
		ID: challengeID, TenantID: tenantID,
		Status: BossChallengeStatusActive, RequiredScore: 100,
	}

	d.challengeR.On("GetByID", mock.Anything, challengeID).Return(challenge, nil)
	d.participantR.On("Upsert", mock.Anything, mock.AnythingOfType("*gamification.BossChallengeParticipant")).Return(nil)

	got, err := d.svc.RecordParticipation(ctx, challengeID, gcid, 75)
	assert.NoError(t, err)
	assert.Equal(t, 75, got.Score)
	assert.Equal(t, gcid, got.GCID)
	d.challengeR.AssertExpectations(t)
	d.participantR.AssertExpectations(t)
}

func TestBossChallenge_RecordParticipation_NotActive(t *testing.T) {
	t.Parallel()
	d := newTestBossChallengeService()
	ctx := context.Background()
	challengeID := uuid.New()
	gcid := uuid.New()

	challenge := &BossChallenge{
		ID: challengeID, Status: BossChallengeStatusScheduled,
		TenantID: uuid.New(),
	}

	d.challengeR.On("GetByID", mock.Anything, challengeID).Return(challenge, nil)

	_, err := d.svc.RecordParticipation(ctx, challengeID, gcid, 50)
	assert.ErrorIs(t, err, ErrBossChallengeNotActive)
}

// ---------------------------------------------------------------------------
// Tests — Complete + DistributeRewards
// ---------------------------------------------------------------------------

func TestBossChallenge_Complete_Success(t *testing.T) {
	t.Parallel()
	d := newTestBossChallengeService()
	ctx := context.Background()
	challengeID := uuid.New()
	tenantID := uuid.New()
	gcid1 := uuid.New()
	gcid2 := uuid.New()

	challenge := &BossChallenge{
		ID: challengeID, TenantID: tenantID,
		Status: BossChallengeStatusActive, RequiredScore: 100,
		RewardXP: 500, RewardCoins: 200,
	}

	participants := []*BossChallengeParticipant{
		{ID: uuid.New(), ChallengeID: challengeID, GCID: gcid1, Score: 120, TenantID: tenantID},
		{ID: uuid.New(), ChallengeID: challengeID, GCID: gcid2, Score: 50, TenantID: tenantID},
	}

	d.challengeR.On("GetByID", mock.Anything, challengeID).Return(challenge, nil)
	d.challengeR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.BossChallenge")).Return(nil)
	d.participantR.On("ListByChallenge", mock.Anything, challengeID).Return(participants, nil)
	d.participantR.On("Upsert", mock.Anything, mock.AnythingOfType("*gamification.BossChallengeParticipant")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	got, err := d.svc.Complete(ctx, challengeID)
	assert.NoError(t, err)
	assert.Equal(t, BossChallengeStatusCompleted, got.Status)

	// gcid1 met the required score — reward claimed
	assert.True(t, participants[0].RewardClaimed)
	assert.NotNil(t, participants[0].CompletedAt)
	// gcid2 did not meet required score — no reward
	assert.False(t, participants[1].RewardClaimed)

	d.challengeR.AssertExpectations(t)
	d.participantR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestBossChallenge_Complete_NotActive(t *testing.T) {
	t.Parallel()
	d := newTestBossChallengeService()
	ctx := context.Background()
	challengeID := uuid.New()

	challenge := &BossChallenge{
		ID: challengeID, Status: BossChallengeStatusScheduled,
		TenantID: uuid.New(),
	}

	d.challengeR.On("GetByID", mock.Anything, challengeID).Return(challenge, nil)

	_, err := d.svc.Complete(ctx, challengeID)
	assert.ErrorIs(t, err, ErrBossChallengeNotActive)
}

// ---------------------------------------------------------------------------
// Tests — ListActive
// ---------------------------------------------------------------------------

func TestBossChallenge_ListActive_Success(t *testing.T) {
	t.Parallel()
	d := newTestBossChallengeService()
	ctx := context.Background()
	tenantID := uuid.New()

	challenges := []*BossChallenge{
		{ID: uuid.New(), Status: BossChallengeStatusActive},
	}

	d.challengeR.On("ListByStatus", mock.Anything, tenantID, BossChallengeStatusActive, 0, 20).Return(challenges, nil)

	got, err := d.svc.ListActive(ctx, tenantID, 0, 20)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	d.challengeR.AssertExpectations(t)
}
