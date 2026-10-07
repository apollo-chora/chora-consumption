// companion_memory_recall.go — Postgres (pgvector) adapter for the F4
// per-Companion RAG memory port (companion.CompanionMemory).
//
// Schema mirrors migrations/0045_familiar_memory_recall.up.sql (objects renamed live by 0110_companion_rename):
//
//	companion_memory_recall
//	  id                UUID PRIMARY KEY DEFAULT gen_random_uuid()
//	  tenant_id         UUID NOT NULL
//	  owner_gcid        UUID NOT NULL
//	  companion_id       UUID NOT NULL
//	  scope_key         TEXT NOT NULL          -- 'companion:{companion_id}'
//	  memory_type       VARCHAR(32) NOT NULL DEFAULT 'chat_turn'
//	  content_text      TEXT NOT NULL
//	  embedding         vector(1024) NOT NULL
//	  model_id          VARCHAR(64) NOT NULL
//	  source_turn_id    UUID
//	  source_session_id UUID
//	  created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
//	  expires_at        TIMESTAMPTZ            -- TTL; NULL = no expiry
//	  deleted_at        TIMESTAMPTZ            -- soft-delete
//	  source_metadata   JSONB                  -- mig 0094; grounded provenance
//
// source_metadata (ADR-231 D4, CHO-2179) carries a GROUNDED note's durable
// provenance — the issued web_search_queries + citations{domain,title,snippet}.
// It is NULL for every non-grounded memory_type. The Vertex grounding-api-redirect
// uri is deliberately NOT persisted: it expires (~30 days), so a note that stored
// it would rot to dead links. Provenance is kept OUT of content_text because that
// column is what gets vector-embedded.
//
// Per multi-tenant-rls SKILL: every read/write runs rls.ApplySession to set
// chora.tenant_id + chora.user_gcid session GUCs inside the transaction
// (defence-in-depth complementing per-database domain isolation). Callers MUST
// set tenant_id on ctx BEFORE invoking these methods — otherwise the methods
// return rls.ErrNoTenantContext from ApplySession.
//
// pgvector has no first-class pgx binary codec wired here, so the 1024-d
// embedding is bound as a Postgres text literal `[a,b,...]` and cast with
// `$N::vector` in SQL — identical to the chora-creation atom_embeddings path.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// defaultRecallTopK bounds a Recall call when the caller passes topK <= 0.
const defaultRecallTopK = 5

// defaultMemoryType matches the table DEFAULT — applied when the input leaves
// MemoryType empty.
const defaultMemoryType = "chat_turn"

// CompanionMemoryRepo is the pgvector-backed implementation of
// companion.CompanionMemory.
type CompanionMemoryRepo struct {
	tx TxRunner

	// defaultTTL is the retention applied to a Record that does not name its own
	// (register 6.4 R6). Zero keeps the pre-R6 behaviour exactly: expires_at NULL,
	// retained until soft-delete or closure.
	defaultTTL time.Duration
}

// NewCompanionMemoryRepo constructs the repo around a TxRunner. Production wires
// a PgxTxRunner backed by a pgxpool.Pool; tests inject memStubTxRunner.
func NewCompanionMemoryRepo(tx TxRunner) *CompanionMemoryRepo {
	return &CompanionMemoryRepo{tx: tx}
}

// WithDefaultTTL sets the store-wide retention for memories recorded without an
// explicit TTL, and returns the repo for chaining.
//
// The TTL belongs HERE rather than on the callers. There are five Record call
// sites (chat turn, ceremony note, skill-invoke note, the p1b gRPC tool note and
// the weakness-RAG subscriber) and not one of them ever set
// RecordMemoryInput.TTL, which is exactly why expires_at was universally NULL
// and no memory had ever expired despite the column and the Recall filter both
// shipping in mig 0045. Threading a duration through five call sites would leave
// the sixth to be forgotten; one store-level default covers every writer.
//
// Zero or negative is a no-op, so an unconfigured deployment behaves precisely
// as before. That default is deliberate: silently adopting some retention here
// would start destroying learner memory on the next deploy, and how long a
// learner's conversation is kept is an operator's decision, not a constructor's.
func (r *CompanionMemoryRepo) WithDefaultTTL(d time.Duration) *CompanionMemoryRepo {
	if d > 0 {
		r.defaultTTL = d
	}
	return r
}

// Compile-time guarantee that *CompanionMemoryRepo satisfies the port.
var _ companion.CompanionMemory = (*CompanionMemoryRepo)(nil)

// ---------------------------------------------------------------------------
// Record — persist one per-Companion memory
// ---------------------------------------------------------------------------

// Record persists a single memory row. The 1024-d embedding is precomputed by
// the Embedder port and bound as a `[...]::vector` literal. memory_type defaults
// to "chat_turn"; scope_key is minted via companion.MemoryScopeKey; expires_at is
// Now+TTL when TTL > 0, else NULL (no expiry).
func (r *CompanionMemoryRepo) Record(ctx context.Context, in companion.RecordMemoryInput) error {
	if strings.TrimSpace(in.CompanionID) == "" {
		return errors.New("pg: companion_id required")
	}
	if strings.TrimSpace(in.ContentText) == "" {
		return errors.New("pg: content_text required")
	}
	if len(in.Embedding) == 0 {
		return errors.New("pg: embedding required")
	}

	memoryType := in.MemoryType
	if strings.TrimSpace(memoryType) == "" {
		memoryType = defaultMemoryType
	}
	scopeKey := companion.MemoryScopeKey(in.CompanionID)

	// expires_at: nil for no-expiry, else Now + TTL. An explicit per-call TTL
	// wins over the store default so a caller with a genuine reason to differ
	// still can; absent both, the column stays NULL exactly as before R6.
	ttl := in.TTL
	if ttl <= 0 {
		ttl = r.defaultTTL
	}
	var expiresAt any
	if ttl > 0 {
		expiresAt = in.Now.Add(ttl)
	}

	// source_metadata: a grounded note's durable provenance as JSONB (ADR-231 D4).
	// nil Provenance ⇒ NULL — a non-grounded note (chat_turn / recap / ceremony)
	// carries no empty husk. A marshal failure is LOUD: we would rather fail the
	// note than persist one that silently cannot say where it came from.
	var sourceMetadata any
	if in.Provenance != nil {
		b, err := json.Marshal(in.Provenance)
		if err != nil {
			return fmt.Errorf("pg: marshal companion_memory_recall source_metadata: %w", err)
		}
		sourceMetadata = b
	}

	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, insertCompanionMemoryRecallSQL,
			in.TenantID,
			in.OwnerGCID,
			in.CompanionID,
			scopeKey,
			memoryType,
			in.ContentText,
			vectorLiteral(in.Embedding),
			in.ModelID,
			nullableUUID(in.SourceTurnID),
			nullableUUID(in.SourceSessionID),
			in.Now,
			expiresAt,
			sourceMetadata,
		); err != nil {
			return fmt.Errorf("pg: insert companion_memory_recall: %w", err)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Recall — cosine-nearest live memories for a Companion
// ---------------------------------------------------------------------------

// Recall returns the topK live (not soft-deleted, not expired) memories for the
// Companion, ordered by ascending cosine distance to queryEmbedding. topK <= 0
// defaults to defaultRecallTopK. The query embedding is bound as a
// `[...]::vector` literal. RLS policy `tenant_isolation` scopes by tenant.
func (r *CompanionMemoryRepo) Recall(ctx context.Context, companionID string, queryEmbedding []float32, topK int) ([]companion.MemoryRow, error) {
	if strings.TrimSpace(companionID) == "" {
		return nil, errors.New("pg: companion_id required")
	}
	if len(queryEmbedding) == 0 {
		return nil, errors.New("pg: query embedding required")
	}
	if topK <= 0 {
		topK = defaultRecallTopK
	}
	lit := vectorLiteral(queryEmbedding)

	out := make([]companion.MemoryRow, 0, topK)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, recallCompanionMemoryRecallSQL, companionID, lit, topK)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var m companion.MemoryRow
			var rawProvenance []byte
			if err := rows.Scan(
				&m.ID,
				&m.MemoryType,
				&m.ContentText,
				&m.CreatedAt,
				&m.Distance,
				&rawProvenance,
			); err != nil {
				return fmt.Errorf("pg: scan companion_memory_recall: %w", err)
			}
			p, derr := decodeRecallProvenance(rawProvenance)
			if derr != nil {
				return derr
			}
			m.Provenance = p
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// decodeRecallProvenance turns the source_metadata JSONB into the recall row's
// provenance (CHO-2192).
//
// It unmarshals through companion.MemoryProvenance — the SAME json-tagged struct
// Record marshals FROM, a few lines up this file — so there is exactly one
// definition of the persisted shape and the round-trip cannot drift. (The view
// path decodes through the same struct for the same reason.)
//
// Three states, three behaviours, and the distinction is the whole point:
//
//	NULL / empty  ⇒ nil, no error. A note written before mig 0094 genuinely has
//	                no recoverable provenance, and NULL is the honest record of
//	                that. The row is still GROUNDED (see MemoryRow.IsGrounded) —
//	                the caller discloses "sources not recorded" rather than
//	                inventing any.
//	`{}` husk     ⇒ nil. It says exactly what NULL says; collapsing it keeps ONE
//	                wire shape for "nothing to attribute", so the FE can never
//	                render an empty attribution block.
//	malformed     ⇒ LOUD. It means we wrote something we cannot read back — real
//	                corruption, not an honest absence. Never papered over.
func decodeRecallProvenance(raw []byte) (*companion.MemoryProvenance, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var p companion.MemoryProvenance
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("pg: decode companion_memory_recall source_metadata: %w", err)
	}
	if p.IsEmpty() {
		return nil, nil
	}
	return &p, nil
}

// ---------------------------------------------------------------------------
// vector literal helper
// ---------------------------------------------------------------------------

// vectorLiteral renders a float32 slice as a Postgres pgvector text literal
// `[a,b,c]`. It is cast with `$N::vector` in SQL. float32 precision is
// preserved via FormatFloat with bitSize 32 (no trailing-zero noise).
func vectorLiteral(v []float32) string {
	if len(v) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// ---------------------------------------------------------------------------
// SQL templates (review-via-test)
// ---------------------------------------------------------------------------

// insertCompanionMemoryRecallSQL writes one memory row. id + created_at default
// where omitted; embedding ($7) is bound as a text literal and cast to vector.
// source_turn_id / source_session_id are nullableUUID(nil when empty).
// source_metadata ($13, mig 0094) is the grounded provenance JSONB — NULL for a
// non-grounded note (ADR-231 D4).
const insertCompanionMemoryRecallSQL = `
INSERT INTO companion_memory_recall
       (tenant_id, owner_gcid, companion_id, scope_key, memory_type,
        content_text, embedding, model_id,
        source_turn_id, source_session_id, created_at, expires_at,
        source_metadata)
VALUES ($1, $2, $3, $4, $5,
        $6, $7::vector, $8,
        $9, $10, $11, $12,
        $13)`

// recallCompanionMemoryRecallSQL retrieves the topK ($3) cosine-nearest live
// memories for a companion ($1). The query embedding ($2) is bound as a text
// literal and cast to vector. Excludes soft-deleted + expired rows.
//
// ⚠ source_metadata (CHO-2192) is NOT optional garnish. Until it was projected
// here, the recall path — the ONE path that can attribute a claim the Companion
// is making right now — returned five columns and left the sixth on disk. The
// INSERT was correct, mig 0094 was applied, the provenance was written... and the
// feature was 100% dead, because nothing ever SELECTed it. That is CHO-2179's
// gotcha in a second costume: **the reader never asked for the column.** If you
// are tempted to trim this projection, the attribution silently dies and every
// test still passes.
const recallCompanionMemoryRecallSQL = `
SELECT id, memory_type, content_text, created_at, (embedding <=> $2::vector) AS distance,
       source_metadata
  FROM companion_memory_recall
 WHERE companion_id = $1 AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at > now())
 ORDER BY embedding <=> $2::vector
 LIMIT $3`

// softDeleteMemoryByCompanionSQL — CHO-2096 retire purge: soft-delete every
// LIVE memory row the companion retains (tenant + companion scoped; RLS also
// fences the tenant). Never a hard DELETE (ddd-enforcement #4).
const softDeleteMemoryByCompanionSQL = `
UPDATE companion_memory_recall
   SET deleted_at = now()
 WHERE tenant_id = $1 AND companion_id = $2 AND deleted_at IS NULL`

// ---------------------------------------------------------------------------
// Expiry + learner-initiated delete (register 6.4 R6)
// ---------------------------------------------------------------------------

// memoryRedactedTombstone replaces the conversation text on an expired or
// learner-deleted row. It is a marker rather than ” because content_text
// carries CHECK (length(content_text) > 0) from mig 0045.
const memoryRedactedTombstone = "[redacted]"

// redactedEmbedding is the 1024-d zero vector written over the embedding when a
// memory is expired or deleted.
//
// The embedding is redacted alongside the text and that is not belt-and-braces.
// It is a lossy but genuine representation of the same conversation: it stays
// cosine-matchable, and inversion back toward the source text is a known attack
// class. Erasing the prose while leaving the vector would still answer "roughly
// what did this learner talk about", which is the question expiry exists to stop
// answering. Zero is safe here because these rows carry deleted_at and Recall
// excludes them, so nothing ever computes a distance against them (cosine
// against a zero vector is undefined).
const redactedEmbedding = "array_fill(0::real, ARRAY[1024])::vector"

// purgeExpiredMemorySQL tombstones AND empties every live row whose TTL has
// passed. Scoped to the ctx tenant by RLS (there is no cross-tenant sweep: the
// app role is NOBYPASSRLS and widening it would need a new ADR amending the
// ADR-165/184/192 bypass chain, so a platform sweep must iterate tenants).
//
// Never a hard DELETE (ddd-enforcement #4): the row survives as a contentless
// tombstone carrying its ids and timestamps for audit.
const purgeExpiredMemorySQL = `
UPDATE companion_memory_recall
   SET deleted_at = now(),
       content_text = $1,
       embedding = ` + redactedEmbedding + `
 WHERE deleted_at IS NULL
   AND expires_at IS NOT NULL
   AND expires_at <= now()`

// PurgeExpired erases every expired memory for the tenant in ctx and returns the
// number of rows redacted.
//
// WHY THIS EXISTS AT ALL, given expires_at shipped in mig 0045 and Recall has
// always filtered it. Not one of the five Record call sites ever set a TTL, so
// expires_at was universally NULL, the filter could never exclude anything, and
// no memory had ever expired. The filter alone would also be the wrong fix: it
// hides a row while the learner's conversation and its embedding stay on disk
// indefinitely, which is not what expiry means to the person whose conversation
// it was.
func (r *CompanionMemoryRepo) PurgeExpired(ctx context.Context) (int64, error) {
	var n int64
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		// MANDATORY, and for the same reason SoftDeleteByCompanion documents
		// above: without the GUC this UPDATE matches no row and reports a clean
		// (0, nil), indistinguishable from "nothing had expired".
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, purgeExpiredMemorySQL, memoryRedactedTombstone)
		if err != nil {
			return fmt.Errorf("purge expired companion memory: %w", err)
		}
		n = tag.RowsAffected
		return nil
	})
	return n, err
}

// deleteOwnedMemorySQL erases ONE memory the caller owns.
//
// The owner_gcid predicate is load-bearing, not defensive dressing. RLS fences
// the TENANT only, so without it any learner could erase a co-tenant's memory by
// guessing an id. Tenant comes from both the explicit predicate and the policy;
// ownership has no policy behind it and must be stated here.
const deleteOwnedMemorySQL = `
UPDATE companion_memory_recall
   SET deleted_at = now(),
       content_text = $1,
       embedding = ` + redactedEmbedding + `
 WHERE id = $2
   AND tenant_id = $3
   AND owner_gcid = $4
   AND deleted_at IS NULL`

// DeleteOwnedMemory erases one memory on the learner's own instruction and
// returns the number of rows affected. A return of 0 means no LIVE row matched
// (already deleted, wrong owner, or unknown id) and the caller must surface that
// as a 404 rather than reporting a deletion that never happened.
func (r *CompanionMemoryRepo) DeleteOwnedMemory(ctx context.Context, tenantID, ownerGCID, memoryID string) (int64, error) {
	if strings.TrimSpace(tenantID) == "" {
		return 0, errors.New("pg: tenant_id required")
	}
	if strings.TrimSpace(ownerGCID) == "" {
		return 0, errors.New("pg: owner_gcid required")
	}
	if strings.TrimSpace(memoryID) == "" {
		return 0, errors.New("pg: memory_id required")
	}

	var n int64
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, deleteOwnedMemorySQL, memoryRedactedTombstone, memoryID, tenantID, ownerGCID)
		if err != nil {
			return fmt.Errorf("delete owned companion memory: %w", err)
		}
		n = tag.RowsAffected
		return nil
	})
	return n, err
}

// SoftDeleteByCompanion implements companion.MemoryRetirer (CHO-2096). Returns
// the purged row count; 0 is a valid no-op (nothing was remembered).
func (r *CompanionMemoryRepo) SoftDeleteByCompanion(ctx context.Context, tenantID, companionID string) (int64, error) {
	var n int64
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		// MANDATORY — without it this purge silently deletes NOTHING.
		//
		// companion_memory_recall is FORCE-RLS'd under
		// USING (tenant_id = current_setting('chora.tenant_id', true)::uuid), and
		// the app role is NOBYPASSRLS, so the policy stacks on top of the explicit
		// tenant predicate below even for the owner. With the GUC unset the policy
		// evaluates against NULL, matches no row, and the UPDATE tombstones nothing
		// while returning (0, nil) — indistinguishable from "nothing was remembered".
		// On a POOLED connection it is worse: a prior SET LOCAL leaves the custom
		// GUC as the EMPTY STRING (not unset), so ''::uuid raises 22P02 and the
		// retirement fails looking like a data bug.
		//
		// Verified against live chora_consumption 2026-07-14: with the GUC, 62 rows
		// were visible for the fixture tenant; without it, 0.
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, softDeleteMemoryByCompanionSQL, tenantID, companionID)
		if err != nil {
			return fmt.Errorf("soft-delete companion memory: %w", err)
		}
		n = tag.RowsAffected
		return nil
	})
	return n, err
}
