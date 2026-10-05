package engagement

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// services_cover_test.go — characterizes the error-return branches and the
// fully-untested MarkRead path. These are the failure legs of each service
// method (repo lookup error, save error, downstream publish error) that the
// happy-path tests skip; each asserts the method surfaces the error and does
// NOT advance to the next side effect.

// ---------------------------------------------------------------------------
// StreakService.RecordActivity — error legs
// ---------------------------------------------------------------------------

func TestStreakService_RecordActivity_GetError(t *testing.T) {
	t.Parallel()
	repo := new(mockStreakRepo)
	pub := new(mockEventPublisher)
	svc := NewStreakService(repo, pub)
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, errors.New("db down"))

	_, err := svc.RecordActivity(context.Background(), gcid, tenantID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get streak")
	repo.AssertNotCalled(t, "Save", mock.Anything, mock.Anything)
}

func TestStreakService_RecordActivity_SaveError(t *testing.T) {
	t.Parallel()
	repo := new(mockStreakRepo)
	pub := new(mockEventPublisher)
	svc := NewStreakService(repo, pub)
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
	repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.Streak")).Return(errors.New("save failed"))

	_, err := svc.RecordActivity(context.Background(), gcid, tenantID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "save streak")
	pub.AssertNotCalled(t, "Publish")
}

func TestStreakService_RecordActivity_PublishError(t *testing.T) {
	t.Parallel()
	repo := new(mockStreakRepo)
	pub := new(mockEventPublisher)
	svc := NewStreakService(repo, pub)
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
	repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.Streak")).Return(nil)
	pub.On("Publish", mock.Anything, TopicEngagementEvents, mock.Anything).Return(errors.New("pubsub down"))

	_, err := svc.RecordActivity(context.Background(), gcid, tenantID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish streak updated event")
}

func TestStreakService_RecordActivity_MilestoneSaveError(t *testing.T) {
	t.Parallel()
	repo := new(mockStreakRepo)
	pub := new(mockEventPublisher)
	svc := NewStreakService(repo, pub)
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	// CurrentDays 6 + a fresh day → ticks to 7, which is a defined milestone,
	// so SaveMilestone is reached and its error surfaces.
	existing := &Streak{
		ID:             uuid.Must(uuid.NewV7()),
		GCID:           gcid,
		TenantID:       tenantID,
		Status:         StreakStatusActive,
		CurrentDays:    6,
		LongestStreak:  6,
		LastActivityAt: time.Now().UTC().Add(-48 * time.Hour),
	}
	repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(existing, nil)
	repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.Streak")).Return(nil)
	pub.On("Publish", mock.Anything, TopicEngagementEvents, mock.MatchedBy(func(e DomainEvent) bool {
		return e.EventType == EventStreakUpdated
	})).Return(nil)
	repo.On("SaveMilestone", mock.Anything, gcid, tenantID, mock.AnythingOfType("engagement.StreakMilestone")).
		Return(errors.New("milestone save failed"))

	_, err := svc.RecordActivity(context.Background(), gcid, tenantID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "save milestone")
}

func TestStreakService_RecordActivity_MilestonePublishError(t *testing.T) {
	t.Parallel()
	repo := new(mockStreakRepo)
	pub := new(mockEventPublisher)
	svc := NewStreakService(repo, pub)
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	// Tick from day 6 to day 7 (a defined milestone). The StreakUpdated
	// publish + SaveMilestone succeed; the milestone-reached publish fails.
	existing := &Streak{
		ID:             uuid.Must(uuid.NewV7()),
		GCID:           gcid,
		TenantID:       tenantID,
		Status:         StreakStatusActive,
		CurrentDays:    6,
		LongestStreak:  6,
		LastActivityAt: time.Now().UTC().Add(-48 * time.Hour),
	}
	repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(existing, nil)
	repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.Streak")).Return(nil)
	repo.On("SaveMilestone", mock.Anything, gcid, tenantID, mock.AnythingOfType("engagement.StreakMilestone")).Return(nil)
	pub.On("Publish", mock.Anything, TopicEngagementEvents, mock.MatchedBy(func(e DomainEvent) bool {
		return e.EventType == EventStreakUpdated
	})).Return(nil)
	pub.On("Publish", mock.Anything, TopicEngagementEvents, mock.MatchedBy(func(e DomainEvent) bool {
		return e.EventType == EventStreakMilestoneReached
	})).Return(errors.New("pubsub down"))

	_, err := svc.RecordActivity(context.Background(), gcid, tenantID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish milestone event")
}

// ---------------------------------------------------------------------------
// XPService — error legs
// ---------------------------------------------------------------------------

func TestXPService_GetXP_ListEntriesError(t *testing.T) {
	t.Parallel()
	repo := new(mockXPLedgerRepo)
	pub := new(mockEventPublisher)
	svc := NewXPService(repo, pub)
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
	repo.On("ListEntries", mock.Anything, gcid, tenantID, 5).Return(nil, errors.New("query failed"))

	_, _, err := svc.GetXP(context.Background(), gcid, tenantID, 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list xp entries")
}

func TestXPService_AwardXP_GetError(t *testing.T) {
	t.Parallel()
	repo := new(mockXPLedgerRepo)
	pub := new(mockEventPublisher)
	svc := NewXPService(repo, pub)
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, errors.New("db down"))

	_, err := svc.AwardXP(context.Background(), gcid, tenantID, 10, XPSourceAtomCorrect, nil, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get xp ledger")
	repo.AssertNotCalled(t, "Save", mock.Anything, mock.Anything)
}

func TestXPService_AwardXP_AppendEntryError(t *testing.T) {
	t.Parallel()
	repo := new(mockXPLedgerRepo)
	pub := new(mockEventPublisher)
	svc := NewXPService(repo, pub)
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
	repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.XPLedger")).Return(nil)
	repo.On("AppendEntry", mock.Anything, mock.AnythingOfType("*engagement.XPEntry")).
		Return(errors.New("append failed"))

	_, err := svc.AwardXP(context.Background(), gcid, tenantID, 10, XPSourceAtomCorrect, nil, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "append xp entry")
	pub.AssertNotCalled(t, "Publish")
}

func TestXPService_AwardXP_PublishXPAwardedError(t *testing.T) {
	t.Parallel()
	repo := new(mockXPLedgerRepo)
	pub := new(mockEventPublisher)
	svc := NewXPService(repo, pub)
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
	repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.XPLedger")).Return(nil)
	repo.On("AppendEntry", mock.Anything, mock.AnythingOfType("*engagement.XPEntry")).Return(nil)
	// Award only 10 XP → no level up, so only the xp_awarded publish fires.
	pub.On("Publish", mock.Anything, TopicEngagementEvents, mock.Anything).
		Return(errors.New("pubsub down"))

	_, err := svc.AwardXP(context.Background(), gcid, tenantID, 10, XPSourceAtomCorrect, nil, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish xp awarded event")
}

func TestXPService_AwardXP_PublishLevelUpError(t *testing.T) {
	t.Parallel()
	repo := new(mockXPLedgerRepo)
	pub := new(mockEventPublisher)
	svc := NewXPService(repo, pub)
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	// Existing ledger at level 1 with 90 XP; +20 crosses to level 2 → level-up
	// publish is attempted. First publish (xp_awarded) succeeds, second fails.
	ledger := &XPLedger{
		ID:        uuid.Must(uuid.NewV7()),
		GCID:      gcid,
		TenantID:  tenantID,
		TotalXP:   90,
		Level:     1,
		ComboTier: ComboTierBase,
	}
	repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(ledger, nil)
	repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.XPLedger")).Return(nil)
	repo.On("AppendEntry", mock.Anything, mock.AnythingOfType("*engagement.XPEntry")).Return(nil)
	pub.On("Publish", mock.Anything, TopicEngagementEvents, mock.MatchedBy(func(e DomainEvent) bool {
		return e.EventType == EventXPAwarded
	})).Return(nil)
	pub.On("Publish", mock.Anything, TopicEngagementEvents, mock.MatchedBy(func(e DomainEvent) bool {
		return e.EventType == EventLevelUp
	})).Return(errors.New("pubsub down"))

	_, err := svc.AwardXP(context.Background(), gcid, tenantID, 20, XPSourceAtomCorrect, nil, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish level up event")
}

func TestXPService_UpdateCombo_GetError(t *testing.T) {
	t.Parallel()
	repo := new(mockXPLedgerRepo)
	pub := new(mockEventPublisher)
	svc := NewXPService(repo, pub)
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, errors.New("db down"))

	tier, err := svc.UpdateCombo(context.Background(), gcid, tenantID, true)
	require.Error(t, err)
	assert.Equal(t, ComboTierBase, tier)
}

func TestXPService_UpdateCombo_NilLedgerReturnsBaseNoError(t *testing.T) {
	t.Parallel()
	repo := new(mockXPLedgerRepo)
	pub := new(mockEventPublisher)
	svc := NewXPService(repo, pub)
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	// No ledger yet → combo cannot advance; returns base with no error,
	// and Save must not be called.
	repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)

	tier, err := svc.UpdateCombo(context.Background(), gcid, tenantID, true)
	require.NoError(t, err)
	assert.Equal(t, ComboTierBase, tier)
	repo.AssertNotCalled(t, "Save", mock.Anything, mock.Anything)
}

func TestXPService_UpdateCombo_SaveError(t *testing.T) {
	t.Parallel()
	repo := new(mockXPLedgerRepo)
	pub := new(mockEventPublisher)
	svc := NewXPService(repo, pub)
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	ledger := &XPLedger{ID: uuid.Must(uuid.NewV7()), GCID: gcid, TenantID: tenantID, ComboTier: ComboTierBase}
	repo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(ledger, nil)
	repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.XPLedger")).Return(errors.New("save failed"))

	tier, err := svc.UpdateCombo(context.Background(), gcid, tenantID, false)
	require.Error(t, err)
	assert.Equal(t, ComboTierBase, tier)
}

// ---------------------------------------------------------------------------
// GoalService — error legs
// ---------------------------------------------------------------------------

func TestGoalService_GetGoal_RepoError(t *testing.T) {
	t.Parallel()
	repo := new(mockGoalChallengeRepo)
	pub := new(mockEventPublisher)
	svc := NewGoalService(repo, pub)
	id := uuid.Must(uuid.NewV7())

	repo.On("GetByID", mock.Anything, id).Return(nil, errors.New("db down"))

	_, err := svc.GetGoal(context.Background(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get goal")
}

func TestGoalService_CreateGoal_CountError(t *testing.T) {
	t.Parallel()
	repo := new(mockGoalChallengeRepo)
	pub := new(mockEventPublisher)
	svc := NewGoalService(repo, pub)
	tenantID, gcid, createdBy := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	repo.On("CountActiveByAssignee", mock.Anything, gcid, tenantID).Return(0, errors.New("count failed"))

	_, err := svc.CreateGoal(context.Background(), tenantID, gcid, createdBy, "Master Algebra", "",
		GoalTargetScope{}, time.Now().UTC().Add(24*time.Hour), 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "count active goals")
	repo.AssertNotCalled(t, "Save", mock.Anything, mock.Anything)
}

func TestGoalService_CreateGoal_SaveError(t *testing.T) {
	t.Parallel()
	repo := new(mockGoalChallengeRepo)
	pub := new(mockEventPublisher)
	svc := NewGoalService(repo, pub)
	tenantID, gcid, createdBy := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	repo.On("CountActiveByAssignee", mock.Anything, gcid, tenantID).Return(0, nil)
	repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.GoalChallenge")).Return(errors.New("save failed"))

	_, err := svc.CreateGoal(context.Background(), tenantID, gcid, createdBy, "Master Algebra", "",
		GoalTargetScope{}, time.Now().UTC().Add(24*time.Hour), 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "save goal")
}

func TestGoalService_AcceptGoal_RepoError(t *testing.T) {
	t.Parallel()
	repo := new(mockGoalChallengeRepo)
	pub := new(mockEventPublisher)
	svc := NewGoalService(repo, pub)
	id := uuid.Must(uuid.NewV7())

	repo.On("GetByID", mock.Anything, id).Return(nil, errors.New("db down"))

	_, err := svc.AcceptGoal(context.Background(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get goal")
}

func TestGoalService_AcceptGoal_SaveError(t *testing.T) {
	t.Parallel()
	repo := new(mockGoalChallengeRepo)
	pub := new(mockEventPublisher)
	svc := NewGoalService(repo, pub)
	id := uuid.Must(uuid.NewV7())

	goal := &GoalChallenge{ID: id, Status: GoalStatusPending, TenantID: uuid.Must(uuid.NewV7()), AssigneeGCID: uuid.Must(uuid.NewV7())}
	repo.On("GetByID", mock.Anything, id).Return(goal, nil)
	repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.GoalChallenge")).Return(errors.New("save failed"))

	_, err := svc.AcceptGoal(context.Background(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "save accepted goal")
	pub.AssertNotCalled(t, "Publish")
}

func TestGoalService_AcceptGoal_PublishError(t *testing.T) {
	t.Parallel()
	repo := new(mockGoalChallengeRepo)
	pub := new(mockEventPublisher)
	svc := NewGoalService(repo, pub)
	id := uuid.Must(uuid.NewV7())

	goal := &GoalChallenge{
		ID:           id,
		Status:       GoalStatusPending,
		TenantID:     uuid.Must(uuid.NewV7()),
		AssigneeGCID: uuid.Must(uuid.NewV7()),
		Deadline:     time.Now().UTC().Add(24 * time.Hour),
	}
	repo.On("GetByID", mock.Anything, id).Return(goal, nil)
	repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.GoalChallenge")).Return(nil)
	pub.On("Publish", mock.Anything, TopicEngagementEvents, mock.Anything).Return(errors.New("pubsub down"))

	_, err := svc.AcceptGoal(context.Background(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish goal accepted event")
}

func TestGoalService_DeclineGoal_RepoError(t *testing.T) {
	t.Parallel()
	repo := new(mockGoalChallengeRepo)
	pub := new(mockEventPublisher)
	svc := NewGoalService(repo, pub)
	id := uuid.Must(uuid.NewV7())

	repo.On("GetByID", mock.Anything, id).Return(nil, errors.New("db down"))

	_, err := svc.DeclineGoal(context.Background(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get goal")
}

func TestGoalService_DeclineGoal_SaveError(t *testing.T) {
	t.Parallel()
	repo := new(mockGoalChallengeRepo)
	pub := new(mockEventPublisher)
	svc := NewGoalService(repo, pub)
	id := uuid.Must(uuid.NewV7())

	goal := &GoalChallenge{ID: id, Status: GoalStatusPending}
	repo.On("GetByID", mock.Anything, id).Return(goal, nil)
	repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.GoalChallenge")).Return(errors.New("save failed"))

	_, err := svc.DeclineGoal(context.Background(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "save declined goal")
}

func TestGoalService_CompleteGoal_RepoError(t *testing.T) {
	t.Parallel()
	repo := new(mockGoalChallengeRepo)
	pub := new(mockEventPublisher)
	svc := NewGoalService(repo, pub)
	id := uuid.Must(uuid.NewV7())

	repo.On("GetByID", mock.Anything, id).Return(nil, errors.New("db down"))

	_, err := svc.CompleteGoal(context.Background(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get goal")
}

func TestGoalService_CompleteGoal_SaveError(t *testing.T) {
	t.Parallel()
	repo := new(mockGoalChallengeRepo)
	pub := new(mockEventPublisher)
	svc := NewGoalService(repo, pub)
	id := uuid.Must(uuid.NewV7())

	goal := &GoalChallenge{ID: id, Status: GoalStatusActive, TenantID: uuid.Must(uuid.NewV7()), AssigneeGCID: uuid.Must(uuid.NewV7())}
	repo.On("GetByID", mock.Anything, id).Return(goal, nil)
	repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.GoalChallenge")).Return(errors.New("save failed"))

	_, err := svc.CompleteGoal(context.Background(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "save completed goal")
	pub.AssertNotCalled(t, "Publish")
}

func TestGoalService_CompleteGoal_PublishError(t *testing.T) {
	t.Parallel()
	repo := new(mockGoalChallengeRepo)
	pub := new(mockEventPublisher)
	svc := NewGoalService(repo, pub)
	id := uuid.Must(uuid.NewV7())

	goal := &GoalChallenge{ID: id, Status: GoalStatusActive, TenantID: uuid.Must(uuid.NewV7()), AssigneeGCID: uuid.Must(uuid.NewV7())}
	repo.On("GetByID", mock.Anything, id).Return(goal, nil)
	repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.GoalChallenge")).Return(nil)
	pub.On("Publish", mock.Anything, TopicEngagementEvents, mock.Anything).Return(errors.New("pubsub down"))

	_, err := svc.CompleteGoal(context.Background(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish goal completed event")
}

// ---------------------------------------------------------------------------
// DailyDoseService.CompleteSession — error legs
// ---------------------------------------------------------------------------

func TestDailyDoseService_CompleteSession_GetError(t *testing.T) {
	t.Parallel()
	repo := new(mockDailyDoseSessionRepo)
	svc := NewDailyDoseService(repo)
	id := uuid.Must(uuid.NewV7())

	repo.On("GetByID", mock.Anything, id).Return(nil, errors.New("db down"))

	_, err := svc.CompleteSession(context.Background(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get session")
	repo.AssertNotCalled(t, "Save", mock.Anything, mock.Anything)
}

func TestDailyDoseService_CompleteSession_SaveError(t *testing.T) {
	t.Parallel()
	repo := new(mockDailyDoseSessionRepo)
	svc := NewDailyDoseService(repo)
	id := uuid.Must(uuid.NewV7())

	session := &DailyDoseSession{ID: id, GCID: uuid.Must(uuid.NewV7()), TenantID: uuid.Must(uuid.NewV7())}
	repo.On("GetByID", mock.Anything, id).Return(session, nil)
	repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.DailyDoseSession")).Return(errors.New("save failed"))

	_, err := svc.CompleteSession(context.Background(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "save completed session")
}

// ---------------------------------------------------------------------------
// LeaderboardService.GetLeaderboard — error legs
// ---------------------------------------------------------------------------

func TestLeaderboardService_GetLeaderboard_LearnerRankError(t *testing.T) {
	t.Parallel()
	repo := new(mockLeaderboardRepo)
	svc := NewLeaderboardService(repo)
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	repo.On("GetRanked", mock.Anything, tenantID, LeaderboardPeriodWeekly, LeaderboardScopeTenant,
		(*uuid.UUID)(nil), (*string)(nil), 10).Return([]LeaderboardEntry{}, nil)
	repo.On("GetLearnerRank", mock.Anything, gcid, tenantID, LeaderboardPeriodWeekly, LeaderboardScopeTenant,
		(*uuid.UUID)(nil)).Return(nil, errors.New("rank query failed"))

	_, _, err := svc.GetLeaderboard(context.Background(), gcid, tenantID, LeaderboardPeriodWeekly,
		LeaderboardScopeTenant, nil, nil, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get learner rank")
}

// ---------------------------------------------------------------------------
// NotificationService.MarkRead — 0% before; full path coverage.
// ---------------------------------------------------------------------------

func TestNotificationService_MarkRead_Success(t *testing.T) {
	t.Parallel()
	notifRepo := new(mockNotificationRepo)
	prefsRepo := new(mockNotificationPrefsRepo)
	svc := NewNotificationService(notifRepo, prefsRepo)
	id := uuid.Must(uuid.NewV7())

	notif := &EngagementNotification{ID: id, Title: "Level up!", Read: false}
	notifRepo.On("GetByID", mock.Anything, id).Return(notif, nil)
	notifRepo.On("MarkRead", mock.Anything, id).Return(nil)

	got, err := svc.MarkRead(context.Background(), id)
	require.NoError(t, err)
	// The returned model must reflect the read flag flipped to true.
	assert.True(t, got.Read)
	assert.Equal(t, "Level up!", got.Title)
	notifRepo.AssertExpectations(t)
}

func TestNotificationService_MarkRead_NotFound(t *testing.T) {
	t.Parallel()
	notifRepo := new(mockNotificationRepo)
	prefsRepo := new(mockNotificationPrefsRepo)
	svc := NewNotificationService(notifRepo, prefsRepo)
	id := uuid.Must(uuid.NewV7())

	notifRepo.On("GetByID", mock.Anything, id).Return(nil, nil)

	_, err := svc.MarkRead(context.Background(), id)
	require.ErrorIs(t, err, ErrNotificationNotFound)
	notifRepo.AssertNotCalled(t, "MarkRead", mock.Anything, mock.Anything)
}

func TestNotificationService_MarkRead_GetError(t *testing.T) {
	t.Parallel()
	notifRepo := new(mockNotificationRepo)
	prefsRepo := new(mockNotificationPrefsRepo)
	svc := NewNotificationService(notifRepo, prefsRepo)
	id := uuid.Must(uuid.NewV7())

	notifRepo.On("GetByID", mock.Anything, id).Return(nil, errors.New("db down"))

	_, err := svc.MarkRead(context.Background(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get notification")
}

func TestNotificationService_MarkRead_MarkError(t *testing.T) {
	t.Parallel()
	notifRepo := new(mockNotificationRepo)
	prefsRepo := new(mockNotificationPrefsRepo)
	svc := NewNotificationService(notifRepo, prefsRepo)
	id := uuid.Must(uuid.NewV7())

	notif := &EngagementNotification{ID: id, Read: false}
	notifRepo.On("GetByID", mock.Anything, id).Return(notif, nil)
	notifRepo.On("MarkRead", mock.Anything, id).Return(errors.New("update failed"))

	_, err := svc.MarkRead(context.Background(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mark read")
}

// ---------------------------------------------------------------------------
// DashboardService.GetDashboard — error legs (XP, goals, dose lookups).
// ---------------------------------------------------------------------------

func newDashboardDeps() (*mockStreakRepo, *mockXPLedgerRepo, *mockGoalChallengeRepo, *mockDailyDoseSessionRepo, *mockEventPublisher, *DashboardService) {
	streakRepo := new(mockStreakRepo)
	xpRepo := new(mockXPLedgerRepo)
	goalRepo := new(mockGoalChallengeRepo)
	doseRepo := new(mockDailyDoseSessionRepo)
	pub := new(mockEventPublisher)
	streakSvc := NewStreakService(streakRepo, pub)
	xpSvc := NewXPService(xpRepo, pub)
	dash := NewDashboardService(streakSvc, xpSvc, goalRepo, doseRepo)
	return streakRepo, xpRepo, goalRepo, doseRepo, pub, dash
}

func TestDashboardService_GetDashboard_StreakError(t *testing.T) {
	t.Parallel()
	streakRepo, _, _, _, _, dash := newDashboardDeps()
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	streakRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, errors.New("db down"))

	_, err := dash.GetDashboard(context.Background(), gcid, tenantID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get streak for dashboard")
}

func TestDashboardService_GetDashboard_XPError(t *testing.T) {
	t.Parallel()
	streakRepo, xpRepo, _, _, _, dash := newDashboardDeps()
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	streakRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
	streakRepo.On("ListMilestones", mock.Anything, gcid, tenantID).Return([]StreakMilestone{}, nil)
	xpRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, errors.New("xp down"))

	_, err := dash.GetDashboard(context.Background(), gcid, tenantID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get xp for dashboard")
}

func TestDashboardService_GetDashboard_GoalsError(t *testing.T) {
	t.Parallel()
	streakRepo, xpRepo, goalRepo, _, _, dash := newDashboardDeps()
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	streakRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
	streakRepo.On("ListMilestones", mock.Anything, gcid, tenantID).Return([]StreakMilestone{}, nil)
	xpRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
	xpRepo.On("ListEntries", mock.Anything, gcid, tenantID, 0).Return([]XPEntry{}, nil)
	activeStatus := GoalStatusActive
	goalRepo.On("ListByAssignee", mock.Anything, gcid, tenantID, &activeStatus, (*uuid.UUID)(nil), MaxConcurrentGoals).
		Return(nil, errors.New("goal query failed"))

	_, err := dash.GetDashboard(context.Background(), gcid, tenantID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "count active goals")
}

func TestDashboardService_GetDashboard_DoseError(t *testing.T) {
	t.Parallel()
	streakRepo, xpRepo, goalRepo, doseRepo, _, dash := newDashboardDeps()
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	streakRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
	streakRepo.On("ListMilestones", mock.Anything, gcid, tenantID).Return([]StreakMilestone{}, nil)
	xpRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
	xpRepo.On("ListEntries", mock.Anything, gcid, tenantID, 0).Return([]XPEntry{}, nil)
	activeStatus := GoalStatusActive
	goalRepo.On("ListByAssignee", mock.Anything, gcid, tenantID, &activeStatus, (*uuid.UUID)(nil), MaxConcurrentGoals).
		Return([]GoalChallenge{}, nil)
	doseRepo.On("GetTodayByGCID", mock.Anything, gcid, tenantID).Return(nil, errors.New("dose down"))

	_, err := dash.GetDashboard(context.Background(), gcid, tenantID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get today dose")
}

func TestDashboardService_GetDashboard_DoseCompletedStatus(t *testing.T) {
	t.Parallel()
	streakRepo, xpRepo, goalRepo, doseRepo, _, dash := newDashboardDeps()
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	streakRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
	streakRepo.On("ListMilestones", mock.Anything, gcid, tenantID).Return([]StreakMilestone{}, nil)
	xpRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)
	xpRepo.On("ListEntries", mock.Anything, gcid, tenantID, 0).Return([]XPEntry{}, nil)
	activeStatus := GoalStatusActive
	goalRepo.On("ListByAssignee", mock.Anything, gcid, tenantID, &activeStatus, (*uuid.UUID)(nil), MaxConcurrentGoals).
		Return([]GoalChallenge{}, nil)
	completedAt := time.Now().UTC()
	doseRepo.On("GetTodayByGCID", mock.Anything, gcid, tenantID).
		Return(&DailyDoseSession{ID: uuid.Must(uuid.NewV7()), CompletedAt: &completedAt}, nil)

	resp, err := dash.GetDashboard(context.Background(), gcid, tenantID)
	require.NoError(t, err)
	// A completed dose session maps to the "completed" dashboard status.
	assert.Equal(t, DailyDoseStatusCompleted, resp.DailyDoseStatus)
}

// ---------------------------------------------------------------------------
// StarAccountService — getOrCreate create-error + AddXP/GetLevelProgress
// propagation of the getOrCreate failure.
// ---------------------------------------------------------------------------

func TestStarAccountService_GetStarAccount_CreateError(t *testing.T) {
	t.Parallel()
	repo := new(mockStarAccountRepo)
	pub := new(mockEventPublisher)
	svc := NewStarAccountService(repo, pub)
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	// No existing account → getOrCreate attempts Create, which fails.
	repo.On("GetByGCID", mock.Anything, gcid, tenantID).Return(nil, nil)
	repo.On("Create", mock.Anything, mock.AnythingOfType("*engagement.StarAccount")).Return(errors.New("create failed"))

	_, err := svc.GetStarAccount(context.Background(), gcid, tenantID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create star account")
}

func TestStarAccountService_AddXP_GetOrCreateError(t *testing.T) {
	t.Parallel()
	repo := new(mockStarAccountRepo)
	pub := new(mockEventPublisher)
	svc := NewStarAccountService(repo, pub)
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	repo.On("GetByGCID", mock.Anything, gcid, tenantID).Return(nil, errors.New("db down"))

	_, err := svc.AddXP(context.Background(), gcid, tenantID, 50)
	require.Error(t, err)
	repo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
}

func TestStarAccountService_GetLevelProgress_GetOrCreateError(t *testing.T) {
	t.Parallel()
	repo := new(mockStarAccountRepo)
	pub := new(mockEventPublisher)
	svc := NewStarAccountService(repo, pub)
	gcid, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	repo.On("GetByGCID", mock.Anything, gcid, tenantID).Return(nil, errors.New("db down"))

	_, err := svc.GetLevelProgress(context.Background(), gcid, tenantID)
	require.Error(t, err)
}
