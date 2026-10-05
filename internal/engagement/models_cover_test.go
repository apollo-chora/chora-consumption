package engagement

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// RevealStatus.IsValid — 0% before, exercises every switch arm + default.
// ---------------------------------------------------------------------------

func TestRevealStatus_IsValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status RevealStatus
		want   bool
	}{
		{"hidden is valid", RevealStatusHidden, true},
		{"revealed is valid", RevealStatusRevealed, true},
		{"conquered is valid", RevealStatusConquered, true},
		{"empty is invalid", RevealStatus(""), false},
		{"unknown is invalid", RevealStatus("exploded"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.status.IsValid())
		})
	}
}

// ---------------------------------------------------------------------------
// NextStarTier — Silver/Gold/Platinum arms were uncovered (only Bronze and
// the diamond default were hit indirectly via GetLevelProgress). This locks
// the full tier ladder + the required-level mapping.
// ---------------------------------------------------------------------------

func TestNextStarTier_Ladder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		current  StarTier
		wantNext StarTier
		wantLvl  int
	}{
		{"bronze -> silver at 5", StarTierBronze, StarTierSilver, 5},
		{"silver -> gold at 10", StarTierSilver, StarTierGold, 10},
		{"gold -> platinum at 15", StarTierGold, StarTierPlatinum, 15},
		{"platinum -> diamond at 20", StarTierPlatinum, StarTierDiamond, 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			next, lvl := NextStarTier(tt.current)
			require.NotNil(t, next)
			require.NotNil(t, lvl)
			assert.Equal(t, tt.wantNext, *next)
			assert.Equal(t, tt.wantLvl, *lvl)
		})
	}
}

func TestNextStarTier_DiamondIsMax(t *testing.T) {
	t.Parallel()

	next, lvl := NextStarTier(StarTierDiamond)
	assert.Nil(t, next)
	assert.Nil(t, lvl)
}

func TestNextStarTier_UnknownTierHitsDefault(t *testing.T) {
	t.Parallel()

	// An unrecognized tier falls through the switch to the default arm,
	// which returns (nil, nil) — same as diamond (no "next").
	next, lvl := NextStarTier(StarTier("mythical"))
	assert.Nil(t, next)
	assert.Nil(t, lvl)
}
