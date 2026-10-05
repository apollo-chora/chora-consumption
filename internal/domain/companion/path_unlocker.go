// path_unlocker.go — ADR-218 D3: the species-Path unlocker mints the
// cumulative banded Path entries as OWNED grants on every stage transition.
//
// Two-phase since CHO-2039 (CR §8 R6-3): MintThroughStage (compute + mint,
// joinable into the caller's award transaction via the pg ambient-Querier
// ctx) and PublishGranted (post-commit announcements). UnlockThroughStage
// composes both for the hatch-shaped lanes.
//
// Idempotent + self-healing by construction: it always unlocks THROUGH the
// current stage (not just the delta), grant minting is ON-CONFLICT-skip,
// and the growth service re-runs the mint on duplicate award replays — so
// any historically lagging Path heals on Pub/Sub redelivery.
//
// Craft Skills mint Equipped=true (owned = in effect, slot-free); active
// Skills mint unequipped — the learner equips them in the Grimoire (P2).
// Each REAL mint emits chora.consumption.companion.skill_granted.v1 via the
// outbox port (envelope-compliant); replays that mint nothing emit nothing.
package companion

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Loadout/growth event topics (domain-owned mirrors of the adapter events
// package constants — the outbox adapter asserts equality in its tests).
const (
	TopicCompanionSkillGranted   = "chora.consumption.companion.skill_granted.v1"
	TopicCompanionLoadoutChanged = "chora.consumption.companion.loadout_changed.v1"
)

// GrantMint is one grant row to mint (idempotently) on a companion.
type GrantMint struct {
	SkillKey        string
	Equipped        bool
	UnlockedVia     string
	UnlockedAtStage int
}

// GrantWriter is the grants persistence port the unlocker (and the loadout
// handlers) use. Implemented by the pg adapter over companion_skill_grants.
type GrantWriter interface {
	// ListGrantedSkillKeys returns the skill keys already granted
	// (non-deleted) on the companion.
	ListGrantedSkillKeys(ctx context.Context, tenantID, companionID string) ([]string, error)
	// MintGrants inserts grant rows idempotently (ON CONFLICT (companion,
	// skill) DO NOTHING) and returns the keys ACTUALLY inserted.
	MintGrants(ctx context.Context, tenantID, companionID string, mints []GrantMint) ([]string, error)
}

// LoadoutEnvelope mirrors the mandatory event-envelope fields (the outbox
// adapter translates it to the canonical events.Envelope).
type LoadoutEnvelope struct {
	EventID        string
	IdempotencyKey string
	TenantID       string
	GCID           string
	OccurredAt     time.Time
	PublishedAt    time.Time
	Traceparent    string
	Tracestate     string
	SourceProject  string
	SourceService  string
	SchemaVersion  int
}

// LoadoutOutbox publishes loadout-lifecycle events durably (outbox).
type LoadoutOutbox interface {
	PublishLoadoutEvent(ctx context.Context, topic string, payload map[string]any, env LoadoutEnvelope) error
}

// UnlockInput identifies the companion and the stage to unlock through.
type UnlockInput struct {
	TenantID    string
	CompanionID string
	OwnerGCID   string
	Species     string
	Stage       int
	Traceparent string
	Tracestate  string
}

// PathUnlockerConfig wires the unlocker (per feedback_no_inline_config the
// caller resolves adapters; Clock/NewID injected for test determinism).
type PathUnlockerConfig struct {
	Catalog CatalogReader
	Paths   SpeciesPathReader
	Grants  GrantWriter
	Outbox  LoadoutOutbox
	// Bands is the K-per-stage unlock vector; zero value ⇒
	// DefaultStageUnlockCounts.
	Bands [7]int
	Clock func() time.Time
	NewID func() string
}

// PathUnlocker advances species-Path unlocks. Satisfies growth.SkillUnlocker
// via the adapter shim in cmd/server (identical signature shape).
type PathUnlocker struct {
	cfg PathUnlockerConfig
}

// NewPathUnlocker validates dependencies and constructs the unlocker.
func NewPathUnlocker(cfg PathUnlockerConfig) (*PathUnlocker, error) {
	if cfg.Catalog == nil || cfg.Paths == nil || cfg.Grants == nil || cfg.Outbox == nil {
		return nil, errors.New("companion.pathunlocker: catalog/paths/grants/outbox all required")
	}
	if cfg.Bands == ([7]int{}) {
		cfg.Bands = DefaultStageUnlockCounts
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	if cfg.NewID == nil {
		// CHO-2225: event_id is a mandatory UUIDv7 envelope field. There is no
		// safe default — a locally-minted stand-in is a non-UUID that 22P02s
		// every consumer keying idempotency on event_id against a UUID column.
		return nil, errors.New("companion.pathunlocker: id factory required")
	}
	return &PathUnlocker{cfg: cfg}, nil
}

// MintedGrant is one Path grant ACTUALLY minted by MintThroughStage — the
// unit PublishGranted announces. Skill kind is captured at mint time so the
// publish phase needs no second catalogue read.
type MintedGrant struct {
	SkillKey        string
	SkillKind       SkillKind
	Equipped        bool
	UnlockedVia     string
	UnlockedAtStage int
}

// UnlockThroughStage mints every Path entry banded at or below in.Stage
// that the companion does not own yet, then announces each real mint. It is
// the single-call composition used by the hatch-shaped lanes; the AwardExp
// lane calls the two phases separately so the mint joins the award
// transaction (CHO-2039) while the announcements stay post-commit.
func (u *PathUnlocker) UnlockThroughStage(ctx context.Context, in UnlockInput) error {
	minted, err := u.MintThroughStage(ctx, in)
	if err != nil {
		return err
	}
	return u.PublishGranted(ctx, in, minted)
}

// MintThroughStage computes + mints (idempotently) every Path entry banded
// at or below in.Stage that the companion does not own yet, and returns the
// grants ACTUALLY minted. It performs NO event publishing — callers run
// PublishGranted after the minting transaction has committed. When ctx
// carries an ambient repo transaction (the growth award hook) every read +
// mint here joins it, making the unlock atomic with the award.
func (u *PathUnlocker) MintThroughStage(ctx context.Context, in UnlockInput) ([]MintedGrant, error) {
	n := CumulativeUnlocksThroughStage(in.Stage, u.cfg.Bands)
	if n <= 0 {
		return nil, nil
	}
	path, err := u.cfg.Paths.ActivePathForSpecies(ctx, in.Species)
	if err != nil {
		return nil, fmt.Errorf("companion.pathunlocker: load %q path: %w", in.Species, err)
	}
	if n > len(path.Entries) {
		n = len(path.Entries)
	}
	catalogue, err := u.cfg.Catalog.ListCatalogue(ctx)
	if err != nil {
		return nil, fmt.Errorf("companion.pathunlocker: load catalogue: %w", err)
	}
	grantedKeys, err := u.cfg.Grants.ListGrantedSkillKeys(ctx, in.TenantID, in.CompanionID)
	if err != nil {
		return nil, fmt.Errorf("companion.pathunlocker: list grants: %w", err)
	}
	granted := make(map[string]bool, len(grantedKeys))
	for _, k := range grantedKeys {
		granted[k] = true
	}

	var mints []GrantMint
	mintMeta := make(map[string]MintedGrant)
	for i := 0; i < n; i++ {
		key := path.Entries[i]
		if granted[key] {
			continue
		}
		entry, ok := catalogue[key]
		if !ok {
			// Seed corruption — the Path names a Skill the catalogue does
			// not carry. Fail loud (D3), never mint blind.
			return nil, fmt.Errorf("%w: %q at position %d of the %s path", ErrPathUnknownSkill, key, i+1, in.Species)
		}
		unlockStage := UnlockStageForPosition(i+1, u.cfg.Bands)
		mint := GrantMint{
			SkillKey: key,
			// Craft Skills are always in effect (slot-free, owned = equipped).
			// ADR-228 D3: the st1 band (K=1) is the EXP-gated hatch's first
			// Skill (progress_mirror) — minted AUTO-EQUIPPED, since st1 carries
			// 2 slots and the award must arrive usable, not a tease. Every later
			// active band mints UNEQUIPPED: the equip/swap choice is the standing
			// learner agency (the real 4-into-3 pressure lands at the awakening).
			Equipped:        entry.SkillKind == SkillKindCraft || unlockStage == 1,
			UnlockedVia:     "species_path",
			UnlockedAtStage: unlockStage,
		}
		mints = append(mints, mint)
		mintMeta[key] = MintedGrant{
			SkillKey:        key,
			SkillKind:       entry.SkillKind,
			Equipped:        mint.Equipped,
			UnlockedVia:     mint.UnlockedVia,
			UnlockedAtStage: mint.UnlockedAtStage,
		}
	}
	if len(mints) == 0 {
		return nil, nil
	}

	inserted, err := u.cfg.Grants.MintGrants(ctx, in.TenantID, in.CompanionID, mints)
	if err != nil {
		return nil, fmt.Errorf("companion.pathunlocker: mint grants: %w", err)
	}
	out := make([]MintedGrant, 0, len(inserted))
	for _, key := range inserted {
		out = append(out, mintMeta[key])
	}
	return out, nil
}

// PublishGranted emits chora.consumption.companion.skill_granted.v1 for each
// minted grant. MUST run only after the minting transaction committed — a
// durable outbox row must never announce a grant that can still roll back.
// Idempotency-keyed per (companion, skill), so replayed announcements dedupe
// downstream.
func (u *PathUnlocker) PublishGranted(ctx context.Context, in UnlockInput, minted []MintedGrant) error {
	if len(minted) == 0 {
		return nil
	}
	now := u.cfg.Clock()
	for _, g := range minted {
		env := LoadoutEnvelope{
			EventID:        u.cfg.NewID(),
			IdempotencyKey: "skill_granted:" + in.CompanionID + ":" + g.SkillKey,
			TenantID:       in.TenantID,
			GCID:           in.OwnerGCID,
			OccurredAt:     now,
			PublishedAt:    now,
			Traceparent:    in.Traceparent,
			Tracestate:     in.Tracestate,
			SourceProject:  "chora-content",
			SourceService:  "chora-consumption",
			SchemaVersion:  1,
		}
		payload := map[string]any{
			"companion_id":              in.CompanionID,
			"owner_gcid":                in.OwnerGCID,
			"skill_key":                 g.SkillKey,
			"skill_kind":                string(g.SkillKind),
			"unlocked_via":              g.UnlockedVia,
			"unlocked_at_stage":         g.UnlockedAtStage,
			"equipped":                  g.Equipped,
			"granted_at":                now,
			"chora_companion_id":        in.CompanionID,
			"chora_companion_skill_key": g.SkillKey,
		}
		if err := u.cfg.Outbox.PublishLoadoutEvent(ctx, TopicCompanionSkillGranted, payload, env); err != nil {
			return fmt.Errorf("companion.pathunlocker: publish skill_granted for %q: %w", g.SkillKey, err)
		}
	}
	return nil
}
