// growth_edge_uploads_handler.go — A+ Growth-Edge upload PRODUCER (Epic-1b W8b-2).
//
// Per the FROZEN openapi/consumption-growth-edges.yaml:
//
//	POST /v1/me/growth-edges/uploads        multipart → GCS → publish → 202 job
//	GET  /v1/me/growth-edges/uploads/{id}   poll job status
//
// Flow: parse the multipart (file + upload_kind + context_hint), validate size +
// MIME, stream the blob to GCS, mint a UUIDv7 upload_id, INSERT a QUEUED job, and
// publish chora.consumption.weakness_doc.uploaded.v1 (JSON, WITH a traceparent —
// the analyzed.v1 envelope propagates it and consumption's inbound validation
// rejects an empty traceparent). The ai-kernel weakness-analyser crew consumes
// the event; weakness.analyzed.v1 later flips the job to COMPLETED (W8b-3).
//
// WS-4 (ADR-205 D6 / CHO-1956) — upfront mana reserve at the door. When the
// gate is ENABLED (WEAKNESS_UPFRONT_MANA_ENABLED, DARK by default) the premium
// upload requires an active Companion + mana and RESERVES the price on accept,
// stamping the reservation handle onto WeaknessDocUploaded.reservation_id; the
// crew settles on success / refunds on fail. When disabled (default) the upload
// accepts free with an empty reservation_id (today's behaviour). The gate runs
// BEFORE any side effect, so a refused upload leaves no orphan blob or job.
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/storage"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	wb "github.com/apollo-chora/chora-consumption/internal/domain/weakness_blob"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

const (
	growthEdgeUploadsByIDPrefix = "/v1/me/growth-edges/uploads/"

	// topicWeaknessDocUploaded is the JSON topic the ai-kernel analyser pulls.
	// Kept local (not in events.publisher consts) to minimise shared-file churn.
	topicWeaknessDocUploaded = "chora.consumption.weakness_doc.uploaded.v1"

	// maxMultipartMemory caps in-RAM multipart parsing; larger bodies spill to
	// temp files. The hard size cap is enforced by storage.MaxWeaknessBlobBytes.
	maxMultipartMemory = 16 << 20 // 16 MiB
)

// allowedUploadMIME mirrors the contract: PDF / PNG / JPEG / TXT / MD.
var allowedUploadMIME = map[string]bool{
	"application/pdf": true,
	"image/png":       true,
	"image/jpeg":      true,
	"image/jpg":       true, // some browsers
	"text/plain":      true,
	"text/markdown":   true,
}

// growthEdgeUploadJobDTO is the wire shape of GrowthEdgeUploadJob.
type growthEdgeUploadJobDTO struct {
	UploadID              string   `json:"upload_id"`
	Status                string   `json:"status"`
	UpsertedGrowthEdgeIDs []string `json:"upserted_growth_edge_ids,omitempty"`
	FailureReason         string   `json:"failure_reason,omitempty"`
	// ADR-205 D4 (CHO-1973) — the bounded HITL review panel, present ONLY while
	// status == AWAITING_REVIEW (status-gated below). The domain ReviewPanel is
	// the wire shape (snake_case json tags) — the stored JSONB IS what the FE
	// polls, so it embeds directly. Omitted on every other state.
	Review *wu.ReviewPanel `json:"review,omitempty"`
	// WS-4 (CHO-1956) — price-shown-upfront. Both omitted on the free/dark path
	// (gate disabled ⇒ zero price + empty handle).
	ManaReserved  int64  `json:"mana_reserved,omitempty"`
	ReservationID string `json:"reservation_id,omitempty"`
}

func uploadJobToDTO(u wu.Upload) growthEdgeUploadJobDTO {
	dto := growthEdgeUploadJobDTO{
		UploadID:              u.UploadID,
		Status:                string(u.Status),
		UpsertedGrowthEdgeIDs: u.UpsertedEdgeIDs,
		FailureReason:         u.FailureReason,
	}
	// Surface the panel ONLY at the bounded HITL interrupt — never carry a stale
	// panel into a terminal state (the FE renders the review affordance off this).
	if u.Status == wu.StatusAwaitingReview {
		dto.Review = u.Review
	}
	return dto
}

// handleMeGrowthEdgeUploads — POST /v1/me/growth-edges/uploads.
func (s *ExtServer) handleMeGrowthEdgeUploads(w http.ResponseWriter, r *http.Request) {
	if s.WeaknessUploads == nil || s.WeaknessBlobs == nil {
		extWriteError(w, http.StatusServiceUnavailable, "GROWTH_EDGE_UPLOADS_UNAVAILABLE", "upload path not wired")
		return
	}
	if r.Method != http.MethodPost {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}

	// Cap the request body defensively (handler-level, before the multipart read).
	r.Body = http.MaxBytesReader(w, r.Body, storage.MaxWeaknessBlobBytes+(1<<20))
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_MULTIPART", err.Error())
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_FILE", "multipart field 'file' required")
		return
	}
	defer file.Close()

	kind := strings.TrimSpace(r.FormValue("upload_kind"))
	if !wu.ValidKind(kind) {
		extWriteError(w, http.StatusBadRequest, "INVALID_UPLOAD_KIND", "one of marked_test|notes|scribble|source_material")
		return
	}

	// WS-5 (ADR-205 D8) — source_material (textbook) gate. A grounding textbook
	// is the highest IP-risk input, so before accepting it the learner must give
	// a one-tap upload-rights attestation; the kind is DARK (rejected) until the
	// owner enables it. Runs BEFORE the mana reserve so a rejected upload never
	// reserves. nil/disabled gate is a no-op for the existing kinds.
	consentGiven := parseUploadRightsConsent(r.FormValue("upload_rights_consent"))
	if err := s.SourceMaterial.Check(kind, consentGiven); err != nil {
		writeSourceMaterialError(w, err)
		return
	}

	hint := strings.TrimSpace(r.FormValue("context_hint"))
	// ADR-238 (M-D1) — the goal (map) this upload was scoped to. OPTIONAL: absent ⇒
	// "" ⇒ a non-goal upload (unchanged behaviour). The M-D2 ingest resolver reads
	// it to nearest-match each detected edge against that goal's concept subtree.
	//
	// ADR-238 (D2) - the ENTRY concept the learner opened Diagnose from. OPTIONAL
	// and, on the goal-level "Diagnose my map" path, deliberately ABSENT so a
	// whole-map diagnosis is never biased toward an arbitrary node. It is a soft
	// emphasis hint: the resolver marks that concept's sub-tree as a tie-break,
	// applied after the cosine floor, so it can never discard a weakness.
	//
	// Both are shape-validated HERE rather than left to Postgres. They are cast
	// with ::uuid on insert, so a malformed value would otherwise surface as a
	// 22P02 → 500, blaming the server for a client error and burying it in an
	// outage-shaped alert. Validation runs BEFORE the mana reserve, so a rejected
	// upload never charges the learner.
	goalID := strings.TrimSpace(r.FormValue("goal_id"))
	if goalID != "" && !uuidShaped(goalID) {
		extWriteError(w, http.StatusBadRequest, "INVALID_GOAL_REF", "goal_id must be a UUID")
		return
	}
	entryConceptID := strings.TrimSpace(r.FormValue("concept_id"))
	if entryConceptID != "" && !uuidShaped(entryConceptID) {
		extWriteError(w, http.StatusBadRequest, "INVALID_CONCEPT_REF", "concept_id must be a UUID")
		return
	}

	// Wave C (ADR-205 D2/D5 / CHO-1973) — bounded learner steering. The FE sends
	// structured_clues (the injection-safe replacement for context_hint, proto
	// field 10) + requested_outputs (the learner's output pre-selection, proto
	// field 11) as JSON-string multipart parts. Parse+validate them BEFORE any
	// side effect (fail-loud 400 on malformed JSON — never forward garbage
	// downstream, never reserve/store on a bad request). nil ⇒ absent ⇒ omitted
	// from the payload (the analyser applies its defaults).
	structuredClues, err := parseOptionalJSONField(r.FormValue("structured_clues"))
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_STRUCTURED_CLUES", err.Error())
		return
	}
	requestedOutputs, err := parseOptionalJSONField(r.FormValue("requested_outputs"))
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_REQUESTED_OUTPUTS", err.Error())
		return
	}

	mime := normalizeMIME(hdr.Header.Get("Content-Type"))
	if !allowedUploadMIME[mime] {
		extWriteError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", mime)
		return
	}
	if hdr.Size > storage.MaxWeaknessBlobBytes {
		extWriteError(w, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE", "max 32 MiB")
		return
	}

	uploadID := domain.NewUUIDv7()
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)

	// WS-4 (ADR-205 D6) — upfront mana reserve. Runs BEFORE any side effect so a
	// gated / unaffordable upload leaves no orphan blob or job. A nil/disabled
	// gate is a no-op (empty reservation + zero price). Idempotent on
	// gcid+tenant+upload_id (the reserver derives the key from these).
	reserve, rerr := s.WeaknessMana.Reserve(ctx, wu.ReserveInput{TenantID: tenantID, GCID: gcid, UploadID: uploadID})
	if rerr != nil {
		writeReserveError(w, rerr)
		return
	}

	gsURI, err := s.storeWeaknessBlob(ctx, storeBlobInput{
		tenantID: tenantID, gcid: gcid, uploadID: uploadID,
		mime: mime, filename: hdr.Filename, size: hdr.Size, body: file,
	})
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "BLOB_UPLOAD_FAILED", err.Error())
		return
	}

	now := time.Now().UTC()
	// WS-5: record the one-tap upload-rights consent timestamp for source_material
	// (the gate above already enforced it). nil for the other kinds.
	var consentAt *time.Time
	if wu.IsSourceMaterial(kind) && consentGiven {
		t := now
		consentAt = &t
	}
	job, err := wu.New(wu.NewInput{
		UploadID: uploadID, TenantID: tenantID, LearnerGCID: gcid, UploadKind: kind,
		SourceMIME: mime, SourceBlobURI: gsURI, Now: now, UploadRightsConsentAt: consentAt,
		GoalID: goalID, EntryConceptID: entryConceptID,
	})
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "JOB_CREATE_FAILED", err.Error())
		return
	}
	if err := s.WeaknessUploads.Insert(ctx, job); err != nil {
		extWriteError(w, http.StatusInternalServerError, "JOB_INSERT_FAILED", err.Error())
		return
	}

	// Publish weakness_doc.uploaded.v1 (JSON) — traceparent MUST be present so the
	// analyzed.v1 envelope can carry it (W3 validateInboundEnvelope rejects empty).
	tp := tracing.EnsureTraceparent(r.Header.Get("traceparent"))
	ts := r.Header.Get("tracestate")
	env := events.NewEnvelope(tenantID, gcid, tp, ts, "weakness_doc.uploaded."+uploadID)
	payload := map[string]any{
		"upload_id":        uploadID,
		"tenant_id":        tenantID,
		"learner_gcid":     gcid,
		"source_blob_uri":  gsURI,
		"source_mime_type": mime,
		"upload_kind":      kind,
		"context_hint":     hint,
		// WS-4 — reservation handle (proto field 12). Empty on the free/dark
		// path; the crew treats empty as free-tier (no settle/refund).
		"reservation_id": reserve.ReservationID,
	}
	// Wave C — carry the bounded steering through verbatim (proto fields 10 + 11).
	// Only when present, so the analyser sees absence (its default) for a bare
	// upload — matches proto3 "unset" semantics.
	if structuredClues != nil {
		payload["structured_clues"] = structuredClues
	}
	if requestedOutputs != nil {
		payload["requested_outputs"] = requestedOutputs
	}
	// ADR-238 — carry the goal scope through for tracing/consistency (the
	// weakness_doc.uploaded.v1 event is unschematized JSON). Only when present, so a
	// non-goal upload omits it entirely.
	if goalID != "" {
		payload["goal_id"] = goalID
	}
	// ADR-254 D4 (W4 cut): the diagnosis crew's Companion VOICE seed. Two JSON
	// keys, always present: `familiar_id` (the PRE-RENAME wire name inside the
	// kennel lane, decoded by name; a rename is a coordinated cut with WP-K) +
	// `companion_name`, valued as they stand at upload time (the learner's first
	// HATCHED Companion). Empty = no Companion = no voice, never a fabricated id.
	voiceID, voiceName := s.uploadVoiceCompanion(ctx, tenantID, gcid)
	payload["familiar_id"] = voiceID
	payload["companion_name"] = voiceName
	if err := s.Publisher.Publish(topicWeaknessDocUploaded, env, payload); err != nil {
		extWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}

	extWriteJSON(w, http.StatusAccepted, growthEdgeUploadJobDTO{
		UploadID:      uploadID,
		Status:        string(wu.StatusQueued),
		ManaReserved:  reserve.PriceUnits,
		ReservationID: reserve.ReservationID,
	})
}

// writeReserveError maps a WS-4 mana-gate failure to an HTTP response. Every
// branch is fail-loud — a refused reserve NEVER falls through to a free upload.
func writeReserveError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, wu.ErrCompanionRequired):
		extWriteError(w, http.StatusPaymentRequired, "COMPANION_REQUIRED",
			"premium weakness analysis requires an active Companion")
	case errors.Is(err, wu.ErrManaNotConfigured):
		extWriteError(w, http.StatusServiceUnavailable, "MANA_NOT_CONFIGURED",
			"upfront mana gate enabled but not wired")
	default:
		var insf *wu.ErrInsufficientMana
		if errors.As(err, &insf) {
			extWriteError(w, http.StatusPaymentRequired, "INSUFFICIENT_MANA", insf.Error())
			return
		}
		// Wallet unreachable / unexpected upstream error — fail loud (502), never
		// a silent free pass.
		extWriteError(w, http.StatusBadGateway, "MANA_RESERVE_FAILED", err.Error())
	}
}

// storeBlobInput is one raw upload to store (envelope-aware).
type storeBlobInput struct {
	tenantID, gcid, uploadID, mime, filename string
	size                                     int64
	body                                     io.Reader
}

// gcmBlobOverhead is the AES-256-GCM envelope overhead (12-byte nonce + 16-byte
// tag) the ciphertext carries over the plaintext.
const gcmBlobOverhead = 28

// storeWeaknessBlob stores the raw upload. When the envelope is wired (WS-5 /
// ADR-205 D8) it seals the bytes with a fresh per-blob DEK + persists the wrapped
// DEK, so what lands in GCS is ciphertext and a later diagnosis-complete can
// crypto-shred it by deleting the wrapped DEK. When the envelope is nil it streams
// plaintext (today's behaviour). Fail-loud: a cipher/store error NEVER falls
// through to a plaintext write.
func (s *ExtServer) storeWeaknessBlob(ctx context.Context, in storeBlobInput) (string, error) {
	if s.BlobEnvelope == nil {
		// DARK: plaintext streaming (today's path; the analyser reads it directly).
		return s.WeaknessBlobs.Upload(ctx, storage.UploadReq{
			TenantID: in.tenantID, UploadID: in.uploadID, MIME: in.mime,
			Filename: in.filename, Size: in.size, Body: in.body,
		})
	}
	if s.BlobDEKs == nil {
		// Boot invariant (cmd/server wires both together) — but never silently
		// store plaintext if it's violated.
		return "", errors.New("weakness blob: envelope wired without a wrapped-DEK store (refusing — cannot crypto-shred)")
	}
	// Envelope ON: AES-GCM needs the whole plaintext, so buffer it (capped so the
	// ciphertext still fits the GCS object cap), seal, persist the wrapped DEK
	// FIRST (so a later upload failure leaves only a sweepable DEK row, never an
	// un-shreddable orphan ciphertext), then upload the ciphertext.
	maxPlain := storage.MaxWeaknessBlobBytes - gcmBlobOverhead
	plaintext, err := io.ReadAll(io.LimitReader(in.body, maxPlain+1))
	if err != nil {
		return "", fmt.Errorf("weakness blob: read: %w", err)
	}
	if int64(len(plaintext)) > maxPlain {
		return "", fmt.Errorf("weakness blob: encrypted size exceeds max %d", maxPlain)
	}
	aad := wb.BlobAAD(in.tenantID, in.gcid, in.uploadID)
	sealed, err := s.BlobEnvelope.Seal(ctx, aad, plaintext)
	if err != nil {
		return "", fmt.Errorf("weakness blob: seal: %w", err)
	}
	if err := s.BlobDEKs.Put(ctx, wb.WrappedDEK{
		UploadID: in.uploadID, TenantID: in.tenantID, LearnerGCID: in.gcid,
		Wrapped: sealed.WrappedDEK, KEKVersion: sealed.KEKVersion, CreatedAt: time.Now().UTC(),
	}); err != nil {
		return "", fmt.Errorf("weakness blob: persist wrapped DEK: %w", err)
	}
	return s.WeaknessBlobs.Upload(ctx, storage.UploadReq{
		TenantID: in.tenantID, UploadID: in.uploadID, MIME: in.mime, Filename: in.filename,
		Size: int64(len(sealed.Ciphertext)), Body: bytes.NewReader(sealed.Ciphertext),
	})
}

// parseOptionalJSONField reads an optional JSON-string multipart field. Empty ⇒
// nil (absent — the caller omits it from the payload). Present-but-malformed ⇒
// error (fail-loud; never forward garbage downstream). Valid ⇒ the raw JSON
// passed through verbatim: the canonical proto-JSON shape is the FE↔analyser
// contract, so chora-consumption forwards it without re-shaping (no lossy
// re-marshal that could drop a field the contract grows).
func parseOptionalJSONField(v string) (json.RawMessage, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	if !json.Valid([]byte(v)) {
		return nil, errors.New("not valid JSON")
	}
	return json.RawMessage(v), nil
}

// parseUploadRightsConsent reads the one-tap consent checkbox (true/on/1/yes).
func parseUploadRightsConsent(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "on", "1", "yes":
		return true
	default:
		return false
	}
}

// writeSourceMaterialError maps a WS-5 source_material gate failure to HTTP.
func writeSourceMaterialError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, wu.ErrSourceMaterialDisabled):
		extWriteError(w, http.StatusForbidden, "SOURCE_MATERIAL_DISABLED",
			"textbook / source-material uploads are not enabled")
	case errors.Is(err, wu.ErrUploadRightsConsentRequired):
		extWriteError(w, http.StatusForbidden, "UPLOAD_RIGHTS_CONSENT_REQUIRED",
			"uploading a textbook requires a one-tap upload-rights consent")
	default:
		extWriteError(w, http.StatusBadRequest, "SOURCE_MATERIAL_REJECTED", err.Error())
	}
}

// handleMeGrowthEdgeUploadByID — GET /v1/me/growth-edges/uploads/{id} (poll).
// uploadVoiceCompanion picks the Companion that fronts the diagnosis voice:
// the learner's first HATCHED Companion on the roster (stage >= 1; an egg
// cannot speak, the agent refuses pre-hatch). Soft-fail: a roster read error is
// logged loudly and the upload proceeds without a voice (empty keys), because
// the diagnosis itself does not need the Companion.
func (s *ExtServer) uploadVoiceCompanion(ctx context.Context, tenantID, gcid string) (companionID, name string) {
	if s.CompanionInstances == nil {
		return "", ""
	}
	roster, err := s.CompanionInstances.ListRosterByOwner(ctx, tenantID, gcid)
	if err != nil {
		log.Printf("consumption: weakness upload voice seed: roster read failed (tenant=%s gcid=%s): %v - publishing without a Companion voice", tenantID, gcid, err)
		return "", ""
	}
	for _, e := range roster {
		if e == nil || e.Instance == nil || e.Growth == nil || e.Growth.Stage < 1 {
			continue
		}
		return e.Instance.CompanionID, e.Instance.Name
	}
	return "", ""
}

func (s *ExtServer) handleMeGrowthEdgeUploadByID(w http.ResponseWriter, r *http.Request) {
	if s.WeaknessUploads == nil {
		extWriteError(w, http.StatusServiceUnavailable, "GROWTH_EDGE_UPLOADS_UNAVAILABLE", "upload path not wired")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, growthEdgeUploadsByIDPrefix), "/")
	if id == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_PATH_ID", "")
		return
	}
	if r.Method != http.MethodGet {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
	job, err := s.WeaknessUploads.Get(ctx, gcid, id)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "GET_FAILED", err.Error())
		return
	}
	if job == nil {
		extWriteError(w, http.StatusNotFound, "UPLOAD_NOT_FOUND", "")
		return
	}
	extWriteJSON(w, http.StatusOK, uploadJobToDTO(*job))
}

// normalizeMIME strips any "; charset=..." parameter + lower-cases.
func normalizeMIME(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}
