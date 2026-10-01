//go:build integration

package extensionstate_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConcurrentMutationsNoLostUpdate(t *testing.T) {
	testutil.SkipIfShort(t, "concurrent full-catalog mutation stress")
	owner := testOwner(t)

	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := applyWithRetry(t, owner, deviceScope(),
				extensionstate.SetUnitDisabledOp{UnitID: stockUnitIDAt(t, i), Disabled: true})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		testutil.FailErr(t, "concurrent mutation", err)
	}

	path, err := extpacks.DeviceDesiredPath()
	testutil.FailErr(t, "device path", err)
	d, err := extpacks.LoadDesiredFile(path)
	testutil.FailErr(t, "load desired", err)

	got := map[string]bool{}
	for _, id := range d.Disabled {
		got[id] = true
	}
	for i := range n {
		if !got[stockUnitIDAt(t, i)] {
			t.Fatalf("lost update: %s missing from %v (%d of %d present)",
				stockUnitIDAt(t, i), d.Disabled, len(d.Disabled), n)
		}
	}
}

// Project overlays retain only committed state files.
func TestProjectMutationLeavesOnlyStateFilesInOverlay(t *testing.T) {
	owner := testOwner(t)
	projectDir := t.TempDir()
	result := apply(t, owner, projectScope(projectDir),
		extensionstate.SetUnitDisabledOp{UnitID: "workflows/plan", Disabled: true})

	entries, err := os.ReadDir(filepath.Dir(result.DesiredPath))
	testutil.FailErr(t, "read project overlay", err)
	for _, e := range entries {
		if e.Name() != extpacks.DeviceDesiredName {
			t.Fatalf("lock or temp artifact leaked into the committed overlay: %s", e.Name())
		}
	}
}
