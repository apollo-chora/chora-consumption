// ports.go — persistence port for the campaign question-set aggregate.
package campaignquestion

import (
	"context"
	"time"
)

// Repository persists QuestionSets (table campaign_question_sets, mig 0079).
// All reads/writes are tenant-scoped (RLS) + learner-scoped by explicit
// predicate; soft-deleted rows read as absent.
type Repository interface {
	// GetByConceptRung returns the live set for one (learner, concept, rung),
	// or (nil, nil) when none exists (the caller mints lazily).
	GetByConceptRung(ctx context.Context, tenantID, learnerGCID, conceptID string, rung int) (*QuestionSet, error)

	// GetByAssistID resolves the set a qgen terminal event keys on, or
	// (nil, nil) when the assist id is not ours (SHARED topic — ack-skip).
	// Tenant-scoped only (the terminal envelope carries tenant).
	GetByAssistID(ctx context.Context, tenantID, assistID string) (*QuestionSet, error)

	// CountRequestedOn counts the learner's SAME-ORIGIN generation requests
	// stamped on one UTC day across ALL their nodes (the per-origin D13 cap:
	// the march feeder and the explicit tap draw on independent budgets).
	CountRequestedOn(ctx context.Context, tenantID, learnerGCID string, origin RequestOrigin, day time.Time) (int, error)

	// Save upserts on the live (tenant, learner, concept, rung) identity.
	Save(ctx context.Context, s *QuestionSet) error
}
