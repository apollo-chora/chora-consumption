// grown.go — ADR-196 reward spine: the active→grown transition projection the
// recover paths report so the application layer can publish the
// chora.consumption.weakness.grown.v1 reward event.
//
// Closing a Growth Edge (recovering it to <= MasteredStrengthThreshold) is the
// canonical Reward trigger of the Curiosity-Reward(-Social) engagement loop. The
// pg Repository's recover methods return one GrownEdge per REAL active→grown
// transition (never for an already-grown edge recovered further), and the
// derived-weakness projector turns each into a durable, OTLP-traceable event.
package learner_weakness

import "time"

// GrownEdge is the projection of a Growth Edge that just crossed the mastery
// threshold (active→grown) on a recover path. The Repository returns one per
// real transition; the application layer (derived-weakness projector) stamps the
// envelope identity (tenant + learner, held in its own scope) and publishes
// weakness.grown.v1, so this DTO carries only the edge-specific reward facts.
type GrownEdge struct {
	ID            string    // learner_weakness row id (UUIDv7)
	ConceptKey    string    // normalised slug
	ConceptLabel  string    // learner-facing phrase
	FinalStrength float64   // shakiness at grow time (<= MasteredStrengthThreshold)
	Tags          []string  // cross-cutting labels for reward / analytics rollup
	GrownAt       time.Time // when the transition was persisted
}

// RecoverySource values identify which recover path closed the gap; they
// populate the weakness.grown.v1 recovery_source field (ADR-196). Defined in the
// domain so the projector + every future reward consumer share one source of
// truth (and so a rename trips grown_test.go, not a silent wire break).
const (
	// RecoverySourceConceptKey — sustained topic mastery / Ebbinghaus
	// auto-recovery (RecoverByConceptKey).
	RecoverySourceConceptKey = "concept_key"
	// RecoverySourceDrillAtom — practising a suggested drill atom
	// (RecoverByDrillAtomID; closes the upload→practice→grow loop, CHO-1895).
	RecoverySourceDrillAtom = "drill_atom"
)
