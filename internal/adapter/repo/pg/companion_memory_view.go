// companion_memory_view.go — Postgres adapter for the ADR-215 WS-1 tier-(a)
// learner-facing Companion memory READ port (companionmind.MemoryReader).
//
// Reads the SAME companion_memory_recall table as companion_memory_recall.go
// (ADR-173) but on the PASSIVE learner-panel path: recent recalls by recency
// (created_at DESC), NOT the agent-facing cosine top-K search (which needs a
// query embedding). Learner-appropriate projection (ADR-215 D5): it selects
// only id / memory_type / content_text / created_at — never the embedding,
// cosine distance, or model_id.
//
// Per multi-tenant-rls SKILL: the read runs rls.ApplySession (SET LOCAL
// chora.tenant_id + chora.user_gcid) inside the transaction BEFORE the query,
// so RLS policy tenant_isolation scopes the rows. Soft-deleted + TTL-expired
// rows are excluded. Callers MUST set tenant_id on ctx first, else ApplySession
// returns rls.ErrNoTenantContext (fail-loud).
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
)

// ErrCompanionIDRequired is returned when a memory-view read omits the companion id.
var ErrCompanionIDRequired = errors.New("pg: companion_id required")

// CompanionMemoryViewRepo is the pg-backed companionmind.MemoryReader.
type CompanionMemoryViewRepo struct {
	tx TxRunner
}

// NewCompanionMemoryViewRepo constructs the repo around a TxRunner.
func NewCompanionMemoryViewRepo(tx TxRunner) *CompanionMemoryViewRepo {
	return &CompanionMemoryViewRepo{tx: tx}
}

// Compile-time guarantee that *CompanionMemoryViewRepo satisfies the port.
var _ companionmind.MemoryReader = (*CompanionMemoryViewRepo)(nil)

// RecentMemories returns the Companion's live recalls newest-first, capped by a
// clamped limit and optionally narrowed to specific memory_types. RLS-scoped;
// excludes soft-deleted + expired rows. Each row carries its grounded provenance
// (source_metadata) when it has any.
//
// ⚠ The RLS tenant comes from the CONTEXT, never from q.TenantID. That is
// deliberate and load-bearing: the ctx tenant is stamped from the validated
// session (extRequireContext → mesh headers), whereas Query is just a struct a
// caller filled in. Deriving the RLS GUC from a parameter would make the last
// line of defence trust its caller. A ctx with no tenant fails LOUD in
// rls.ApplySession (ErrNoTenantContext) — it does not read 0 rows silently.
func (r *CompanionMemoryViewRepo) RecentMemories(ctx context.Context, q companionmind.Query) ([]companionmind.EpisodicMemory, error) {
	if strings.TrimSpace(q.CompanionID) == "" {
		return nil, ErrCompanionIDRequired
	}
	limit := companionmind.ClampMemoryLimit(q.MemoryLimit)

	// Bind an empty (never nil) text[] so the SQL's cardinality()=0 branch selects
	// every type — the panel's existing contract.
	types := q.MemoryTypes
	if types == nil {
		types = []string{}
	}

	out := make([]companionmind.EpisodicMemory, 0, limit)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, qr Querier) error {
		if err := rls.ApplySession(ctx, qr); err != nil {
			return err
		}
		rows, err := qr.Query(ctx, recentCompanionMemoriesSQL, q.CompanionID, limit, types)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil // stub Querier — empty
		}
		defer rows.Close()
		for rows.Next() {
			var m companionmind.EpisodicMemory
			var sourceMetadata []byte
			if err := rows.Scan(&m.ID, &m.MemoryType, &m.Content, &m.CreatedAt, &sourceMetadata); err != nil {
				return fmt.Errorf("pg: scan companion_memory_recall view: %w", err)
			}
			p, err := decodeMemoryProvenance(sourceMetadata)
			if err != nil {
				return err
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

// decodeMemoryProvenance turns the source_metadata JSONB into the read model.
//
// It unmarshals through companion.MemoryProvenance — the SAME json-tagged struct
// the writer marshals from — so there is exactly ONE definition of the persisted
// shape and the round-trip cannot drift. Re-declaring the tags here would be a
// second, silently-divergable copy of the contract.
//
// NULL / empty ⇒ nil, not an error: a note written before mig 0094 genuinely has
// no recoverable provenance, and NULL is the honest record of that. Malformed
// JSON, by contrast, is LOUD — it means we wrote something we cannot read back.
func decodeMemoryProvenance(raw []byte) (*companionmind.Provenance, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var stored companion.MemoryProvenance
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("pg: decode companion_memory_recall source_metadata: %w", err)
	}
	cits := make([]companionmind.ProvenanceCitation, 0, len(stored.Citations))
	for _, c := range stored.Citations {
		cits = append(cits, companionmind.ProvenanceCitation{
			Domain:  c.Domain,
			Title:   c.Title,
			Snippet: c.Snippet,
		})
	}
	p := &companionmind.Provenance{WebSearchQueries: stored.WebSearchQueries, Citations: cits}
	if p.IsEmpty() {
		// `{}` on disk says the same thing NULL does. Collapse it so the wire has
		// one shape for "nothing to attribute".
		return nil, nil
	}
	return p, nil
}

// recentCompanionMemoriesSQL selects the topN ($2) live recalls for a companion
// ($1) in recency order, optionally narrowed to the memory_types in $3 (an EMPTY
// array means every type). NO embedding / distance / model_id columns are
// projected (learner-appropriate depth, ADR-215 D5). Excludes soft-deleted +
// TTL-expired rows.
//
// source_metadata (mig 0094) is the grounded provenance; NULL on every
// non-grounded note and on every note written before that migration.
//
// $3 is cast to text[] at BOTH its use sites. PostgreSQL deduces one type per
// parameter across all contexts, and two contexts wanting different types is a
// hard 42P08 at PREPARE — the exact class the prepare-smoke list exists to catch.
const recentCompanionMemoriesSQL = `
SELECT id, memory_type, content_text, created_at, source_metadata
  FROM companion_memory_recall
 WHERE companion_id = $1
   AND deleted_at IS NULL
   AND (expires_at IS NULL OR expires_at > now())
   AND (cardinality($3::text[]) = 0 OR memory_type = ANY($3::text[]))
 ORDER BY created_at DESC
 LIMIT $2`
