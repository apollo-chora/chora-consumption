// reveal_ledger.go — the free-on-win fog-reveal LEDGER (ADR-227 D2 fog
// gating, WS-C4 CHO-2083). Winning a campaign node reveals its neighbours for
// free (the D2 conquest reward); the reveal consumer folds
// chora.consumption.campaign.node_won.v1 into exactly ONE free fog-reveal per
// node win, and this ledger is what makes that "exactly once" hold under
// Pub/Sub at-least-once redelivery.
//
// Claim-then-mark protocol (redelivery-safe WITHOUT a cross-connection tx):
//
//  1. Claim — INSERT the reveal row keyed on the live semantic identity
//     (tenant, learner, goal, concept). It returns (claimed=true, the fresh
//     row) when it inserted, or (claimed=false, the existing live row) when a
//     live row already holds the reveal.
//  2. The caller publishes the fog-reveal suggestion request, THEN calls
//     MarkPublished to stamp request_id + published_at.
//  3. On redelivery Claim returns claimed=false: if existing.PublishedAt is
//     set the caller acks (a genuine duplicate); if it is nil the publish
//     crashed between step 1 and step 2, so the caller re-publishes and
//     re-marks (the accepted at-least-once crash-window recovery).
//
// Why the semantic identity is the primary dedupe and node_won_event_id is
// only the belt: won_at is a permanent ratchet (campaign.go D9), so a node is
// won exactly once and its node_won event fires once — the (tenant, learner,
// goal, concept) tuple already bounds reveals to one. The node_won_event_id
// UNIQUE is the braces: it hard-stops a double-fire (two distinct event ids
// for the same win) from ever minting a second reveal even if the ratchet were
// somehow bypassed upstream. Claim arbitrates on the semantic identity because
// that is the bound the product cares about; the event-id UNIQUE guards the
// schema.
package campaign

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// ErrRevealClaimNotFound is returned by RevealLedger.MarkPublished when no live
// reveal row matches the identity — the caller must Claim before it marks, so a
// miss is a broken protocol, not a no-op (fail-loud).
var ErrRevealClaimNotFound = errors.New("campaign: no live reveal claim to mark published")

// RevealClaim is one row of campaign_reveal_ledger (migration 0080): the free
// fog-reveal owed for a single node win. Its live identity is
// (TenantID, LearnerGCID, GoalID, ConceptID); NodeWonEventID is the binding
// event-id dedupe key. RequestID + PublishedAt are nil until MarkPublished
// records the published suggestion request.
type RevealClaim struct {
	ID             string
	TenantID       string
	LearnerGCID    string
	GoalID         string
	ConceptID      string
	NodeWonEventID string

	// RequestID is the published fog-reveal suggestion request id — nil until
	// MarkPublished stamps it.
	RequestID *string
	// PublishedAt is when the suggestion request was published — nil until
	// MarkPublished stamps it. A claimed=false row with a nil PublishedAt is a
	// crash between claim and publish; the caller re-publishes.
	PublishedAt *time.Time

	CreatedAt time.Time
}

// NewRevealClaim mints a fresh unpublished reveal claim for one node win. The
// id is a Go-minted UUIDv7 (the new-tables invariant; gen_random_uuid() is only
// the DB-side fallback). RequestID + PublishedAt stay nil — MarkPublished fills
// them after the publish.
func NewRevealClaim(tenantID, learnerGCID, goalID, conceptID, nodeWonEventID string, now time.Time) (RevealClaim, error) {
	if tenantID == "" || learnerGCID == "" || goalID == "" || conceptID == "" || nodeWonEventID == "" {
		return RevealClaim{}, fmt.Errorf("%w: tenant, learner, goal, concept and node_won_event_id are required", ErrInvalid)
	}
	return RevealClaim{
		ID:             domain.NewUUIDv7(),
		TenantID:       tenantID,
		LearnerGCID:    learnerGCID,
		GoalID:         goalID,
		ConceptID:      conceptID,
		NodeWonEventID: nodeWonEventID,
		CreatedAt:      now,
	}, nil
}

// RevealLedger persists the free-on-win reveal ledger (campaign_reveal_ledger,
// migration 0080). Adapters implement; the domain never imports infrastructure.
type RevealLedger interface {
	// Claim reserves the free reveal for one node win. It inserts on the live
	// semantic identity (tenant, learner, goal, concept) with ON CONFLICT DO
	// NOTHING and returns (claimed=true, the inserted row) on a fresh insert,
	// or (claimed=false, the existing live row) when a live row already holds
	// the reveal — the redelivery signal the caller branches on.
	Claim(ctx context.Context, claim RevealClaim) (claimed bool, existing *RevealClaim, err error)
	// MarkPublished stamps request_id + published_at on the live reveal row for
	// (tenant, learner, goal, concept) after its suggestion request published.
	// No live row matched ⇒ ErrRevealClaimNotFound (fail-loud — Claim precedes
	// MarkPublished).
	MarkPublished(ctx context.Context, tenantID, learnerGCID, goalID, conceptID, requestID string, at time.Time) error
}
