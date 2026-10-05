// concept_suggestion.go — Postgres adapter for the Companion Suggestion
// aggregate (conceptgraph.SuggestionRepository, ADR-212 WS-4). Schema mirrors
// migrations/0060_concept_suggestions.up.sql.
//
// Per multi-tenant-rls: every read/write runs rls.ApplySession (SET LOCAL
// chora.tenant_id [+ chora.user_gcid]) inside the transaction BEFORE the query;
// per-learner scoping is an explicit learner_gcid predicate (writes scoped by id
// AND tenant AND learner as defence-in-depth). atom_refs is a UUID[] bound from
// []string via ::uuid[] and read back ::text[]. Nullable payload columns (the
// non-applicable half per kind, + optional stamps) are written as SQL NULL via
// nullIfEmpty and scanned into *string locals.
package pg

import (
	"context"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

// SuggestionRepo is the Postgres-backed conceptgraph.SuggestionRepository.
type SuggestionRepo struct {
	tx TxRunner
}

// NewSuggestionRepo constructs the repo around a TxRunner.
func NewSuggestionRepo(tx TxRunner) *SuggestionRepo {
	return &SuggestionRepo{tx: tx}
}

// Compile-time check: the pg adapter satisfies the domain ports.
var (
	_ conceptgraph.SuggestionRepository = (*SuggestionRepo)(nil)
	_ conceptgraph.SuggestionAccepter   = (*SuggestionRepo)(nil)
)

// Create persists a new pending Suggestion. The non-applicable payload half
// (edge fields for a concept, title for an edge) is written as NULL so the
// kind-shape CHECK constraints hold.
func (r *SuggestionRepo) Create(ctx context.Context, s *conceptgraph.Suggestion) error {
	if s == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, insertSuggestionSQL,
			s.SuggestionID, s.TenantID, s.LearnerGCID, string(s.Kind), string(s.Status),
			nullIfEmpty(s.Title), uuidArr(s.AtomRefs),
			nullIfEmpty(s.SourceConceptID), nullIfEmpty(s.TargetConceptID), nullIfEmpty(string(s.EdgeClass)),
			nullIfEmpty(s.Rationale), nullIfEmpty(s.ModelID), nullIfEmpty(s.RunID),
			nullIfEmpty(s.SourceCompanionID), nullIfEmpty(s.SourceEventID),
			nullIfEmpty(s.FocalConceptID),
			s.CreatedAt, s.UpdatedAt, s.DecidedAt, s.DeletedAt,
		)
		return err
	})
}

// Update persists the curation-decision fields (status, decided_at, deleted_at,
// updated_at) scoped to (id, tenant, learner). The immutable payload is never
// rewritten.
func (r *SuggestionRepo) Update(ctx context.Context, s *conceptgraph.Suggestion) error {
	if s == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, updateSuggestionSQL,
			s.SuggestionID, s.TenantID, s.LearnerGCID,
			string(s.Status), s.UpdatedAt, s.DecidedAt, s.DeletedAt,
		)
		return err
	})
}

// CreateBatch persists all suggestions in ONE transaction. Used by the
// emitted-event subscriber: a partial failure rolls back the whole batch so
// redelivery re-ingests cleanly (the durable inbox commits its key only on full
// success). Empty/nil slice is a no-op.
func (r *SuggestionRepo) CreateBatch(ctx context.Context, ss []*conceptgraph.Suggestion) error {
	if len(ss) == 0 {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		for _, s := range ss {
			if s == nil {
				continue
			}
			if _, err := q.Exec(ctx, insertSuggestionSQL,
				s.SuggestionID, s.TenantID, s.LearnerGCID, string(s.Kind), string(s.Status),
				nullIfEmpty(s.Title), uuidArr(s.AtomRefs),
				nullIfEmpty(s.SourceConceptID), nullIfEmpty(s.TargetConceptID), nullIfEmpty(string(s.EdgeClass)),
				nullIfEmpty(s.Rationale), nullIfEmpty(s.ModelID), nullIfEmpty(s.RunID),
				nullIfEmpty(s.SourceCompanionID), nullIfEmpty(s.SourceEventID),
				nullIfEmpty(s.FocalConceptID),
				s.CreatedAt, s.UpdatedAt, s.DecidedAt, s.DeletedAt,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

// ApplyAccept atomically materialises an accepted suggestion (ADR-212 WS-4):
// within ONE tx it inserts the minted ConceptNode and/or Edge (reusing the WS-1
// concept_nodes / concept_edges SQL) and flips the suggestion to accepted. A
// concept accept passes BOTH (node + the focal→node link edge); an edge accept
// passes the edge alone; each may be nil and is inserted only when present.
// Mirrors ConceptReRootRepo.Apply's one-tx-many-statements contract (all in
// chora_consumption, intra-DB).
func (r *SuggestionRepo) ApplyAccept(ctx context.Context, s *conceptgraph.Suggestion, node *conceptgraph.ConceptNode, edge *conceptgraph.Edge) error {
	if s == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if node != nil {
			if _, err := q.Exec(ctx, insertConceptNodeSQL, conceptNodeInsertArgs(node)...); err != nil {
				return err
			}
		}
		if edge != nil {
			if _, err := q.Exec(ctx, insertConceptEdgeSQL,
				edge.EdgeID, edge.TenantID, edge.LearnerGCID, edge.SourceConceptID, edge.TargetConceptID,
				string(edge.Class), string(edge.Provenance), edge.CreatedAt, edge.UpdatedAt,
			); err != nil {
				return err
			}
		}
		_, err := q.Exec(ctx, updateSuggestionSQL,
			s.SuggestionID, s.TenantID, s.LearnerGCID,
			string(s.Status), s.UpdatedAt, s.DecidedAt, s.DeletedAt,
		)
		return err
	})
}

// GetByID returns one live Suggestion for the learner, or (nil, nil) when none
// matches.
func (r *SuggestionRepo) GetByID(ctx context.Context, tenantID, learnerGCID, suggestionID string) (*conceptgraph.Suggestion, error) {
	return r.getOne(ctx, getSuggestionByIDSQL, suggestionID, tenantID, learnerGCID)
}

// GetBySourceEvent returns the live Suggestion ingested from the given emitted
// event (idempotency probe), or (nil, nil) when none matches.
func (r *SuggestionRepo) GetBySourceEvent(ctx context.Context, tenantID, learnerGCID, sourceEventID string) (*conceptgraph.Suggestion, error) {
	return r.getOne(ctx, getSuggestionBySourceEventSQL, sourceEventID, tenantID, learnerGCID)
}

func (r *SuggestionRepo) getOne(ctx context.Context, sql, key, tenantID, learnerGCID string) (*conceptgraph.Suggestion, error) {
	var out *conceptgraph.Suggestion
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, sql, key, tenantID, learnerGCID)
		if row == nil {
			return nil // stub Querier — not found
		}
		s, err := scanSuggestion(row)
		if err != nil {
			if err == ErrNoRows {
				return nil
			}
			return err
		}
		out = s
		return nil
	})
	return out, err
}

// ListPending returns the learner's undecided suggestions (curation inbox),
// newest first.
func (r *SuggestionRepo) ListPending(ctx context.Context, tenantID, learnerGCID string) ([]*conceptgraph.Suggestion, error) {
	out := make([]*conceptgraph.Suggestion, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listPendingSuggestionsSQL, tenantID, learnerGCID)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil // stub Querier — empty
		}
		defer rows.Close()
		for rows.Next() {
			s, err := scanSuggestion(rows)
			if err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
}

// ListPendingForFocal returns the learner's undecided suggestions SCOPED to the
// given focal concept: rows whose focal_concept_id equals focalConceptID OR are
// whole-map (NULL). A stale prior-generate batch (a different focal) is thus
// excluded from this node, while whole-map suggestions still surface. Newest
// first. focalConceptID must be a non-empty UUID (the handler routes an empty
// focal to ListPending).
func (r *SuggestionRepo) ListPendingForFocal(ctx context.Context, tenantID, learnerGCID, focalConceptID string) ([]*conceptgraph.Suggestion, error) {
	// Resilience (2026-07-22 live 500): a focal that is not a canonical UUID can
	// equal no stored focal_concept_id (a uuid column), so it resolves to the SAME
	// rows a valid-but-absent focal does: whole-map (NULL-focal) suggestions only.
	// Binding it into `focal_concept_id = $3` would coerce it to uuid and raise
	// 22P02, 500-ing the whole Suggestions tab, so route a non-UUID focal to the
	// NULL-only query. A canonical UUID keeps the exact (= $3 OR IS NULL) behaviour.
	sql := listPendingForFocalSuggestionsSQL
	args := []any{tenantID, learnerGCID, focalConceptID}
	if !conceptgraph.IsUUIDShaped(focalConceptID) {
		sql = listPendingWholeMapSuggestionsSQL
		args = []any{tenantID, learnerGCID}
	}
	out := make([]*conceptgraph.Suggestion, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil // stub Querier — empty
		}
		defer rows.Close()
		for rows.Next() {
			s, err := scanSuggestion(rows)
			if err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
}

// scanSuggestion maps one row (the shared SELECT column order) into a domain
// Suggestion. Satisfied by both Row and Rows. Nullable columns scan into
// *string / *time.Time locals; NULL → the zero value.
func scanSuggestion(s interface{ Scan(dest ...any) error }) (*conceptgraph.Suggestion, error) {
	var (
		sug         conceptgraph.Suggestion
		kind        string
		status      string
		title       *string
		source      *string
		target      *string
		edgeClass   *string
		rationale   *string
		modelID     *string
		runID       *string
		companionID *string
		eventID     *string
		focal       *string
		decidedAt   *time.Time
		deletedAt   *time.Time
	)
	if err := s.Scan(
		&sug.SuggestionID, &sug.TenantID, &sug.LearnerGCID, &kind, &status,
		&title, &sug.AtomRefs, &source, &target, &edgeClass,
		&rationale, &modelID, &runID, &companionID, &eventID,
		&focal,
		&sug.CreatedAt, &sug.UpdatedAt, &decidedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	sug.Kind = conceptgraph.SuggestionKind(kind)
	sug.Status = conceptgraph.SuggestionStatus(status)
	sug.Title = derefStr(title)
	sug.SourceConceptID = derefStr(source)
	sug.TargetConceptID = derefStr(target)
	sug.EdgeClass = conceptgraph.EdgeClass(derefStr(edgeClass))
	sug.Rationale = derefStr(rationale)
	sug.ModelID = derefStr(modelID)
	sug.RunID = derefStr(runID)
	sug.SourceCompanionID = derefStr(companionID)
	sug.SourceEventID = derefStr(eventID)
	sug.FocalConceptID = derefStr(focal)
	sug.DecidedAt = decidedAt
	sug.DeletedAt = deletedAt
	return &sug, nil
}

// nullIfEmpty returns a SQL NULL for an empty string, else the string — so the
// non-applicable payload half (per kind) and unset stamps persist as NULL.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// uuidArr coalesces a nil slice to a non-nil empty slice so the ::uuid[] bind
// yields '{}' (not NULL) for the NOT NULL atom_refs column — edge suggestions
// carry no atoms and a nil here would violate the not-null constraint (23502).
func uuidArr(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// derefStr maps a nullable text column (*string, nil on NULL) to a domain
// string (zero value on NULL).
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// --- SQL templates (review-via-test). atom_refs: bind []string → ::uuid[]; read
// back ::text[]. Nullable uuid columns cast ::text on read so pgx scans into
// *string. ---

const (
	suggestionColumns = `id, tenant_id, learner_gcid, kind, status,
       title, atom_refs::text[], source_concept_id::text, target_concept_id::text, edge_class,
       rationale, model_id, run_id, source_companion_id::text, source_event_id::text,
       focal_concept_id::text,
       created_at, updated_at, decided_at, deleted_at`

	insertSuggestionSQL = `
INSERT INTO concept_suggestions
       (id, tenant_id, learner_gcid, kind, status,
        title, atom_refs, source_concept_id, target_concept_id, edge_class,
        rationale, model_id, run_id, source_companion_id, source_event_id,
        focal_concept_id,
        created_at, updated_at, decided_at, deleted_at)
VALUES ($1, $2, $3, $4, $5,
        $6, $7::uuid[], $8, $9, $10,
        $11, $12, $13, $14, $15,
        $16,
        $17, $18, $19, $20)`

	updateSuggestionSQL = `
UPDATE concept_suggestions SET
       status     = $4,
       updated_at = $5,
       decided_at = $6,
       deleted_at = $7
 WHERE id = $1 AND tenant_id = $2 AND learner_gcid = $3`

	getSuggestionByIDSQL = `
SELECT ` + suggestionColumns + `
  FROM concept_suggestions
 WHERE id = $1 AND tenant_id = $2 AND learner_gcid = $3 AND deleted_at IS NULL`

	getSuggestionBySourceEventSQL = `
SELECT ` + suggestionColumns + `
  FROM concept_suggestions
 WHERE source_event_id = $1 AND tenant_id = $2 AND learner_gcid = $3 AND deleted_at IS NULL`

	listPendingSuggestionsSQL = `
SELECT ` + suggestionColumns + `
  FROM concept_suggestions
 WHERE tenant_id = $1 AND learner_gcid = $2 AND status = 'pending' AND deleted_at IS NULL
 ORDER BY created_at DESC`

	// Focal-scoped inbox: match the requested focal OR whole-map (NULL) rows, so
	// a stale prior-generate batch (a different focal) does not leak onto this
	// node while whole-map suggestions still show. $3 is the focal (untyped param
	// → Postgres infers ::uuid from the column comparison, as source_event_id does).
	listPendingForFocalSuggestionsSQL = `
SELECT ` + suggestionColumns + `
  FROM concept_suggestions
 WHERE tenant_id = $1 AND learner_gcid = $2 AND status = 'pending' AND deleted_at IS NULL
   AND (focal_concept_id = $3 OR focal_concept_id IS NULL)
 ORDER BY created_at DESC`

	// Whole-map-only inbox: pending rows with NO focal (NULL). Served when the
	// requested focal is not a canonical UUID. Such a focal can equal no stored
	// focal_concept_id (a uuid column), so it degrades to the SAME rows a valid-
	// but-absent focal yields, WITHOUT coercing untrusted input through a uuid cast
	// (the 22P02 that 500'd the Suggestions tab on 2026-07-22). Two params only.
	listPendingWholeMapSuggestionsSQL = `
SELECT ` + suggestionColumns + `
  FROM concept_suggestions
 WHERE tenant_id = $1 AND learner_gcid = $2 AND status = 'pending' AND deleted_at IS NULL
   AND focal_concept_id IS NULL
 ORDER BY created_at DESC`
)
