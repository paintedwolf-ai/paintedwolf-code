package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// One read can hold several records; each wait takes the first match and
// leaves the records after it for the next wait.
func TestCaptureWaitsKeepRecordsAfterTheirMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "performance.jsonl")
	testutil.FailErr(t, "write capture", os.WriteFile(path, []byte(
		`{"kind":"work","operation":"app.boot","work":{"statements":3}}`+"\n"+
			`{"kind":"runtime","runtime":{"gauges":{"store_maintenance_drains_done":1}}}`+"\n"+
			`{"kind":"work","operation":"project.remove","work":{"rows_changed":7}}`+"\n"), 0o600))
	// An exited engine ends any wait, so a dropped record fails instead of waiting.
	exited := make(chan struct{})
	close(exited)
	engine := &scaleEngine{exited: exited, exitErr: errors.New("exited"), logs: dir}
	capture := &captureTail{path: path}
	ctx := context.Background()

	boot, err := capture.work(ctx, engine, "app.boot")
	testutil.FailErr(t, "boot record", err)
	gauges, err := capture.gauges(ctx, engine, func(g map[string]int64) bool { return g["store_maintenance_drains_done"] > 0 })
	testutil.FailErr(t, "runtime record after boot", err)
	removal, err := capture.work(ctx, engine, "project.remove")
	testutil.FailErr(t, "removal record after runtime", err)
	if boot["statements"] != 3 || gauges["store_maintenance_drains_done"] != 1 || removal["rows_changed"] != 7 {
		t.Fatalf("boot %v gauges %v removal %v", boot, gauges, removal)
	}
	if _, err := capture.work(ctx, engine, "app.boot"); err == nil {
		t.Fatal("a consumed record matched a second wait")
	}
}
