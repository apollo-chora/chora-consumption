// kg_canvas_growth_overlay_test.go — ADR-204 Slice A-BE: the CANVAS fog DTOs
// must carry the W5 Growth-Edge overlay (camelCase `growthEdge` per neighbor +
// `focalGrowthEdge`) so the FE can paint weak terrain on the hexagon canvas.
//
// Mirrors TestKGFog_GrowthEdgeOverlay (the LEGACY snake_case fog path in
// kg_growth_overlay_test.go) for the canvas camelCase + `{data}` envelope shape.
// Strictly read-time + fail-soft: an overlay miss / nil repo / read error must
// NEVER break the canvas read — the field is simply omitted.
//
// Reuses kgLWStub + kgOverlayEdge (kg_growth_overlay_test.go), kgCanvasSeed +
// kgCanvasSixNeighbors (kg_canvas_handler_test.go), kg* constants + newKGRequest.
package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

func TestCanvasHexagon_GrowthEdgeOverlay(t *testing.T) {
	srv := NewExtServer(nil)
	srv.LearnerWeakness = &kgLWStub{items: []lw.LearnerWeakness{
		kgOverlayEdge(t, "Multiplication Tables", 0.8),
		kgOverlayEdge(t, "Fractions", 0.6),
	}}
	// Focal atom (kgAtomA — the seed/focal) carries topic tags so the focal
	// annotation resolves via atom_index, exactly like the legacy fog path.
	if err := srv.AtomIndex.Save(context.Background(), &atom_index.AtomIndex{
		AtomID:    kgAtomA,
		TenantID:  kgTenantID,
		AtomType:  "mcq",
		TopicTags: []string{"fractions"},
	}); err != nil {
		t.Fatal(err)
	}

	neighbors := kgCanvasSixNeighbors()
	neighbors[0].FogLabel = "Multiplication Tables" // slug-matches the 0.8 edge
	cid, eid, _, _ := kgCanvasSeed(t, srv, neighbors)

	r := newKGRequest(http.MethodGet,
		"/v1/me/knowledge-graph/clusters/"+cid+"/explorations/"+eid+"/hexagon", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("hexagon = %d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			FocalGrowthEdge *struct {
				EdgeID     string  `json:"edgeId"`
				ConceptKey string  `json:"conceptKey"`
				Strength   float64 `json:"strength"`
			} `json:"focalGrowthEdge"`
			Neighbors []struct {
				AtomID     string `json:"atomId"`
				GrowthEdge *struct {
					EdgeID     string  `json:"edgeId"`
					ConceptKey string  `json:"conceptKey"`
					Strength   float64 `json:"strength"`
				} `json:"growthEdge"`
			} `json:"neighbors"`
		} `json:"data"`
	}
	if err := decodeJSON(w, &resp); err != nil {
		t.Fatal(err)
	}

	// Focal painted via its atom_index topic tags (camelCase shape).
	if fe := resp.Data.FocalGrowthEdge; fe == nil || fe.ConceptKey != "fractions" || fe.Strength != 0.6 || fe.EdgeID == "" {
		t.Fatalf("focalGrowthEdge = %+v, want fractions @0.6 with edgeId", fe)
	}

	// Exactly the matching neighbor is annotated; the rest omit the field.
	annotated := 0
	for _, n := range resp.Data.Neighbors {
		if n.GrowthEdge == nil {
			continue
		}
		annotated++
		if n.GrowthEdge.ConceptKey != "multiplication-tables" || n.GrowthEdge.Strength != 0.8 || n.GrowthEdge.EdgeID == "" {
			t.Fatalf("neighbor growthEdge = %+v, want multiplication-tables @0.8 with edgeId", n.GrowthEdge)
		}
	}
	if annotated != 1 {
		t.Fatalf("annotated neighbors = %d, want exactly the matching one", annotated)
	}
}

func TestCanvasHexagon_NoGrowthEdgeWhenRepoNilOrErr(t *testing.T) {
	for name, repo := range map[string]lw.Repository{
		"nil repo": nil,
		"list err": &kgLWStub{listErr: errors.New("boom")},
	} {
		t.Run(name, func(t *testing.T) {
			srv := NewExtServer(nil)
			srv.LearnerWeakness = repo
			neighbors := kgCanvasSixNeighbors()
			neighbors[0].FogLabel = "Multiplication Tables"
			cid, eid, _, _ := kgCanvasSeed(t, srv, neighbors)

			r := newKGRequest(http.MethodGet,
				"/v1/me/knowledge-graph/clusters/"+cid+"/explorations/"+eid+"/hexagon", nil)
			w := httptest.NewRecorder()
			srv.Routes().ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("overlay failure must not break the canvas read: %d body=%s", w.Code, w.Body.String())
			}
			var resp struct {
				Data struct {
					FocalGrowthEdge *struct{} `json:"focalGrowthEdge"`
					Neighbors       []struct {
						GrowthEdge *struct{} `json:"growthEdge"`
					} `json:"neighbors"`
				} `json:"data"`
			}
			if err := decodeJSON(w, &resp); err != nil {
				t.Fatal(err)
			}
			if resp.Data.FocalGrowthEdge != nil {
				t.Errorf("focalGrowthEdge present despite no overlay: %+v", resp.Data.FocalGrowthEdge)
			}
			for i, n := range resp.Data.Neighbors {
				if n.GrowthEdge != nil {
					t.Errorf("neighbor[%d] growthEdge present despite no overlay", i)
				}
			}
		})
	}
}
