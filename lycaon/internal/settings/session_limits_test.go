package settings_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
)

func bundledSessionYAML(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "config", "packs", "painted-wolf", "platform", "host", "session.yaml")
}

func TestDefaultSessionLimitsMatchesBundledYAML(t *testing.T) {
	def := settings.DefaultSessionLimits()
	cfg, err := settings.LoadSessionLimits(bundledSessionYAML(t))
	testutil.FailErr(t, "settings.LoadSessionLimits failed", err)
	if def.MaxIterations != cfg.MaxIterations {
		t.Fatalf("max_iterations default = %d bundled = %d", def.MaxIterations, cfg.MaxIterations)
	}
	if def.MaxToolResultBytes != cfg.MaxToolResultBytes {
		t.Fatalf("max_tool_result_bytes default = %d bundled = %d", def.MaxToolResultBytes, cfg.MaxToolResultBytes)
	}
	if def.MaxToolSpillBytes != cfg.MaxToolSpillBytes {
		t.Fatalf("max_tool_spill_bytes default = %d bundled = %d", def.MaxToolSpillBytes, cfg.MaxToolSpillBytes)
	}
}

func TestLoadSessionLimitsFromBundledYAML(t *testing.T) {
	cfg, err := settings.LoadSessionLimits(bundledSessionYAML(t))
	testutil.FailErr(t, "settings.LoadSessionLimits failed", err)
	if cfg.MaxIterations != 500 {
		t.Fatalf("max_iterations = %d", cfg.MaxIterations)
	}
	// Bundled session.yaml omits continuous budget fields; zeros
	// mean "derive at resolve time".
	if cfg.MaxToolResultBytes != 0 {
		t.Fatalf("max_tool_result_bytes = %d want 0 (derive later)", cfg.MaxToolResultBytes)
	}
	if cfg.MaxCoordinatorLoopCycles != 0 {
		t.Fatalf("max_coordinator_loop_cycles = %d want 0 (derive later)", cfg.MaxCoordinatorLoopCycles)
	}
	derived := settings.ApplyDerivedSessionLimits(cfg, llm.ScaleLimitsFromTrueWindow(262144))
	if derived.MaxToolResultBytes != 524288 {
		t.Fatalf("derived max_tool_result_bytes = %d want 524288", derived.MaxToolResultBytes)
	}
	if derived.MaxCoordinatorLoopCycles != 256 {
		t.Fatalf("derived max_coordinator_loop_cycles = %d want 256", derived.MaxCoordinatorLoopCycles)
	}
	if cfg.MaxToolSpillBytes != 67108864 {
		t.Fatalf("max_tool_spill_bytes = %d", cfg.MaxToolSpillBytes)
	}
	if cfg.OverlayPromoteMaxIterations != 150 {
		t.Fatalf("overlay_promote_max_iterations = %d want 150", cfg.OverlayPromoteMaxIterations)
	}
	if cfg.SpendWarningRatio != 0.8 {
		t.Fatalf("spend_warning_ratio = %v want 0.8", cfg.SpendWarningRatio)
	}
	if !cfg.SpendSoftStopEnabled() {
		t.Fatal("spend soft stop should default on")
	}
	if cfg.WorkerToolBudgetDefault != spawn.DefaultWorkerMaxToolLoops {
		t.Fatalf("worker default = %d want %d", cfg.WorkerToolBudgetDefault, spawn.DefaultWorkerMaxToolLoops)
	}
	// llm_turn_timeout_sec measures from the send, so an approval hold does not
	// spend it and the ceiling stays hour-scale. Hung streams hit the stall
	// guard first.
	const wantLLMTurnSec = 4 * 60 * 60
	if cfg.LLMTurnTimeoutSec != wantLLMTurnSec {
		t.Fatalf("llm_turn_timeout_sec = %d want %d", cfg.LLMTurnTimeoutSec, wantLLMTurnSec)
	}
	const wantTimeoutSec = 3 * 60 * 60
	if cfg.CoordinatorMaxSleepSec != wantTimeoutSec {
		t.Fatalf("coordinator_max_sleep_sec = %d want %d", cfg.CoordinatorMaxSleepSec, wantTimeoutSec)
	}
	// coordinator_host_turn_timeout_sec wraps every tool iteration of one host turn,
	// so it is day-scale: it must outlast max_iterations rounds of real work and only
	// catches a wedged turn. Per-call hangs are caught by the stream stall guard.
	const wantHostTurnSec = 24 * 60 * 60
	if cfg.CoordinatorHostTurnTimeoutSec != wantHostTurnSec {
		t.Fatalf("coordinator_host_turn_timeout_sec = %d want %d", cfg.CoordinatorHostTurnTimeoutSec, wantHostTurnSec)
	}
	// await_parent_workers_timeout_sec is lower than the host turn cap
	// but sized above the wall clock a worker spending its full default tool budget
	// needs, so a busy worker is not reported unfinished while still working.
	const wantAwaitWorkersSec = 60 * 60
	if cfg.AwaitParentWorkersTimeoutSec != wantAwaitWorkersSec {
		t.Fatalf("await_parent_workers_timeout_sec = %d want %d", cfg.AwaitParentWorkersTimeoutSec, wantAwaitWorkersSec)
	}
}

func TestMergeSessionLimitsProjectOverlay(t *testing.T) {
	base := settings.DefaultSessionLimits()
	overlay := settings.SessionLimits{MaxIterations: 3, SpendWarningRatio: 0.65}
	merged := settings.MergeSessionLimits(base, overlay)
	if merged.MaxIterations != 3 {
		t.Fatalf("max_iterations = %d want 3", merged.MaxIterations)
	}
	if merged.SpendWarningRatio != 0.65 {
		t.Fatalf("spend_warning_ratio = %v want 0.65", merged.SpendWarningRatio)
	}
}

func TestNormalizeSessionLimitsCapsWorkerToolBudget(t *testing.T) {
	merged := settings.MergeSessionLimits(settings.DefaultSessionLimits(), settings.SessionLimits{
		WorkerToolBudgetDefault: 180,
		WorkerToolBudgetMin:     2,
		WorkerToolBudgetMax:     200,
	})
	if merged.WorkerToolBudgetMax != spawn.DefaultWorkerToolBudgetMax {
		t.Fatalf("max = %d want %d", merged.WorkerToolBudgetMax, spawn.DefaultWorkerToolBudgetMax)
	}
	if merged.WorkerToolBudgetDefault != spawn.DefaultWorkerToolBudgetMax {
		t.Fatalf("default = %d want %d", merged.WorkerToolBudgetDefault, spawn.DefaultWorkerToolBudgetMax)
	}
}

func TestMergeSessionLimitsSoftStopCanOnlyTighten(t *testing.T) {
	base := settings.DefaultSessionLimits()
	off := false
	merged := settings.MergeSessionLimits(base, settings.SessionLimits{SpendSoftStop: &off})
	if merged.SpendSoftStopEnabled() {
		t.Fatal("overlay should be able to require an immediate closeout")
	}
	on := true
	merged = settings.MergeSessionLimits(merged, settings.SessionLimits{SpendSoftStop: &on})
	if merged.SpendSoftStopEnabled() {
		t.Fatal("lower layer must not re-enable a device hard stop")
	}
}

func TestProjectSpendOverlayOwnsOnlyATighterGuardrail(t *testing.T) {
	global := settings.DefaultSessionLimits()
	global.SpendCeilingEnabled = true
	global.SessionSpendCeilingUSD = 10
	global.SpendWarningRatio = 0.8

	request := global
	request.MaxIterations = 3
	request.SessionSpendCeilingUSD = 5
	request.SpendWarningRatio = 0.6
	overlay := settings.ProjectSpendOverlay(request, global)
	if overlay.MaxIterations != 0 {
		t.Fatalf("project overlay captured max_iterations = %d", overlay.MaxIterations)
	}
	if !overlay.SpendCeilingEnabled || overlay.SessionSpendCeilingUSD != 5 || overlay.SpendWarningRatio != 0.6 {
		t.Fatalf("project guardrail overlay = %+v", overlay)
	}
	if merged := settings.MergeSessionLimits(global, overlay); merged.SessionSpendCeilingUSD != 5 {
		t.Fatalf("effective ceiling = %v want 5", merged.SessionSpendCeilingUSD)
	}
}

func TestProjectSpendOverlayClearsDisabledGuardrail(t *testing.T) {
	global := settings.DefaultSessionLimits()
	global.SpendCeilingEnabled = true
	global.SessionSpendCeilingUSD = 10
	request := global
	request.SpendCeilingEnabled = false
	request.SessionSpendCeilingUSD = 0
	if overlay := settings.ProjectSpendOverlay(request, global); overlay != (settings.SessionLimits{}) {
		t.Fatalf("disabled project guardrail overlay = %+v want empty", overlay)
	}
}

func TestSessionLimitsAccessorsDefaultTrue(t *testing.T) {
	lim := settings.SessionLimits{}
	if !lim.CoordinatorLoopEnabled() {
		t.Fatal("coordinator loop should default on")
	}
	if lim.EffectiveMaxCoordinatorLoopCycles() != 256 {
		t.Fatalf("max auto continue = %d want 256", lim.EffectiveMaxCoordinatorLoopCycles())
	}
}

func TestLoadSessionLimitsMissingFileUsesDefaults(t *testing.T) {
	cfg, err := settings.LoadSessionLimits(filepath.Join(t.TempDir(), "missing.yaml"))
	testutil.FailErr(t, "settings.LoadSessionLimits failed", err)
	if cfg.MaxIterations != settings.DefaultSessionLimits().MaxIterations {
		t.Fatal("missing file should return defaults")
	}
}

func TestLoadSessionLimitsRejectsInvalidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("max_iterations: not-a-number\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if _, err := settings.LoadSessionLimits(path); err == nil {
		t.Fatal("expected parse error")
	}
}
