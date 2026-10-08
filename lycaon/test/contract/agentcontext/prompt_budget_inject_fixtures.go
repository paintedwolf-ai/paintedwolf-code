package contract

import "github.com/lycaon/lycaon/internal/promptunit"

// PromptBudgetInjectSpec is one coordinator_injects budget fixture (SSOT for measure + catalog).
type PromptBudgetInjectSpec struct {
	ID        string
	Template  string // under config/prompts/, e.g. inject/implement-spawn.md
	Fixture   string
	ExtraTrim []string
	Advice    string
	// Hosts lists the turn kinds the inject can ride, so a turn's static
	// stack adds only the injects that can arrive together.
	Hosts []promptunit.Host
}

var (
	coordinatorTurns = []promptunit.Host{promptunit.HostCoordinator}
	workerTurns      = []promptunit.Host{promptunit.HostWorker}
	everyTurn        = []promptunit.Host{promptunit.HostCoordinator, promptunit.HostWorker}
)

// PromptBudgetInjectMatrix is the SSOT for coordinator inject budget fixtures.
var PromptBudgetInjectMatrix = []PromptBudgetInjectSpec{
	{
		ID:       "tool_procedures",
		Hosts:    everyTurn,
		Template: "guidance/tool-procedures.md",
		Fixture:  "RenderToolProceduresBlock with command, verify, terminal, HTTP, and browser tools offered",
		Advice:   "Per-call operating guidance — measure separately from the initial prompt.",
	},
	{
		ID:       "command_jobs",
		Hosts:    coordinatorTurns,
		Template: "inject/command-jobs.md",
		Fixture:  "RenderCommandJobsBlock with awaited and background jobs",
		Advice:   "Command ledger growth — keep lifecycle state concise and host-authoritative.",
	},
	{
		ID:       "worker_command_jobs",
		Hosts:    workerTurns,
		Template: "inject/worker-command-jobs.md",
		Fixture:  "RenderCommandJobsBlock worker surface with awaited and background jobs",
		Advice:   "Worker command ledger growth — keep lifecycle state concise and host-authoritative.",
	},
	{
		ID:       "source_changes",
		Hosts:    coordinatorTurns,
		Template: "guidance/source-changes.md",
		Fixture:  "RenderSourceChangesBlock at the working-set row cap with elsewhere counts",
		Advice:   "Change-brief growth — keep rows single-line and the itemized set capped.",
	},
	{
		ID:       "implement_spawn",
		Hosts:    coordinatorTurns,
		Template: "inject/implement-spawn.md",
		Fixture:  "RenderImplementSpawnInject with full default agent allowlist",
		ExtraTrim: []string{
			"lycaon/internal/coordinator/inject/implement_spawn_inject.go",
		},
		Advice: "Keep the roster and spawn policy to what the coordinator needs to choose an agent.",
	},
	{
		ID:       "worker_task_assignment",
		Hosts:    workerTurns,
		Template: "inject/worker-task-assignment.md",
		Fixture:  "RenderWorkerTaskAssignment with write scope and bounded coverage review",
	},
	{
		ID:       "worker_task_preamble",
		Hosts:    workerTurns,
		Template: "inject/worker-task-preamble.md",
		Fixture:  "RenderWorkerTaskPreamble with write scope + filtered touch paths",
	},
	{
		ID:       "worker_leg",
		Hosts:    workerTurns,
		Template: "inject/worker-leg.md",
		Fixture:  "promptBudgetWorkerLegFixture (path-explorer leg)",
	},
	{
		ID:       "active_workflow",
		Hosts:    coordinatorTurns,
		Template: "inject/active-workflow.md",
		Fixture:  "Maximum of research fixture and every catalog phase exit (including verdict schemas)",
	},
	{
		ID:       "board_orientation",
		Hosts:    workerTurns,
		Template: "inject/board-orientation.md",
		Fixture:  "fatLangSnapshot + RenderBoardOrientationInject",
		ExtraTrim: []string{
			"lycaon/internal/coordinator/inject/board_orientation_inject.go",
		},
	},
}
