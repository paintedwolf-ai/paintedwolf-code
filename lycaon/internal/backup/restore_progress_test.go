package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRestoreProgressBoundsRepeatedFilesAndBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	restore := &pendingRestore{markerPath: path, marker: PendingMarker{StagingDir: t.TempDir()}, appliedSet: map[string]struct{}{}}
	testutil.FailErr(t, "seed pending decision", writeMarker(path, restore.marker))
	read := func() int {
		raw, err := os.ReadFile(filepath.Join(restore.marker.StagingDir, restoreProgressName))
		if os.IsNotExist(err) {
			return 0
		}
		testutil.FailErr(t, "read durable cursor", err)
		var progress restoreProgress
		testutil.FailErr(t, "decode durable cursor", json.Unmarshal(raw, &progress))
		return progress.Completed
	}
	for i := range restoreProgressFiles - 1 {
		testutil.FailErr(t, "record bounded small file", restore.recordApplied(fmt.Sprint(i), 1, i+1))
		if read() != 0 {
			t.Fatal("rewrote complete manifest for an individual small file")
		}
	}
	testutil.FailErr(t, "checkpoint file-count boundary", restore.recordApplied("count-boundary", 1, restore.completed+1))
	if read() != restoreProgressFiles {
		t.Fatal("file-count progress was not durable")
	}
	testutil.FailErr(t, "record partial byte batch", restore.recordApplied("first-half", restoreProgressBytes/2, restore.completed+1))
	if read() != restoreProgressFiles {
		t.Fatal("partial byte batch forced a checkpoint")
	}
	testutil.FailErr(t, "checkpoint byte boundary", restore.recordApplied("second-half", restoreProgressBytes/2, restore.completed+1))
	if read() != restoreProgressFiles+2 {
		t.Fatal("byte-bound progress was not durable")
	}
	testutil.FailErr(t, "checkpoint large file immediately", restore.recordApplied("large", restoreProgressBytes*4, restore.completed+1))
	if read() != restoreProgressFiles+3 {
		t.Fatal("large file left for avoidable replay")
	}
	testutil.FailErr(t, "record final partial batch", restore.recordApplied("partial", 1, restore.completed+1))
	restore.recordFailure(errors.New("interrupted operation"))
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read failed marker", err)
	var marker PendingMarker
	testutil.FailErr(t, "decode failed marker", json.Unmarshal(raw, &marker))
	if len(marker.Applied) != restoreProgressFiles+4 || marker.FailedAt == "" {
		t.Fatal("reported failure lost completed progress")
	}
}

func TestRestoreReplaysUncheckpointedDurableFiles(t *testing.T) {
	for _, prefix := range []int{restoreProgressFiles - 1, restoreProgressFiles + 1} {
		t.Run(fmt.Sprint(prefix), func(t *testing.T) { testRestoreReplayPrefix(t, prefix) })
	}
}

func testRestoreReplayPrefix(t *testing.T, prefix int) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, storeRelPath)
	database := testdbfixture.OpenPath(t, path)
	testutil.FailErr(t, "create retained drafts", os.Mkdir(filepath.Join(root, "drafts"), 0o700))
	for i := range restoreProgressFiles + 1 {
		testutil.FailErr(t, "seed retained body", os.WriteFile(filepath.Join(root, "drafts", fmt.Sprint(i)), []byte(fmt.Sprint(i)), 0o600))
	}
	plan, err := db.PlanUpgrade(t.Context(), database)
	testutil.FailErr(t, "plan fixture recovery", err)
	testutil.FailErr(t, "capture fixture recovery", CaptureUpgradeRecovery(t.Context(), CreateOpts{ConfigDir: root, DBPath: path, SQLDB: database, SchemaUserVersion: db.SchemaVersion}, plan, "test"))
	_, err = StageLatestUpgradeRecovery(t.Context(), StageOpts{ConfigDir: root, DBPath: path, SQLDB: database, SchemaVersion: db.SchemaVersion})
	testutil.FailErr(t, "stage fixture recovery", err)
	testutil.FailErr(t, "close source", database.Close())
	restore, err := loadPendingRestore(root)
	testutil.FailErr(t, "load pending restore", err)
	testutil.FailErr(t, "prepare destinations", restore.prepareFileDestinations(root))
	_, err = restore.progressBinding()
	testutil.FailErr(t, "bind complete restore decision", err)
	restore.marker.Files = restore.marker.Files[:prefix]
	testutil.FailErr(t, "install interrupted batch", restore.installFiles(t.Context(), root))
	// Discard all in-memory progress as a process interruption would.
	resumed, err := loadPendingRestore(root)
	testutil.FailErr(t, "reload original decision", err)
	if len(resumed.applied) != (prefix/restoreProgressFiles)*restoreProgressFiles {
		t.Fatal("durable cursor did not preserve exactly the checkpointed prefix")
	}
	testutil.FailErr(t, "replay durable files and complete", ApplyPending(t.Context(), root))
	for i := range restoreProgressFiles + 1 {
		raw, err := os.ReadFile(filepath.Join(root, "drafts", fmt.Sprint(i)))
		testutil.FailErr(t, "read restored body", err)
		if string(raw) != fmt.Sprint(i) {
			t.Fatal("replayed restore changed a retained body")
		}
	}
	if _, err := os.Stat(PendingMarkerPath(root)); !os.IsNotExist(err) {
		t.Fatalf("restore decision remains: %v", err)
	}
}

func TestRestoreProgressCursorSizeIndependentOfInventory(t *testing.T) {
	var expected int
	for _, count := range []int{64, 8192} {
		restore := &pendingRestore{marker: PendingMarker{StagingDir: t.TempDir()}, completed: 64}
		for i := range count {
			restore.marker.Files = append(restore.marker.Files, PendingFile{RelPath: fmt.Sprintf("drafts/%d", i)})
		}
		testutil.FailErr(t, "checkpoint inventory cursor", restore.checkpointProgress())
		raw, err := os.ReadFile(filepath.Join(restore.marker.StagingDir, restoreProgressName))
		testutil.FailErr(t, "read bounded cursor", err)
		if expected == 0 {
			expected = len(raw)
		}
		if len(raw) != expected || len(raw) > 256 {
			t.Fatalf("progress grew with inventory: %d bytes", len(raw))
		}
	}
}

func TestRestoreProgressRefusesUnboundOrImpossibleCursor(t *testing.T) {
	for _, invalid := range []string{"identity", "count", "version", "absent"} {
		t.Run(invalid, func(t *testing.T) {
			restore := &pendingRestore{marker: PendingMarker{StagingDir: t.TempDir(), Files: []PendingFile{{RelPath: "drafts/body"}}}, appliedSet: map[string]struct{}{}}
			identity, err := restore.progressBinding()
			testutil.FailErr(t, "bind decision", err)
			progress := restoreProgress{Version: 1, Identity: identity, Completed: 1}
			switch invalid {
			case "identity":
				progress.Identity = "other"
			case "count":
				progress.Completed = 2
			case "version":
				progress.Version = 2
			}
			raw, err := json.Marshal(progress)
			testutil.FailErr(t, "encode invalid cursor", err)
			testutil.FailErr(t, "seed invalid cursor", os.WriteFile(filepath.Join(restore.marker.StagingDir, restoreProgressName), raw, 0o600))
			if err := restore.loadProgress(); err == nil || len(restore.applied) != 0 {
				t.Fatal("invalid cursor skipped work")
			}
		})
	}
}

func TestRestoreProgressChecksCheckpointedDeletion(t *testing.T) {
	root := t.TempDir()
	restore := &pendingRestore{markerPath: filepath.Join(root, "pending.json"), marker: PendingMarker{StagingDir: t.TempDir(), DeleteRelPaths: []string{"approvals.yaml"}}, appliedSet: map[string]struct{}{}, completed: 1}
	testutil.FailErr(t, "checkpoint deletion", restore.checkpointProgress())
	path := filepath.Join(root, "approvals.yaml")
	testutil.FailErr(t, "seed incomplete deletion", os.WriteFile(path, []byte("retained"), 0o600))
	if err := restore.loadProgress(); err == nil {
		t.Fatal("cursor skipped a deletion still present")
	}
	testutil.FailErr(t, "finish deletion", os.Remove(path))
	testutil.FailErr(t, "resume completed deletion", restore.loadProgress())
	if len(restore.applied) != 1 {
		t.Fatal("completed deletion not resumed")
	}
}

func TestRestoreProgressPreservesDeletionInspectionError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-a-directory")
	testutil.FailErr(t, "seed invalid destination parent", os.WriteFile(root, []byte("kept"), 0o600))
	restore := &pendingRestore{markerPath: filepath.Join(root, "pending.json"), marker: PendingMarker{StagingDir: t.TempDir(), DeleteRelPaths: []string{"approvals.yaml"}}, appliedSet: map[string]struct{}{}, completed: 1}
	testutil.FailErr(t, "checkpoint deletion", restore.checkpointProgress())
	var pathError *os.PathError
	if err := restore.loadProgress(); !errors.As(err, &pathError) {
		t.Fatalf("lost deletion inspection cause: %v", err)
	}
}
