package gamification

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSkinRarityIsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value SkinRarity
		want  bool
	}{
		{SkinRarityCommon, true},
		{SkinRarityUncommon, true},
		{SkinRarityRare, true},
		{SkinRarityEpic, true},
		{SkinRarityLegendary, true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.value), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.value.IsValid())
		})
	}
}

func TestSkinEarnedViaIsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value SkinEarnedVia
		want  bool
	}{
		{SkinEarnedViaStreak, true},
		{SkinEarnedViaLeagueWin, true},
		{SkinEarnedViaTopicMastery, true},
		{SkinEarnedViaFeaturedWork, true},
		{SkinEarnedViaInstructorAward, true},
		{SkinEarnedViaCertification, true},
		{SkinEarnedViaLegendaryTransfer, true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.value), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.value.IsValid())
		})
	}
}

func TestEquipmentSlotIsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value EquipmentSlot
		want  bool
	}{
		{EquipmentSlotProfilePhoto, true},
		{EquipmentSlotZoomOverlay, true},
		{EquipmentSlotZoomBackground, true},
		{EquipmentSlotNameBadge, true},
		{EquipmentSlotEmailSignature, true},
		{EquipmentSlotClassProfile, true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.value), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.value.IsValid())
		})
	}
}

func TestExportTargetIsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value ExportTarget
		want  bool
	}{
		{ExportTargetZoom, true},
		{ExportTargetLinkedIn, true},
		{ExportTargetEmail, true},
		{ExportTargetOpenBadges, true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.value), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.value.IsValid())
		})
	}
}

func TestExportStatusIsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value ExportStatus
		want  bool
	}{
		{ExportStatusPending, true},
		{ExportStatusCompleted, true},
		{ExportStatusFailed, true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.value), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.value.IsValid())
		})
	}
}

func TestLeagueTierIsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value LeagueTier
		want  bool
	}{
		{LeagueTierBronze, true},
		{LeagueTierSilver, true},
		{LeagueTierGold, true},
		{LeagueTierPlatinum, true},
		{LeagueTierDiamond, true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.value), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.value.IsValid())
		})
	}
}

func TestBadgeTierIsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value BadgeTier
		want  bool
	}{
		{BadgeTierBronze, true},
		{BadgeTierSilver, true},
		{BadgeTierGold, true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.value), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.value.IsValid())
		})
	}
}

func TestBountyStatusIsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value BountyStatus
		want  bool
	}{
		{BountyStatusOpen, true},
		{BountyStatusInProgress, true},
		{BountyStatusCompleted, true},
		{BountyStatusExpired, true},
		{BountyStatusCancelled, true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.value), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.value.IsValid())
		})
	}
}

func TestCoinTransactionTypeIsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value CoinTransactionType
		want  bool
	}{
		{CoinTransactionTypeEarned, true},
		{CoinTransactionTypeSpent, true},
		{CoinTransactionTypeRefunded, true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.value), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.value.IsValid())
		})
	}
}

func TestBossDifficultyTierIsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value BossDifficultyTier
		want  bool
	}{
		{BossDifficultyNormal, true},
		{BossDifficultyHard, true},
		{BossDifficultyLegendary, true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.value), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.value.IsValid())
		})
	}
}

func TestBossChallengeStatusIsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value BossChallengeStatus
		want  bool
	}{
		{BossChallengeStatusScheduled, true},
		{BossChallengeStatusActive, true},
		{BossChallengeStatusCompleted, true},
		{BossChallengeStatusCancelled, true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.value), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.value.IsValid())
		})
	}
}

func TestMaterialTypeIsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value MaterialType
		want  bool
	}{
		{MaterialTypeCommon, true},
		{MaterialTypeUncommon, true},
		{MaterialTypeRare, true},
		{MaterialTypeEpic, true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.value), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.value.IsValid())
		})
	}
}

func TestRecipeOutputTypeIsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value RecipeOutputType
		want  bool
	}{
		{RecipeOutputMaterial, true},
		{RecipeOutputCard, true},
		{RecipeOutputSkin, true},
		{RecipeOutputCoins, true},
		{RecipeOutputXP, true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.value), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.value.IsValid())
		})
	}
}

func TestLootboxTierIsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value LootboxTier
		want  bool
	}{
		{LootboxTierStandard, true},
		{LootboxTierPremium, true},
		{LootboxTierLegendary, true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.value), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.value.IsValid())
		})
	}
}

func TestLootboxSourceIsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value LootboxSource
		want  bool
	}{
		{LootboxSourceLevelUp, true},
		{LootboxSourceBossReward, true},
		{LootboxSourceDailyLogin, true},
		{LootboxSourcePurchase, true},
		{LootboxSourceEvent, true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.value), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.value.IsValid())
		})
	}
}

func TestLootItemTypeIsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value LootItemType
		want  bool
	}{
		{LootItemCard, true},
		{LootItemMaterial, true},
		{LootItemCoins, true},
		{LootItemXP, true},
		{LootItemSkin, true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.value), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.value.IsValid())
		})
	}
}
