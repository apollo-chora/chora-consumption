// suspension_test.go: the ADR-252 / ADR-254 D11 advisory suspension model.
// Pure, table-driven: the decision function and the event validation carry the
// whole semantics, so the pg and in-memory projections stay thin.
package companion

import (
	"errors"
	"testing"
	"time"
)

const (
	suspTenantA = "11111111-1111-7111-8111-000000000001"
	suspTenantB = "11111111-1111-7111-8111-000000000002"
	suspActor   = "01970000-0000-7000-9000-000000000001"
	suspChatKey = "companion_chat_turn_basic"
	suspCiteKey = "companion_skill_cite_atom"
)

var suspT0 = time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)

func engagedRow(id string, scope SuspensionScope, tenant, skill, reason string, at time.Time) SuspensionRecord {
	return SuspensionRecord{
		SuspensionID:  id,
		Scope:         scope,
		TenantID:      tenant,
		SkillKey:      skill,
		Engaged:       true,
		Reason:        reason,
		ActorGCID:     suspActor,
		SourceVersion: 1,
		ChangedAt:     at,
	}
}

func TestSuspensionScope_Valid(t *testing.T) {
	if !SuspensionScopePlatform.Valid() || !SuspensionScopeTenant.Valid() {
		t.Fatal("platform and tenant must be valid scopes")
	}
	if SuspensionScope("").Valid() || SuspensionScope("global").Valid() {
		t.Fatal("unknown scopes must be invalid")
	}
}

func TestErrCodeCompanionSuspended(t *testing.T) {
	if ErrCodeCompanionSuspended != "COMPANION_SUSPENDED" {
		t.Fatalf("ErrCodeCompanionSuspended = %q", ErrCodeCompanionSuspended)
	}
}

func TestDecideSuspension(t *testing.T) {
	platformAll := engagedRow("p-all", SuspensionScopePlatform, "", "", "platform incident", suspT0)
	platformChat := engagedRow("p-chat", SuspensionScopePlatform, "", suspChatKey, "chat model regression", suspT0.Add(time.Minute))
	tenantAAll := engagedRow("a-all", SuspensionScopeTenant, suspTenantA, "", "tenant A review", suspT0.Add(2*time.Minute))
	tenantACite := engagedRow("a-cite", SuspensionScopeTenant, suspTenantA, suspCiteKey, "tenant A cite audit", suspT0.Add(3*time.Minute))
	tenantBAll := engagedRow("b-all", SuspensionScopeTenant, suspTenantB, "", "tenant B review", suspT0.Add(4*time.Minute))
	released := engagedRow("rel", SuspensionScopePlatform, "", "", "released one", suspT0.Add(5*time.Minute))
	released.Engaged = false
	released.SourceVersion = 2

	cases := []struct {
		name       string
		rows       []SuspensionRecord
		tenantID   string
		actionCode string
		want       SuspensionStatus
	}{
		{
			name: "no rows = not paused",
			rows: nil, tenantID: suspTenantA, actionCode: suspChatKey,
			want: SuspensionStatus{},
		},
		{
			name: "released rows never pause",
			rows: []SuspensionRecord{released}, tenantID: suspTenantA, actionCode: suspChatKey,
			want: SuspensionStatus{},
		},
		{
			name: "platform all-skills pauses every tenant and every action",
			rows: []SuspensionRecord{platformAll}, tenantID: suspTenantB, actionCode: suspCiteKey,
			want: SuspensionStatus{Paused: true, Reason: "platform incident", Scope: SuspensionScopePlatform},
		},
		{
			name: "platform all-skills pauses the whole-companion view (empty action code)",
			rows: []SuspensionRecord{platformAll}, tenantID: suspTenantA, actionCode: "",
			want: SuspensionStatus{Paused: true, Reason: "platform incident", Scope: SuspensionScopePlatform},
		},
		{
			name: "platform skill row pauses only that action",
			rows: []SuspensionRecord{platformChat}, tenantID: suspTenantA, actionCode: suspChatKey,
			want: SuspensionStatus{Paused: true, Reason: "chat model regression", Scope: SuspensionScopePlatform},
		},
		{
			name: "platform skill row does not pause another action",
			rows: []SuspensionRecord{platformChat}, tenantID: suspTenantA, actionCode: suspCiteKey,
			want: SuspensionStatus{},
		},
		{
			name: "platform skill row does not pause the whole-companion view",
			rows: []SuspensionRecord{platformChat}, tenantID: suspTenantA, actionCode: "",
			want: SuspensionStatus{},
		},
		{
			name: "tenant all-skills pauses its own tenant",
			rows: []SuspensionRecord{tenantAAll}, tenantID: suspTenantA, actionCode: suspChatKey,
			want: SuspensionStatus{Paused: true, Reason: "tenant A review", Scope: SuspensionScopeTenant},
		},
		{
			name: "tenant all-skills does not leak to another tenant",
			rows: []SuspensionRecord{tenantAAll}, tenantID: suspTenantB, actionCode: suspChatKey,
			want: SuspensionStatus{},
		},
		{
			name: "tenant row never matches an empty tenant id",
			rows: []SuspensionRecord{tenantAAll}, tenantID: "", actionCode: suspChatKey,
			want: SuspensionStatus{},
		},
		{
			name: "tenant skill row pauses only that action in that tenant",
			rows: []SuspensionRecord{tenantACite}, tenantID: suspTenantA, actionCode: suspCiteKey,
			want: SuspensionStatus{Paused: true, Reason: "tenant A cite audit", Scope: SuspensionScopeTenant},
		},
		{
			name: "tenant skill row does not pause another action",
			rows: []SuspensionRecord{tenantACite}, tenantID: suspTenantA, actionCode: suspChatKey,
			want: SuspensionStatus{},
		},
		{
			name: "platform outranks tenant when both cover the turn",
			rows: []SuspensionRecord{tenantAAll, platformAll}, tenantID: suspTenantA, actionCode: suspChatKey,
			want: SuspensionStatus{Paused: true, Reason: "platform incident", Scope: SuspensionScopePlatform},
		},
		{
			name: "all-skills outranks a skill row in the same scope",
			rows: []SuspensionRecord{platformChat, platformAll}, tenantID: suspTenantA, actionCode: suspChatKey,
			want: SuspensionStatus{Paused: true, Reason: "platform incident", Scope: SuspensionScopePlatform},
		},
		{
			name: "a tenant row still pauses when the only platform row names another skill",
			rows: []SuspensionRecord{platformChat, tenantAAll}, tenantID: suspTenantA, actionCode: suspCiteKey,
			want: SuspensionStatus{Paused: true, Reason: "tenant A review", Scope: SuspensionScopeTenant},
		},
		{
			name: "other tenants' rows are ignored even when mixed in",
			rows: []SuspensionRecord{tenantBAll, tenantACite}, tenantID: suspTenantA, actionCode: suspChatKey,
			want: SuspensionStatus{},
		},
		{
			name:     "a record with an unknown scope never covers anything",
			rows:     []SuspensionRecord{engagedRow("weird", SuspensionScope("global"), "", "", "bad scope", suspT0)},
			tenantID: suspTenantA, actionCode: suspChatKey,
			want: SuspensionStatus{},
		},
		{
			name: "newest wins among equal-rank rows",
			rows: []SuspensionRecord{
				engagedRow("a-all-old", SuspensionScopeTenant, suspTenantA, "", "older reason", suspT0),
				engagedRow("a-all-new", SuspensionScopeTenant, suspTenantA, "", "newer reason", suspT0.Add(time.Hour)),
			},
			tenantID: suspTenantA, actionCode: suspChatKey,
			want: SuspensionStatus{Paused: true, Reason: "newer reason", Scope: SuspensionScopeTenant},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideSuspension(tc.rows, tc.tenantID, tc.actionCode)
			if got != tc.want {
				t.Fatalf("DecideSuspension() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func validSuspensionChanged() SuspensionChanged {
	return SuspensionChanged{
		EventID:      "01990000-0000-7000-8000-000000000001",
		SuspensionID: "01990000-0000-7000-8000-000000000010",
		Scope:        SuspensionScopeTenant,
		TenantID:     suspTenantA,
		SkillKey:     suspChatKey,
		Engaged:      true,
		Version:      1,
		Reason:       "incident 42",
		ActorGCID:    suspActor,
		ChangedAt:    suspT0,
	}
}

func TestSuspensionChanged_Validate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*SuspensionChanged)
		wantErr error
	}{
		{"valid tenant engage", func(*SuspensionChanged) {}, nil},
		{"valid platform release", func(e *SuspensionChanged) {
			e.Scope = SuspensionScopePlatform
			e.TenantID = ""
			e.SkillKey = ""
			e.Engaged = false
			e.Version = 2
		}, nil},
		{"missing event id", func(e *SuspensionChanged) { e.EventID = "" }, ErrSuspensionEventIDRequired},
		{"missing suspension id", func(e *SuspensionChanged) { e.SuspensionID = "  " }, ErrSuspensionIDRequired},
		{"unknown scope", func(e *SuspensionChanged) { e.Scope = "global" }, ErrSuspensionInvalidScope},
		{"tenant scope needs a tenant", func(e *SuspensionChanged) { e.TenantID = "" }, ErrSuspensionTenantRequired},
		{"platform scope carries no tenant", func(e *SuspensionChanged) {
			e.Scope = SuspensionScopePlatform
		}, ErrSuspensionTenantOnPlatform},
		{"reason required", func(e *SuspensionChanged) { e.Reason = "" }, ErrSuspensionReasonRequired},
		{"actor required", func(e *SuspensionChanged) { e.ActorGCID = "" }, ErrSuspensionActorRequired},
		{"version must be >= 1", func(e *SuspensionChanged) { e.Version = 0 }, ErrSuspensionVersionInvalid},
		{"changed_at required", func(e *SuspensionChanged) { e.ChangedAt = time.Time{} }, ErrSuspensionChangedAtRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := validSuspensionChanged()
			tc.mutate(&ev)
			err := ev.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestSuspensionChanged_Record(t *testing.T) {
	ev := validSuspensionChanged()
	rec := ev.Record()
	want := SuspensionRecord{
		SuspensionID:  ev.SuspensionID,
		Scope:         ev.Scope,
		TenantID:      ev.TenantID,
		SkillKey:      ev.SkillKey,
		Engaged:       true,
		Reason:        ev.Reason,
		ActorGCID:     ev.ActorGCID,
		SourceVersion: 1,
		ChangedAt:     suspT0,
		LastEventID:   ev.EventID,
	}
	if rec != want {
		t.Fatalf("Record() = %+v, want %+v", rec, want)
	}
}

// TestSuspensionRecord_Supersedes pins the monotonic guard both projections
// share: strictly newer version wins; an equal version only yields to a later
// changed_at; anything else is stale and must be ignored.
func TestSuspensionRecord_Supersedes(t *testing.T) {
	base := validSuspensionChanged().Record()
	cases := []struct {
		name     string
		existing SuspensionRecord
		incoming SuspensionRecord
		want     bool
	}{
		{"newer version supersedes", base, func() SuspensionRecord { r := base; r.SourceVersion = 2; return r }(), true},
		{"same version same time is a duplicate", base, base, false},
		{"same version later changed_at supersedes", base, func() SuspensionRecord {
			r := base
			r.ChangedAt = base.ChangedAt.Add(time.Second)
			return r
		}(), true},
		{"older version is stale", func() SuspensionRecord { r := base; r.SourceVersion = 2; return r }(), base, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.incoming.Supersedes(tc.existing); got != tc.want {
				t.Fatalf("Supersedes() = %v, want %v", got, tc.want)
			}
		})
	}
}
