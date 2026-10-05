// Package weakness_upload is the Growth-Edge upload-job aggregate (Epic-1b W8b):
// the async lifecycle of one uploaded weakness document, distinct from the
// LearnerWeakness concept aggregate it eventually produces.
//
// Flow: POST .../uploads mints a QUEUED job + publishes weakness_doc.uploaded.v1
// → the ai-kernel analyser crew runs → weakness.analyzed.v1 → chora-consumption
// flips the job to COMPLETED with the upserted Growth-Edge ids. GET .../uploads/
// {id} polls it. The analyser is fail-soft (0 edges is still a COMPLETED), so
// the common terminal is COMPLETED; FAILED is reserved for an explicit failure
// signal (e.g. a future DLQ/timeout sweep) and is a valid but rarely-set state.
package weakness_upload

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Status is the upload-job lifecycle state (mirrors the OpenAPI
// GrowthEdgeUploadJob.status enum — UPPERCASE on the wire).
type Status string

const (
	StatusQueued    Status = "QUEUED"
	StatusAnalyzing Status = "ANALYZING"
	// StatusAwaitingReview parks the job at the bounded HITL interrupt (ADR-205
	// D4, CHO-1973). The graduated (LangGraph) analyser crew PAUSES after
	// diagnosing candidate edges but BEFORE persisting; it emits
	// chora.consumption.weakness.review_pending.v1 carrying the full review panel
	// (cross-DB forbidden), and chora-consumption parks the job here + stores the
	// panel so the A+ FE can drive the accept/reject/merge decision. The learner's
	// bounded decision resumes the SAME orchestrator thread (deterministic on
	// {tenant, upload}). Reachable only on the graph path (DARK until cutover).
	StatusAwaitingReview Status = "AWAITING_REVIEW"
	StatusCompleted      Status = "COMPLETED"
	StatusFailed         Status = "FAILED"
)

// Upload-kind values (mirror weakness.proto WeaknessDocUploaded.upload_kind).
const (
	KindMarkedTest = "marked_test"
	KindNotes      = "notes"
	KindScribble   = "scribble"
	// KindSourceMaterial is a grounding textbook/reference (ADR-205 D8 / WS-5):
	// TRANSIENT context for the analyse pass, never persisted verbatim. It is a
	// KNOWN kind here (so it round-trips), but its ACCEPTANCE is gated by the
	// SourceMaterialGate (one-tap upload-rights consent + DARK by default).
	KindSourceMaterial = "source_material"
)

// ValidKind reports whether k is one of the accepted upload kinds. Note this is
// a KNOWN-kind check, not an acceptance check — source_material is known here but
// gated by SourceMaterialGate (see source_material_gate.go).
func ValidKind(k string) bool {
	switch k {
	case KindMarkedTest, KindNotes, KindScribble, KindSourceMaterial:
		return true
	default:
		return false
	}
}

// Upload is one weakness-document upload job.
type Upload struct {
	UploadID    string
	TenantID    string
	LearnerGCID string
	GoalID      string // ADR-238: the goal (map) this upload was scoped to; "" = non-goal upload
	// EntryConceptID is ADR-238 D2's SOFT entry hint: the concept whose drawer
	// Diagnose was opened from. It BIASES ranking inside the goal's candidate
	// set (a tie-break applied after the cosine floor); it never filters. "" is
	// the deliberate value for a goal-level "Diagnose my map" upload.
	EntryConceptID  string
	UploadKind      string
	SourceMIME      string
	SourceBlobURI   string
	Status          Status
	UpsertedEdgeIDs []string
	EdgeCount       int
	FailureReason   string
	CreatedAt       time.Time
	AnalyzedAt      *time.Time // nil until COMPLETED / FAILED
	// UploadRightsConsentAt records the one-tap upload-rights attestation for a
	// source_material (textbook) upload (WS-5). nil for the other kinds.
	UploadRightsConsentAt *time.Time
	// Review is the bounded HITL review panel (ADR-205 D4, CHO-1973), present
	// ONLY while Status == StatusAwaitingReview (persisted as the review_payload
	// JSONB column). nil for every other state. The analyser orchestrator owns
	// proposed_edge_id — chora-consumption treats it OPAQUELY (stores + echoes it).
	Review *ReviewPanel
}

// Scope is the ADR-238 scoping an upload was taken under, as the ingest resolver
// needs it. The two fields are deliberately different in kind:
//
//   - GoalID BOUNDS the candidate concepts (D1: the goal/map is the unit of
//     analysis). "" = a non-goal upload, so there is no map to scope to.
//   - EntryConceptID only BIASES ranking inside that boundary (D2: the concept
//     drawer is a soft entry, not a filter). "" = a goal-level "Diagnose my map"
//     upload, which carries no hint by design.
//
// Lives in the domain so both the pg repo and the subscriber depend on the
// domain rather than on each other.
type Scope struct {
	GoalID         string
	EntryConceptID string
}

// NewInput is the data to mint a fresh QUEUED upload job. UploadID is minted by
// the caller (UUIDv7) so it can also publish weakness_doc.uploaded.v1 with it.
type NewInput struct {
	UploadID      string
	TenantID      string
	LearnerGCID   string
	UploadKind    string
	SourceMIME    string
	SourceBlobURI string
	GoalID        string // ADR-238: optional goal (map) scope this upload was taken under; "" = non-goal upload
	// EntryConceptID is ADR-238 D2's optional soft entry hint; "" = a goal-level
	// upload, which carries no hint by design.
	EntryConceptID string
	Now            time.Time
	// UploadRightsConsentAt is set by the handler when a source_material upload
	// passes the one-tap consent gate (WS-5). nil for the other kinds.
	UploadRightsConsentAt *time.Time
}

// New validates + mints a QUEUED upload. Fails loud on any missing required
// field or an unknown upload_kind (no silent defaults).
func New(in NewInput) (Upload, error) {
	if strings.TrimSpace(in.UploadID) == "" {
		return Upload{}, errors.New("weakness_upload: upload_id required")
	}
	if strings.TrimSpace(in.TenantID) == "" {
		return Upload{}, errors.New("weakness_upload: tenant_id required")
	}
	if strings.TrimSpace(in.LearnerGCID) == "" {
		return Upload{}, errors.New("weakness_upload: learner_gcid required")
	}
	if strings.TrimSpace(in.SourceBlobURI) == "" {
		return Upload{}, errors.New("weakness_upload: source_blob_uri required")
	}
	if !ValidKind(in.UploadKind) {
		return Upload{}, fmt.Errorf("weakness_upload: invalid upload_kind %q", in.UploadKind)
	}
	now := in.Now
	if now.IsZero() {
		return Upload{}, errors.New("weakness_upload: now required (inject a clock)")
	}
	return Upload{
		UploadID:              in.UploadID,
		TenantID:              in.TenantID,
		LearnerGCID:           in.LearnerGCID,
		GoalID:                strings.TrimSpace(in.GoalID),
		EntryConceptID:        strings.TrimSpace(in.EntryConceptID),
		UploadKind:            in.UploadKind,
		SourceMIME:            in.SourceMIME,
		SourceBlobURI:         in.SourceBlobURI,
		Status:                StatusQueued,
		UpsertedEdgeIDs:       []string{},
		CreatedAt:             now.UTC(),
		UploadRightsConsentAt: in.UploadRightsConsentAt,
	}, nil
}
