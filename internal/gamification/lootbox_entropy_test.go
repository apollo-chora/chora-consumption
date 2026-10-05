package gamification

import (
	"context"
	crand "crypto/rand"
	"errors"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Entropy stubs
//
// gosec G404 flagged lootbox_service.go for drawing loot from math/rand. Loot
// is real economic value in the three-currency economy (XP / Coins /
// Reputation), so a predictable draw is a farmable draw. These tests pin the
// crypto/rand replacement AND the fail-loud contract that goes with it: a
// secure source can fail, and when it does the learner must not receive loot.
// ---------------------------------------------------------------------------

// errEntropy always fails. It stands in for a crypto/rand.Reader that cannot
// serve bytes.
type errEntropy struct{ err error }

func (r errEntropy) Read(p []byte) (int, error) { return 0, r.err }

// zeroEntropy always yields 0x00 bytes, which crypto/rand.Int maps to the
// value 0 for any positive bound. It makes the cumulative bucket walk
// assertable without reimplementing rejection sampling in the test.
type zeroEntropy struct{}

func (zeroEntropy) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

var errEntropyDrained = errors.New("entropy pool drained")

func lootEntry(weight, qtyMin, qtyMax int, item LootItemType) *LootTable {
	return &LootTable{
		ID:          uuid.New(),
		TenantID:    uuid.New(),
		Tier:        "standard",
		ItemType:    item,
		QuantityMin: qtyMin,
		QuantityMax: qtyMax,
		Weight:      weight,
	}
}

// ---------------------------------------------------------------------------
// weightedSelect
// ---------------------------------------------------------------------------

func TestWeightedSelect_EntropyFailure_FailsLoud(t *testing.T) {
	t.Parallel()

	entries := []*LootTable{
		lootEntry(70, 10, 50, LootItemCoins),
		lootEntry(30, 25, 100, LootItemXP),
	}

	got, err := weightedSelect(entries, errEntropy{err: errEntropyDrained})

	require.Error(t, err, "a failed secure draw must surface, never fall back to a weak source")
	assert.Nil(t, got)
	assert.ErrorIs(t, err, errEntropyDrained)
}

func TestWeightedSelect_ZeroTotalWeight_ReturnsErrorNotPanic(t *testing.T) {
	t.Parallel()

	// A tenant loot table whose entries all carry weight 0. The math/rand
	// implementation called rand.Intn(0), which panics and takes the request
	// down. A misconfigured loot table is a 4xx-shaped condition, not a crash.
	entries := []*LootTable{
		lootEntry(0, 1, 1, LootItemCoins),
		lootEntry(0, 1, 1, LootItemXP),
	}

	require.NotPanics(t, func() {
		got, err := weightedSelect(entries, crand.Reader)
		require.Error(t, err)
		assert.Nil(t, got)
		assert.ErrorIs(t, err, ErrLootTableWeightsInvalid)
	})
}

func TestWeightedSelect_NegativeWeight_ReturnsError(t *testing.T) {
	t.Parallel()

	// A negative weight silently shrinks the draw range under the old
	// cumulative walk and can make an entry unreachable. Reject it.
	entries := []*LootTable{
		lootEntry(-5, 1, 1, LootItemCoins),
		lootEntry(10, 1, 1, LootItemXP),
	}

	got, err := weightedSelect(entries, crand.Reader)
	require.Error(t, err)
	assert.Nil(t, got)
	assert.ErrorIs(t, err, ErrLootTableWeightsInvalid)
}

func TestWeightedSelect_ZeroDrawSelectsFirstBucket(t *testing.T) {
	t.Parallel()

	first := lootEntry(1, 1, 1, LootItemCoins)
	second := lootEntry(999, 1, 1, LootItemXP)

	got, err := weightedSelect([]*LootTable{first, second}, zeroEntropy{})
	require.NoError(t, err)
	assert.Same(t, first, got, "draw 0 falls in the first cumulative bucket")
}

func TestWeightedSelect_HonoursWeights(t *testing.T) {
	t.Parallel()

	light := lootEntry(1, 1, 1, LootItemCoins)
	heavy := lootEntry(99, 1, 1, LootItemXP)
	entries := []*LootTable{light, heavy}

	counts := map[uuid.UUID]int{}
	const draws = 3000
	for i := 0; i < draws; i++ {
		got, err := weightedSelect(entries, crand.Reader)
		require.NoError(t, err)
		counts[got.ID]++
	}

	assert.Greater(t, counts[heavy.ID], counts[light.ID],
		"the 99-weight entry must dominate the 1-weight entry")
	assert.Positive(t, counts[light.ID],
		"the 1-weight entry must still be reachable")
	assert.Equal(t, draws, counts[heavy.ID]+counts[light.ID])
}

// ---------------------------------------------------------------------------
// rollQuantity
// ---------------------------------------------------------------------------

func TestRollQuantity_EntropyFailure_FailsLoud(t *testing.T) {
	t.Parallel()

	got, err := rollQuantity(errEntropy{err: errEntropyDrained}, 10, 50)

	require.Error(t, err)
	assert.Zero(t, got)
	assert.ErrorIs(t, err, errEntropyDrained)
}

func TestRollQuantity_FixedRangeNeedsNoDraw(t *testing.T) {
	t.Parallel()

	// min == max leaves nothing to draw, so a dead entropy source must not
	// stop a deterministic reward being handed out.
	got, err := rollQuantity(errEntropy{err: errEntropyDrained}, 7, 7)
	require.NoError(t, err)
	assert.Equal(t, 7, got)
}

func TestRollQuantity_StaysWithinInclusiveRange(t *testing.T) {
	t.Parallel()

	seen := map[int]bool{}
	for i := 0; i < 500; i++ {
		got, err := rollQuantity(crand.Reader, 10, 12)
		require.NoError(t, err)
		require.GreaterOrEqual(t, got, 10)
		require.LessOrEqual(t, got, 12)
		seen[got] = true
	}

	assert.True(t, seen[10] && seen[11] && seen[12],
		"the range is inclusive at both ends, got %v", seen)
}

// ---------------------------------------------------------------------------
// Open: end to end fail-loud
// ---------------------------------------------------------------------------

func TestLootbox_Open_EntropyFailure_LeavesLootboxUnopened(t *testing.T) {
	t.Parallel()

	lr := &mockLootboxRepo{}
	tr := &mockLootTableRepo{}
	ep := &mockEventPublisher{}
	svc := NewLootboxService(lr, tr, ep)
	svc.entropy = errEntropy{err: errEntropyDrained}

	lootboxID := uuid.New()
	tenantID := uuid.New()
	lootbox := &Lootbox{
		ID: lootboxID, TenantID: tenantID, GCID: uuid.New(),
		LootboxTier: LootboxTierStandard, Source: LootboxSourceLevelUp,
	}
	entries := []*LootTable{lootEntry(70, 10, 50, LootItemCoins)}

	lr.On("GetByID", mock.Anything, lootboxID).Return(lootbox, nil)
	tr.On("ListByTier", mock.Anything, tenantID, "standard").Return(entries, nil)

	result, err := svc.Open(context.Background(), lootboxID)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.False(t, lootbox.IsOpened, "an undrawn lootbox must not be burned")
	assert.Nil(t, lootbox.OpenedAt)
	lr.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
	ep.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything, mock.Anything)
}

func TestLootbox_Open_InvalidWeights_FailsLoud(t *testing.T) {
	t.Parallel()

	lr := &mockLootboxRepo{}
	tr := &mockLootTableRepo{}
	ep := &mockEventPublisher{}
	svc := NewLootboxService(lr, tr, ep)

	lootboxID := uuid.New()
	tenantID := uuid.New()
	lootbox := &Lootbox{
		ID: lootboxID, TenantID: tenantID, GCID: uuid.New(),
		LootboxTier: LootboxTierStandard, Source: LootboxSourceLevelUp,
	}
	entries := []*LootTable{lootEntry(0, 1, 1, LootItemCoins)}

	lr.On("GetByID", mock.Anything, lootboxID).Return(lootbox, nil)
	tr.On("ListByTier", mock.Anything, tenantID, "standard").Return(entries, nil)

	require.NotPanics(t, func() {
		result, err := svc.Open(context.Background(), lootboxID)
		require.ErrorIs(t, err, ErrLootTableWeightsInvalid)
		assert.Nil(t, result)
	})
	assert.False(t, lootbox.IsOpened)
	lr.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
}

func TestLootbox_Open_DefaultEntropyIsCryptoRand(t *testing.T) {
	t.Parallel()

	// The constructor leaves the field nil; production must resolve to
	// crypto/rand.Reader rather than to a zero value that cannot serve bytes.
	svc := NewLootboxService(&mockLootboxRepo{}, &mockLootTableRepo{}, &mockEventPublisher{})
	assert.Nil(t, svc.entropy, "constructor must not pin a source")
	assert.Equal(t, crand.Reader, svc.entropySource())
}

// ---------------------------------------------------------------------------
// Source guard: the gosec G404 finding itself
// ---------------------------------------------------------------------------

func TestLootboxService_DoesNotImportMathRand(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "lootbox_service.go", nil, parser.ImportsOnly)
	require.NoError(t, err)

	for _, imp := range f.Imports {
		path, uerr := strconv.Unquote(imp.Path.Value)
		require.NoError(t, uerr)
		assert.NotEqual(t, "math/rand", path,
			"loot draws are security relevant, gosec G404 stays closed")
		assert.NotEqual(t, "math/rand/v2", path,
			"loot draws are security relevant, gosec G404 stays closed")
	}
}
