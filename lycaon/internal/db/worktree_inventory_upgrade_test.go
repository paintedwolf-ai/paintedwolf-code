package db_test

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/project/worktrees"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
)

func TestUpgradePreservesLegacyWorktreeBindingsAndAmbiguousHistory(t *testing.T) {
	testutil.SkipIfRace(t, "corpus upgrade exercises full migration route")
	raw, err := os.ReadFile("../../testdata/upgrade-corpus/1.0.2/store.db")
	testutil.FailErr(t, "read 1.0.2 released store", err)
	storePath := filepath.Join(t.TempDir(), "store.db")
	testutil.FailErr(t, "copy 1.0.2 store", os.WriteFile(storePath, raw, 0o600))

	base := gitrepo.CanonicalDir(t.TempDir())
	primary := filepath.Join(base, "primary")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(primary, 0o755))
	runGit := func(dir string, args ...string) {
		t.Helper()
		full := append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "-C", dir}, args...)
		out, cmdErr := exec.Command("git", full...).CombinedOutput()
		if cmdErr != nil {
			t.Fatalf("git %v: %v\n%s", args, cmdErr, string(out))
		}
	}
	runGit(primary, "init", "-q", "-b", "main")
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(primary, "readme.md"), []byte("hello\n"), 0o644))
	runGit(primary, "add", "readme.md")
	runGit(primary, "commit", "-qm", "init")

	boundWT := filepath.Join(base, "bound-wt")
	runGit(primary, "worktree", "add", "-q", "-b", "session/bound", boundWT)

	ambiguousWT := filepath.Join(base, "ambiguous-wt")
	runGit(primary, "worktree", "add", "-q", "--detach", ambiguousWT)

	// Seed pre-upgrade revision-4 source_worktrees and session_worktrees rows.
	legacyDB, err := sql.Open("sqlite", storePath)
	testutil.FailErr(t, "open legacy store", err)
	var projectID, sessionID string
	testutil.FailErr(t, "read project", legacyDB.QueryRow(`SELECT id FROM projects ORDER BY id LIMIT 1`).Scan(&projectID))
	testutil.FailErr(t, "read session", legacyDB.QueryRow(`SELECT id FROM sessions WHERE parent_session_id IS NULL ORDER BY id LIMIT 1`).Scan(&sessionID))

	repoID := git.DeriveRepoID(primary)
	now := timelayout.Format(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	boundID := uuid.NewString()
	ambiguousID := uuid.NewString()

	_, err = legacyDB.Exec(
		`INSERT INTO source_worktrees (id, project_id, repo_id, toplevel, worktree_path, branch, base_branch, created_at) VALUES (?, ?, ?, ?, ?, 'session/bound', 'main', ?)`,
		boundID, projectID, repoID, primary, boundWT, now,
	)
	testutil.FailErr(t, "seed legacy bound source_worktree", err)
	_, err = legacyDB.Exec(
		`INSERT INTO session_worktrees (session_id, project_id, worktree_id, created_at) VALUES (?, ?, ?, ?)`,
		sessionID, projectID, boundID, now,
	)
	testutil.FailErr(t, "seed legacy session_worktree", err)
	// Ambiguous row claims branch 'old-feature', while disk holds a detached checkout at ambiguousWT.
	_, err = legacyDB.Exec(
		`INSERT INTO source_worktrees (id, project_id, repo_id, toplevel, worktree_path, branch, base_branch, created_at) VALUES (?, ?, ?, ?, ?, 'old-feature', 'main', ?)`,
		ambiguousID, projectID, repoID, primary, ambiguousWT, now,
	)
	testutil.FailErr(t, "seed legacy ambiguous source_worktree", err)
	_, err = legacyDB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	testutil.FailErr(t, "checkpoint legacy store", err)
	testutil.FailErr(t, "close legacy store", legacyDB.Close())

	testutil.FailErr(t, "upgrade staged", db.UpgradeStaged(t.Context(), storePath))

	upgraded, err := db.Open(storePath)
	testutil.FailErr(t, "open upgraded store", err)
	defer func() { _ = upgraded.Close() }()

	repos := store.NewSQL(upgraded)
	binding, ok, err := repos.Sessions.GetWorktreeBinding(t.Context(), sessionID)
	testutil.FailErr(t, "get migrated binding", err)
	if !ok || binding.WorktreeID != boundID || !binding.Managed {
		t.Fatalf("unexpected migrated binding: %+v (ok=%v)", binding, ok)
	}

	inv := worktrees.New(upgraded, worktrees.Options{})
	items, err := inv.Read(t.Context(), worktrees.RepoSpec{
		ProjectID: projectID,
		RepoID:    repoID,
		Toplevel:  primary,
	})
	testutil.FailErr(t, "reconcile migrated worktrees", err)

	var sawBound, sawAmbiguousReplacement bool
	for _, item := range items {
		if item.WorktreePath == boundWT {
			sawBound = true
			if item.ID != boundID || !item.Managed || item.AdminIdentity == "" {
				t.Fatalf("migrated bound worktree lost ID or evidence: %+v", item)
			}
		}
		if item.WorktreePath == ambiguousWT {
			sawAmbiguousReplacement = true
			if item.ID == ambiguousID {
				t.Fatalf("ambiguous legacy row reused old ID %s for detached checkout", ambiguousID)
			}
		}
	}
	if !sawBound || !sawAmbiguousReplacement {
		t.Fatalf("expected both bound and ambiguous checkouts in %+v", items)
	}

	retiredAmbiguous, err := inv.Get(t.Context(), projectID, ambiguousID)
	testutil.FailErr(t, "read retired ambiguous legacy row", err)
	if retiredAmbiguous.State != "retired" || retiredAmbiguous.SupersededByID == "" {
		t.Fatalf("expected ambiguous legacy row retired with superseded_by_id, got %+v", retiredAmbiguous)
	}
	if gotBranch := sourcebranch.ForWorktree(ambiguousID); gotBranch == "" {
		t.Fatal("expected historical branch addressable")
	}
}
