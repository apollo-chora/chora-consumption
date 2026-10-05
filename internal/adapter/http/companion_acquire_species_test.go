package http

// companion_acquire_species_test.go — CHO-2013 P1 (R3-7 + dev_hatched honest
// fix): sovereign acquire gains an optional learner-picked `species` (5-hero
// enum) and every acquired companion is BORN HATCHED (stage 1) via the growth
// InitBornHatched port instead of minting a silent stage-0 egg. The response
// echoes the EFFECTIVE species either way (pick, or the 0046 rolled default).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

type fakeBornHatcher struct {
	err     error
	calls   []growth.InitBornHatchedInput
	species string // effective species echoed back ("" ⇒ echo the pick)
}

func (f *fakeBornHatcher) InitBornHatched(_ context.Context, in growth.InitBornHatchedInput) (*growth.InitBornHatchedResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.calls = append(f.calls, in)
	eff := f.species
	if eff == "" {
		eff = in.Species
	}
	return &growth.InitBornHatchedResponse{
		State:   growth.State{CompanionID: in.CompanionID, GrowthStage: 1, StageName: "baby", Species: eff},
		Species: eff,
	}, nil
}

func TestAcquire_SpeciesPickThreadsToBornHatch(t *testing.T) {
	s, insts, _ := acqServer(false, mapsPtr("c-root"), acqRootConcept("Scrum"))
	bh := &fakeBornHatcher{}
	s.GrowthInit = bh
	w := acqServe(s, `{"goalId":"g-1","companionName":"Hoot","mode":"dev_hatched","species":"owl"}`, fmTenantID, fmGCID)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d (body=%s)", w.Code, w.Body.String())
	}
	if len(bh.calls) != 1 {
		t.Fatalf("InitBornHatched calls = %d", len(bh.calls))
	}
	if bh.calls[0].Species != "owl" || bh.calls[0].CompanionID != insts.created.CompanionID {
		t.Fatalf("InitBornHatched input: %+v", bh.calls[0])
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["species"] != "owl" {
		t.Fatalf("resp species = %v", resp["species"])
	}
	if resp["growthStage"] != float64(1) {
		t.Fatalf("resp growthStage = %v want 1 (born hatched)", resp["growthStage"])
	}
}

func TestAcquire_OmittedSpeciesEchoesRolledDefault(t *testing.T) {
	s, _, _ := acqServer(false, mapsPtr("c-root"), acqRootConcept("Scrum"))
	bh := &fakeBornHatcher{species: "penguin"} // the 0046 default the repo rolled
	s.GrowthInit = bh
	w := acqServe(s, `{"goalId":"g-1","mode":"dev_hatched"}`, fmTenantID, fmGCID)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d (body=%s)", w.Code, w.Body.String())
	}
	if len(bh.calls) != 1 || bh.calls[0].Species != "" {
		t.Fatalf("expected empty species pick, got %+v", bh.calls)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["species"] != "penguin" {
		t.Fatalf("resp species = %v want the rolled default", resp["species"])
	}
}

func TestAcquire_InvalidSpecies422_NoMint(t *testing.T) {
	s, insts, _ := acqServer(false, mapsPtr("c-root"), acqRootConcept("Scrum"))
	s.GrowthInit = &fakeBornHatcher{}
	w := acqServe(s, `{"goalId":"g-1","mode":"dev_hatched","species":"basilisk"}`, fmTenantID, fmGCID)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d want 422", w.Code)
	}
	if insts.created != nil {
		t.Fatalf("instance minted despite invalid species")
	}
}

func TestAcquire_BornHatchFailureCompensatesOrphan(t *testing.T) {
	s, insts, goals := acqServer(false, mapsPtr("c-root"), acqRootConcept("Scrum"))
	s.GrowthInit = &fakeBornHatcher{err: errors.New("pg down")}
	w := acqServe(s, `{"goalId":"g-1","mode":"dev_hatched"}`, fmTenantID, fmGCID)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d want 500", w.Code)
	}
	if len(insts.softDeleted) != 1 || insts.softDeleted[0] != insts.created.CompanionID {
		t.Fatalf("orphan not compensated: softDeleted=%v created=%+v", insts.softDeleted, insts.created)
	}
	if goals.updated != nil {
		t.Fatalf("goal must not attach when born-hatch failed")
	}
}

func TestAcquire_GrowthInitUnwiredFailsLoud(t *testing.T) {
	s, insts, _ := acqServer(false, mapsPtr("c-root"), acqRootConcept("Scrum"))
	s.GrowthInit = nil
	w := acqServe(s, `{"goalId":"g-1","mode":"dev_hatched"}`, fmTenantID, fmGCID)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d want 503 (fail-loud unwired born-hatch)", w.Code)
	}
	if insts.created != nil {
		t.Fatalf("instance minted despite unwired growth init")
	}
}
