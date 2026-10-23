package projectsource

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRetainedTreeBackupRejectsUnavailableCustodyAndUnadmittedStorage(t *testing.T) {
	for _, failure := range []string{"missing", "changed", "pending missing", "admission", "unknown binding"} {
		t.Run(failure, func(t *testing.T) {
			service, project, root, _ := sourceMutationFixture(t)
			testutil.FailErr(t, "seed tree", os.Mkdir(filepath.Join(root, "tree"), 0o700))
			testutil.FailErr(t, "seed content", os.WriteFile(filepath.Join(root, "tree", "file"), []byte("original"), 0o600))
			removal := agentRemoval(project, "tree", "")
			removal.Recursive = true
			id, err := service.RemoveEntry(t.Context(), removal)
			testutil.FailErr(t, "remove tree", err)
			location, err := service.recovery.treeLocation(id)
			testutil.FailErr(t, "locate custody", err)
			path := filepath.Join(location.Root, location.Rel)
			switch failure {
			case "missing", "pending missing":
				testutil.FailErr(t, "remove test custody", os.RemoveAll(path))
			case "changed":
				testutil.FailErr(t, "change retained bytes", os.WriteFile(filepath.Join(path, "file"), []byte("different"), 0o600))
			}
			captureDir := t.TempDir()
			dbPath := filepath.Join(captureDir, "store.db")
			testutil.FailErr(t, "snapshot live store", db.CreateSnapshot(t.Context(), service.Journal.db, dbPath))
			captured := testdbfixture.OpenPath(t, dbPath)
			if failure == "pending missing" {
				_, err = captured.ExecContext(t.Context(), `UPDATE source_mutations SET status='prepared' WHERE id=?`, id)
				testutil.FailErr(t, "seed interrupted captured journal", err)
			}
			if failure == "unknown binding" {
				_, err = captured.ExecContext(t.Context(), `UPDATE source_history_entries SET undo_plan_json=json_set(undo_plan_json,'$.retained_tree.version',99) WHERE id=?`, id)
				testutil.FailErr(t, "seed unknown captured binding", err)
			}
			admissions := 0
			capacityErr := errors.New("test storage admission refused")
			err = MaterializeRecoveryBackup(t.Context(), captured, filepath.Dir(location.Root), filepath.Join(captureDir, "source-content"), func(content, allocated int64) error {
				admissions++
				if content != int64(len("original")) || allocated <= 0 {
					t.Fatalf("capture admission used wrong facts: content=%d allocated=%d", content, allocated)
				}
				return capacityErr
			})
			if failure == "missing" || failure == "changed" {
				testutil.FailErr(t, "capture unavailable history", err)
			} else if err == nil {
				t.Fatal("incomplete or unadmitted capture succeeded")
			}
			if failure == "admission" {
				if !errors.Is(err, capacityErr) || admissions != 1 {
					t.Fatalf("storage refusal: admissions=%d error=%v", admissions, err)
				}
			} else if admissions != 0 {
				t.Fatalf("unavailable or unknown custody reached byte capture: %d", admissions)
			}
			var availability, liveAvailability string
			testutil.FailErr(t, "read captured availability", captured.QueryRowContext(t.Context(), `SELECT availability FROM source_history_entries WHERE id=?`, id).Scan(&availability))
			testutil.FailErr(t, "read live availability", service.Journal.db.QueryRowContext(t.Context(), `SELECT availability FROM source_history_entries WHERE id=?`, id).Scan(&liveAvailability))
			want := "available"
			if failure == "missing" || failure == "changed" || failure == "pending missing" {
				want = "unavailable"
			}
			if availability != want || liveAvailability != "available" {
				t.Fatalf("captured/live availability=%s/%s, want=%s/available", availability, liveAvailability, want)
			}
			var entries int
			testutil.FailErr(t, "count captured entries", captured.QueryRowContext(t.Context(), `SELECT count(*) FROM source_recovery_entries`).Scan(&entries))
			if entries != 0 {
				t.Fatalf("failed custody/admission fabricated %d manifest entries", entries)
			}
		})
	}
}
