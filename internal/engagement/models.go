package engagement

import (
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

// RevealStatus represents the fog-of-war state of a TopicNode for a learner.
type RevealStatus string

const (
	RevealStatusHidden    RevealStatus = "hidden"
	RevealStatusRevealed  RevealStatus = "revealed"
	RevealStatusConquered RevealStatus = "conquered"
)

// IsValid returns true if the RevealStatus value is recognized.
func (s RevealStatus) IsValid() bool {
	switch s {
	case RevealStatusHidden, RevealStatusRevealed, RevealStatusConquered:
		return true
	}
	return false
}

// DailyDoseStatus represents the state of today's DailyDose session.
type DailyDoseStatus string

const (
	DailyDoseStatusAvailable     DailyDoseStatus = "available"
	DailyDoseStatusCompleted     DailyDoseStatus = "completed"
	DailyDoseStatusNotConfigured DailyDoseStatus = "not_configured"
)

// StreakStatus represents the current streak state.
type StreakStatus string

const (
	StreakStatusActive StreakStatus = "active"
	StreakStatusAtRisk StreakStatus = "at_risk"
	StreakStatusBroken StreakStatus = "broken"
)

// ComboTier represents the XP combo multiplier tier.
// Escalates on consecutive correct answers: base(1x) → bronze(2x) → silver(3x) → gold(4x).
type ComboTier string

const (
	ComboTierBase   ComboTier = "base"
	ComboTierBronze ComboTier = "bronze"
	ComboTierSilver ComboTier = "silver"
	ComboTierGold   ComboTier = "gold"
)

// ComboMultiplier returns the integer multiplier for the tier.
func (t ComboTier) ComboMultiplier() int {
	switch t {
	case ComboTierBronze:
		return 2
	case ComboTierSilver:
		return 3
	case ComboTierGold:
		return 4
	default:
		return 1
	}
}

// NextTier returns the next combo tier after a correct answer.
func (t ComboTier) NextTier() ComboTier {
	switch t {
	case ComboTierBase:
		return ComboTierBronze
	case ComboTierBronze:
		return ComboTierSilver
	case ComboTierSilver:
		return ComboTierGold
	default:
		return ComboTierGold // gold is max
	}
}

// GoalStatus represents the lifecycle state of a goal challenge.
type GoalStatus string

const (
	GoalStatusPending   GoalStatus = "pending"
	GoalStatusActive    GoalStatus = "active"
	GoalStatusCompleted GoalStatus = "completed"
	GoalStatusExpired   GoalStatus = "expired"
	GoalStatusDeclined  GoalStatus = "declined"
)

// GoalTargetType defines what a goal measures.
type GoalTargetType string

const (
	GoalTargetAtomCount      GoalTargetType = "atom_count"
	GoalTargetRetentionPct   GoalTargetType = "retention_pct"
	GoalTargetPathCompletion GoalTargetType = "path_completion"
)

// NotificationType represents the type of engagement notification.
type NotificationType string

const (
	NotificationTypeDailyDose    NotificationType = "daily_dose"
	NotificationTypeStreakAlert  NotificationType = "streak_alert"
	NotificationTypeGoalDeadline NotificationType = "goal_deadline"
	NotificationTypeGoalComplete NotificationType = "goal_completed"
	NotificationTypeLevelUp      NotificationType = "level_up"
	NotificationTypeSkinUnlocked NotificationType = "skin_unlocked"
	NotificationTypeInactivity   NotificationType = "inactivity"
)

// LeaderboardPeriod represents the time period for leaderboard ranking.
type LeaderboardPeriod string

const (
	LeaderboardPeriodWeekly  LeaderboardPeriod = "weekly"
	LeaderboardPeriodMonthly LeaderboardPeriod = "monthly"
	LeaderboardPeriodAllTime LeaderboardPeriod = "all_time"
)

// LeaderboardScope represents the scope of the leaderboard ranking.
type LeaderboardScope string

const (
	LeaderboardScopeTenant LeaderboardScope = "tenant"
	LeaderboardScopeTopic  LeaderboardScope = "topic"
	LeaderboardScopePath   LeaderboardScope = "path"
)

// XPSource represents the activity that generated XP.
type XPSource string

const (
	XPSourceAtomCorrect     XPSource = "atom_correct"
	XPSourceAtomIncorrect   XPSource = "atom_incorrect"
	XPSourceDailyDoseBonus  XPSource = "daily_dose_bonus"
	XPSourceGoalCompleted   XPSource = "goal_completed"
	XPSourceStreakMilestone XPSource = "streak_milestone"
	XPSourceNewTopic        XPSource = "new_topic"
)

// ---------------------------------------------------------------------------
// XP Constants
// ---------------------------------------------------------------------------

const (
	BaseXPCorrect        = 10
	BaseXPIncorrect      = 2
	BonusXPDailyDose     = 50
	BonusXPGoalCompleted = 100
	BonusXPNewTopic      = 25
	MaxConcurrentGoals   = 3
)

// StreakMilestones defines the milestone thresholds and their XP rewards.
var StreakMilestones = map[int]int{
	7:   200,
	30:  500,
	100: 1000,
	365: 5000,
}

// ---------------------------------------------------------------------------
// Domain Entities
// ---------------------------------------------------------------------------

// Streak tracks a learner's daily learning streak per tenant.
type Streak struct {
	ID             uuid.UUID    `json:"id"`
	GCID           uuid.UUID    `json:"gcid"`
	TenantID       uuid.UUID    `json:"tenant_id"`
	CurrentDays    int          `json:"current_days"`
	LongestStreak  int          `json:"longest_streak"`
	Status         StreakStatus `json:"status"`
	LastActivityAt time.Time    `json:"last_activity_at"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
}

// StreakMilestone records when a learner reached a streak milestone.
type StreakMilestone struct {
	Days       int       `json:"days"`
	ReachedAt  time.Time `json:"reached_at"`
	RewardType string    `json:"reward_type"`
}

// XPLedger tracks a learner's XP total and level per tenant.
type XPLedger struct {
	ID        uuid.UUID `json:"id"`
	GCID      uuid.UUID `json:"gcid"`
	TenantID  uuid.UUID `json:"tenant_id"`
	TotalXP   int       `json:"total_xp"`
	Level     int       `json:"level"`
	ComboTier ComboTier `json:"combo_tier"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// XPEntry is an append-only record of a single XP earning event.
type XPEntry struct {
	ID              uuid.UUID  `json:"id"`
	GCID            uuid.UUID  `json:"gcid"`
	TenantID        uuid.UUID  `json:"tenant_id"`
	XPEarned        int        `json:"xp_earned"`
	Source          XPSource   `json:"source"`
	AtomID          *uuid.UUID `json:"atom_id,omitempty"`
	ComboMultiplier int        `json:"combo_multiplier"`
	EarnedAt        time.Time  `json:"earned_at"`
}

// GoalChallenge is a goal challenge assigned to a learner by an instructor/parent.
// Maximum 3 concurrent active goals per learner.
type GoalChallenge struct {
	ID                uuid.UUID       `json:"id"`
	TenantID          uuid.UUID       `json:"tenant_id"`
	AssigneeGCID      uuid.UUID       `json:"assignee_gcid"`
	CreatedBy         uuid.UUID       `json:"created_by"`
	Title             string          `json:"title"`
	Description       string          `json:"description"`
	TargetScope       GoalTargetScope `json:"target_scope"`
	Deadline          time.Time       `json:"deadline"`
	BountyStarCredits int             `json:"bounty_star_credits"`
	Status            GoalStatus      `json:"status"`
	ProgressPct       float64         `json:"progress_pct"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	DeletedAt         *time.Time      `json:"deleted_at,omitempty"`
}

// GoalTargetScope defines what a goal measures and its target.
type GoalTargetScope struct {
	Type        GoalTargetType `json:"type"`
	TopicID     *uuid.UUID     `json:"topic_id,omitempty"`
	TargetValue int            `json:"target_value"`
}

// DailyDoseSession represents an AI-curated daily review session.
type DailyDoseSession struct {
	ID               uuid.UUID       `json:"session_id"`
	GCID             uuid.UUID       `json:"gcid"`
	TenantID         uuid.UUID       `json:"tenant_id"`
	Atoms            []DailyDoseAtom `json:"atoms"`
	Composition      DoseComposition `json:"composition"`
	EstimatedMinutes int             `json:"estimated_minutes"`
	GoalAlignedCount int             `json:"goal_aligned_count"`
	AtomsCompleted   int             `json:"atoms_completed"`
	AtomsCorrect     int             `json:"atoms_correct"`
	TotalXPEarned    int             `json:"total_xp_earned"`
	CompletedAt      *time.Time      `json:"completed_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
}

// DailyDoseAtom is a single atom within a DailyDose session.
type DailyDoseAtom struct {
	AtomID           uuid.UUID `json:"atom_id"`
	AtomType         string    `json:"atom_type"`
	TopicName        string    `json:"topic_name"`
	Difficulty       int       `json:"difficulty"`
	EstimatedSeconds int       `json:"estimated_seconds"`
	GoalAligned      bool      `json:"goal_aligned"`
}

// DoseComposition describes how a DailyDose session was composed.
type DoseComposition struct {
	EbbinghausPct  int `json:"ebbinghaus_pct"`
	CuriosityPct   int `json:"curiosity_pct"`
	WeaknessPct    int `json:"weakness_pct"`
	GoalOverlayPct int `json:"goal_overlay_pct"`
}

// LeaderboardEntry is a read model for a single leaderboard position.
type LeaderboardEntry struct {
	Rank        int       `json:"rank"`
	GCID        uuid.UUID `json:"gcid"`
	DisplayName string    `json:"display_name"`
	AvatarURL   *string   `json:"avatar_url,omitempty"`
	TotalXP     int       `json:"total_xp"`
	Level       int       `json:"level"`
	StreakDays  int       `json:"streak_days"`
}

// EngagementNotification is a notification for a learner.
type EngagementNotification struct {
	ID        uuid.UUID        `json:"id"`
	GCID      uuid.UUID        `json:"gcid"`
	TenantID  uuid.UUID        `json:"tenant_id"`
	Type      NotificationType `json:"type"`
	Title     string           `json:"title"`
	Message   string           `json:"message"`
	Read      bool             `json:"read"`
	ActionURL *string          `json:"action_url,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
}

// NotificationPreferences holds a learner's notification channel preferences.
type NotificationPreferences struct {
	GCID               uuid.UUID `json:"gcid"`
	TenantID           uuid.UUID `json:"tenant_id"`
	PushEnabled        bool      `json:"push_enabled"`
	EmailDigestEnabled bool      `json:"email_digest_enabled"`
	StreakAlerts       bool      `json:"streak_alerts"`
	GoalAlerts         bool      `json:"goal_alerts"`
	DailyDoseReminders bool      `json:"daily_dose_reminders"`
}

// PathProgressSummary shows progress on a single LockedPath.
type PathProgressSummary struct {
	PathID         uuid.UUID `json:"path_id"`
	PathTitle      string    `json:"path_title"`
	CompletionPct  float64   `json:"completion_pct"`
	StepsCompleted int       `json:"steps_completed"`
	StepsTotal     int       `json:"steps_total"`
}

// DashboardResponse is the consolidated engagement dashboard for a learner.
type DashboardResponse struct {
	Streak           StreakSummary         `json:"streak"`
	XP               XPSummary             `json:"xp"`
	Level            int                   `json:"level"`
	DailyDoseStatus  DailyDoseStatus       `json:"daily_dose_status"`
	ActiveGoalsCount int                   `json:"active_goals_count"`
	PathProgress     []PathProgressSummary `json:"path_progress"`
}

// StreakSummary is compact streak data for dashboard embedding.
type StreakSummary struct {
	CurrentDays    int          `json:"current_days"`
	Status         StreakStatus `json:"status"`
	LongestStreak  int          `json:"longest_streak"`
	LastActivityAt time.Time    `json:"last_activity_at"`
}

// XPSummary is compact XP data for dashboard embedding.
type XPSummary struct {
	TotalXP         int `json:"total_xp"`
	Level           int `json:"level"`
	XPToNextLevel   int `json:"xp_to_next_level"`
	ComboMultiplier int `json:"combo_multiplier"`
}

// PageInfo contains cursor-based pagination metadata.
type PageInfo struct {
	NextCursor *string `json:"next_cursor,omitempty"`
	HasNext    bool    `json:"has_next"`
}

// ---------------------------------------------------------------------------
// Predicted Grade (Phase 52.1.7)
// ---------------------------------------------------------------------------

// PredictedGrade represents an ML-predicted grade per learner per topic.
// Actual ML computation is deferred to AI agents. This service stores and
// retrieves predictions.
type PredictedGrade struct {
	ID             uuid.UUID `json:"id"`
	GCID           uuid.UUID `json:"gcid"`
	TenantID       uuid.UUID `json:"tenant_id"`
	TopicID        uuid.UUID `json:"topic_id"`
	TopicName      string    `json:"topic_name"`
	PredictedScore float64   `json:"predicted_score"`
	Confidence     float64   `json:"confidence"`
	ModelVersion   string    `json:"model_version"`
	PredictedAt    time.Time `json:"predicted_at"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Level Calculation
// ---------------------------------------------------------------------------

// LevelForXP computes the level for a given XP total.
// Uses a simple quadratic formula: level N requires N*100 XP cumulative.
func LevelForXP(totalXP int) int {
	level := 1
	threshold := 100
	for totalXP >= threshold {
		level++
		threshold += level * 100
	}
	return level
}

// XPForNextLevel returns the XP remaining to reach the next level.
func XPForNextLevel(totalXP int) int {
	threshold := 0
	level := 0
	for {
		level++
		threshold += level * 100
		if totalXP < threshold {
			return threshold - totalXP
		}
	}
}

// ---------------------------------------------------------------------------
// Fog-of-War Discovery (Choraverse)
// ---------------------------------------------------------------------------

// UIDiscoveryState tracks the fog-of-war reveal status of a TopicNode
// for a specific learner within a tenant.
type UIDiscoveryState struct {
	ID                  uuid.UUID              `json:"id"`
	GCID                uuid.UUID              `json:"gcid"`
	TenantID            uuid.UUID              `json:"tenant_id"`
	TopicNodeID         uuid.UUID              `json:"topic_node_id"`
	RevealStatus        RevealStatus           `json:"reveal_status"`
	RevealedAt          *time.Time             `json:"revealed_at,omitempty"`
	ConqueredAt         *time.Time             `json:"conquered_at,omitempty"`
	MapPositionOverride map[string]interface{} `json:"map_position_override,omitempty"`
	CreatedAt           time.Time              `json:"created_at"`
	UpdatedAt           time.Time              `json:"updated_at"`
}

// FogMapNode represents a topic node's fog state for the BFF fog-map response.
type FogMapNode struct {
	TopicNodeID        uuid.UUID    `json:"topic_node_id"`
	RevealStatus       RevealStatus `json:"reveal_status"`
	ConqueredAt        *time.Time   `json:"conquered_at,omitempty"`
	IsBossNode         bool         `json:"is_boss_node"`
	NeighborTeaseTitle *string      `json:"neighbor_tease_title,omitempty"`
}

// ---------------------------------------------------------------------------
// Star Account (XP Level Progression — Phase 59.2)
// ---------------------------------------------------------------------------

// StarTier represents the star tier derived from a learner's current level.
type StarTier string

const (
	StarTierBronze   StarTier = "bronze_star"
	StarTierSilver   StarTier = "silver_star"
	StarTierGold     StarTier = "gold_star"
	StarTierPlatinum StarTier = "platinum_star"
	StarTierDiamond  StarTier = "diamond_star"
)

// IsValid returns true if the StarTier value is recognized.
func (s StarTier) IsValid() bool {
	switch s {
	case StarTierBronze, StarTierSilver, StarTierGold, StarTierPlatinum, StarTierDiamond:
		return true
	}
	return false
}

// StarTierForLevel returns the star tier for a given level.
//   - bronze_star:   levels 1-4
//   - silver_star:   levels 5-9
//   - gold_star:     levels 10-14
//   - platinum_star: levels 15-19
//   - diamond_star:  levels 20+
func StarTierForLevel(level int) StarTier {
	switch {
	case level >= 20:
		return StarTierDiamond
	case level >= 15:
		return StarTierPlatinum
	case level >= 10:
		return StarTierGold
	case level >= 5:
		return StarTierSilver
	default:
		return StarTierBronze
	}
}

// NextStarTier returns the next tier and the level required, or nil if already diamond.
func NextStarTier(current StarTier) (*StarTier, *int) {
	var next StarTier
	var lvl int
	switch current {
	case StarTierBronze:
		next = StarTierSilver
		lvl = 5
	case StarTierSilver:
		next = StarTierGold
		lvl = 10
	case StarTierGold:
		next = StarTierPlatinum
		lvl = 15
	case StarTierPlatinum:
		next = StarTierDiamond
		lvl = 20
	default:
		return nil, nil // diamond is max
	}
	return &next, &lvl
}

// StarAccount tracks a learner's cumulative XP and maps it to a level and star tier.
type StarAccount struct {
	ID              uuid.UUID  `json:"id"`
	TenantID        uuid.UUID  `json:"tenant_id"`
	GCID            uuid.UUID  `json:"gcid"`
	TotalXP         int64      `json:"total_xp"`
	CurrentLevel    int        `json:"current_level"`
	CurrentStarTier StarTier   `json:"current_star_tier"`
	LevelAchievedAt *time.Time `json:"level_achieved_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
}

// LevelProgress contains detailed level progression information.
type LevelProgress struct {
	CurrentLevel      int       `json:"current_level"`
	CurrentXP         int64     `json:"current_xp"`
	XPForCurrentLevel int64     `json:"xp_for_current_level"`
	XPForNextLevel    int64     `json:"xp_for_next_level"`
	XPRemaining       int64     `json:"xp_remaining"`
	ProgressPct       float64   `json:"progress_pct"`
	CurrentStarTier   StarTier  `json:"current_star_tier"`
	NextStarTier      *StarTier `json:"next_star_tier,omitempty"`
	NextStarTierLevel *int      `json:"next_star_tier_level,omitempty"`
}

// DefaultRevealThreshold is the platform default fog reveal threshold.
const DefaultRevealThreshold = 0.6

// DefaultConquerThreshold is the platform default fog conquer threshold.
const DefaultConquerThreshold = 0.8

// MinFogThreshold is the minimum allowed fog reveal threshold.
const MinFogThreshold = 0.1

// MaxFogThreshold is the maximum allowed fog reveal threshold.
const MaxFogThreshold = 1.0
