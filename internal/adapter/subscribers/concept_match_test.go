// concept_match_test.go — ADR-238 M-D2 (goal-scoped Diagnose ingest resolver).
// Unit tests for the PURE nearest-concept matcher: exact concept_key hit first
// (free + deterministic), else cosine-similarity argmax gated by a threshold,
// then ADR-238 D2's SOFT bias toward the entry concept's subtree.
// Domain-grade logic, so covered exhaustively (exact-key precedence, cosine hit
// at/just-below threshold, argmax tie-break, empty/zero-norm/mismatched guards,
// and every way the D2 bias must NOT change the matched/unmatched decision).
package subscribers

import (
	"math"
	"testing"
)

// unitAtCos builds a unit vector whose cosine against the edge vector {1,0} is
// exactly c. Lets each test state the similarity it means instead of hiding it
// behind hand-computed components.
func unitAtCos(c float64) []float32 {
	return []float32{float32(c), float32(math.Sqrt(1 - c*c))}
}

// edgeVec is the reference edge embedding every cosine test compares against.
func edgeVec() []float32 { return []float32{1, 0} }

func TestMatchConcept_ExactKeyHitBeatsCosine(t *testing.T) {
	// Candidate A is orthogonal to the edge embedding (cosine 0), but its
	// concept_key equals the (normalised) edge key — exact-key wins regardless.
	cands := []ConceptCandidate{
		{ConceptID: "cA", ConceptKey: "alpha", Embedding: []float32{0, 1, 0}},
		{ConceptID: "cB", ConceptKey: "beta", Embedding: []float32{1, 0, 0}}, // aligned (higher cosine)
	}
	id, key, ok := matchConcept("Alpha", []float32{1, 0, 0}, cands, 0.55, 0)
	if !ok || id != "cA" || key != "alpha" {
		t.Fatalf("exact-key match = (%q,%q,%v); want (cA,alpha,true)", id, key, ok)
	}
}

func TestMatchConcept_ExactKeyEvenWithEmptyEmbedding(t *testing.T) {
	// No embedding at all, but the key matches exactly → still a match (free).
	cands := []ConceptCandidate{{ConceptID: "cA", ConceptKey: "causes-of-flooding", Embedding: nil}}
	id, key, ok := matchConcept("Causes of Flooding", nil, cands, 0.9, 0)
	if !ok || id != "cA" || key != "causes-of-flooding" {
		t.Fatalf("exact-key/no-embed match = (%q,%q,%v); want (cA,causes-of-flooding,true)", id, key, ok)
	}
}

func TestMatchConcept_CosineHitReturnsArgmaxKey(t *testing.T) {
	cands := []ConceptCandidate{
		{ConceptID: "cA", ConceptKey: "kA", Embedding: []float32{1, 0, 0}},
		{ConceptID: "cB", ConceptKey: "kB", Embedding: []float32{0, 1, 0}},
	}
	// [0.9,0.1,0] is much closer to A → cosine ~0.99.
	id, key, ok := matchConcept("no-key-overlap", []float32{0.9, 0.1, 0}, cands, 0.55, 0)
	if !ok || id != "cA" || key != "kA" {
		t.Fatalf("cosine match = (%q,%q,%v); want (cA,kA,true)", id, key, ok)
	}
}

func TestMatchConcept_PicksHighestCosineNotFirst(t *testing.T) {
	// The highest-cosine candidate is SECOND in the slice — argmax, not first-hit.
	cands := []ConceptCandidate{
		{ConceptID: "cA", ConceptKey: "kA", Embedding: []float32{1, 0, 0}}, // cosine ~0.10
		{ConceptID: "cB", ConceptKey: "kB", Embedding: []float32{0, 1, 0}}, // cosine ~0.99
	}
	id, _, ok := matchConcept("x", []float32{0.1, 0.99, 0}, cands, 0.55, 0)
	if !ok || id != "cB" {
		t.Fatalf("argmax = (%q,%v); want (cB,true)", id, ok)
	}
}

func TestMatchConcept_ThresholdInclusiveAtBoundary(t *testing.T) {
	// Identical vectors → cosine 1.0; minCosine 1.0 must be inclusive (>=).
	cands := []ConceptCandidate{{ConceptID: "cA", ConceptKey: "kA", Embedding: []float32{1, 0, 0}}}
	if _, _, ok := matchConcept("x", []float32{1, 0, 0}, cands, 1.0, 0); !ok {
		t.Fatal("cosine 1.0 at minCosine 1.0 must match (>= is inclusive)")
	}
}

func TestMatchConcept_BelowThresholdNoMatch(t *testing.T) {
	// Orthogonal → cosine 0.0, below the 0.55 floor.
	cands := []ConceptCandidate{{ConceptID: "cA", ConceptKey: "kA", Embedding: []float32{0, 1, 0}}}
	if id, key, ok := matchConcept("x", []float32{1, 0, 0}, cands, 0.55, 0); ok {
		t.Fatalf("below-threshold matched = (%q,%q); want no match", id, key)
	}
}

func TestMatchConcept_EmptyCandidates(t *testing.T) {
	if _, _, ok := matchConcept("alpha", []float32{1, 0, 0}, nil, 0.55, 0); ok {
		t.Fatal("empty candidates must never match")
	}
}

func TestMatchConcept_ZeroNormEdgeEmbeddingNoMatch(t *testing.T) {
	// Zero-norm edge vector + no exact key → no cosine, no match (never NaN/panic).
	cands := []ConceptCandidate{{ConceptID: "cA", ConceptKey: "kA", Embedding: []float32{1, 0, 0}}}
	if _, _, ok := matchConcept("no-key", []float32{0, 0, 0}, cands, 0.0, 0); ok {
		t.Fatal("zero-norm edge embedding must not match on cosine")
	}
}

func TestMatchConcept_MismatchedVectorLengthsSkipped(t *testing.T) {
	// The only candidate has a differently-sized vector → skipped → no match.
	cands := []ConceptCandidate{{ConceptID: "cA", ConceptKey: "kA", Embedding: []float32{1, 0}}}
	if _, _, ok := matchConcept("no-key", []float32{1, 0, 0}, cands, 0.5, 0); ok {
		t.Fatal("length-mismatched candidate must be skipped, not matched")
	}
}

func TestMatchConcept_ZeroNormCandidateSkipped(t *testing.T) {
	// A zero-norm candidate is skipped; the good one still wins.
	cands := []ConceptCandidate{
		{ConceptID: "cZero", ConceptKey: "kZ", Embedding: []float32{0, 0, 0}},
		{ConceptID: "cA", ConceptKey: "kA", Embedding: []float32{1, 0, 0}},
	}
	id, _, ok := matchConcept("no-key", []float32{1, 0, 0}, cands, 0.55, 0)
	if !ok || id != "cA" {
		t.Fatalf("zero-norm candidate handling = (%q,%v); want (cA,true)", id, ok)
	}
}

// --- ADR-238 D2: the entry concept is a SOFT bias, never a filter ------------

func TestMatchConcept_PreferredWinsNearTie(t *testing.T) {
	// cA scores marginally higher, but cB is inside the entry concept's subtree
	// and the gap (0.02) is inside the 0.03 tie band → the hint breaks the tie.
	cands := []ConceptCandidate{
		{ConceptID: "cA", ConceptKey: "kA", Embedding: unitAtCos(0.80)},
		{ConceptID: "cB", ConceptKey: "kB", Embedding: unitAtCos(0.78), Preferred: true},
	}
	id, key, ok := matchConcept("no-key", edgeVec(), cands, 0.55, 0.03)
	if !ok || id != "cB" || key != "kB" {
		t.Fatalf("near-tie with hint = (%q,%q,%v); want (cB,kB,true) - the entry subtree breaks a tie", id, key, ok)
	}
}

func TestMatchConcept_PreferredLosesOutsideTieBand(t *testing.T) {
	// The hinted candidate is decisively worse (0.20 apart, band 0.03). D2 says
	// bias, NEVER a hard filter - a clearly better off-subtree concept still wins.
	cands := []ConceptCandidate{
		{ConceptID: "cA", ConceptKey: "kA", Embedding: unitAtCos(0.80)},
		{ConceptID: "cB", ConceptKey: "kB", Embedding: unitAtCos(0.60), Preferred: true},
	}
	id, _, ok := matchConcept("no-key", edgeVec(), cands, 0.55, 0.03)
	if !ok || id != "cA" {
		t.Fatalf("outside-band hint = (%q,%v); want (cA,true) - the hint must not override a clearly better match", id, ok)
	}
}

func TestMatchConcept_BiasNeverTurnsAMatchIntoANonMatch(t *testing.T) {
	// THE load-bearing guard. The hinted candidate sits BELOW the floor; the
	// unhinted one clears it. A bias applied before the floor would elect the
	// hinted candidate and then fail the floor, silently DROPPING a weakness -
	// the exact class of defect ADR-238 exists to close. The floor comes first,
	// so the match/no-match decision is identical to the unbiased result.
	cands := []ConceptCandidate{
		{ConceptID: "cA", ConceptKey: "kA", Embedding: unitAtCos(0.80)},
		{ConceptID: "cB", ConceptKey: "kB", Embedding: unitAtCos(0.60), Preferred: true},
	}
	// Band is deliberately wide enough (0.30) to span both candidates.
	id, _, ok := matchConcept("no-key", edgeVec(), cands, 0.70, 0.30)
	if !ok || id != "cA" {
		t.Fatalf("sub-floor hint = (%q,%v); want (cA,true) - a hint below the floor must never suppress a valid match", id, ok)
	}
}

func TestMatchConcept_ZeroTieBandDisablesBiasEntirely(t *testing.T) {
	// tieBand 0 is the kill-switch: pure argmax, byte-identical to pre-D2.
	cands := []ConceptCandidate{
		{ConceptID: "cA", ConceptKey: "kA", Embedding: unitAtCos(0.80)},
		{ConceptID: "cB", ConceptKey: "kB", Embedding: unitAtCos(0.78), Preferred: true},
	}
	id, _, ok := matchConcept("no-key", edgeVec(), cands, 0.55, 0)
	if !ok || id != "cA" {
		t.Fatalf("tieBand=0 = (%q,%v); want (cA,true) - a zero band must disable the bias", id, ok)
	}
}

func TestMatchConcept_HighestCosineWinsAmongPreferred(t *testing.T) {
	// Several hinted candidates inside the band → the best of them, not the first.
	cands := []ConceptCandidate{
		{ConceptID: "cA", ConceptKey: "kA", Embedding: unitAtCos(0.76), Preferred: true},
		{ConceptID: "cB", ConceptKey: "kB", Embedding: unitAtCos(0.78), Preferred: true},
		{ConceptID: "cC", ConceptKey: "kC", Embedding: unitAtCos(0.80)},
	}
	id, _, ok := matchConcept("no-key", edgeVec(), cands, 0.55, 0.05)
	if !ok || id != "cB" {
		t.Fatalf("multi-preferred = (%q,%v); want (cB,true) - argmax among the hinted ones", id, ok)
	}
}

func TestMatchConcept_ExactKeyIgnoresBias(t *testing.T) {
	// Stage 1 is free + deterministic and outranks the bias: an exact key hit on
	// an UNhinted candidate still wins over a hinted cosine candidate.
	cands := []ConceptCandidate{
		{ConceptID: "cA", ConceptKey: "sprint-backlog", Embedding: unitAtCos(0.10)},
		{ConceptID: "cB", ConceptKey: "kB", Embedding: unitAtCos(0.99), Preferred: true},
	}
	id, key, ok := matchConcept("Sprint Backlog", edgeVec(), cands, 0.55, 0.90)
	if !ok || id != "cA" || key != "sprint-backlog" {
		t.Fatalf("exact key vs hint = (%q,%q,%v); want (cA,sprint-backlog,true)", id, key, ok)
	}
}

func TestMatchConcept_BiasIsOrderIndependent(t *testing.T) {
	// The winner must not depend on slice order - a near-tie chain must not let
	// iteration order decide which concept a learner's weakness lands on.
	a := ConceptCandidate{ConceptID: "cA", ConceptKey: "kA", Embedding: unitAtCos(0.80)}
	b := ConceptCandidate{ConceptID: "cB", ConceptKey: "kB", Embedding: unitAtCos(0.78), Preferred: true}
	fwd, _, okF := matchConcept("no-key", edgeVec(), []ConceptCandidate{a, b}, 0.55, 0.03)
	rev, _, okR := matchConcept("no-key", edgeVec(), []ConceptCandidate{b, a}, 0.55, 0.03)
	if !okF || !okR || fwd != rev {
		t.Fatalf("order dependence: forward=%q(%v) reversed=%q(%v); want the same winner", fwd, okF, rev, okR)
	}
}

func TestMatchConcept_NoPreferredCandidateBehavesLikeArgmax(t *testing.T) {
	// A non-zero band with NO hinted candidate must not perturb plain argmax.
	cands := []ConceptCandidate{
		{ConceptID: "cA", ConceptKey: "kA", Embedding: unitAtCos(0.78)},
		{ConceptID: "cB", ConceptKey: "kB", Embedding: unitAtCos(0.80)},
	}
	id, _, ok := matchConcept("no-key", edgeVec(), cands, 0.55, 0.03)
	if !ok || id != "cB" {
		t.Fatalf("band with no hint = (%q,%v); want (cB,true) - plain argmax", id, ok)
	}
}
