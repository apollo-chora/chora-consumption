package companion

// summoner.go — CHO-2013 P1 (R3-1): the companion-side effects of a Goal
// attach ("Summon-onto-Goal"). Binding IS the theme commitment: on attach the
// companion's specialization derives from the Goal's theme (root-concept
// title), the awakening resonant pick is cleared (re-picked inside the new
// Goal's subgraph), and the change is announced on
// chora.consumption.companion.specialization_changed.v1 — the first real
// emitter of that contract. Egg-born 'general' stubs heal at their first
// bind. Same-theme re-attach is a no-op so idempotent PATCH retries stay
// clean.
//
// Partial-failure window (documented, mirrors the acquire compensation
// stance): specialization persists BEFORE the event publish; a publish
// failure surfaces as a 5xx and the retry no-ops (spec already == theme), so
// that one event can be lost. Acceptable in P1 — the event has no consumers
// yet; P2+ consumers must tolerate a missing initial specialization_changed.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TopicCompanionSpecializationChanged — the defined-in-contracts topic this
// unit starts emitting. Mirrors the adapter events package constant (domain
// packages own their topic strings, per path_unlocker.go precedent).
const TopicCompanionSpecializationChanged = "chora.consumption.companion.specialization_changed.v1"

// SummonInstanceStore is the narrow instance persistence surface the
// Summoner needs (the full InstanceRepository is a superset).
type SummonInstanceStore interface {
	Get(ctx context.Context, companionID string) (*Instance, error)
	Update(ctx context.Context, inst *Instance) error
}

// ResonanceClearer clears the awakening resonant-concept pick on rebind
// (growth repo adapter closes the gap in wiring).
type ResonanceClearer interface {
	ClearResonance(ctx context.Context, tenantID, companionID string) error
}

// SummonerConfig wires the Summoner (all fields required).
type SummonerConfig struct {
	Instances SummonInstanceStore
	Resonance ResonanceClearer
	Outbox    LoadoutOutbox
	// Clock + NewID injected for test determinism.
	Clock func() time.Time
	NewID func() string
}

// Summoner applies the companion-side bind effects.
type Summoner struct {
	cfg SummonerConfig
}

// NewSummoner validates dependencies.
func NewSummoner(cfg SummonerConfig) (*Summoner, error) {
	if cfg.Instances == nil {
		return nil, errors.New("companion.summoner: instance store required")
	}
	if cfg.Resonance == nil {
		return nil, errors.New("companion.summoner: resonance clearer required")
	}
	if cfg.Outbox == nil {
		return nil, errors.New("companion.summoner: outbox required")
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	if cfg.NewID == nil {
		return nil, errors.New("companion.summoner: id factory required")
	}
	return &Summoner{cfg: cfg}, nil
}

// SummonInput identifies the bind + the derived theme.
type SummonInput struct {
	TenantID    string
	OwnerGCID   string
	CompanionID string
	Theme       string // the Goal's resolved theme (root-concept title fallback chain)
	Traceparent string
	Tracestate  string
}

// ValidateOwned loads the instance and enforces the (tenant, owner) guard —
// the goals PATCH pre-check runs this BEFORE mutating the Goal so an unknown
// or foreign companion rejects the attach up front (no cross-owner
// disclosure: foreign == missing).
func (s *Summoner) ValidateOwned(ctx context.Context, tenantID, ownerGCID, companionID string) (*Instance, error) {
	inst, err := s.cfg.Instances.Get(ctx, companionID)
	if err != nil {
		return nil, err
	}
	if inst == nil || inst.TenantID != tenantID || inst.OwnerGCID != ownerGCID {
		return nil, ErrInstanceNotFound
	}
	return inst, nil
}

// OnGoalBound applies the companion-side effects of a successful Goal attach.
// Returns changed=false (and does nothing) when the specialization already
// matches the theme — the idempotent re-attach path.
func (s *Summoner) OnGoalBound(ctx context.Context, in SummonInput) (changed bool, err error) {
	theme := strings.TrimSpace(in.Theme)
	if theme == "" {
		return false, ErrSpecializationBad
	}
	inst, err := s.ValidateOwned(ctx, in.TenantID, in.OwnerGCID, in.CompanionID)
	if err != nil {
		return false, err
	}
	if inst.Specialization == theme {
		return false, nil
	}

	previous, err := inst.ChangeSpecialization(theme)
	if err != nil {
		return false, err
	}
	// Clear the resonant pick FIRST (idempotent), then persist the re-tag:
	// a crash between the two re-runs cleanly on retry (spec still old →
	// full path re-executes; ClearResonance is a no-op on NULL).
	if err := s.cfg.Resonance.ClearResonance(ctx, in.TenantID, in.CompanionID); err != nil {
		return false, fmt.Errorf("companion.summoner: clear resonance: %w", err)
	}
	if err := s.cfg.Instances.Update(ctx, inst); err != nil {
		return false, fmt.Errorf("companion.summoner: persist specialization: %w", err)
	}

	now := s.cfg.Clock()
	env := LoadoutEnvelope{
		EventID:        s.cfg.NewID(),
		IdempotencyKey: "specialization:" + in.CompanionID + ":" + theme,
		TenantID:       in.TenantID,
		GCID:           in.OwnerGCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    in.Traceparent,
		Tracestate:     in.Tracestate,
		SourceProject:  "chora-content",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
	payload := map[string]any{
		"companion_id":            in.CompanionID,
		"owner_gcid":              in.OwnerGCID,
		"previous_specialization": previous,
		"new_specialization":      theme,
		// The bind derive never touches the Memory Bank — carry_over is the
		// non-destructive default (the learner made no memory decision).
		"memory_handling":    "carry_over",
		"user_reason":        "",
		"changed_at":         now,
		"chora_companion_id": in.CompanionID,
	}
	if err := s.cfg.Outbox.PublishLoadoutEvent(ctx, TopicCompanionSpecializationChanged, payload, env); err != nil {
		return false, fmt.Errorf("companion.summoner: publish specialization_changed: %w", err)
	}
	return true, nil
}
