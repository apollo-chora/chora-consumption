// dose_pref_test.go — pure-domain TDD for the per-KG daily-dose preference
// aggregate (ADR-224 change #3 / CHO-2045). Verifies construction + validation
// (the three opaque-UUID scope fields required), field trimming, UUIDv7 minting,
// clock stamping, and the include/exclude polarity. No infra imports — the clock
// is always passed in (hexagonal purity, mirrors learner_weakness / topic_retention).
package dose_pref

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var dpNow = time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

const (
	tTenant = "01970000-0000-7000-8000-000000000001"
	tGCID   = "01970000-0000-7000-9000-000000000001"
	tMap    = "01970000-0000-7000-a000-000000000001"
)

func TestNew_Success(t *testing.T) {
	p, err := New(tTenant, tGCID, tMap, true, dpNow)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.ID == "" || len(p.ID) != 36 {
		t.Errorf("expected a minted UUIDv7 id; got %q", p.ID)
	}
	if p.ID[14] != '7' {
		t.Errorf("id %q is not a v7 UUID (version nibble != 7)", p.ID)
	}
	if p.TenantID != tTenant || p.LearnerGCID != tGCID || p.MapID != tMap {
		t.Errorf("scope not stamped: %+v", p)
	}
	if !p.Included {
		t.Error("Included = false; want true")
	}
	if !p.CreatedAt.Equal(dpNow) || !p.UpdatedAt.Equal(dpNow) {
		t.Errorf("timestamps not seeded from now: created=%v updated=%v", p.CreatedAt, p.UpdatedAt)
	}
}

func TestNew_ExcludedPolarity(t *testing.T) {
	p, err := New(tTenant, tGCID, tMap, false, dpNow)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Included {
		t.Error("Included = true; want false (explicit exclude row)")
	}
}

func TestNew_Validation(t *testing.T) {
	cases := map[string]struct {
		tenant, gcid, mapID string
	}{
		"blank tenant":      {"", tGCID, tMap},
		"whitespace tenant": {"   ", tGCID, tMap},
		"blank gcid":        {tTenant, "", tMap},
		"whitespace gcid":   {tTenant, "  ", tMap},
		"blank map_id":      {tTenant, tGCID, ""},
		"whitespace map_id": {tTenant, tGCID, "\t"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := New(c.tenant, c.gcid, c.mapID, true, dpNow)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v; want ErrInvalid", err)
			}
			if p != nil {
				t.Errorf("expected nil pref on invalid input; got %+v", p)
			}
		})
	}
}

func TestNew_TrimsScopeFields(t *testing.T) {
	p, err := New("  "+tTenant+"  ", "\t"+tGCID+"\n", " "+tMap+" ", true, dpNow)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.TenantID != tTenant || p.LearnerGCID != tGCID || p.MapID != tMap {
		t.Errorf("scope fields not trimmed: %+v", p)
	}
}

func TestNew_ZeroNowDefaultsToUTCNow(t *testing.T) {
	p, err := New(tTenant, tGCID, tMap, true, time.Time{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.CreatedAt.IsZero() {
		t.Error("zero now must default to a real timestamp")
	}
	if !p.CreatedAt.Equal(p.UpdatedAt) {
		t.Errorf("created (%v) != updated (%v) on mint", p.CreatedAt, p.UpdatedAt)
	}
	if loc := p.CreatedAt.Location(); loc != time.UTC {
		t.Errorf("defaulted now must be UTC; got %v", loc)
	}
}

func TestNew_MintsDistinctIDs(t *testing.T) {
	a, _ := New(tTenant, tGCID, tMap, true, dpNow)
	b, _ := New(tTenant, tGCID, tMap, true, dpNow)
	if a.ID == b.ID {
		t.Errorf("expected distinct UUIDv7 ids; both were %q", a.ID)
	}
	if strings.TrimSpace(a.ID) == "" {
		t.Error("empty id")
	}
}
