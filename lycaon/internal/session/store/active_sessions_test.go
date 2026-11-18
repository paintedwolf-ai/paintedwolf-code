package store

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestHasActiveProjectSessions(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "active_sessions.db")
	projectID := "proj-active-test"
	testdbseed.InsertProjectRoot(t, sqlDB, projectID, t.TempDir())

	sqlStore := NewSQL(sqlDB)

	ctx := t.Context()
	now := time.Now().UTC()

	hasSQL, err := sqlStore.Sessions.HasActiveProjectSessions(ctx, projectID, now.Add(-time.Hour))
	testutil.FailErr(t, "check empty sql store", err)
	if hasSQL {
		t.Fatal("expected no active sessions in empty sql store")
	}

	req := api.CreateSessionRequest{ProjectID: projectID, Posture: api.SessionPostureBuild}
	sessSQL, err := sqlStore.Sessions.Create(ctx, req, projectID)
	testutil.FailErr(t, "create session in sql", err)

	hasSQL, err = sqlStore.Sessions.HasActiveProjectSessions(ctx, projectID, now.Add(-time.Hour))
	testutil.FailErr(t, "check sql store with fresh session", err)
	if !hasSQL {
		t.Fatal("expected active session in sql store")
	}

	hasSQL, err = sqlStore.Sessions.HasActiveProjectSessions(ctx, projectID, now.Add(time.Hour))
	testutil.FailErr(t, "check sql store with future cutoff", err)
	if hasSQL {
		t.Fatal("expected no active sessions for future cutoff")
	}

	testutil.FailErr(t, "set sql session busy", sqlStore.Sessions.SetSessionStatus(ctx, sessSQL.ID, api.SessionStatusBusy))

	hasSQL, err = sqlStore.Sessions.HasActiveProjectSessions(ctx, projectID, now.Add(time.Hour))
	testutil.FailErr(t, "check sql store with busy status", err)
	if !hasSQL {
		t.Fatal("expected active session when status is busy in sql")
	}

	nowArchive := time.Now().UTC()
	testutil.FailErr(t, "archive sql session", sqlStore.Sessions.UpdateSession(ctx, sessSQL.ID, func(s *api.Session) {
		s.ArchivedAt = &nowArchive
		s.Status = api.SessionStatusIdle
	}))

	hasSQL, err = sqlStore.Sessions.HasActiveProjectSessions(ctx, projectID, now.Add(-time.Hour))
	testutil.FailErr(t, "check sql store archived session", err)
	if hasSQL {
		t.Fatal("expected archived session not to count as active in sql")
	}

}
