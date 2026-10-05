// companion_config_server_test.go — unit tests for ResolveCompanionConfig.
//
// Covers the transport surface I own: nil-reader fail-loud, request
// validation, RLS/ownership guards (NotFound), and the configured_rules /
// skill / kg-neighbor / learner-persona mapping helpers. The happy-path
// instance+growth → proto field copy needs a live growth.Service (no in-mem
// growth repo exists) and is exercised by the sub-phase-5 deploy e2e.
package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"
)

// fakeInstanceReader is a test double for CompanionInstanceReader.
type fakeInstanceReader struct {
	inst *companion.Instance
	err  error
}

func (f fakeInstanceReader) Get(_ context.Context, _ string) (*companion.Instance, error) {
	return f.inst, f.err
}

func codeOf(t *testing.T, err error) codes.Code {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	return st.Code()
}

func TestResolveCompanionConfig_NilReaderUnimplemented(t *testing.T) {
	s := NewCompanionGrowthServer(nil) // no reader option → fail-loud
	_, err := s.ResolveCompanionConfig(context.Background(), &consumptionv1.ResolveCompanionConfigRequest{
		TenantId: "t", CompanionId: "f", CallerGcid: "g",
	})
	if got := codeOf(t, err); got != codes.Unimplemented {
		t.Fatalf("nil reader: want Unimplemented, got %s", got)
	}
}

func TestResolveCompanionConfig_MissingArgs(t *testing.T) {
	s := NewCompanionGrowthServer(nil, WithCompanionInstanceReader(fakeInstanceReader{}))
	cases := []*consumptionv1.ResolveCompanionConfigRequest{
		{CompanionId: "f", CallerGcid: "g"}, // no tenant
		{TenantId: "t", CallerGcid: "g"},    // no companion
		{TenantId: "t", CompanionId: "f"},   // no caller
		{},                                  // all empty
	}
	for i, req := range cases {
		if _, err := s.ResolveCompanionConfig(context.Background(), req); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("case %d: want InvalidArgument", i)
		}
	}
}

func TestResolveCompanionConfig_InstanceNotFound(t *testing.T) {
	s := NewCompanionGrowthServer(nil, WithCompanionInstanceReader(
		fakeInstanceReader{err: companion.ErrInstanceNotFound}))
	_, err := s.ResolveCompanionConfig(context.Background(), &consumptionv1.ResolveCompanionConfigRequest{
		TenantId: "t", CompanionId: "f", CallerGcid: "g",
	})
	if got := codeOf(t, err); got != codes.NotFound {
		t.Fatalf("missing instance: want NotFound, got %s", got)
	}
}

func TestResolveCompanionConfig_OwnershipMismatch(t *testing.T) {
	// Reader returns a valid instance owned by someone else → NotFound BEFORE
	// the growth call (so a nil svc is never dereferenced).
	s := NewCompanionGrowthServer(nil, WithCompanionInstanceReader(fakeInstanceReader{
		inst: &companion.Instance{CompanionID: "f", TenantID: "t", OwnerGCID: "someone-else"},
	}))
	_, err := s.ResolveCompanionConfig(context.Background(), &consumptionv1.ResolveCompanionConfigRequest{
		TenantId: "t", CompanionId: "f", CallerGcid: "g",
	})
	if got := codeOf(t, err); got != codes.NotFound {
		t.Fatalf("ownership mismatch: want NotFound, got %s", got)
	}
}

func TestMapConfiguredRules_DefaultsAndAliases(t *testing.T) {
	d := mapConfiguredRules(nil) // empty → safe defaults
	if d.Tone != "encouraging" || d.HintProgression != "ladder" || d.DifficultyCap != "intermediate" ||
		d.Language != "en" || d.CitationStrictness != "strict" || d.MaxHintsBeforeReveal != 3 {
		t.Errorf("defaults wrong: %+v", d)
	}
	// max_hint_count alias + explicit values honored.
	r := mapConfiguredRules(map[string]string{
		"tone": "socratic", "max_hint_count": "1", "difficulty_cap": "advanced",
		"language": "ms", "citation_strictness": "lenient", "hint_progression": "uniform",
	})
	if r.Tone != "socratic" || r.MaxHintsBeforeReveal != 1 || r.DifficultyCap != "advanced" ||
		r.Language != "ms" || r.CitationStrictness != "lenient" || r.HintProgression != "uniform" {
		t.Errorf("alias mapping wrong: %+v", r)
	}
}

func TestMapConfiguredRules_PersonaKnobs(t *testing.T) {
	// CHO-2015 (ADR-219 D2): address_style + comma-joined interest_chips.
	d := mapConfiguredRules(nil)
	if d.AddressStyle != "first_name" {
		t.Errorf("default address_style = %q; want first_name", d.AddressStyle)
	}
	if len(d.InterestChips) != 0 {
		t.Errorf("default interest_chips = %v; want empty (non-nil)", d.InterestChips)
	}
	r := mapConfiguredRules(map[string]string{
		"address_style":  "nickname",
		"interest_chips": "rockets, black holes , ,dinosaurs",
	})
	if r.AddressStyle != "nickname" {
		t.Errorf("address_style = %q; want nickname", r.AddressStyle)
	}
	if len(r.InterestChips) != 3 || r.InterestChips[0] != "rockets" || r.InterestChips[2] != "dinosaurs" {
		t.Errorf("interest_chips parse wrong (trim + drop-empty): %v", r.InterestChips)
	}
}

func TestLearnerPersonaOrDefault(t *testing.T) {
	if learnerPersonaOrDefault(nil) != "curious-explorer" {
		t.Error("nil → curious-explorer")
	}
	if learnerPersonaOrDefault(map[string]string{"learner_persona": "cert-focused"}) != "cert-focused" {
		t.Error("explicit persona not honored")
	}
}
