// Package atomrefresh is the ADR-244 D5 standing refresh trigger: when
// chora.creation.atom.published.v1 / atom.updated.v1 lands, the Companion may
// OFFER the atom to concepts that could take it, as a pending
// ConceptSuggestion. Per ADR-212 D9 the events are refresh SUGGESTIONS, never
// authority that mutates the learner's graph: this package writes zero
// atom_refs and emits proposals only (the learner-accepted single-writer of
// ADR-244 D1 stays the sole binding path).
//
// Reachability is designed in, not bolted on: the 2026-08-16 UI walk proved
// pending suggestions are only actionable on a WON focal hex inside a
// companion-attached goal, so the trigger targets exactly those (supply is
// never shipped into a blocked pipe).
//
// The ledger below makes the trigger redelivery-safe AND backfill-safe: the
// creation service can re-emit atom.published for every atom of a tenant
// (backfill-published / backfill-topic-tags), so dedupe keyed on event_id
// alone would still storm. One live ledger row per (tenant, learner, atom,
// focal) bounds each pairing to a single proposal request, ever; the per-day
// cap bounds the LLM spend a publish burst can create.
package atomrefresh

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// Caps bounding the proposal fan-out. Every suggestion request costs exactly
// one fog-orchestrator LLM call, so these are cost bounds, not tuning knobs:
// raise them only with a recorded cost decision.
const (
	// MaxProposalsPerAtomEvent bounds how many (learner, focal) requests one
	// atom event may emit, across all learners of the tenant.
	MaxProposalsPerAtomEvent = 3
	// MaxProposalsPerLearnerPerDay bounds how many atom-refresh requests a
	// single learner may receive per UTC day, so a publish burst (e.g. a qgen
	// batch or an operator backfill) cannot flood one learner's pending queue.
	MaxProposalsPerLearnerPerDay = 5
)

// ErrInvalid marks a constructor rejection (missing identity field).
var ErrInvalid = errors.New("atom_refresh: invalid")

// ErrRefreshClaimNotFound is returned by Ledger.MarkPublished when no live
// claim row matches the identity. Claim always precedes MarkPublished, so a
// miss is a broken protocol, not a no-op (fail-loud).
var ErrRefreshClaimNotFound = errors.New("atom_refresh: no live refresh claim to mark published")

// RefreshClaim is one row of atom_refresh_ledger (migration 0108): the single
// proposal request owed for one (tenant, learner, atom, focal) pairing. Its
// live identity is (TenantID, LearnerGCID, AtomID, FocalConceptID);
// TriggerEventID records WHICH atom event minted it (audit, deliberately not
// unique: one event fans out to several pairings). RequestID + PublishedAt
// stay nil until MarkPublished records the published suggestion request; a
// claimed-but-unpublished row is either the crash-window (re-publish) or the
// audit trace of a post-claim skip (not entitled / over cap), mirroring the
// campaign reveal ledger's posture.
type RefreshClaim struct {
	ID             string
	TenantID       string
	LearnerGCID    string
	AtomID         string
	FocalConceptID string
	TriggerEventID string

	// RequestID is the published suggestion request id, nil until
	// MarkPublished stamps it.
	RequestID *string
	// PublishedAt is when the suggestion request published, nil until
	// MarkPublished stamps it.
	PublishedAt *time.Time

	CreatedAt time.Time
}

// NewRefreshClaim mints a fresh unpublished claim. The id is a Go-minted
// UUIDv7 (the new-tables invariant; gen_random_uuid() is only the DB-side
// fallback). RequestID + PublishedAt stay nil until MarkPublished.
func NewRefreshClaim(tenantID, learnerGCID, atomID, focalConceptID, triggerEventID string, now time.Time) (RefreshClaim, error) {
	if tenantID == "" || learnerGCID == "" || atomID == "" || focalConceptID == "" || triggerEventID == "" {
		return RefreshClaim{}, fmt.Errorf("%w: tenant, learner, atom, focal and trigger_event_id are required", ErrInvalid)
	}
	return RefreshClaim{
		ID:             domain.NewUUIDv7(),
		TenantID:       tenantID,
		LearnerGCID:    learnerGCID,
		AtomID:         atomID,
		FocalConceptID: focalConceptID,
		TriggerEventID: triggerEventID,
		CreatedAt:      now,
	}, nil
}

// Ledger persists the atom-refresh proposal ledger (atom_refresh_ledger,
// migration 0108). Adapters implement; the domain never imports
// infrastructure. Claim-then-mark protocol identical to campaign.RevealLedger:
// Claim INSERTs on the live semantic identity with ON CONFLICT DO NOTHING and
// reports (claimed, existing); the caller publishes, then MarkPublished stamps
// request_id + published_at.
type Ledger interface {
	// Claim reserves the proposal for one (tenant, learner, atom, focal)
	// pairing. (true, fresh row) on insert; (false, existing live row) when
	// one already holds it, which the caller branches on: PublishedAt set is
	// a genuine duplicate (skip), nil is the crash-window (re-publish).
	Claim(ctx context.Context, claim RefreshClaim) (claimed bool, existing *RefreshClaim, err error)
	// MarkPublished stamps request_id + published_at on the live claim for
	// (tenant, learner, atom, focal). No live row matched means
	// ErrRefreshClaimNotFound (fail-loud, Claim precedes MarkPublished).
	MarkPublished(ctx context.Context, tenantID, learnerGCID, atomID, focalConceptID, requestID string, at time.Time) error
	// CountClaimedSince counts live claims for the learner created at or
	// after `since` (the per-day cap read; the caller passes the UTC
	// day-start).
	CountClaimedSince(ctx context.Context, tenantID, learnerGCID string, since time.Time) (int, error)
}
