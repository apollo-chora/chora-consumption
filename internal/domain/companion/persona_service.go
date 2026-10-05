// persona_service.go — the application service for the learner-designed
// Persona sheet (CHO-2015, ADR-219 D2). It orchestrates the load →
// owner-check → apply → persist flow over the InstanceRepository port.
//
// Separation of concerns:
//   - persona.go (the aggregate): validates the bounded SHAPE + applies the
//     edit + bumps the version. Pure domain.
//   - PersonaService (this file): loads the owning Instance, enforces
//     ownership, delegates the mutation to ApplyPersona, and persists.
//   - the HTTP handler (adapter): authenticates, decodes, and — crucially —
//     Model-Armor-screens the free-text note for SAFETY *before* calling
//     UpdatePersona. Screening is an outbound-adapter concern, so it stays
//     at the edge; this service is guardrail-agnostic and receives an edit
//     whose note has already cleared the safety gate.
//
// Ownership: a non-owner request is indistinguishable from a missing
// companion (both → ErrInstanceNotFound), mirroring the growth service's
// no-cross-owner-disclosure posture (resonance.go). Tenant isolation is
// enforced upstream by RLS on the request context (the pg InstanceRepository
// SETs LOCAL chora.tenant_id inside its tx); this service adds the
// owner-level check on top.
//
// B6 (deferred) attaches a `companion.persona_updated.v1` outbox emit to
// UpdatePersona — the load-mutate-persist body here is the natural seam.
package companion

import (
	"context"
	"errors"
)

// PersonaService loads + persists the Persona sheet over the Instance
// aggregate. Construct via NewPersonaService.
type PersonaService struct {
	repo InstanceRepository
}

// NewPersonaService wires the service over an InstanceRepository. A nil
// repo is a wiring error (fail-loud — never a silently no-op service).
func NewPersonaService(repo InstanceRepository) (*PersonaService, error) {
	if repo == nil {
		return nil, errors.New("companion.PersonaService: repo required")
	}
	return &PersonaService{repo: repo}, nil
}

// GetPersona returns the current editable Persona view for a companion the
// caller owns. Missing companion OR non-owner → ErrInstanceNotFound (no
// cross-owner disclosure). A never-edited companion returns the coherent
// default sheet (see Instance.Persona).
func (s *PersonaService) GetPersona(ctx context.Context, companionID, ownerGCID string) (PersonaView, error) {
	inst, err := s.load(ctx, companionID, ownerGCID)
	if err != nil {
		return PersonaView{}, err
	}
	return inst.Persona(), nil
}

// UpdatePersona applies a full Persona-sheet edit to a companion the caller
// owns and persists it. The edit's free-text note MUST already have cleared
// the Model Armor safety gate at the HTTP edge; this method enforces the
// bounded SHAPE (via Instance.ApplyPersona) and the ownership invariant.
//
// Atomicity: an invalid edit returns the shape error and persists NOTHING
// (ApplyPersona validates before mutating, and Update is only called on a
// clean apply). Returns the freshly-persisted view (version bumped).
func (s *PersonaService) UpdatePersona(ctx context.Context, companionID, ownerGCID string, edit PersonaEdit) (PersonaView, error) {
	inst, err := s.load(ctx, companionID, ownerGCID)
	if err != nil {
		return PersonaView{}, err
	}
	if err := inst.ApplyPersona(edit); err != nil {
		return PersonaView{}, err
	}
	if err := s.repo.Update(ctx, inst); err != nil {
		return PersonaView{}, err
	}
	return inst.Persona(), nil
}

// load fetches the instance and enforces ownership. A non-owner is
// reported as ErrInstanceNotFound so a caller cannot enumerate other
// learners' companions within the same tenant.
func (s *PersonaService) load(ctx context.Context, companionID, ownerGCID string) (*Instance, error) {
	inst, err := s.repo.Get(ctx, companionID)
	if err != nil {
		return nil, err
	}
	if inst.OwnerGCID != ownerGCID {
		return nil, ErrInstanceNotFound
	}
	return inst, nil
}
