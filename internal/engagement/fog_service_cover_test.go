package engagement

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// FogService error-path coverage. The existing fog tests cover the happy
// paths + idempotency; these cover the three failure branches each of
// RevealNode and ConquerNode (lookup error, save error, publish error).
// ---------------------------------------------------------------------------

func TestRevealNode_LookupError(t *testing.T) {
	t.Parallel()
	d := newTestFogService()
	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	topicNodeID := uuid.Must(uuid.NewV7())

	d.repo.On("GetByGCIDAndTopicNode", mock.Anything, gcid, tenantID, topicNodeID).
		Return(nil, errors.New("db down"))

	_, err := d.svc.RevealNode(context.Background(), gcid, tenantID, topicNodeID)
	require.Error(t, err)
	// No save attempted when lookup fails.
	d.repo.AssertNotCalled(t, "Save", mock.Anything, mock.Anything)
	d.publisher.AssertNotCalled(t, "Publish")
}

func TestRevealNode_SaveError(t *testing.T) {
	t.Parallel()
	d := newTestFogService()
	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	topicNodeID := uuid.Must(uuid.NewV7())

	d.repo.On("GetByGCIDAndTopicNode", mock.Anything, gcid, tenantID, topicNodeID).Return(nil, nil)
	d.repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.UIDiscoveryState")).
		Return(errors.New("save failed"))

	_, err := d.svc.RevealNode(context.Background(), gcid, tenantID, topicNodeID)
	require.Error(t, err)
	// Event must NOT be published if the state failed to persist.
	d.publisher.AssertNotCalled(t, "Publish")
}

func TestRevealNode_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestFogService()
	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	topicNodeID := uuid.Must(uuid.NewV7())

	d.repo.On("GetByGCIDAndTopicNode", mock.Anything, gcid, tenantID, topicNodeID).Return(nil, nil)
	d.repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.UIDiscoveryState")).Return(nil)
	d.publisher.On("Publish", mock.Anything, TopicChoraverseEvents, mock.Anything).
		Return(errors.New("pubsub unavailable"))

	_, err := d.svc.RevealNode(context.Background(), gcid, tenantID, topicNodeID)
	require.Error(t, err)
}

func TestConquerNode_LookupError(t *testing.T) {
	t.Parallel()
	d := newTestFogService()
	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())
	topicNodeID := uuid.Must(uuid.NewV7())

	d.repo.On("GetByGCIDAndTopicNode", mock.Anything, gcid, tenantID, topicNodeID).
		Return(nil, errors.New("db down"))

	_, err := d.svc.ConquerNode(context.Background(), gcid, tenantID, topicNodeID)
	require.Error(t, err)
	d.repo.AssertNotCalled(t, "Save", mock.Anything, mock.Anything)
}

func TestConquerNode_SaveError(t *testing.T) {
	t.Parallel()
	d := newTestFogService()
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
	d.repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.UIDiscoveryState")).
		Return(errors.New("save failed"))

	_, err := d.svc.ConquerNode(context.Background(), gcid, tenantID, topicNodeID)
	require.Error(t, err)
	d.publisher.AssertNotCalled(t, "Publish")
}

func TestConquerNode_PublishError(t *testing.T) {
	t.Parallel()
	d := newTestFogService()
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
	d.publisher.On("Publish", mock.Anything, TopicChoraverseEvents, mock.Anything).
		Return(errors.New("pubsub unavailable"))

	_, err := d.svc.ConquerNode(context.Background(), gcid, tenantID, topicNodeID)
	require.Error(t, err)
}
