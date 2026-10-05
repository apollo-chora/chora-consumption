// growth_edge_uploads_review_test.go — RED tests for the poll DTO `review` block
// (ADR-205 D4 / CHO-1973): GET .../uploads/{id} surfaces the bounded HITL panel
// ONLY while status == AWAITING_REVIEW. Reuses the ueServer/fakeUploadRepo/ueReq
// helpers from growth_edge_uploads_handler_test.go (same http_test package).
package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

func awaitingReviewJob() *wu.Upload {
	return &wu.Upload{
		UploadID:    "up-1",
		TenantID:    geTenant,
		LearnerGCID: geGCID,
		Status:      wu.StatusAwaitingReview,
		CreatedAt:   time.Now().UTC(),
		Review: &wu.ReviewPanel{
			Companion: &wu.ReviewCompanion{CompanionID: "fam-1", Name: "Ember", Species: "dragon"},
			ProposedEdges: []wu.ProposedEdge{{
				ProposedEdgeID:      "pe-opaque-1",
				ConceptLabel:        "causes of riverine flooding",
				Summary:             "shaky on causation",
				SuggestedAngles:     []string{"fluvial vs pluvial"},
				Strength:            0.7,
				SuggestedDifficulty: "standard",
			}},
			CandidateStruggles: []wu.CandidateStruggle{{ConceptKey: "tectonics", ConceptLabel: "plate boundaries"}},
			AvailableOutputs:   []wu.AvailableOutput{{Kind: "practice_test", ManaPrice: 50, DefaultSelected: true}},
		},
	}
}

func TestGetUploadJob_AwaitingReview_IncludesReviewPanel(t *testing.T) {
	srv := ueServer(&fakeUploadRepo{getResult: awaitingReviewJob()}, &fakeBlobUploader{})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("GET", "/v1/me/growth-edges/uploads/up-1", nil, "", true))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["status"] != "AWAITING_REVIEW" {
		t.Fatalf("status = %v want AWAITING_REVIEW", got["status"])
	}
	review, ok := got["review"].(map[string]any)
	if !ok {
		t.Fatalf("review block missing/!object: %v", got["review"])
	}
	// Companion (snake_case keys).
	fam, ok := review["companion"].(map[string]any)
	if !ok || fam["companion_id"] != "fam-1" || fam["species"] != "dragon" {
		t.Errorf("review.companion = %v", review["companion"])
	}
	// Proposed edges — the orchestrator-owned proposed_edge_id rides through opaquely.
	edges, ok := review["proposed_edges"].([]any)
	if !ok || len(edges) != 1 {
		t.Fatalf("review.proposed_edges = %v", review["proposed_edges"])
	}
	e0 := edges[0].(map[string]any)
	if e0["proposed_edge_id"] != "pe-opaque-1" || e0["concept_label"] != "causes of riverine flooding" {
		t.Errorf("proposed edge = %v", e0)
	}
	if e0["suggested_difficulty"] != "standard" {
		t.Errorf("suggested_difficulty = %v", e0["suggested_difficulty"])
	}
	// Candidate struggles + available outputs.
	if cs, ok := review["candidate_struggles"].([]any); !ok || len(cs) != 1 {
		t.Errorf("review.candidate_struggles = %v", review["candidate_struggles"])
	}
	outs, ok := review["available_outputs"].([]any)
	if !ok || len(outs) != 1 {
		t.Fatalf("review.available_outputs = %v", review["available_outputs"])
	}
	o0 := outs[0].(map[string]any)
	if o0["kind"] != "practice_test" || o0["mana_price"].(float64) != 50 || o0["default_selected"] != true {
		t.Errorf("available output = %v", o0)
	}
}

func TestGetUploadJob_Completed_OmitsReviewPanel(t *testing.T) {
	// Status-gated: even if a (stale) Review object is attached, a non-AWAITING
	// state must NOT surface the panel — the FE only renders it at the interrupt.
	job := awaitingReviewJob()
	job.Status = wu.StatusCompleted
	job.UpsertedEdgeIDs = []string{"e1"}
	srv := ueServer(&fakeUploadRepo{getResult: job}, &fakeBlobUploader{})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("GET", "/v1/me/growth-edges/uploads/up-1", nil, "", true))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var got map[string]any
	_ = json.NewDecoder(w.Body).Decode(&got)
	if _, present := got["review"]; present {
		t.Errorf("review must be omitted when status != AWAITING_REVIEW; got %v", got["review"])
	}
}
