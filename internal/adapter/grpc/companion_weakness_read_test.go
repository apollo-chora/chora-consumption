package grpc

// companion_weakness_read_test.go — CHO-2014 (workstream B, R3-3): the
// weakness.read gRPC READ-tool that backs the weakness_sight Skill.
//
// ReadWeakness returns the OWNER's top-N Growth Edges (learner_weakness
// aggregate) ordered strength-desc (shakiest first). It mirrors
// ReadLearnerProfile: owner-scoped (foreign caller ⇒ NotFound; no cross-owner
// disclosure), Unimplemented when unwired, and LEARNER-SAFE — the projection
// carries the concept slug + natural label + a SANITIZED summary only, NEVER
// the edge id, learner gcid, topic_id, or embedding (no-UUID-leak discipline,
// CHO-2059/2060).

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"
)

// fakeWeaknessReader captures the ListQuery it is called with + returns a
// configured page. It structurally satisfies the adapter's WeaknessReader.
type fakeWeaknessReader struct {
	result    lw.ListResult
	err       error
	lastQuery lw.ListQuery
	calls     int
}

func (f *fakeWeaknessReader) List(_ context.Context, q lw.ListQuery) (lw.ListResult, error) {
	f.calls++
	f.lastQuery = q
	return f.result, f.err
}

func weaknessFixture() lw.ListResult {
	now := time.Now().UTC()
	return lw.ListResult{
		Items: []lw.LearnerWeakness{
			{
				ID: "01957c8c-aaaa-7000-aaaa-aaaaaaaaaaaa", TenantID: "t", LearnerGCID: "g",
				ConceptKey: "long-division", ConceptLabel: "Long division",
				Strength: 0.82, Category: "arithmetic", Tags: []string{"math", "core"},
				Status: lw.StatusActive, TopicID: "01957c8c-dddd-7000-dddd-dddddddddddd",
				Descriptor:      lw.Descriptor{Summary: "keeps dropping the remainder step"},
				LastEvidencedAt: now.Add(-2 * time.Hour),
			},
			{
				ID: "01957c8c-bbbb-7000-bbbb-bbbbbbbbbbbb", TenantID: "t", LearnerGCID: "g",
				ConceptKey: "fractions", ConceptLabel: "Fractions",
				Strength: 0.55, Category: "arithmetic",
				Status:          lw.StatusActive,
				LastEvidencedAt: now.Add(-3 * time.Hour),
			},
		},
	}
}

func TestReadWeakness_HappyPathTopNStrengthDesc(t *testing.T) {
	reader := &fakeWeaknessReader{result: weaknessFixture()}
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithWeaknessReader(reader),
	)
	seedStage2(repo)

	resp, err := s.ReadWeakness(context.Background(), &consumptionv1.ReadWeaknessRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g", Limit: 5,
	})
	if err != nil {
		t.Fatalf("ReadWeakness: %v", err)
	}
	edges := resp.GetEdges()
	if len(edges) != 2 {
		t.Fatalf("edges = %d want 2", len(edges))
	}
	// Order preserved from the reader (strength desc).
	if edges[0].GetConceptKey() != "long-division" || edges[1].GetConceptKey() != "fractions" {
		t.Fatalf("order = %q,%q", edges[0].GetConceptKey(), edges[1].GetConceptKey())
	}
	e0 := edges[0]
	if e0.GetConceptLabel() != "Long division" || e0.GetStrength() != 0.82 ||
		e0.GetCategory() != "arithmetic" || e0.GetStatus() != "active" {
		t.Fatalf("edge0 mapping = %+v", e0)
	}
	if got := e0.GetTags(); len(got) != 2 || got[0] != "math" || got[1] != "core" {
		t.Fatalf("tags = %v", got)
	}
	if e0.GetSummary() != "keeps dropping the remainder step" {
		t.Fatalf("summary = %q", e0.GetSummary())
	}
	if e0.GetLastEvidencedAt() == nil {
		t.Fatal("last_evidenced_at not threaded")
	}
	// The reader was asked for strength-desc, active-only, page-sized to the limit.
	if reader.lastQuery.Sort != lw.SortStrengthDesc {
		t.Fatalf("sort = %q want strength_desc", reader.lastQuery.Sort)
	}
	if reader.lastQuery.IncludeGrown {
		t.Fatal("include_grown must be false (mastered edges are not weaknesses)")
	}
	if reader.lastQuery.LearnerGCID != "g" || reader.lastQuery.TenantID != "t" {
		t.Fatalf("query scope = %+v", reader.lastQuery)
	}
}

func TestReadWeakness_LearnerSafeNoUUIDLeak(t *testing.T) {
	now := time.Now().UTC()
	leakID := "01957c8c-aaaa-7000-aaaa-aaaaaaaaaaaa"
	leakGCID := "01957c8c-9999-7000-9999-999999999999"
	leakTopic := "01957c8c-dddd-7000-dddd-dddddddddddd"
	reader := &fakeWeaknessReader{result: lw.ListResult{Items: []lw.LearnerWeakness{{
		ID: leakID, TenantID: "t", LearnerGCID: leakGCID, TopicID: leakTopic,
		ConceptKey: "long-division", ConceptLabel: "Long division", Strength: 0.9,
		Status:          lw.StatusActive,
		Descriptor:      lw.Descriptor{Summary: "atom " + leakTopic + " keeps tripping them up"},
		LastEvidencedAt: now,
	}}}}
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithWeaknessReader(reader),
	)
	seedStage2(repo)

	resp, err := s.ReadWeakness(context.Background(), &consumptionv1.ReadWeaknessRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
	})
	if err != nil {
		t.Fatalf("ReadWeakness: %v", err)
	}
	// Flatten every string the Companion could parrot + assert no raw UUID leaks.
	e := resp.GetEdges()[0]
	blob := strings.Join([]string{
		e.GetConceptKey(), e.GetConceptLabel(), e.GetCategory(),
		e.GetStatus(), e.GetSummary(), strings.Join(e.GetTags(), " "),
	}, "\x00")
	for _, leak := range []string{leakID, leakGCID, leakTopic} {
		if strings.Contains(blob, leak) {
			t.Fatalf("UUID leak in learner-facing output: %q present in %q", leak, blob)
		}
	}
	// The concept label survives; the summary is sanitized (UUID stripped).
	if e.GetConceptLabel() != "Long division" {
		t.Fatalf("concept_label = %q", e.GetConceptLabel())
	}
	if e.GetSummary() == "" || strings.Contains(e.GetSummary(), leakTopic) {
		t.Fatalf("summary not sanitized: %q", e.GetSummary())
	}
}

func TestReadWeakness_LimitClamp(t *testing.T) {
	cases := []struct {
		name string
		in   int32
		want int
	}{
		{"zero_defaults", 0, weaknessDefaultLimit},
		{"negative_defaults", -3, weaknessDefaultLimit},
		{"over_max_clamps", 1000, weaknessMaxLimit},
		{"in_range_kept", 7, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeWeaknessReader{}
			s, repo := p1bServer(t,
				WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
				WithWeaknessReader(reader),
			)
			seedStage2(repo)
			if _, err := s.ReadWeakness(context.Background(), &consumptionv1.ReadWeaknessRequest{
				TenantId: "t", CompanionId: "fam-1", CallerGcid: "g", Limit: tc.in,
			}); err != nil {
				t.Fatalf("ReadWeakness: %v", err)
			}
			if reader.lastQuery.PageSize != tc.want {
				t.Fatalf("page_size = %d want %d", reader.lastQuery.PageSize, tc.want)
			}
		})
	}
}

func TestReadWeakness_ForeignCallerHidden(t *testing.T) {
	reader := &fakeWeaknessReader{result: weaknessFixture()}
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithWeaknessReader(reader),
	)
	seedStage2(repo)

	_, err := s.ReadWeakness(context.Background(), &consumptionv1.ReadWeaknessRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "intruder",
	})
	if got := codeOf(t, err); got != codes.NotFound {
		t.Fatalf("foreign caller: want NotFound, got %s", got)
	}
	if reader.calls != 0 {
		t.Fatal("must not read another owner's Growth Edges")
	}
}

func TestReadWeakness_UnwiredUnimplemented(t *testing.T) {
	s, repo := p1bServer(t, WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}))
	seedStage2(repo)
	_, err := s.ReadWeakness(context.Background(), &consumptionv1.ReadWeaknessRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
	})
	if got := codeOf(t, err); got != codes.Unimplemented {
		t.Fatalf("unwired: want Unimplemented, got %s", got)
	}
}

func TestReadWeakness_Validation(t *testing.T) {
	reader := &fakeWeaknessReader{result: weaknessFixture()}
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithWeaknessReader(reader),
	)
	seedStage2(repo)
	cases := []struct {
		name string
		req  *consumptionv1.ReadWeaknessRequest
	}{
		{"no tenant", &consumptionv1.ReadWeaknessRequest{CompanionId: "fam-1", CallerGcid: "g"}},
		{"no companion", &consumptionv1.ReadWeaknessRequest{TenantId: "t", CallerGcid: "g"}},
		{"no caller", &consumptionv1.ReadWeaknessRequest{TenantId: "t", CompanionId: "fam-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ReadWeakness(context.Background(), tc.req)
			if got := codeOf(t, err); got != codes.InvalidArgument {
				t.Fatalf("want InvalidArgument, got %s", got)
			}
		})
	}
}

func TestReadWeakness_ReaderErrorIsInternal(t *testing.T) {
	reader := &fakeWeaknessReader{err: context.DeadlineExceeded}
	s, repo := p1bServer(t,
		WithCompanionInstanceReader(fakeInstanceReader{inst: p1bInstance()}),
		WithWeaknessReader(reader),
	)
	seedStage2(repo)
	_, err := s.ReadWeakness(context.Background(), &consumptionv1.ReadWeaknessRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
	})
	if got := codeOf(t, err); got != codes.Internal {
		t.Fatalf("reader error: want Internal, got %s", got)
	}
}
