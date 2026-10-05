// ritual_repository.go — the driven port for CompanionRitual aggregate
// persistence (CHO-2016 G3; hexagonal — the domain owns the interface, the pg
// adapter satisfies it). The run ledger is a SEPARATE port (RunRepository,
// ritual_ports.go). Default reads filter deleted_at IS NULL.
package companion

import "context"

// RitualRepository persists the CompanionRitual designer aggregate + its
// append-only revisions. Reads/writes are tenant-scoped (RLS defence-in-depth
// on top of the app-side tenant_id predicate). GetByID returns (nil, nil) when
// nothing live matches.
type RitualRepository interface {
	// Create inserts a fresh DRAFT ritual (no revisions, disabled).
	Create(ctx context.Context, r *Ritual) error

	// GetByID loads a non-deleted ritual WITH its ordered revisions
	// (CurrentRevision resolved). Returns (nil, nil) when absent/deleted.
	GetByID(ctx context.Context, tenantID, ritualID string) (*Ritual, error)

	// ListByCompanion returns the companion's non-deleted rituals (with
	// revisions), newest first.
	ListByCompanion(ctx context.Context, tenantID, companionID string) ([]*Ritual, error)

	// AppendRevision atomically inserts one append-only revision AND updates
	// the parent ritual's current_revision + published_price_units + updated_at
	// (the Publish persistence). Idempotent under replay via the
	// UNIQUE(ritual_id, revision_no) arbiter.
	AppendRevision(ctx context.Context, tenantID, ritualID string, rev RitualRevision, publishedPriceUnits int) error

	// SetEnabled persists Enable/Disable.
	SetEnabled(ctx context.Context, tenantID, ritualID string, enabled bool) error

	// CountEnabledByCompanion counts the companion's currently-enabled,
	// non-deleted rituals EXCLUDING excludeRitualID (the OtherEnabledCount fed
	// into RitualCapabilityContext for the enabled-quota check). Pass "" to
	// exclude nothing.
	CountEnabledByCompanion(ctx context.Context, tenantID, companionID, excludeRitualID string) (int, error)

	// SoftDelete marks the ritual deleted_at (never hard-delete). Idempotent.
	SoftDelete(ctx context.Context, tenantID, ritualID string) error
}
