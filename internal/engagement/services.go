package engagement

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// StreakService — manages daily learning streaks
// ---------------------------------------------------------------------------

// StreakService handles streak retrieval and updates.
type StreakService struct {
	repo   StreakRepository
	events EventPublisher
}

// NewStreakService creates a StreakService with the given repository and event publisher.
func NewStreakService(repo StreakRepository, events EventPublisher) *StreakService {
	return &StreakService{repo: repo, events: events}
}

// GetStreak retrieves a learner's streak. Returns a zero-value streak if none exists.
func (s *StreakService) GetStreak(ctx context.Context, gcid, tenantID uuid.UUID) (*Streak, []StreakMilestone, error) {
	streak, err := s.repo.GetByGCIDAndTenant(ctx, gcid, tenantID)
	if err != nil {
		return nil, nil, fmt.Errorf("get streak: %w", err)
	}
	if streak == nil {
		streak = &Streak{
			ID:       uuid.Must(uuid.NewV7()),
			GCID:     gcid,
			TenantID: tenantID,
			Status:   StreakStatusBroken,
		}
	}

	milestones, err := s.repo.ListMilestones(ctx, gcid, tenantID)
	if err != nil {
		return nil, nil, fmt.Errorf("list milestones: %w", err)
	}

	return streak, milestones, nil
}

// RecordActivity updates the streak after an atom completion. Publishes
// StreakUpdated and StreakMilestoneReached events as appropriate.
func (s *StreakService) RecordActivity(ctx context.Context, gcid, tenantID uuid.UUID) (*Streak, error) {
	streak, err := s.repo.GetByGCIDAndTenant(ctx, gcid, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get streak: %w", err)
	}

	now := time.Now().UTC()
	previousStatus := StreakStatusBroken

	if streak == nil {
		streak = &Streak{
			ID:       uuid.Must(uuid.NewV7()),
			GCID:     gcid,
			TenantID: tenantID,
		}
	} else {
		previousStatus = streak.Status
	}

	streak.Status = StreakStatusActive
	streak.UpdatedAt = now

	// Check if this is a new day vs same day — compare BEFORE overwriting LastActivityAt
	today := now.Truncate(24 * time.Hour)
	lastDay := streak.LastActivityAt.Truncate(24 * time.Hour)
	if !today.Equal(lastDay) || streak.CurrentDays == 0 {
		streak.CurrentDays++
	}
	streak.LastActivityAt = now
	if streak.CurrentDays > streak.LongestStreak {
		streak.LongestStreak = streak.CurrentDays
	}

	if err := s.repo.Save(ctx, streak); err != nil {
		return nil, fmt.Errorf("save streak: %w", err)
	}

	// Publish streak updated event
	evt := NewDomainEvent(EventStreakUpdated, tenantID, &gcid, streak.ID, "Streak", map[string]interface{}{
		"gcid":            gcid.String(),
		"streak_days":     streak.CurrentDays,
		"status":          string(streak.Status),
		"previous_status": string(previousStatus),
	})
	if err := s.events.Publish(ctx, TopicEngagementEvents, evt); err != nil {
		return nil, fmt.Errorf("publish streak updated event: %w", err)
	}

	// Check for milestone
	if xpReward, ok := StreakMilestones[streak.CurrentDays]; ok {
		milestone := StreakMilestone{
			Days:       streak.CurrentDays,
			ReachedAt:  now,
			RewardType: "xp_bonus",
		}
		if err := s.repo.SaveMilestone(ctx, gcid, tenantID, milestone); err != nil {
			return nil, fmt.Errorf("save milestone: %w", err)
		}

		mEvt := NewDomainEvent(EventStreakMilestoneReached, tenantID, &gcid, streak.ID, "Streak", map[string]interface{}{
			"gcid":        gcid.String(),
			"streak_days": streak.CurrentDays,
			"milestone":   streak.CurrentDays,
			"reward_type": "xp_bonus",
		})
		if err := s.events.Publish(ctx, TopicEngagementEvents, mEvt); err != nil {
			return nil, fmt.Errorf("publish milestone event: %w", err)
		}
		_ = xpReward // XP award handled by XPService
	}

	return streak, nil
}

// ---------------------------------------------------------------------------
// XPService — manages experience points and leveling
// ---------------------------------------------------------------------------

// XPService handles XP retrieval and awarding.
type XPService struct {
	repo   XPLedgerRepository
	events EventPublisher
}

// NewXPService creates an XPService with the given repository and event publisher.
func NewXPService(repo XPLedgerRepository, events EventPublisher) *XPService {
	return &XPService{repo: repo, events: events}
}

// GetXP retrieves a learner's XP state and recent history.
func (s *XPService) GetXP(ctx context.Context, gcid, tenantID uuid.UUID, historyLimit int) (*XPLedger, []XPEntry, error) {
	ledger, err := s.repo.GetByGCIDAndTenant(ctx, gcid, tenantID)
	if err != nil {
		return nil, nil, fmt.Errorf("get xp ledger: %w", err)
	}
	if ledger == nil {
		ledger = &XPLedger{
			ID:        uuid.Must(uuid.NewV7()),
			GCID:      gcid,
			TenantID:  tenantID,
			Level:     1,
			ComboTier: ComboTierBase,
		}
	}

	entries, err := s.repo.ListEntries(ctx, gcid, tenantID, historyLimit)
	if err != nil {
		return nil, nil, fmt.Errorf("list xp entries: %w", err)
	}

	return ledger, entries, nil
}

// AwardXP adds XP for a given source and publishes events. Returns the updated ledger.
func (s *XPService) AwardXP(ctx context.Context, gcid, tenantID uuid.UUID, amount int, source XPSource, atomID *uuid.UUID, comboMultiplier int) (*XPLedger, error) {
	ledger, err := s.repo.GetByGCIDAndTenant(ctx, gcid, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get xp ledger: %w", err)
	}
	if ledger == nil {
		ledger = &XPLedger{
			ID:        uuid.Must(uuid.NewV7()),
			GCID:      gcid,
			TenantID:  tenantID,
			Level:     1,
			ComboTier: ComboTierBase,
		}
	}

	previousLevel := ledger.Level
	ledger.TotalXP += amount
	ledger.Level = LevelForXP(ledger.TotalXP)
	ledger.UpdatedAt = time.Now().UTC()

	if err := s.repo.Save(ctx, ledger); err != nil {
		return nil, fmt.Errorf("save xp ledger: %w", err)
	}

	entry := &XPEntry{
		ID:              uuid.Must(uuid.NewV7()),
		GCID:            gcid,
		TenantID:        tenantID,
		XPEarned:        amount,
		Source:          source,
		AtomID:          atomID,
		ComboMultiplier: comboMultiplier,
		EarnedAt:        time.Now().UTC(),
	}
	if err := s.repo.AppendEntry(ctx, entry); err != nil {
		return nil, fmt.Errorf("append xp entry: %w", err)
	}

	// Publish XP awarded event
	evt := NewDomainEvent(EventXPAwarded, tenantID, &gcid, ledger.ID, "XPLedger", map[string]interface{}{
		"gcid":      gcid.String(),
		"xp_amount": amount,
		"source":    string(source),
		"new_total": ledger.TotalXP,
		"new_level": ledger.Level,
	})
	if err := s.events.Publish(ctx, TopicEngagementEvents, evt); err != nil {
		return nil, fmt.Errorf("publish xp awarded event: %w", err)
	}

	// Check for level up
	if ledger.Level > previousLevel {
		lvlEvt := NewDomainEvent(EventLevelUp, tenantID, &gcid, ledger.ID, "XPLedger", map[string]interface{}{
			"gcid":           gcid.String(),
			"new_level":      ledger.Level,
			"previous_level": previousLevel,
			"total_xp":       ledger.TotalXP,
		})
		if err := s.events.Publish(ctx, TopicEngagementEvents, lvlEvt); err != nil {
			return nil, fmt.Errorf("publish level up event: %w", err)
		}
	}

	return ledger, nil
}

// UpdateCombo updates the combo tier after an atom completion.
func (s *XPService) UpdateCombo(ctx context.Context, gcid, tenantID uuid.UUID, correct bool) (ComboTier, error) {
	ledger, err := s.repo.GetByGCIDAndTenant(ctx, gcid, tenantID)
	if err != nil {
		return ComboTierBase, fmt.Errorf("get xp ledger: %w", err)
	}
	if ledger == nil {
		return ComboTierBase, nil
	}

	if correct {
		ledger.ComboTier = ledger.ComboTier.NextTier()
	} else {
		ledger.ComboTier = ComboTierBase
	}
	ledger.UpdatedAt = time.Now().UTC()

	if err := s.repo.Save(ctx, ledger); err != nil {
		return ComboTierBase, fmt.Errorf("save combo: %w", err)
	}

	return ledger.ComboTier, nil
}

// ---------------------------------------------------------------------------
// GoalService — manages goal challenges
// ---------------------------------------------------------------------------

// GoalService handles goal creation, acceptance, decline, and retrieval.
type GoalService struct {
	repo   GoalChallengeRepository
	events EventPublisher
}

// NewGoalService creates a GoalService with the given repository and event publisher.
func NewGoalService(repo GoalChallengeRepository, events EventPublisher) *GoalService {
	return &GoalService{repo: repo, events: events}
}

// ListGoals returns goal challenges for a learner with optional status filter.
func (s *GoalService) ListGoals(ctx context.Context, gcid, tenantID uuid.UUID, status *GoalStatus, cursor *uuid.UUID, limit int) ([]GoalChallenge, error) {
	goals, err := s.repo.ListByAssignee(ctx, gcid, tenantID, status, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("list goals: %w", err)
	}
	return goals, nil
}

// GetGoal retrieves a single goal challenge by ID.
func (s *GoalService) GetGoal(ctx context.Context, id uuid.UUID) (*GoalChallenge, error) {
	goal, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get goal %s: %w", id, err)
	}
	if goal == nil {
		return nil, ErrGoalNotFound
	}
	return goal, nil
}

// CreateGoal creates a new goal challenge. Returns ErrGoalSlotsFull if the
// learner already has MaxConcurrentGoals active goals.
func (s *GoalService) CreateGoal(ctx context.Context, tenantID, assigneeGCID, createdBy uuid.UUID, title, description string, targetScope GoalTargetScope, deadline time.Time, bountyStarCredits int) (*GoalChallenge, error) {
	if title == "" {
		return nil, fmt.Errorf("title must not be empty: %w", ErrValidationFailed)
	}
	if !deadline.After(time.Now().UTC()) {
		return nil, fmt.Errorf("deadline must be in the future: %w", ErrValidationFailed)
	}

	activeCount, err := s.repo.CountActiveByAssignee(ctx, assigneeGCID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("count active goals: %w", err)
	}
	if activeCount >= MaxConcurrentGoals {
		return nil, ErrGoalSlotsFull
	}

	goal := &GoalChallenge{
		ID:                uuid.Must(uuid.NewV7()),
		TenantID:          tenantID,
		AssigneeGCID:      assigneeGCID,
		CreatedBy:         createdBy,
		Title:             title,
		Description:       description,
		TargetScope:       targetScope,
		Deadline:          deadline,
		BountyStarCredits: bountyStarCredits,
		Status:            GoalStatusPending,
	}

	if err := s.repo.Save(ctx, goal); err != nil {
		return nil, fmt.Errorf("save goal: %w", err)
	}

	return goal, nil
}

// AcceptGoal transitions a goal from pending to active. Publishes GoalAccepted event.
func (s *GoalService) AcceptGoal(ctx context.Context, id uuid.UUID) (*GoalChallenge, error) {
	goal, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get goal %s: %w", id, err)
	}
	if goal == nil {
		return nil, ErrGoalNotFound
	}

	if goal.Status == GoalStatusExpired {
		return nil, ErrGoalExpired
	}
	if goal.Status != GoalStatusPending {
		return nil, ErrGoalAlreadyAccepted
	}

	goal.Status = GoalStatusActive
	goal.UpdatedAt = time.Now().UTC()

	if err := s.repo.Save(ctx, goal); err != nil {
		return nil, fmt.Errorf("save accepted goal: %w", err)
	}

	evt := NewDomainEvent(EventGoalAccepted, goal.TenantID, &goal.AssigneeGCID, goal.ID, "GoalChallenge", map[string]interface{}{
		"gcid":                goal.AssigneeGCID.String(),
		"goal_id":             goal.ID.String(),
		"title":               goal.Title,
		"deadline":            goal.Deadline.Format(time.RFC3339),
		"bounty_star_credits": goal.BountyStarCredits,
	})
	if err := s.events.Publish(ctx, TopicEngagementEvents, evt); err != nil {
		return nil, fmt.Errorf("publish goal accepted event: %w", err)
	}

	return goal, nil
}

// DeclineGoal transitions a goal from pending to declined.
func (s *GoalService) DeclineGoal(ctx context.Context, id uuid.UUID) (*GoalChallenge, error) {
	goal, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get goal %s: %w", id, err)
	}
	if goal == nil {
		return nil, ErrGoalNotFound
	}

	if goal.Status == GoalStatusExpired {
		return nil, ErrGoalExpired
	}
	if goal.Status == GoalStatusDeclined {
		return nil, ErrGoalAlreadyDeclined
	}
	if goal.Status != GoalStatusPending {
		return nil, ErrGoalAlreadyAccepted
	}

	goal.Status = GoalStatusDeclined
	goal.UpdatedAt = time.Now().UTC()

	if err := s.repo.Save(ctx, goal); err != nil {
		return nil, fmt.Errorf("save declined goal: %w", err)
	}

	return goal, nil
}

// CompleteGoal transitions a goal from active to completed. Publishes GoalCompleted event.
func (s *GoalService) CompleteGoal(ctx context.Context, id uuid.UUID) (*GoalChallenge, error) {
	goal, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get goal %s: %w", id, err)
	}
	if goal == nil {
		return nil, ErrGoalNotFound
	}

	if goal.Status == GoalStatusExpired {
		return nil, ErrGoalExpired
	}
	if goal.Status != GoalStatusActive {
		return nil, fmt.Errorf("goal %s has status %s, expected active: %w", id, goal.Status, ErrValidationFailed)
	}

	goal.Status = GoalStatusCompleted
	goal.ProgressPct = 100
	goal.UpdatedAt = time.Now().UTC()

	if err := s.repo.Save(ctx, goal); err != nil {
		return nil, fmt.Errorf("save completed goal: %w", err)
	}

	evt := NewDomainEvent(EventGoalCompleted, goal.TenantID, &goal.AssigneeGCID, goal.ID, "GoalChallenge", map[string]interface{}{
		"gcid":                 goal.AssigneeGCID.String(),
		"goal_id":              goal.ID.String(),
		"title":                goal.Title,
		"star_credits_awarded": goal.BountyStarCredits,
	})
	if err := s.events.Publish(ctx, TopicEngagementEvents, evt); err != nil {
		return nil, fmt.Errorf("publish goal completed event: %w", err)
	}

	return goal, nil
}

// ---------------------------------------------------------------------------
// PredictedGradeService — manages ML-predicted grades
// ---------------------------------------------------------------------------

// PredictedGradeService handles storage and retrieval of ML-predicted grades.
// Actual ML computation is deferred to AI agents (e.g., Retention Predictor).
type PredictedGradeService struct {
	repo PredictedGradeRepository
}

// NewPredictedGradeService creates a PredictedGradeService.
func NewPredictedGradeService(repo PredictedGradeRepository) *PredictedGradeService {
	return &PredictedGradeService{repo: repo}
}

// GetPredictions returns predicted grades for a learner.
// If topicID is provided, filters to that topic only.
func (s *PredictedGradeService) GetPredictions(ctx context.Context, gcid, tenantID uuid.UUID, topicID *uuid.UUID) ([]PredictedGrade, error) {
	predictions, err := s.repo.ListByGCID(ctx, gcid, tenantID, topicID)
	if err != nil {
		return nil, fmt.Errorf("list predicted grades: %w", err)
	}
	return predictions, nil
}

// StorePrediction creates or updates a predicted grade for a learner+topic pair.
// Called by AI agents after computing predictions.
func (s *PredictedGradeService) StorePrediction(ctx context.Context, gcid, tenantID, topicID uuid.UUID, topicName string, predictedScore, confidence float64, modelVersion string) (*PredictedGrade, error) {
	if predictedScore < 0 || predictedScore > 100 {
		return nil, fmt.Errorf("predicted_score must be between 0 and 100: %w", ErrValidationFailed)
	}
	if confidence < 0 || confidence > 1 {
		return nil, fmt.Errorf("confidence must be between 0 and 1: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	grade := &PredictedGrade{
		ID:             uuid.Must(uuid.NewV7()),
		GCID:           gcid,
		TenantID:       tenantID,
		TopicID:        topicID,
		TopicName:      topicName,
		PredictedScore: predictedScore,
		Confidence:     confidence,
		ModelVersion:   modelVersion,
		PredictedAt:    now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := s.repo.Upsert(ctx, grade); err != nil {
		return nil, fmt.Errorf("upsert predicted grade: %w", err)
	}

	return grade, nil
}

// ---------------------------------------------------------------------------
// DailyDoseService — manages DailyDose sessions
// ---------------------------------------------------------------------------

// DailyDoseService handles DailyDose retrieval and completion.
type DailyDoseService struct {
	sessionRepo DailyDoseSessionRepository
}

// NewDailyDoseService creates a DailyDoseService.
func NewDailyDoseService(sessionRepo DailyDoseSessionRepository) *DailyDoseService {
	return &DailyDoseService{sessionRepo: sessionRepo}
}

// GetTodaySession retrieves today's DailyDose session for a learner.
func (s *DailyDoseService) GetTodaySession(ctx context.Context, gcid, tenantID uuid.UUID) (*DailyDoseSession, error) {
	session, err := s.sessionRepo.GetTodayByGCID(ctx, gcid, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get today session: %w", err)
	}
	if session == nil {
		return nil, ErrDailyDoseNotAvailable
	}
	return session, nil
}

// CompleteSession marks a DailyDose session as completed.
func (s *DailyDoseService) CompleteSession(ctx context.Context, sessionID uuid.UUID) (*DailyDoseSession, error) {
	session, err := s.sessionRepo.GetByID(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get session %s: %w", sessionID, err)
	}
	if session == nil {
		return nil, ErrDailyDoseSessionNotFound
	}
	if session.CompletedAt != nil {
		return nil, ErrDailyDoseAlreadyCompleted
	}

	now := time.Now().UTC()
	session.CompletedAt = &now

	if err := s.sessionRepo.Save(ctx, session); err != nil {
		return nil, fmt.Errorf("save completed session: %w", err)
	}

	return session, nil
}

// ---------------------------------------------------------------------------
// LeaderboardService — queries leaderboard rankings
// ---------------------------------------------------------------------------

// LeaderboardService handles leaderboard retrieval.
type LeaderboardService struct {
	repo LeaderboardRepository
}

// NewLeaderboardService creates a LeaderboardService.
func NewLeaderboardService(repo LeaderboardRepository) *LeaderboardService {
	return &LeaderboardService{repo: repo}
}

// GetLeaderboard retrieves ranked entries and the requesting learner's own rank.
func (s *LeaderboardService) GetLeaderboard(ctx context.Context, gcid, tenantID uuid.UUID, period LeaderboardPeriod, scope LeaderboardScope, scopeID *uuid.UUID, cursor *string, limit int) ([]LeaderboardEntry, *LeaderboardEntry, error) {
	entries, err := s.repo.GetRanked(ctx, tenantID, period, scope, scopeID, cursor, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("get ranked: %w", err)
	}

	learnerRank, err := s.repo.GetLearnerRank(ctx, gcid, tenantID, period, scope, scopeID)
	if err != nil {
		return nil, nil, fmt.Errorf("get learner rank: %w", err)
	}

	return entries, learnerRank, nil
}

// ---------------------------------------------------------------------------
// NotificationService — manages engagement notifications
// ---------------------------------------------------------------------------

// NotificationService handles notification listing and preference management.
type NotificationService struct {
	notifRepo NotificationRepository
	prefsRepo NotificationPreferencesRepository
}

// NewNotificationService creates a NotificationService.
func NewNotificationService(notifRepo NotificationRepository, prefsRepo NotificationPreferencesRepository) *NotificationService {
	return &NotificationService{notifRepo: notifRepo, prefsRepo: prefsRepo}
}

// ListNotifications returns a learner's engagement notifications.
func (s *NotificationService) ListNotifications(ctx context.Context, gcid, tenantID uuid.UUID, unreadOnly bool, cursor *uuid.UUID, limit int) ([]EngagementNotification, error) {
	notifs, err := s.notifRepo.ListByGCID(ctx, gcid, tenantID, unreadOnly, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	return notifs, nil
}

// GetPreferences retrieves a learner's notification preferences.
func (s *NotificationService) GetPreferences(ctx context.Context, gcid, tenantID uuid.UUID) (*NotificationPreferences, error) {
	prefs, err := s.prefsRepo.GetByGCIDAndTenant(ctx, gcid, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get preferences: %w", err)
	}
	if prefs == nil {
		// Return defaults
		prefs = &NotificationPreferences{
			GCID:               gcid,
			TenantID:           tenantID,
			PushEnabled:        true,
			EmailDigestEnabled: true,
			StreakAlerts:       true,
			GoalAlerts:         true,
			DailyDoseReminders: true,
		}
	}
	return prefs, nil
}

// MarkRead marks a single notification as read and returns it.
func (s *NotificationService) MarkRead(ctx context.Context, id uuid.UUID) (*EngagementNotification, error) {
	notif, err := s.notifRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get notification %s: %w", id, err)
	}
	if notif == nil {
		return nil, ErrNotificationNotFound
	}

	if err := s.notifRepo.MarkRead(ctx, id); err != nil {
		return nil, fmt.Errorf("mark read %s: %w", id, err)
	}

	notif.Read = true
	return notif, nil
}

// UpdatePreferences saves a learner's notification preferences.
func (s *NotificationService) UpdatePreferences(ctx context.Context, prefs *NotificationPreferences) (*NotificationPreferences, error) {
	if err := s.prefsRepo.Save(ctx, prefs); err != nil {
		return nil, fmt.Errorf("save preferences: %w", err)
	}
	return prefs, nil
}

// ---------------------------------------------------------------------------
// DashboardService — aggregates engagement data for the dashboard
// ---------------------------------------------------------------------------

// DashboardService assembles the consolidated engagement dashboard.
type DashboardService struct {
	streakSvc *StreakService
	xpSvc     *XPService
	goalRepo  GoalChallengeRepository
	doseRepo  DailyDoseSessionRepository
}

// NewDashboardService creates a DashboardService.
func NewDashboardService(streakSvc *StreakService, xpSvc *XPService, goalRepo GoalChallengeRepository, doseRepo DailyDoseSessionRepository) *DashboardService {
	return &DashboardService{streakSvc: streakSvc, xpSvc: xpSvc, goalRepo: goalRepo, doseRepo: doseRepo}
}

// GetDashboard assembles the learner's engagement dashboard.
func (s *DashboardService) GetDashboard(ctx context.Context, gcid, tenantID uuid.UUID) (*DashboardResponse, error) {
	streak, _, err := s.streakSvc.GetStreak(ctx, gcid, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get streak for dashboard: %w", err)
	}

	ledger, _, err := s.xpSvc.GetXP(ctx, gcid, tenantID, 0)
	if err != nil {
		return nil, fmt.Errorf("get xp for dashboard: %w", err)
	}

	activeStatus := GoalStatusActive
	activeGoals, err := s.goalRepo.ListByAssignee(ctx, gcid, tenantID, &activeStatus, nil, MaxConcurrentGoals)
	if err != nil {
		return nil, fmt.Errorf("count active goals: %w", err)
	}

	// Determine daily dose status
	doseStatus := DailyDoseStatusNotConfigured
	session, err := s.doseRepo.GetTodayByGCID(ctx, gcid, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get today dose: %w", err)
	}
	if session != nil {
		if session.CompletedAt != nil {
			doseStatus = DailyDoseStatusCompleted
		} else {
			doseStatus = DailyDoseStatusAvailable
		}
	}

	return &DashboardResponse{
		Streak: StreakSummary{
			CurrentDays:    streak.CurrentDays,
			Status:         streak.Status,
			LongestStreak:  streak.LongestStreak,
			LastActivityAt: streak.LastActivityAt,
		},
		XP: XPSummary{
			TotalXP:         ledger.TotalXP,
			Level:           ledger.Level,
			XPToNextLevel:   XPForNextLevel(ledger.TotalXP),
			ComboMultiplier: ledger.ComboTier.ComboMultiplier(),
		},
		Level:            ledger.Level,
		DailyDoseStatus:  doseStatus,
		ActiveGoalsCount: len(activeGoals),
		PathProgress:     []PathProgressSummary{}, // TODO: cross-context query to chora-atomic
	}, nil
}

// ---------------------------------------------------------------------------
// StarAccountService — manages XP level progression and star tiers
// ---------------------------------------------------------------------------

// StarAccountService handles star account retrieval, XP addition, and level progression.
type StarAccountService struct {
	repo   StarAccountRepository
	events EventPublisher
}

// NewStarAccountService creates a StarAccountService with the given repository and event publisher.
func NewStarAccountService(repo StarAccountRepository, events EventPublisher) *StarAccountService {
	return &StarAccountService{repo: repo, events: events}
}

// getOrCreate retrieves an existing star account or creates a default one.
func (s *StarAccountService) getOrCreate(ctx context.Context, gcid, tenantID uuid.UUID) (*StarAccount, error) {
	account, err := s.repo.GetByGCID(ctx, gcid, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get star account: %w", err)
	}
	if account != nil {
		return account, nil
	}

	now := time.Now().UTC()
	account = &StarAccount{
		ID:              uuid.Must(uuid.NewV7()),
		TenantID:        tenantID,
		GCID:            gcid,
		TotalXP:         0,
		CurrentLevel:    1,
		CurrentStarTier: StarTierBronze,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.repo.Create(ctx, account); err != nil {
		return nil, fmt.Errorf("create star account: %w", err)
	}
	return account, nil
}

// GetStarAccount retrieves a learner's star account. Creates a default if none exists.
func (s *StarAccountService) GetStarAccount(ctx context.Context, gcid, tenantID uuid.UUID) (*StarAccount, error) {
	return s.getOrCreate(ctx, gcid, tenantID)
}

// AddXP adds XP to a learner's star account. If the XP causes a level change,
// a star_account.level_up event is published. Returns the updated account.
func (s *StarAccountService) AddXP(ctx context.Context, gcid, tenantID uuid.UUID, xpAmount int64) (*StarAccount, error) {
	account, err := s.getOrCreate(ctx, gcid, tenantID)
	if err != nil {
		return nil, err
	}

	previousLevel := account.CurrentLevel
	previousTier := account.CurrentStarTier

	account.TotalXP += xpAmount
	account.CurrentLevel = LevelForXP(int(account.TotalXP))
	account.CurrentStarTier = StarTierForLevel(account.CurrentLevel)
	now := time.Now().UTC()
	account.UpdatedAt = now

	if account.CurrentLevel > previousLevel {
		account.LevelAchievedAt = &now
	}

	if err := s.repo.Update(ctx, account); err != nil {
		return nil, fmt.Errorf("update star account: %w", err)
	}

	// Publish level-up event if level changed
	if account.CurrentLevel > previousLevel {
		evt := NewDomainEvent(EventStarAccountLevelUp, tenantID, &gcid, account.ID, AggregateStarAccount, map[string]interface{}{
			"gcid":               gcid.String(),
			"new_level":          account.CurrentLevel,
			"previous_level":     previousLevel,
			"total_xp":           account.TotalXP,
			"new_star_tier":      string(account.CurrentStarTier),
			"previous_star_tier": string(previousTier),
		})
		if err := s.events.Publish(ctx, TopicEngagementEvents, evt); err != nil {
			return nil, fmt.Errorf("publish star account level up event: %w", err)
		}
	}

	return account, nil
}

// GetLevelProgress returns detailed level progression information for a learner.
func (s *StarAccountService) GetLevelProgress(ctx context.Context, gcid, tenantID uuid.UUID) (*LevelProgress, error) {
	account, err := s.getOrCreate(ctx, gcid, tenantID)
	if err != nil {
		return nil, err
	}

	totalXP := int(account.TotalXP)
	xpForNext := XPForNextLevel(totalXP)

	// Calculate threshold for current level (cumulative XP at which current level was reached)
	xpForCurrentLevel := int64(0)
	if account.CurrentLevel > 1 {
		// Sum thresholds: level 1 needs 100, level 2 needs 200 more, etc.
		// The threshold for level N = sum(i*100 for i in 1..N-1)
		sum := 0
		for i := 1; i < account.CurrentLevel; i++ {
			sum += i * 100
		}
		xpForCurrentLevel = int64(sum)
	}

	// XP threshold for next level — use cumulative formula directly.
	nextLevelThreshold := int64(0)
	{
		sum := 0
		for i := 1; i <= account.CurrentLevel; i++ {
			sum += i * 100
		}
		nextLevelThreshold = int64(sum)
	}

	// Progress within current level
	xpInCurrentLevel := account.TotalXP - xpForCurrentLevel
	levelRange := nextLevelThreshold - xpForCurrentLevel
	progressPct := 0.0
	if levelRange > 0 {
		progressPct = float64(xpInCurrentLevel) / float64(levelRange)
	}

	nextTier, nextTierLevel := NextStarTier(account.CurrentStarTier)

	return &LevelProgress{
		CurrentLevel:      account.CurrentLevel,
		CurrentXP:         account.TotalXP,
		XPForCurrentLevel: xpForCurrentLevel,
		XPForNextLevel:    nextLevelThreshold,
		XPRemaining:       int64(xpForNext),
		ProgressPct:       progressPct,
		CurrentStarTier:   account.CurrentStarTier,
		NextStarTier:      nextTier,
		NextStarTierLevel: nextTierLevel,
	}, nil
}
