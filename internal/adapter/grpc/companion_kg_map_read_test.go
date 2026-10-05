package grpc

// companion_kg_map_read_test.go — CHO-2014 (map_sight / kg.read_map): the
// ReadKGMap RPC. It resolves the Companion's resonant-concept centre + growth
// stage from the growth State, derives the ring reach from the stage
// (RingsForStage), and delegates the ring-scoped concept read to the injected
// KGMapReader. Owner-scoped (via GetCompanionGrowth), Unimplemented when unwired,
// empty map when no centre picked, learner-safe titles.

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/apollo-chora/chora-consumption/internal/adapter/kgmapread"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"
)

type fakeKGMapReader struct {
	nodes      []kgmapread.MapNode
	err        error
	lastCentre string
	lastRings  int
	calls      int
}

func (f *fakeKGMapReader) ReadRingMap(_ context.Context, _, _, centre string, rings int) ([]kgmapread.MapNode, error) {
	f.calls++
	f.lastCentre = centre
	f.lastRings = rings
	return f.nodes, f.err
}

// seedGrowthRow seeds fam-1 owned by g at the given stage + resonant concept.
func seedGrowthRow(repo *inmem.GrowthRepo, stage int, resonantConceptID string) {
	hatched := fixedClock.Add(-48 * time.Hour)
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "g",
		GrowthStage: stage, Species: "penguin", HatchedAt: &hatched,
		ResonantConceptID: resonantConceptID,
	})
}

func kgMapFixture() []kgmapread.MapNode {
	return []kgmapread.MapNode{
		{Title: "Long division", Ring: 0, IsCentre: true},
		{Title: "Remainders", Ring: 1},
		{Title: "Fractions", Ring: 1},
	}
}

func TestReadKGMap_Structural2RingsHappyPath(t *testing.T) {
	reader := &fakeKGMapReader{nodes: kgMapFixture()}
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithKGMapReader(reader),
	)
	seedGrowthRow(repo, 4, "concept-centre") // Structural → 2 rings

	resp, err := s.ReadKGMap(context.Background(), &consumptionv1.ReadKGMapRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
	})
	if err != nil {
		t.Fatalf("ReadKGMap: %v", err)
	}
	if resp.GetRings() != 2 {
		t.Fatalf("rings = %d want 2 (Structural st4)", resp.GetRings())
	}
	if reader.lastRings != 2 || reader.lastCentre != "concept-centre" {
		t.Fatalf("reader called with centre=%q rings=%d", reader.lastCentre, reader.lastRings)
	}
	if resp.GetCentreTitle() != "Long division" {
		t.Fatalf("centre_title = %q", resp.GetCentreTitle())
	}
	if len(resp.GetConcepts()) != 3 {
		t.Fatalf("concepts = %d want 3", len(resp.GetConcepts()))
	}
	c0 := resp.GetConcepts()[0]
	if c0.GetTitle() != "Long division" || c0.GetRing() != 0 || !c0.GetIsCentre() {
		t.Fatalf("centre concept mapping = %+v", c0)
	}
}

func TestReadKGMap_Awakened1Ring(t *testing.T) {
	reader := &fakeKGMapReader{nodes: kgMapFixture()}
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithKGMapReader(reader),
	)
	seedGrowthRow(repo, 3, "concept-centre") // Awakened → 1 ring

	resp, err := s.ReadKGMap(context.Background(), &consumptionv1.ReadKGMapRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
	})
	if err != nil {
		t.Fatalf("ReadKGMap: %v", err)
	}
	if resp.GetRings() != 1 || reader.lastRings != 1 {
		t.Fatalf("rings = %d, reader.lastRings = %d; want 1 (Awakened st3)", resp.GetRings(), reader.lastRings)
	}
}

func TestReadKGMap_NoCentreYieldsEmptyMapNoRead(t *testing.T) {
	reader := &fakeKGMapReader{nodes: kgMapFixture()}
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithKGMapReader(reader),
	)
	seedGrowthRow(repo, 4, "") // hatched but no resonant concept picked yet

	resp, err := s.ReadKGMap(context.Background(), &consumptionv1.ReadKGMapRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
	})
	if err != nil {
		t.Fatalf("ReadKGMap: %v", err)
	}
	if reader.calls != 0 {
		t.Fatal("no centre ⇒ must not hit the KG reader")
	}
	if resp.GetRings() != 2 || resp.GetCentreTitle() != "" || len(resp.GetConcepts()) != 0 {
		t.Fatalf("empty-centre response = %+v", resp)
	}
}

func TestReadKGMap_ForeignCallerHidden(t *testing.T) {
	reader := &fakeKGMapReader{nodes: kgMapFixture()}
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithKGMapReader(reader),
	)
	seedGrowthRow(repo, 4, "concept-centre")

	_, err := s.ReadKGMap(context.Background(), &consumptionv1.ReadKGMapRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "intruder",
	})
	if got := codeOf(t, err); got != codes.NotFound {
		t.Fatalf("foreign caller: want NotFound, got %s", got)
	}
	if reader.calls != 0 {
		t.Fatal("must not read another owner's map")
	}
}

func TestReadKGMap_UnwiredUnimplemented(t *testing.T) {
	s, repo := p1bServer(t, WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}))
	seedGrowthRow(repo, 4, "concept-centre")
	_, err := s.ReadKGMap(context.Background(), &consumptionv1.ReadKGMapRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
	})
	if got := codeOf(t, err); got != codes.Unimplemented {
		t.Fatalf("unwired: want Unimplemented, got %s", got)
	}
}

func TestReadKGMap_Validation(t *testing.T) {
	reader := &fakeKGMapReader{nodes: kgMapFixture()}
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithKGMapReader(reader),
	)
	seedGrowthRow(repo, 4, "concept-centre")
	cases := []struct {
		name string
		req  *consumptionv1.ReadKGMapRequest
	}{
		{"no tenant", &consumptionv1.ReadKGMapRequest{CompanionId: "fam-1", CallerGcid: "g"}},
		{"no companion", &consumptionv1.ReadKGMapRequest{TenantId: "t", CallerGcid: "g"}},
		{"no caller", &consumptionv1.ReadKGMapRequest{TenantId: "t", CompanionId: "fam-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ReadKGMap(context.Background(), tc.req)
			if got := codeOf(t, err); got != codes.InvalidArgument {
				t.Fatalf("want InvalidArgument, got %s", got)
			}
		})
	}
}

func TestReadKGMap_ReaderErrorIsInternal(t *testing.T) {
	reader := &fakeKGMapReader{err: errors.New("concept graph down")}
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithKGMapReader(reader),
	)
	seedGrowthRow(repo, 4, "concept-centre")
	_, err := s.ReadKGMap(context.Background(), &consumptionv1.ReadKGMapRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
	})
	if got := codeOf(t, err); got != codes.Internal {
		t.Fatalf("reader error: want Internal, got %s", got)
	}
}
