// concept_match.go — ADR-238 M-D2 (goal-scoped Diagnose ingest resolver): the
// PURE nearest-concept matcher. Given one analysed Growth-Edge (its concept key
// + precomputed embedding) and the candidate ConceptNodes of the upload's goal
// sub-tree, decide which on-map node (if any) the edge resolves to.
//
// Three stages, cheapest-first:
//  1. EXACT concept_key — when the normalised edge key equals a candidate's
//     concept_key the analyser already nailed an on-map label; take it (free,
//     robust, deterministic; independent of embedding quality).
//  2. COSINE argmax ABOVE THE FLOOR - the highest cosine-similarity candidate,
//     but only among those clearing minCosine. Below the floor the edge stays
//     goal-level (unmatched) rather than being force-snapped to a weak relative.
//  3. ADR-238 D2 SOFT BIAS - among the candidates that already cleared the floor
//     AND sit within tieBand of the stage-2 argmax, prefer one inside the entry
//     concept's sub-tree (Preferred). The concept drawer is a soft ENTRY, so it
//     may break a near-tie; it is never a filter.
//
// Why the floor runs BEFORE the bias (load-bearing): applying the bias first
// could elect a Preferred candidate that then fails the floor, silently DROPPING
// a weakness - precisely the class of defect ADR-238 exists to close. Ordered
// this way, the bias can only change WHICH concept is chosen, never WHETHER a
// match happened: the matched/unmatched decision is identical to the unbiased
// result for every input. tieBand <= 0 disables the bias entirely (kill-switch).
//
// The stage-3 scan is over the whole confident set rather than incremental, so
// the winner never depends on candidate slice order (a near-tie chain would
// otherwise let repo ordering decide where a learner's weakness lands).
//
// PURITY: stdlib-only, no I/O, no clock — this is domain-grade logic and is unit
// tested exhaustively. Every degenerate input (no candidates, empty/zero-norm/
// mismatched-length vectors) yields "no match", never a panic or NaN.
package subscribers

import (
	"math"

	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// ConceptCandidate is one on-map ConceptNode the resolver considers for an edge:
// its id, its stable concept_key (the shared slug vocabulary), its Title
// embedding (precomputed once by the resolver), and whether it lies inside the
// upload's ENTRY concept sub-tree (ADR-238 D2's soft emphasis hint).
type ConceptCandidate struct {
	ConceptID  string
	ConceptKey string
	Embedding  []float32
	// Preferred marks a candidate inside the entry concept's sub-tree. It is a
	// TIE-BREAK ONLY (stage 3) - it never admits a candidate the floor rejected
	// and never excludes one, so it cannot drop a weakness.
	Preferred bool
}

// matchConcept resolves an analysed edge to its best on-map ConceptCandidate.
// Returns (conceptID, conceptKey, true) on a confident match — the caller stamps
// UpsertInput.TargetConceptID = conceptID AND aligns UpsertInput.ConceptKey =
// conceptKey. Returns ("","",false) when nothing is confident enough (the edge is
// then persisted goal-level, unchanged).
func matchConcept(edgeKey string, edgeEmbedding []float32, candidates []ConceptCandidate, minCosine, tieBand float64) (conceptID, conceptKey string, matched bool) {
	if len(candidates) == 0 {
		return "", "", false
	}

	// (1) EXACT key — the analyser already produced an on-map label. Compared on
	// the shared normalised slug so casing/punctuation never blocks a real hit.
	// Outranks the D2 bias: an exact key is stronger evidence than an entry hint.
	if normEdge := lw.NormalizeConceptKey(edgeKey); normEdge != "" {
		for _, c := range candidates {
			if c.ConceptKey != "" && c.ConceptKey == normEdge {
				return c.ConceptID, c.ConceptKey, true
			}
		}
	}

	// scored pairs a candidate index with its cosine against the edge.
	type scored struct {
		idx int
		cos float64
	}

	// (2) COSINE argmax over candidates with a comparable, non-degenerate vector
	// that CLEAR THE FLOOR. Collect the confident set so stage 3 needs no recompute.
	confident := make([]scored, 0, len(candidates))
	best := scored{idx: -1, cos: math.Inf(-1)}
	for i := range candidates {
		cos, ok := cosineSimilarity(edgeEmbedding, candidates[i].Embedding)
		if !ok {
			continue // empty / zero-norm / length-mismatched → not comparable
		}
		if cos < minCosine {
			continue // below the confidence floor → not a candidate at all
		}
		confident = append(confident, scored{idx: i, cos: cos})
		if cos > best.cos {
			best = scored{idx: i, cos: cos}
		}
	}
	if best.idx < 0 {
		return "", "", false
	}

	// (3) ADR-238 D2 soft bias - break a near-tie toward the entry sub-tree. Only
	// ever reorders WITHIN the already-confident set, so matched-ness is fixed.
	if tieBand > 0 {
		bestPref := scored{idx: -1, cos: math.Inf(-1)}
		for _, s := range confident {
			if !candidates[s.idx].Preferred {
				continue
			}
			if s.cos < best.cos-tieBand {
				continue // decisively worse than the argmax → the hint must not win
			}
			if s.cos > bestPref.cos {
				bestPref = s
			}
		}
		if bestPref.idx >= 0 {
			best = bestPref
		}
	}

	c := candidates[best.idx]
	return c.ConceptID, c.ConceptKey, true
}

// cosineSimilarity returns dot(a,b)/(‖a‖·‖b‖) and ok=true, or ok=false when the
// vectors are empty, differ in length, or either has zero norm (division guard).
func cosineSimilarity(a, b []float32) (float64, bool) {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0, false
	}
	var dot, normA, normB float64
	for i := range a {
		af, bf := float64(a[i]), float64(b[i])
		dot += af * bf
		normA += af * af
		normB += bf * bf
	}
	if normA == 0 || normB == 0 {
		return 0, false
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB)), true
}
