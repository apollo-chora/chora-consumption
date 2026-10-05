// concept_emitter.go - ADR-244 D4 (CHO-2303): the envelope-building emitter for
// chora.consumption.concept.atoms_bound.v1.
//
// Before this, NO concept domain event existed: manual attach, suggestion
// accept, cluster projection, merge and split all mutated `concept_nodes`
// silently, so a binding was a side effect of a handler rather than a domain
// event with an owner. Every path that changes `atom_refs` emits through here;
// a path that mutates bindings without emitting is a defect.
//
// The event carries the DELTA and the RESULTING state together (one topic, not
// separate attach/detach lanes) so consumers are idempotent and self-healing: a
// consumer that misses a message recovers on the next rather than drifting.
//
// Rides the transactional-outbox Publisher (persist-then-emit; the outbox row is
// durable-pending once Publish returns), like campaign_emitter.go.
package events

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// Change sources for ConceptAtomsBound.change_source (ADR-244 D4). Consumers
// MUST tolerate an unrecognised value rather than error: the set grows as new
// binding paths appear, and an exhaustive switch would drift silently.
const (
	ChangeSourceManualAttach       = "manual_attach"
	ChangeSourceSuggestionAccepted = "suggestion_accepted"
	ChangeSourceClusterProjection  = "cluster_projection"
	ChangeSourceSplit              = "split"
	ChangeSourceMerge              = "merge"
	ChangeSourceCeremonyEdge       = "ceremony_edge"
)

// ConceptAtomsBoundInput is the full delta plus the resulting state.
type ConceptAtomsBoundInput struct {
	TenantID          string
	LearnerGCID       string
	ConceptID         string
	AttachedAtomIDs   []string
	DetachedAtomIDs   []string
	ResultingAtomRefs []string
	ChangeSource      string
	Provenance        string
	OccurredAt        time.Time
	Traceparent       string
	Tracestate        string
}

// ConceptEmitter builds envelope-complete concept events.
type ConceptEmitter struct {
	pub Publisher
	now func() time.Time
}

// NewConceptEmitter wires the emitter; now stamps PublishedAt (nil → time.Now
// UTC).
func NewConceptEmitter(pub Publisher, now func() time.Time) *ConceptEmitter {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &ConceptEmitter{pub: pub, now: now}
}

// ConceptAtomsBound emits chora.consumption.concept.atoms_bound.v1.
//
// Two deliberate refusals to publish:
//   - An unscoped event (missing tenant/gcid/concept) is a PRODUCER BUG and
//     returns an error rather than shipping an unattributable fact.
//   - A no-op change (nothing attached AND nothing detached) publishes nothing.
//     The aggregate's AttachAtom/DetachAtom are idempotent and only touch() on a
//     real change, so an emitted event always denotes a real transition and a
//     consumer never has to defend against phantom churn.
func (e *ConceptEmitter) ConceptAtomsBound(ctx context.Context, in ConceptAtomsBoundInput) error {
	tenant := strings.TrimSpace(in.TenantID)
	gcid := strings.TrimSpace(in.LearnerGCID)
	conceptID := strings.TrimSpace(in.ConceptID)
	if tenant == "" || gcid == "" || conceptID == "" {
		return fmt.Errorf("concept_emitter: tenant_id, learner_gcid and concept_id are required")
	}
	if len(in.AttachedAtomIDs) == 0 && len(in.DetachedAtomIDs) == 0 {
		return nil // no-op: nothing actually changed
	}
	occurred := in.OccurredAt.UTC()
	idem := fmt.Sprintf("%s:%s:%s", TopicConceptAtomsBound, conceptID, occurred.Format(time.RFC3339Nano))
	env := Envelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: idem,
		TenantID:       tenant,
		GCID:           gcid,
		OccurredAt:     occurred,
		PublishedAt:    e.now().UTC(),
		Traceparent:    tracing.EnsureTraceparent(firstNonEmpty(in.Traceparent, tracing.TraceparentFromContext(ctx))),
		Tracestate:     in.Tracestate,
		SourceProject:  SourceProject,
		SourceService:  SourceService,
		SchemaVersion:  1,
	}
	return e.pub.Publish(TopicConceptAtomsBound, env, map[string]any{
		"concept_id":          conceptID,
		"tenant_id":           tenant,
		"learner_gcid":        gcid,
		"attached_atom_ids":   nonNilStrings(in.AttachedAtomIDs),
		"detached_atom_ids":   nonNilStrings(in.DetachedAtomIDs),
		"resulting_atom_refs": nonNilStrings(in.ResultingAtomRefs),
		"change_source":       strings.TrimSpace(in.ChangeSource),
		"provenance":          strings.TrimSpace(in.Provenance),
		"occurred_at":         occurred,
	})
}

// ConceptDeletedInput scopes a concept soft-delete for the cascade event.
type ConceptDeletedInput struct {
	TenantID    string
	LearnerGCID string
	ConceptID   string
	OccurredAt  time.Time
	Traceparent string
	Tracestate  string
}

// ConceptDeleted emits chora.consumption.concept.deleted.v1 (CHO-2324) - the
// durable trigger for the edge-cleanup cascade. The concept node is already
// tombstoned in the caller's request; this REPORTS that removal so the
// edge-cleanup subscriber can soft-delete the concept's incident edges (edges are
// a SEPARATE aggregate per ADR-212, cleaned via the event rather than an inline
// cross-aggregate write).
//
// An unscoped event (missing tenant/gcid/concept) is a PRODUCER BUG and returns
// an error rather than shipping an unattributable fact.
func (e *ConceptEmitter) ConceptDeleted(ctx context.Context, in ConceptDeletedInput) error {
	tenant := strings.TrimSpace(in.TenantID)
	gcid := strings.TrimSpace(in.LearnerGCID)
	conceptID := strings.TrimSpace(in.ConceptID)
	if tenant == "" || gcid == "" || conceptID == "" {
		return fmt.Errorf("concept_emitter: tenant_id, learner_gcid and concept_id are required")
	}
	occurred := in.OccurredAt.UTC()
	idem := fmt.Sprintf("%s:%s:%s", TopicConceptDeleted, conceptID, occurred.Format(time.RFC3339Nano))
	env := Envelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: idem,
		TenantID:       tenant,
		GCID:           gcid,
		OccurredAt:     occurred,
		PublishedAt:    e.now().UTC(),
		Traceparent:    tracing.EnsureTraceparent(firstNonEmpty(in.Traceparent, tracing.TraceparentFromContext(ctx))),
		Tracestate:     in.Tracestate,
		SourceProject:  SourceProject,
		SourceService:  SourceService,
		SchemaVersion:  1,
	}
	return e.pub.Publish(TopicConceptDeleted, env, map[string]any{
		"concept_id":   conceptID,
		"tenant_id":    tenant,
		"learner_gcid": gcid,
		"occurred_at":  occurred,
	})
}

// nonNilStrings keeps the wire shape stable - a nil slice and an empty one must
// serialise identically, so a consumer never has to distinguish them.
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ClusterProjectionBindingSink adapts ConceptEmitter to
// clusterprojection.BindingSink (ADR-244 D4). The domain package must not
// import the events package, so the narrow signature lives there and this
// adapter translates it.
type ClusterProjectionBindingSink struct {
	emitter *ConceptEmitter
}

// NewClusterProjectionBindingSink wires the adapter.
func NewClusterProjectionBindingSink(e *ConceptEmitter) *ClusterProjectionBindingSink {
	return &ClusterProjectionBindingSink{emitter: e}
}

// ConceptAtomsBound reports a freshly-projected root concept's seed atoms. A
// new node has no prior state, so its whole ref set is the attach delta.
func (s *ClusterProjectionBindingSink) ConceptAtomsBound(
	ctx context.Context, conceptID, tenantID, learnerGCID, provenance string,
	atomRefs []string, at time.Time,
) error {
	if s == nil || s.emitter == nil {
		return nil
	}
	return s.emitter.ConceptAtomsBound(ctx, ConceptAtomsBoundInput{
		TenantID:          tenantID,
		LearnerGCID:       learnerGCID,
		ConceptID:         conceptID,
		AttachedAtomIDs:   atomRefs,
		ResultingAtomRefs: atomRefs,
		ChangeSource:      ChangeSourceClusterProjection,
		Provenance:        provenance,
		OccurredAt:        at,
	})
}
