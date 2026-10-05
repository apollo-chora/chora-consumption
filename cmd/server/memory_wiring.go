// memory_wiring.go — composition root for the ADR-173 / F4 per-Companion
// pgvector RAG conversational memory.
//
// Wires two collaborators onto the Server:
//   - CompanionMemory: the pg-backed companion_memory_recall store (cosine
//     top-K recall + record). Attached whenever a pgx pool is available.
//   - Embedder: the chora-model-gateway Embed client (G1' gap 1). Attached
//     ONLY when CHORA_MODEL_GATEWAY_GRPC_URL is set
//     (feedback_no_inline_config); every embedding then lands a ledger row at
//     the gateway chokepoint.
//
// The chat handler activates memory only when BOTH are non-nil; otherwise it
// skips recall + record and chat is unchanged (graceful degrade — F4 is
// supplementary, never load-bearing).
package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
)

// wireCompanionMemory attaches the F4 per-Companion memory collaborators.
func wireCompanionMemory(_ context.Context, srv *httpadapter.Server, pool *pgxpool.Pool) {
	if pool != nil {
		repo := pg.NewCompanionMemoryRepo(pg.NewPgxTxRunner(pool)).
			WithDefaultTTL(companionMemoryTTL())
		srv.CompanionMemory = repo
		// CHO-2096 — retire-time purge rides the same repo (narrow port).
		srv.CompanionMemoryRetire = repo
	} else {
		log.Printf("consumption: F4 companion memory store NOT wired (no pgx pool); recall+record disabled")
	}

	// Optional recall depth override.
	if raw := strings.TrimSpace(os.Getenv("COMPANION_MEMORY_RECALL_TOPK")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			srv.CompanionMemoryRecallTopK = n
		} else {
			log.Printf("consumption: COMPANION_MEMORY_RECALL_TOPK=%q invalid — handler default applies", raw)
		}
	}

	target := strings.TrimSpace(os.Getenv("CHORA_MODEL_GATEWAY_GRPC_URL"))
	if target == "" {
		log.Printf("consumption: CHORA_MODEL_GATEWAY_GRPC_URL unset: F4 memory DISABLED (recall+record skipped)")
		return
	}
	embedder, err := clients.NewGatewayEmbeddingClient(target, 0)
	if err != nil {
		log.Printf("consumption: gateway embedding client failed: %v, F4 memory DISABLED", err)
		return
	}
	srv.Embedder = embedder
	srv.CompanionEmbeddingModelID = embedder.ModelID()
	log.Printf("consumption: F4 companion memory wired via model-gateway (embedding_model=%s, recall_topK=%d, store=%t)",
		srv.CompanionEmbeddingModelID, srv.CompanionMemoryRecallTopK, srv.CompanionMemory != nil)
}

// companionMemoryTTL reads the store-wide retention for conversation memory
// (register 6.4 R6) from COMPANION_MEMORY_TTL_DAYS. Per secrets-and-env the
// value comes from the deployment, never from a literal in code.
//
// UNSET OR ZERO MEANS NO EXPIRY, and that is the safe default rather than an
// oversight: adopting some retention here would begin erasing learner
// conversation on the next rollout, which is an operator's decision. The column
// and the Recall filter have existed since mig 0045; what was missing was any
// writer that set them, so until this env is set the behaviour is unchanged and
// expiry remains dormant.
func companionMemoryTTL() time.Duration {
	raw := strings.TrimSpace(os.Getenv("COMPANION_MEMORY_TTL_DAYS"))
	if raw == "" {
		log.Printf("consumption: COMPANION_MEMORY_TTL_DAYS unset: conversation memory is retained indefinitely (R6 expiry dormant)")
		return 0
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 0 {
		// Fail loud rather than silently retaining forever: a typo'd retention
		// must not read as a deliberate "keep everything".
		log.Printf("consumption: COMPANION_MEMORY_TTL_DAYS=%q invalid (want a non-negative integer); retention NOT applied", raw)
		return 0
	}
	if days == 0 {
		log.Printf("consumption: COMPANION_MEMORY_TTL_DAYS=0: conversation memory retained indefinitely (explicit)")
		return 0
	}
	log.Printf("consumption: conversation memory retention = %d days (R6)", days)
	return time.Duration(days) * 24 * time.Hour
}
