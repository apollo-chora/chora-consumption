package engagement

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestComboTier_ComboMultiplier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tier ComboTier
		want int
	}{
		{ComboTierBase, 1},
		{ComboTierBronze, 2},
		{ComboTierSilver, 3},
		{ComboTierGold, 4},
	}

	for _, tt := range tests {
		t.Run(string(tt.tier), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.tier.ComboMultiplier())
		})
	}
}

func TestComboTier_NextTier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tier ComboTier
		want ComboTier
	}{
		{ComboTierBase, ComboTierBronze},
		{ComboTierBronze, ComboTierSilver},
		{ComboTierSilver, ComboTierGold},
		{ComboTierGold, ComboTierGold},
	}

	for _, tt := range tests {
		t.Run(string(tt.tier), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.tier.NextTier())
		})
	}
}

func TestLevelForXP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		totalXP int
		want    int
	}{
		{"zero XP is level 1", 0, 1},
		{"99 XP is still level 1", 99, 1},
		{"100 XP reaches level 2", 100, 2},
		{"299 XP is still level 2", 299, 2},
		{"300 XP reaches level 3", 300, 3},
		{"599 XP is still level 3", 599, 3},
		{"600 XP reaches level 4", 600, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, LevelForXP(tt.totalXP))
		})
	}
}

func TestXPForNextLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		totalXP int
		want    int
	}{
		{"0 XP needs 100 for level 2", 0, 100},
		{"99 XP needs 1 for level 2", 99, 1},
		{"100 XP needs 200 for level 3", 100, 200},
		{"150 XP needs 150 for level 3", 150, 150},
		{"300 XP needs 300 for level 4", 300, 300},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, XPForNextLevel(tt.totalXP))
		})
	}
}

func TestStreakMilestones_are_defined(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 200, StreakMilestones[7])
	assert.Equal(t, 500, StreakMilestones[30])
	assert.Equal(t, 1000, StreakMilestones[100])
	assert.Equal(t, 5000, StreakMilestones[365])
	assert.Len(t, StreakMilestones, 4)
}
