//go:build budgets

package contract

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

func measureCoordinatorInjectSizes(
	t *testing.T,
	root string,
	engine *prompts.FileTemplateEngine,
	out map[string]int,
) {
	t.Helper()
	lycaonRoot := filepath.Join(root, "lycaon")
	renderer := prompts.NewInjectRenderer(engine)
	for _, spec := range PromptBudgetInjectMatrix {
		out[spec.ID] = len(measureCoordinatorInjectBlock(t, lycaonRoot, renderer, spec))
	}
}

func measureCoordinatorInjectBlock(
	t *testing.T,
	lycaonRoot string,
	renderer *prompts.InjectRenderer,
	spec PromptBudgetInjectSpec,
) string {
	t.Helper()
	ctx := context.Background()
	switch spec.ID {
	case "tool_procedures":
		block, err := inject.RenderToolProceduresBlock(ctx, renderer, "sess-inject-test", "coordinator",
			[]string{"command", "verify", "terminal_open", "http_request", "capture_page", "page_open", "page_snapshot", "page_act", "page_close", "measure_page", "render_view"}, nil)
		contractcheck.FailErr(t, "render tool procedures", err)
		return block
	case "command_jobs", "worker_command_jobs":
		now := time.Date(2026, 8, 1, 21, 0, 0, 0, time.UTC)
		jobs := make([]bgprocess.JobSnapshot, 0, 8)
		for i := 0; i < 8; i++ {
			mode := bgprocess.JobModeBackground
			timeout := time.Duration(0)
			if i < bgprocess.DefaultMaxAwaited {
				mode = bgprocess.JobModeAwaited
				timeout = 10 * time.Minute
			}
			jobs = append(jobs, bgprocess.JobSnapshot{
				Handle: fmt.Sprintf("command-%d", i), Mode: mode, OriginTool: "verify",
				StartedAt: now.Add(-time.Duration(i+1) * time.Minute), Timeout: timeout,
				Stages: []hostcmd.StageResult{{
					// A live job has not exited, so it carries no exit code.
					Command: "./task test:digest -- ./internal/" + strings.Repeat("wide-scope/", 80),
				}},
			})
		}
		surface := "coordinator"
		if spec.ID == "worker_command_jobs" {
			surface = "worker"
		}
		return inject.RenderCommandJobsBlock(ctx, renderer, "sess-inject-test", jobs, nil, now, surface)
	case "source_changes":
		brief := inject.SourceChangeBrief{Truncated: true, OtherFiles: 240, OtherEffects: 999}
		for i := 0; i < 20; i++ {
			brief.Files = append(brief.Files, inject.SourceChangeFile{
				Path:    fmt.Sprintf("@secondary/src/components/project/%02d/ReviewDiffViewLongName.tsx", i),
				Actor:   "agent",
				Detail:  "long-lived refactor worker",
				Op:      "rename",
				At:      "14:02 UTC",
				Effects: 12,
			})
		}
		for i := 0; i < 10; i++ {
			brief.Git = append(brief.Git, inject.SourceGitLine{
				Root: "@secondary", Kind: "checkout",
				FromRef: "feature/long-running-provenance-branch", ToRef: "main",
				FromCommit: "abc123def456", ToCommit: "fedcba654321",
				Detail: "Merge branch 'feature/long-running-provenance-branch' into main",
				At:     "14:02 UTC",
			})
		}
		return inject.RenderSourceChangesBlock(ctx, renderer, "sess-inject-test", brief)
	case "implement_spawn":
		block, err := inject.RenderImplementSpawnInject(
			ctx, renderer, "sess-inject-test", spawn.AmbientAllowedAgents(), spawn.MaxInFlightTaskWorkers, spawn.SurfaceImplementSynthesis, 1, false, true,
			nil, nil)
		contractcheck.FailErr(t, "RenderImplementSpawnInject", err)
		return block
	case "worker_task_assignment":
		block, err := inject.RenderWorkerTaskAssignment(ctx, renderer, inject.WorkerTaskAssignmentInput{
			SessionID:  "sess-inject-test",
			ProjectDir: t.TempDir(),
			Charter: api.WorkerTaskCharter{
				Goal: "Implement login fix", DoneWhen: []string{"The scoped auth behavior is corrected."},
			},
			AgentType:    "implementer",
			WorkerJobID:  "job-budget-fixture",
			Scope:        api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"internal/auth/**"}},
			MaxToolLoops: 8,
		})
		contractcheck.FailErr(t, "RenderWorkerTaskAssignment", err)
		return block
	case "worker_task_preamble":
		block, err := worker.RenderWorkerTaskPreamble(ctx, renderer, "sess-inject-test", lycaonRoot,
			[]string{"internal/**", "lycaon/**"},
			&api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"internal/auth/**"}},
		)
		contractcheck.FailErr(t, "RenderWorkerTaskPreamble", err)
		return block
	case "worker_leg":
		block, err := inject.RenderWorkerLegInject(ctx, renderer, "sess-inject-test", promptBudgetWorkerLegFixture())
		contractcheck.FailErr(t, "RenderWorkerLegInject", err)
		return block
	case "active_workflow":
		hintCfg := &guidance.HintConfig{HintCodes: map[string]guidance.HintEntry{
			"WORKFLOW_GATE_UNMET": {Message: "gate blocked", Fix: "fix gates"},
		}}
		gateCfg, err := feedback.LoadGateFeedbackCatalog()
		contractcheck.FailErr(t, "LoadGateFeedbackCatalog", err)
		block, err := inject.RenderActiveWorkflowInject(
			ctx, renderer, "sess-inject-test",
			inject.CoordinatorTurnFrame{RunContext: promptBudgetActiveWorkflowFixture(), Runtime: promptBudgetWorkflowSnapshotFixture()},
			hintCfg, []string{"WORKFLOW_GATE_UNMET"}, gateCfg,
		)
		contractcheck.FailErr(t, "RenderActiveWorkflowInject", err)
		return largestCatalogPhaseInject(t, renderer, hintCfg, gateCfg, block)
	case "board_orientation":
		now := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
		block, err := inject.RenderBoardOrientationInject(
			ctx, renderer, "sess-inject-test", fatLangSnapshot(), packboard.InjectScopeFull, false, true, now,
		)
		contractcheck.FailErr(t, "RenderBoardOrientationInject", err)
		return block
	default:
		t.Fatalf("PromptBudgetInjectMatrix missing measure hook for %q — add case in measureCoordinatorInjectBlock", spec.ID)
		return ""
	}
}

// Measure live phase-exit shapes as well as the baseline research fixture, so
// required verdict fields cannot grow outside the measured prompt surface.
func largestCatalogPhaseInject(t *testing.T, renderer *prompts.InjectRenderer, hints *guidance.HintConfig, gates *feedback.GateFeedbackCatalog, largest string) string {
	t.Helper()
	manifests, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "load phase budget catalog", err)
	for _, manifest := range manifests {
		rows := make([]inject.WorkflowPhaseRow, 0, len(manifest.PhaseDefs))
		for _, phase := range manifest.PhaseDefs {
			rows = append(rows, inject.WorkflowPhaseRow{ID: phase.ID, CompleteWhen: phase.CompleteWhen, Next: phase.Next, Terminal: phase.Terminal})
		}
		for _, phase := range manifest.PhaseDefs {
			frame := inject.CoordinatorTurnFrame{
				RunContext: api.CoordinatorRunContext{WorkflowID: manifest.ID, CurrentPhase: phase.ID, RunID: "budget-run", RunStatus: "running"},
				Runtime:    inject.WorkflowRuntimeSnapshot{Phases: rows, PhaseExit: workflow.ProjectPhaseExit(manifest, phase, nil, nil).InjectView()},
			}
			block, err := inject.RenderActiveWorkflowInject(t.Context(), renderer, "sess-inject-test", frame, hints, nil, gates)
			contractcheck.FailErr(t, "render catalog phase budget", err)
			if len(block) > len(largest) {
				largest = block
			}
		}
	}
	return largest
}
