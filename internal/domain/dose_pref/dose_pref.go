// Package dose_pref models a learner's per-map (per-KG) include/exclude
// preference for the daily dose (ADR-224 change #3 / CHO-2045).
//
// Semantics: the ABSENCE of a row means the map is INCLUDED in dose sampling
// (default-included). An explicit row carries either polarity — most rows exist
// to record an EXCLUSION. Excluding a map never stops weakness/retention
// tracking for that map; it only removes the map's atoms from the daily-dose
// candidate pool (the dose composer's pool filter consumes ExcludedMapIDs).
//
// Hexagonal purity: NO infra imports, NO time.Now() leaks — the constructor
// takes a `now` (with a defensive zero-fallback, mirroring learner_weakness.New).
// Per .claude/rules/ddd-enforcement.md §4 closure is soft-delete (the table
// carries a `deleted_at` column read-filtered `WHERE deleted_at IS NULL`, never
// a hard delete); §5 new rows mint UUIDv7; tenant_id + learner_gcid + map_id are
// opaque UUIDs held WITHOUT FK constraints (cross-domain refs validate via
// events, never a JOIN). Mirrors internal/domain/learner_weakness.
package dose_pref

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// DoseKGPref is a learner's include/exclude preference for one map/KG in the
// daily dose (ADR-224 change #3). Default (no row) = included.
type DoseKGPref struct {
	ID          string
	TenantID    string
	LearnerGCID string
	MapID       string
	Included    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Repository is the per-KG dose-preference port (RLS tenant+gcid scoped).
type Repository interface {
	// List returns every explicit preference row for a learner.
	List(ctx context.Context, tenantID, gcid string) ([]DoseKGPref, error)
	// ExcludedMapIDs returns the set of map_ids the learner excluded (included=false).
	// This is what the dose composer's pool filter consumes.
	ExcludedMapIDs(ctx context.Context, tenantID, gcid string) (map[string]bool, error)
	// Set upserts one preference (include/exclude a map), minting UUIDv7 on insert
	// and stamping now. Returns the resulting row.
	Set(ctx context.Context, tenantID, gcid, mapID string, included bool, now time.Time) (DoseKGPref, error)
}

// ErrInvalid is the sentinel returned by New for any validation failure.
var ErrInvalid = errors.New("dose_pref: invalid")

// New constructs a fresh preference row. It validates the three required
// opaque-UUID scope fields (tenant_id, learner_gcid, map_id) non-empty after
// trimming, mints a UUIDv7 id, and stamps created_at + updated_at from now
// (defaulting a zero now to time.Now().UTC()). `included` is stored verbatim.
func New(tenantID, gcid, mapID string, included bool, now time.Time) (*DoseKGPref, error) {
	tenantID = strings.TrimSpace(tenantID)
	gcid = strings.TrimSpace(gcid)
	mapID = strings.TrimSpace(mapID)
	if tenantID == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalid)
	}
	if gcid == "" {
		return nil, fmt.Errorf("%w: learner_gcid required", ErrInvalid)
	}
	if mapID == "" {
		return nil, fmt.Errorf("%w: map_id required", ErrInvalid)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return &DoseKGPref{
		ID:          domain.NewUUIDv7(),
		TenantID:    tenantID,
		LearnerGCID: gcid,
		MapID:       mapID,
		Included:    included,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}
