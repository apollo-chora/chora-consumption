package engagement

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// FogService manages the fog-of-war discovery state for learners.
type FogService struct {
	repo   UIDiscoveryStateRepository
	events EventPublisher
}

// NewFogService creates a FogService with the given repository and event publisher.
func NewFogService(repo UIDiscoveryStateRepository, events EventPublisher) *FogService {
	return &FogService{repo: repo, events: events}
}

// RevealNode transitions a TopicNode from hidden to revealed for a learner.
// Idempotent: if already revealed or conquered, returns existing state.
func (s *FogService) RevealNode(ctx context.Context, gcid, tenantID, topicNodeID uuid.UUID) (*UIDiscoveryState, error) {
	existing, err := s.repo.GetByGCIDAndTopicNode(ctx, gcid, tenantID, topicNodeID)
	if err != nil {
		return nil, err
	}

	// Idempotent — already revealed or conquered
	if existing != nil && existing.RevealStatus != RevealStatusHidden {
		return existing, nil
	}

	now := time.Now().UTC()
	state := &UIDiscoveryState{
		ID:           uuid.Must(uuid.NewV7()),
		GCID:         gcid,
		TenantID:     tenantID,
		TopicNodeID:  topicNodeID,
		RevealStatus: RevealStatusRevealed,
		RevealedAt:   &now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := s.repo.Save(ctx, state); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventFogNodeRevealed,
		tenantID,
		&gcid,
		state.ID,
		"UIDiscoveryState",
		map[string]interface{}{
			"topic_node_id": topicNodeID.String(),
		},
	)
	if err := s.events.Publish(ctx, TopicChoraverseEvents, event); err != nil {
		return nil, err
	}

	return state, nil
}

// ConquerNode transitions a TopicNode to conquered for a learner.
// If hidden, auto-reveals first. Idempotent: if already conquered, returns existing state.
func (s *FogService) ConquerNode(ctx context.Context, gcid, tenantID, topicNodeID uuid.UUID) (*UIDiscoveryState, error) {
	existing, err := s.repo.GetByGCIDAndTopicNode(ctx, gcid, tenantID, topicNodeID)
	if err != nil {
		return nil, err
	}

	// Idempotent — already conquered
	if existing != nil && existing.RevealStatus == RevealStatusConquered {
		return existing, nil
	}

	now := time.Now().UTC()

	if existing == nil {
		// Hidden → conquered (auto-reveal)
		existing = &UIDiscoveryState{
			ID:          uuid.Must(uuid.NewV7()),
			GCID:        gcid,
			TenantID:    tenantID,
			TopicNodeID: topicNodeID,
			RevealedAt:  &now,
			CreatedAt:   now,
		}
	}

	existing.RevealStatus = RevealStatusConquered
	existing.ConqueredAt = &now
	existing.UpdatedAt = now

	if err := s.repo.Save(ctx, existing); err != nil {
		return nil, err
	}

	event := NewDomainEvent(
		EventFogNodeConquered,
		tenantID,
		&gcid,
		existing.ID,
		"UIDiscoveryState",
		map[string]interface{}{
			"topic_node_id": topicNodeID.String(),
		},
	)
	if err := s.events.Publish(ctx, TopicChoraverseEvents, event); err != nil {
		return nil, err
	}

	return existing, nil
}

// GetDiscoveryStates returns all fog-of-war states for a learner.
func (s *FogService) GetDiscoveryStates(ctx context.Context, gcid, tenantID uuid.UUID) ([]UIDiscoveryState, error) {
	return s.repo.ListByGCID(ctx, gcid, tenantID)
}

// HandleAtomCompleted processes an atom completion event and triggers fog
// reveal or conquer transitions based on TopicRetention vs thresholds.
func (s *FogService) HandleAtomCompleted(ctx context.Context, gcid, tenantID, topicNodeID uuid.UUID, retention, revealThreshold, conquerThreshold float64) error {
	if retention < revealThreshold {
		return nil // Below reveal threshold — no action
	}

	if retention >= conquerThreshold {
		_, err := s.ConquerNode(ctx, gcid, tenantID, topicNodeID)
		return err
	}

	// Between reveal and conquer thresholds — reveal only
	_, err := s.RevealNode(ctx, gcid, tenantID, topicNodeID)
	return err
}

// ValidateFogThreshold validates that a fog reveal threshold is within 0.1-1.0.
func (s *FogService) ValidateFogThreshold(threshold float64) error {
	if threshold < MinFogThreshold || threshold > MaxFogThreshold {
		return ErrFogThresholdInvalid
	}
	return nil
}
