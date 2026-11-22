package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

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
