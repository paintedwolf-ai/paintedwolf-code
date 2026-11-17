package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
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
