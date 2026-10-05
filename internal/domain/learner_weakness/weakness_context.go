// weakness_context.go - the ADR-247 weakness-load read-projection (F2) + its
// narrow read port. A distilled view of a learner's EXPLICITLY-diagnosed Growth
// Edge for ONE concept, shaped for the two KG-generation surfaces (Capability A
// question generation + Capability B fog suggestions). Both live in the domain
// so consumers depend only on the domain, never on the pg adapter.
package learner_weakness

import "context"

// WeaknessContext carries only the fields the ADR-247 flows compose: the
// descriptor summary, the observed misconceptions, and the redacted
// wrong-answer exemplars flattened to evidence lines. Never the raw upload,
// never PII, never a cross-domain ref.
type WeaknessContext struct {
	Descriptor     string   // the weakness summary/descriptor (Descriptor.Summary)
	Misconceptions []string // specific misconceptions observed (may be empty)
	Evidence       []string // flattened redacted wrong-answer exemplars (may be empty)
}

// WeaknessContextLoader is the NARROW read port for ADR-247 weakness
// enrichment. The consumers (the campaign question wiring, the concept
// suggestion producers, the won-subscriber) depend only on this one method, so
// the many unrelated mocks of the full Growth-Edge Repository need not
// implement it (interface segregation). The pg LearnerWeaknessRepo satisfies
// both this and Repository.
type WeaknessContextLoader interface {
	// LoadWeaknessContextByConceptKey returns the learner's EXPLICITLY-diagnosed
	// weakness (the marked-test / upload Diagnose lane, ADR-238 D4) for one
	// concept_key as a distilled WeaknessContext, or (nil, nil) when there is
	// none. conceptKey is normalised internally; tenant scoping rides the ctx
	// (RLS); tenantID is accepted for signature symmetry, not as a second filter.
	LoadWeaknessContextByConceptKey(ctx context.Context, tenantID, learnerGCID, conceptKey string) (*WeaknessContext, error)
}
