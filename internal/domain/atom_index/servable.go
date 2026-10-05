package atom_index

// Servable reports whether an atom may be put in front of a learner: it is
// Playable (published, not soft-deleted) AND IsAnswerable (a gradable MCQ or an
// open-ended question).
//
// This is the SINGLE definition of the predicate ADR-242 §1.2 names and ADR-243
// D4 requires Source A to reuse. It had three hand-written copies before
// (the dose universe, the goal-scoped dose tranche, and the concept-attachment
// candidate source), which is a fork waiting to happen: a caller that keeps the
// Playable half and drops IsAnswerable offers an atom that can be opened but
// never graded, so it produces no signal, and the concept it is attached to can
// never light up on the map with nothing to tell the learner why.
//
// A nil projection is not servable. Every caller reaches this straight off a
// repository read where (nil, nil) is the documented "unprojected" answer, so
// nil is a reachable input and the honest reading of it is "nothing to serve"
// rather than a panic.
func (a *AtomIndex) Servable() bool {
	return a != nil && a.Playable() && a.IsAnswerable()
}
