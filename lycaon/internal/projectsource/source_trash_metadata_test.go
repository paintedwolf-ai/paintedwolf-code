package projectsource

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

func TestTrashReclamationMetadataRollsBackAndRetries(t *testing.T) {
	for _, path := range []string{"history", "discarded mutation"} {
		for _, projection := range []string{"mutation", "undo", "redo"} {
			t.Run(path+"/"+projection, func(t *testing.T) {
				service, project, root, _ := sourceMutationFixture(t)
				t.Cleanup(func() { testutil.FailErr(t, "close service", service.Close()) })
				testutil.FailErr(t, "seed removed file", os.WriteFile(filepath.Join(root, "entry"), []byte("retained"), 0o600))
				id, err := service.RemoveEntry(t.Context(), agentRemoval(t, project, "entry", textfile.SHA256([]byte("retained"))))
				testutil.FailErr(t, "remove actual source entry", err)
				testutil.FailErr(t, "finish initial reclamation", service.WaitReclamation(t.Context()))
				testutil.FailErr(t, "stop asynchronous reclaimer", service.Close())
				entries, err := loadHistoryReclaimEntries(t.Context(), service.History)
				testutil.FailErr(t, "load retained recovery metadata", err)
				if len(entries) != 1 || entries[0].undoPlan.NativeTrash == nil || entries[0].redoPlan.NativeTrash == nil {
					t.Fatalf("expected actual removal recovery plans: %+v", entries)
				}
				slot := entries[0].undoPlan.NativeTrash.Receipt.Path
				if _, err := os.Lstat(slot); err != nil {
					testutil.FailErr(t, "verify retained physical slot", err)
				}
				service.History.SetRecoveryQuotaBytes(1)
				reclaim := func() error { return reclaimMovedAsideRecovery(t.Context(), service) }
				if path == "discarded mutation" {
					// This pass receives the already-computed set of reachable slots.
					reclaim = func() error {
						return reclaimDiscardedMutationSlots(t.Context(), service.Journal.db, map[string]bool{}, map[string]bool{})
					}
				}
				table, column, field := "source_mutations", "plan_json", "$.native_trash.reclaimed"
				if projection != "mutation" {
					table, column = "source_history_entries", projection+"_plan_json"
				}
				trigger := fmt.Sprintf(`CREATE TRIGGER fail_trash_metadata BEFORE UPDATE OF %s ON %s
 WHEN NEW.id='%s' AND json_extract(NEW.%s,'%s')=1
 BEGIN SELECT RAISE(ABORT,'injected trash metadata failure'); END`, column, table, id, column, field)
				_, err = service.Journal.db.ExecContext(t.Context(), trigger)
				testutil.FailErr(t, "install metadata failure", err)
				if err := reclaim(); err == nil || !strings.Contains(err.Error(), "injected trash metadata failure") {
					t.Fatalf("reclaim did not report failed %s projection: %v", projection, err)
				}
				assertTrashMetadataReclaimed(t, service, id, 0)
				if _, err := os.Lstat(slot); !os.IsNotExist(err) {
					t.Fatalf("physical removal did not precede metadata failure: %v", err)
				}
				_, err = service.Journal.db.ExecContext(t.Context(), `DROP TRIGGER fail_trash_metadata`)
				testutil.FailErr(t, "remove injected failure", err)
				// Each pass reloads its plans from SQLite, including after rollback.
				testutil.FailErr(t, "retry actual reclamation with missing slot", reclaim())
				assertTrashMetadataReclaimed(t, service, id, 1)
				testutil.FailErr(t, "repeat completed reclamation", reclaim())
				if _, err := os.Lstat(filepath.Join(root, "entry")); !os.IsNotExist(err) {
					t.Fatalf("reclamation resurrected source entry: %v", err)
				}
			})
		}
	}
}

func assertTrashMetadataReclaimed(t *testing.T, service *SourceMutationService, id string, want int) {
	t.Helper()
	var mutation, undo, redo int
	testutil.FailErr(t, "read all durable trash projections", service.Journal.db.QueryRowContext(t.Context(), `
 SELECT COALESCE(json_extract(m.plan_json,'$.native_trash.reclaimed'),0),
 COALESCE(json_extract(h.undo_plan_json,'$.native_trash.reclaimed'),0),
 COALESCE(json_extract(h.redo_plan_json,'$.native_trash.reclaimed'),0)
 FROM source_mutations m JOIN source_history_entries h ON h.id=m.id WHERE m.id=?`, id).Scan(&mutation, &undo, &redo))
	if mutation != want || undo != want || redo != want {
		t.Fatalf("reclaimed mutation/undo/redo=%d/%d/%d, want all %d", mutation, undo, redo, want)
	}
}
