// learner_profile_preferences_test.go — TDD for the WS1 preferences leg
// (ADR-200 deferred event `consumption.preferences.updated` → FactPreference).
//
// Owner rulings 2026-07-07 baked in:
//
//	Q2 stable `dose.map.<map_id>` key · Q3 a cleared preference projects an
//	`included` fact (NEVER hard-delete) · Q4 preference changes are SUPPRESSED
//	from the RecentActivity log — so NewPreferenceFacts returns facts ONLY,
//	there is no activity entry.
package learner_profile

import (
	"testing"
	"time"
)

func basePrefUpdate() NewPreferenceUpdateInput {
	return NewPreferenceUpdateInput{
		TenantID:      "11111111-1111-1111-1111-111111111111",
		LearnerGCID:   "22222222-2222-2222-2222-222222222222",
		SourceEventID: "55555555-5555-5555-5555-555555555555",
		Prefs: []PreferenceKV{
			{Key: "dose.map.aaaa1111-1111-1111-1111-111111111111", Value: "excluded"},
			{Key: "dose.map.bbbb2222-2222-2222-2222-222222222222", Value: "included"},
		},
		OccurredAt: time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC),
		Now:        time.Date(2026, 7, 7, 9, 5, 0, 0, time.UTC),
	}
}

// One preferences.updated event fans out to one verified FactPreference per key.
// (Q4: NO activity entry — facts only.)
func TestNewPreferenceFacts_FansOutOnePerKey(t *testing.T) {
	facts, err := NewPreferenceFacts(basePrefUpdate())
	if err != nil {
		t.Fatalf("NewPreferenceFacts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("want 2 preference facts (one per key), got %d", len(facts))
	}
	for _, f := range facts {
		if f.Type != FactPreference {
			t.Errorf("type = %q; want preference", f.Type)
		}
		if f.SourceEventID != "55555555-5555-5555-5555-555555555555" {
			t.Errorf("every fact must cite the verified source event; got %q", f.SourceEventID)
		}
		if f.ID == "" {
			t.Error("expected a generated id per fact")
		}
		if f.TenantID != "11111111-1111-1111-1111-111111111111" || f.LearnerGCID != "22222222-2222-2222-2222-222222222222" {
			t.Errorf("scope wrong: tenant=%q learner=%q", f.TenantID, f.LearnerGCID)
		}
		if !f.OccurredAt.Equal(time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC)) {
			t.Errorf("occurred_at = %v; want the event instant", f.OccurredAt)
		}
		if !f.RecordedAt.Equal(time.Date(2026, 7, 7, 9, 5, 0, 0, time.UTC)) {
			t.Errorf("recorded_at should equal injected Now; got %v", f.RecordedAt)
		}
	}
	// Q2: RefID = the stable dose.map.<id> key, Detail.Label = value.
	if facts[0].RefID != "dose.map.aaaa1111-1111-1111-1111-111111111111" || facts[0].Detail.Label != "excluded" {
		t.Errorf("fact[0] key/value wrong: ref=%q label=%q", facts[0].RefID, facts[0].Detail.Label)
	}
}

// Q3: a cleared preference (re-include a previously-muted map) projects an
// `included` fact — a normal verified fact, NEVER a hard delete / soft-delete.
func TestNewPreferenceFacts_ClearedProjectsIncludedFact(t *testing.T) {
	in := basePrefUpdate()
	in.Prefs = []PreferenceKV{{Key: "dose.map.cccc3333-3333-3333-3333-333333333333", Value: "included"}}
	facts, err := NewPreferenceFacts(in)
	if err != nil {
		t.Fatalf("NewPreferenceFacts: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("want 1 fact, got %d", len(facts))
	}
	if facts[0].Detail.Label != "included" {
		t.Errorf("cleared pref must project an included fact; got %q", facts[0].Detail.Label)
	}
	if facts[0].DeletedAt != nil {
		t.Error("a cleared preference must NOT be soft-deleted — it is an included fact")
	}
}

// Verified-only invariant (ADR-203 / L16): no projection without the source
// event id — no self-declaration path.
func TestNewPreferenceFacts_VerifiedOnly(t *testing.T) {
	in := basePrefUpdate()
	in.SourceEventID = "   "
	if _, err := NewPreferenceFacts(in); err == nil {
		t.Fatal("expected error: preference facts must cite a verified source_event_id")
	}
}

// An empty preferences set has nothing to project → error (never a silent no-op).
func TestNewPreferenceFacts_EmptySetRejected(t *testing.T) {
	in := basePrefUpdate()
	in.Prefs = nil
	if _, err := NewPreferenceFacts(in); err == nil {
		t.Fatal("expected error for an empty preferences set")
	}
}

func TestNewPreferenceFacts_Validation(t *testing.T) {
	cases := map[string]func(*NewPreferenceUpdateInput){
		"no tenant":   func(in *NewPreferenceUpdateInput) { in.TenantID = " " },
		"no learner":  func(in *NewPreferenceUpdateInput) { in.LearnerGCID = "" },
		"no source":   func(in *NewPreferenceUpdateInput) { in.SourceEventID = "" },
		"zero occ":    func(in *NewPreferenceUpdateInput) { in.OccurredAt = time.Time{} },
		"blank key":   func(in *NewPreferenceUpdateInput) { in.Prefs = []PreferenceKV{{Key: "  ", Value: "excluded"}} },
		"blank value": func(in *NewPreferenceUpdateInput) { in.Prefs = []PreferenceKV{{Key: "dose.map.x", Value: ""}} },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			in := basePrefUpdate()
			mut(&in)
			if _, err := NewPreferenceFacts(in); err == nil {
				t.Fatalf("expected error for %q", name)
			}
		})
	}
}

// The produced facts must surface through BuildView under Preferences[key]=value
// — the leg reaches the agent-facing read model with no view change.
func TestNewPreferenceFacts_ReachesAgentView(t *testing.T) {
	facts, err := NewPreferenceFacts(basePrefUpdate())
	if err != nil {
		t.Fatalf("NewPreferenceFacts: %v", err)
	}
	view := BuildView("22222222-2222-2222-2222-222222222222", facts, nil)
	if got := view.Preferences["dose.map.aaaa1111-1111-1111-1111-111111111111"]; got != "excluded" {
		t.Errorf("agent view Preferences[key] = %q; want excluded", got)
	}
	if len(view.Preferences) != 2 {
		t.Errorf("want 2 projected preferences in the view, got %d", len(view.Preferences))
	}
}
