// growth_scan_optional_test.go — exercises the optional-field dereference
// paths of scanGrowthRow + scanGrowthEventRow / scanGrowthEventRows.
package pg

import (
	"testing"
	"time"
)

// rowFixtureFull populates every optional pointer-targeted field in
// scanGrowthRow so the *deref branches all execute.
func rowFixtureFull() scanFn {
	return func(dest ...any) error {
		*(dest[0].(*string)) = "fam-1"
		*(dest[1].(*string)) = pgTenantID
		*(dest[2].(*string)) = pgUserGCID
		*(dest[3].(*int)) = 3
		s1 := "owl"
		*(dest[4].(**string)) = &s1
		b1 := true
		*(dest[5].(**bool)) = &b1
		s2 := "common"
		*(dest[6].(**string)) = &s2
		f1 := 35.0
		*(dest[7].(**float64)) = &f1
		*(dest[8].(*int)) = 200
		s3 := "01970000-0000-7000-a000-000000000001"
		*(dest[9].(**string)) = &s3
		sc := "01970000-0000-7000-a000-00000000cc01"
		*(dest[10].(**string)) = &sc // resonant_concept_id (CHO-2013 P1)
		ts1 := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
		ts0 := ts1.Add(-time.Hour)
		*(dest[11].(**time.Time)) = &ts0 // revealed_at (CHO-2229)
		*(dest[12].(**time.Time)) = &ts1
		*(dest[13].(*bool)) = true
		ts2 := ts1.Add(24 * time.Hour)
		*(dest[14].(**time.Time)) = &ts2
		s4 := "pro"
		*(dest[15].(**string)) = &s4
		s5 := "egg.standard.v1"
		*(dest[16].(**string)) = &s5
		s6 := "purchase-1"
		*(dest[17].(**string)) = &s6
		ts3 := ts1.Add(-24 * time.Hour)
		*(dest[18].(**time.Time)) = &ts3
		ts4 := ts1.Add(30 * 24 * time.Hour)
		*(dest[19].(**time.Time)) = &ts4
		ts5 := ts1.Add(60 * 24 * time.Hour)
		*(dest[20].(**time.Time)) = &ts5
		s7 := "purchase"
		*(dest[21].(**string)) = &s7
		s8 := "flash"
		*(dest[22].(**string)) = &s8
		ts6 := ts1
		*(dest[23].(**time.Time)) = &ts6
		s9 := "Pip"
		*(dest[24].(**string)) = &s9
		s10 := "encouraging"
		*(dest[25].(**string)) = &s10
		s11 := "curious-explorer"
		*(dest[26].(**string)) = &s11
		s12 := "general"
		*(dest[27].(**string)) = &s12
		return nil
	}
}

func TestScanGrowthRow_PopulatesAllOptionalFields(t *testing.T) {
	row := stubRow{fn: rowFixtureFull()}
	out, err := scanGrowthRow(row)
	if err != nil {
		t.Fatalf("scanGrowthRow: %v", err)
	}
	if out.Species != "owl" {
		t.Errorf("Species = %s", out.Species)
	}
	if !out.ShinyVariant {
		t.Errorf("ShinyVariant = false")
	}
	if out.SpeciesRarity != "common" {
		t.Errorf("Rarity = %s", out.SpeciesRarity)
	}
	if out.RolledProb != 35.0 {
		t.Errorf("RolledProb = %v", out.RolledProb)
	}
	if out.GrowthExp != 200 {
		t.Errorf("GrowthExp = %d", out.GrowthExp)
	}
	if out.ResonantAtom == "" {
		t.Errorf("ResonantAtom empty")
	}
	if out.ResonantConceptID != "01970000-0000-7000-a000-00000000cc01" {
		t.Errorf("ResonantConceptID = %s", out.ResonantConceptID)
	}
	if out.HatchedAt == nil {
		t.Errorf("HatchedAt nil")
	}
	if !out.AhaMomentConsumed {
		t.Errorf("AhaMomentConsumed false")
	}
	if out.AhaMomentActiveUntil == nil {
		t.Errorf("AhaMomentActiveUntil nil")
	}
	if out.AhaMomentPreviewLLMTier != "pro" {
		t.Errorf("AhaMomentPreviewLLMTier = %s", out.AhaMomentPreviewLLMTier)
	}
	if out.EggSku != "egg.standard.v1" {
		t.Errorf("EggSku = %s", out.EggSku)
	}
	if out.EggPurchaseID != "purchase-1" {
		t.Errorf("EggPurchaseID = %s", out.EggPurchaseID)
	}
	if out.EggSource != "purchase" {
		t.Errorf("EggSource = %s", out.EggSource)
	}
	if out.EffectiveLLMTierCached != "flash" {
		t.Errorf("EffectiveLLMTierCached = %s", out.EffectiveLLMTierCached)
	}
	if out.LastStageUpAt == nil {
		t.Errorf("LastStageUpAt nil")
	}
	if out.DisplayName != "Pip" {
		t.Errorf("DisplayName = %s", out.DisplayName)
	}
	if out.Tone != "encouraging" {
		t.Errorf("Tone = %s", out.Tone)
	}
	if out.LearnerPersona != "curious-explorer" {
		t.Errorf("LearnerPersona = %s", out.LearnerPersona)
	}
	if out.Specialization != "general" {
		t.Errorf("Specialization = %s", out.Specialization)
	}
}

// growthEventScanFull populates the optional pointer-typed fields on
// companion_growth_events so scanGrowthEventRow's deref branches fire.
func growthEventScanFull() scanFn {
	return func(dest ...any) error {
		*(dest[0].(*string)) = "evt-1"
		*(dest[1].(*string)) = pgTenantID
		*(dest[2].(*string)) = "fam-1"
		*(dest[3].(*string)) = pgUserGCID
		*(dest[4].(*string)) = "atom_session"
		*(dest[5].(*int)) = 3
		*(dest[6].(*int)) = 3
		*(dest[7].(*int)) = 51
		*(dest[8].(*bool)) = false
		*(dest[9].(*bool)) = true
		*(dest[10].(*bool)) = false
		*(dest[11].(*string)) = "idem-evt-1"
		s1 := "upstream-event-1"
		*(dest[12].(**string)) = &s1
		s2 := "chora.creation.atom.published.v1"
		*(dest[13].(**string)) = &s2
		s3 := "01970000-0000-7000-c000-000000000001"
		*(dest[14].(**string)) = &s3
		i1 := 7
		*(dest[15].(**int)) = &i1
		*(dest[16].(*time.Time)) = time.Now().UTC()
		return nil
	}
}

func TestScanGrowthEventRow_PopulatesAllOptionals(t *testing.T) {
	row := stubRow{fn: growthEventScanFull()}
	ev, err := scanGrowthEventRow(row)
	if err != nil {
		t.Fatalf("scanGrowthEventRow: %v", err)
	}
	if ev.SourceEventID != "upstream-event-1" {
		t.Errorf("SourceEventID = %s", ev.SourceEventID)
	}
	if ev.SourceTopic != "chora.creation.atom.published.v1" {
		t.Errorf("SourceTopic = %s", ev.SourceTopic)
	}
	if ev.SourceSessionID == "" {
		t.Errorf("SourceSessionID empty")
	}
	if ev.SourceTurnSeq != 7 {
		t.Errorf("SourceTurnSeq = %d", ev.SourceTurnSeq)
	}
}

// scanGrowthEventRows is exercised by the same data shape via a 1-row Rows.
func TestScanGrowthEventRows_PopulatesAllOptionals(t *testing.T) {
	rows := &stubRows{
		scans: []scanFn{growthEventScanFull()},
	}
	rows.Next()
	ev, err := scanGrowthEventRows(rows)
	if err != nil {
		t.Fatalf("scanGrowthEventRows: %v", err)
	}
	if ev.SourceEventID != "upstream-event-1" {
		t.Errorf("SourceEventID = %s", ev.SourceEventID)
	}
	if ev.SourceTurnSeq != 7 {
		t.Errorf("SourceTurnSeq = %d", ev.SourceTurnSeq)
	}
}
