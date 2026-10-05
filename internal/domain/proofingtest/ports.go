// ports.go — driven ports the proofing-test composed runner depends on
// (hexagonal: the domain owns the interfaces; adapters satisfy them).
package proofingtest

import "context"

// Repository persists the ProofingTest aggregate. Reads/writes are scoped by
// (tenantID [, learnerGCID]) as defence-in-depth on top of RLS. GetByID /
// GetByAssistID return (nil, nil) when nothing live matches.
type Repository interface {
	Create(ctx context.Context, p *ProofingTest) error
	GetByID(ctx context.Context, tenantID, learnerGCID, id string) (*ProofingTest, error)
	// GetByAssistID resolves the row the qgen terminal events key on. Tenant-
	// scoped only (the terminal envelope carries tenant + gcid; the row's own
	// learner_gcid is the truth the caller re-checks).
	GetByAssistID(ctx context.Context, tenantID, assistID string) (*ProofingTest, error)
	// ListByLearner returns the learner's live proofing tests, newest first.
	// goalID == "" lists across goals; non-empty filters to one goal.
	ListByLearner(ctx context.Context, tenantID, learnerGCID, goalID string) ([]*ProofingTest, error)
	Update(ctx context.Context, p *ProofingTest) error
}

// TickedEdgeReader reads the goal's ticked ceremony learning-edges: LIVE
// intent-tagged (remediate|explore) ConceptNodes inside the goal root's
// hierarchy subtree (migration 0066 `concept_nodes.intent`; ADR-214 D1
// subtree scoping). READ-ONLY — the runner never writes the concept graph.
//
// R8-7 note: TargetEdge.Key is the qgen concept key. ConceptNode carries NO
// key field yet (the KG session is adding key-at-mint separately) — the pg
// adapter yields Key == "" until that lands, which trips the runner's
// EDGES_LACK_KEYS 422 gate.
type TickedEdgeReader interface {
	ListTicked(ctx context.Context, tenantID, learnerGCID, rootConceptID string) ([]TargetEdge, error)
}

// ReserveInput / ReserveResult shape the pre-flight mana reserve (spec §3:
// reserve = DeductMana(units=0) with the deterministic idempotency key
// action:tenant:gcid:proofing_test_id; the key doubles as the reservation
// handle so a refused generation refunds against it).
type ReserveInput struct {
	TenantID       string
	GCID           string
	ProofingTestID string
}

type ReserveResult struct {
	ReservationID string
	PriceUnits    int64
}

// RefundInput returns a reservation (publish failure / crew refusal).
type RefundInput struct {
	TenantID      string
	GCID          string
	ReservationID string
	Units         int64
	Reason        string
}

// ManaReserver is the reserve→settle/refund economy port. Reserve returns
// *ErrInsufficientMana (typed) on a genuine balance refusal; any other error
// is infrastructure and MUST surface (fail-loud — this is a paid action,
// never fail-open).
type ManaReserver interface {
	Reserve(ctx context.Context, in ReserveInput) (ReserveResult, error)
	Refund(ctx context.Context, in RefundInput) error
}
