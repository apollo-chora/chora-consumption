// overlay_test.go — W5: the read-time Growth-Edge overlay index the KG paint
// uses (annotate nodes by label/tag match; NO node mutation).
package learner_weakness

import (
	"testing"
	"time"
)

func overlayEdge(t *testing.T, label string, strength float64, tags ...string) LearnerWeakness {
	t.Helper()
	w, err := New(UpsertInput{
		TenantID:     "11111111-1111-7111-8111-111111111111",
		LearnerGCID:  "00000000-0000-7000-8000-000000001999",
		ConceptLabel: label,
		Embedding:    []float32{0.1},
		Strength:     strength,
		Tags:         tags,
		Source:       SourceDerived,
		Now:          time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return *w
}

func TestOverlayIndex_MatchesByConceptKeyAndTags(t *testing.T) {
	ix := BuildOverlayIndex([]LearnerWeakness{
		overlayEdge(t, "Multiplication Tables", 0.8, "arithmetic"),
		overlayEdge(t, "European Capitals", 0.5),
	})

	// Label slug match (case/punctuation-insensitive).
	if e := ix.Match("multiplication   TABLES!"); e == nil || e.ConceptKey != "multiplication-tables" {
		t.Fatalf("label match = %+v", e)
	}
	// Tag slug match.
	if e := ix.Match("Arithmetic"); e == nil || e.ConceptKey != "multiplication-tables" {
		t.Fatalf("tag match = %+v", e)
	}
	// First matching label wins across the variadic list.
	if e := ix.Match("nope", "european capitals"); e == nil || e.ConceptKey != "european-capitals" {
		t.Fatalf("variadic match = %+v", e)
	}
	if e := ix.Match("unrelated"); e != nil {
		t.Fatalf("no-match must be nil, got %+v", e)
	}
	if ix.Empty() {
		t.Fatal("index with edges must not be Empty")
	}
}

func TestOverlayIndex_StrongestWinsOnCollision(t *testing.T) {
	ix := BuildOverlayIndex([]LearnerWeakness{
		overlayEdge(t, "Fractions", 0.3),
		overlayEdge(t, "Decimal Division", 0.9, "fractions"),
	})
	if e := ix.Match("fractions"); e == nil || e.Strength != 0.9 {
		t.Fatalf("collision must keep the strongest, got %+v", e)
	}
}

func TestOverlayIndex_SkipsGrownAndDeleted(t *testing.T) {
	grown := overlayEdge(t, "Mastered Topic", 0.05) // statusFor → grown
	deleted := overlayEdge(t, "Dismissed Topic", 0.8)
	now := time.Now().UTC()
	deleted.SoftDelete(now)

	ix := BuildOverlayIndex([]LearnerWeakness{grown, deleted})
	if e := ix.Match("mastered topic"); e != nil {
		t.Fatalf("grown edges must not paint, got %+v", e)
	}
	if e := ix.Match("dismissed topic"); e != nil {
		t.Fatalf("soft-deleted edges must not paint, got %+v", e)
	}
	if !ix.Empty() {
		t.Fatal("index of only grown/deleted edges must be Empty")
	}
}

func TestOverlayIndex_NilSafe(t *testing.T) {
	var ix *OverlayIndex
	if e := ix.Match("anything"); e != nil {
		t.Fatal("nil index must match nothing")
	}
	if !ix.Empty() {
		t.Fatal("nil index must be Empty")
	}
	if e := BuildOverlayIndex(nil).Match("anything"); e != nil {
		t.Fatal("empty index must match nothing")
	}
}

// overlayEdgeWithTarget builds an edge carrying an explicit resolved on-map
// TargetConceptID (ADR-238) alongside a DELIBERATELY unrelated concept_key, so
// an id-join can be proven independent of the concept_key slug join.
func overlayEdgeWithTarget(t *testing.T, label, conceptKey, targetConceptID string, strength float64) LearnerWeakness {
	t.Helper()
	w, err := New(UpsertInput{
		TenantID:        "11111111-1111-7111-8111-111111111111",
		LearnerGCID:     "00000000-0000-7000-8000-000000001999",
		ConceptKey:      conceptKey,
		ConceptLabel:    label,
		Embedding:       []float32{0.1},
		Strength:        strength,
		TargetConceptID: targetConceptID,
		Source:          SourceDerived,
		Now:             time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return *w
}

// TestOverlayIndex_MatchConceptID_IdJoin pins ADR-238 M-D3: the read-side id-join
// resolves an edge by its resolved on-map TargetConceptID, independent of any
// slug. This lights the correct node even when the concept_key slug no longer
// matches the live node title (rename / merge / pre-0098 edges).
func TestOverlayIndex_MatchConceptID_IdJoin(t *testing.T) {
	const nodeID = "01980000-0000-7000-8000-0000000000aa"
	ix := BuildOverlayIndex([]LearnerWeakness{
		overlayEdgeWithTarget(t, "Old Renamed Label", "old-renamed-slug", nodeID, 0.7),
	})
	e := ix.MatchConceptID(nodeID)
	if e == nil || e.TargetConceptID != nodeID {
		t.Fatalf("id-join must resolve the edge by TargetConceptID, got %+v", e)
	}
	// The query is trimmed before lookup.
	if ix.MatchConceptID("  "+nodeID+"  ") == nil {
		t.Fatal("MatchConceptID must trim the query")
	}
	// Unknown + blank ids resolve nothing.
	if ix.MatchConceptID("no-such-node") != nil {
		t.Fatal("unknown id must resolve nil")
	}
	if ix.MatchConceptID("") != nil || ix.MatchConceptID("   ") != nil {
		t.Fatal("blank id must resolve nil")
	}
	if ix.Empty() {
		t.Fatal("an index holding an id-joined edge must not be Empty")
	}
}

// TestOverlayIndex_MatchConceptID_StrongestWinsOnCollision mirrors the byKey
// strongest-wins rule for the id-join map.
func TestOverlayIndex_MatchConceptID_StrongestWinsOnCollision(t *testing.T) {
	const nodeID = "01980000-0000-7000-8000-0000000000bb"
	ix := BuildOverlayIndex([]LearnerWeakness{
		overlayEdgeWithTarget(t, "Weak Evidence", "weak", nodeID, 0.3),
		overlayEdgeWithTarget(t, "Strong Evidence", "strong", nodeID, 0.9),
	})
	if e := ix.MatchConceptID(nodeID); e == nil || e.Strength != 0.9 {
		t.Fatalf("id collision must keep the strongest, got %+v", e)
	}
}

// TestOverlayIndex_MatchConceptID_SkipsGrownAndDeleted: grown/soft-deleted edges
// never enter the id-join, exactly as they never enter byKey.
func TestOverlayIndex_MatchConceptID_SkipsGrownAndDeleted(t *testing.T) {
	grown := overlayEdgeWithTarget(t, "Mastered", "mastered", "node-grown", 0.05) // statusFor → grown
	deleted := overlayEdgeWithTarget(t, "Dismissed", "dismissed", "node-del", 0.8)
	deleted.SoftDelete(time.Now().UTC())

	ix := BuildOverlayIndex([]LearnerWeakness{grown, deleted})
	if ix.MatchConceptID("node-grown") != nil {
		t.Fatal("grown edge must not id-join")
	}
	if ix.MatchConceptID("node-del") != nil {
		t.Fatal("soft-deleted edge must not id-join")
	}
	if !ix.Empty() {
		t.Fatal("index of only grown/deleted edges must be Empty")
	}
}

// TestOverlayIndex_MatchConceptID_NilSafe: nil + empty indexes resolve nothing.
func TestOverlayIndex_MatchConceptID_NilSafe(t *testing.T) {
	var ix *OverlayIndex
	if ix.MatchConceptID("anything") != nil {
		t.Fatal("nil index must resolve no id")
	}
	if BuildOverlayIndex(nil).MatchConceptID("anything") != nil {
		t.Fatal("empty index must resolve no id")
	}
}

// TestOverlayIndex_Empty_ReflectsIDOnlyOverlay: an overlay whose ONLY paintable
// entries are id-joins (byKey empty) must NOT report Empty — otherwise the paint
// short-circuits and the id-join never fires.
func TestOverlayIndex_Empty_ReflectsIDOnlyOverlay(t *testing.T) {
	e := overlayEdgeWithTarget(t, "x", "x", "node-1", 0.5)
	ix := &OverlayIndex{byTargetID: map[string]*LearnerWeakness{"node-1": &e}}
	if ix.Empty() {
		t.Fatal("an id-only overlay must NOT report Empty")
	}
	if ix.MatchConceptID("node-1") == nil {
		t.Fatal("an id-only overlay must still resolve its id")
	}
}

// TestOverlayIndex_MatchesByConceptLabelWhenKeyDrifts pins KG #8 (CHO-2065):
// the analyser mints a short / domain-prefixed concept_key ("binary-search")
// that does NOT slug-match the concept NODE title the paint joins on
// (concept_graph_handler paints via matchFE(node.Title)), but the human
// concept_label ("Binary Search Algorithm") DOES equal the title. The edge
// must therefore be reachable by its concept_label slug — else the node stays
// dark, focalDiagnosis() returns null, and the concept-scoped dose deep-link
// falls back to generic. concept_key stays the canonical (gcid,concept_key)
// upsert/idempotency key (unchanged) — this is a read-side index widening only.
func TestOverlayIndex_MatchesByConceptLabelWhenKeyDrifts(t *testing.T) {
	drift, err := New(UpsertInput{
		TenantID:     "11111111-1111-7111-8111-111111111111",
		LearnerGCID:  "00000000-0000-7000-8000-000000001999",
		ConceptKey:   "binary-search",           // short analyser key (persisted verbatim)
		ConceptLabel: "Binary Search Algorithm", // == the concept NODE title
		Embedding:    []float32{0.1},
		Strength:     1.0,
		Source:       SourceDerived,
		Now:          time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if drift.ConceptKey != "binary-search" {
		t.Fatalf("precondition: concept_key must stay the analyser slug, got %q", drift.ConceptKey)
	}
	ix := BuildOverlayIndex([]LearnerWeakness{*drift})

	// The fix: reachable by the node title == concept_label slug.
	if e := ix.Match("Binary Search Algorithm"); e == nil || e.ConceptKey != "binary-search" {
		t.Fatalf("must match by concept_label slug, got %+v", e)
	}
	// Regression: still reachable by the (drifted) concept_key slug.
	if e := ix.Match("binary-search"); e == nil {
		t.Fatal("must still match by concept_key")
	}
}
