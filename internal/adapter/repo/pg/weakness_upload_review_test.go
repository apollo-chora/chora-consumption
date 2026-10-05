package pg

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

func reviewPanelSample() *wu.ReviewPanel {
	return &wu.ReviewPanel{
		Companion: &wu.ReviewCompanion{CompanionID: "fam-1", Name: "Ember", Species: "dragon"},
		ProposedEdges: []wu.ProposedEdge{{
			ProposedEdgeID:      "pe-opaque-1", // orchestrator-owned; stored verbatim
			ConceptLabel:        "causes of riverine flooding",
			Summary:             "shaky on causation",
			SuggestedAngles:     []string{"fluvial vs pluvial"},
			Strength:            0.7,
			SuggestedDifficulty: "standard",
		}},
		CandidateStruggles: []wu.CandidateStruggle{{ConceptKey: "tectonics", ConceptLabel: "plate boundaries"}},
		AvailableOutputs:   []wu.AvailableOutput{{Kind: "practice_test", ManaPrice: 50, DefaultSelected: true}},
	}
}

func TestWeaknessUploadRepo_MarkAwaitingReview(t *testing.T) {
	tx := &wuTx{}
	repo := NewWeaknessUploadRepo(tx)
	now := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	if err := repo.MarkAwaitingReview(wuCtx(), "gcid-1", "up-1", reviewPanelSample(), now); err != nil {
		t.Fatalf("MarkAwaitingReview: %v", err)
	}
	e, ok := tx.q.find("status = 'AWAITING_REVIEW'")
	if !ok {
		t.Fatalf("no AWAITING_REVIEW update exec; got %d execs", len(tx.q.execs))
	}
	// Status-guarded transition: only flips a QUEUED/ANALYZING job (idempotent —
	// a re-delivered review_pending for an already-parked/terminal job is a no-op).
	if !strings.Contains(e.sql, "status IN ('QUEUED', 'ANALYZING')") {
		t.Errorf("MarkAwaitingReview SQL must guard on QUEUED/ANALYZING; got:\n%s", e.sql)
	}
	// args: [0]upload_id [1]learner_gcid [2]review_payload(JSONB bytes)
	if e.args[0] != "up-1" || e.args[1] != "gcid-1" {
		t.Fatalf("MarkAwaitingReview id args = %v", e.args[:2])
	}
	raw, ok := e.args[2].([]byte)
	if !ok {
		t.Fatalf("review_payload arg must be []byte JSONB; got %T", e.args[2])
	}
	var back wu.ReviewPanel
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("review_payload is not valid JSON: %v", err)
	}
	if len(back.ProposedEdges) != 1 || back.ProposedEdges[0].ProposedEdgeID != "pe-opaque-1" {
		t.Errorf("stored panel lost the opaque proposed_edge_id: %+v", back.ProposedEdges)
	}
	if back.Companion == nil || back.Companion.Species != "dragon" {
		t.Errorf("stored panel companion = %+v", back.Companion)
	}
	// RLS applied first (SET LOCAL pair precedes the update).
	if len(tx.q.execs) < 3 {
		t.Fatalf("expected SET LOCAL pair before the update; got %d execs", len(tx.q.execs))
	}
}

func TestWeaknessUploadRepo_MarkAwaitingReview_RLSError(t *testing.T) {
	repo := NewWeaknessUploadRepo(&wuTx{})
	if err := repo.MarkAwaitingReview(context.Background(), "gcid-1", "up-1", reviewPanelSample(), time.Now()); err == nil {
		t.Fatal("MarkAwaitingReview: want RLS error on no-tenant ctx")
	}
}

func TestWeaknessUploadRepo_MarkAwaitingReview_ExecError(t *testing.T) {
	repo := NewWeaknessUploadRepo(&wuTx{q: &wuQuerier{execErr: errors.New("boom")}})
	if err := repo.MarkAwaitingReview(wuCtx(), "gcid-1", "up-1", reviewPanelSample(), time.Now()); err == nil {
		t.Fatal("MarkAwaitingReview: want exec error to propagate")
	}
}

func TestWeaknessUploadRepo_Get_ReadsReviewPayload(t *testing.T) {
	panelJSON, _ := json.Marshal(reviewPanelSample())
	row := wuRow{scan: func(dest ...any) error {
		*dest[0].(*string) = "up-1"
		*dest[1].(*string) = "gcid-1"
		*dest[2].(*string) = "marked_test"
		*dest[3].(*string) = "application/pdf"
		*dest[4].(*string) = "gs://b/o.pdf"
		*dest[5].(*string) = "AWAITING_REVIEW"
		*dest[6].(*[]string) = []string{}
		*dest[7].(*int) = 0
		*dest[8].(*string) = ""
		*dest[9].(*time.Time) = time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
		*dest[10].(**time.Time) = nil
		*dest[11].(*[]byte) = panelJSON
		return nil
	}}
	repo := NewWeaknessUploadRepo(&wuTx{q: &wuQuerier{row: row}})

	got, err := repo.Get(wuCtx(), "gcid-1", "up-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.Status != wu.StatusAwaitingReview {
		t.Fatalf("got = %+v", got)
	}
	if got.Review == nil {
		t.Fatal("Get must hydrate Review from review_payload when AWAITING_REVIEW")
	}
	if len(got.Review.ProposedEdges) != 1 || got.Review.ProposedEdges[0].ProposedEdgeID != "pe-opaque-1" {
		t.Errorf("hydrated panel = %+v", got.Review)
	}
}

func TestWeaknessUploadRepo_Get_NoReviewPayload_NilReview(t *testing.T) {
	// A COMPLETED job has a NULL review_payload → Review stays nil (no panic).
	row := wuRow{scan: func(dest ...any) error {
		*dest[0].(*string) = "up-1"
		*dest[1].(*string) = "gcid-1"
		*dest[2].(*string) = "marked_test"
		*dest[3].(*string) = "application/pdf"
		*dest[4].(*string) = "gs://b/o.pdf"
		*dest[5].(*string) = "COMPLETED"
		*dest[6].(*[]string) = []string{"e1"}
		*dest[7].(*int) = 1
		*dest[8].(*string) = ""
		*dest[9].(*time.Time) = time.Now().UTC()
		*dest[10].(**time.Time) = nil
		*dest[11].(*[]byte) = nil // NULL JSONB
		return nil
	}}
	repo := NewWeaknessUploadRepo(&wuTx{q: &wuQuerier{row: row}})
	got, err := repo.Get(wuCtx(), "gcid-1", "up-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Review != nil {
		t.Errorf("Review must be nil when review_payload is NULL; got %+v", got.Review)
	}
}
