package testdbseed

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const DefaultProjectID = "00000000-0000-4000-8000-000000000001"

type store interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// InsertProject inserts a minimal projects row for FK-backed tests.
func InsertProject(t *testing.T, sqlDB store, projectID string) {
	t.Helper()
	now := formatTime(time.Now().UTC())
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT OR IGNORE INTO projects (id, name, last_opened_at, created_at, roots_generation)
		VALUES (?, NULL, ?, ?, 0)
	`, projectID, now, now)
	testutil.FailErr(t, "insert project", err)
}

// InsertProjectRoot attaches one folder root.
func InsertProjectRoot(t *testing.T, sqlDB store, projectID, path string) string {
	t.Helper()
	return InsertProjectRootWithID(t, sqlDB, projectID, uuid.NewString(), path)
}

// InsertProjectRootWithID attaches a folder root with a caller-selected identity.
func InsertProjectRootWithID(t *testing.T, sqlDB store, projectID, rootID, path string) string {
	t.Helper()
	InsertProject(t, sqlDB, projectID)
	var existing int
	err := sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM project_roots WHERE project_id = ?`, projectID).Scan(&existing)
	testutil.FailErr(t, "count project roots", err)
	isPrimary := 0
	if existing == 0 {
		isPrimary = 1
	}
	now := formatTime(time.Now().UTC())
	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, rootID, projectID, path, rootID, isPrimary, now)
	testutil.FailErr(t, "insert project root", err)
	return rootID
}

// InsertDraftScratchRoot attaches the primary draft root.
func InsertDraftScratchRoot(t *testing.T, sqlDB store, projectID string) (rootID, scratchPath string) {
	t.Helper()
	scratch, err := draftScratchPath(projectID)
	testutil.FailErr(t, "draft workspace", err)
	err = os.MkdirAll(scratch, 0o700)
	testutil.FailErr(t, "create draft workspace", err)
	return InsertProjectRoot(t, sqlDB, projectID, scratch), scratch
}

func draftScratchPath(projectID string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", nil
	}
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return enginepaths.DraftWorkspaceUnder(dir, projectID), nil
}

// BindSessionWorkspace sets computed workspace_path on memory-backed sessions in tests.
func BindSessionWorkspace(t *testing.T, store interface {
	UpdateSession(ctx context.Context, id string, fn func(*api.Session)) error
}, sessionID, path string) {
	t.Helper()
	err := store.UpdateSession(t.Context(), sessionID, func(s *api.Session) {
		s.WorkspacePath = path
	})
	testutil.FailErr(t, "bind session workspace", err)
}

// OrchestrationWorkspace binds a temporary session root.
func OrchestrationWorkspace(t *testing.T, store interface {
	UpdateSession(ctx context.Context, id string, fn func(*api.Session)) error
}, sess *api.Session) string {
	t.Helper()
	dir := t.TempDir()
	BindSessionWorkspace(t, store, sess.ID, dir)
	return dir
}

func InsertSession(t *testing.T, sqlDB store, sessionID, projectID string) {
	t.Helper()
	InsertProject(t, sqlDB, projectID)
	now := formatTime(time.Now().UTC())
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT INTO sessions (
			id, project_id, owner_person_id, posture, status, created_at, activity_at, updated_at
		) VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', ?, ?, ?)
	`, sessionID, projectID, now, now, now)
	testutil.FailErr(t, "insert session", err)
}

// InsertWorkflowRun inserts a minimal running workflow for FK-backed tests.
func InsertWorkflowRun(t *testing.T, sqlDB store, runID, sessionID, projectID string) {
	t.Helper()
	InsertSession(t, sqlDB, sessionID, projectID)
	now := formatTime(time.Now().UTC())
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT INTO workflow_runs (
			id, session_id, project_id, workflow_id, workflow_version,
			status, revision, current_phase, created_at, updated_at
		) VALUES (?, ?, ?, 'test-workflow', '1.0.0', 'running', 1, 'test-phase', ?, ?)
	`, runID, sessionID, projectID, now, now)
	testutil.FailErr(t, "insert workflow run", err)
}

// InsertSessionWithRoot inserts a session bound to a primary project root path.
func InsertSessionWithRoot(t *testing.T, sqlDB store, sessionID, projectID, dir string) {
	t.Helper()
	rootID := InsertProjectRoot(t, sqlDB, projectID, dir)
	now := formatTime(time.Now().UTC())
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT INTO sessions (
			id, project_id, owner_person_id, workspace_root_id, posture, status, created_at, activity_at, updated_at
		) VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), ?, 'build', 'idle', ?, ?, ?)
	`, sessionID, projectID, rootID, now, now, now)
	testutil.FailErr(t, "insert session", err)
}

// InsertSessionEntry reserves one immutable timeline position for a projection row.
func InsertSessionEntry(t *testing.T, sqlDB store, entryID, sessionID, resourceKind, resourceID string, ord int64) {
	t.Helper()
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT INTO session_entries (id, session_id, ord, resource_kind, resource_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, entryID, sessionID, ord, resourceKind, resourceID, formatTime(time.Now().UTC()))
	testutil.FailErr(t, "insert session entry", err)
}

// OwnerID returns the host owner every store seeds at creation.
func OwnerID(t testing.TB, sqlDB store) string {
	t.Helper()
	var id string
	if err := sqlDB.QueryRowContext(context.Background(), `SELECT id FROM people WHERE role = 'owner'`).Scan(&id); err != nil {
		t.Fatalf("read host owner: %v", err)
	}
	return id
}

// OwnerCaller binds the store's host owner as the authenticated caller, as the
// API does for its device credential.
func OwnerCaller(t testing.TB, ctx context.Context, sqlDB store) context.Context {
	t.Helper()
	return people.WithCaller(ctx, people.Person{ID: OwnerID(t, sqlDB), Role: api.PersonRoleOwner})
}
