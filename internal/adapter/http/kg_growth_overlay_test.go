// kg_growth_overlay_test.go — W5: Growth-Edge read-overlay on the KG read
// paths (cluster cards + hexagon fog). Annotation only — no node mutation;
// overlay failures NEVER break the KG read (fail-soft).
package http

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// kgLWStub is a settable lw.Repository double for the overlay reads.
type kgLWStub struct {
	items   []lw.LearnerWeakness
	listErr error
	gotQ    lw.ListQuery
}

func (s *kgLWStub) Upsert(context.Context, lw.UpsertInput) (lw.UpsertResult, error) {
	return lw.UpsertResult{}, nil
}
func (s *kgLWStub) List(_ context.Context, q lw.ListQuery) (lw.ListResult, error) {
	s.gotQ = q
	if s.listErr != nil {
		return lw.ListResult{}, s.listErr
	}
	return lw.ListResult{Items: s.items}, nil
}
func (s *kgLWStub) ListAll(_ context.Context, q lw.ListQuery) ([]lw.LearnerWeakness, error) {
	s.gotQ = q
	return s.items, s.listErr
}
func (s *kgLWStub) Get(context.Context, string, string) (*lw.LearnerWeakness, error) {
	return nil, nil
}
func (s *kgLWStub) SoftDelete(context.Context, string, string, time.Time) error { return nil }
func (s *kgLWStub) RecoverByConceptKey(context.Context, string, string, float64, time.Time) ([]lw.GrownEdge, error) {
	return nil, nil
}
func (s *kgLWStub) RecoverByDrillAtomID(context.Context, string, string, float64, time.Time) ([]lw.GrownEdge, error) {
	return nil, nil
}
func (s *kgLWStub) SetCachedDrillAtoms(context.Context, string, string, []string, time.Time) error {
	return nil
}

func kgOverlayEdge(t *testing.T, label string, strength float64, tags ...string) lw.LearnerWeakness {
	t.Helper()
	w, err := lw.New(lw.UpsertInput{
		TenantID:     kgTenantID,
		LearnerGCID:  kgGCID,
		ConceptLabel: label,
		Embedding:    []float32{0.1},
		Strength:     strength,
		Tags:         tags,
		Source:       lw.SourceDerived,
		Now:          time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("edge: %v", err)
	}
	return *w
}

func TestKGClusters_List_GrowthEdgeOverlay(t *testing.T) {
	srv := newKGSrv()
	srv.LearnerWeakness = &kgLWStub{items: []lw.LearnerWeakness{
		kgOverlayEdge(t, "Agile", 0.7),
	}}
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	if err := srv.KGClusters.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}

	w := serveKG(srv, newKGRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Clusters []struct {
				ClusterID  string `json:"clusterId"`
				GrowthEdge *struct {
					EdgeID     string  `json:"edgeId"`
					ConceptKey string  `json:"conceptKey"`
					Strength   float64 `json:"strength"`
				} `json:"growthEdge"`
			} `json:"clusters"`
		} `json:"data"`
	}
	if err := decodeJSON(w, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data.Clusters) != 1 {
		t.Fatalf("clusters = %d", len(resp.Data.Clusters))
	}
	ge := resp.Data.Clusters[0].GrowthEdge
	if ge == nil || ge.ConceptKey != "agile" || ge.Strength != 0.7 || ge.EdgeID == "" {
		t.Fatalf("growthEdge = %+v, want agile @0.7", ge)
	}
}

func TestKGClusters_List_NoOverlayWhenRepoNilOrErr(t *testing.T) {
	for name, repo := range map[string]lw.Repository{
		"nil repo": nil,
		"list err": &kgLWStub{listErr: errors.New("boom")},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newKGSrv()
			srv.LearnerWeakness = repo
			c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
			_ = srv.KGClusters.Save(context.Background(), c)

			w := serveKG(srv, newKGRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters", nil))
			if w.Code != http.StatusOK {
				t.Fatalf("overlay failure must not break the read: %d", w.Code)
			}
			var resp struct {
				Data struct {
					Clusters []struct {
						GrowthEdge *struct{} `json:"growthEdge"`
					} `json:"clusters"`
				} `json:"data"`
			}
			if err := decodeJSON(w, &resp); err != nil {
				t.Fatal(err)
			}
			if len(resp.Data.Clusters) != 1 || resp.Data.Clusters[0].GrowthEdge != nil {
				t.Fatalf("no overlay expected, got %+v", resp.Data.Clusters)
			}
		})
	}
}

func TestKGFog_GrowthEdgeOverlay(t *testing.T) {
	srv := newKGSrv()
	srv.LearnerWeakness = &kgLWStub{items: []lw.LearnerWeakness{
		kgOverlayEdge(t, "Multiplication Tables", 0.8),
		kgOverlayEdge(t, "Fractions", 0.6),
	}}
	// Focal atom carries topic tags so the focal annotation can resolve.
	if err := srv.AtomIndex.Save(context.Background(), &atom_index.AtomIndex{
		AtomID:    kgAtomA,
		TenantID:  kgTenantID,
		AtomType:  "mcq",
		TopicTags: []string{"fractions"},
	}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	if err := srv.KGClusters.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	exp, _ := userknowledgegraph.NewExploration(c.ClusterID, kgTenantID, kgGCID, kgAtomA)
	if err := srv.KGExplorations.Save(ctx, exp); err != nil {
		t.Fatal(err)
	}
	neighbors := kgFakeNeighbors(kgAtomA)
	neighbors[0].FogLabel = "Multiplication Tables" // slug-matches the 0.8 edge
	hex, _ := userknowledgegraph.NewHexagonNode(c.ClusterID, exp.ExplorationID, kgTenantID, kgGCID, kgAtomA, neighbors, "run-1", "model-1")
	if err := srv.KGHexagons.Upsert(ctx, hex); err != nil {
		t.Fatal(err)
	}

	w := serveKG(srv, newKGRequest(http.MethodGet,
		"/v1/me/knowledge-graph/clusters/"+c.ClusterID+"/focal/"+kgAtomA+"/fog", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("fog = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Hexagon struct {
			FocalGrowthEdge *struct {
				ConceptKey string  `json:"concept_key"`
				Strength   float64 `json:"strength"`
			} `json:"focal_growth_edge"`
			Neighbors []struct {
				FogLabel   string `json:"fog_label"`
				GrowthEdge *struct {
					ConceptKey string  `json:"concept_key"`
					Strength   float64 `json:"strength"`
				} `json:"growth_edge"`
			} `json:"neighbors"`
		} `json:"hexagon"`
	}
	if err := decodeJSON(w, &resp); err != nil {
		t.Fatal(err)
	}
	if fe := resp.Hexagon.FocalGrowthEdge; fe == nil || fe.ConceptKey != "fractions" || fe.Strength != 0.6 {
		t.Fatalf("focal_growth_edge = %+v, want fractions @0.6", fe)
	}
	annotated := 0
	for _, n := range resp.Hexagon.Neighbors {
		if n.GrowthEdge != nil {
			annotated++
			if n.FogLabel != "Multiplication Tables" || n.GrowthEdge.ConceptKey != "multiplication-tables" || n.GrowthEdge.Strength != 0.8 {
				t.Fatalf("neighbor overlay = %+v", n)
			}
		}
	}
	if annotated != 1 {
		t.Fatalf("annotated neighbors = %d, want exactly the matching one", annotated)
	}
}
