// Package learner_profile is the per-GCID global LearnerProfile read-model in
// chora-consumption (ADR-200, WS1 of the Companion Holistic Redesign).
//
// It is a PROJECTION, never a write-model: chora-consumption never owns courses,
// certifications, assessments or identity preferences — those live in other
// domain databases and cross-DB queries are FORBIDDEN (ddd-enforcement #1). The
// profile is fed ONLY by verified Pub/Sub events (delivery.course.completed,
// delivery.certification.issued, {delivery|creation}.assessment.graded,
// consumption.path.completed, identity.preferences.updated) and is injected to
// the Companion agent as session-state so coaching is grounded in what the learner
// has actually, verifiably done.
//
// VERIFIED-ONLY INVARIANT (ADR-203 / L16, anti-gaming): a Fact or ActivityEntry
// can NEVER be constructed without a `source_event_id` — every row must cite the
// Chora system event that proves it. Self-declaration is not a source. This same
// stream is, by construction, the un-gameable companion-EXP source and the
// CompanionGoal achievement feed (one stream, three jobs).
//
// HEXAGONAL purity: stdlib-only, NO infra imports, NO time.Now() leaks — every
// clock-dependent call takes a `Now` (mirrors internal/domain/learner_weakness +
// topic_retention). Per ddd-enforcement: closure is soft-delete (#5, never hard
// delete); new rows use UUIDv7 (#7); gcid + cross-domain ref ids are opaque UUIDs
// without FK constraints (#3).
package learner_profile

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// MaxRecentActivity bounds how many activity-log lines the projected view
// carries into the agent session-state (keeps the prompt budget sane).
const MaxRecentActivity = 20

// FactType enumerates the verified learner facts the profile projects. Each
// value corresponds to exactly one verified source event class.
type FactType string

const (
	FactEnrollment          FactType = "enrollment"           // delivery.enrollment.created.v1
	FactCourseCompleted     FactType = "course_completed"     // delivery.course.completed.v1
	FactCertificationIssued FactType = "certification_issued" // delivery.certification.issued.v1
	FactAssessmentGraded    FactType = "assessment_graded"    // {delivery|creation}.assessment.graded.v1
	FactPathCompleted       FactType = "path_completed"       // consumption.path.completed.v1
	FactPreference          FactType = "preference"           // identity.preferences.updated.v1
)

// Valid reports whether t is a recognised fact type.
func (t FactType) Valid() bool {
	switch t {
	case FactEnrollment, FactCourseCompleted, FactCertificationIssued,
		FactAssessmentGraded, FactPathCompleted, FactPreference:
		return true
	}
	return false
}

// Detail is the distilled, persisted metadata for a Fact (JSONB on the wire).
// Fields are optional and fact-type-specific: assessments carry Score/Passed/
// HintCount (HintCount=0 enables the ADR-203 first-attempt-mastery bonus);
// preferences put the value in Label (or Extra); everything carries a human Label.
type Detail struct {
	Label     string            `json:"label,omitempty"`
	Score     *float64          `json:"score,omitempty"`
	Passed    *bool             `json:"passed,omitempty"`
	HintCount *int              `json:"hint_count,omitempty"`
	Extra     map[string]string `json:"extra,omitempty"`
}

// Fact is one verified, projected learner fact. Its natural identity is
// (tenant, learner, type, ref) — a learner completing the same course is one
// fact; each assessment attempt is its own fact (ref = the attempt id).
type Fact struct {
	ID            string
	TenantID      string
	LearnerGCID   string
	Type          FactType
	RefID         string // cross-domain UUID (course/cert/assessment/path/pref key); no FK
	Detail        Detail
	SourceEventID string // the verified event_id — REQUIRED (anti-gaming anchor + idempotency)
	OccurredAt    time.Time
	RecordedAt    time.Time
	UpdatedAt     time.Time
	DeletedAt     *time.Time
}

// ErrInvalid is the sentinel returned by the constructors for any validation
// failure.
var ErrInvalid = errors.New("learner_profile: invalid")

// NewFactInput is the constructor input for New.
type NewFactInput struct {
	TenantID      string
	LearnerGCID   string
	Type          FactType
	RefID         string
	Detail        Detail
	SourceEventID string
	OccurredAt    time.Time
	Now           time.Time
}

// New constructs a verified profile fact. It enforces the verified-only
// invariant: a fact MUST cite a source_event_id (the Chora system event that
// proves it) and the time it occurred — there is no self-declaration path.
func New(in NewFactInput) (*Fact, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalid)
	}
	if strings.TrimSpace(in.LearnerGCID) == "" {
		return nil, fmt.Errorf("%w: learner_gcid required", ErrInvalid)
	}
	if !in.Type.Valid() {
		return nil, fmt.Errorf("%w: unknown fact type %q", ErrInvalid, in.Type)
	}
	if strings.TrimSpace(in.RefID) == "" {
		return nil, fmt.Errorf("%w: ref_id required", ErrInvalid)
	}
	if strings.TrimSpace(in.SourceEventID) == "" {
		return nil, fmt.Errorf("%w: source_event_id required (verified-only: a profile/EXP fact must cite a Chora system event, never self-declaration)", ErrInvalid)
	}
	if in.OccurredAt.IsZero() {
		return nil, fmt.Errorf("%w: occurred_at required", ErrInvalid)
	}

	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	return &Fact{
		ID:            domain.NewUUIDv7(),
		TenantID:      strings.TrimSpace(in.TenantID),
		LearnerGCID:   strings.TrimSpace(in.LearnerGCID),
		Type:          in.Type,
		RefID:         strings.TrimSpace(in.RefID),
		Detail:        in.Detail,
		SourceEventID: strings.TrimSpace(in.SourceEventID),
		OccurredAt:    in.OccurredAt.UTC(),
		RecordedAt:    now,
		UpdatedAt:     now,
	}, nil
}

// NaturalKey is the idempotent-upsert identity: (tenant, learner, type, ref).
func (f *Fact) NaturalKey() string {
	return f.TenantID + "|" + f.LearnerGCID + "|" + string(f.Type) + "|" + f.RefID
}

// SoftDelete dismisses a fact without removing it (hard delete forbidden,
// ddd-enforcement #5).
func (f *Fact) SoftDelete(now time.Time) {
	f.DeletedAt = &now
	f.UpdatedAt = now
}

// ActivityEntry is one append-only line of the per-learner activity/decision log
// ("completed X", "scored Y", "skipped Z") the Companion can recall conversationally.
type ActivityEntry struct {
	ID            string
	TenantID      string
	LearnerGCID   string
	Kind          string // e.g. completed_course / earned_certification / scored_assessment
	Summary       string
	RefID         string
	SourceEventID string // verified-only anchor
	OccurredAt    time.Time
	RecordedAt    time.Time
}

// NewActivityInput is the constructor input for NewActivity.
type NewActivityInput struct {
	TenantID      string
	LearnerGCID   string
	Kind          string
	Summary       string
	RefID         string
	SourceEventID string
	OccurredAt    time.Time
	Now           time.Time
}

// NewActivity constructs a verified activity-log entry (same verified-only
// invariant as New).
func NewActivity(in NewActivityInput) (*ActivityEntry, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalid)
	}
	if strings.TrimSpace(in.LearnerGCID) == "" {
		return nil, fmt.Errorf("%w: learner_gcid required", ErrInvalid)
	}
	if strings.TrimSpace(in.Kind) == "" {
		return nil, fmt.Errorf("%w: kind required", ErrInvalid)
	}
	if strings.TrimSpace(in.Summary) == "" {
		return nil, fmt.Errorf("%w: summary required", ErrInvalid)
	}
	if strings.TrimSpace(in.SourceEventID) == "" {
		return nil, fmt.Errorf("%w: source_event_id required (verified-only)", ErrInvalid)
	}
	if in.OccurredAt.IsZero() {
		return nil, fmt.Errorf("%w: occurred_at required", ErrInvalid)
	}

	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	return &ActivityEntry{
		ID:            domain.NewUUIDv7(),
		TenantID:      strings.TrimSpace(in.TenantID),
		LearnerGCID:   strings.TrimSpace(in.LearnerGCID),
		Kind:          strings.TrimSpace(in.Kind),
		Summary:       strings.TrimSpace(in.Summary),
		RefID:         strings.TrimSpace(in.RefID),
		SourceEventID: strings.TrimSpace(in.SourceEventID),
		OccurredAt:    in.OccurredAt.UTC(),
		RecordedAt:    now,
	}, nil
}

// --- Projected view (the agent session-state payload) ---

// FactRef is a lightweight projected reference to a completed/issued/enrolled item.
type FactRef struct {
	RefID      string
	Label      string
	OccurredAt time.Time
}

// AssessmentResult carries the graded outcome (Score/Passed/HintCount drive
// ADR-203 assessment-tier EXP + first-attempt-mastery).
type AssessmentResult struct {
	RefID      string
	Label      string
	Score      *float64
	Passed     *bool
	HintCount  *int
	OccurredAt time.Time
}

// ActivityLine is one recent activity-log line for conversational recall.
type ActivityLine struct {
	Kind       string
	Summary    string
	OccurredAt time.Time
}

// ProfileView is the assembled, agent-facing read model injected as
// sessionState["learner_profile"].
type ProfileView struct {
	LearnerGCID      string
	Enrollments      []FactRef
	CompletedCourses []FactRef
	CompletedPaths   []FactRef
	Certifications   []FactRef
	Assessments      []AssessmentResult
	Preferences      map[string]string
	RecentActivity   []ActivityLine
}

// BuildView assembles the projected view from live facts + the activity log.
// Soft-deleted facts are excluded; recent activity is sorted newest-first and
// capped at MaxRecentActivity.
func BuildView(learnerGCID string, facts []*Fact, activity []*ActivityEntry) ProfileView {
	v := ProfileView{LearnerGCID: learnerGCID, Preferences: map[string]string{}}

	for _, f := range facts {
		if f == nil || f.DeletedAt != nil {
			continue
		}
		switch f.Type {
		case FactEnrollment:
			v.Enrollments = append(v.Enrollments, FactRef{f.RefID, f.Detail.Label, f.OccurredAt})
		case FactCourseCompleted:
			v.CompletedCourses = append(v.CompletedCourses, FactRef{f.RefID, f.Detail.Label, f.OccurredAt})
		case FactPathCompleted:
			v.CompletedPaths = append(v.CompletedPaths, FactRef{f.RefID, f.Detail.Label, f.OccurredAt})
		case FactCertificationIssued:
			v.Certifications = append(v.Certifications, FactRef{f.RefID, f.Detail.Label, f.OccurredAt})
		case FactAssessmentGraded:
			v.Assessments = append(v.Assessments, AssessmentResult{
				RefID: f.RefID, Label: f.Detail.Label,
				Score: f.Detail.Score, Passed: f.Detail.Passed, HintCount: f.Detail.HintCount,
				OccurredAt: f.OccurredAt,
			})
		case FactPreference:
			val := f.Detail.Label
			if val == "" && f.Detail.Extra != nil {
				val = f.Detail.Extra["value"]
			}
			v.Preferences[f.RefID] = val
		}
	}

	sorted := make([]*ActivityEntry, 0, len(activity))
	for _, a := range activity {
		if a != nil {
			sorted = append(sorted, a)
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].OccurredAt.After(sorted[j].OccurredAt) })
	for i, a := range sorted {
		if i >= MaxRecentActivity {
			break
		}
		v.RecentActivity = append(v.RecentActivity, ActivityLine{a.Kind, a.Summary, a.OccurredAt})
	}
	return v
}
