// service.go — domain service for the ADR-149 Companion Growth axis.
//
// The Service is the single entry point used by the gRPC server, the REST
// handlers, and the cross-domain subscribers. It composes:
//
//   - Repository — persistence (companion_instances + companion_growth_events
//   - companion_growth_daily_counters)
//   - OutboxPort — durable event publishing (6 ADR-149 topics)
//   - BreedDistributionProvider — egg SKU breed_distribution lookup
//   - Curve helpers (this package) — stage thresholds, daily caps, etc.
//
// Per hexagonal SKILL: this file has NO infra imports. crypto/rand is used
// for the breed roll only (pure math; deterministic seam via the breed.go
// RollBreedWithRand helper).
package growth

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Topic constants — ADR-149 §"Topic:" docstrings. Kept in one place so all
// service methods publish to the same canonical names.
const (
	TopicCompanionEggPurchased     = "chora.consumption.companion.egg_purchased.v1"
	TopicCompanionBreedRevealed    = "chora.consumption.companion.breed_revealed.v1"
	TopicCompanionHatched          = "chora.consumption.companion.hatched.v1"
	TopicCompanionExpAwarded       = "chora.consumption.companion.exp_awarded.v1"
	TopicCompanionStageUp          = "chora.consumption.companion.stage_up.v1"
	TopicCompanionSourceRevelation = "chora.consumption.companion.source_revelation.v1"
	// TopicCompanionSkillSlotUnlocked announces the slot allowance change on
	// every stage transition (ADR-218 D2: slots = stage + 1). Defined since
	// 0006-era contracts; FIRST emitted by CHO-2012 P0.
	TopicCompanionSkillSlotUnlocked = "chora.consumption.companion.skill_slot_unlocked.v1"
	// TopicCompanionStirring is the one-shot incubation "your egg is stirring"
	// signal emitted when a Stage-0 egg accrues EXP across the hatch
	// threshold (F-I1.2, CHO-2088, ADR-228). Publish-only in this slice — a
	// notifications subscriber is a follow-up.
	TopicCompanionStirring = "chora.consumption.companion.stirring.v1"
)

// SkillUnlocker advances a companion's species-Path unlocks after a stage
// transition (ADR-218 D3: at each stage-up the next K Path entries become
// owned grants) in two phases, so the grant mint composes into the SAME
// database transaction as the EXP award (CHO-2039 / CR §8 R6-3) while the
// skill_granted publishes stay after commit:
//
//   - MintThroughStage idempotently mints every Path entry banded at or
//     below in.Stage and returns the grants ACTUALLY minted. When ctx
//     carries the repository's ambient transaction (the AwardExpTx
//     PostAwardInTx hook) the mints join that transaction — award and
//     grants commit or roll back together, so a companion can never stage
//     up grantless (the CHO-2032 half-grown class). Outside a hook it
//     runs in its own transaction (the hatch-shaped lanes).
//   - PublishGranted announces the minted grants (skill_granted.v1). It
//     MUST run only after the minting transaction committed — outbox rows
//     must never describe grants that can still roll back.
//
// MintThroughStage MUST stay idempotent — the service re-runs it on
// duplicate award replays so any historical Path lag heals on redelivery.
type SkillUnlocker interface {
	MintThroughStage(ctx context.Context, in UnlockThroughStageInput) ([]MintedPathGrant, error)
	PublishGranted(ctx context.Context, in UnlockThroughStageInput, minted []MintedPathGrant) error
}

// MintedPathGrant is one species-Path grant actually minted by
// MintThroughStage — the unit PublishGranted announces post-commit.
type MintedPathGrant struct {
	SkillKey        string
	SkillKind       string
	Equipped        bool
	UnlockedVia     string
	UnlockedAtStage int
}

// UnlockThroughStageInput identifies the companion + the stage to unlock
// through (cumulative: every Path entry banded at or below Stage).
type UnlockThroughStageInput struct {
	TenantID    string
	CompanionID string
	OwnerGCID   string
	Species     string
	Stage       int
	Traceparent string
	Tracestate  string
}

// SourceProject + SourceService for envelope provenance. Mirrors the
// existing chora-consumption events package convention.
const (
	envelopeSourceProject = "chora-content"
	envelopeSourceService = "chora-consumption"
)

// Canonical tone + persona enums. Mirror the proto enum values; matches the
// CompanionHatched protobuf payload docstring.
var canonicalTones = map[string]bool{
	"socratic":    true,
	"direct":      true,
	"encouraging": true,
}
var canonicalPersonas = map[string]bool{
	"curious-explorer": true,
	"cert-focused":     true,
	"social-leader":    true,
}

// ServiceConfig wires the Service. Per `feedback_no_inline_config`, callers
// (cmd/server) populate this from env-resolved adapters.
type ServiceConfig struct {
	Repo   Repository
	Outbox OutboxPort
	Dist   BreedDistributionProvider
	// Clock is injected for test determinism; it defaults to time.Now UTC.
	Clock func() time.Time
	// NewID mints the envelope event_id and is REQUIRED — the growth domain
	// avoids importing google/uuid, so the composition root must inject a real
	// UUIDv7 mint (domain.NewUUIDv7). NewService refuses a nil NewID rather
	// than defaulting: event_id is a mandatory UUIDv7 envelope field.
	NewID func() string
	// AhaMomentWindowSeconds is the duration of the Stage-3 Source
	// Revelation preview window. Defaults to DefaultAhaMomentWindowSeconds
	// when 0.
	AhaMomentWindowSeconds int
	// HatchExpThreshold is the incubation EXP a Stage-0 egg must accrue
	// before it is stirring and may hatch (F-I1, ADR-228). cmd/server
	// resolves COMPANION_HATCH_EXP_THRESHOLD (validated 0 < t < 50 at boot);
	// NewService defaults it to DefaultHatchExpThreshold so the hatch gate is
	// never silently disabled (fail-closed).
	HatchExpThreshold int
	// ExpRules resolves per-source EXP rules (ADR-218 D6 — identity
	// ExpRuleResolver via gRPC). nil ⇒ the documented in-code fallback
	// (parity-seeded, behaviour-identical).
	ExpRules ExpRuler
	// OnExpRuleFallback fires when ExpRules FAILS and the award proceeds on
	// the fallback map — the caller logs/traces it loudly (never silent).
	OnExpRuleFallback func(source string, err error)
	// SkillUnlocker advances species-Path unlocks on stage transitions
	// (ADR-218 D3). nil ⇒ Path unlocking not wired (dark until P2 seeds
	// activate; awards still flow).
	SkillUnlocker SkillUnlocker
	// GoalBinding + Concepts back the awakening resonant-concept pick
	// (R3-1, CHO-2013 P1). Both nil ⇒ PickResonantConcept refuses with
	// ErrResonanceNotWired (fail-loud; growth reads still work).
	GoalBinding GoalBinding
	Concepts    ConceptChecker
}

// Service is the ADR-149 growth domain service.
type Service struct {
	cfg ServiceConfig
}

// NewService validates dependencies and constructs the Service.
func NewService(cfg ServiceConfig) (*Service, error) {
	if cfg.Repo == nil {
		return nil, errors.New("growth: repo required")
	}
	if cfg.Outbox == nil {
		return nil, errors.New("growth: outbox required")
	}
	if cfg.Dist == nil {
		return nil, errors.New("growth: breed distribution provider required")
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	if cfg.NewID == nil {
		// event_id is a mandatory UUIDv7 envelope field, so NewID is as
		// load-bearing as Repo/Outbox/Dist. There is no safe default: any
		// locally-minted stand-in is a non-UUID that 22P02s every consumer
		// keying idempotency on event_id against a UUID column (CHO-2225).
		return nil, errors.New("growth: id factory required")
	}
	if cfg.AhaMomentWindowSeconds <= 0 {
		cfg.AhaMomentWindowSeconds = DefaultAhaMomentWindowSeconds
	}
	if cfg.HatchExpThreshold <= 0 {
		// Fail-closed default: an unset threshold must not disable the hatch
		// gate. cmd/server still validates the env override at boot.
		cfg.HatchExpThreshold = DefaultHatchExpThreshold
	}
	return &Service{cfg: cfg}, nil
}

// State is the proto-shaped CompanionGrowthState; the gRPC layer marshals
// this into the generated message type.
type State struct {
	CompanionID string
	GrowthStage int
	StageName   string
	// DisplayName is the learner-committed name (empty pre-hatch) — the
	// FE profile header renders it directly (CHO-2028).
	DisplayName            string
	Species                string
	ShinyVariant           bool
	Rarity                 string
	ExpCurrent             int
	ExpNextThreshold       int
	ExpCumulative          int
	EffectiveLLMTier       string
	EffectiveMaxOutputToks int
	UnlockedTools          []string
	ResonantAtomID         string
	// ResonantConceptID — the awakening resonant-concept pick (R3-1,
	// CHO-2013 P1): ring-radius centre; empty until picked. Replaces the
	// retired per-stage visible_kg_neighbors geometry.
	ResonantConceptID    string
	AhaMomentConsumed    bool
	AhaMomentActiveUntil *time.Time
	// RevealedAt is the breed-roll moment (CHO-2229) — set on a Stage-0 pod
	// once RevealBreed persists the roll; nil until then (and on born-hatched
	// rows, which never walk the ceremony). The FE ceremony resumes past the
	// crack step when this is set.
	RevealedAt    *time.Time
	HatchedAt     *time.Time
	LastStageUpAt *time.Time
}

// projectState converts a persisted row into a State. Effective output-token
// max comes from the table in ADR-149 §"The 7 stages".
func (s *Service) projectState(row *CompanionGrowthRow, manaTier string) State {
	stage := row.GrowthStage
	llmTier := LLMTierForStageAndMana(stage, manaTier)
	thr := NextThreshold(stage)
	// CHO-2089 (ADR-228 D2): a Stage-0 egg's "next stage" is the hatch, which
	// is threshold-gated — surface the configured gate as the read's
	// denominator (FE warming bar). The curve's NextThreshold(0) stays 0 so
	// stage-up arithmetic can never auto-hatch past the ceremony.
	if stage == StageEgg {
		thr = s.cfg.HatchExpThreshold
	}
	cum := row.GrowthExp
	var current int
	if stage <= 1 {
		current = cum
	} else {
		// EXP into the current stage = cum - lower_threshold
		lower := stageThresholds[stage]
		current = cum - lower
		if current < 0 {
			current = 0
		}
	}
	st := State{
		CompanionID:            row.CompanionID,
		GrowthStage:            stage,
		StageName:              StageName(stage),
		DisplayName:            row.DisplayName,
		Species:                row.Species,
		ShinyVariant:           row.ShinyVariant,
		Rarity:                 row.SpeciesRarity,
		ExpCurrent:             current,
		ExpNextThreshold:       thr,
		ExpCumulative:          cum,
		EffectiveLLMTier:       llmTier,
		EffectiveMaxOutputToks: maxOutputTokensForStageAndTier(stage, llmTier),
		UnlockedTools:          UnlockedToolsForStage(stage),
		ResonantAtomID:         row.ResonantAtom,
		ResonantConceptID:      row.ResonantConceptID,
		AhaMomentConsumed:      row.AhaMomentConsumed,
		AhaMomentActiveUntil:   row.AhaMomentActiveUntil,
		RevealedAt:             row.RevealedAt,
		HatchedAt:              row.HatchedAt,
		LastStageUpAt:          row.LastStageUpAt,
	}
	return st
}

// maxOutputTokensForStageAndTier — the effective max output tokens at this
// stage × tier intersection. Approximation of ADR-149 §"The 7 stages" LLM
// column (smallest of the stage cap and the tier cap).
func maxOutputTokensForStageAndTier(stage int, llmTier string) int {
	stageCap := []int{100, 600, 800, 1000, 1200, 1400, 2000}[clampStage(stage)]
	tierCap := 0
	switch llmTier {
	case "flash-lite":
		tierCap = 1200
	case "flash":
		tierCap = 1600
	case "flash-reasoning":
		tierCap = 1800
	case "pro":
		tierCap = 2000
	default:
		tierCap = 1000
	}
	if stageCap < tierCap {
		return stageCap
	}
	return tierCap
}

func clampStage(stage int) int {
	if stage < 0 {
		return 0
	}
	if stage > 6 {
		return 6
	}
	return stage
}

// GetCompanionGrowth returns the current state for a Companion. mana_tier
// optionally bounds the effective LLM tier; pass "" to let the cached value
// stand.
func (s *Service) GetCompanionGrowth(ctx context.Context, tenantID, companionID, callerGCID string) (*State, error) {
	row, err := s.cfg.Repo.GetGrowthRow(ctx, tenantID, companionID)
	if err != nil {
		return nil, err
	}
	if row.OwnerGCID != callerGCID {
		return nil, ErrCompanionNotFound
	}
	// Use cached LLM tier when present; the runtime re-resolves with the
	// live mana tier at call time.
	manaTier := "basic"
	if row.EffectiveLLMTierCached != "" {
		// Decode the cached tier → mana tier hint. Conservative: default to
		// basic so the projected state never overshoots the caller's plan.
		manaTier = ""
	}
	st := s.projectState(row, manaTier)
	if row.EffectiveLLMTierCached != "" {
		st.EffectiveLLMTier = row.EffectiveLLMTierCached
	}
	return &st, nil
}

// AwardExpInput is the AwardExp request shape.
type AwardExpInput struct {
	TenantID        string
	CompanionID     string
	OwnerGCID       string
	Source          string
	RequestedDelta  int
	IdempotencyKey  string
	SourceEventID   string
	SourceTopic     string
	SourceSessionID string
	SourceTurnSeq   int
	ManaTier        string
	Traceparent     string
	Tracestate      string
}

// AwardExpResponse is the AwardExp response shape.
type AwardExpResponse struct {
	Row                       *CompanionGrowthRow
	ClampedDelta              int
	DailyCapHit               bool
	Duplicate                 bool
	StageUpTriggered          bool
	SourceRevelationTriggered bool
	State                     State
	NewlyUnlockedTools        []string
	NewMemoryMode             string
	EffectiveLLMTier          string
	// Skipped=true means the resolved rule disabled the source (ADR-218
	// D6 editor knob): nothing was written and no events were emitted.
	Skipped    bool
	SkipReason string
}

// AwardExp applies an EXP increment + emits exp_awarded + stage_up (and,
// on any stage transition, skill_slot_unlocked) events, then advances the
// species Path via the SkillUnlocker.
//
// The per-source rule (value / daily cap / enabled) is resolved through the
// ExpRuler port (ADR-218 D6); RequestedDelta 0 means "use the resolved
// value" while an explicit positive delta (atom_session incorrect=1,
// admin_grant) is honoured as-is — both are clamped by the RESOLVED cap.
// Server-side daily cap enforcement; idempotent on
// (companion_id, source, idempotency_key).
func (s *Service) AwardExp(ctx context.Context, in AwardExpInput) (*AwardExpResponse, error) {
	if !IsValidSource(in.Source) {
		return nil, fmt.Errorf("%w: source %q", ErrInvalidSource, in.Source)
	}
	if in.RequestedDelta < 0 {
		return nil, fmt.Errorf("%w: requested_delta must be >= 0 (got %d; 0 = resolved default)", ErrInvalidArguments, in.RequestedDelta)
	}
	if strings.TrimSpace(in.TenantID) == "" || strings.TrimSpace(in.CompanionID) == "" || strings.TrimSpace(in.IdempotencyKey) == "" {
		return nil, fmt.Errorf("%w: tenant/companion/idempotency required", ErrInvalidArguments)
	}
	if strings.TrimSpace(in.Traceparent) == "" {
		return nil, fmt.Errorf("%w: traceparent required", ErrInvalidArguments)
	}

	rule := resolveExpRuleOrFallback(ctx, s.cfg.ExpRules, in.TenantID, in.Source, s.cfg.OnExpRuleFallback)
	if !rule.Enabled {
		// Editor-disabled source: award nothing, write nothing, emit
		// nothing — surface the current state so callers stay total.
		row, err := s.cfg.Repo.GetGrowthRow(ctx, in.TenantID, in.CompanionID)
		if err != nil {
			return nil, err
		}
		return &AwardExpResponse{
			Row:        row,
			State:      s.projectState(row, in.ManaTier),
			Skipped:    true,
			SkipReason: "source_disabled",
		}, nil
	}
	// CHO-2239 (owner decision 2026-07-16 #4, mechanism (b)): a refresher
	// re-clear never warms an UNHATCHED pod — re-warming retention is
	// maintenance, not incubation drive. Without this, the client-supplied
	// rung on the answers door lets re-answering mastered sets farm up to
	// 12 warmth/day, compressing the intended ~4 dose-day hatch to ~2.
	// Fresh clears (campaign_rung_cleared) keep warming the pod, and
	// refreshers pay normally from stage 1. Reads the row pre-transaction:
	// a hatch racing this check costs at most one 3-EXP tick at the
	// boundary, which the economy tolerates.
	if in.Source == SourceCampaignRungRefreshed {
		row, err := s.cfg.Repo.GetGrowthRow(ctx, in.TenantID, in.CompanionID)
		if err != nil {
			return nil, err
		}
		if row.GrowthStage == 0 && row.HatchedAt == nil {
			return &AwardExpResponse{
				Row:        row,
				State:      s.projectState(row, in.ManaTier),
				Skipped:    true,
				SkipReason: "unhatched_refresher",
			}, nil
		}
	}

	effectiveDelta := in.RequestedDelta
	if effectiveDelta == 0 {
		effectiveDelta = rule.Value
	}
	if effectiveDelta <= 0 {
		return nil, fmt.Errorf("%w: source %q resolves to 0 EXP — explicit requested_delta required", ErrInvalidArguments, in.Source)
	}

	now := s.cfg.Clock()
	txIn := AwardExpTxInput{
		TenantID:        in.TenantID,
		CompanionID:     in.CompanionID,
		OwnerGCID:       in.OwnerGCID,
		Source:          in.Source,
		RequestedDelta:  effectiveDelta,
		IdempotencyKey:  in.IdempotencyKey,
		SourceEventID:   in.SourceEventID,
		SourceTopic:     in.SourceTopic,
		SourceSessionID: in.SourceSessionID,
		SourceTurnSeq:   in.SourceTurnSeq,
		Now:             now,
		ManaTier:        in.ManaTier,
		DailyCap:        rule.DailyCap,
	}

	// CHO-2039 (CR §8 R6-3): the species-Path mint runs INSIDE the award
	// transaction via PostAwardInTx, so a stage-up can never commit without
	// its grants (the CHO-2032 half-grown class). The duplicate-replay
	// repair lane — rows whose Path lags their stage — rides the same hook
	// and is therefore atomic with the replay transaction too. Only the
	// idempotency-keyed skill_granted publishes stay post-commit (below),
	// preserving the outbox ordering exactly.
	var (
		minted      []MintedPathGrant
		mintedInput UnlockThroughStageInput
		hookRan     bool
	)
	if s.cfg.SkillUnlocker != nil {
		txIn.PostAwardInTx = func(txCtx context.Context, txOut *AwardExpTxOutput) error {
			hookRan = true
			if txOut == nil || txOut.Row == nil || txOut.Row.GrowthStage < 1 {
				return nil
			}
			// Same policy as the pre-atomic flow: unlock on a fresh
			// stage-up and on every duplicate replay (self-heal lane).
			if !txOut.TriggeredStageUp && !txOut.Duplicate {
				return nil
			}
			uin := unlockInputForRow(txOut.Row, in.Traceparent, in.Tracestate)
			m, uerr := s.cfg.SkillUnlocker.MintThroughStage(txCtx, uin)
			if uerr != nil {
				return fmt.Errorf("growth: species-path unlock through stage %d: %w", txOut.Row.GrowthStage, uerr)
			}
			minted, mintedInput = m, uin
			return nil
		}
	}

	out, err := s.cfg.Repo.AwardExpTx(ctx, txIn)
	if err != nil {
		return nil, err
	}
	if txIn.PostAwardInTx != nil && !hookRan {
		// Fail-loud contract guard: a Repository that skips the
		// in-transaction hook silently reintroduces the half-grown window.
		return nil, fmt.Errorf("growth: repository did not run PostAwardInTx — species-path unlock cannot be atomic with the award (CHO-2039)")
	}
	resp := &AwardExpResponse{
		Row:                out.Row,
		ClampedDelta:       out.ClampedDelta,
		DailyCapHit:        out.DailyCapHit,
		Duplicate:          out.Duplicate,
		StageUpTriggered:   out.TriggeredStageUp,
		State:              s.projectState(out.Row, in.ManaTier),
		NewlyUnlockedTools: out.NewlyUnlockedTools,
		NewMemoryMode:      out.NewMemoryMode,
		EffectiveLLMTier:   out.EffectiveLLMTier,
	}
	if out.Duplicate {
		// Redelivery replay: no growth events. The repair lane already
		// minted any lagging grants INSIDE the replay transaction
		// (PostAwardInTx above) — announce them now that it committed.
		// Constraint (unchanged by CHO-2039, inherited from the
		// ON-CONFLICT-skip mint): a publish failure here NACKs loudly, but
		// a later replay re-mints nothing for already-committed grants and
		// therefore cannot re-announce them — the grant ROWS are the
		// source of truth (Grimoire reads companion_skill_grants directly);
		// skill_granted is downstream fan-out only.
		if err := s.publishMintedGrants(ctx, mintedInput, minted); err != nil {
			return nil, err
		}
		return resp, nil
	}

	env := s.envelope(in.TenantID, in.OwnerGCID, in.Traceparent, in.Tracestate, in.IdempotencyKey, now)

	// Emit exp_awarded.
	expPayload := map[string]any{
		"companion_id":              in.CompanionID,
		"owner_gcid":                in.OwnerGCID,
		"exp_delta":                 out.ClampedDelta,
		"exp_total_after":           out.Row.GrowthExp,
		"source":                    in.Source,
		"source_event_id":           in.SourceEventID,
		"source_topic":              in.SourceTopic,
		"source_session_id":         in.SourceSessionID,
		"source_turn_seq":           in.SourceTurnSeq,
		"daily_cap_hit":             out.DailyCapHit,
		"triggers_stage_up":         out.TriggeredStageUp,
		"awarded_at":                now,
		"chora_companion_id":        in.CompanionID,
		"chora_growth_source":       in.Source,
		"chora_growth_stage_before": out.PreviousStage,
		"chora_growth_stage_after":  out.NewStage,
		"chora_exp_delta":           out.ClampedDelta,
		"chora_daily_cap_hit":       out.DailyCapHit,
		"chora_duplicate":           false,
	}
	if err := s.cfg.Outbox.PublishGrowthEvent(ctx, TopicCompanionExpAwarded, expPayload, env); err != nil {
		return resp, fmt.Errorf("growth: publish exp_awarded: %w", err)
	}

	// F-I1.2 (ADR-228 incubation): a Stage-0 egg that just accrued EXP across
	// the hatch threshold is now "stirring" — emit the one-shot signal. The
	// crossing is derived here from prevExp/newExp (no persisted flag); the
	// strict boundary guarantees exactly one award stirs, and duplicates
	// short-circuit above, so this can never double-fire.
	prevExp := out.Row.GrowthExp - out.ClampedDelta
	if CrossesHatchThreshold(out.PreviousStage, prevExp, out.Row.GrowthExp, s.cfg.HatchExpThreshold) {
		stirEnv := env
		stirEnv.EventID = s.cfg.NewID()
		stirEnv.IdempotencyKey = "stirring:" + in.CompanionID
		stirPayload := map[string]any{
			"companion_id":       in.CompanionID,
			"owner_gcid":         in.OwnerGCID,
			"growth_exp":         out.Row.GrowthExp,
			"hatch_threshold":    s.cfg.HatchExpThreshold,
			"stirred_at":         now,
			"chora_companion_id": in.CompanionID,
		}
		if err := s.cfg.Outbox.PublishGrowthEvent(ctx, TopicCompanionStirring, stirPayload, stirEnv); err != nil {
			return resp, fmt.Errorf("growth: publish stirring: %w", err)
		}
	}

	// Emit stage_up if applicable.
	if out.TriggeredStageUp {
		newLLM := LLMTierForStageAndMana(out.NewStage, in.ManaTier)
		stageUpPayload := map[string]any{
			"companion_id":                in.CompanionID,
			"owner_gcid":                  in.OwnerGCID,
			"companion_display_name":      out.Row.DisplayName, // CHO-2266: name the Companion in C+ copy
			"stage_from":                  out.PreviousStage,
			"stage_to":                    out.NewStage,
			"stage_from_name":             StageName(out.PreviousStage),
			"stage_to_name":               StageName(out.NewStage),
			"newly_unlocked_tools":        out.NewlyUnlockedTools,
			"newly_revealed_kg_neighbors": []string{},
			"new_llm_tier":                newLLM,
			"new_memory_mode":             MemoryModeForStage(out.NewStage),
			"stage_up_at":                 now,
			"chora_companion_id":          in.CompanionID,
			"chora_growth_stage_before":   out.PreviousStage,
			"chora_growth_stage_after":    out.NewStage,
		}
		stageUpEnv := env
		stageUpEnv.EventID = s.cfg.NewID()
		stageUpEnv.IdempotencyKey = "stage_up:" + in.CompanionID + ":" + StageName(out.NewStage)
		if err := s.cfg.Outbox.PublishGrowthEvent(ctx, TopicCompanionStageUp, stageUpPayload, stageUpEnv); err != nil {
			return resp, fmt.Errorf("growth: publish stage_up: %w", err)
		}

		// Slot allowance changed with the stage (ADR-218 D2) — first real
		// emission of the defined skill_slot_unlocked topic.
		if err := s.publishSlotUnlocked(ctx, env, in.CompanionID, in.OwnerGCID, out.PreviousStage, out.NewStage, now); err != nil {
			return resp, err
		}

		// Announce the species-Path grants the in-transaction mint
		// produced (ADR-218 D3) — they committed atomically with the
		// award above. Fail-loud: a publish error NACKs the upstream
		// event. Constraint (inherited from the ON-CONFLICT-skip mint,
		// unchanged by CHO-2039): the replay dedupes the award and its
		// re-mint no-ops for already-committed grants, so their
		// announcements are not re-attempted — the grant ROWS are the
		// source of truth (Grimoire reads companion_skill_grants directly);
		// skill_granted is downstream fan-out only. The half-grown STATE
		// window this change closes can no longer occur.
		if err := s.publishMintedGrants(ctx, mintedInput, minted); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

// publishSlotUnlocked emits chora.consumption.companion.skill_slot_unlocked.v1
// for a stage transition. Idempotency key is stage-scoped so redeliveries
// of the same transition dedupe downstream.
func (s *Service) publishSlotUnlocked(ctx context.Context, env GrowthEnvelope, companionID, ownerGCID string, fromStage, toStage int, now time.Time) error {
	slotEnv := env
	slotEnv.EventID = s.cfg.NewID()
	slotEnv.IdempotencyKey = "skill_slot:" + companionID + ":" + StageName(toStage)
	payload := map[string]any{
		"companion_id":       companionID,
		"owner_gcid":         ownerGCID,
		"stage_from":         fromStage,
		"stage_to":           toStage,
		"slots_before":       SlotsForStage(fromStage),
		"slots_after":        SlotsForStage(toStage),
		"unlocked_at":        now,
		"chora_companion_id": companionID,
	}
	if err := s.cfg.Outbox.PublishGrowthEvent(ctx, TopicCompanionSkillSlotUnlocked, payload, slotEnv); err != nil {
		return fmt.Errorf("growth: publish skill_slot_unlocked: %w", err)
	}
	return nil
}

// unlockPath advances the species Path through the row's CURRENT stage via
// the SkillUnlocker port: mint (in the unlocker's own transaction — no
// ambient hook here), then announce. Used by the hatch-shaped lanes
// (HatchEgg / CommitBornHatched) whose stage transition commits in its own
// repo call; the AwardExp lane composes the mint into the award transaction
// via PostAwardInTx instead. No-ops when the unlocker is not wired (dark)
// or the companion is pre-hatch (stage 0 has no species / no Path bands).
func (s *Service) unlockPath(ctx context.Context, row *CompanionGrowthRow, traceparent, tracestate string) error {
	if s.cfg.SkillUnlocker == nil || row == nil || row.GrowthStage < 1 {
		return nil
	}
	uin := unlockInputForRow(row, traceparent, tracestate)
	minted, err := s.cfg.SkillUnlocker.MintThroughStage(ctx, uin)
	if err != nil {
		return fmt.Errorf("growth: species-path unlock through stage %d: %w", row.GrowthStage, err)
	}
	return s.publishMintedGrants(ctx, uin, minted)
}

// publishMintedGrants announces grants minted by a committed
// MintThroughStage run. No-ops when nothing was minted.
func (s *Service) publishMintedGrants(ctx context.Context, uin UnlockThroughStageInput, minted []MintedPathGrant) error {
	if s.cfg.SkillUnlocker == nil || len(minted) == 0 {
		return nil
	}
	if err := s.cfg.SkillUnlocker.PublishGranted(ctx, uin, minted); err != nil {
		return fmt.Errorf("growth: species-path unlock through stage %d: %w", uin.Stage, err)
	}
	return nil
}

// unlockInputForRow projects a growth row into the unlock-port input.
func unlockInputForRow(row *CompanionGrowthRow, traceparent, tracestate string) UnlockThroughStageInput {
	return UnlockThroughStageInput{
		TenantID:    row.TenantID,
		CompanionID: row.CompanionID,
		OwnerGCID:   row.OwnerGCID,
		Species:     row.Species,
		Stage:       row.GrowthStage,
		Traceparent: traceparent,
		Tracestate:  tracestate,
	}
}

// RevealBreedInput is RevealBreed's request shape (CHO-2229).
type RevealBreedInput struct {
	TenantID    string
	CompanionID string
	OwnerGCID   string
	ManaTier    string
	Traceparent string
	Tracestate  string
}

// RevealBreedResponse is RevealBreed's response shape. AlreadyRevealed
// marks an idempotent replay: the persisted roll came back verbatim and no
// breed_revealed was (re-)published.
type RevealBreedResponse struct {
	Row               *CompanionGrowthRow
	State             State
	Species           string
	ShinyVariant      bool
	Rarity            string
	RolledProbability float64
	RevealedAt        time.Time
	AlreadyRevealed   bool
	// AwakeningClass flavours the reveal metaphor (CHO-2235, art brief §2):
	// hatch | wake | power_on, resolved from the persisted species.
	AwakeningClass string
}

// RevealBreed rolls the breed on a stirring Stage-0 pod and PERSISTS the
// roll, so the reveal precedes naming (CHO-2229, ADR-228 Phase 2 — owner
// decision 2026-07-16 #2). The pod-mystery invariant (0032:36-38, CHO-2227)
// holds right up to this call: species comes into existence here and only
// here. Idempotent — a revealed pod returns its persisted roll and never
// re-rolls, so the learner can close the tab mid-ceremony and resume.
// breed_revealed publishes at REVEAL time (it used to fire inside the hatch
// commit); the payload shape is unchanged.
func (s *Service) RevealBreed(ctx context.Context, in RevealBreedInput) (*RevealBreedResponse, error) {
	if strings.TrimSpace(in.TenantID) == "" || strings.TrimSpace(in.CompanionID) == "" || strings.TrimSpace(in.OwnerGCID) == "" {
		return nil, fmt.Errorf("%w: tenant/companion/owner required", ErrInvalidArguments)
	}
	if strings.TrimSpace(in.Traceparent) == "" {
		return nil, fmt.Errorf("%w: traceparent required", ErrInvalidArguments)
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
	// Idempotent replay: the roll already exists — return it verbatim,
	// publish nothing. A re-POST must never re-roll.
	if row.RevealedAt != nil {
		return s.revealResponseFromRow(row, in.ManaTier, true), nil
	}
	// F-I1.3 (ADR-228 incubation): the reveal inherits the hatch gate — the
	// same threshold, the same error — so an un-stirred pod cannot leak its
	// breed early.
	if row.GrowthExp < s.cfg.HatchExpThreshold {
		return nil, fmt.Errorf("%w: has %d EXP, needs %d", ErrNotStirring, row.GrowthExp, s.cfg.HatchExpThreshold)
	}

	dist, err := s.cfg.Dist.Lookup(in.TenantID, row.EggSku)
	if err != nil {
		return nil, fmt.Errorf("growth: lookup distribution: %w", err)
	}

	// No repeats while an unseen species remains (owner ruling 2026-08-07,
	// INVERTING the 2026-07-08 one-species directive). The exclusion happens
	// BEFORE the roll, never after: RollBreed must see the EFFECTIVE pool so
	// the probability it returns is the weight it actually rolled against.
	//
	// The retired override rolled first and then overwrote `species`, leaving
	// rolled_probability carrying the DISCARDED roll's weight - companion_growth_
	// events then paired a species with an unrelated probability and skewed the
	// IMDA D2 (ADR-149) chi-square fairness audit. Rolling the excluded pool is
	// what closes that: there is no second value to disagree with.
	//
	// WRAP: ExcludeOwnedSpecies returns the declared distribution unchanged once
	// every offered species is owned, so a hatch is never blocked.
	//
	// The DECLARED distribution is validated FIRST. ExcludeOwnedSpecies
	// renormalises its survivors to 100, which would otherwise launder a
	// malformed SKU (a sum-20 table becomes a valid-looking sum-100 pool) and
	// hide an upstream misconfiguration behind a successful hatch - while the
	// odds published to the learner still read the broken declared weights.
	if err := ValidateBreedDistribution(dist); err != nil {
		return nil, fmt.Errorf("growth: declared distribution for %q: %w", row.EggSku, err)
	}
	owned, err := s.cfg.Repo.OwnerSpeciesSet(ctx, in.TenantID, in.OwnerGCID, in.CompanionID)
	if err != nil {
		return nil, fmt.Errorf("growth: owner species set: %w", err)
	}
	species, rarity, prob, err := RollBreed(ExcludeOwnedSpecies(dist, owned))
	if err != nil {
		return nil, fmt.Errorf("growth: roll breed: %w", err)
	}

	rosterCount, err := s.cfg.Repo.CountCompanionsOfSpecies(ctx, in.TenantID, in.OwnerGCID, species, in.CompanionID)
	if err != nil {
		return nil, fmt.Errorf("growth: shiny lookup: %w", err)
	}
	shiny := rosterCount > 0

	now := s.cfg.Clock()
	out, err := s.cfg.Repo.CommitReveal(ctx, RevealTxInput{
		TenantID:          in.TenantID,
		CompanionID:       in.CompanionID,
		OwnerGCID:         in.OwnerGCID,
		Species:           species,
		ShinyVariant:      shiny,
		SpeciesRarity:     rarity,
		RolledProbability: prob,
		Now:               now,
	})
	if err != nil {
		return nil, err
	}
	if out.Duplicate {
		// Race loser: a concurrent reveal held the row lock first. Its
		// persisted roll is the truth — adopt it, publish nothing (the
		// winner already announced it).
		return s.revealResponseFromRow(out.Row, in.ManaTier, true), nil
	}

	env := s.envelope(in.TenantID, in.OwnerGCID, in.Traceparent, in.Tracestate, "breed_revealed:"+in.CompanionID, now)
	payload := map[string]any{
		"companion_id":       in.CompanionID,
		"owner_gcid":         in.OwnerGCID,
		"species":            species,
		"shiny_variant":      shiny,
		"rarity":             rarity,
		"egg_sku":            row.EggSku,
		"rolled_probability": prob,
		"revealed_at":        now,
		"chora_companion_id": in.CompanionID,
	}
	if err := s.cfg.Outbox.PublishGrowthEvent(ctx, TopicCompanionBreedRevealed, payload, env); err != nil {
		return nil, fmt.Errorf("growth: publish breed_revealed: %w", err)
	}

	return s.revealResponseFromRow(out.Row, in.ManaTier, false), nil
}

// revealResponseFromRow projects a revealed row into the response shape.
func (s *Service) revealResponseFromRow(row *CompanionGrowthRow, manaTier string, replay bool) *RevealBreedResponse {
	resp := &RevealBreedResponse{
		Row:               row,
		State:             s.projectState(row, manaTier),
		Species:           row.Species,
		ShinyVariant:      row.ShinyVariant,
		Rarity:            row.SpeciesRarity,
		RolledProbability: row.RolledProb,
		AlreadyRevealed:   replay,
		AwakeningClass:    AwakeningClass(row.Species),
	}
	if row.RevealedAt != nil {
		resp.RevealedAt = *row.RevealedAt
	}
	return resp
}

// HatchEggInput is HatchEgg's request shape.
type HatchEggInput struct {
	TenantID       string
	CompanionID    string
	OwnerGCID      string
	DisplayName    string
	Tone           string
	LearnerPersona string
	ResonantAtomID string
	ManaTier       string
	Traceparent    string
	Tracestate     string
}

// HatchEggResponse is HatchEgg's response shape.
type HatchEggResponse struct {
	Row               *CompanionGrowthRow
	State             State
	Species           string
	ShinyVariant      bool
	Rarity            string
	RolledProbability float64
}

// HatchEgg commits the row transition Stage 0 → 1. Commit-only since
// CHO-2229 (ADR-228 Phase 2): the breed was rolled + persisted by
// RevealBreed — the ceremony reveals BEFORE naming — so this lane requires
// a revealed pod, reads the persisted roll, and publishes hatched +
// stage_up only (breed_revealed fired at reveal time).
func (s *Service) HatchEgg(ctx context.Context, in HatchEggInput) (*HatchEggResponse, error) {
	if strings.TrimSpace(in.TenantID) == "" || strings.TrimSpace(in.CompanionID) == "" || strings.TrimSpace(in.OwnerGCID) == "" {
		return nil, fmt.Errorf("%w: tenant/companion/owner required", ErrInvalidArguments)
	}
	if strings.TrimSpace(in.DisplayName) == "" {
		return nil, fmt.Errorf("%w: display_name required", ErrInvalidArguments)
	}
	if !canonicalTones[in.Tone] {
		return nil, fmt.Errorf("%w: invalid tone %q", ErrInvalidArguments, in.Tone)
	}
	if !canonicalPersonas[in.LearnerPersona] {
		return nil, fmt.Errorf("%w: invalid learner_persona %q", ErrInvalidArguments, in.LearnerPersona)
	}
	// CHO-2028: resonant atom is OPTIONAL at hatch (mig 0038 seeds NULL,
	// "resolved post-hatch"). When set it must be a canonical UUID —
	// companion_instances.resonant_atom_id is a UUID column, so rejecting
	// malformed values here surfaces 422 instead of a pg 22P02 500.
	in.ResonantAtomID = strings.TrimSpace(in.ResonantAtomID)
	if in.ResonantAtomID != "" && !isCanonicalUUID(in.ResonantAtomID) {
		return nil, fmt.Errorf("%w: resonant_atom_id must be a UUID when set", ErrInvalidArguments)
	}
	if strings.TrimSpace(in.Traceparent) == "" {
		return nil, fmt.Errorf("%w: traceparent required", ErrInvalidArguments)
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
	// F-I1.3 (ADR-228 incubation): an egg may only hatch once it is stirring
	// — it has incubated up to the hatch EXP threshold. NewService guarantees
	// a positive threshold (fail-closed), so this gate is always active.
	if row.GrowthExp < s.cfg.HatchExpThreshold {
		return nil, fmt.Errorf("%w: has %d EXP, needs %d", ErrNotStirring, row.GrowthExp, s.cfg.HatchExpThreshold)
	}
	// CHO-2229: commit-only — the roll must already be persisted. The
	// ceremony calls RevealBreed first; there is no roll inside the commit,
	// so the species the learner named against is the species they get.
	if row.RevealedAt == nil {
		return nil, fmt.Errorf("%w: pod %s has no persisted roll", ErrNotRevealed, in.CompanionID)
	}
	species := row.Species
	shiny := row.ShinyVariant
	rarity := row.SpeciesRarity
	prob := row.RolledProb

	now := s.cfg.Clock()
	updated, err := s.cfg.Repo.CommitHatch(ctx, HatchTxInput{
		TenantID:       in.TenantID,
		CompanionID:    in.CompanionID,
		OwnerGCID:      in.OwnerGCID,
		DisplayName:    in.DisplayName,
		Tone:           in.Tone,
		LearnerPersona: in.LearnerPersona,
		ResonantAtomID: in.ResonantAtomID,
		Now:            now,
	})
	if err != nil {
		return nil, err
	}

	specialization := deriveSpecialization(in.ResonantAtomID)

	// Publish 2 events in order: hatched → stage_up. breed_revealed fired
	// at reveal time (RevealBreed) — never here.
	env := s.envelope(in.TenantID, in.OwnerGCID, in.Traceparent, in.Tracestate, "hatched:"+in.CompanionID, now)
	hatchEnv := env
	hatchPayload := map[string]any{
		"companion_id":       in.CompanionID,
		"owner_gcid":         in.OwnerGCID,
		"display_name":       in.DisplayName,
		"tone":               in.Tone,
		"learner_persona":    in.LearnerPersona,
		"resonant_atom_id":   in.ResonantAtomID,
		"specialization":     specialization,
		"species":            species,
		"shiny_variant":      shiny,
		"hatched_at":         now,
		"chora_companion_id": in.CompanionID,
	}
	if err := s.cfg.Outbox.PublishGrowthEvent(ctx, TopicCompanionHatched, hatchPayload, hatchEnv); err != nil {
		return nil, fmt.Errorf("growth: publish hatched: %w", err)
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
		"stage_from_name":             "egg",
		"stage_to_name":               "baby",
		"newly_unlocked_tools":        NewlyUnlockedTools(0, 1),
		"newly_revealed_kg_neighbors": []string{},
		"new_llm_tier":                LLMTierForStageAndMana(1, in.ManaTier),
		"new_memory_mode":             MemoryModeForStage(1),
		"stage_up_at":                 now,
		"chora_companion_id":          in.CompanionID,
	}
	if err := s.cfg.Outbox.PublishGrowthEvent(ctx, TopicCompanionStageUp, stageUpPayload, stageEnv); err != nil {
		return nil, fmt.Errorf("growth: publish stage_up: %w", err)
	}

	// Hatch is the 0→1 stage transition: announce the slot change (1→2 per
	// ADR-218 D2) and run the Path unlocker (band no-op at stage 1 today,
	// but the K bands are editor-tunable).
	if err := s.publishSlotUnlocked(ctx, env, in.CompanionID, in.OwnerGCID, 0, 1, now); err != nil {
		return nil, err
	}
	if err := s.unlockPath(ctx, updated, in.Traceparent, in.Tracestate); err != nil {
		return nil, err
	}

	return &HatchEggResponse{
		Row:               updated,
		State:             s.projectState(updated, in.ManaTier),
		Species:           species,
		ShinyVariant:      shiny,
		Rarity:            rarity,
		RolledProbability: prob,
	}, nil
}

func deriveSpecialization(_ string) string {
	// Iter G.6 wires this to chora-creation atom→cluster lookup; default
	// for now keeps the FE animation copy stable.
	return "general"
}

// isCanonicalUUID reports whether s is a canonical 8-4-4-4-12 hex UUID.
// Local by design — the growth domain avoids importing google/uuid (IDs are
// minted by the injected NewID factory; this is validation only).
func isCanonicalUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// TriggerSourceRevelationInput is the request shape.
type TriggerSourceRevelationInput struct {
	TenantID               string
	CompanionID            string
	OwnerGCID              string
	ManaTier               string
	WindowDurationOverride int
	Traceparent            string
	Tracestate             string
}

// TriggerSourceRevelationResponse is the response shape.
type TriggerSourceRevelationResponse struct {
	Row                   *CompanionGrowthRow
	State                 State
	PreviewLLMTier        string
	WindowExpiresAt       time.Time
	WindowDurationSeconds int
}

// TriggerSourceRevelation opens the 24h Stage-3 Source Revelation preview window.
func (s *Service) TriggerSourceRevelation(ctx context.Context, in TriggerSourceRevelationInput) (*TriggerSourceRevelationResponse, error) {
	if strings.TrimSpace(in.Traceparent) == "" {
		return nil, fmt.Errorf("%w: traceparent required", ErrInvalidArguments)
	}
	row, err := s.cfg.Repo.GetGrowthRow(ctx, in.TenantID, in.CompanionID)
	if err != nil {
		return nil, err
	}
	if row.OwnerGCID != in.OwnerGCID {
		return nil, ErrCompanionNotFound
	}
	if row.GrowthStage < 3 {
		return nil, fmt.Errorf("%w: source revelation requires stage >= 3 (got %d)", ErrInvalidArguments, row.GrowthStage)
	}
	if row.AhaMomentConsumed {
		return nil, ErrAhaMomentConsumed
	}
	manaTier := in.ManaTier
	if manaTier == "" {
		manaTier = "basic"
	}
	previewTier := PreviewLLMTierForAhaMoment(manaTier)
	window := in.WindowDurationOverride
	if window <= 0 {
		window = s.cfg.AhaMomentWindowSeconds
	}
	now := s.cfg.Clock()
	expires := now.Add(time.Duration(window) * time.Second)
	updated, err := s.cfg.Repo.MarkAhaMoment(ctx, AhaMomentInput{
		TenantID:        in.TenantID,
		CompanionID:     in.CompanionID,
		OwnerGCID:       in.OwnerGCID,
		PreviewLLMTier:  previewTier,
		WindowExpiresAt: expires,
	})
	if err != nil {
		return nil, err
	}
	env := s.envelope(in.TenantID, in.OwnerGCID, in.Traceparent, in.Tracestate,
		"source_revelation:"+in.CompanionID, now)
	payload := map[string]any{
		"companion_id":            in.CompanionID,
		"owner_gcid":              in.OwnerGCID,
		"companion_display_name":  row.DisplayName, // CHO-2266: name the Companion in C+ ceremony copy
		"preview_llm_tier":        previewTier,
		"preview_tools":           UnlockedToolsForStage(6),
		"revelation_at":           now,
		"window_expires_at":       expires,
		"window_duration_seconds": window,
		"chora_companion_id":      in.CompanionID,
		"chora_preview_llm_tier":  previewTier,
	}
	if err := s.cfg.Outbox.PublishGrowthEvent(ctx, TopicCompanionSourceRevelation, payload, env); err != nil {
		return nil, fmt.Errorf("growth: publish source_revelation: %w", err)
	}
	return &TriggerSourceRevelationResponse{
		Row:                   updated,
		State:                 s.projectState(updated, in.ManaTier),
		PreviewLLMTier:        previewTier,
		WindowExpiresAt:       expires,
		WindowDurationSeconds: window,
	}, nil
}

// PreviewEggOddsInput is the request shape.
type PreviewEggOddsInput struct {
	TenantID string
	EggSku   string
}

// PreviewEggOddsResponse mirrors the proto response.
type PreviewEggOddsResponse struct {
	EggSku                string
	Odds                  []BreedWeight
	TotalWeight           float64
	DistributionUpdatedAt time.Time
}

// PreviewEggOdds returns the IMDA D2 transparency probability table for an egg SKU.
func (s *Service) PreviewEggOdds(_ context.Context, in PreviewEggOddsInput) (*PreviewEggOddsResponse, error) {
	if strings.TrimSpace(in.EggSku) == "" {
		return nil, fmt.Errorf("%w: egg_sku required", ErrInvalidArguments)
	}
	dist, err := s.cfg.Dist.Lookup(in.TenantID, in.EggSku)
	if err != nil {
		return nil, err
	}
	sorted := make([]BreedWeight, len(dist))
	copy(sorted, dist)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Probability > sorted[j].Probability
	})
	var sum float64
	for _, w := range sorted {
		sum += w.Probability
	}
	return &PreviewEggOddsResponse{
		EggSku:                in.EggSku,
		Odds:                  sorted,
		TotalWeight:           sum,
		DistributionUpdatedAt: s.cfg.Clock(),
	}, nil
}

// ListGrowthEvents returns the paginated EXP ledger.
func (s *Service) ListGrowthEvents(ctx context.Context, in ListGrowthEventsInput) (*ListGrowthEventsOutput, error) {
	if strings.TrimSpace(in.TenantID) == "" || strings.TrimSpace(in.CompanionID) == "" {
		return nil, fmt.Errorf("%w: tenant/companion required", ErrInvalidArguments)
	}
	if in.PageSize <= 0 {
		in.PageSize = 50
	}
	if in.PageSize > 200 {
		in.PageSize = 200
	}
	return s.cfg.Repo.ListGrowthEvents(ctx, in)
}

// ProvisionEgg provisions a Stage-0 companion_instances row + publishes
// chora.consumption.companion.egg_purchased.v1. Called by the egg-purchase
// subscriber on chora.payments.companion_egg_purchase.payment_captured.v1
// (direct-consume, mana-pattern parity).
func (s *Service) ProvisionEgg(ctx context.Context, in ProvisionEggInput) (*CompanionGrowthRow, error) {
	if strings.TrimSpace(in.TenantID) == "" || strings.TrimSpace(in.OwnerGCID) == "" || strings.TrimSpace(in.EggSku) == "" || strings.TrimSpace(in.EggPurchaseID) == "" {
		return nil, fmt.Errorf("%w: tenant/owner/egg_sku/egg_purchase_id required", ErrInvalidArguments)
	}
	if strings.TrimSpace(in.Traceparent) == "" {
		return nil, fmt.Errorf("%w: traceparent required", ErrInvalidArguments)
	}
	now := in.Now
	if now.IsZero() {
		now = s.cfg.Clock()
	}
	in.Now = now
	if in.SoftExpiryAt.IsZero() {
		in.SoftExpiryAt = now.AddDate(0, 0, 30)
	}
	if in.HardExpiryAt.IsZero() {
		in.HardExpiryAt = now.AddDate(0, 0, 60)
	}
	row, err := s.cfg.Repo.ProvisionEgg(ctx, in)
	if err != nil {
		return nil, err
	}
	env := s.envelope(in.TenantID, in.OwnerGCID, in.Traceparent, in.Tracestate,
		"egg_purchased:"+in.EggPurchaseID, now)
	payload := map[string]any{
		"companion_id":            row.CompanionID,
		"owner_gcid":              in.OwnerGCID,
		"egg_sku":                 in.EggSku,
		"source":                  in.EggSource,
		"egg_purchase_id":         in.EggPurchaseID,
		"stripe_session_id":       in.StripeSessionID,
		"amount_cents":            in.AmountCents,
		"currency":                in.Currency,
		"suggested_focal_atom_id": in.SuggestedFocalAtomID,
		"soft_expiry_at":          in.SoftExpiryAt,
		"hard_expiry_at":          in.HardExpiryAt,
		"purchased_at":            now,
		"chora_companion_id":      row.CompanionID,
	}
	if err := s.cfg.Outbox.PublishGrowthEvent(ctx, TopicCompanionEggPurchased, payload, env); err != nil {
		return row, fmt.Errorf("growth: publish egg_purchased: %w", err)
	}
	return row, nil
}

// envelope builds an outbound event envelope. Tests inject a fixed Clock +
// NewID for determinism.
func (s *Service) envelope(tenantID, gcid, traceparent, tracestate, idemKey string, now time.Time) GrowthEnvelope {
	return GrowthEnvelope{
		EventID:        s.cfg.NewID(),
		IdempotencyKey: idemKey,
		TenantID:       tenantID,
		GCID:           gcid,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    traceparent,
		Tracestate:     tracestate,
		SourceProject:  envelopeSourceProject,
		SourceService:  envelopeSourceService,
		SchemaVersion:  1,
	}
}
