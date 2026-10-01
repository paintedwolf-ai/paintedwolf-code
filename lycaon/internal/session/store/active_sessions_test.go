package store

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestHasActiveProjectSessionsSQLAndMemory(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "active_sessions.db")
	projectID := "proj-active-test"
	testdbseed.InsertProjectRoot(t, sqlDB, projectID, t.TempDir())

	sqlStore := NewSQL(sqlDB)
	memoryStore := NewMemory()

	ctx := t.Context()
	now := time.Now().UTC()

	hasSQL, err := sqlStore.HasActiveProjectSessions(ctx, projectID, now.Add(-time.Hour))
	testutil.FailErr(t, "check empty sql store", err)
	if hasSQL {
		t.Fatal("expected no active sessions in empty sql store")
	}

	hasMem, err := memoryStore.HasActiveProjectSessions(ctx, projectID, now.Add(-time.Hour))
	testutil.FailErr(t, "check empty memory store", err)
	if hasMem {
		t.Fatal("expected no active sessions in empty memory store")
	}

	req := api.CreateSessionRequest{ProjectID: projectID, Posture: api.SessionPostureBuild}
	sessSQL, err := sqlStore.Create(ctx, req, projectID)
	testutil.FailErr(t, "create session in sql", err)

	sessMem, err := memoryStore.Create(ctx, req, projectID)
	testutil.FailErr(t, "create session in memory", err)

	hasSQL, err = sqlStore.HasActiveProjectSessions(ctx, projectID, now.Add(-time.Hour))
	testutil.FailErr(t, "check sql store with fresh session", err)
	if !hasSQL {
		t.Fatal("expected active session in sql store")
	}

	hasMem, err = memoryStore.HasActiveProjectSessions(ctx, projectID, now.Add(-time.Hour))
	testutil.FailErr(t, "check memory store with fresh session", err)
	if !hasMem {
		t.Fatal("expected active session in memory store")
	}

	hasSQL, err = sqlStore.HasActiveProjectSessions(ctx, projectID, now.Add(time.Hour))
	testutil.FailErr(t, "check sql store with future cutoff", err)
	if hasSQL {
		t.Fatal("expected no active sessions for future cutoff")
	}

	hasMem, err = memoryStore.HasActiveProjectSessions(ctx, projectID, now.Add(time.Hour))
	testutil.FailErr(t, "check memory store with future cutoff", err)
	if hasMem {
		t.Fatal("expected no active sessions for future cutoff in memory")
	}

	testutil.FailErr(t, "set sql session busy", sqlStore.SetSessionStatus(ctx, sessSQL.ID, api.SessionStatusBusy))
	testutil.FailErr(t, "set mem session busy", memoryStore.SetSessionStatus(ctx, sessMem.ID, api.SessionStatusBusy))

	hasSQL, err = sqlStore.HasActiveProjectSessions(ctx, projectID, now.Add(time.Hour))
	testutil.FailErr(t, "check sql store with busy status", err)
	if !hasSQL {
		t.Fatal("expected active session when status is busy in sql")
	}

	hasMem, err = memoryStore.HasActiveProjectSessions(ctx, projectID, now.Add(time.Hour))
	testutil.FailErr(t, "check mem store with busy status", err)
	if !hasMem {
		t.Fatal("expected active session when status is busy in memory")
	}

	nowArchive := time.Now().UTC()
	testutil.FailErr(t, "archive sql session", sqlStore.UpdateSession(ctx, sessSQL.ID, func(s *api.Session) {
		s.ArchivedAt = &nowArchive
		s.Status = api.SessionStatusIdle
	}))
	testutil.FailErr(t, "archive mem session", memoryStore.UpdateSession(ctx, sessMem.ID, func(s *api.Session) {
		s.ArchivedAt = &nowArchive
		s.Status = api.SessionStatusIdle
	}))

	hasSQL, err = sqlStore.HasActiveProjectSessions(ctx, projectID, now.Add(-time.Hour))
	testutil.FailErr(t, "check sql store archived session", err)
	if hasSQL {
		t.Fatal("expected archived session not to count as active in sql")
	}

	hasMem, err = memoryStore.HasActiveProjectSessions(ctx, projectID, now.Add(-time.Hour))
	testutil.FailErr(t, "check mem store archived session", err)
	if hasMem {
		t.Fatal("expected archived session not to count as active in memory")
	}
}
