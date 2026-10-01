package workflow_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestBundledImplementManifestLoadValidation(t *testing.T) {
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	impl, err := manifests.Get("implement", "1.0.0")
	testutil.FailErr(t, "Get implement", err)
	if impl.Attach.Policy != workflowdef.AttachPolicySessionCreate {
		t.Fatalf("attach.policy = %q want session_create", impl.Attach.Policy)
	}
	if impl.IsCatalogVisible() {
		t.Fatal("implement must not be catalog-visible")
	}
	if impl.SurfaceProfile != "implement" {
		t.Fatalf("surface_profile = %q want implement", impl.SurfaceProfile)
	}
	work, ok := impl.PhaseByID("work")
	if !ok || work.Next != "work" {
		t.Fatalf("work phase = %+v", work)
	}
	if work.OnReenter.InjectKick != "worker.task.finished" {
		t.Fatalf("work on_reenter inject_kick = %q", work.OnReenter.InjectKick)
	}
	if work.OnReenter.ReenterLeg != "implement-work:{session_id}" {
		t.Fatalf("work on_reenter reenter_leg = %q", work.OnReenter.ReenterLeg)
	}
	if impl.Controls.PhaseAdvance != workflowdef.PhaseAdvanceHost {
		t.Fatalf("controls.phase_advance = %q want host", impl.Controls.PhaseAdvance)
	}
	boot, ok := impl.PhaseByID("boot")
	if !ok {
		t.Fatal("missing boot phase")
	}
	assertImplementParallelTaskCaps(t, "boot", boot.ParallelTask)
	if boot.Next != "work" {
		t.Fatalf("boot.next = %q want work", boot.Next)
	}
	assertImplementParallelTaskCaps(t, "work", work.ParallelTask)
}

func assertImplementParallelTaskCaps(t *testing.T, phaseID string, pt *workflowdef.ParallelTask) {
	t.Helper()
	if pt == nil {
		t.Fatalf("%s phase missing parallel_task", phaseID)
	}
	if pt.MaxWorkers != spawn.MaxInFlightTaskWorkers {
		t.Fatalf("%s max_workers = %d want %d", phaseID, pt.MaxWorkers, spawn.MaxInFlightTaskWorkers)
	}
	if pt.MaxReadWorkers != spawn.DefaultMaxReadTaskWorkers {
		t.Fatalf("%s max_read_workers = %d want %d", phaseID, pt.MaxReadWorkers, spawn.DefaultMaxReadTaskWorkers)
	}
	if pt.MaxWriteWorkers != spawn.DefaultMaxWriteTaskWorkers {
		t.Fatalf("%s max_write_workers = %d want %d", phaseID, pt.MaxWriteWorkers, spawn.DefaultMaxWriteTaskWorkers)
	}
}
