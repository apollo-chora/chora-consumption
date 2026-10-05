// state.go — domain types + repository ports for the ADR-149 growth axis.
//
// Repositories live in adapter/repo/{pg,inmem}; the domain owns the contract.
// Per hexagonal SKILL: domain code never imports the repo adapters; the
// adapters depend on these interfaces.
package growth

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors used by the gRPC layer.
var (
	ErrCompanionNotFound = errors.New("growth: companion not found")
	ErrAlreadyHatched    = errors.New("growth: companion already hatched")
	ErrNotInEggStage     = errors.New("growth: companion not at stage 0 (egg)")
	// ErrNotStirring is returned when a Stage-0 egg is asked to hatch before
	// it has accrued the hatch EXP threshold (F-I1.3, ADR-228 incubation).
	// The egg must incubate — accrue EXP up to the threshold — to become
	// "stirring" before the learner can tap to hatch it.
	ErrNotStirring = errors.New("growth: egg is not stirring yet (needs more incubation EXP to hatch)")
	// ErrNotRevealed is returned when HatchEgg is asked to commit a Stage-0
	// pod whose breed has not been revealed yet (CHO-2229, ADR-228 Phase 2:
	// the roll happens at RevealBreed so the reveal precedes naming; hatch
	// is commit-only and reads the persisted roll).
	ErrNotRevealed       = errors.New("growth: breed not revealed yet (reveal precedes the hatch commit)")
	ErrInvalidArguments  = errors.New("growth: invalid arguments")
	ErrAhaMomentConsumed = errors.New("growth: aha moment already consumed")
	ErrInvalidSource     = errors.New("growth: invalid source")
	// ErrSpeciesAlreadyOwned is returned when an explicit species pick names a
	// companion type the learner ALREADY owns while an unseen one is still
	// available. Owner ruling 2026-08-07: a ceremony must not hand back a
	// species the learner already has until the roster wraps. Once every
	// species is owned the pick is allowed again (the wrap rule).
	ErrSpeciesAlreadyOwned = errors.New("growth: you already have this companion type (pick one you have not met yet)")
)

// CompanionGrowthRow is the persisted projection of growth-axis columns on
// companion_instances. The repo layer returns this; the service composes
// CompanionGrowthState (proto-shaped) from it.
type CompanionGrowthRow struct {
	CompanionID   string
	TenantID      string
	OwnerGCID     string
	GrowthStage   int
	Species       string // empty when stage=0
	ShinyVariant  bool
	SpeciesRarity string
	RolledProb    float64
	GrowthExp     int
	ResonantAtom  string // empty when stage=0
	// ResonantConceptID is the awakening resonant-concept pick (R3-1,
	// CHO-2013 P1): the learner-elected ring-radius centre inside the
	// bound Goal's subgraph. Empty until picked; cleared on rebind. It
	// supersedes the retired visible_kg_neighbors geometry (the DB column
	// stays, unread, per the xp_unlock_threshold discipline).
	ResonantConceptID string

	// RevealedAt is the moment the breed roll happened (CHO-2229: rolled at
	// RevealBreed, BEFORE naming/commit). NULL for an unrevealed pod — and
	// species is NULL exactly while RevealedAt is (the pod-mystery invariant,
	// 0032:36-38 + CHO-2227). Also NULL on born-hatched rows, which never
	// walk the reveal ceremony.
	RevealedAt              *time.Time
	HatchedAt               *time.Time
	AhaMomentConsumed       bool
	AhaMomentActiveUntil    *time.Time
	AhaMomentPreviewLLMTier string

	EggSku         string
	EggPurchaseID  string
	EggPurchasedAt *time.Time
	EggSoftExpiry  *time.Time
	EggHardExpiry  *time.Time
	EggSource      string

	EffectiveLLMTierCached string
	LastStageUpAt          *time.Time

	// Hatching-only fields, used for HatchEgg server logic.
	DisplayName    string
	Tone           string
	LearnerPersona string
	Specialization string
}

// GrowthEventRow is a row of companion_growth_events. Append-only.
type GrowthEventRow struct {
	GrowthEventID       string
	TenantID            string
	CompanionID         string
	OwnerGCID           string
	Source              string
	RequestedDelta      int
	AwardedDelta        int
	ExpTotalAfter       int
	DailyCapHit         bool
	TriggeredStageUp    bool
	TriggeredRevelation bool
	IdempotencyKey      string
	SourceEventID       string
	SourceTopic         string
	SourceSessionID     string
	SourceTurnSeq       int
	AwardedAt           time.Time
}

// DailyCounterRow tracks per-(companion, source, day) running totals for
// daily-cap enforcement.
type DailyCounterRow struct {
	TenantID      string
	CompanionID   string
	Source        string
	DayBucket     time.Time // truncated to UTC midnight
	ExpAwarded    int
	EventCount    int
	LastAwardedAt time.Time
}

// Repository is the persistence port for the growth axis. The Postgres
// adapter satisfies it; the in-memory adapter satisfies it for tests.
type Repository interface {
	// GetGrowthRow returns the growth-axis snapshot for a companion.
	// Returns ErrCompanionNotFound on miss.
	GetGrowthRow(ctx context.Context, tenantID, companionID string) (*CompanionGrowthRow, error)

	// AwardExpTx applies an EXP increment atomically:
	//   1. UPSERT companion_growth_daily_counters
	//   2. INSERT companion_growth_events (UNIQUE (companion_id, source,
	//      idempotency_key))
	//   3. UPDATE companion_instances (growth_exp + growth_stage +
	//      last_stage_up_at + effective_llm_tier_cached)
	// Returns the post-award snapshot + a duplicate flag if the
	// (companion_id, source, idempotency_key) tuple already existed.
	AwardExpTx(ctx context.Context, in AwardExpTxInput) (*AwardExpTxOutput, error)

	// CommitReveal persists the breed roll on a stirring Stage-0 pod
	// (CHO-2229: the reveal precedes naming). Sets species / shiny_variant /
	// species_rarity / rolled_probability + revealed_at atomically, guarded
	// by revealed_at IS NULL under row lock — a concurrent second reveal
	// loses the race, MUST NOT overwrite the winner's roll, and returns the
	// persisted roll with Duplicate=true. ErrAlreadyHatched past Stage 0;
	// ErrCompanionNotFound on miss/tenant/owner mismatch.
	CommitReveal(ctx context.Context, in RevealTxInput) (*RevealTxOutput, error)

	// CommitHatch advances a REVEALED Stage-0 row to Stage 1 with the
	// supplied identity bundle. Commit-only since CHO-2229: the breed roll
	// is already persisted by CommitReveal and is never written here.
	// Returns ErrAlreadyHatched if the row is already past Stage 0 and
	// ErrNotRevealed if the pod has no persisted roll.
	CommitHatch(ctx context.Context, in HatchTxInput) (*CompanionGrowthRow, error)

	// SetResonantConcept persists the awakening resonant-concept pick
	// (R3-1, CHO-2013 P1). nil clears it (rebind). Returns the refreshed
	// row; ErrCompanionNotFound on miss.
	SetResonantConcept(ctx context.Context, tenantID, companionID string, conceptID *string) (*CompanionGrowthRow, error)

	// CommitBornHatched advances a freshly-minted stage-0 sovereign row to
	// stage 1 (born hatched, CHO-2013 P1): growth_stage=1, slots=2,
	// hatched_at stamped, species overridden when picked. Returns
	// ErrAlreadyHatched if the row is past stage 0.
	CommitBornHatched(ctx context.Context, in BornHatchedTxInput) (*CompanionGrowthRow, error)

	// MarkAhaMoment sets aha_moment_consumed=true + aha_moment_active_until
	// + preview tier. Single-shot; returns ErrAhaMomentConsumed if already.
	MarkAhaMoment(ctx context.Context, in AhaMomentInput) (*CompanionGrowthRow, error)

	// CountCompanionsOfSpecies counts the caller's non-deleted Companions of
	// the given species (excluding the supplied companionID). Used to detect
	// shiny variants on a fresh hatch.
	CountCompanionsOfSpecies(ctx context.Context, tenantID, ownerGCID, species, excludeCompanionID string) (int, error)

	// OwnerSpeciesSet returns the DISTINCT non-empty species carried by the
	// caller's ACTIVE (deleted_at IS NULL) Companions, excluding
	// excludeCompanionID. An empty map ⇒ every species is still unseen.
	//
	// Owner ruling 2026-08-07 (see the ADR amending the 2026-07-08 one-species
	// directive): this set is subtracted from the egg SKU's breed_distribution
	// by ExcludeOwnedSpecies BEFORE the roll, so a ceremony cannot hand back a
	// companion type the learner already has while an unseen one remains. An
	// unrevealed pod carries no species and therefore contributes nothing.
	OwnerSpeciesSet(ctx context.Context, tenantID, ownerGCID, excludeCompanionID string) (map[string]bool, error)

	// ListGrowthEvents returns a paginated slice of the ledger. pageToken
	// is empty on first call; an empty next_page_token in the response
	// means end-of-results.
	ListGrowthEvents(ctx context.Context, in ListGrowthEventsInput) (*ListGrowthEventsOutput, error)

	// ProvisionEgg inserts a Stage-0 companion_instances row in response to
	// chora.tenancy.companion_egg.payment_succeeded.v1. Idempotent on
	// egg_purchase_id — duplicate webhooks return the existing row.
	ProvisionEgg(ctx context.Context, in ProvisionEggInput) (*CompanionGrowthRow, error)
}

// AwardExpTxInput is the AwardExp transaction shape.
type AwardExpTxInput struct {
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
	Now             time.Time
	ManaTier        string // for effective_llm_tier_cached cache
	// DailyCap is the RESOLVED per-source daily ceiling (ADR-218 D6 —
	// ExpRuler output or its documented fallback); 0 = uncapped. Repos
	// clamp with ClampDeltaWithCap; the in-code fallback map is no longer
	// read inside the transaction.
	DailyCap int
	// PostAwardInTx, when non-nil, MUST be invoked by the Repository INSIDE
	// the same database transaction as the award writes — after they are
	// applied on a fresh award, or after the snapshot read on a duplicate
	// replay — with the would-be output. A non-nil error MUST abort the
	// transaction so every award write rolls back with the hook's writes.
	//
	// CHO-2039 (CR §8 R6-3): the growth service rides this hook to mint the
	// species-Path grants atomically with the award, closing the
	// staged-up-but-grantless window (the CHO-2032 half-grown class).
	// Adapters expose their open transaction through ctx (pg: ambient
	// Querier) so same-DB repositories invoked by the hook join it.
	PostAwardInTx func(ctx context.Context, out *AwardExpTxOutput) error
}

// AwardExpTxOutput captures the post-award projection.
type AwardExpTxOutput struct {
	Row                *CompanionGrowthRow
	GrowthEvent        *GrowthEventRow
	Duplicate          bool
	ClampedDelta       int
	DailyCapHit        bool
	TriggeredStageUp   bool
	PreviousStage      int
	NewStage           int
	NewlyUnlockedTools []string
	NewMemoryMode      string
	EffectiveLLMTier   string
}

// RevealTxInput is the CommitReveal shape — the rolled breed to persist on
// a stirring Stage-0 pod (CHO-2229).
type RevealTxInput struct {
	TenantID          string
	CompanionID       string
	OwnerGCID         string
	Species           string
	ShinyVariant      bool
	SpeciesRarity     string
	RolledProbability float64
	Now               time.Time
}

// RevealTxOutput carries the post-reveal row. Duplicate=true means the roll
// was ALREADY persisted (idempotent replay or race loser) — the returned
// row holds the original roll and the caller must not publish again.
type RevealTxOutput struct {
	Row       *CompanionGrowthRow
	Duplicate bool
}

// HatchTxInput is the HatchEgg commit shape. Identity-only since CHO-2229 —
// the breed roll is persisted at CommitReveal and read back, never passed in.
type HatchTxInput struct {
	TenantID       string
	CompanionID    string
	OwnerGCID      string
	DisplayName    string
	Tone           string
	LearnerPersona string
	ResonantAtomID string
	Now            time.Time
}

// AhaMomentInput is the MarkAhaMoment shape.
type AhaMomentInput struct {
	TenantID        string
	CompanionID     string
	OwnerGCID       string
	PreviewLLMTier  string
	WindowExpiresAt time.Time
}

// ListGrowthEventsInput pagination request.
type ListGrowthEventsInput struct {
	TenantID    string
	CompanionID string
	CallerGCID  string
	PageSize    int
	PageToken   string
}

// ListGrowthEventsOutput pagination response.
type ListGrowthEventsOutput struct {
	Events        []*GrowthEventRow
	NextPageToken string
}

// ProvisionEggInput captures the Stage-0 row provisioning shape.
type ProvisionEggInput struct {
	TenantID             string
	OwnerGCID            string
	EggSku               string
	EggPurchaseID        string
	EggSource            string // "purchase" | "subscription_inclusion" | "tenant_grant" | "trial"
	Now                  time.Time
	SoftExpiryAt         time.Time
	HardExpiryAt         time.Time
	SuggestedFocalAtomID string
	// StripeSessionID / AmountCents / Currency populate the matching
	// egg_purchased.v1 schema fields (CHO-2028) — sourced from the payments
	// capture event, not persisted on companion_instances.
	StripeSessionID string
	AmountCents     int64
	Currency        string
	// Traceparent / Tracestate carry the inbound payments event's W3C trace
	// context onto the emitted egg_purchased.v1 envelope. Mandatory —
	// validateInboundEnvelope guarantees the subscriber has one.
	Traceparent string
	Tracestate  string
}

// OutboxPort is the publishing port the service uses. The implementing
// adapter is outbox.Publisher; tests use a fake.
type OutboxPort interface {
	PublishGrowthEvent(ctx context.Context, topic string, payload map[string]any, env GrowthEnvelope) error
}

// GrowthEnvelope mirrors the events.Envelope fields the service hands to the
// outbox publisher. Defined here to keep the domain decoupled from the
// adapter's concrete Envelope type.
type GrowthEnvelope struct {
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
	SchemaVersion  int32
}

// DefaultAhaMomentWindowSeconds — 24h. Per-tenant override is in scope for
// Iter G.6.
const DefaultAhaMomentWindowSeconds = 86400
