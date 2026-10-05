package growth

// born_hatched.go — CHO-2013 P1 (R3-7 + the dev_hatched honest fix):
// sovereign acquisition ("acquire a Companion for a map") mints a creature
// that is BORN HATCHED — stage 1, hatched_at stamped — instead of the silent
// stage-0 egg the P0 scratch walk exposed (nothing ever hatched it; the first
// EXP award "self-healed" 0→1 only because threshold(1)=0).
//
// The learner may pick a hero species they have NOT met yet (R3-7, PROVISIONAL
// adopted-AFK); when omitted the domain resolves one from the unseen set and
// writes it EXPLICITLY. It never leaves the 0046 random-hero column DEFAULT
// standing: that default rolls at INSERT knowing nothing about the learner's
// roster, so leaving it is exactly how a repeat slips through (owner ruling
// 2026-08-07). The egg funnel keeps its gacha roll+reveal (HatchEgg) -
// born-hatched emits NO breed_revealed. Emissions mirror the hatch chain's
// meaningful subset: hatched → stage_up(0→1) → skill_slot_unlocked, then the
// species Path unlocker runs (band no-op at stage 1 with today's K bands).

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// sovereignPool is the candidate pool for the sovereign lane, which has no egg
// SKU to derive odds from (companion_instances rows minted by acquire carry no
// egg_sku, so BreedDistributionProvider has nothing to look up). It is DERIVED
// from CanonicalSpecies - the same roll authority ValidateBreedDistribution
// enforces - so a species authored later joins this lane by being added there,
// with no second roster to keep in step. Uniform: the sovereign lane is a
// choice, not a gacha, and carries no rarity weighting.
func sovereignPool() []BreedWeight {
	share := 100.0 / float64(len(CanonicalSpecies))
	pool := make([]BreedWeight, 0, len(CanonicalSpecies))
	var sum float64
	for _, sp := range CanonicalSpecies {
		pool = append(pool, BreedWeight{Species: sp, Probability: share})
		sum += share
	}
	// Absorb float drift so the sum-to-100 invariant holds exactly (RollBreed
	// validates the pool it is handed).
	pool[0].Probability += 100.0 - sum
	return pool
}

// unseenSpeciesRemain reports whether any species in the roll authority is still
// unowned. It is the wrap test: false ⇒ the roster has wrapped and a repeat is
// allowed again (the ruling never blocks an acquisition).
func unseenSpeciesRemain(owned map[string]bool) bool {
	for _, sp := range CanonicalSpecies {
		if !owned[sp] {
			return true
		}
	}
	return false
}

// InitBornHatchedInput identifies the mint + the optional species pick.
type InitBornHatchedInput struct {
	TenantID    string
	CompanionID string
	OwnerGCID   string
	Species     string // optional learner pick; "" ⇒ resolved from the unseen set
	DisplayName string
	Traceparent string
	Tracestate  string
}

// InitBornHatchedResponse returns the refreshed state + the effective
// species (the pick, or the unseen-set resolution when omitted) so the acquire
// response can echo it either way (R3-7).
type InitBornHatchedResponse struct {
	State   State
	Species string
}

// BornHatchedTxInput is the repository transition input.
type BornHatchedTxInput struct {
	TenantID    string
	CompanionID string
	OwnerGCID   string
	Species     string // always set since 2026-08-07; "" would leave the 0046 DEFAULT standing
	Now         time.Time
}

// InitBornHatched advances a freshly-minted stage-0 sovereign companion to
// stage 1 (born hatched), applying the learner's species pick or, when it is
// omitted, one resolved from the species they have not met yet.
func (s *Service) InitBornHatched(ctx context.Context, in InitBornHatchedInput) (*InitBornHatchedResponse, error) {
	if strings.TrimSpace(in.TenantID) == "" || strings.TrimSpace(in.CompanionID) == "" || strings.TrimSpace(in.OwnerGCID) == "" {
		return nil, fmt.Errorf("%w: tenant/companion/owner required", ErrInvalidArguments)
	}
	in.Species = strings.TrimSpace(in.Species)
	if in.Species != "" && !IsValidSpecies(in.Species) {
		return nil, fmt.Errorf("%w: unknown species %q (canonical: %s)", ErrInvalidArguments, in.Species, strings.Join(CanonicalSpecies, "|"))
	}
	if strings.TrimSpace(in.Traceparent) == "" {
		return nil, fmt.Errorf("%w: traceparent required", ErrInvalidArguments)
	}

	// No repeats while an unseen species remains (owner ruling 2026-08-07,
	// INVERTING the 2026-07-08 one-species directive, which forced every later
	// Companion to inherit the first one's species):
	//
	//   - an explicit pick of a species the learner ALREADY owns is REFUSED;
	//   - an omitted pick is resolved from the UNSEEN set and written
	//     explicitly, so the blind 0046 column DEFAULT never survives;
	//   - WRAP: once every species is owned both rules relax - the pick stands
	//     and the roll uses the full pool, so acquisition is never blocked.
	owned, err := s.cfg.Repo.OwnerSpeciesSet(ctx, in.TenantID, in.OwnerGCID, in.CompanionID)
	if err != nil {
		return nil, err
	}
	if unseenSpeciesRemain(owned) {
		if in.Species != "" && owned[in.Species] {
			return nil, fmt.Errorf("%w: you already have a %s, and species you have not met yet remain", ErrSpeciesAlreadyOwned, in.Species)
		}
	}
	if in.Species == "" {
		picked, _, _, perr := RollBreed(ExcludeOwnedSpecies(sovereignPool(), owned))
		if perr != nil {
			return nil, fmt.Errorf("growth: pick unseen species: %w", perr)
		}
		in.Species = picked
	}

	row, err := s.cfg.Repo.GetGrowthRow(ctx, in.TenantID, in.CompanionID)
	if err != nil {
		return nil, err
	}
	if row.OwnerGCID != in.OwnerGCID {
		return nil, ErrCompanionNotFound
	}
	if row.GrowthStage != 0 || row.HatchedAt != nil {
		return nil, ErrAlreadyHatched
	}

	now := s.cfg.Clock()
	updated, err := s.cfg.Repo.CommitBornHatched(ctx, BornHatchedTxInput{
		TenantID:    in.TenantID,
		CompanionID: in.CompanionID,
		OwnerGCID:   in.OwnerGCID,
		Species:     in.Species,
		Now:         now,
	})
	if err != nil {
		return nil, err
	}

	env := GrowthEnvelope{
		EventID:        s.cfg.NewID(),
		IdempotencyKey: "hatched:" + in.CompanionID,
		TenantID:       in.TenantID,
		GCID:           in.OwnerGCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    in.Traceparent,
		Tracestate:     in.Tracestate,
		SourceProject:  envelopeSourceProject,
		SourceService:  envelopeSourceService,
		SchemaVersion:  1,
	}
	hatchPayload := map[string]any{
		"companion_id":       in.CompanionID,
		"owner_gcid":         in.OwnerGCID,
		"display_name":       in.DisplayName,
		"specialization":     updated.Specialization,
		"species":            updated.Species,
		"shiny_variant":      updated.ShinyVariant,
		"hatched_at":         now,
		"chora_companion_id": in.CompanionID,
	}
	if err := s.cfg.Outbox.PublishGrowthEvent(ctx, TopicCompanionHatched, hatchPayload, env); err != nil {
		return nil, fmt.Errorf("growth: publish hatched (born): %w", err)
	}

	stageEnv := env
	stageEnv.EventID = s.cfg.NewID()
	stageEnv.IdempotencyKey = "stage_up:" + in.CompanionID + ":baby"
	stageUpPayload := map[string]any{
		"companion_id":                in.CompanionID,
		"owner_gcid":                  in.OwnerGCID,
		"companion_display_name":      in.DisplayName, // CHO-2266: name the Companion in C+ copy
		"stage_from":                  0,
		"stage_to":                    1,
		"stage_from_name":             StageName(0),
		"stage_to_name":               StageName(1),
		"newly_unlocked_tools":        NewlyUnlockedTools(0, 1),
		"newly_revealed_kg_neighbors": []string{},
		"new_llm_tier":                LLMTierForStageAndMana(1, ""),
		"new_memory_mode":             MemoryModeForStage(1),
		"stage_up_at":                 now,
		"chora_companion_id":          in.CompanionID,
	}
	if err := s.cfg.Outbox.PublishGrowthEvent(ctx, TopicCompanionStageUp, stageUpPayload, stageEnv); err != nil {
		return nil, fmt.Errorf("growth: publish stage_up (born): %w", err)
	}

	if err := s.publishSlotUnlocked(ctx, env, in.CompanionID, in.OwnerGCID, 0, 1, now); err != nil {
		return nil, err
	}
	if err := s.unlockPath(ctx, updated, in.Traceparent, in.Tracestate); err != nil {
		return nil, err
	}

	return &InitBornHatchedResponse{
		State:   s.projectState(updated, ""),
		Species: updated.Species,
	}, nil
}
