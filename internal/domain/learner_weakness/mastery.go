// mastery.go — the ONE canonical "which concepts has this learner mastered"
// derivation, shared by the A+ Goal %-ring read path (goals_handler.go) and the
// goal-graduation subscriber (CHO-1962). Keeping a single impl is the debt-free
// requirement: the subscriber MUST graduate a Goal at exactly the share the FE
// renders as 100%, so both sides compute mastered-vs-total identically here.
//
// Pure domain service over the read port — no infra import. Error policy is the
// CALLER's: the read path swallows it (fail-soft, a 0%-ring never breaks a list);
// the subscriber propagates it (fail-loud → NACK → redelivery).
package learner_weakness

import "context"

// GrownEdgeLister is the narrow read capability MasteredConceptKeys needs — the
// full Repository satisfies it, but a subscriber/handler (and tests) can depend
// on just this slice.
type GrownEdgeLister interface {
	ListAll(ctx context.Context, q ListQuery) ([]LearnerWeakness, error)
}

// MasteredConceptKeys returns the set of NORMALISED concept keys the learner has
// mastered (Growth Edges in the "grown" state). A nil lister yields an empty set
// (the fail-soft default at the source); a read error is returned for the caller
// to handle. Mastered edges are soft-archived to grown, so IncludeGrown is set.
func MasteredConceptKeys(ctx context.Context, lister GrownEdgeLister, tenantID, gcid string) (map[string]bool, error) {
	mastered := map[string]bool{}
	if lister == nil {
		return mastered, nil
	}
	edges, err := lister.ListAll(ctx, ListQuery{
		TenantID:     tenantID,
		LearnerGCID:  gcid,
		IncludeGrown: true, // mastered edges are soft-archived to grown; must opt in
	})
	if err != nil {
		return nil, err
	}
	for _, e := range edges {
		if e.IsGrown() {
			mastered[NormalizeConceptKey(e.ConceptKey)] = true
		}
	}
	return mastered, nil
}

// CountMastered counts how many of conceptSet's members are in the mastered set,
// normalising each concept the SAME way the set was keyed (NormalizeConceptKey).
// This is the exact loop the A+ %-ring uses, factored out so the read path and
// the graduation subscriber can never drift on the count.
func CountMastered(conceptSet []string, mastered map[string]bool) int {
	n := 0
	for _, c := range conceptSet {
		if mastered[NormalizeConceptKey(c)] {
			n++
		}
	}
	return n
}
