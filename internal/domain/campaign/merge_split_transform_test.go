// merge_split_transform_test.go — WS-C6 (CHO-2085, ADR-227 D14) contract for
// the concept merge/split ladder transforms. Merge = survivor keeps the AND
// of cleared rungs + the weakest retention (retention lives in the sibling
// topic_retention package); split = children inherit the parent's ladder.
// These are pure projections — no events, no XP re-fire; the caller
// materialises the survivor/children copies silently.
//
// RED phase: MergeProgress / SplitProgress / PriorLadderState do not exist
// yet — this file must fail to COMPILE until the transform is written.
package campaign

import (
	"errors"
	"testing"
	"time"
)

// ladderFixture builds a NodeProgress with `rungs` cleared, a positional audit
// trail spaced one hour apart from base (honouring the campaign_node_progress
// cardinality invariant len(RungClearedAt)==RungsCleared), WonAt set iff the
// node is at the 6/6 summit, and the supplied LastAdvanceDate. CurrentRungCorrect
// is seeded non-zero so the transforms can prove they reset it.
func ladderFixture(concept string, rungs int, base time.Time, lastAdv *time.Time) *NodeProgress {
	p := &NodeProgress{
		ID:                 "pre-" + concept,
		TenantID:           cTenant,
		LearnerGCID:        cGCID,
		ConceptID:          concept,
		RungsCleared:       rungs,
		CurrentRungCorrect: 1,
		LastAdvanceDate:    lastAdv,
	}
	for i := 0; i < rungs; i++ {
		p.RungClearedAt = append(p.RungClearedAt, base.Add(time.Duration(i)*time.Hour))
	}
	if rungs == TotalRungs {
		won := base.Add(time.Duration(rungs) * time.Hour)
		p.WonAt = &won
	}
	return p
}

func dptr(t time.Time) *time.Time { return &t }

func TestMergeProgress_AndOfRungsKeepsSurvivorAudit(t *testing.T) {
	base := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	day7 := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	day8 := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)

	survivor := ladderFixture("c-surv", 4, base, dptr(day7))
	absorbed := ladderFixture("c-abs", 2, base.Add(100*time.Hour), dptr(day8))

	got, err := MergeProgress(survivor, absorbed, cTenant, cGCID, "c-surv", cNow)
	if err != nil {
		t.Fatalf("MergeProgress: %v", err)
	}
	if got == nil {
		t.Fatal("expected a merged row")
	}
	if got.RungsCleared != 2 {
		t.Errorf("RungsCleared = %d, want 2 (AND of 4 and 2)", got.RungsCleared)
	}
	if len(got.RungClearedAt) != 2 {
		t.Fatalf("audit cardinality = %d, want 2 (== RungsCleared)", len(got.RungClearedAt))
	}
	for i := 0; i < 2; i++ {
		if !got.RungClearedAt[i].Equal(survivor.RungClearedAt[i]) {
			t.Errorf("stamp[%d] = %v, want survivor's own %v", i, got.RungClearedAt[i], survivor.RungClearedAt[i])
		}
	}
	if got.WonAt != nil {
		t.Errorf("WonAt = %v, want nil (2/6 is not won)", got.WonAt)
	}
	if got.CurrentRungCorrect != 0 {
		t.Errorf("CurrentRungCorrect = %d, want 0 (fresh counter at the new frontier)", got.CurrentRungCorrect)
	}
	if got.LastAdvanceDate == nil || !got.LastAdvanceDate.Equal(day8) {
		t.Errorf("LastAdvanceDate = %v, want the later day %v", got.LastAdvanceDate, day8)
	}
	if got.TenantID != cTenant || got.LearnerGCID != cGCID || got.ConceptID != "c-surv" {
		t.Errorf("identity = (%s,%s,%s), want (%s,%s,c-surv)", got.TenantID, got.LearnerGCID, got.ConceptID, cTenant, cGCID)
	}
	if got.ID == "" || got.ID == survivor.ID {
		t.Errorf("ID = %q, want a fresh non-empty id (not the survivor's pre-merge id)", got.ID)
	}
	if !got.CreatedAt.Equal(cNow) || !got.UpdatedAt.Equal(cNow) {
		t.Errorf("timestamps = (%v,%v), want now %v", got.CreatedAt, got.UpdatedAt, cNow)
	}
	if got.DeletedAt != nil {
		t.Errorf("DeletedAt = %v, want nil (live row)", got.DeletedAt)
	}
}

func TestMergeProgress_BothWonKeepsSurvivorWonAt(t *testing.T) {
	sBase := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	aBase := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	survivor := ladderFixture("c-surv", 6, sBase, dptr(sBase))
	absorbed := ladderFixture("c-abs", 6, aBase, dptr(aBase))
	if survivor.WonAt == nil || absorbed.WonAt == nil {
		t.Fatal("fixture: both sides must be won")
	}

	got, err := MergeProgress(survivor, absorbed, cTenant, cGCID, "c-surv", cNow)
	if err != nil {
		t.Fatalf("MergeProgress: %v", err)
	}
	if got.RungsCleared != 6 {
		t.Errorf("RungsCleared = %d, want 6", got.RungsCleared)
	}
	if got.WonAt == nil {
		t.Fatal("WonAt = nil, want survivor's stamp (both sides won)")
	}
	if !got.WonAt.Equal(*survivor.WonAt) {
		t.Errorf("WonAt = %v, want survivor's own %v (not absorbed's %v)", *got.WonAt, *survivor.WonAt, *absorbed.WonAt)
	}
	if got.WonAt == survivor.WonAt {
		t.Error("WonAt aliases the survivor's pointer; want a deep copy")
	}
}

func TestMergeProgress_OneSideWonStaysUnwon(t *testing.T) {
	sBase := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	aBase := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	survivor := ladderFixture("c-surv", 6, sBase, dptr(sBase)) // won
	absorbed := ladderFixture("c-abs", 4, aBase, dptr(aBase))  // not won

	got, err := MergeProgress(survivor, absorbed, cTenant, cGCID, "c-surv", cNow)
	if err != nil {
		t.Fatalf("MergeProgress: %v", err)
	}
	if got.RungsCleared != 4 {
		t.Errorf("RungsCleared = %d, want 4 (AND of 6 and 4)", got.RungsCleared)
	}
	if got.WonAt != nil {
		t.Errorf("WonAt = %v, want nil (only one side won)", *got.WonAt)
	}
	if len(got.RungClearedAt) != 4 {
		t.Errorf("audit cardinality = %d, want 4", len(got.RungClearedAt))
	}
}

func TestMergeProgress_ExactlyOneNilCarriesPacing(t *testing.T) {
	otherBase := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	advDay := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)

	t.Run("nil survivor + absorbed 3/6", func(t *testing.T) {
		absorbed := ladderFixture("c-abs", 3, otherBase, dptr(advDay))
		got, err := MergeProgress(nil, absorbed, cTenant, cGCID, "c-surv", cNow)
		if err != nil {
			t.Fatalf("MergeProgress: %v", err)
		}
		if got == nil {
			t.Fatal("expected a fresh 0-rung row, got nil")
		}
		if got.RungsCleared != 0 || len(got.RungClearedAt) != 0 {
			t.Errorf("rungs=%d audit=%d, want 0/0 (nil side -> min 0)", got.RungsCleared, len(got.RungClearedAt))
		}
		if got.WonAt != nil {
			t.Errorf("WonAt = %v, want nil", got.WonAt)
		}
		if got.LastAdvanceDate == nil || !got.LastAdvanceDate.Equal(advDay) {
			t.Errorf("LastAdvanceDate = %v, want carried %v (pacing survives the merge)", got.LastAdvanceDate, advDay)
		}
	})

	t.Run("survivor 5/6 + nil absorbed", func(t *testing.T) {
		survivor := ladderFixture("c-surv", 5, otherBase, dptr(advDay))
		got, err := MergeProgress(survivor, nil, cTenant, cGCID, "c-surv", cNow)
		if err != nil {
			t.Fatalf("MergeProgress: %v", err)
		}
		if got == nil {
			t.Fatal("expected a fresh 0-rung row, got nil")
		}
		if got.RungsCleared != 0 || len(got.RungClearedAt) != 0 {
			t.Errorf("rungs=%d audit=%d, want 0/0", got.RungsCleared, len(got.RungClearedAt))
		}
		if got.LastAdvanceDate == nil || !got.LastAdvanceDate.Equal(advDay) {
			t.Errorf("LastAdvanceDate = %v, want carried %v", got.LastAdvanceDate, advDay)
		}
	})
}

func TestMergeProgress_BothNilReturnsNil(t *testing.T) {
	got, err := MergeProgress(nil, nil, cTenant, cGCID, "c-surv", cNow)
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if got != nil {
		t.Errorf("got %+v, want nil (nothing to carry)", got)
	}
}

func TestMergeProgress_LastAdvanceDateIsLater(t *testing.T) {
	d7 := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	d8 := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	sBase := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	aBase := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		survAdv *time.Time
		absAdv  *time.Time
		want    *time.Time
	}{
		{"survivor later", dptr(d8), dptr(d7), dptr(d8)},
		{"absorbed later", dptr(d7), dptr(d8), dptr(d8)},
		{"survivor nil -> absorbed", nil, dptr(d8), dptr(d8)},
		{"absorbed nil -> survivor", dptr(d7), nil, dptr(d7)},
		{"both dates nil -> nil", nil, nil, nil},
		{"equal -> that date", dptr(d8), dptr(d8), dptr(d8)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			survivor := ladderFixture("c-surv", 2, sBase, tc.survAdv)
			absorbed := ladderFixture("c-abs", 2, aBase, tc.absAdv)
			got, err := MergeProgress(survivor, absorbed, cTenant, cGCID, "c-surv", cNow)
			if err != nil {
				t.Fatalf("MergeProgress: %v", err)
			}
			switch {
			case tc.want == nil && got.LastAdvanceDate != nil:
				t.Errorf("LastAdvanceDate = %v, want nil", got.LastAdvanceDate)
			case tc.want != nil && got.LastAdvanceDate == nil:
				t.Errorf("LastAdvanceDate = nil, want %v", *tc.want)
			case tc.want != nil && !got.LastAdvanceDate.Equal(*tc.want):
				t.Errorf("LastAdvanceDate = %v, want %v", *got.LastAdvanceDate, *tc.want)
			}
		})
	}
}

func TestMergeProgress_InputsNotMutatedAuditNonAliased(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	survivor := ladderFixture("c-surv", 4, base, dptr(base))
	absorbed := ladderFixture("c-abs", 3, base.Add(50*time.Hour), dptr(base.Add(24*time.Hour)))

	survRungs, survAuditLen, survStamp0 := survivor.RungsCleared, len(survivor.RungClearedAt), survivor.RungClearedAt[0]
	absRungs, absAuditLen := absorbed.RungsCleared, len(absorbed.RungClearedAt)

	got, err := MergeProgress(survivor, absorbed, cTenant, cGCID, "c-surv", cNow)
	if err != nil {
		t.Fatalf("MergeProgress: %v", err)
	}
	if survivor.RungsCleared != survRungs || len(survivor.RungClearedAt) != survAuditLen {
		t.Error("survivor mutated by MergeProgress")
	}
	if absorbed.RungsCleared != absRungs || len(absorbed.RungClearedAt) != absAuditLen {
		t.Error("absorbed mutated by MergeProgress")
	}
	if len(got.RungClearedAt) == 0 {
		t.Fatal("expected a non-empty merged audit trail for this case")
	}
	got.RungClearedAt[0] = got.RungClearedAt[0].Add(999 * time.Hour)
	if !survivor.RungClearedAt[0].Equal(survStamp0) {
		t.Error("merged audit slice aliases the survivor's backing array; want a deep copy")
	}
}

func TestMergeProgress_ValidatesIDs(t *testing.T) {
	survivor := ladderFixture("c-surv", 2, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), nil)
	cases := []struct {
		name                  string
		tenant, learner, surv string
	}{
		{"blank tenant", "", cGCID, "c-surv"},
		{"blank learner", cTenant, "", "c-surv"},
		{"blank survivor concept", cTenant, cGCID, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// non-nil survivor so the both-nil short-circuit does not fire first.
			_, err := MergeProgress(survivor, nil, tc.tenant, tc.learner, tc.surv, cNow)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestMergeProgress_AuditCardinalityInvariant(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	combos := []struct{ s, a int }{
		{0, 0}, {1, 0}, {0, 3}, {2, 4}, {4, 2}, {6, 6}, {6, 4}, {5, 5},
	}
	for _, c := range combos {
		survivor := ladderFixture("c-surv", c.s, base, dptr(base))
		absorbed := ladderFixture("c-abs", c.a, base.Add(72*time.Hour), dptr(base))
		got, err := MergeProgress(survivor, absorbed, cTenant, cGCID, "c-surv", cNow)
		if err != nil {
			t.Fatalf("s=%d a=%d: %v", c.s, c.a, err)
		}
		if got.RungsCleared != min(c.s, c.a) {
			t.Errorf("s=%d a=%d: RungsCleared %d != min", c.s, c.a, got.RungsCleared)
		}
		if len(got.RungClearedAt) != got.RungsCleared {
			t.Errorf("s=%d a=%d: audit len %d != RungsCleared %d (DB CHECK invariant)", c.s, c.a, len(got.RungClearedAt), got.RungsCleared)
		}
	}
}

func TestSplitProgress_CopiesLadderToEachChild(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	adv := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	parent := ladderFixture("c-parent", 4, base, dptr(adv)) // 4/6, not won

	children, err := SplitProgress(parent, cTenant, cGCID, []string{"c-a", "c-b"}, cNow)
	if err != nil {
		t.Fatalf("SplitProgress: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("got %d children, want 2", len(children))
	}
	seen := map[string]bool{"c-a": false, "c-b": false}
	for _, ch := range children {
		if _, ok := seen[ch.ConceptID]; !ok {
			t.Errorf("unexpected child concept %q", ch.ConceptID)
			continue
		}
		seen[ch.ConceptID] = true
		if ch.RungsCleared != 4 {
			t.Errorf("child %s RungsCleared = %d, want 4", ch.ConceptID, ch.RungsCleared)
		}
		if len(ch.RungClearedAt) != 4 {
			t.Errorf("child %s audit len = %d, want 4", ch.ConceptID, len(ch.RungClearedAt))
		}
		for i := 0; i < len(ch.RungClearedAt) && i < 4; i++ {
			if !ch.RungClearedAt[i].Equal(parent.RungClearedAt[i]) {
				t.Errorf("child %s stamp[%d] = %v, want parent's %v", ch.ConceptID, i, ch.RungClearedAt[i], parent.RungClearedAt[i])
			}
		}
		if ch.WonAt != nil {
			t.Errorf("child %s WonAt = %v, want nil (parent not won)", ch.ConceptID, ch.WonAt)
		}
		if ch.LastAdvanceDate == nil || !ch.LastAdvanceDate.Equal(adv) {
			t.Errorf("child %s LastAdvanceDate = %v, want %v", ch.ConceptID, ch.LastAdvanceDate, adv)
		}
		if ch.CurrentRungCorrect != 0 {
			t.Errorf("child %s CurrentRungCorrect = %d, want 0", ch.ConceptID, ch.CurrentRungCorrect)
		}
		if ch.TenantID != cTenant || ch.LearnerGCID != cGCID {
			t.Errorf("child %s identity = (%s,%s), want (%s,%s)", ch.ConceptID, ch.TenantID, ch.LearnerGCID, cTenant, cGCID)
		}
		if ch.ID == "" || ch.ID == parent.ID {
			t.Errorf("child %s ID = %q, want a fresh id", ch.ConceptID, ch.ID)
		}
		if !ch.CreatedAt.Equal(cNow) || !ch.UpdatedAt.Equal(cNow) {
			t.Errorf("child %s timestamps not now", ch.ConceptID)
		}
	}
	for id, ok := range seen {
		if !ok {
			t.Errorf("missing child %q", id)
		}
	}
	if children[0].ID == children[1].ID {
		t.Error("children share an ID; want a distinct fresh UUIDv7 per child")
	}
}

func TestSplitProgress_WonParentYieldsWonChildren(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	parent := ladderFixture("c-parent", 6, base, dptr(base)) // won
	if parent.WonAt == nil {
		t.Fatal("fixture: parent must be won")
	}
	children, err := SplitProgress(parent, cTenant, cGCID, []string{"c-a", "c-b", "c-c"}, cNow)
	if err != nil {
		t.Fatalf("SplitProgress: %v", err)
	}
	for _, ch := range children {
		if ch.RungsCleared != 6 {
			t.Errorf("child %s RungsCleared = %d, want 6 (inherit won parent)", ch.ConceptID, ch.RungsCleared)
		}
		if ch.WonAt == nil {
			t.Errorf("child %s WonAt = nil, want the inherited stamp", ch.ConceptID)
			continue
		}
		if !ch.WonAt.Equal(*parent.WonAt) {
			t.Errorf("child %s WonAt = %v, want parent's %v", ch.ConceptID, *ch.WonAt, *parent.WonAt)
		}
		if ch.WonAt == parent.WonAt {
			t.Errorf("child %s WonAt aliases parent's pointer; want a deep copy", ch.ConceptID)
		}
	}
}

func TestSplitProgress_SliceNonAliasing(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	parent := ladderFixture("c-parent", 3, base, dptr(base))
	parentStamp0 := parent.RungClearedAt[0]

	children, err := SplitProgress(parent, cTenant, cGCID, []string{"c-a", "c-b"}, cNow)
	if err != nil {
		t.Fatalf("SplitProgress: %v", err)
	}
	children[0].RungClearedAt[0] = children[0].RungClearedAt[0].Add(500 * time.Hour)
	if !parent.RungClearedAt[0].Equal(parentStamp0) {
		t.Error("child audit aliases the parent's slice")
	}
	if children[1].RungClearedAt[0].Equal(children[0].RungClearedAt[0]) {
		t.Error("sibling children share the same backing audit array")
	}
}

func TestSplitProgress_NilParent(t *testing.T) {
	got, err := SplitProgress(nil, cTenant, cGCID, []string{"c-a"}, cNow)
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil for a nil parent", got)
	}
}

func TestSplitProgress_Guards(t *testing.T) {
	parent := ladderFixture("c-parent", 2, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), nil)
	cases := []struct {
		name     string
		children []string
	}{
		{"empty slice", nil},
		{"one blank child id", []string{"c-a", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SplitProgress(parent, cTenant, cGCID, tc.children, cNow)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestSplitProgress_GuardsTenantLearner(t *testing.T) {
	parent := ladderFixture("c-parent", 2, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), nil)
	if _, err := SplitProgress(parent, "", cGCID, []string{"c-a"}, cNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("blank tenant: err = %v, want ErrInvalid", err)
	}
	if _, err := SplitProgress(parent, cTenant, "", []string{"c-a"}, cNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("blank learner: err = %v, want ErrInvalid", err)
	}
}

func TestPriorLadderState(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	t.Run("empty is zero and never-won", func(t *testing.T) {
		mx, won := PriorLadderState(nil)
		if mx != 0 || won {
			t.Errorf("got (%d,%v), want (0,false)", mx, won)
		}
	})

	t.Run("max rungs and everWon from any won row", func(t *testing.T) {
		rows := []*NodeProgress{
			ladderFixture("a", 2, base, nil),
			ladderFixture("b", 5, base, nil), // 5/6 is not won
			ladderFixture("c", 6, base, nil), // won
			ladderFixture("d", 3, base, nil),
		}
		mx, won := PriorLadderState(rows)
		if mx != 6 {
			t.Errorf("maxRungsCleared = %d, want 6", mx)
		}
		if !won {
			t.Error("everWon = false, want true (row c reached 6/6)")
		}
	})

	t.Run("no won rows", func(t *testing.T) {
		rows := []*NodeProgress{
			ladderFixture("a", 4, base, nil),
			ladderFixture("b", 1, base, nil),
		}
		mx, won := PriorLadderState(rows)
		if mx != 4 || won {
			t.Errorf("got (%d,%v), want (4,false)", mx, won)
		}
	})

	t.Run("nil entries tolerated", func(t *testing.T) {
		rows := []*NodeProgress{nil, ladderFixture("a", 2, base, nil), nil}
		mx, won := PriorLadderState(rows)
		if mx != 2 || won {
			t.Errorf("got (%d,%v), want (2,false)", mx, won)
		}
	})
}
