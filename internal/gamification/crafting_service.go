package gamification

import (
	"context"

	"github.com/google/uuid"
)

// ListRecipes returns available crafting recipes for a tenant.
func (s *CraftingService) ListRecipes(ctx context.Context, tenantID uuid.UUID, offset, limit int) ([]*CraftingRecipe, error) {
	return s.recipeRepo.List(ctx, tenantID, offset, limit)
}

// ExecuteRecipe validates materials, deducts inputs, and produces output.
func (s *CraftingService) ExecuteRecipe(
	ctx context.Context,
	tenantID, gcid, recipeID uuid.UUID,
) (*CraftResult, error) {
	recipe, err := s.recipeRepo.GetByID(ctx, recipeID)
	if err != nil {
		return nil, err
	}
	if recipe == nil {
		return nil, ErrCraftingRecipeNotFound
	}

	// Validate all input materials are available in sufficient quantity.
	inventories := make([]*MaterialInventory, 0, len(recipe.InputMaterials))
	for _, input := range recipe.InputMaterials {
		inv, err := s.inventoryRepo.GetByGCIDAndMaterial(ctx, tenantID, gcid, input.MaterialID)
		if err != nil {
			return nil, err
		}
		if inv == nil || inv.Quantity < input.Quantity {
			return nil, ErrInsufficientMaterials
		}
		inventories = append(inventories, inv)
	}

	// Deduct input materials.
	for i, input := range recipe.InputMaterials {
		inventories[i].Quantity -= input.Quantity
		if err := s.inventoryRepo.Update(ctx, inventories[i]); err != nil {
			return nil, err
		}
	}

	result := &CraftResult{
		RecipeID:       recipe.ID,
		OutputType:     recipe.OutputType,
		OutputID:       recipe.OutputID,
		OutputQuantity: recipe.OutputQuantity,
	}

	event := NewDomainEvent(
		EventCraftingRecipeCompleted,
		tenantID,
		&gcid,
		recipe.ID,
		AggregateCraftingRecipe,
		map[string]interface{}{
			"recipe_name":     recipe.RecipeName,
			"output_type":     string(recipe.OutputType),
			"output_quantity": recipe.OutputQuantity,
		},
	)
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, event); err != nil {
		return nil, err
	}

	return result, nil
}
