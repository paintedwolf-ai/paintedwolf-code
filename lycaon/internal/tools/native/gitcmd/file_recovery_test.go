package gitcmd

import (
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestTrackedRemovalAndMoveSourceRecovery(t *testing.T) {
	for _, command := range []string{"rm", "mv"} {
		t.Run(command, func(t *testing.T) {
			h := newApplyHarness(t)
			database := testdbfixture.Open(t, "store.db")
			rootID := testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, h.root)
			ledger := sourceledger.New(database, filepath.Join(t.TempDir(), "content"))
			service := projectsource.NewSourceMutationService(database, ledger)
			t.Cleanup(func() { testutil.FailErr(t, "close source service", service.Close()) })
			h.tc.Identity.ProjectID = testdbseed.DefaultProjectID
			h.tc.Identity.UserTurn = 1
			h.tc.Source.Roots[0].ID = rootID
			h.tc.Source.ActiveRootID = rootID
			h.tc.Source.SourceLedger = ledger
			h.tc.Source.SourceMutations = service
			h.tc.Source.History = tools.SourceHistory{Files: ledger.History, Comparison: ledger.Comparisons, Git: ledger.Git, Authorship: ledger.Walk}
			h.tc.Source.GitMutations = ledger.Git
			var err error
			h.paths, err = projectpaths.NewProgramPaths(t.Context(), h.owners.Boundary, h.tc, "")
			testutil.FailErr(t, "paths", err)
			args := []string{"git", command, "--", "sub/b.txt"}
			if command == "mv" {
				args = append(args, "renamed.txt")
			}
			got := h.exec(args...)
			testutil.FailErr(t, "tracked mutation", got.err)
			if got.result.Status != 0 {
				t.Fatalf("mutation %+v %s", got.result, got.stderr)
			}
			if command == "rm" {
				_, err := (&native.RestoreVersionTool{Boundary: h.owners.Boundary}).Run(t.Context(), map[string]any{"path": "sub/b.txt"}, h.tc)
				testutil.FailErr(t, "restore_version after git rm", err)
				if h.read("sub/b.txt") != "bee\n" {
					t.Fatal("restore_version recovered different bytes")
				}
				if staged := h.git("ls-files", "--", "sub/b.txt"); staged != "" {
					t.Fatal("source recovery changed index")
				}
				return
			}
			p := &project.Project{ID: testdbseed.DefaultProjectID, Roots: []project.Root{{ID: rootID, ProjectID: testdbseed.DefaultProjectID, Path: h.root, IsPrimary: true}}}
			state, err := service.History.State(t.Context(), p.ID)
			testutil.FailErr(t, "source history", err)
			if state.Undo == nil {
				t.Fatal("missing recoverable source effect")
			}
			_, err = service.Undo(t.Context(), uuid.NewString(), p, projectsource.SourceHistoryMutationRequest{ExpectedEntryID: state.Undo.ID})
			testutil.FailErr(t, "undo filesystem change", err)
			if h.read("sub/b.txt") != "bee\n" {
				t.Fatal("recovered bytes differ")
			}
			if command == "mv" && !h.absent("renamed.txt") {
				t.Fatal("move undo retained destination")
			}
		})
	}
}
