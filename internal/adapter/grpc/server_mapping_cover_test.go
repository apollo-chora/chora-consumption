// server_mapping_cover_test.go — white-box unit tests for the pure mapping +
// error-translation helpers and the documented Unimplemented contract of the
// three Wave-1 contract-registration servers.
//
// stateToProto + mapDomainError are pure functions (no I/O); the three
// New*Server constructors embed the generated Unimplemented*Server so that
// un-wired RPCs fail-loud with codes.Unimplemented (feedback_no_stubs_real_
// wiring). The tests below assert that fail-loud contract directly rather than
// the trivial "constructor returns non-nil" — a regression that swaps the
// embedded type (e.g. to a silent stub) would be caught here.
package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"
)

// ----- stateToProto -----

func TestStateToProto_Nil(t *testing.T) {
	assert.Nil(t, stateToProto(nil))
}

func TestStateToProto_FullState(t *testing.T) {
	aha := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	hatched := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	stageUp := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)

	st := &growth.State{
		CompanionID:            "fam-1",
		GrowthStage:            3,
		StageName:              "awakened",
		Species:                "dragon",
		ShinyVariant:           true,
		Rarity:                 "legendary",
		ExpCurrent:             10,
		ExpNextThreshold:       100,
		ExpCumulative:          250,
		EffectiveLLMTier:       "pro",
		EffectiveMaxOutputToks: 2000,
		UnlockedTools:          []string{"calculator", "web_search"},
		ResonantAtomID:         "atom-9",
		AhaMomentConsumed:      true,
		AhaMomentActiveUntil:   &aha,
		HatchedAt:              &hatched,
		LastStageUpAt:          &stageUp,
	}

	got := stateToProto(st)
	require.NotNil(t, got)
	assert.Equal(t, "fam-1", got.CompanionId)
	assert.Equal(t, int32(3), got.GrowthStage)
	assert.Equal(t, "awakened", got.StageName)
	assert.Equal(t, "dragon", got.Species)
	assert.True(t, got.ShinyVariant)
	assert.Equal(t, "legendary", got.Rarity)
	assert.Equal(t, int32(10), got.ExpCurrent)
	assert.Equal(t, int32(100), got.ExpNextThreshold)
	assert.Equal(t, int32(250), got.ExpCumulative)
	assert.Equal(t, "pro", got.EffectiveLlmTier)
	assert.Equal(t, int32(2000), got.EffectiveMaxOutputTokens)
	assert.Equal(t, []string{"calculator", "web_search"}, got.UnlockedTools)
	assert.Equal(t, "atom-9", got.ResonantAtomId)
	assert.True(t, got.AhaMomentConsumed)
	// Pointer-timestamp fields are set.
	require.NotNil(t, got.AhaMomentActiveUntil)
	assert.Equal(t, aha.Unix(), got.AhaMomentActiveUntil.AsTime().Unix())
	require.NotNil(t, got.HatchedAt)
	assert.Equal(t, hatched.Unix(), got.HatchedAt.AsTime().Unix())
	require.NotNil(t, got.LastStageUpAt)
	assert.Equal(t, stageUp.Unix(), got.LastStageUpAt.AsTime().Unix())
	// visible_kg_neighbors: RETIRED (R3-1, CHO-2013 P1) — always empty.
	assert.Empty(t, got.VisibleKgNeighbors)
}

func TestStateToProto_NilTimestampsLeftUnset(t *testing.T) {
	got := stateToProto(&growth.State{CompanionID: "fam-2", GrowthStage: 0})
	require.NotNil(t, got)
	assert.Nil(t, got.AhaMomentActiveUntil)
	assert.Nil(t, got.HatchedAt)
	assert.Nil(t, got.LastStageUpAt)
}

// ----- mapDomainError -----

func TestMapDomainError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"not-found", growth.ErrCompanionNotFound, codes.NotFound},
		{"already-hatched", growth.ErrAlreadyHatched, codes.FailedPrecondition},
		{"not-in-egg-stage", growth.ErrNotInEggStage, codes.FailedPrecondition},
		{"aha-consumed", growth.ErrAhaMomentConsumed, codes.FailedPrecondition},
		{"not-bound", growth.ErrCompanionNotBound, codes.FailedPrecondition},
		{"not-hatched", growth.ErrCompanionNotHatched, codes.FailedPrecondition},
		{"invalid-args", growth.ErrInvalidArguments, codes.InvalidArgument},
		{"invalid-source", growth.ErrInvalidSource, codes.InvalidArgument},
		{"unknown", errors.New("boom"), codes.Internal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := mapDomainError(tc.err)
			assert.Equal(t, tc.want, codeOf(t, err))
		})
	}
}

func TestMapDomainError_WrappedSentinel(t *testing.T) {
	// errors.Is walks the wrap chain, so a wrapped sentinel still maps — this
	// matches how the domain service returns fmt.Errorf("...: %w", sentinel).
	wrapped := wrapErr(growth.ErrCompanionNotFound)
	assert.Equal(t, codes.NotFound, codeOf(t, mapDomainError(wrapped)))
}

func wrapErr(e error) error { return errWrap{e} }

type errWrap struct{ inner error }

func (w errWrap) Error() string { return "wrapped: " + w.inner.Error() }
func (w errWrap) Unwrap() error { return w.inner }

// ----- Unimplemented contract of Wave-1 contract-registration servers -----

// TestUnimplementedSurfaces_FailLoud asserts the documented fail-loud
// contract: because each server embeds the generated Unimplemented*Server,
// calling an un-wired RPC returns codes.Unimplemented (NOT a silent stub).
func TestUnimplementedSurfaces_FailLoud(t *testing.T) {
	t.Run("Consumption", func(t *testing.T) {
		s := NewConsumptionServer(nil)
		require.NotNil(t, s)
		// StartSession is contract-registered but unwired → Unimplemented.
		_, err := s.StartSession(context.Background(), &consumptionv1.StartSessionRequest{})
		assert.Equal(t, codes.Unimplemented, codeOf(t, err))
	})

	t.Run("KnowledgeGraph", func(t *testing.T) {
		s := NewKnowledgeGraphServer()
		require.NotNil(t, s)
		_, err := s.GetMapCluster(context.Background(), &consumptionv1.GetMapClusterRequest{})
		assert.Equal(t, codes.Unimplemented, codeOf(t, err))
	})
}
