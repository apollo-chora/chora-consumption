// weakness_companion_rag_wiring.go — composition root for the ADR-205 D5
// Companion-RAG coaching seam (CHO-1966): on a completed Growth-Edge analysis
// where the learner opted into familiar_coaching at the bounded HITL review, the
// diagnosis is written into their Companion's pgvector memory (ADR-173).
//
// DARK by default behind WEAKNESS_COMPANION_RAG_ENABLED. Per feedback_no_inline_config
// the flag is read here at the composition root; the subscriber + hook never
// touch the environment. The hook itself SOFT-FAILS (coaching memory is a
// best-effort enhancement — the diagnosis edges always persist); this wiring is
// fail-loud only at boot when the feature is ENABLED but a hard dependency
// (Embedder / CompanionMemory) is missing, so the feature is never a silent no-op.
package main

import (
	"log"
	"os"
	"strings"
	"time"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
)

func weaknessCompanionRAGEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("WEAKNESS_COMPANION_RAG_ENABLED")), "true")
}

// maybeWithCompanionRAGHook attaches the ADR-205 D5 coaching hook to the analyzed
// subscriber when WEAKNESS_COMPANION_RAG_ENABLED=true: a completed analysis whose
// learner opted into familiar_coaching writes the diagnosis into their Companion's
// pgvector memory. DARK by default ⇒ returns sub unchanged. The Companion resolver
// is the shared pickCompanionResolver — since CHO-2012 (ADR-218 D7) it attributes
// through Goal attachment (most recent summon) with the oldest-roster read as
// fallback; subject-aware routing by edge.Category→Instance.Specialization stays
// a future enhancement. `goals` may be nil (legacy roster-only behaviour).
func maybeWithCompanionRAGHook(sub *subscribers.WeaknessAnalyzedSubscriber, srv *httpadapter.Server, goals subscribers.GoalAttachmentLister) *subscribers.WeaknessAnalyzedSubscriber {
	if !weaknessCompanionRAGEnabled() {
		log.Printf("consumption: weakness companion-RAG coaching DISABLED (DARK) — set WEAKNESS_COMPANION_RAG_ENABLED=true to enable [ADR-205 D5 / CHO-1966]")
		return sub
	}
	// ENABLED — the embedder + memory are mandatory; fail-loud at boot rather than
	// silently disable coaching (the subscriber-wiring already guarantees a non-nil
	// Embedder + pgx pool before this runs, so CompanionMemory is wired too).
	if srv == nil || srv.Embedder == nil || srv.CompanionMemory == nil {
		log.Fatalf("consumption: WEAKNESS_COMPANION_RAG_ENABLED=true but Embedder/CompanionMemory not wired (need CHORA_MODEL_GATEWAY_GRPC_URL + a pg pool). Refusing to boot.")
	}
	resolver := pickCompanionResolver(srv, goals)
	hook := subscribers.NewCompanionRAGHook(
		resolver, srv.Embedder, srv.CompanionMemory, srv.CompanionEmbeddingModelID,
		func() time.Time { return time.Now().UTC() },
	)
	log.Printf("consumption: weakness companion-RAG coaching ENABLED — diagnosis → Companion pgvector memory on familiar_coaching opt-in [ADR-205 D5 / CHO-1966]")
	return sub.WithCompanionRAGHook(hook)
}
