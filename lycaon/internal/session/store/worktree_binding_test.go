package store

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"path/filepath"
	"testing"
	"time"
)

type worktreeStore interface {
	Create(ctx context.Context, req api.CreateSessionRequest, projectID string) (*api.Session, error)
	GetWorktreeBinding(ctx context.Context, sessionID string) (*WorktreeBinding, bool, error)
	PutWorktreeBinding(ctx context.Context, b WorktreeBinding) error
	DeleteWorktreeBinding(ctx context.Context, sessionID string) error
}

func TestWorktreeBindingRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) (worktreeStore, string, string)
	}{
		{
			name: "sql",
			setup: func(t *testing.T) (worktreeStore, string, string) {
				t.Helper()
				dir := t.TempDir()
				sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
				testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
				st := NewSQL(sqlDB)
				sess, err := st.Create(context.Background(), api.CreateSessionRequest{
					Posture:   api.SessionPostureBuild,
					ProjectID: testdbseed.DefaultProjectID,
				}, testdbseed.DefaultProjectID)
				testutil.FailErr(t, "create", err)
				return st, sess.ID, testdbseed.DefaultProjectID
			},
		},
		{
			name: "memory",
			setup: func(t *testing.T) (worktreeStore, string, string) {
				t.Helper()
				st := NewMemory()
				sess, err := st.Create(context.Background(), api.CreateSessionRequest{
					Posture:   api.SessionPostureBuild,
					ProjectID: testdbseed.DefaultProjectID,
				}, testdbseed.DefaultProjectID)
				testutil.FailErr(t, "create", err)
				return st, sess.ID, testdbseed.DefaultProjectID
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, sessionID, projectID := tc.setup(t)
			ctx := context.Background()

			_, ok, err := st.GetWorktreeBinding(ctx, sessionID)
			testutil.FailErr(t, "get unbound", err)
			if ok {
				t.Fatal("unbound session must return ok=false")
			}

			created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			want := WorktreeBinding{
				SessionID:    sessionID,
				ProjectID:    projectID,
				RepoID:       "r-abc",
				Toplevel:     "/repo",
				WorktreePath: "/wt",
				Branch:       "session/" + sessionID,
				BaseBranch:   "main",
				CreatedAt:    created,
			}
			testutil.FailErr(t, "put", st.PutWorktreeBinding(ctx, want))

			got, ok, err := st.GetWorktreeBinding(ctx, sessionID)
			testutil.FailErr(t, "get bound", err)
			if !ok || got == nil {
				t.Fatal("expected binding")
			}
			if got.SessionID != want.SessionID || got.ProjectID != want.ProjectID ||
				got.RepoID != want.RepoID || got.Toplevel != want.Toplevel ||
				got.WorktreePath != want.WorktreePath || got.Branch != want.Branch ||
				got.BaseBranch != want.BaseBranch {
				t.Fatalf("binding = %+v want %+v", got, want)
			}
			if !got.CreatedAt.Equal(created) {
				t.Fatalf("CreatedAt = %v want %v", got.CreatedAt, created)
			}

			originalID := got.WorktreeID
			if originalID == "" {
				t.Fatal("worktree has no durable identity")
			}
			testutil.FailErr(t, "repeat binding", st.PutWorktreeBinding(ctx, want))
			repeated, _, err := st.GetWorktreeBinding(ctx, sessionID)
			testutil.FailErr(t, "read repeated binding", err)
			if repeated.WorktreeID != originalID {
				t.Fatal("repeat binding forked worktree history")
			}

			replaced := want
			replaced.Branch = "session/renamed"
			replaced.WorktreePath = "/wt2"
			replaced.CreatedAt = time.Time{} // upsert keeps the original created_at
			testutil.FailErr(t, "upsert", st.PutWorktreeBinding(ctx, replaced))
			got, ok, err = st.GetWorktreeBinding(ctx, sessionID)
			testutil.FailErr(t, "get after upsert", err)
			if !ok || got.Branch != "session/renamed" || got.WorktreePath != "/wt2" {
				t.Fatalf("upsert result = %+v", got)
			}
			if !got.CreatedAt.Equal(created) {
				t.Fatalf("upsert must preserve CreatedAt: got %v want %v", got.CreatedAt, created)
			}

			testutil.FailErr(t, "delete", st.DeleteWorktreeBinding(ctx, sessionID))
			_, ok, err = st.GetWorktreeBinding(ctx, sessionID)
			testutil.FailErr(t, "get after delete", err)
			if ok {
				t.Fatal("expected unbound after delete")
			}
			testutil.FailErr(t, "delete idempotent", st.DeleteWorktreeBinding(ctx, sessionID))
			testutil.FailErr(t, "rebind original checkout", st.PutWorktreeBinding(ctx, want))
			rebound, _, err := st.GetWorktreeBinding(ctx, sessionID)
			testutil.FailErr(t, "read rebound checkout", err)
			if rebound.WorktreeID != originalID {
				t.Fatal("unbind and rebind lost worktree history")
			}
		})
	}
}

func TestWorktreeBindingProjectCascade(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	st := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{
		Posture:   api.SessionPostureBuild,
		ProjectID: testdbseed.DefaultProjectID,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create", err)
	testutil.FailErr(t, "put", st.PutWorktreeBinding(ctx, WorktreeBinding{
		SessionID:    sess.ID,
		ProjectID:    testdbseed.DefaultProjectID,
		RepoID:       "r1",
		Toplevel:     "/repo",
		WorktreePath: "/wt",
		Branch:       "session/x",
		BaseBranch:   "main",
	}))

	_, err = sqlDB.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "delete project", err)
	_, ok, err := st.GetWorktreeBinding(ctx, sess.ID)
	testutil.FailErr(t, "get after project delete", err)
	if ok {
		t.Fatal("project delete must cascade worktree bindings")
	}
}
