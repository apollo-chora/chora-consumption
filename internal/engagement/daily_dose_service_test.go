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

func TestDailyDoseService_GetTodaySession(t *testing.T) {
	t.Parallel()

	gcid := uuid.Must(uuid.NewV7())
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("returns session from repo", func(t *testing.T) {
		t.Parallel()

		repo := new(mockDailyDoseSessionRepo)

		session := &DailyDoseSession{
			ID:       uuid.Must(uuid.NewV7()),
			GCID:     gcid,
			TenantID: tenantID,
			Atoms: []DailyDoseAtom{
				{AtomID: uuid.Must(uuid.NewV7()), AtomType: "mcq", TopicName: "Math"},
			},
			EstimatedMinutes: 5,
		}
		repo.On("GetTodayByGCID", mock.Anything, gcid, tenantID).Return(session, nil)

		svc := NewDailyDoseService(repo)
		got, err := svc.GetTodaySession(context.Background(), gcid, tenantID)

		require.NoError(t, err)
		assert.Equal(t, session.ID, got.ID)
		assert.Len(t, got.Atoms, 1)
	})

	t.Run("returns ErrDailyDoseNotAvailable when no session exists", func(t *testing.T) {
		t.Parallel()

		repo := new(mockDailyDoseSessionRepo)

		repo.On("GetTodayByGCID", mock.Anything, gcid, tenantID).Return(nil, nil)

		svc := NewDailyDoseService(repo)
		_, err := svc.GetTodaySession(context.Background(), gcid, tenantID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrDailyDoseNotAvailable)
	})

	t.Run("propagates repository error", func(t *testing.T) {
		t.Parallel()

		repo := new(mockDailyDoseSessionRepo)

		repo.On("GetTodayByGCID", mock.Anything, gcid, tenantID).Return(nil, errors.New("db error"))

		svc := NewDailyDoseService(repo)
		_, err := svc.GetTodaySession(context.Background(), gcid, tenantID)

		require.Error(t, err)
		assert.ErrorContains(t, err, "db error")
	})
}

func TestDailyDoseService_CompleteSession(t *testing.T) {
	t.Parallel()

	t.Run("marks session as completed", func(t *testing.T) {
		t.Parallel()

		sessionID := uuid.Must(uuid.NewV7())
		repo := new(mockDailyDoseSessionRepo)

		session := &DailyDoseSession{
			ID:        sessionID,
			CreatedAt: time.Now(),
		}
		repo.On("GetByID", mock.Anything, sessionID).Return(session, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.DailyDoseSession")).Return(nil)

		svc := NewDailyDoseService(repo)
		got, err := svc.CompleteSession(context.Background(), sessionID)

		require.NoError(t, err)
		assert.NotNil(t, got.CompletedAt)
	})

	t.Run("returns ErrDailyDoseSessionNotFound when nil", func(t *testing.T) {
		t.Parallel()

		sessionID := uuid.Must(uuid.NewV7())
		repo := new(mockDailyDoseSessionRepo)

		repo.On("GetByID", mock.Anything, sessionID).Return(nil, nil)

		svc := NewDailyDoseService(repo)
		_, err := svc.CompleteSession(context.Background(), sessionID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrDailyDoseSessionNotFound)
	})

	t.Run("returns ErrDailyDoseAlreadyCompleted when already completed", func(t *testing.T) {
		t.Parallel()

		sessionID := uuid.Must(uuid.NewV7())
		repo := new(mockDailyDoseSessionRepo)

		completed := time.Now()
		session := &DailyDoseSession{
			ID:          sessionID,
			CompletedAt: &completed,
		}
		repo.On("GetByID", mock.Anything, sessionID).Return(session, nil)

		svc := NewDailyDoseService(repo)
		_, err := svc.CompleteSession(context.Background(), sessionID)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrDailyDoseAlreadyCompleted)
	})

	t.Run("saves session after completion", func(t *testing.T) {
		t.Parallel()

		sessionID := uuid.Must(uuid.NewV7())
		repo := new(mockDailyDoseSessionRepo)

		session := &DailyDoseSession{ID: sessionID}
		repo.On("GetByID", mock.Anything, sessionID).Return(session, nil)
		repo.On("Save", mock.Anything, mock.AnythingOfType("*engagement.DailyDoseSession")).Return(nil)

		svc := NewDailyDoseService(repo)
		_, err := svc.CompleteSession(context.Background(), sessionID)

		require.NoError(t, err)
		repo.AssertCalled(t, "Save", mock.Anything, mock.AnythingOfType("*engagement.DailyDoseSession"))
	})
}
