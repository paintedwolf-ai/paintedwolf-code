package projectsource

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

func TestRecoveryRetentionFollowsHistoryAcrossRestartAndBranching(t *testing.T) {
	for _, kind := range []string{"binary", "empty file", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			service, project, root, _ := sourceMutationFixture(t)
			ledger := service.settlement.recorder.(*sourceledger.Store)
			verify, reviewed := seedRetainedEntry(t, root, kind)
			id, err := service.RemoveEntry(t.Context(), agentRemoval(project, "entry", reviewed))
			testutil.FailErr(t, "remove retained entry", err)
			for cycle := 0; cycle < 3; cycle++ {
				service = recoverWithMaintenance(t, service, ledger)
				undoHistoryHead(t, service, project, id)
				testutil.FailErr(t, "collect with Redo reachable", ledger.Retention.SweepBlobs(t.Context()))
				verify()
				service = recoverWithMaintenance(t, service, ledger)
				redoHistoryHead(t, service, project, id)
				testutil.FailErr(t, "collect with Undo reachable", ledger.Retention.SweepBlobs(t.Context()))
				if _, err := os.Lstat(filepath.Join(root, "entry")); !os.IsNotExist(err) {
					t.Fatalf("Redo left entry present: %v", err)
				}
			}
			undoHistoryHead(t, service, project, id)
			_, err = service.Create(t.Context(), uuid.NewString(), project, SourceEntryCreateRequest{
				RootID: project.Roots[0].ID, Path: "new branch", Kind: SourceEntryFile,
			})
			testutil.FailErr(t, "replace Redo branch", err)
			service = recoverWithMaintenance(t, service, ledger)
			history, err := service.History.State(t.Context(), project.ID)
			testutil.FailErr(t, "read branched history", err)
			if history.Redo != nil {
				t.Fatal("abandoned Redo remains reachable")
			}
			var entries, objects int
			testutil.FailErr(t, "count released manifest", service.Journal.db.QueryRowContext(t.Context(),
				`SELECT count(*) FROM source_recovery_entries WHERE project_id=? AND recovery_id=?`, project.ID, id).Scan(&entries))
			testutil.FailErr(t, "count released objects", service.Journal.db.QueryRowContext(t.Context(),
				`SELECT count(*) FROM source_recovery_objects WHERE project_id=? AND recovery_id=?`, project.ID, id).Scan(&objects))
			if entries != 0 || objects != 0 {
				t.Fatalf("abandoned recovery retained entries=%d objects=%d", entries, objects)
			}
			verify()
		})
	}
}

func recoverWithMaintenance(t *testing.T, service *SourceMutationService, ledger *sourceledger.Store) *SourceMutationService {
	t.Helper()
	testutil.FailErr(t, "collect before recovery", ledger.Retention.SweepBlobs(t.Context()))
	restarted := NewSourceMutationService(service.Journal.db, ledger)
	installTestTrash(t, restarted)
	testutil.FailErr(t, "recover durable mutations", restarted.Recover(t.Context()))
	testutil.FailErr(t, "collect after recovery", ledger.Retention.SweepBlobs(t.Context()))
	return restarted
}

func seedRetainedEntry(t *testing.T, root, kind string) (func(), string) {
	t.Helper()
	path := filepath.Join(root, "entry")
	switch kind {
	case "directory":
		testutil.FailErr(t, "seed directory", os.Mkdir(path, 0o750))
		return func() {
			info, err := os.Lstat(path)
			testutil.FailErr(t, "read restored directory", err)
			if !info.IsDir() || info.Mode().Perm() != 0o750 {
				t.Fatalf("restored directory mode=%v", info.Mode())
			}
		}, ""
	case "symlink":
		target := filepath.Join(t.TempDir(), "outside project")
		testutil.FailErr(t, "seed unselected target", os.WriteFile(target, []byte("untouched"), 0o600))
		testutil.FailErr(t, "seed symlink", os.Symlink(target, path))
		return func() {
			got, err := os.Readlink(path)
			testutil.FailErr(t, "read restored symlink", err)
			if got != target {
				t.Fatalf("restored target=%q want=%q", got, target)
			}
			data, err := os.ReadFile(target)
			testutil.FailErr(t, "read unselected target", err)
			if string(data) != "untouched" {
				t.Fatal("recovery changed unselected target")
			}
		}, ""
	default:
		var body []byte
		if kind == "binary" {
			body = bytes.Repeat([]byte{0, 1, 255, 42}, sourceledger.MaxRevisionContentBytes/2)
		}
		testutil.FailErr(t, "seed file", os.WriteFile(path, body, 0o750))
		return func() {
			got, err := os.ReadFile(path)
			testutil.FailErr(t, "read restored file", err)
			info, err := os.Stat(path)
			testutil.FailErr(t, "stat restored file", err)
			if !bytes.Equal(got, body) || info.Mode().Perm() != 0o750 {
				t.Fatalf("restored file bytes=%d mode=%v", len(got), info.Mode())
			}
		}, textfile.SHA256(body)
	}
}
