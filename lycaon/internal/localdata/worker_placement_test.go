package localdata

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
)

// registerPlacements writes one reachable folder holding a branch and one absent
// folder, in the on-disk shapes the registry reads.
func registerPlacements(t *testing.T, state string) (folder string) {
	t.Helper()
	folder = filepath.Join(t.TempDir(), enginepaths.WorkerPlacementDirName(1, state))
	tree := filepath.Join(enginepaths.WorkerBranchesRootUnder(folder), "0123456789abcdef", "here")
	testutil.FailErr(t, "create tree", os.MkdirAll(tree, 0o700))
	testutil.FailErr(t, "write tree", os.WriteFile(filepath.Join(tree, "a.txt"), []byte("12345"), 0o600))
	marker, err := json.Marshal(map[string]any{"format_version": 1, "id": "here", "state_key": enginepaths.ProjectKey(state)})
	testutil.FailErr(t, "encode marker", err)
	testutil.FailErr(t, "write marker", os.WriteFile(filepath.Join(folder, "placement.json"), marker, 0o600))
	registry, err := json.Marshal(map[string]any{
		"format_version": 1,
		"folders": []map[string]any{
			{"id": "here", "path": folder, "volume_root": filepath.Dir(folder), "kind": "volume_root"},
			{"id": "gone", "path": filepath.Join(t.TempDir(), "absent"), "volume_root": "/absent", "kind": "volume_root"},
		},
		"branches": map[string]string{"0123456789abcdef/here": "here", "0123456789abcdef/gone": "gone"},
	})
	testutil.FailErr(t, "encode registry", err)
	testutil.FailErr(t, "write registry", os.WriteFile(filepath.Join(state, enginepaths.WorkerPlacementsFileName), registry, 0o600))
	return folder
}

func TestWorkerBranchesBucketWithoutAnEngineFindsPlacedBranches(t *testing.T) {
	state := t.TempDir()
	folder := registerPlacements(t, state)
	registry, err := New(state)
	testutil.FailErr(t, "registry", err)
	status, err := registry.Status(t.Context(), BucketWorkerBranches)
	testutil.FailErr(t, "status", err)
	if !status.Present || status.Bytes != 5 || status.Unavailable != 1 {
		t.Fatalf("status = %+v", status)
	}
	testutil.FailErr(t, "clear", registry.ClearOne(t.Context(), BucketWorkerBranches))
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Fatalf("clear left the reachable folder: %v", err)
	}
	status, err = registry.Status(t.Context(), BucketWorkerBranches)
	testutil.FailErr(t, "status after clear", err)
	if status.Present || status.Unavailable != 1 {
		t.Fatalf("status after clear = %+v", status)
	}
}

func TestStoreResetPurgesReachablePlacementsAndKeepsAbsentRecords(t *testing.T) {
	state := t.TempDir()
	folder := registerPlacements(t, state)
	testutil.FailErr(t, "reset", ResetStoreCoupled(t.Context(), filepath.Join(state, storeFileName)))
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Fatalf("reset left the reachable folder: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(state, enginepaths.WorkerPlacementsFileName))
	testutil.FailErr(t, "read registry", err)
	var kept struct {
		Folders []struct {
			ID string `json:"id"`
		} `json:"folders"`
	}
	testutil.FailErr(t, "decode registry", json.Unmarshal(raw, &kept))
	if len(kept.Folders) != 1 || kept.Folders[0].ID != "gone" {
		t.Fatalf("registry after reset = %s", raw)
	}
}
