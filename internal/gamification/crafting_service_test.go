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

type testCraftingDeps struct {
	svc       *CraftingService
	matR      *mockMaterialRepo
	invR      *mockMaterialInventoryRepo
	recipeR   *mockCraftingRecipeRepo
	publisher *mockEventPublisher
}

func newTestCraftingService() testCraftingDeps {
	mr := &mockMaterialRepo{}
	ir := &mockMaterialInventoryRepo{}
	rr := &mockCraftingRecipeRepo{}
	ep := &mockEventPublisher{}
	return testCraftingDeps{
		svc:       NewCraftingService(mr, ir, rr, ep),
		matR:      mr,
		invR:      ir,
		recipeR:   rr,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// Tests — ListRecipes
// ---------------------------------------------------------------------------

func TestCrafting_ListRecipes_Success(t *testing.T) {
	t.Parallel()
	d := newTestCraftingService()
	ctx := context.Background()
	tenantID := uuid.New()

	recipes := []*CraftingRecipe{
		{ID: uuid.New(), RecipeName: "Iron Sword"},
	}

	d.recipeR.On("List", mock.Anything, tenantID, 0, 20).Return(recipes, nil)

	got, err := d.svc.ListRecipes(ctx, tenantID, 0, 20)
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "Iron Sword", got[0].RecipeName)
	d.recipeR.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// Tests — ExecuteRecipe
// ---------------------------------------------------------------------------

func TestCrafting_ExecuteRecipe_Success(t *testing.T) {
	t.Parallel()
	d := newTestCraftingService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	recipeID := uuid.New()
	mat1ID := uuid.New()
	mat2ID := uuid.New()

	recipe := &CraftingRecipe{
		ID:         recipeID,
		TenantID:   tenantID,
		RecipeName: "Iron Sword",
		InputMaterials: []RecipeInput{
			{MaterialID: mat1ID, Quantity: 2},
			{MaterialID: mat2ID, Quantity: 1},
		},
		OutputType:     RecipeOutputCoins,
		OutputQuantity: 100,
	}

	inv1 := &MaterialInventory{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		MaterialID: mat1ID, Quantity: 5,
	}
	inv2 := &MaterialInventory{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		MaterialID: mat2ID, Quantity: 3,
	}

	d.recipeR.On("GetByID", mock.Anything, recipeID).Return(recipe, nil)
	d.invR.On("GetByGCIDAndMaterial", mock.Anything, tenantID, gcid, mat1ID).Return(inv1, nil)
	d.invR.On("GetByGCIDAndMaterial", mock.Anything, tenantID, gcid, mat2ID).Return(inv2, nil)
	d.invR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.MaterialInventory")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)

	result, err := d.svc.ExecuteRecipe(ctx, tenantID, gcid, recipeID)
	assert.NoError(t, err)
	assert.Equal(t, RecipeOutputCoins, result.OutputType)
	assert.Equal(t, 100, result.OutputQuantity)

	// Verify materials were deducted
	assert.Equal(t, 3, inv1.Quantity) // 5 - 2
	assert.Equal(t, 2, inv2.Quantity) // 3 - 1

	d.recipeR.AssertExpectations(t)
	d.invR.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestCrafting_ExecuteRecipe_InsufficientMaterials(t *testing.T) {
	t.Parallel()
	d := newTestCraftingService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	recipeID := uuid.New()
	mat1ID := uuid.New()

	recipe := &CraftingRecipe{
		ID:         recipeID,
		TenantID:   tenantID,
		RecipeName: "Iron Sword",
		InputMaterials: []RecipeInput{
			{MaterialID: mat1ID, Quantity: 5},
		},
		OutputType:     RecipeOutputCoins,
		OutputQuantity: 100,
	}

	inv1 := &MaterialInventory{
		ID: uuid.New(), TenantID: tenantID, GCID: gcid,
		MaterialID: mat1ID, Quantity: 2, // Not enough
	}

	d.recipeR.On("GetByID", mock.Anything, recipeID).Return(recipe, nil)
	d.invR.On("GetByGCIDAndMaterial", mock.Anything, tenantID, gcid, mat1ID).Return(inv1, nil)

	_, err := d.svc.ExecuteRecipe(ctx, tenantID, gcid, recipeID)
	assert.ErrorIs(t, err, ErrInsufficientMaterials)
}

func TestCrafting_ExecuteRecipe_RecipeNotFound(t *testing.T) {
	t.Parallel()
	d := newTestCraftingService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	recipeID := uuid.New()

	d.recipeR.On("GetByID", mock.Anything, recipeID).Return(nil, nil)

	_, err := d.svc.ExecuteRecipe(ctx, tenantID, gcid, recipeID)
	assert.ErrorIs(t, err, ErrCraftingRecipeNotFound)
}

func TestCrafting_ExecuteRecipe_MaterialNotInInventory(t *testing.T) {
	t.Parallel()
	d := newTestCraftingService()
	ctx := context.Background()
	tenantID := uuid.New()
	gcid := uuid.New()
	recipeID := uuid.New()
	mat1ID := uuid.New()

	recipe := &CraftingRecipe{
		ID:         recipeID,
		TenantID:   tenantID,
		RecipeName: "Iron Sword",
		InputMaterials: []RecipeInput{
			{MaterialID: mat1ID, Quantity: 2},
		},
		OutputType:     RecipeOutputCoins,
		OutputQuantity: 100,
	}

	d.recipeR.On("GetByID", mock.Anything, recipeID).Return(recipe, nil)
	d.invR.On("GetByGCIDAndMaterial", mock.Anything, tenantID, gcid, mat1ID).Return(nil, nil)

	_, err := d.svc.ExecuteRecipe(ctx, tenantID, gcid, recipeID)
	assert.ErrorIs(t, err, ErrInsufficientMaterials)
}
