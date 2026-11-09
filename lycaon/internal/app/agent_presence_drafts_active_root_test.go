package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Overlay change spellings address the session folder's root set: a bare
// baseline spelling is a primary-root file even when the task's active root
// is a different attached root.
func TestPresenceDraftsResolveBaselineSpellingsAgainstTheSessionFolder(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "presence-drafts-active-root.db")
	projectID := testdbseed.DefaultProjectID
	primaryID, secondaryID := uuid.NewString(), uuid.NewString()
	primaryDir, secondaryDir, branch := t.TempDir(), t.TempDir(), t.TempDir()
	testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, primaryID, primaryDir)
	testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, secondaryID, secondaryDir)

	ledger := sourceledger.New(sqlDB, t.TempDir())
	projects := project.NewSQLRegistry(sqlDB)
	documents := editordoc.New(editordoc.NewStore(sqlDB), ledger, ledger.History, projects)
	p, err := projects.Get(t.Context(), projectID)
	testutil.FailErr(t, "load project", err)
	roots := project.RootRefsFrom(p)

	branchDirs := map[string]string{}
	for _, root := range roots {
		dir, err := projectroot.BranchDirForID(root.ID)
		testutil.FailErr(t, "branch dir", err)
		branchDirs[root.ID] = dir
		testutil.FailErr(t, "mkdir branch root", os.MkdirAll(filepath.Join(branch, dir), 0o750))
	}
	// The primary side holds both roots' files; the branch starts from them.
	writeFixtureFile(t, primaryDir, "edit.txt", "a\nb\n")
	writeFixtureFile(t, filepath.Join(branch, branchDirs[primaryID]), "edit.txt", "a\nb\n")
	writeFixtureFile(t, secondaryDir, "made.txt", "one\n")
	writeFixtureFile(t, filepath.Join(branch, branchDirs[secondaryID]), "made.txt", "one\n")

	store := workspacebaseline.New(nil, sourceblob.New(t.TempDir()), t.TempDir())
	baselineRef, err := store.Capture(t.Context(), "job-draft-active", workspacebaseline.Branch(roots, branch))
	testutil.FailErr(t, "capture baseline", err)

	// The worker edits a primary file (bare baseline spelling) and adds a
	// secondary file (@label spelling) while its active root is secondary.
	writeFixtureFile(t, filepath.Join(branch, branchDirs[primaryID]), "edit.txt", "a\nB\n")
	writeFixtureFile(t, filepath.Join(branch, branchDirs[secondaryID]), "made.txt", "one\ntwo\n")

	task := &wire.WorkerTask{ID: "job-draft-active", ProjectID: projectID,
		WorkspaceRootID: secondaryID, WorkspacePath: primaryDir, WorkspaceRoot: branch,
		WorkspaceBaselinePath: baselineRef, Scope: &wire.TaskScope{Mode: wire.TaskScopeModeWrite}}
	drafts := presenceDrafts{tasks: func(jobID string) (*wire.WorkerTask, bool) {
		if jobID != task.ID {
			return nil, false
		}
		return task, true
	}, projects: projects, documents: documents}

	files, err := drafts.ReadyFiles(t.Context(), task.ID)
	testutil.FailErr(t, "ready files", err)
	type draft struct {
		rootID     string
		insertions *int
	}
	byPath := make(map[string]draft, len(files))
	for _, file := range files {
		byPath[file.Path] = draft{file.RootID, file.Insertions}
	}
	if len(byPath) != 2 {
		t.Fatalf("draft files = %+v", files)
	}
	primaryDraft := byPath["edit.txt"]
	if primaryDraft.rootID != primaryID {
		t.Fatalf("bare spelling attributed to %q, want the primary root %q", primaryDraft.rootID, primaryID)
	}
	if primaryDraft.insertions == nil || *primaryDraft.insertions != 1 {
		t.Fatalf("primary draft lines = %+v", primaryDraft)
	}
	secondaryDraft := byPath["made.txt"]
	if secondaryDraft.rootID != secondaryID {
		t.Fatalf("labeled spelling attributed to %q, want root %q", secondaryDraft.rootID, secondaryID)
	}
	if secondaryDraft.insertions == nil || *secondaryDraft.insertions != 1 {
		t.Fatalf("secondary draft lines = %+v", secondaryDraft)
	}
}
