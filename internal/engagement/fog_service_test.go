package engagement

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

type testFogDeps struct {
	svc       *FogService
	repo      *mockDiscoveryStateRepo
	publisher *mockEventPublisher
}

func newTestFogService() testFogDeps {
	r := &mockDiscoveryStateRepo{}
	ep := &mockEventPublisher{}
	return testFogDeps{
		svc:       NewFogService(r, ep),
		repo:      r,
		publisher: ep,
	}
}

// ---------------------------------------------------------------------------
// Tests — RevealNode
// ---------------------------------------------------------------------------

func TestRevealNode_Success(t *testing.T) {
	t.Parallel()
	d := newTestFogService()
	ctx := context.Background()
	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	topicNodeID := uuid.Must(uuid.NewV7())

	// No existing state — node is hidden by default
	d.repo.On("GetByGCIDAndTopicNode", mock.Anything, gcid, tenantID, topicNodeID).Return(nil, nil)
	d.repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.UIDiscoveryState")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicChoraverseEvents, mock.MatchedBy(func(e DomainEvent) bool {
		return e.EventType == EventFogNodeRevealed
	})).Return(nil)

	state, err := d.svc.RevealNode(ctx, gcid, tenantID, topicNodeID)
	require.NoError(t, err)
	assert.Equal(t, RevealStatusRevealed, state.RevealStatus)
	assert.NotNil(t, state.RevealedAt)
	d.repo.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestRevealNode_AlreadyRevealed(t *testing.T) {
	t.Parallel()
	d := newTestFogService()
	ctx := context.Background()
	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	topicNodeID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	existing := &UIDiscoveryState{
		ID:           uuid.Must(uuid.NewV7()),
		GCID:         gcid,
		TenantID:     tenantID,
		TopicNodeID:  topicNodeID,
		RevealStatus: RevealStatusRevealed,
		RevealedAt:   &now,
	}

	d.repo.On("GetByGCIDAndTopicNode", mock.Anything, gcid, tenantID, topicNodeID).Return(existing, nil)

	// Idempotent — returns existing state without error
	state, err := d.svc.RevealNode(ctx, gcid, tenantID, topicNodeID)
	require.NoError(t, err)
	assert.Equal(t, RevealStatusRevealed, state.RevealStatus)
}

func TestRevealNode_AlreadyConquered(t *testing.T) {
	t.Parallel()
	d := newTestFogService()
	ctx := context.Background()
	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	topicNodeID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	existing := &UIDiscoveryState{
		ID:           uuid.Must(uuid.NewV7()),
		GCID:         gcid,
		TenantID:     tenantID,
		TopicNodeID:  topicNodeID,
		RevealStatus: RevealStatusConquered,
		RevealedAt:   &now,
		ConqueredAt:  &now,
	}

	d.repo.On("GetByGCIDAndTopicNode", mock.Anything, gcid, tenantID, topicNodeID).Return(existing, nil)

	// Idempotent — returns existing conquered state
	state, err := d.svc.RevealNode(ctx, gcid, tenantID, topicNodeID)
	require.NoError(t, err)
	assert.Equal(t, RevealStatusConquered, state.RevealStatus)
}

// ---------------------------------------------------------------------------
// Tests — ConquerNode
// ---------------------------------------------------------------------------

func TestConquerNode_Success(t *testing.T) {
	t.Parallel()
	d := newTestFogService()
	ctx := context.Background()
	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	topicNodeID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	existing := &UIDiscoveryState{
		ID:           uuid.Must(uuid.NewV7()),
		GCID:         gcid,
		TenantID:     tenantID,
		TopicNodeID:  topicNodeID,
		RevealStatus: RevealStatusRevealed,
		RevealedAt:   &now,
	}

	d.repo.On("GetByGCIDAndTopicNode", mock.Anything, gcid, tenantID, topicNodeID).Return(existing, nil)
	d.repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.UIDiscoveryState")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicChoraverseEvents, mock.MatchedBy(func(e DomainEvent) bool {
		return e.EventType == EventFogNodeConquered
	})).Return(nil)

	state, err := d.svc.ConquerNode(ctx, gcid, tenantID, topicNodeID)
	require.NoError(t, err)
	assert.Equal(t, RevealStatusConquered, state.RevealStatus)
	assert.NotNil(t, state.ConqueredAt)
	d.repo.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestConquerNode_AlreadyConquered(t *testing.T) {
	t.Parallel()
	d := newTestFogService()
	ctx := context.Background()
	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	topicNodeID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	existing := &UIDiscoveryState{
		ID:           uuid.Must(uuid.NewV7()),
		GCID:         gcid,
		TenantID:     tenantID,
		TopicNodeID:  topicNodeID,
		RevealStatus: RevealStatusConquered,
		RevealedAt:   &now,
		ConqueredAt:  &now,
	}

	d.repo.On("GetByGCIDAndTopicNode", mock.Anything, gcid, tenantID, topicNodeID).Return(existing, nil)

	state, err := d.svc.ConquerNode(ctx, gcid, tenantID, topicNodeID)
	require.NoError(t, err)
	assert.Equal(t, RevealStatusConquered, state.RevealStatus)
}

func TestConquerNode_HiddenNodeAutoReveals(t *testing.T) {
	t.Parallel()
	d := newTestFogService()
	ctx := context.Background()
	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	topicNodeID := uuid.Must(uuid.NewV7())

	// No existing state — conquering a hidden node reveals + conquers it
	d.repo.On("GetByGCIDAndTopicNode", mock.Anything, gcid, tenantID, topicNodeID).Return(nil, nil)
	d.repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.UIDiscoveryState")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicChoraverseEvents, mock.Anything).Return(nil)

	state, err := d.svc.ConquerNode(ctx, gcid, tenantID, topicNodeID)
	require.NoError(t, err)
	assert.Equal(t, RevealStatusConquered, state.RevealStatus)
	assert.NotNil(t, state.RevealedAt)
	assert.NotNil(t, state.ConqueredAt)
}

// ---------------------------------------------------------------------------
// Tests — GetDiscoveryStates
// ---------------------------------------------------------------------------

func TestGetDiscoveryStates_Success(t *testing.T) {
	t.Parallel()
	d := newTestFogService()
	ctx := context.Background()
	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	states := []UIDiscoveryState{
		{ID: uuid.Must(uuid.NewV7()), RevealStatus: RevealStatusRevealed},
		{ID: uuid.Must(uuid.NewV7()), RevealStatus: RevealStatusConquered},
	}

	d.repo.On("ListByGCID", mock.Anything, gcid, tenantID).Return(states, nil)

	got, err := d.svc.GetDiscoveryStates(ctx, gcid, tenantID)
	require.NoError(t, err)
	assert.Len(t, got, 2)
	d.repo.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// Tests — HandleAtomCompleted (event handler)
// ---------------------------------------------------------------------------

func TestHandleAtomCompleted_RevealsWhenAboveThreshold(t *testing.T) {
	t.Parallel()
	d := newTestFogService()
	ctx := context.Background()
	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	topicNodeID := uuid.Must(uuid.NewV7())

	// No existing state — node should be revealed
	d.repo.On("GetByGCIDAndTopicNode", mock.Anything, gcid, tenantID, topicNodeID).Return(nil, nil)
	d.repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.UIDiscoveryState")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicChoraverseEvents, mock.Anything).Return(nil)

	err := d.svc.HandleAtomCompleted(ctx, gcid, tenantID, topicNodeID, 0.65, 0.6, 0.8)
	require.NoError(t, err)
	d.repo.AssertExpectations(t)
}

func TestHandleAtomCompleted_ConquersWhenAboveConquerThreshold(t *testing.T) {
	t.Parallel()
	d := newTestFogService()
	ctx := context.Background()
	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	topicNodeID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	existing := &UIDiscoveryState{
		ID:           uuid.Must(uuid.NewV7()),
		GCID:         gcid,
		TenantID:     tenantID,
		TopicNodeID:  topicNodeID,
		RevealStatus: RevealStatusRevealed,
		RevealedAt:   &now,
	}

	d.repo.On("GetByGCIDAndTopicNode", mock.Anything, gcid, tenantID, topicNodeID).Return(existing, nil)
	d.repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.UIDiscoveryState")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicChoraverseEvents, mock.MatchedBy(func(e DomainEvent) bool {
		return e.EventType == EventFogNodeConquered
	})).Return(nil)

	err := d.svc.HandleAtomCompleted(ctx, gcid, tenantID, topicNodeID, 0.85, 0.6, 0.8)
	require.NoError(t, err)
	d.repo.AssertExpectations(t)
	d.publisher.AssertExpectations(t)
}

func TestHandleAtomCompleted_BelowThresholdNoOp(t *testing.T) {
	t.Parallel()
	d := newTestFogService()
	ctx := context.Background()
	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	topicNodeID := uuid.Must(uuid.NewV7())

	// Retention below reveal threshold — no action
	err := d.svc.HandleAtomCompleted(ctx, gcid, tenantID, topicNodeID, 0.3, 0.6, 0.8)
	require.NoError(t, err)
	d.repo.AssertNotCalled(t, "Save")
}

// ---------------------------------------------------------------------------
// Tests — ValidateFogThreshold
// ---------------------------------------------------------------------------

func TestValidateFogThreshold_Valid(t *testing.T) {
	t.Parallel()
	d := newTestFogService()

	assert.NoError(t, d.svc.ValidateFogThreshold(0.1))
	assert.NoError(t, d.svc.ValidateFogThreshold(0.5))
	assert.NoError(t, d.svc.ValidateFogThreshold(1.0))
}

func TestValidateFogThreshold_TooLow(t *testing.T) {
	t.Parallel()
	d := newTestFogService()

	assert.ErrorIs(t, d.svc.ValidateFogThreshold(0.0), ErrFogThresholdInvalid)
	assert.ErrorIs(t, d.svc.ValidateFogThreshold(0.05), ErrFogThresholdInvalid)
	assert.ErrorIs(t, d.svc.ValidateFogThreshold(-0.1), ErrFogThresholdInvalid)
}

func TestValidateFogThreshold_TooHigh(t *testing.T) {
	t.Parallel()
	d := newTestFogService()

	assert.ErrorIs(t, d.svc.ValidateFogThreshold(1.1), ErrFogThresholdInvalid)
	assert.ErrorIs(t, d.svc.ValidateFogThreshold(2.0), ErrFogThresholdInvalid)
}
