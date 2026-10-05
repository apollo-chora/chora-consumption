// mergesplit_test.go — WS-C6 (CHO-2085): the composite-apply gate refuses
// structurally unusable deltas before any SQL runs (fail-loud).
package mergesplit

import (
	"testing"
	"time"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

func validGraphDelta() Apply {
	return Apply{
		TenantID:    "11111111-1111-7111-8111-111111111111",
		LearnerGCID: "00000000-0000-7000-8000-000000001999",
		Now:         time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		Lineage: []conceptgraph.LineageRecord{{
			Operation:     conceptgraph.LineageOperationMerge,
			FromConceptID: "a", ToConceptID: "b",
			FromConceptKey: "a", ToConceptKey: "b",
		}},
	}
}

func TestApplyValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Apply)
		wantErr bool
	}{
		{"valid", func(a *Apply) {}, false},
		{"missing tenant", func(a *Apply) { a.TenantID = "" }, true},
		{"missing learner", func(a *Apply) { a.LearnerGCID = "" }, true},
		{"zero clock", func(a *Apply) { a.Now = time.Time{} }, true},
		{"empty graph delta", func(a *Apply) {
			a.Lineage = nil
			a.NodesToTombstone, a.NodesToCreate = nil, nil
			a.EdgesToSoftDelete, a.EdgesToCreate = nil, nil
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := validGraphDelta()
			tc.mutate(&a)
			err := a.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
