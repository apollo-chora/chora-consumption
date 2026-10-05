// companion_suspension_projection_test.go: the in-memory ADR-254 D11
// projection must carry the same apply + status semantics as the pg adapter
// (monotonic version guard, stale = not applied, release clears).
package inmem

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

const (
	suspTenantA = "11111111-1111-7111-8111-000000000001"
	suspTenantB = "11111111-1111-7111-8111-000000000002"
	suspActor   = "01970000-0000-7000-9000-000000000001"
	suspChatKey = "companion_chat_turn_basic"
)

var suspT0 = time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)

func suspEvent(eventID, suspensionID string, scope companion.SuspensionScope, tenant, skill string, engaged bool, version int64, at time.Time) companion.SuspensionChanged {
	return companion.SuspensionChanged{
		EventID:      eventID,
		SuspensionID: suspensionID,
		Scope:        scope,
		TenantID:     tenant,
		SkillKey:     skill,
		Engaged:      engaged,
		Version:      version,
		Reason:       "reason for " + eventID,
		ActorGCID:    suspActor,
		ChangedAt:    at,
	}
}

func TestCompanionSuspensionProjection_ImplementsPort(t *testing.T) {
	var _ companion.SuspensionProjection = NewCompanionSuspensionProjection()
}

func TestCompanionSuspensionProjection_EngageThenStatus(t *testing.T) {
	p := NewCompanionSuspensionProjection()
	ctx := context.Background()

	applied, err := p.Apply(ctx, suspEvent("e1", "s1", companion.SuspensionScopeTenant, suspTenantA, "", true, 1, suspT0))
	if err != nil || !applied {
		t.Fatalf("Apply = (%v, %v), want (true, nil)", applied, err)
	}

	st, err := p.Status(ctx, suspTenantA, suspChatKey)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Paused || st.Scope != companion.SuspensionScopeTenant || st.Reason != "reason for e1" {
		t.Fatalf("Status = %+v, want paused tenant-scope", st)
	}

	other, err := p.Status(ctx, suspTenantB, suspChatKey)
	if err != nil {
		t.Fatalf("Status other tenant: %v", err)
	}
	if other.Paused {
		t.Fatalf("tenant B must not be paused by tenant A's suspension: %+v", other)
	}
}

func TestCompanionSuspensionProjection_ReleaseClears(t *testing.T) {
	p := NewCompanionSuspensionProjection()
	ctx := context.Background()

	if _, err := p.Apply(ctx, suspEvent("e1", "s1", companion.SuspensionScopePlatform, "", "", true, 1, suspT0)); err != nil {
		t.Fatal(err)
	}
	applied, err := p.Apply(ctx, suspEvent("e2", "s1", companion.SuspensionScopePlatform, "", "", false, 2, suspT0.Add(time.Minute)))
	if err != nil || !applied {
		t.Fatalf("release Apply = (%v, %v), want (true, nil)", applied, err)
	}
	st, err := p.Status(ctx, suspTenantA, suspChatKey)
	if err != nil {
		t.Fatal(err)
	}
	if st.Paused {
		t.Fatalf("released suspension must not pause: %+v", st)
	}
}

func TestCompanionSuspensionProjection_StaleAndDuplicateAreNotApplied(t *testing.T) {
	p := NewCompanionSuspensionProjection()
	ctx := context.Background()

	// Release (v2) arrives BEFORE the engage (v1): out-of-order redelivery.
	applied, err := p.Apply(ctx, suspEvent("e2", "s1", companion.SuspensionScopePlatform, "", "", false, 2, suspT0.Add(time.Minute)))
	if err != nil || !applied {
		t.Fatalf("first Apply = (%v, %v), want (true, nil)", applied, err)
	}
	applied, err = p.Apply(ctx, suspEvent("e1", "s1", companion.SuspensionScopePlatform, "", "", true, 1, suspT0))
	if err != nil || applied {
		t.Fatalf("stale engage Apply = (%v, %v), want (false, nil)", applied, err)
	}
	// Duplicate of the release.
	applied, err = p.Apply(ctx, suspEvent("e2", "s1", companion.SuspensionScopePlatform, "", "", false, 2, suspT0.Add(time.Minute)))
	if err != nil || applied {
		t.Fatalf("duplicate Apply = (%v, %v), want (false, nil)", applied, err)
	}
	st, err := p.Status(ctx, suspTenantA, suspChatKey)
	if err != nil {
		t.Fatal(err)
	}
	if st.Paused {
		t.Fatalf("a late engage must never resurrect a lifted suspension: %+v", st)
	}
	recs := p.Records()
	if len(recs) != 1 || recs[0].SourceVersion != 2 || recs[0].Engaged || recs[0].LastEventID != "e2" {
		t.Fatalf("Records() = %+v, want one released v2 row from e2", recs)
	}
}

func TestCompanionSuspensionProjection_RejectsInvalidEvent(t *testing.T) {
	p := NewCompanionSuspensionProjection()
	bad := suspEvent("e1", "s1", companion.SuspensionScopeTenant, "", "", true, 1, suspT0) // tenant scope without tenant
	applied, err := p.Apply(context.Background(), bad)
	if applied || !errors.Is(err, companion.ErrSuspensionTenantRequired) {
		t.Fatalf("Apply(bad) = (%v, %v), want (false, ErrSuspensionTenantRequired)", applied, err)
	}
}

func TestCompanionSuspensionProjection_SkillRowOnlyPausesThatAction(t *testing.T) {
	p := NewCompanionSuspensionProjection()
	ctx := context.Background()
	if _, err := p.Apply(ctx, suspEvent("e1", "s1", companion.SuspensionScopePlatform, "", suspChatKey, true, 1, suspT0)); err != nil {
		t.Fatal(err)
	}
	chat, _ := p.Status(ctx, suspTenantA, suspChatKey)
	cite, _ := p.Status(ctx, suspTenantA, "companion_skill_cite_atom")
	whole, _ := p.Status(ctx, suspTenantA, "")
	if !chat.Paused || cite.Paused || whole.Paused {
		t.Fatalf("chat=%+v cite=%+v whole=%+v; want only chat paused", chat, cite, whole)
	}
}
