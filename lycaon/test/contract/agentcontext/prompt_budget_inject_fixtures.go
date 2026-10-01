package contract

// PromptBudgetInjectSpec is one coordinator_injects budget fixture (SSOT for measure + catalog).
type PromptBudgetInjectSpec struct {
	ID        string
	Template  string // under config/prompts/, e.g. inject/implement-spawn.md
	Fixture   string
	ExtraTrim []string
	BumpNote  string
}

// PromptBudgetInjectMatrix is the SSOT for coordinator inject budget fixtures.
var PromptBudgetInjectMatrix = []PromptBudgetInjectSpec{
	{
		ID:       "tool_procedures",
		Template: "guidance/tool-procedures.md",
		Fixture:  "RenderToolProceduresBlock with command, verify, terminal, HTTP, and browser tools offered",
		BumpNote: "Per-call operating guidance — measure separately from the initial prompt.",
	},
	{
		ID:       "command_jobs",
		Template: "inject/command-jobs.md",
		Fixture:  "RenderCommandJobsBlock with awaited and background jobs",
		BumpNote: "Command ledger growth — keep lifecycle state concise and host-authoritative.",
	},
	{
		ID:       "worker_command_jobs",
		Template: "inject/worker-command-jobs.md",
		Fixture:  "RenderCommandJobsBlock worker surface with awaited and background jobs",
		BumpNote: "Worker command ledger growth — keep lifecycle state concise and host-authoritative.",
	},
	{
		ID:       "source_changes",
		Template: "guidance/source-changes.md",
		Fixture:  "RenderSourceChangesBlock at the working-set row cap with elsewhere counts",
		BumpNote: "Change-brief growth — keep rows single-line and the itemized set capped.",
	},
	{
		ID:       "implement_spawn",
		Template: "inject/implement-spawn.md",
		Fixture:  "RenderImplementSpawnInject with full default agent allowlist",
		ExtraTrim: []string{
			"lycaon/internal/coordinator/inject/implement_spawn_inject.go",
		},
		BumpNote: "Roster or spawn policy growth — confirm allowlist/cap copy is necessary before bumping.",
	},
	{
		ID:       "worker_task_assignment",
		Template: "inject/worker-task-assignment.md",
		Fixture:  "RenderWorkerTaskAssignment with write scope",
	},
	{
		ID:       "worker_task_preamble",
		Template: "inject/worker-task-preamble.md",
		Fixture:  "RenderWorkerTaskPreamble with write scope + filtered touch paths",
	},
	{
		ID:       "worker_leg",
		Template: "inject/worker-leg.md",
		Fixture:  "promptBudgetWorkerLegFixture (path-explorer leg)",
	},
	{
		ID:       "active_workflow",
		Template: "inject/active-workflow.md",
		Fixture:  "Maximum of research fixture and every catalog phase exit (including verdict schemas)",
	},
	{
		ID:       "board_orientation",
		Template: "inject/board-orientation.md",
		Fixture:  "fatLangSnapshot + RenderBoardOrientationInject",
		ExtraTrim: []string{
			"lycaon/internal/coordinator/inject/board_orientation_inject.go",
		},
	},
}
