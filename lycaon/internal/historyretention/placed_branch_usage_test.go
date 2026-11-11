package historyretention

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStorageUsageCountsReachablePlacedBranches(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "store.db")
	database := testdbfixture.OpenPath(t, storePath)
	folder := filepath.Join(t.TempDir(), enginepaths.WorkerPlacementDirName(1, root))
	tree := filepath.Join(enginepaths.WorkerBranchesRootUnder(folder), "0123456789abcdef", "job")
	testutil.FailErr(t, "create tree", os.MkdirAll(tree, 0o700))
	testutil.FailErr(t, "write tree", os.WriteFile(filepath.Join(tree, "a.txt"), []byte("placed bytes"), 0o600))
	marker, err := json.Marshal(map[string]any{"format_version": 1, "id": "here", "state_key": enginepaths.ProjectKey(root)})
	testutil.FailErr(t, "encode marker", err)
	testutil.FailErr(t, "write marker", os.WriteFile(filepath.Join(folder, "placement.json"), marker, 0o600))
	registry, err := json.Marshal(map[string]any{
		"format_version": 1,
		"folders": []map[string]any{
			{"id": "here", "path": folder, "volume_root": filepath.Dir(folder), "kind": "volume_root"},
			{"id": "gone", "path": filepath.Join(root, "absent"), "volume_root": "/absent", "kind": "volume_root"},
		},
		"branches": map[string]string{},
	})
	testutil.FailErr(t, "encode registry", err)
	testutil.FailErr(t, "write registry", os.WriteFile(filepath.Join(root, enginepaths.WorkerPlacementsFileName), registry, 0o600))

	service := New(database, storePath, nil)
	testutil.FailErr(t, "measure", service.refreshUsage(t.Context()))
	status, err := service.Status(t.Context())
	testutil.FailErr(t, "status", err)
	for _, lane := range status.Lanes {
		if lane.ID == enginepaths.WorkerBranchesDirName {
			if lane.StoredBytes != int64(len("placed bytes")) {
				t.Fatalf("branch lane = %d bytes", lane.StoredBytes)
			}
			return
		}
	}
	t.Fatal("branch lane missing")
}
