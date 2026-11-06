package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPlacementsListsAndPurgesWithConfirmation(t *testing.T) {
	state := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, state)
	root, err := configdir.UserConfigDir()
	testutil.FailErr(t, "state root", err)
	testutil.FailErr(t, "create state", os.MkdirAll(root, 0o700))
	reachable := filepath.Join(t.TempDir(), enginepaths.WorkerPlacementDirName(1, root))
	absent := filepath.Join(t.TempDir(), "gone", enginepaths.WorkerPlacementDirName(1, root))
	testutil.FailErr(t, "create folder", os.MkdirAll(reachable, 0o700))
	marker, err := json.Marshal(map[string]any{"format_version": 1, "id": "here", "state_key": enginepaths.ProjectKey(root)})
	testutil.FailErr(t, "encode marker", err)
	testutil.FailErr(t, "write marker", os.WriteFile(filepath.Join(reachable, "placement.json"), marker, 0o600))
	registry, err := json.Marshal(map[string]any{
		"format_version": 1,
		"folders": []map[string]any{
			{"id": "here", "path": reachable, "volume_root": filepath.Dir(reachable), "kind": "volume_root"},
			{"id": "gone", "path": absent, "volume_root": filepath.Dir(absent), "kind": "volume_root"},
		},
		"branches": map[string]string{"k/j": "gone"},
	})
	testutil.FailErr(t, "encode registry", err)
	testutil.FailErr(t, "write registry", os.WriteFile(filepath.Join(root, enginepaths.WorkerPlacementsFileName), registry, 0o600))

	var out bytes.Buffer
	testutil.FailErr(t, "list", runPlacements(t.Context(), []string{"list"}, &out))
	if !strings.Contains(out.String(), "available\t"+reachable) || !strings.Contains(out.String(), "unavailable\t"+absent) {
		t.Fatalf("list = %q", out.String())
	}
	if err := runPlacements(t.Context(), []string{"purge"}, &out); err == nil {
		t.Fatal("purge ran without --yes")
	}
	if _, err := os.Stat(reachable); err != nil {
		t.Fatalf("refused purge removed the folder: %v", err)
	}
	out.Reset()
	testutil.FailErr(t, "purge", runPlacements(t.Context(), []string{"purge", "--yes"}, &out))
	if !strings.Contains(out.String(), "removed\t"+reachable) || !strings.Contains(out.String(), "unavailable\t"+absent) {
		t.Fatalf("purge = %q", out.String())
	}
	if _, err := os.Stat(reachable); !os.IsNotExist(err) {
		t.Fatalf("folder remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, enginepaths.WorkerPlacementsFileName)); err != nil {
		t.Fatalf("the record of the absent folder is gone: %v", err)
	}
}
