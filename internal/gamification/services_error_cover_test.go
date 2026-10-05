package gamification

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// ===========================================================================
// BossChallengeService — repo/publisher error branches
// ===========================================================================

func TestBossChallenge_Create_RepoAndPublishErrors(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	topicNodeID := uuid.New()
	start := time.Now().Add(time.Hour)
	end := start.Add(time.Hour)

	t.Run("challenge Create error propagates", func(t *testing.T) {
		d := newTestBossChallengeService()
		d.challengeR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.BossChallenge")).Return(errBoom)

		_, err := d.svc.Create(ctx, tenantID, "B", topicNodeID, BossDifficultyNormal, 100, 500, 200, nil, start, end)
		assert.ErrorIs(t, err, errBoom)
		d.publisher.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("publish error propagates", func(t *testing.T) {
		d := newTestBossChallengeService()
		d.challengeR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.BossChallenge")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errBoom)

		_, err := d.svc.Create(ctx, tenantID, "B", topicNodeID, BossDifficultyNormal, 100, 500, 200, nil, start, end)
		assert.ErrorIs(t, err, errBoom)
	})
}

func TestBossChallenge_Start_RepoErrors(t *testing.T) {
	ctx := context.Background()
	challengeID := uuid.New()

	t.Run("GetByID error propagates", func(t *testing.T) {
		d := newTestBossChallengeService()
		d.challengeR.On("GetByID", mock.Anything, challengeID).Return(nil, errBoom)

		_, err := d.svc.Start(ctx, challengeID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("Update error propagates", func(t *testing.T) {
		d := newTestBossChallengeService()
		ch := &BossChallenge{ID: challengeID, Status: BossChallengeStatusScheduled, TenantID: uuid.New()}
		d.challengeR.On("GetByID", mock.Anything, challengeID).Return(ch, nil)
		d.challengeR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.BossChallenge")).Return(errBoom)

		_, err := d.svc.Start(ctx, challengeID)
		assert.ErrorIs(t, err, errBoom)
	})
}

func TestBossChallenge_RecordParticipation_RepoErrors(t *testing.T) {
	ctx := context.Background()
	challengeID := uuid.New()
	gcid := uuid.New()

	t.Run("GetByID error propagates", func(t *testing.T) {
		d := newTestBossChallengeService()
		d.challengeR.On("GetByID", mock.Anything, challengeID).Return(nil, errBoom)

		_, err := d.svc.RecordParticipation(ctx, challengeID, gcid, 10)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("challenge not found", func(t *testing.T) {
		d := newTestBossChallengeService()
		d.challengeR.On("GetByID", mock.Anything, challengeID).Return(nil, nil)

		_, err := d.svc.RecordParticipation(ctx, challengeID, gcid, 10)
		assert.ErrorIs(t, err, ErrBossChallengeNotFound)
	})

	t.Run("Upsert error propagates", func(t *testing.T) {
		d := newTestBossChallengeService()
		ch := &BossChallenge{ID: challengeID, Status: BossChallengeStatusActive, TenantID: uuid.New()}
		d.challengeR.On("GetByID", mock.Anything, challengeID).Return(ch, nil)
		d.participantR.On("Upsert", mock.Anything, mock.AnythingOfType("*gamification.BossChallengeParticipant")).Return(errBoom)

		_, err := d.svc.RecordParticipation(ctx, challengeID, gcid, 10)
		assert.ErrorIs(t, err, errBoom)
	})
}

func TestBossChallenge_Complete_RepoErrors(t *testing.T) {
	ctx := context.Background()
	challengeID := uuid.New()
	tenantID := uuid.New()

	t.Run("GetByID error propagates", func(t *testing.T) {
		d := newTestBossChallengeService()
		d.challengeR.On("GetByID", mock.Anything, challengeID).Return(nil, errBoom)

		_, err := d.svc.Complete(ctx, challengeID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("challenge not found", func(t *testing.T) {
		d := newTestBossChallengeService()
		d.challengeR.On("GetByID", mock.Anything, challengeID).Return(nil, nil)

		_, err := d.svc.Complete(ctx, challengeID)
		assert.ErrorIs(t, err, ErrBossChallengeNotFound)
	})

	t.Run("ListByChallenge error propagates", func(t *testing.T) {
		d := newTestBossChallengeService()
		ch := &BossChallenge{ID: challengeID, Status: BossChallengeStatusActive, TenantID: tenantID, RequiredScore: 100}
		d.challengeR.On("GetByID", mock.Anything, challengeID).Return(ch, nil)
		d.participantR.On("ListByChallenge", mock.Anything, challengeID).Return(nil, errBoom)

		_, err := d.svc.Complete(ctx, challengeID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("qualifying participant Upsert error propagates", func(t *testing.T) {
		d := newTestBossChallengeService()
		ch := &BossChallenge{ID: challengeID, Status: BossChallengeStatusActive, TenantID: tenantID, RequiredScore: 100}
		parts := []*BossChallengeParticipant{
			{ID: uuid.New(), ChallengeID: challengeID, GCID: uuid.New(), Score: 150, TenantID: tenantID},
		}
		d.challengeR.On("GetByID", mock.Anything, challengeID).Return(ch, nil)
		d.participantR.On("ListByChallenge", mock.Anything, challengeID).Return(parts, nil)
		d.participantR.On("Upsert", mock.Anything, mock.AnythingOfType("*gamification.BossChallengeParticipant")).Return(errBoom)

		_, err := d.svc.Complete(ctx, challengeID)
		assert.ErrorIs(t, err, errBoom)
		d.challengeR.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
	})

	t.Run("challenge Update error propagates", func(t *testing.T) {
		d := newTestBossChallengeService()
		ch := &BossChallenge{ID: challengeID, Status: BossChallengeStatusActive, TenantID: tenantID, RequiredScore: 100}
		// non-qualifying participant so Upsert is skipped, isolating the Update error
		parts := []*BossChallengeParticipant{
			{ID: uuid.New(), ChallengeID: challengeID, GCID: uuid.New(), Score: 10, TenantID: tenantID},
		}
		d.challengeR.On("GetByID", mock.Anything, challengeID).Return(ch, nil)
		d.participantR.On("ListByChallenge", mock.Anything, challengeID).Return(parts, nil)
		d.challengeR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.BossChallenge")).Return(errBoom)

		_, err := d.svc.Complete(ctx, challengeID)
		assert.ErrorIs(t, err, errBoom)
	})
}

// ===========================================================================
// CraftingService.ExecuteRecipe — inventory/update/publish error branches
// ===========================================================================

func TestCrafting_ExecuteRecipe_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	recipeID := uuid.New()
	matID := uuid.New()

	newRecipe := func() *CraftingRecipe {
		return &CraftingRecipe{
			ID:             recipeID,
			TenantID:       tenantID,
			RecipeName:     "R",
			OutputType:     RecipeOutputCoins,
			OutputQuantity: 10,
			InputMaterials: []RecipeInput{{MaterialID: matID, Quantity: 2}},
		}
	}

	t.Run("recipe lookup error propagates", func(t *testing.T) {
		d := newTestCraftingService()
		d.recipeR.On("GetByID", mock.Anything, recipeID).Return(nil, errBoom)

		_, err := d.svc.ExecuteRecipe(ctx, tenantID, gcid, recipeID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("inventory lookup error propagates", func(t *testing.T) {
		d := newTestCraftingService()
		d.recipeR.On("GetByID", mock.Anything, recipeID).Return(newRecipe(), nil)
		d.invR.On("GetByGCIDAndMaterial", mock.Anything, tenantID, gcid, matID).Return(nil, errBoom)

		_, err := d.svc.ExecuteRecipe(ctx, tenantID, gcid, recipeID)
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("inventory Update error propagates", func(t *testing.T) {
		d := newTestCraftingService()
		inv := &MaterialInventory{ID: uuid.New(), TenantID: tenantID, GCID: gcid, MaterialID: matID, Quantity: 5}
		d.recipeR.On("GetByID", mock.Anything, recipeID).Return(newRecipe(), nil)
		d.invR.On("GetByGCIDAndMaterial", mock.Anything, tenantID, gcid, matID).Return(inv, nil)
		d.invR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.MaterialInventory")).Return(errBoom)

		_, err := d.svc.ExecuteRecipe(ctx, tenantID, gcid, recipeID)
		assert.ErrorIs(t, err, errBoom)
		d.publisher.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("publish error propagates", func(t *testing.T) {
		d := newTestCraftingService()
		inv := &MaterialInventory{ID: uuid.New(), TenantID: tenantID, GCID: gcid, MaterialID: matID, Quantity: 5}
		d.recipeR.On("GetByID", mock.Anything, recipeID).Return(newRecipe(), nil)
		d.invR.On("GetByGCIDAndMaterial", mock.Anything, tenantID, gcid, matID).Return(inv, nil)
		d.invR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.MaterialInventory")).Return(nil)
		d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(errBoom)

		_, err := d.svc.ExecuteRecipe(ctx, tenantID, gcid, recipeID)
		assert.ErrorIs(t, err, errBoom)
	})
}
