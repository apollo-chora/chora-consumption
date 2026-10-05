package companion

import (
	"context"
	"errors"
	"testing"
)

// personaFakeRepo is a minimal in-memory InstanceRepository for the
// persona service tests. Only Get + Update carry behaviour; the rest
// satisfy the port. updateCalls records how many times Update ran so a
// test can assert an INVALID edit persists NOTHING.
type personaFakeRepo struct {
	byID        map[string]*Instance
	getErr      error
	updateErr   error
	updateCalls int
	updated     *Instance
}

func newPersonaFakeRepo() *personaFakeRepo {
	return &personaFakeRepo{byID: map[string]*Instance{}}
}

func (f *personaFakeRepo) Create(_ context.Context, inst *Instance, _ int) error {
	f.byID[inst.CompanionID] = inst
	return nil
}

func (f *personaFakeRepo) Get(_ context.Context, companionID string) (*Instance, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	inst, ok := f.byID[companionID]
	if !ok {
		return nil, ErrInstanceNotFound
	}
	return inst, nil
}

func (f *personaFakeRepo) ListByOwner(_ context.Context, _, _ string) ([]*Instance, error) {
	return nil, nil
}

func (f *personaFakeRepo) ListRosterByOwner(_ context.Context, _, _ string) ([]*RosterEntry, error) {
	return nil, nil
}

func (f *personaFakeRepo) Update(_ context.Context, inst *Instance) error {
	f.updateCalls++
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updated = inst
	f.byID[inst.CompanionID] = inst
	return nil
}

func (f *personaFakeRepo) SoftDelete(_ context.Context, _ string) error { return nil }

// validEdit returns a bounded-grammar-valid PersonaEdit.
func validEdit() PersonaEdit {
	return PersonaEdit{
		Tone:                 "encouraging",
		HintProgression:      "ladder",
		MaxHintsBeforeReveal: 2,
		DifficultyCap:        "intermediate",
		Language:             "en",
		CitationStrictness:   "strict",
		Archetype:            "curious-explorer",
		AddressStyle:         "first_name",
		InterestChips:        []string{"space", "dinosaurs"},
		GuidanceNote:         "Use space analogies when you can.",
	}
}

func seedInstance(repo *personaFakeRepo, companionID, ownerGCID string) *Instance {
	inst := &Instance{
		CompanionID:     companionID,
		TenantID:        "tenant-1",
		OwnerGCID:       ownerGCID,
		ConfiguredRules: map[string]string{},
	}
	repo.byID[companionID] = inst
	return inst
}

func TestNewPersonaService_RejectsNilRepo(t *testing.T) {
	t.Parallel()
	if _, err := NewPersonaService(nil); err == nil {
		t.Fatal("NewPersonaService(nil): expected error")
	}
}

func TestGetPersona_NeverEdited_ReturnsDefaultView(t *testing.T) {
	t.Parallel()
	repo := newPersonaFakeRepo()
	seedInstance(repo, "fam-1", "owner-1")
	svc, err := NewPersonaService(repo)
	if err != nil {
		t.Fatalf("NewPersonaService: %v", err)
	}
	got, err := svc.GetPersona(context.Background(), "fam-1", "owner-1")
	if err != nil {
		t.Fatalf("GetPersona: %v", err)
	}
	// A never-edited companion returns the coherent default sheet (defaults
	// mirror the agent's mapConfiguredRules) at version 0.
	if got.Tone != "encouraging" || got.DifficultyCap != "intermediate" || got.CitationStrictness != "strict" {
		t.Errorf("default view = %+v; want encouraging/intermediate/strict defaults", got)
	}
	if got.Version != 0 {
		t.Errorf("Version = %d; want 0 on never-edited", got.Version)
	}
}

func TestGetPersona_NotFound(t *testing.T) {
	t.Parallel()
	repo := newPersonaFakeRepo()
	svc, _ := NewPersonaService(repo)
	_, err := svc.GetPersona(context.Background(), "missing", "owner-1")
	if !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("GetPersona(missing) err = %v; want ErrInstanceNotFound", err)
	}
}

func TestGetPersona_WrongOwner_IsIndistinguishableFromMissing(t *testing.T) {
	t.Parallel()
	repo := newPersonaFakeRepo()
	seedInstance(repo, "fam-1", "owner-1")
	svc, _ := NewPersonaService(repo)
	_, err := svc.GetPersona(context.Background(), "fam-1", "other-owner")
	if !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("GetPersona(wrong owner) err = %v; want ErrInstanceNotFound (no cross-owner disclosure)", err)
	}
}

func TestUpdatePersona_HappyPath_PersistsAndBumpsVersion(t *testing.T) {
	t.Parallel()
	repo := newPersonaFakeRepo()
	seedInstance(repo, "fam-1", "owner-1")
	svc, _ := NewPersonaService(repo)
	got, err := svc.UpdatePersona(context.Background(), "fam-1", "owner-1", validEdit())
	if err != nil {
		t.Fatalf("UpdatePersona: %v", err)
	}
	if repo.updateCalls != 1 {
		t.Fatalf("Update calls = %d; want 1", repo.updateCalls)
	}
	if got.Version != 1 {
		t.Errorf("view.Version = %d; want 1 after first save", got.Version)
	}
	if got.Tone != "encouraging" || got.Archetype != "curious-explorer" || got.GuidanceNote != "Use space analogies when you can." {
		t.Errorf("persisted view = %+v; unexpected", got)
	}
	// Typed knobs must land in ConfiguredRules (the shape the agent reads).
	if repo.updated.ConfiguredRules[RuleTone] != "encouraging" || repo.updated.ConfiguredRules[RuleAddressStyle] != "first_name" {
		t.Errorf("ConfiguredRules = %v; want tone+address_style written", repo.updated.ConfiguredRules)
	}
	if repo.updated.GuidanceNote != "Use space analogies when you can." {
		t.Errorf("GuidanceNote = %q; not persisted", repo.updated.GuidanceNote)
	}
}

func TestUpdatePersona_InvalidEdit_PersistsNothing(t *testing.T) {
	t.Parallel()
	repo := newPersonaFakeRepo()
	seedInstance(repo, "fam-1", "owner-1")
	svc, _ := NewPersonaService(repo)
	bad := validEdit()
	bad.Tone = "sarcastic" // not in the bounded grammar
	_, err := svc.UpdatePersona(context.Background(), "fam-1", "owner-1", bad)
	if !errors.Is(err, ErrPersonaBadTone) {
		t.Fatalf("UpdatePersona(bad tone) err = %v; want ErrPersonaBadTone", err)
	}
	if repo.updateCalls != 0 {
		t.Fatalf("Update calls = %d; want 0 — an invalid edit must persist NOTHING", repo.updateCalls)
	}
}

func TestUpdatePersona_WrongOwner_IsIndistinguishableFromMissing(t *testing.T) {
	t.Parallel()
	repo := newPersonaFakeRepo()
	seedInstance(repo, "fam-1", "owner-1")
	svc, _ := NewPersonaService(repo)
	_, err := svc.UpdatePersona(context.Background(), "fam-1", "intruder", validEdit())
	if !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("UpdatePersona(wrong owner) err = %v; want ErrInstanceNotFound", err)
	}
	if repo.updateCalls != 0 {
		t.Fatalf("Update calls = %d; want 0 for a non-owner", repo.updateCalls)
	}
}

func TestUpdatePersona_RepoUpdateError_Surfaces(t *testing.T) {
	t.Parallel()
	repo := newPersonaFakeRepo()
	seedInstance(repo, "fam-1", "owner-1")
	repo.updateErr = errors.New("boom")
	svc, _ := NewPersonaService(repo)
	_, err := svc.UpdatePersona(context.Background(), "fam-1", "owner-1", validEdit())
	if err == nil {
		t.Fatal("UpdatePersona: expected repo Update error to surface (fail-loud)")
	}
}
