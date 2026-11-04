// Program mountholder is a real process that serves a real view mount and uses it, for the teardown
// tests in scripts/verification_tests: it reports its mount, then runs until stop appears or it is
// killed. Unlike a test binary, it has nothing else that could hold the mount.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

type report struct {
	PID   int    `json:"pid"`
	Mount string `json:"mount"`
}

func main() {
	if len(os.Args) != 4 {
		fail("usage", fmt.Errorf("mountholder <dir> <ready> <stop>"))
	}
	dir, ready, stop := os.Args[1], os.Args[2], os.Args[3]
	trunk := filepath.Join(dir, "trunk")
	if err := os.MkdirAll(trunk, 0o755); err != nil {
		fail("mkdir trunk", err)
	}
	if err := os.WriteFile(filepath.Join(trunk, "f"), []byte("x"), 0o644); err != nil {
		fail("seed trunk", err)
	}
	request := adapter.PrepareRequest{
		ViewID: "view-holder", BackingDir: filepath.Join(dir, "backing"), RuntimeDir: filepath.Join(dir, "runtime"),
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunk, IsPrimary: true}},
	}
	adp, view, useMount, err := prepareView(request)
	if err != nil {
		fail("prepare", err)
	}
	raw, err := json.Marshal(report{PID: os.Getpid(), Mount: view.PrimaryWorkPath})
	if err != nil {
		fail("report", err)
	}
	if err := os.WriteFile(ready+".tmp", raw, 0o600); err != nil {
		fail("write report", err)
	}
	if err := os.Rename(ready+".tmp", ready); err != nil {
		fail("publish report", err)
	}
	if useMount != nil && os.Getenv("MOUNTHOLDER_WEDGE") != "" {
		useMount(view)
	}
	for {
		if _, err := os.Stat(stop); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	release(adp, view)
}

func fail(step string, err error) {
	fmt.Fprintf(os.Stderr, "mountholder: %s: %v\n", step, err)
	os.Exit(1)
}
