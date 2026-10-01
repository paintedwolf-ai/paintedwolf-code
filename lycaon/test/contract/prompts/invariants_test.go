package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestDoomLoopThreshold(t *testing.T) {
	t.Parallel()
	if loopguard.DoomLoopMaxAttempts != 8 {
		t.Fatalf("DoomLoopMaxAttempts = %d, want 8", loopguard.DoomLoopMaxAttempts)
	}
}

func TestWorkerMaxToolLoopsHostCap(t *testing.T) {
	t.Parallel()
	budget := spawn.DefaultWorkerToolBudget()
	if budget.Min < spawn.WorkerToolBudgetFloor || budget.Min > budget.Default || budget.Default > budget.Max {
		t.Fatalf("worker tool budget violates floor(%d) ≤ min(%d) ≤ default(%d) ≤ max(%d)",
			spawn.WorkerToolBudgetFloor, budget.Min, budget.Default, budget.Max)
	}
	if budget.Max <= budget.Default {
		t.Fatalf("budget max %d must exceed the default %d so a coordinator can grant more", budget.Max, budget.Default)
	}
}

func TestWorkerRunningCap(t *testing.T) {
	t.Parallel()
	if worker.MaxWorkers != 48 {
		t.Fatalf("MaxWorkers = %d, want 48", worker.MaxWorkers)
	}
}

func TestMaxWorkerSummaryChars(t *testing.T) {
	t.Parallel()
	cfg := compaction.DefaultCompactionConfig()
	windows := modelinfo.DefaultModelContextWindows()
	live, _, err := llm.ApplyLiveBudget(cfg, llm.ModelPolicy{}, nil, windows)
	contractcheck.FailErr(t, "llm.ApplyLiveBudget failed", err)
	if live.MaxWorkerSummaryChars != limits.DefaultWorkerSummaryMaxChars {
		t.Fatalf("MaxWorkerSummaryChars = %d, want %d", live.MaxWorkerSummaryChars, limits.DefaultWorkerSummaryMaxChars)
	}
}

func TestBoardCharBudgets(t *testing.T) {
	t.Parallel()
	if api.MaxBoardInjectChars != 320 {
		t.Fatalf("MaxBoardInjectChars = %d, want 320", api.MaxBoardInjectChars)
	}
	if api.MaxBoardCompactChars != 480 {
		t.Fatalf("MaxBoardCompactChars = %d, want 480", api.MaxBoardCompactChars)
	}
	if api.MaxBoardDetailChars != 1200 {
		t.Fatalf("MaxBoardDetailChars = %d, want 1200", api.MaxBoardDetailChars)
	}
}

func TestEvidencePathFormat(t *testing.T) {
	t.Parallel()
	root := inspector.DefaultEvidenceDir
	path := inspector.EvidencePath(root, "dep-1", "task-1", evidence.GateTypeVerify)
	want := inspector.DefaultEvidenceDir + "/dep-1/task-1/verify.jsonl"
	if path != want {
		t.Fatalf("EvidencePath = %q, want %q", path, want)
	}
	if !strings.HasPrefix(path, inspector.DefaultEvidenceDir) {
		t.Fatalf("path should be under %s", inspector.DefaultEvidenceDir)
	}
	if !strings.HasSuffix(path, ".jsonl") {
		t.Fatal("path should end with .jsonl")
	}
}
