package contract

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/spawn"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCoordinatorSurfaceInvariants_overlayPromoteIncludesWait(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	promote := surfaces[surfaceImplementOverlayPromote]
	if !contractcheck.ContainsString(promote, "wait") {
		t.Fatalf("%s must include wait for wait(resume=true) while siblings in flight", surfaceImplementOverlayPromote)
	}
}

func TestCoordinatorSurfaceInvariants_taskIncludesWorkerLifecycle(t *testing.T) {
	t.Parallel()
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile coordinator tool plans", err)
	for id, plan := range plans {
		if !plan.Addressable("task") {
			continue
		}
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			for _, tool := range orchestrationLifecycleTools {
				if !plan.Addressable(tool) {
					t.Errorf("surface %s can dispatch workers but cannot manage their lifecycle: missing %s", id, tool)
				}
				if plan.Deferred(tool) && !plan.Immediate("request_tools") {
					t.Errorf("surface %s defers %s without offering request_tools", id, tool)
				}
			}
		})
	}
}

func TestCoordinatorSurfaceInvariants_orchestrationLifecycleOnPinnedSurfaces(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	for _, id := range orchestrationPinnedSurfaces {
		wire := surfaces[id]
		for _, tool := range orchestrationLifecycleTools {
			if !contractcheck.ContainsString(wire, tool) {
				t.Fatalf("%s must include orchestration lifecycle tool %q — host can pin this surface while coordinator-core references it", id, tool)
			}
		}
	}
}

// Closeout surfaces are catalogued.
func TestCoordinatorCloseoutSurfacesTrackYAMLSSOT(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)

	closeoutIDs := []string{toolcontract.SurfaceImplementInvestigate, spawn.SurfaceImplementSynthesis}
	for _, id := range closeoutIDs {
		if !surface.SurfaceDeliversReport(id) {
			t.Fatalf("expected %q to be a coordinator closeout surface", id)
		}
		if _, ok := surfaces[id]; !ok {
			t.Fatalf("closeout surface %q is not defined in coordinator-surfaces.yaml — a rename must update surface.SurfaceDeliversReport", id)
		}
	}

	assertDenNamesNoCloseoutSurface(t, closeoutIDs)
}

// Den receives closeout state through loop progress.
func assertDenNamesNoCloseoutSurface(t *testing.T, closeoutIDs []string) {
	t.Helper()
	denSrc := filepath.Join(contractcheck.RepoRoot(t), "lycaon-den", "src")
	var offenders []string
	err := filepath.WalkDir(denSrc, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".ts") && !strings.HasSuffix(name, ".tsx") {
			return nil
		}
		if strings.Contains(name, ".test.") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, id := range closeoutIDs {
			if strings.Contains(string(body), `"`+id+`"`) {
				rel, _ := filepath.Rel(denSrc, path)
				offenders = append(offenders, rel+" names "+id)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk den src", err)
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("Den must not name a coordinator closeout surface — branch on CoordinatorLoopProgress.provisional_hidden instead:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
