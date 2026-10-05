// learner_profile_preferences.go — the WS1 preferences leg fan-out constructor
// (ADR-200 deferred `consumption.preferences.updated` → FactPreference; CHO-2049).
//
// One verified preferences.updated event carries N changed key/value pairs; each
// becomes one verified FactPreference (RefID = the stable preference key,
// Detail.Label = the value). This keeps the "one event → N facts" rule in the
// DOMAIN (hexagonal) rather than looping in the adapter, and reuses the existing
// Fact shape / table / BuildView mapping — no schema change.
//
// Owner rulings 2026-07-07:
//
//	Q2 the key is the stable `dose.map.<map_id>` (opaque, no staleness).
//	Q3 a cleared preference (re-include a muted map) is projected as an
//	   `included` fact — a normal verified fact, NEVER a hard/soft delete.
//	Q4 a preference change is NOT logged to the activity/decision feed, so this
//	   constructor returns facts ONLY (no ActivityEntry) — it must not crowd the
//	   Companion's conversational RecentActivity with config changes.
package learner_profile

import (
	"fmt"
	"strings"
	"time"
)

// PreferenceKV is one changed preference: a stable key (Q2: `dose.map.<map_id>`)
// and its new value (e.g. "included" | "excluded"). Both are opaque to the
// projection — a later identity-sourced preference (locale/notifications) reuses
// the same shape without any consumer change.
type PreferenceKV struct {
	Key   string
	Value string
}

// NewPreferenceUpdateInput is the constructor input for NewPreferenceFacts — the
// distilled, verified `consumption.preferences.updated` event.
type NewPreferenceUpdateInput struct {
	TenantID      string
	LearnerGCID   string
	SourceEventID string // the verified event_id — REQUIRED (anti-gaming anchor)
	Prefs         []PreferenceKV
	OccurredAt    time.Time
	Now           time.Time
}

// NewPreferenceFacts turns one verified preferences.updated event into one
// verified FactPreference per changed key. It enforces the verified-only
// invariant (a source_event_id is REQUIRED) and rejects an empty change set —
// there is no self-declaration path and no silent no-op. Per Q4 it returns NO
// activity entry (preference changes are suppressed from RecentActivity).
func NewPreferenceFacts(in NewPreferenceUpdateInput) ([]*Fact, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalid)
	}
	if strings.TrimSpace(in.LearnerGCID) == "" {
		return nil, fmt.Errorf("%w: learner_gcid required", ErrInvalid)
	}
	if strings.TrimSpace(in.SourceEventID) == "" {
		return nil, fmt.Errorf("%w: source_event_id required (verified-only: a preference fact must cite a Chora system event, never self-declaration)", ErrInvalid)
	}
	if in.OccurredAt.IsZero() {
		return nil, fmt.Errorf("%w: occurred_at required", ErrInvalid)
	}
	if len(in.Prefs) == 0 {
		return nil, fmt.Errorf("%w: at least one preference required (an empty update projects nothing)", ErrInvalid)
	}

	facts := make([]*Fact, 0, len(in.Prefs))
	for _, kv := range in.Prefs {
		if strings.TrimSpace(kv.Key) == "" {
			return nil, fmt.Errorf("%w: preference key required", ErrInvalid)
		}
		if strings.TrimSpace(kv.Value) == "" {
			return nil, fmt.Errorf("%w: preference value required (key %q)", ErrInvalid, kv.Key)
		}
		f, err := New(NewFactInput{
			TenantID:      in.TenantID,
			LearnerGCID:   in.LearnerGCID,
			Type:          FactPreference,
			RefID:         kv.Key,
			Detail:        Detail{Label: kv.Value},
			SourceEventID: in.SourceEventID,
			OccurredAt:    in.OccurredAt,
			Now:           in.Now,
		})
		if err != nil {
			return nil, err // New already wraps ErrInvalid (e.g. blank key/value)
		}
		facts = append(facts, f)
	}
	return facts, nil
}
