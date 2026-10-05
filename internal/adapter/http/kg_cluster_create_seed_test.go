// kg_cluster_create_seed_test.go — RED-phase tests for the cluster-create
// seed-atom resolution (KG-hexagon fold-in).
//
// The A+ canvas shell sends ONLY `{"seedTopic": "..."}` (camelCase, no
// seed atom — kg-fog.model.ts ClusterCreateRequest). The handler used to
// decode snake_case only and 400 on the missing seed_atom_id ("resolution
// lands in S5.3") — so the FE create path could never work. Now the
// handler accepts both key forms and resolves the seed atom from the
// learner's REAL atom universe (LearningPath.AtomIDs → atom_index, the
// daily-dose seam): substring match on title/topic, no match → 422
// INVALID_SEED (the FE maps 422 → invalid_seed).
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// kgSeedUniverse seeds a learner path + projected atoms so the resolver
// has a real universe to match against.
func kgSeedUniverse(t *testing.T, srv *ExtServer) {
	t.Helper()
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	atoms := []struct {
		id, title string
		topics    []string
	}{
		{kgAtomA, "Version Control: Merge vs Rebase", []string{"software-engineering", "git"}},
		{kgAtomB, "SOLID: Open-Closed Principle", []string{"software-engineering", "design"}},
	}
	ids := make([]string, 0, len(atoms))
	for _, a := range atoms {
		entry, err := atom_index.New(atom_index.NewParams{
			AtomID: a.id, TenantID: kgTenantID, Title: a.title,
			TopicTags: a.topics, PublishedAt: now,
		})
		if err != nil {
			t.Fatalf("atom_index.New: %v", err)
		}
		if err := srv.AtomIndex.Save(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.id)
	}
	p, err := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID: kgTenantID, LearnerGCID: kgGCID, CourseID: "01970000-0000-7000-b000-000000000099",
		EnrollmentID: "enroll-kg-seed", AtomIDs: ids, Title: "KG seed universe", Now: now,
	})
	if err != nil {
		t.Fatalf("bootstrap path: %v", err)
	}
	if err := srv.Paths.Save(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

func postKGCluster(t *testing.T, srv *ExtServer, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, newKGRequest("POST", "/v1/me/knowledge-graph/clusters", body))
	return w
}

func TestCreateKGCluster_CamelCaseSeedTopicResolvesAtom(t *testing.T) {
	srv := NewExtServer(nil)
	kgSeedUniverse(t, srv)

	w := postKGCluster(t, srv, map[string]any{"seedTopic": "merge vs rebase"})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201 (camelCase seedTopic + server-side seed resolution)", w.Code, w.Body.String())
	}
	var resp struct {
		Cluster struct {
			SeedAtomID string `json:"seed_atom_id"`
			SeedTopic  string `json:"seed_topic"`
		} `json:"cluster"`
		InitialExploration struct {
			ExplorationID string `json:"exploration_id"`
		} `json:"initial_exploration"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Cluster.SeedAtomID != kgAtomA {
		t.Errorf("seed_atom_id = %q; want %q (title match on the learner's universe)", resp.Cluster.SeedAtomID, kgAtomA)
	}
	if resp.InitialExploration.ExplorationID == "" {
		t.Error("initial_exploration.exploration_id empty")
	}
}

func TestCreateKGCluster_TopicTagMatchResolves(t *testing.T) {
	srv := NewExtServer(nil)
	kgSeedUniverse(t, srv)

	w := postKGCluster(t, srv, map[string]any{"seedTopic": "design"})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Cluster struct {
			SeedAtomID string `json:"seed_atom_id"`
		} `json:"cluster"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Cluster.SeedAtomID != kgAtomB {
		t.Errorf("seed_atom_id = %q; want %q (topic-tag match)", resp.Cluster.SeedAtomID, kgAtomB)
	}
}

func TestCreateKGCluster_UnresolvableSeed422(t *testing.T) {
	srv := NewExtServer(nil)
	kgSeedUniverse(t, srv)

	w := postKGCluster(t, srv, map[string]any{"seedTopic": "quantum chromodynamics"})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s; want 422 INVALID_SEED (no fake atoms)", w.Code, w.Body.String())
	}
}

func TestCreateKGCluster_ExplicitSeedAtomStillHonoured(t *testing.T) {
	srv := NewExtServer(nil)
	kgSeedUniverse(t, srv)

	w := postKGCluster(t, srv, map[string]any{"seed_topic": "git", "seed_atom_id": kgAtomB})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Cluster struct {
			SeedAtomID string `json:"seed_atom_id"`
		} `json:"cluster"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Cluster.SeedAtomID != kgAtomB {
		t.Errorf("seed_atom_id = %q; explicit caller value must win", resp.Cluster.SeedAtomID)
	}
}
