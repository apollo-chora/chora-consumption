package gamification

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// testSkinDeps holds all mocks wired into a SkinService.
type testSkinDeps struct {
	svc        *SkinService
	skinR      *mockSkinRepo
	awardR     *mockAwardRepo
	equipR     *mockEquipmentRepo
	exportR    *mockExportRepo
	legendaryR *mockLegendaryRepo
	publisher  *mockEventPublisher
}

// newTestSkinService creates a SkinService with fresh mocks.
func newTestSkinService() testSkinDeps {
	sr := &mockSkinRepo{}
	ar := &mockAwardRepo{}
	er := &mockEquipmentRepo{}
	xr := &mockExportRepo{}
	lr := &mockLegendaryRepo{}
	ep := &mockEventPublisher{}
	return testSkinDeps{
		svc:        NewSkinService(sr, ar, er, xr, lr, ep),
		skinR:      sr,
		awardR:     ar,
		equipR:     er,
		exportR:    xr,
		legendaryR: lr,
		publisher:  ep,
	}
}

// ---------------------------------------------------------------------------
// TestCreateSkin
// ---------------------------------------------------------------------------

func TestCreateSkin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      *DigitalSkin
		setupMocks func(d testSkinDeps)
		wantErr    error
		assert     func(t *testing.T, got *DigitalSkin)
	}{
		{
			name: "success: creates skin with UUIDv7",
			input: &DigitalSkin{
				SkinCode:    "SKN-FLAME-001",
				Name:        "Flame Avatar",
				Description: "A fiery avatar skin earned through streak mastery",
				Category:    "avatar",
				Rarity:      SkinRarityEpic,
				AssetURL:    "https://cdn.chora.io/skins/flame-avatar.png",
			},
			setupMocks: func(d testSkinDeps) {
				d.skinR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.DigitalSkin")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)
			},
			assert: func(t *testing.T, got *DigitalSkin) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, "SKN-FLAME-001", got.SkinCode)
				assert.Equal(t, SkinRarityEpic, got.Rarity)
				assert.False(t, got.CreatedAt.IsZero())
				assert.False(t, got.UpdatedAt.IsZero())
			},
		},
		{
			name: "error: invalid rarity",
			input: &DigitalSkin{
				SkinCode:    "SKN-BAD-001",
				Name:        "Bad Rarity Skin",
				Description: "Invalid rarity value",
				Category:    "avatar",
				Rarity:      SkinRarity("mythical"),
				AssetURL:    "https://cdn.chora.io/skins/bad.png",
			},
			wantErr: ErrValidationFailed,
		},
		{
			name: "error: empty skin_code",
			input: &DigitalSkin{
				SkinCode:    "",
				Name:        "No Code Skin",
				Description: "Missing skin code",
				Category:    "avatar",
				Rarity:      SkinRarityCommon,
				AssetURL:    "https://cdn.chora.io/skins/nocode.png",
			},
			wantErr: ErrValidationFailed,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSkinService()
			if tc.setupMocks != nil {
				tc.setupMocks(d)
			}

			got, err := d.svc.CreateSkin(context.Background(), tc.input)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
				return
			}
			assert.NoError(t, err)
			if tc.assert != nil {
				tc.assert(t, got)
			}
			d.skinR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetSkin
// ---------------------------------------------------------------------------

func TestGetSkin(t *testing.T) {
	t.Parallel()

	skinID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		id         uuid.UUID
		setupMocks func(d testSkinDeps)
		wantErr    error
		assert     func(t *testing.T, got *DigitalSkin)
	}{
		{
			name: "success: retrieves skin by ID",
			id:   skinID,
			setupMocks: func(d testSkinDeps) {
				d.skinR.On("GetByID", mock.Anything, skinID).Return(&DigitalSkin{
					ID:       skinID,
					SkinCode: "SKN-FLAME-001",
					Rarity:   SkinRarityEpic,
				}, nil)
			},
			assert: func(t *testing.T, got *DigitalSkin) {
				assert.Equal(t, skinID, got.ID)
				assert.Equal(t, "SKN-FLAME-001", got.SkinCode)
			},
		},
		{
			name: "error: skin not found",
			id:   uuid.Must(uuid.NewV7()),
			setupMocks: func(d testSkinDeps) {
				d.skinR.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(nil, nil)
			},
			wantErr: ErrSkinNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSkinService()
			if tc.setupMocks != nil {
				tc.setupMocks(d)
			}

			got, err := d.svc.GetSkin(context.Background(), tc.id)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
				return
			}
			assert.NoError(t, err)
			if tc.assert != nil {
				tc.assert(t, got)
			}
			d.skinR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListSkins
// ---------------------------------------------------------------------------

func TestListSkins(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	rarity := SkinRarityRare
	category := "avatar"

	tests := []struct {
		name       string
		tenantID   *uuid.UUID
		rarity     *SkinRarity
		category   *string
		offset     int
		limit      int
		setupMocks func(d testSkinDeps)
		wantCount  int
	}{
		{
			name:     "success: lists skins with filters",
			tenantID: &tenantID,
			rarity:   &rarity,
			category: &category,
			offset:   0,
			limit:    20,
			setupMocks: func(d testSkinDeps) {
				d.skinR.On("List", mock.Anything, &tenantID, &rarity, &category, 0, 20).Return([]*DigitalSkin{
					{ID: uuid.Must(uuid.NewV7()), SkinCode: "SKN-001", Rarity: SkinRarityRare},
					{ID: uuid.Must(uuid.NewV7()), SkinCode: "SKN-002", Rarity: SkinRarityRare},
				}, nil)
			},
			wantCount: 2,
		},
		{
			name:     "success: lists skins with no filters",
			tenantID: nil,
			rarity:   nil,
			category: nil,
			offset:   0,
			limit:    50,
			setupMocks: func(d testSkinDeps) {
				d.skinR.On("List", mock.Anything, (*uuid.UUID)(nil), (*SkinRarity)(nil), (*string)(nil), 0, 50).Return([]*DigitalSkin{}, nil)
			},
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSkinService()
			if tc.setupMocks != nil {
				tc.setupMocks(d)
			}

			got, err := d.svc.ListSkins(context.Background(), tc.tenantID, tc.rarity, tc.category, tc.offset, tc.limit)
			assert.NoError(t, err)
			assert.Len(t, got, tc.wantCount)
			d.skinR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestUpdateSkin
// ---------------------------------------------------------------------------

func TestUpdateSkin(t *testing.T) {
	t.Parallel()

	skinID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		input      *DigitalSkin
		setupMocks func(d testSkinDeps)
		wantErr    error
		assert     func(t *testing.T, got *DigitalSkin)
	}{
		{
			name: "success: updates skin",
			input: &DigitalSkin{
				ID:          skinID,
				SkinCode:    "SKN-FLAME-001",
				Name:        "Updated Flame Avatar",
				Description: "Updated description",
				Category:    "avatar",
				Rarity:      SkinRarityEpic,
				AssetURL:    "https://cdn.chora.io/skins/flame-v2.png",
			},
			setupMocks: func(d testSkinDeps) {
				d.skinR.On("Update", mock.Anything, mock.AnythingOfType("*gamification.DigitalSkin")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)
			},
			assert: func(t *testing.T, got *DigitalSkin) {
				assert.Equal(t, skinID, got.ID)
				assert.Equal(t, "Updated Flame Avatar", got.Name)
				assert.False(t, got.UpdatedAt.IsZero())
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSkinService()
			if tc.setupMocks != nil {
				tc.setupMocks(d)
			}

			got, err := d.svc.UpdateSkin(context.Background(), tc.input)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
				return
			}
			assert.NoError(t, err)
			if tc.assert != nil {
				tc.assert(t, got)
			}
			d.skinR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestDeleteSkin
// ---------------------------------------------------------------------------

func TestDeleteSkin(t *testing.T) {
	t.Parallel()

	skinID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		id         uuid.UUID
		setupMocks func(d testSkinDeps)
		wantErr    error
	}{
		{
			name: "success: soft-deletes skin",
			id:   skinID,
			setupMocks: func(d testSkinDeps) {
				d.skinR.On("Delete", mock.Anything, skinID).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSkinService()
			if tc.setupMocks != nil {
				tc.setupMocks(d)
			}

			err := d.svc.DeleteSkin(context.Background(), tc.id)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}
			assert.NoError(t, err)
			d.skinR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestAwardSkin
// ---------------------------------------------------------------------------

func TestAwardSkin(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	skinID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name          string
		tenantID      uuid.UUID
		gcid          uuid.UUID
		skinID        uuid.UUID
		earnedVia     SkinEarnedVia
		earnedContext map[string]interface{}
		setupMocks    func(d testSkinDeps)
		wantErr       error
		assert        func(t *testing.T, got *SkinAward)
	}{
		{
			name:      "success: awards skin to learner",
			tenantID:  tenantID,
			gcid:      gcid,
			skinID:    skinID,
			earnedVia: SkinEarnedViaStreak,
			earnedContext: map[string]interface{}{
				"streak_length": 30,
			},
			setupMocks: func(d testSkinDeps) {
				d.skinR.On("GetByID", mock.Anything, skinID).Return(&DigitalSkin{
					ID:       skinID,
					SkinCode: "SKN-FLAME-001",
				}, nil)
				d.awardR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.SkinAward")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)
			},
			assert: func(t *testing.T, got *SkinAward) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, tenantID, got.TenantID)
				assert.Equal(t, gcid, got.GCID)
				assert.Equal(t, skinID, got.SkinID)
				assert.Equal(t, SkinEarnedViaStreak, got.EarnedVia)
				assert.False(t, got.EarnedAt.IsZero())
			},
		},
		{
			name:          "error: skin not found",
			tenantID:      tenantID,
			gcid:          gcid,
			skinID:        uuid.Must(uuid.NewV7()),
			earnedVia:     SkinEarnedViaTopicMastery,
			earnedContext: map[string]interface{}{},
			setupMocks: func(d testSkinDeps) {
				d.skinR.On("GetByID", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(nil, nil)
			},
			wantErr: ErrSkinNotFound,
		},
		{
			name:      "error: already owned",
			tenantID:  tenantID,
			gcid:      gcid,
			skinID:    skinID,
			earnedVia: SkinEarnedViaStreak,
			earnedContext: map[string]interface{}{
				"streak_length": 30,
			},
			setupMocks: func(d testSkinDeps) {
				d.skinR.On("GetByID", mock.Anything, skinID).Return(&DigitalSkin{
					ID:       skinID,
					SkinCode: "SKN-FLAME-001",
				}, nil)
				d.awardR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.SkinAward")).Return(ErrSkinAlreadyOwned)
			},
			wantErr: ErrSkinAlreadyOwned,
		},
		{
			name:          "error: invalid earned_via",
			tenantID:      tenantID,
			gcid:          gcid,
			skinID:        skinID,
			earnedVia:     SkinEarnedVia("purchased"),
			earnedContext: map[string]interface{}{},
			wantErr:       ErrValidationFailed,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSkinService()
			if tc.setupMocks != nil {
				tc.setupMocks(d)
			}

			got, err := d.svc.AwardSkin(context.Background(), tc.tenantID, tc.gcid, tc.skinID, tc.earnedVia, tc.earnedContext)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
				return
			}
			assert.NoError(t, err)
			if tc.assert != nil {
				tc.assert(t, got)
			}
			d.skinR.AssertExpectations(t)
			d.awardR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestEquipSkin
// ---------------------------------------------------------------------------

func TestEquipSkin(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	skinAwardID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name        string
		tenantID    uuid.UUID
		gcid        uuid.UUID
		slot        EquipmentSlot
		skinAwardID uuid.UUID
		setupMocks  func(d testSkinDeps)
		wantErr     error
		assert      func(t *testing.T, got *SkinEquipment)
	}{
		{
			name:        "success: equips skin to profile_photo slot",
			tenantID:    tenantID,
			gcid:        gcid,
			slot:        EquipmentSlotProfilePhoto,
			skinAwardID: skinAwardID,
			setupMocks: func(d testSkinDeps) {
				d.equipR.On("EquipSlot", mock.Anything, mock.AnythingOfType("*gamification.SkinEquipment")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)
			},
			assert: func(t *testing.T, got *SkinEquipment) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, tenantID, got.TenantID)
				assert.Equal(t, gcid, got.GCID)
				assert.Equal(t, EquipmentSlotProfilePhoto, got.Slot)
				assert.Equal(t, skinAwardID, got.SkinAwardID)
				assert.False(t, got.EquippedAt.IsZero())
			},
		},
		{
			name:        "success: equips skin to zoom_background slot",
			tenantID:    tenantID,
			gcid:        gcid,
			slot:        EquipmentSlotZoomBackground,
			skinAwardID: skinAwardID,
			setupMocks: func(d testSkinDeps) {
				d.equipR.On("EquipSlot", mock.Anything, mock.AnythingOfType("*gamification.SkinEquipment")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)
			},
			assert: func(t *testing.T, got *SkinEquipment) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, EquipmentSlotZoomBackground, got.Slot)
			},
		},
		{
			name:        "error: invalid slot",
			tenantID:    tenantID,
			gcid:        gcid,
			slot:        EquipmentSlot("helmet"),
			skinAwardID: skinAwardID,
			wantErr:     ErrValidationFailed,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSkinService()
			if tc.setupMocks != nil {
				tc.setupMocks(d)
			}

			got, err := d.svc.EquipSkin(context.Background(), tc.tenantID, tc.gcid, tc.slot, tc.skinAwardID)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
				return
			}
			assert.NoError(t, err)
			if tc.assert != nil {
				tc.assert(t, got)
			}
			d.equipR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestUnequipSkin
// ---------------------------------------------------------------------------

func TestUnequipSkin(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		tenantID   uuid.UUID
		gcid       uuid.UUID
		slot       EquipmentSlot
		setupMocks func(d testSkinDeps)
		wantErr    error
	}{
		{
			name:     "success: unequips skin from slot",
			tenantID: tenantID,
			gcid:     gcid,
			slot:     EquipmentSlotProfilePhoto,
			setupMocks: func(d testSkinDeps) {
				d.equipR.On("UnequipSlot", mock.Anything, tenantID, gcid, EquipmentSlotProfilePhoto).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSkinService()
			if tc.setupMocks != nil {
				tc.setupMocks(d)
			}

			err := d.svc.UnequipSkin(context.Background(), tc.tenantID, tc.gcid, tc.slot)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}
			assert.NoError(t, err)
			d.equipR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetEquipment
// ---------------------------------------------------------------------------

func TestGetEquipment(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		tenantID   uuid.UUID
		gcid       uuid.UUID
		setupMocks func(d testSkinDeps)
		wantCount  int
	}{
		{
			name:     "success: returns all equipped skins",
			tenantID: tenantID,
			gcid:     gcid,
			setupMocks: func(d testSkinDeps) {
				d.equipR.On("GetByGCID", mock.Anything, tenantID, gcid).Return([]*SkinEquipment{
					{ID: uuid.Must(uuid.NewV7()), Slot: EquipmentSlotProfilePhoto},
					{ID: uuid.Must(uuid.NewV7()), Slot: EquipmentSlotZoomBackground},
				}, nil)
			},
			wantCount: 2,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSkinService()
			if tc.setupMocks != nil {
				tc.setupMocks(d)
			}

			got, err := d.svc.GetEquipment(context.Background(), tc.tenantID, tc.gcid)
			assert.NoError(t, err)
			assert.Len(t, got, tc.wantCount)
			d.equipR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestExportSkin
// ---------------------------------------------------------------------------

func TestExportSkin(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())
	skinAwardID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name        string
		tenantID    uuid.UUID
		gcid        uuid.UUID
		skinAwardID uuid.UUID
		target      ExportTarget
		setupMocks  func(d testSkinDeps)
		wantErr     error
		assert      func(t *testing.T, got *SkinExport)
	}{
		{
			name:        "success: exports to Zoom target with pending status",
			tenantID:    tenantID,
			gcid:        gcid,
			skinAwardID: skinAwardID,
			target:      ExportTargetZoom,
			setupMocks: func(d testSkinDeps) {
				d.exportR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.SkinExport")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)
			},
			assert: func(t *testing.T, got *SkinExport) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, tenantID, got.TenantID)
				assert.Equal(t, gcid, got.GCID)
				assert.Equal(t, skinAwardID, got.SkinAwardID)
				assert.Equal(t, ExportTargetZoom, got.ExportTarget)
				assert.Equal(t, ExportStatusPending, got.ExportStatus)
				assert.False(t, got.ExportedAt.IsZero())
			},
		},
		{
			name:        "success: exports to Open Badges target",
			tenantID:    tenantID,
			gcid:        gcid,
			skinAwardID: skinAwardID,
			target:      ExportTargetOpenBadges,
			setupMocks: func(d testSkinDeps) {
				d.exportR.On("Create", mock.Anything, mock.AnythingOfType("*gamification.SkinExport")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)
			},
			assert: func(t *testing.T, got *SkinExport) {
				assert.NotEqual(t, uuid.Nil, got.ID)
				assert.Equal(t, ExportTargetOpenBadges, got.ExportTarget)
				assert.Equal(t, ExportStatusPending, got.ExportStatus)
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSkinService()
			if tc.setupMocks != nil {
				tc.setupMocks(d)
			}

			got, err := d.svc.ExportSkin(context.Background(), tc.tenantID, tc.gcid, tc.skinAwardID, tc.target)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
				return
			}
			assert.NoError(t, err)
			if tc.assert != nil {
				tc.assert(t, got)
			}
			d.exportR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListExports
// ---------------------------------------------------------------------------

func TestListExports(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		tenantID   uuid.UUID
		gcid       uuid.UUID
		offset     int
		limit      int
		setupMocks func(d testSkinDeps)
		wantCount  int
	}{
		{
			name:     "success: returns exports paginated",
			tenantID: tenantID,
			gcid:     gcid,
			offset:   0,
			limit:    20,
			setupMocks: func(d testSkinDeps) {
				d.exportR.On("ListByGCID", mock.Anything, tenantID, gcid, 0, 20).Return([]*SkinExport{
					{ID: uuid.Must(uuid.NewV7()), ExportTarget: ExportTargetZoom},
				}, nil)
			},
			wantCount: 1,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSkinService()
			if tc.setupMocks != nil {
				tc.setupMocks(d)
			}

			got, err := d.svc.ListExports(context.Background(), tc.tenantID, tc.gcid, tc.offset, tc.limit)
			assert.NoError(t, err)
			assert.Len(t, got, tc.wantCount)
			d.exportR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListAwardsByGCID
// ---------------------------------------------------------------------------

func TestListAwardsByGCID(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	gcid := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		tenantID   uuid.UUID
		gcid       uuid.UUID
		offset     int
		limit      int
		setupMocks func(d testSkinDeps)
		wantCount  int
	}{
		{
			name:     "success: returns awards paginated",
			tenantID: tenantID,
			gcid:     gcid,
			offset:   0,
			limit:    20,
			setupMocks: func(d testSkinDeps) {
				d.awardR.On("ListByGCID", mock.Anything, tenantID, gcid, 0, 20).Return([]*SkinAward{
					{ID: uuid.Must(uuid.NewV7()), EarnedVia: SkinEarnedViaStreak},
					{ID: uuid.Must(uuid.NewV7()), EarnedVia: SkinEarnedViaLeagueWin},
				}, nil)
			},
			wantCount: 2,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSkinService()
			if tc.setupMocks != nil {
				tc.setupMocks(d)
			}

			got, err := d.svc.ListAwardsByGCID(context.Background(), tc.tenantID, tc.gcid, tc.offset, tc.limit)
			assert.NoError(t, err)
			assert.Len(t, got, tc.wantCount)
			d.awardR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestTransferLegendarySkin
// ---------------------------------------------------------------------------

func TestTransferLegendarySkin(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	skinID := uuid.Must(uuid.NewV7())
	currentHolder := uuid.Must(uuid.NewV7())
	newHolder := uuid.Must(uuid.NewV7())

	tests := []struct {
		name          string
		tenantID      uuid.UUID
		skinID        uuid.UUID
		newHolderGCID uuid.UUID
		reason        string
		setupMocks    func(d testSkinDeps)
		wantErr       error
		assert        func(t *testing.T, got *LegendarySkin)
	}{
		{
			name:          "success: transfers to new holder",
			tenantID:      tenantID,
			skinID:        skinID,
			newHolderGCID: newHolder,
			reason:        "Weekly league champion",
			setupMocks: func(d testSkinDeps) {
				d.legendaryR.On("GetBySkinID", mock.Anything, tenantID, skinID).Return(&LegendarySkin{
					ID:                uuid.Must(uuid.NewV7()),
					TenantID:          tenantID,
					SkinID:            skinID,
					CurrentHolderGCID: currentHolder,
					HeldSince:         time.Now().Add(-7 * 24 * time.Hour),
					TransferReason:    "Previous champion",
					PreviousHolders:   []map[string]interface{}{},
				}, nil)
				d.legendaryR.On("TransferHolder", mock.Anything, mock.AnythingOfType("*gamification.LegendarySkin")).Return(nil)
				d.publisher.On("Publish", mock.Anything, TopicGamificationEvents, mock.Anything).Return(nil)
			},
			assert: func(t *testing.T, got *LegendarySkin) {
				assert.Equal(t, newHolder, got.CurrentHolderGCID)
				assert.Equal(t, "Weekly league champion", got.TransferReason)
				assert.Len(t, got.PreviousHolders, 1)
				assert.Equal(t, currentHolder.String(), got.PreviousHolders[0]["gcid"])
			},
		},
		{
			name:          "error: skin not legendary",
			tenantID:      tenantID,
			skinID:        uuid.Must(uuid.NewV7()),
			newHolderGCID: newHolder,
			reason:        "Attempted transfer of non-legendary skin",
			setupMocks: func(d testSkinDeps) {
				d.legendaryR.On("GetBySkinID", mock.Anything, tenantID, mock.AnythingOfType("uuid.UUID")).Return(nil, nil)
			},
			wantErr: ErrSkinNotFound,
		},
		{
			name:          "error: new holder same as current",
			tenantID:      tenantID,
			skinID:        skinID,
			newHolderGCID: currentHolder,
			reason:        "Self-transfer should be rejected",
			setupMocks: func(d testSkinDeps) {
				d.legendaryR.On("GetBySkinID", mock.Anything, tenantID, skinID).Return(&LegendarySkin{
					ID:                uuid.Must(uuid.NewV7()),
					TenantID:          tenantID,
					SkinID:            skinID,
					CurrentHolderGCID: currentHolder,
					HeldSince:         time.Now().Add(-7 * 24 * time.Hour),
					TransferReason:    "Previous champion",
					PreviousHolders:   []map[string]interface{}{},
				}, nil)
			},
			wantErr: ErrValidationFailed,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSkinService()
			if tc.setupMocks != nil {
				tc.setupMocks(d)
			}

			got, err := d.svc.TransferLegendarySkin(context.Background(), tc.tenantID, tc.skinID, tc.newHolderGCID, tc.reason)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
				return
			}
			assert.NoError(t, err)
			if tc.assert != nil {
				tc.assert(t, got)
			}
			d.legendaryR.AssertExpectations(t)
			d.publisher.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetLegendarySkin
// ---------------------------------------------------------------------------

func TestGetLegendarySkin(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())
	skinID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		tenantID   uuid.UUID
		skinID     uuid.UUID
		setupMocks func(d testSkinDeps)
		wantErr    error
		assert     func(t *testing.T, got *LegendarySkin)
	}{
		{
			name:     "success: retrieves legendary skin holder",
			tenantID: tenantID,
			skinID:   skinID,
			setupMocks: func(d testSkinDeps) {
				d.legendaryR.On("GetBySkinID", mock.Anything, tenantID, skinID).Return(&LegendarySkin{
					ID:                uuid.Must(uuid.NewV7()),
					TenantID:          tenantID,
					SkinID:            skinID,
					CurrentHolderGCID: uuid.Must(uuid.NewV7()),
				}, nil)
			},
			assert: func(t *testing.T, got *LegendarySkin) {
				assert.Equal(t, skinID, got.SkinID)
				assert.NotEqual(t, uuid.Nil, got.CurrentHolderGCID)
			},
		},
		{
			name:     "error: legendary skin not found",
			tenantID: tenantID,
			skinID:   uuid.Must(uuid.NewV7()),
			setupMocks: func(d testSkinDeps) {
				d.legendaryR.On("GetBySkinID", mock.Anything, tenantID, mock.AnythingOfType("uuid.UUID")).Return(nil, nil)
			},
			wantErr: ErrSkinNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSkinService()
			if tc.setupMocks != nil {
				tc.setupMocks(d)
			}

			got, err := d.svc.GetLegendarySkin(context.Background(), tc.tenantID, tc.skinID)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
				return
			}
			assert.NoError(t, err)
			if tc.assert != nil {
				tc.assert(t, got)
			}
			d.legendaryR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// TestListLegendarySkins
// ---------------------------------------------------------------------------

func TestListLegendarySkins(t *testing.T) {
	t.Parallel()

	tenantID := uuid.Must(uuid.NewV7())

	tests := []struct {
		name       string
		tenantID   uuid.UUID
		offset     int
		limit      int
		setupMocks func(d testSkinDeps)
		wantCount  int
	}{
		{
			name:     "success: returns legendary skins paginated",
			tenantID: tenantID,
			offset:   0,
			limit:    20,
			setupMocks: func(d testSkinDeps) {
				d.legendaryR.On("List", mock.Anything, tenantID, 0, 20).Return([]*LegendarySkin{
					{ID: uuid.Must(uuid.NewV7()), SkinID: uuid.Must(uuid.NewV7())},
				}, nil)
			},
			wantCount: 1,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestSkinService()
			if tc.setupMocks != nil {
				tc.setupMocks(d)
			}

			got, err := d.svc.ListLegendarySkins(context.Background(), tc.tenantID, tc.offset, tc.limit)
			assert.NoError(t, err)
			assert.Len(t, got, tc.wantCount)
			d.legendaryR.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// Compile-time verification: ensure test file references real domain types.
// These are not tests themselves but verify the test imports compile correctly.
// ---------------------------------------------------------------------------

func TestSkinServiceTestInfrastructure(t *testing.T) {
	t.Parallel()

	// Verify SkinService can be constructed with all required dependencies.
	d := newTestSkinService()
	assert.NotNil(t, d.svc, "SkinService should be constructable")
	assert.NotNil(t, d.skinR, "mockSkinRepo should exist")
	assert.NotNil(t, d.awardR, "mockAwardRepo should exist")
	assert.NotNil(t, d.equipR, "mockEquipmentRepo should exist")
	assert.NotNil(t, d.exportR, "mockExportRepo should exist")
	assert.NotNil(t, d.legendaryR, "mockLegendaryRepo should exist")
	assert.NotNil(t, d.publisher, "mockEventPublisher should exist")

	// Verify domain type constants are accessible from tests.
	assert.True(t, SkinRarityEpic.IsValid())
	assert.True(t, SkinEarnedViaStreak.IsValid())
	assert.True(t, EquipmentSlotProfilePhoto.IsValid())
	assert.True(t, ExportTargetZoom.IsValid())

	// Verify event constants exist.
	assert.Equal(t, "skin.created", EventSkinCreated)
	assert.Equal(t, "skin.awarded", EventSkinAwarded)
	assert.Equal(t, "skin.equipped", EventSkinEquipped)
	assert.Equal(t, "skin.exported", EventSkinExported)
	assert.Equal(t, "legendary.transferred", EventLegendaryTransferred)

	// Verify error sentinels exist.
	assert.NotNil(t, ErrSkinNotFound)
	assert.NotNil(t, ErrSkinAlreadyOwned)
	assert.NotNil(t, ErrValidationFailed)

	// Verify entity fields are accessible (compile-time type check).
	skin := &DigitalSkin{
		SkinCode: "test",
		Rarity:   SkinRarityCommon,
	}
	assert.Equal(t, "test", skin.SkinCode)

	award := &SkinAward{
		EarnedVia: SkinEarnedViaStreak,
		EarnedAt:  time.Now().UTC(),
	}
	assert.Equal(t, SkinEarnedViaStreak, award.EarnedVia)

	equip := &SkinEquipment{
		Slot: EquipmentSlotZoomOverlay,
	}
	assert.Equal(t, EquipmentSlotZoomOverlay, equip.Slot)
}
