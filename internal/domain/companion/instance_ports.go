// instance_ports.go — repository port + service-level invariants for the
// multi-Companion (1:N) aggregate.
//
// Per .claude/skills/hexagonal/SKILL.md: domain owns the port (interface);
// adapters implement it. Adapters live in
// services/chora-consumption/internal/adapter/{inmem,repo/pg}/.
//
// Roster cap (max_companions_per_user) and tenant_entitlements lookup are
// service-layer concerns, NOT entity invariants — but the port surfaces
// the cap as a parameter so the repo can enforce it atomically with the
// list-count query (avoiding TOCTOU on concurrent Create calls).
package companion

import (
	"context"
	"errors"
)

// DefaultMaxCompanionsPerUser is the free-tier roster cap fallback when
// tenant_entitlements.config_overrides.max_companions_per_user is missing.
// Per docs/architecture/multi-companion-per-user-2026-05-11.md §6 Q8 +
// ADR-142 economy doc.
const DefaultMaxCompanionsPerUser = 3

// Sentinel errors. ErrRosterCapReached + ErrInstanceNotFound are surfaced
// to handlers and mapped to HTTP 409 / 404 respectively.
var (
	ErrRosterCapReached = errors.New("companion.instance: roster cap reached")
	ErrInstanceNotFound = errors.New("companion.instance: not found")
)

// InstanceRepository is the port for multi-Companion persistence.
//
// Implementations: inmem.CompanionInstanceRepo (tests + MVP) and
// pg.CompanionInstanceRepo (production, M14.1).
//
// Cap enforcement: Create accepts `maxPerUser` and atomically checks the
// active-Instance count for the owner BEFORE inserting. Callers resolve
// `maxPerUser` from chora_tenancy via the tenant_entitlements read model
// (cross-DB forbidden — event-sourced cache).
type InstanceRepository interface {
	// Create persists a new Instance. Returns ErrRosterCapReached if the
	// owner's active-Instance count is already at maxPerUser.
	Create(ctx context.Context, inst *Instance, maxPerUser int) error

	// Get returns the Instance by CompanionID. Excludes soft-deleted.
	Get(ctx context.Context, companionID string) (*Instance, error)

	// ListByOwner returns all NON-DELETED Instances owned by (tenantID, ownerGCID).
	ListByOwner(ctx context.Context, tenantID, ownerGCID string) ([]*Instance, error)

	// ListRosterByOwner returns all NON-DELETED Instances enriched with
	// the ADR-149 growth axis snapshot + cosmetic projection per debt #44
	// / A24 / B5 (2026-05-16).
	//
	// Wire contract: a single fetch replaces the FE's prior `GET
	// /v1/me/companions` + per-Companion `GET /v1/me/companions/{id}/growth`
	// N+1 pattern. The repo's pg adapter projects all growth-axis columns
	// from `companion_instances` (added by migration 0032_familiar_growth.
	// sql) in the same SELECT — no JOIN needed since the columns are
	// additive on the same table.
	//
	// `RosterEntry.Growth` + `RosterEntry.Cosmetic` are populated for
	// every row. On Stage-0 (Egg) rows the breed/cosmetic fields are
	// empty/zero (mirroring the schema NULLs) — handlers render the
	// pre-hatch state.
	ListRosterByOwner(ctx context.Context, tenantID, ownerGCID string) ([]*RosterEntry, error)

	// Update upserts an existing Instance. Used for tier promotion, skill
	// grant persistence, configured_rules updates.
	Update(ctx context.Context, inst *Instance) error

	// SoftDelete marks the Instance deleted_at without removing the row.
	// Idempotent — re-deleting a soft-deleted row is a no-op.
	SoftDelete(ctx context.Context, companionID string) error
}
