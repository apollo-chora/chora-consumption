// learner_profile_preferences_subscriber.go — the WS1 preferences leg handler
// (ADR-200 deferred `consumption.preferences.updated` → FactPreference; CHO-2049).
//
// One verified consumption.preferences.updated event → N FactPreference upserts
// (one per changed key), inside the idempotency Store's claim-run-persist so the
// dedup key commits only after every write succeeds (a transient repo error is
// re-run by Pub/Sub redelivery, never silently dropped). Per owner ruling Q4 a
// preference change is NOT written to the activity log — it must not crowd the
// Companion's conversational RecentActivity with config changes. The verified-only
// invariant holds by construction: NewPreferenceFacts stamps env.EventID as the
// source on every fact.
package subscribers

import (
	"context"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
)

// PreferencesUpdatedPayload mirrors chora.consumption.preferences.updated.v1
// (built by the push handler from the decoded event). Prefs is an ORDERED list of
// changed (key, value) pairs; for dose preferences the key is the stable
// `dose.map.<map_id>` (Q2) and the value is "included" | "excluded" (Q3: a
// re-include is an `included` value, never a delete). The payload is
// source-agnostic — a later identity.preferences.updated maps to the same shape.
type PreferencesUpdatedPayload struct {
	LearnerGCID string
	Prefs       []learner_profile.PreferenceKV
	OccurredAt  time.Time
}

// HandlePreferencesUpdated projects the verified preferences event into N
// FactPreference rows (no activity line, per Q4).
func (s *LearnerProfileSubscriber) HandlePreferencesUpdated(env events.Envelope, p PreferencesUpdatedPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	occurred := p.OccurredAt
	if occurred.IsZero() {
		occurred = env.OccurredAt
	}
	// RLS: the pg ProjectionRepo reads the tenant from the CONTEXT (SET LOCAL
	// chora.tenant_id), not an explicit arg — stamp the envelope tenant so the
	// projection lands under the right tenant (mirrors project()).
	ctx := tracing.WithTenantID(context.Background(), env.TenantID)

	return s.processOnce(env.EventID, func() error {
		facts, err := learner_profile.NewPreferenceFacts(learner_profile.NewPreferenceUpdateInput{
			TenantID:      env.TenantID,
			LearnerGCID:   p.LearnerGCID,
			SourceEventID: env.EventID,
			Prefs:         p.Prefs,
			OccurredAt:    occurred,
		})
		if err != nil {
			return fmt.Errorf("learner_profile project preferences: %w", err)
		}
		for _, f := range facts {
			if err := s.repo.UpsertFact(ctx, f); err != nil {
				return fmt.Errorf("learner_profile upsert preference %q: %w", f.RefID, err)
			}
		}
		return nil
	})
}
