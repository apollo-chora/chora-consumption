// ritual_publisher.go — CHO-2016 G5: the publish/enable orchestration (domain
// service, PathUnlocker style). Composes the earned capability context →
// validates + freezes the composed price on the aggregate → persists (draft
// Create for a new ritual, then the append-only AppendRevision) → emits
// ritual_published.v1. A rejected publish (stage gate / invalid steps)
// persists NOTHING and emits nothing (validate in-memory BEFORE any write).
package companion

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrRitualNotFound is returned when a re-publish / enable targets a ritual
// that does not exist (or is soft-deleted). Mapped to 404 at the edge.
var ErrRitualNotFound = errors.New("companion.ritual: not found")

// ErrRitualSinkNotWired fires when the declared sink is in the ADR-218 D9
// closed list but has no registered writer in THIS deployment. Mapped to 422
// RITUAL_SINK_NOT_WIRED at the edge (N5).
//
// This is a distinct condition from ErrRitualUnknownSink: the sink is a real
// platform sink, the learner did not typo it, and it may become publishable
// later without any code change here. Refusing at publish is the whole point:
// the alternative, which is what shipped, lets the ritual publish and then fail
// at WriteToSink on every run, refunding each time. A refund is not a fix; the
// ritual was never runnable.
var ErrRitualSinkNotWired = errors.New("companion.ritual: sink has no writer registered in this deployment")

// SinkRegistry reports which of the closed sinks have a registered writer.
// Implemented by the adapter that already owns the writer map
// (clients.RitualSinkRouter), so the publish gate and the run-time router can
// never disagree about what is wired.
type SinkRegistry interface {
	IsSinkWired(sink string) bool
	// WiredSinks lists the registered sinks in the ADR-218 D9 closed-list
	// order. Served to the composer so it greys from the deployment's own
	// answer rather than a client-side guess that can drift (B3b).
	WiredSinks() []string
}

// RitualCapabilityResolver resolves the companion's earned capability context
// (growth stage + equipped-active Skills + craft flags + catalogue +
// OtherEnabledCount) at the moment of a designer action. excludeRitualID is
// omitted from the OtherEnabledCount so a re-enable of the ritual itself does
// not count against its own quota. The production impl reads the loadout +
// growth + catalogue; tests fake it.
type RitualCapabilityResolver interface {
	ResolveCapability(ctx context.Context, tenantID, companionID, ownerGCID, excludeRitualID string) (RitualCapabilityContext, error)
}

// RitualPublisherConfig wires the publisher (Clock/NewID injected for test
// determinism).
type RitualPublisherConfig struct {
	Repo   RitualRepository
	Caps   RitualCapabilityResolver
	Outbox RitualOutbox
	// Sinks gates publish on what this deployment can actually write (N5).
	// REQUIRED: an absent registry would have to mean "assume every sink is
	// wired", which is exactly the silent optimism this row exists to remove.
	Sinks SinkRegistry
	Clock func() time.Time
	NewID func() string
}

// RitualPublisher orchestrates publish + enable/disable.
type RitualPublisher struct {
	cfg RitualPublisherConfig
}

// NewRitualPublisher validates dependencies + applies defaults.
func NewRitualPublisher(cfg RitualPublisherConfig) (*RitualPublisher, error) {
	if cfg.Repo == nil || cfg.Caps == nil || cfg.Outbox == nil || cfg.Sinks == nil {
		return nil, errors.New("companion.ritualpublisher: repo/caps/outbox/sinks all required")
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	if cfg.NewID == nil {
		// CHO-2225: event_id is a mandatory UUIDv7 envelope field. There is no
		// safe default — a locally-minted stand-in is a non-UUID that 22P02s
		// every consumer keying idempotency on event_id against a UUID column.
		return nil, errors.New("companion.ritualpublisher: id factory required")
	}
	return &RitualPublisher{cfg: cfg}, nil
}

// CreateDraft creates + persists a fresh DRAFT ritual (no revision, disabled).
// The st4 gate is enforced at Publish, not here — a learner may draft the shell
// (the FE designer creates it, then composes steps + publishes). Returns the
// draft with a minted RitualID.
func (p *RitualPublisher) CreateDraft(ctx context.Context, tenantID, companionID, name string, trigger RitualTrigger, sink string) (*Ritual, error) {
	r, err := NewRitual(tenantID, companionID, name, trigger, sink)
	if err != nil {
		return nil, err
	}
	if err := p.cfg.Repo.Create(ctx, r); err != nil {
		return nil, fmt.Errorf("companion.ritualpublisher: create draft: %w", err)
	}
	return r, nil
}

// PublishRitualInput is a publish request. RitualID empty ⇒ create a new ritual;
// non-empty ⇒ append a revision to that ritual.
type PublishRitualInput struct {
	TenantID     string
	CompanionID  string
	OwnerGCID    string
	RitualID     string
	Name         string
	Trigger      RitualTrigger
	Sink         string
	Steps        []RitualStep
	ArmorVerdict string
	Traceparent  string
	Tracestate   string
}

// Publish validates + freezes + persists + emits. Returns the ritual + the new
// revision.
func (p *RitualPublisher) Publish(ctx context.Context, in PublishRitualInput) (*Ritual, RitualRevision, error) {
	caps, err := p.cfg.Caps.ResolveCapability(ctx, in.TenantID, in.CompanionID, in.OwnerGCID, in.RitualID)
	if err != nil {
		return nil, RitualRevision{}, fmt.Errorf("companion.ritualpublisher: resolve capability: %w", err)
	}

	isNew := in.RitualID == ""
	var r *Ritual
	if isNew {
		r, err = NewRitual(in.TenantID, in.CompanionID, in.Name, in.Trigger, in.Sink)
		if err != nil {
			return nil, RitualRevision{}, err
		}
	} else {
		r, err = p.cfg.Repo.GetByID(ctx, in.TenantID, in.RitualID)
		if err != nil {
			return nil, RitualRevision{}, fmt.Errorf("companion.ritualpublisher: load ritual: %w", err)
		}
		if r == nil {
			return nil, RitualRevision{}, fmt.Errorf("%w: %q", ErrRitualNotFound, in.RitualID)
		}
	}

	// N5: refuse a sink this deployment cannot write, BEFORE any persistence,
	// so the refusal costs nothing and leaves nothing behind. Checked against
	// the ritual's own sink (which, on a re-publish, is the stored one) rather
	// than the input, since only a new ritual carries in.Sink.
	if !p.cfg.Sinks.IsSinkWired(r.Sink) {
		return nil, RitualRevision{}, fmt.Errorf("%w: %q", ErrRitualSinkNotWired, r.Sink)
	}

	// Validate + freeze IN MEMORY first — a rejected publish must persist nothing.
	rev, err := r.Publish(in.Steps, in.ArmorVerdict, caps, p.cfg.Clock)
	if err != nil {
		return nil, RitualRevision{}, err
	}

	// Persist: draft Create for a new ritual, then the append-only revision.
	if isNew {
		if err := p.cfg.Repo.Create(ctx, r); err != nil {
			return nil, RitualRevision{}, fmt.Errorf("companion.ritualpublisher: create ritual: %w", err)
		}
	}
	if err := p.cfg.Repo.AppendRevision(ctx, in.TenantID, r.RitualID, rev, r.PublishedPriceUnits); err != nil {
		return nil, RitualRevision{}, fmt.Errorf("companion.ritualpublisher: append revision: %w", err)
	}
	if err := p.emitPublished(ctx, r, rev, in.OwnerGCID, in.Traceparent, in.Tracestate); err != nil {
		return nil, RitualRevision{}, err
	}
	return r, rev, nil
}

// SetEnabled enables (quota-checked) or disables a published ritual.
func (p *RitualPublisher) SetEnabled(ctx context.Context, tenantID, companionID, ownerGCID, ritualID string, enabled bool) error {
	r, err := p.cfg.Repo.GetByID(ctx, tenantID, ritualID)
	if err != nil {
		return fmt.Errorf("companion.ritualpublisher: load ritual: %w", err)
	}
	if r == nil {
		return fmt.Errorf("%w: %q", ErrRitualNotFound, ritualID)
	}
	if enabled {
		caps, err := p.cfg.Caps.ResolveCapability(ctx, tenantID, companionID, ownerGCID, ritualID)
		if err != nil {
			return fmt.Errorf("companion.ritualpublisher: resolve capability: %w", err)
		}
		if err := r.Enable(caps); err != nil {
			return err
		}
	} else {
		r.Disable()
	}
	if err := p.cfg.Repo.SetEnabled(ctx, tenantID, ritualID, r.Enabled); err != nil {
		return fmt.Errorf("companion.ritualpublisher: persist enabled: %w", err)
	}
	return nil
}

// emitPublished publishes ritual_published.v1 (outbox, envelope-compliant).
func (p *RitualPublisher) emitPublished(ctx context.Context, r *Ritual, rev RitualRevision, ownerGCID, traceparent, tracestate string) error {
	now := p.cfg.Clock()
	env := LoadoutEnvelope{
		EventID:        p.cfg.NewID(),
		IdempotencyKey: "ritual_published:" + r.RitualID + ":" + fmt.Sprintf("%d", rev.RevisionNo),
		TenantID:       r.TenantID,
		GCID:           ownerGCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    traceparent,
		Tracestate:     tracestate,
		SourceProject:  "chora-content",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
	payload := map[string]any{
		"ritual_id":             r.RitualID,
		"companion_id":          r.CompanionID,
		"revision_no":           rev.RevisionNo,
		"trigger":               string(r.Trigger),
		"sink":                  r.Sink,
		"published_price_units": r.PublishedPriceUnits,
	}
	if err := p.cfg.Outbox.PublishRitualEvent(ctx, TopicCompanionRitualPublished, payload, env); err != nil {
		return fmt.Errorf("companion.ritualpublisher: publish ritual_published: %w", err)
	}
	return nil
}
