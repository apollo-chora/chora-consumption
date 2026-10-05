package companion_test

// summoner_test.go — CHO-2013 P1 (R3-1): the companion-side effects of a Goal
// attach ("Summon-onto-Goal"). Binding IS the theme commitment:
//
//   - specialization derives from the Goal's theme (root-concept title) via
//     ChangeSpecialization — first real emitter of
//     chora.consumption.companion.specialization_changed.v1
//   - the awakening resonant pick is CLEARED on rebind (re-picked in the
//     new Goal's subgraph)
//   - same-theme re-attach is a no-op (idempotent PATCH retries)
//   - egg-born 'general' stubs heal at their first bind

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

type summonInstanceStore struct {
	instances map[string]*companion.Instance
	updateErr error
	updated   []*companion.Instance
}

func (s *summonInstanceStore) Get(_ context.Context, companionID string) (*companion.Instance, error) {
	inst, ok := s.instances[companionID]
	if !ok {
		return nil, companion.ErrInstanceNotFound
	}
	cp := *inst
	return &cp, nil
}

func (s *summonInstanceStore) Update(_ context.Context, inst *companion.Instance) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	s.updated = append(s.updated, inst)
	s.instances[inst.CompanionID] = inst
	return nil
}

type summonResonance struct {
	cleared []string
	err     error
}

func (s *summonResonance) ClearResonance(_ context.Context, _, companionID string) error {
	if s.err != nil {
		return s.err
	}
	s.cleared = append(s.cleared, companionID)
	return nil
}

type summonOutbox struct {
	topics   []string
	payloads []map[string]any
	envs     []companion.LoadoutEnvelope
	err      error
}

func (s *summonOutbox) PublishLoadoutEvent(_ context.Context, topic string, payload map[string]any, env companion.LoadoutEnvelope) error {
	if s.err != nil {
		return s.err
	}
	s.topics = append(s.topics, topic)
	s.payloads = append(s.payloads, payload)
	s.envs = append(s.envs, env)
	return nil
}

const (
	summonTenant = "019eb1b4-0000-7000-8000-00000000c0de"
	summonOwner  = "019eb1b4-0000-7000-8000-00000000face"
	summonFam    = "019f2620-0000-7000-8000-00000000f001"
)

func seedSummonInstance(spec string) *summonInstanceStore {
	return &summonInstanceStore{instances: map[string]*companion.Instance{
		summonFam: {
			CompanionID:    summonFam,
			TenantID:       summonTenant,
			OwnerGCID:      summonOwner,
			Name:           "Ember",
			Specialization: spec,
		},
	}}
}

func newSummoner(t *testing.T, store *summonInstanceStore, res *summonResonance, out *summonOutbox) *companion.Summoner {
	t.Helper()
	s, err := companion.NewSummoner(companion.SummonerConfig{
		Instances: store,
		Resonance: res,
		Outbox:    out,
		Clock:     func() time.Time { return time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC) },
		NewID:     func() string { return "evt-summon-1" },
	})
	if err != nil {
		t.Fatalf("NewSummoner: %v", err)
	}
	return s
}

func TestOnGoalBound_DerivesSpecializationClearsResonanceEmits(t *testing.T) {
	store := seedSummonInstance("general")
	res := &summonResonance{}
	out := &summonOutbox{}
	s := newSummoner(t, store, res, out)

	changed, err := s.OnGoalBound(context.Background(), companion.SummonInput{
		TenantID:    summonTenant,
		OwnerGCID:   summonOwner,
		CompanionID: summonFam,
		Theme:       "Astronomy Foundations",
		Traceparent: "00-abc-def-01",
	})
	if err != nil {
		t.Fatalf("OnGoalBound: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if got := store.instances[summonFam].Specialization; got != "Astronomy Foundations" {
		t.Fatalf("specialization = %q", got)
	}
	if len(res.cleared) != 1 || res.cleared[0] != summonFam {
		t.Fatalf("resonance cleared = %v", res.cleared)
	}
	if len(out.topics) != 1 || out.topics[0] != companion.TopicCompanionSpecializationChanged {
		t.Fatalf("topics = %v", out.topics)
	}
	p := out.payloads[0]
	if p["previous_specialization"] != "general" || p["new_specialization"] != "Astronomy Foundations" {
		t.Fatalf("payload spec fields: %+v", p)
	}
	if p["companion_id"] != summonFam || p["owner_gcid"] != summonOwner {
		t.Fatalf("payload identity fields: %+v", p)
	}
	if p["memory_handling"] != "carry_over" {
		t.Fatalf("memory_handling = %v (bind derive is non-destructive)", p["memory_handling"])
	}
	env := out.envs[0]
	if env.TenantID != summonTenant || env.GCID != summonOwner {
		t.Fatalf("envelope tenant/gcid: %+v", env)
	}
	if env.Traceparent != "00-abc-def-01" {
		t.Fatalf("envelope traceparent: %q", env.Traceparent)
	}
	if env.IdempotencyKey == "" || env.EventID == "" {
		t.Fatalf("envelope idempotency/event id empty: %+v", env)
	}
}

func TestOnGoalBound_SameThemeIsNoOp(t *testing.T) {
	store := seedSummonInstance("Astronomy Foundations")
	res := &summonResonance{}
	out := &summonOutbox{}
	s := newSummoner(t, store, res, out)

	changed, err := s.OnGoalBound(context.Background(), companion.SummonInput{
		TenantID:    summonTenant,
		OwnerGCID:   summonOwner,
		CompanionID: summonFam,
		Theme:       "Astronomy Foundations",
	})
	if err != nil {
		t.Fatalf("OnGoalBound: %v", err)
	}
	if changed {
		t.Fatal("changed = true, want false (idempotent re-attach)")
	}
	if len(store.updated) != 0 || len(res.cleared) != 0 || len(out.topics) != 0 {
		t.Fatalf("no-op path had side effects: updated=%d cleared=%d emitted=%d",
			len(store.updated), len(res.cleared), len(out.topics))
	}
}

func TestOnGoalBound_ForeignOwnerHidden(t *testing.T) {
	store := seedSummonInstance("general")
	s := newSummoner(t, store, &summonResonance{}, &summonOutbox{})

	_, err := s.OnGoalBound(context.Background(), companion.SummonInput{
		TenantID:    summonTenant,
		OwnerGCID:   "019eb1b4-0000-7000-8000-000000000bad",
		CompanionID: summonFam,
		Theme:       "Astronomy Foundations",
	})
	if !errors.Is(err, companion.ErrInstanceNotFound) {
		t.Fatalf("want ErrInstanceNotFound, got %v", err)
	}
}

func TestOnGoalBound_BlankThemeRejected(t *testing.T) {
	store := seedSummonInstance("general")
	s := newSummoner(t, store, &summonResonance{}, &summonOutbox{})

	_, err := s.OnGoalBound(context.Background(), companion.SummonInput{
		TenantID:    summonTenant,
		OwnerGCID:   summonOwner,
		CompanionID: summonFam,
		Theme:       "   ",
	})
	if !errors.Is(err, companion.ErrSpecializationBad) {
		t.Fatalf("want ErrSpecializationBad, got %v", err)
	}
}

func TestOnGoalBound_UpdateFailurePropagates(t *testing.T) {
	store := seedSummonInstance("general")
	store.updateErr = errors.New("pg down")
	s := newSummoner(t, store, &summonResonance{}, &summonOutbox{})

	_, err := s.OnGoalBound(context.Background(), companion.SummonInput{
		TenantID:    summonTenant,
		OwnerGCID:   summonOwner,
		CompanionID: summonFam,
		Theme:       "Astronomy Foundations",
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestValidateOwned_ReturnsInstanceForOwner(t *testing.T) {
	store := seedSummonInstance("general")
	s := newSummoner(t, store, &summonResonance{}, &summonOutbox{})

	inst, err := s.ValidateOwned(context.Background(), summonTenant, summonOwner, summonFam)
	if err != nil {
		t.Fatalf("ValidateOwned: %v", err)
	}
	if inst == nil || inst.CompanionID != summonFam {
		t.Fatalf("instance = %+v", inst)
	}
	if _, err := s.ValidateOwned(context.Background(), summonTenant, "gcid-other", summonFam); !errors.Is(err, companion.ErrInstanceNotFound) {
		t.Fatalf("cross-owner want ErrInstanceNotFound, got %v", err)
	}
}
