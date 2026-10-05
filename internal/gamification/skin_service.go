package gamification

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// CreateSkin creates a new DigitalSkin in the catalog.
func (s *SkinService) CreateSkin(ctx context.Context, skin *DigitalSkin) (*DigitalSkin, error) {
	if skin.SkinCode == "" {
		return nil, fmt.Errorf("skin_code is required: %w", ErrValidationFailed)
	}
	if !skin.Rarity.IsValid() {
		return nil, fmt.Errorf("invalid rarity %q: %w", skin.Rarity, ErrValidationFailed)
	}

	now := time.Now().UTC()
	skin.ID = uuid.Must(uuid.NewV7())
	skin.CreatedAt = now
	skin.UpdatedAt = now

	if err := s.skinRepo.Create(ctx, skin); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(EventSkinCreated, uuid.Nil, nil, skin.ID, AggregateDigitalSkin, map[string]interface{}{
		"skin_code": skin.SkinCode,
		"rarity":    string(skin.Rarity),
	})
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, evt); err != nil {
		return nil, err
	}

	return skin, nil
}

// GetSkin retrieves a DigitalSkin by ID.
func (s *SkinService) GetSkin(ctx context.Context, id uuid.UUID) (*DigitalSkin, error) {
	skin, err := s.skinRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if skin == nil {
		return nil, ErrSkinNotFound
	}
	return skin, nil
}

// ListSkins returns skins matching the given filters.
func (s *SkinService) ListSkins(ctx context.Context, tenantID *uuid.UUID, rarity *SkinRarity, category *string, offset, limit int) ([]*DigitalSkin, error) {
	return s.skinRepo.List(ctx, tenantID, rarity, category, offset, limit)
}

// UpdateSkin modifies an existing DigitalSkin.
func (s *SkinService) UpdateSkin(ctx context.Context, skin *DigitalSkin) (*DigitalSkin, error) {
	if skin.SkinCode == "" {
		return nil, fmt.Errorf("skin_code is required: %w", ErrValidationFailed)
	}
	if !skin.Rarity.IsValid() {
		return nil, fmt.Errorf("invalid rarity %q: %w", skin.Rarity, ErrValidationFailed)
	}

	skin.UpdatedAt = time.Now().UTC()

	if err := s.skinRepo.Update(ctx, skin); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(EventSkinUpdated, uuid.Nil, nil, skin.ID, AggregateDigitalSkin, map[string]interface{}{
		"skin_code": skin.SkinCode,
	})
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, evt); err != nil {
		return nil, err
	}

	return skin, nil
}

// DeleteSkin soft-deletes a DigitalSkin.
func (s *SkinService) DeleteSkin(ctx context.Context, id uuid.UUID) error {
	if err := s.skinRepo.Delete(ctx, id); err != nil {
		return err
	}

	evt := NewDomainEvent(EventSkinDeleted, uuid.Nil, nil, id, AggregateDigitalSkin, map[string]interface{}{})
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, evt); err != nil {
		return err
	}

	return nil
}

// AwardSkin awards a DigitalSkin to a learner.
func (s *SkinService) AwardSkin(ctx context.Context, tenantID, gcid, skinID uuid.UUID, earnedVia SkinEarnedVia, earnedContext map[string]interface{}) (*SkinAward, error) {
	if !earnedVia.IsValid() {
		return nil, fmt.Errorf("invalid earned_via %q: %w", earnedVia, ErrValidationFailed)
	}

	skin, err := s.skinRepo.GetByID(ctx, skinID)
	if err != nil {
		return nil, err
	}
	if skin == nil {
		return nil, ErrSkinNotFound
	}

	award := &SkinAward{
		ID:            uuid.Must(uuid.NewV7()),
		TenantID:      tenantID,
		GCID:          gcid,
		SkinID:        skinID,
		EarnedAt:      time.Now().UTC(),
		EarnedVia:     earnedVia,
		EarnedContext: earnedContext,
	}

	if err := s.awardRepo.Create(ctx, award); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(EventSkinAwarded, tenantID, &gcid, award.ID, AggregateSkinAward, map[string]interface{}{
		"skin_id":    skinID.String(),
		"earned_via": string(earnedVia),
	})
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, evt); err != nil {
		return nil, err
	}

	return award, nil
}

// ListAwardsByGCID returns all skin awards for a learner.
func (s *SkinService) ListAwardsByGCID(ctx context.Context, tenantID, gcid uuid.UUID, offset, limit int) ([]*SkinAward, error) {
	return s.awardRepo.ListByGCID(ctx, tenantID, gcid, offset, limit)
}

// EquipSkin equips a skin to a display slot.
func (s *SkinService) EquipSkin(ctx context.Context, tenantID, gcid uuid.UUID, slot EquipmentSlot, skinAwardID uuid.UUID) (*SkinEquipment, error) {
	if !slot.IsValid() {
		return nil, fmt.Errorf("invalid slot %q: %w", slot, ErrValidationFailed)
	}

	equipment := &SkinEquipment{
		ID:          uuid.Must(uuid.NewV7()),
		TenantID:    tenantID,
		GCID:        gcid,
		Slot:        slot,
		SkinAwardID: skinAwardID,
		EquippedAt:  time.Now().UTC(),
	}

	if err := s.equipRepo.EquipSlot(ctx, equipment); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(EventSkinEquipped, tenantID, &gcid, equipment.ID, AggregateSkinEquipment, map[string]interface{}{
		"slot":          string(slot),
		"skin_award_id": skinAwardID.String(),
	})
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, evt); err != nil {
		return nil, err
	}

	return equipment, nil
}

// UnequipSkin removes a skin from a display slot.
func (s *SkinService) UnequipSkin(ctx context.Context, tenantID, gcid uuid.UUID, slot EquipmentSlot) error {
	if !slot.IsValid() {
		return fmt.Errorf("invalid slot %q: %w", slot, ErrValidationFailed)
	}

	if err := s.equipRepo.UnequipSlot(ctx, tenantID, gcid, slot); err != nil {
		return err
	}

	evt := NewDomainEvent(EventSkinUnequipped, tenantID, &gcid, uuid.Nil, AggregateSkinEquipment, map[string]interface{}{
		"slot": string(slot),
	})
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, evt); err != nil {
		return err
	}

	return nil
}

// GetEquipment returns all equipped skins for a learner.
func (s *SkinService) GetEquipment(ctx context.Context, tenantID, gcid uuid.UUID) ([]*SkinEquipment, error) {
	return s.equipRepo.GetByGCID(ctx, tenantID, gcid)
}

// ExportSkin exports a skin to an external platform.
func (s *SkinService) ExportSkin(ctx context.Context, tenantID, gcid, skinAwardID uuid.UUID, target ExportTarget) (*SkinExport, error) {
	if !target.IsValid() {
		return nil, fmt.Errorf("invalid export target %q: %w", target, ErrValidationFailed)
	}

	export := &SkinExport{
		ID:           uuid.Must(uuid.NewV7()),
		TenantID:     tenantID,
		GCID:         gcid,
		SkinAwardID:  skinAwardID,
		ExportTarget: target,
		ExportedAt:   time.Now().UTC(),
		ExportStatus: ExportStatusPending,
	}

	if err := s.exportRepo.Create(ctx, export); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(EventSkinExported, tenantID, &gcid, export.ID, AggregateSkinExport, map[string]interface{}{
		"target": string(target),
		"status": string(ExportStatusPending),
	})
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, evt); err != nil {
		return nil, err
	}

	return export, nil
}

// ListExports returns skin exports for a learner.
func (s *SkinService) ListExports(ctx context.Context, tenantID, gcid uuid.UUID, offset, limit int) ([]*SkinExport, error) {
	return s.exportRepo.ListByGCID(ctx, tenantID, gcid, offset, limit)
}

// TransferLegendarySkin transfers a legendary skin to a new holder.
func (s *SkinService) TransferLegendarySkin(ctx context.Context, tenantID, skinID, newHolderGCID uuid.UUID, reason string) (*LegendarySkin, error) {
	legendary, err := s.legendaryRepo.GetBySkinID(ctx, tenantID, skinID)
	if err != nil {
		return nil, err
	}
	if legendary == nil {
		return nil, ErrSkinNotFound
	}

	if legendary.CurrentHolderGCID == newHolderGCID {
		return nil, fmt.Errorf("new holder is the same as current holder: %w", ErrValidationFailed)
	}

	// Record previous holder.
	previousHolder := map[string]interface{}{
		"gcid":       legendary.CurrentHolderGCID.String(),
		"held_since": legendary.HeldSince.Format(time.RFC3339),
		"reason":     legendary.TransferReason,
	}
	legendary.PreviousHolders = append(legendary.PreviousHolders, previousHolder)
	legendary.CurrentHolderGCID = newHolderGCID
	legendary.HeldSince = time.Now().UTC()
	legendary.TransferReason = reason

	if err := s.legendaryRepo.TransferHolder(ctx, legendary); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(EventLegendaryTransferred, tenantID, &newHolderGCID, legendary.ID, AggregateLegendarySkin, map[string]interface{}{
		"skin_id":    skinID.String(),
		"new_holder": newHolderGCID.String(),
		"reason":     reason,
	})
	if err := s.publisher.Publish(ctx, TopicGamificationEvents, evt); err != nil {
		return nil, err
	}

	return legendary, nil
}

// GetLegendarySkin retrieves the current holder of a legendary skin.
func (s *SkinService) GetLegendarySkin(ctx context.Context, tenantID, skinID uuid.UUID) (*LegendarySkin, error) {
	legendary, err := s.legendaryRepo.GetBySkinID(ctx, tenantID, skinID)
	if err != nil {
		return nil, err
	}
	if legendary == nil {
		return nil, ErrSkinNotFound
	}
	return legendary, nil
}

// ListLegendarySkins returns all legendary skins with current holders.
func (s *SkinService) ListLegendarySkins(ctx context.Context, tenantID uuid.UUID, offset, limit int) ([]*LegendarySkin, error) {
	return s.legendaryRepo.List(ctx, tenantID, offset, limit)
}
