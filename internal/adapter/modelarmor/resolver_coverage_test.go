// resolver_coverage_test.go — residual branch coverage for the resolver +
// adapter (pure functions; no real Cloud Model Armor backend). Extends the
// canonical adapter_test.go suite without weakening any of its assertions.
package modelarmor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cgcmodelarmor "github.com/apollo-chora/chora-common/modelarmor"
)

// ---------- LoadResolverFromFile ----------

func TestLoadResolverFromFile_ReadsAndParses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-guardrail-mapping.yaml")
	if err := os.WriteFile(path, []byte(minimalYAML), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	r, err := LoadResolverFromFile(path, testProject, testLocation, testEnv)
	if err != nil {
		t.Fatalf("LoadResolverFromFile: %v", err)
	}
	got, err := r.Resolve(AgentIDCompanionPersona)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := "chora-guardrail-strict-dev"; got != want {
		t.Errorf("Resolve = %q, want %q", got, want)
	}
}

func TestLoadResolverFromFile_ReadErrorFailsLoud(t *testing.T) {
	if _, err := LoadResolverFromFile(filepath.Join(t.TempDir(), "missing.yaml"), testProject, testLocation, testEnv); err == nil {
		t.Fatal("LoadResolverFromFile(missing file) must fail loud")
	}
}

// ---------- LoadResolverFromBytes error arms ----------

func TestLoadResolverFromBytes_RequiresEnvInputs(t *testing.T) {
	for name, mutate := range map[string]func(p, l, e string) (string, string, string){
		"project":     func(_, l, e string) (string, string, string) { return "  ", l, e },
		"location":    func(p, _, e string) (string, string, string) { return p, " ", e },
		"environment": func(p, l, _ string) (string, string, string) { return p, l, "" },
	} {
		p, l, e := mutate(testProject, testLocation, testEnv)
		if _, err := LoadResolverFromBytes([]byte(minimalYAML), p, l, e); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestLoadResolverFromBytes_MalformedYAMLFailsLoud(t *testing.T) {
	if _, err := LoadResolverFromBytes([]byte("agents: [not: [closed"), testProject, testLocation, testEnv); err == nil {
		t.Fatal("malformed YAML must fail loud")
	} else if !strings.Contains(err.Error(), "yaml") {
		t.Errorf("err = %v, want yaml parse error", err)
	}
}

func TestLoadResolverFromBytes_UnknownDefaultTierFailsLoud(t *testing.T) {
	bad := `
version: 1
agents:
  foo:
    template_tier: strict
default:
  template_tier: bogus_default
`
	if _, err := LoadResolverFromBytes([]byte(bad), testProject, testLocation, testEnv); err == nil {
		t.Fatal("unknown default tier must fail loud")
	}
}

func TestLoadResolverFromBytes_MissingDefaultFallsBackToStrict(t *testing.T) {
	noDefault := `
version: 1
agents:
  foo:
    template_tier: strict
`
	r, err := LoadResolverFromBytes([]byte(noDefault), testProject, testLocation, testEnv)
	if err != nil {
		t.Fatalf("LoadResolverFromBytes: %v", err)
	}
	got, err := r.Resolve("unknown_agent")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := "chora-guardrail-strict-" + testEnv; !strings.Contains(got, want) {
		t.Errorf("default-less fallback = %q, want to contain %q", got, want)
	}
}

// ---------- Resolve error arms ----------

func TestResolve_RejectsBlankAgentID(t *testing.T) {
	r := mustResolver(t)
	if _, err := r.Resolve("   "); err == nil {
		t.Fatal("Resolve(blank agent_id) must error")
	}
}

func TestResolve_NilReceiverErrors(t *testing.T) {
	var r *TemplateResolver
	if _, err := r.Resolve(AgentIDCompanionPersona); err == nil {
		t.Fatal("Resolve on nil receiver must error")
	}
}

// ---------- PermissiveTemplates ----------

func TestPermissiveTemplates_DefaultTierPermissiveTail(t *testing.T) {
	// Default tier permissive with NO permissive agent forces the
	// default-tier add() tail to append a genuinely new entry (the only
	// reachable path where that tail appends — permissive agents + a
	// permissive default collapse to the same template name and dedupe).
	yaml := `
version: 1
agents:
  companion_persona:
    template_tier: strict
default:
  template_tier: permissive
`
	r, err := LoadResolverFromBytes([]byte(yaml), testProject, testLocation, testEnv)
	if err != nil {
		t.Fatalf("LoadResolverFromBytes: %v", err)
	}
	got := r.PermissiveTemplates()
	want := "chora-guardrail-permissive-dev"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("PermissiveTemplates() = %v, want [%q] (permissive default tail)", got, want)
	}
}

func TestPermissiveTemplates_DedupesRepeatedPermissiveNames(t *testing.T) {
	// Two permissive agents + a permissive default all map to the same
	// template resource name — the dedupe collapses them to one entry.
	yaml := `
version: 1
agents:
  saga_a:
    template_tier: permissive
  saga_b:
    template_tier: permissive
default:
  template_tier: permissive
`
	r, err := LoadResolverFromBytes([]byte(yaml), testProject, testLocation, testEnv)
	if err != nil {
		t.Fatalf("LoadResolverFromBytes: %v", err)
	}
	if got := r.PermissiveTemplates(); len(got) != 1 {
		t.Fatalf("PermissiveTemplates() = %v, want exactly 1 deduped entry", got)
	}
}

// ---------- Screen error arm ----------

// TestAdapter_Screen_ResolverErrorFailsLoud drives the resolver-resolve error
// arm of Screen (blank agent_id → Resolve fails → Screen wraps).
func TestAdapter_Screen_ResolverErrorFailsLoud(t *testing.T) {
	t.Parallel()
	adapter, err := NewAdapter(cgcmodelarmor.NewStubScreener(), mustResolver(t))
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	_, err = adapter.Screen(context.Background(), ScreenRequest{AgentID: "  "})
	if err == nil {
		t.Fatal("Screen with unresolvable agent_id must fail loud")
	}
}

// ---------- toScreenVerdict / resultFor / buildViolations direct arms ----------

func TestBuildViolations_EmptyAndNoMatchArms(t *testing.T) {
	if got := buildViolations(cgcmodelarmor.ScreenResult{}); got != nil {
		t.Errorf("buildViolations(no filters) = %v, want nil", got)
	}
	onlyNoMatch := cgcmodelarmor.ScreenResult{
		Verdict: cgcmodelarmor.VerdictAllow,
		Filters: []cgcmodelarmor.FilterHit{{
			FilterName: cgcmodelarmor.FilterNameRAI,
			MatchState: cgcmodelarmor.MatchStateNoMatchFound,
		}},
	}
	if got := buildViolations(onlyNoMatch); got != nil {
		t.Errorf("buildViolations(all NO_MATCH_FOUND) = %v, want nil (dropped)", got)
	}
}

func TestBuildViolations_NoSubcategoryUsesFilterName(t *testing.T) {
	in := cgcmodelarmor.ScreenResult{
		Verdict: cgcmodelarmor.VerdictBlock,
		Filters: []cgcmodelarmor.FilterHit{{
			FilterName: cgcmodelarmor.FilterNamePIAndJailbreak,
			MatchState: cgcmodelarmor.MatchStateMatchFound,
		}},
	}
	got := buildViolations(in)
	if len(got) != 1 {
		t.Fatalf("buildViolations = %v, want 1", got)
	}
	if got[0].ViolationType != cgcmodelarmor.FilterNamePIAndJailbreak {
		t.Errorf("ViolationType = %q, want bare filter name %q", got[0].ViolationType, cgcmodelarmor.FilterNamePIAndJailbreak)
	}
	if got[0].Result != "block" {
		t.Errorf("Result = %q, want block", got[0].Result)
	}
}

func TestResultFor_AllVerdictArms(t *testing.T) {
	if got := resultFor(cgcmodelarmor.VerdictBlock); got != "block" {
		t.Errorf("resultFor(Block) = %q, want block", got)
	}
	if got := resultFor(cgcmodelarmor.VerdictInspectOnly); got != "flag" {
		t.Errorf("resultFor(InspectOnly) = %q, want flag", got)
	}
	if got := resultFor(cgcmodelarmor.VerdictAllow); got != "pass" {
		t.Errorf("resultFor(Allow) = %q, want pass", got)
	}
}
