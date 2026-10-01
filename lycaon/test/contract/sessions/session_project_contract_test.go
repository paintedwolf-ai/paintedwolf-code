package contract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func openContractTestDB(t *testing.T) *db.Store {
	t.Helper()
	return testdbfixture.Open(t, "session-project.db")
}

func TestSessionCreateNoFolderUsesDraftScratchWorkspace(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	sqlDB := openContractTestDB(t)
	store := store.NewSQL(sqlDB)
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	rootID, wantScratch := testdbseed.InsertDraftScratchRoot(t, sqlDB, testdbseed.DefaultProjectID)

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		ProjectID: testdbseed.DefaultProjectID,
		Posture:   api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	if sess.WorkspacePath != wantScratch {
		t.Fatalf("workspace_path = %q want %q", sess.WorkspacePath, wantScratch)
	}
	if sess.WorkspaceRootID != rootID {
		t.Fatalf("workspace_root_id = %q want %q", sess.WorkspaceRootID, rootID)
	}
	if info, statErr := os.Stat(sess.WorkspacePath); statErr != nil || !info.IsDir() {
		t.Fatalf("scratch workspace not created: %v", statErr)
	}

	got, err := store.Get(ctx, sess.ID)
	testutil.FailErr(t, "get session", err)
	if !strings.HasPrefix(got.WorkspacePath, wantScratch) {
		t.Fatalf("GET workspace_path = %q want prefix %q", got.WorkspacePath, wantScratch)
	}
}

func TestSessionCreateMultiRootResolvesWorkspaceRootID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sqlDB := openContractTestDB(t)
	store := store.NewSQL(sqlDB)
	primary := filepath.Join(t.TempDir(), "primary")
	secondary := filepath.Join(t.TempDir(), "secondary")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, primary)
	secondaryID := testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, secondary)

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspaceRootID: secondaryID,
		Posture:         api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	if sess.WorkspaceRootID != secondaryID {
		t.Fatalf("workspace_root_id = %q want %q", sess.WorkspaceRootID, secondaryID)
	}
	if sess.WorkspacePath != secondary {
		t.Fatalf("workspace_path = %q want %q", sess.WorkspacePath, secondary)
	}

	got, err := store.Get(ctx, sess.ID)
	testutil.FailErr(t, "get session", err)
	if got.WorkspacePath != secondary {
		t.Fatalf("GET workspace_path = %q want %q", got.WorkspacePath, secondary)
	}
}

func TestSessionCreateDefaultsPrimaryRoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sqlDB := openContractTestDB(t)
	store := store.NewSQL(sqlDB)
	dir := t.TempDir()
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		ProjectID: testdbseed.DefaultProjectID,
		Posture:   api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	if sess.WorkspaceRootID != rootID {
		t.Fatalf("workspace_root_id = %q want %q", sess.WorkspaceRootID, rootID)
	}
	if sess.WorkspacePath != dir {
		t.Fatalf("workspace_path = %q want %q", sess.WorkspacePath, dir)
	}
}
