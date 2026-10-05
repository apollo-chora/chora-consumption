// weakness_companion_rag.go — the Companion-RAG coaching hook (ADR-205 D5 /
// ADR-173, CHO-1966). When a learner opts into familiar_coaching at the bounded
// HITL review, the analysed Growth-Edge diagnosis is distilled into a memory and
// written into THEIR Companion's pgvector store, so the companion can recall it in
// future turns ("last time I reviewed your test you were shaky on …").
//
// This is the consumption-side seam flagged by the orchestrator's WS-7
// adapter/weakness/outputs.py: the orchestrator never writes another domain's DB
// (cross-DB forbidden) — it emits weakness.analyzed.v1 carrying output_selection,
// and chora-consumption performs the familiar_coaching write here.
//
// The learner's diagnosis descriptor (misconceptions / sample-wrong) is NEVER the
// memory content verbatim — only the reframed concept labels + the one-line
// summary, consistent with ADR-205 D5 ("the learner never sees the raw
// descriptor"). The hook is built in cmd/server (maybeWithCompanionRAGHook) from
// the Companion resolver + embedder + memory ports; this file is the pure logic.
package subscribers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// CompanionMemoryWriter is the slice of the per-Companion pgvector memory port the
// coaching hook needs (ADR-173). *companion.CompanionMemory satisfies it.
type CompanionMemoryWriter interface {
	Record(ctx context.Context, in companion.RecordMemoryInput) error
}

// NewCompanionRAGHook builds the ADR-205 D5 coaching hook. It resolves the
// learner's active Companion, distils the diagnosed edges into a recall memory,
// embeds it as a RETRIEVAL_DOCUMENT, and records it. ErrNoCompanion (the learner
// has no Companion yet) is a silent drop, not an error. Any other failure is
// returned to the subscriber, which SOFT-FAILS it (best-effort coaching memory;
// the diagnosis edges already persisted).
//
// now defaults to time.Now().UTC() when nil (tests inject a fixed clock).
func NewCompanionRAGHook(resolver CompanionResolver, embedder companion.Embedder, memory CompanionMemoryWriter, modelID string, now func() time.Time) CompanionRAGHook {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return func(ctx context.Context, p WeaknessAnalyzedPayload) error {
		companionID, err := resolver.ResolveActiveCompanion(ctx, p.TenantID, p.LearnerGCID)
		if errors.Is(err, ErrNoCompanion) {
			return nil // no Companion yet → nothing to coach (provisioning is async)
		}
		if err != nil {
			return fmt.Errorf("subscribers: resolve companion for coaching: %w", err)
		}
		content := composeWeaknessDiagnosisMemory(p.Edges)
		if strings.TrimSpace(content) == "" {
			return nil // nothing diagnosed (empty/unreadable upload) → nothing to record
		}
		vec, err := embedder.Embed(ctx, companion.EmbedInput{
			Text:     content,
			TaskType: companion.EmbedTaskDocument,
			TenantID: p.TenantID,
		})
		if err != nil {
			return fmt.Errorf("subscribers: embed weakness diagnosis for coaching: %w", err)
		}
		return memory.Record(ctx, companion.RecordMemoryInput{
			TenantID:    p.TenantID,
			OwnerGCID:   p.LearnerGCID,
			CompanionID: companionID,
			MemoryType:  "weakness_diagnosis",
			ContentText: content,
			Embedding:   vec,
			ModelID:     modelID,
			// Deterministic provenance keyed on the upload — also the natural
			// idempotency key if the memory store later dedups by source.
			SourceSessionID: "weakness:" + p.UploadID,
			Now:             now(),
		})
	}
}

// composeWeaknessDiagnosisMemory distils the analysed edges into a concise,
// Companion-facing recall memory. Uses the reframed concept label + the one-line
// descriptor summary (never the raw misconceptions / sample-wrong). Empty when
// nothing keyable was diagnosed.
//
// The lead clause is framed by SOURCE so the memory never misattributes its
// provenance: the analyser-born path references the learner's reviewed upload;
// the ceremony-born (CHO-2040 binding-ceremony remediate ticks) path has NO
// upload — those edges are LEARNER-DECLARED — so it names the ceremony instead.
func composeWeaknessDiagnosisMemory(edges []ExtractedGrowthEdgePayload) string {
	parts := make([]string, 0, len(edges))
	for _, e := range edges {
		label := strings.TrimSpace(e.ConceptLabel)
		if label == "" {
			continue
		}
		if summary := edgeDescriptorSummary(e.DescriptorJSON); summary != "" {
			parts = append(parts, label+" — "+summary)
		} else {
			parts = append(parts, label)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	joined := strings.Join(parts, "; ")
	if ceremonyBornEdges(edges) {
		return "At the Companion binding ceremony the learner chose to strengthen these concepts next: " +
			joined + "."
	}
	return "Growth-Edge diagnosis from the learner's reviewed upload — concepts to grow next: " +
		joined + "."
}

// ceremonyBornEdges reports whether the analysis originated at the Companion
// binding ceremony (CHO-2040) rather than a graded upload — the ceremony
// confirm hook tags every remediate edge "ceremony" and derives a synthetic
// upload id (there is no weakness_doc_uploads row). Any tagged edge marks the
// whole event ceremony-born: a single weakness.analyzed event never mixes
// sources (analyser-born vs. ceremony-born).
func ceremonyBornEdges(edges []ExtractedGrowthEdgePayload) bool {
	for _, e := range edges {
		for _, tag := range e.Tags {
			if strings.EqualFold(strings.TrimSpace(tag), "ceremony") {
				return true
			}
		}
	}
	return false
}

// edgeDescriptorSummary pulls just the "summary" key from the analyser's
// descriptor_json (fail-soft: malformed/empty → ""). Decoupled from the
// learner_weakness Descriptor shape on purpose — only the summary is surfaced.
func edgeDescriptorSummary(descriptorJSON string) string {
	if strings.TrimSpace(descriptorJSON) == "" {
		return ""
	}
	var d struct {
		Summary string `json:"summary"`
	}
	_ = json.Unmarshal([]byte(descriptorJSON), &d)
	return strings.TrimSpace(d.Summary)
}
