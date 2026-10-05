package engagement

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestNotificationService_ListNotifications(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("returns notifications from repo", func(t *testing.T) {
		t.Parallel()

		notifRepo := new(mockNotificationRepo)
		prefsRepo := new(mockNotificationPrefsRepo)

		notifs := []EngagementNotification{
			{ID: uuid.Must(uuid.NewV7()), Type: NotificationTypeStreakAlert, Title: "Streak at risk!", Read: false},
			{ID: uuid.Must(uuid.NewV7()), Type: NotificationTypeLevelUp, Title: "Level 5!", Read: true},
		}
		notifRepo.On("ListByGCID", mock.Anything, gcid, tenantID, false, (*uuid.UUID)(nil), 20).Return(notifs, nil)

		svc := NewNotificationService(notifRepo, prefsRepo)
		got, err := svc.ListNotifications(context.Background(), gcid, tenantID, false, nil, 20)

		require.NoError(t, err)
		assert.Len(t, got, 2)
	})

	t.Run("propagates repository error", func(t *testing.T) {
		t.Parallel()

		notifRepo := new(mockNotificationRepo)
		prefsRepo := new(mockNotificationPrefsRepo)

		notifRepo.On("ListByGCID", mock.Anything, gcid, tenantID, false, (*uuid.UUID)(nil), 20).Return(nil, errors.New("db error"))

		svc := NewNotificationService(notifRepo, prefsRepo)
		_, err := svc.ListNotifications(context.Background(), gcid, tenantID, false, nil, 20)

		require.Error(t, err)
	})
}

func TestNotificationService_GetPreferences(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("returns existing preferences", func(t *testing.T) {
		t.Parallel()

		notifRepo := new(mockNotificationRepo)
		prefsRepo := new(mockNotificationPrefsRepo)

		prefs := &NotificationPreferences{
			GCID:               gcid,
			TenantID:           tenantID,
			PushEnabled:        false,
			EmailDigestEnabled: true,
			StreakAlerts:       true,
			GoalAlerts:         false,
			DailyDoseReminders: true,
		}
		prefsRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(prefs, nil)

		svc := NewNotificationService(notifRepo, prefsRepo)
		got, err := svc.GetPreferences(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.False(t, got.PushEnabled)
		assert.False(t, got.GoalAlerts)
	})

	t.Run("returns defaults when no preferences exist", func(t *testing.T) {
		t.Parallel()

		notifRepo := new(mockNotificationRepo)
		prefsRepo := new(mockNotificationPrefsRepo)

		prefsRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, nil)

		svc := NewNotificationService(notifRepo, prefsRepo)
		got, err := svc.GetPreferences(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.True(t, got.PushEnabled)
		assert.True(t, got.EmailDigestEnabled)
		assert.True(t, got.StreakAlerts)
		assert.True(t, got.GoalAlerts)
		assert.True(t, got.DailyDoseReminders)
	})

	t.Run("propagates repository error", func(t *testing.T) {
		t.Parallel()

		notifRepo := new(mockNotificationRepo)
		prefsRepo := new(mockNotificationPrefsRepo)

		prefsRepo.On("GetByGCIDAndTenant", mock.Anything, gcid, tenantID).Return(nil, errors.New("db error"))

		svc := NewNotificationService(notifRepo, prefsRepo)
		_, err := svc.GetPreferences(context.Background(), gcid, tenantID)

		require.Error(t, err)
	})
}

func TestNotificationService_UpdatePreferences(t *testing.T) {
	t.Parallel()

	t.Run("saves preferences", func(t *testing.T) {
		t.Parallel()

		notifRepo := new(mockNotificationRepo)
		prefsRepo := new(mockNotificationPrefsRepo)

		prefs := &NotificationPreferences{
			GCID:               uuid.Must(uuid.NewV7()),
			TenantID:           uuid.Must(uuid.NewV7()),
			PushEnabled:        false,
			EmailDigestEnabled: false,
			StreakAlerts:       false,
			GoalAlerts:         true,
			DailyDoseReminders: true,
		}
		prefsRepo.On("Save", mock.Anything, prefs).Return(nil)

		svc := NewNotificationService(notifRepo, prefsRepo)
		got, err := svc.UpdatePreferences(context.Background(), prefs)

		require.NoError(t, err)
		assert.False(t, got.PushEnabled)
		prefsRepo.AssertCalled(t, "Save", mock.Anything, prefs)
	})

	t.Run("propagates save error", func(t *testing.T) {
		t.Parallel()

		notifRepo := new(mockNotificationRepo)
		prefsRepo := new(mockNotificationPrefsRepo)

		prefs := &NotificationPreferences{
			GCID:     uuid.Must(uuid.NewV7()),
			TenantID: uuid.Must(uuid.NewV7()),
		}
		prefsRepo.On("Save", mock.Anything, prefs).Return(errors.New("save failed"))

		svc := NewNotificationService(notifRepo, prefsRepo)
		_, err := svc.UpdatePreferences(context.Background(), prefs)

		require.Error(t, err)
	})
}
