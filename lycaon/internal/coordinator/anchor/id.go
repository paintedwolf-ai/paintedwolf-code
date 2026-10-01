package anchor

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
)

// ID is a catalog Anchor id (dotted lifecycle name).
type ID string

// Named consts are Emit conveniences; anchor membership is defined by
// anchors/catalog.yaml (anchorcatalog.InstallFile), and every const must appear there.
const (
	GateBlocked                  ID = "gate.blocked"
	ComposeDone                  ID = "compose.done"
	LegFinished                  ID = "leg.finished"
	WorkerTaskFinished           ID = "worker.task.finished"
	PhaseAdvanced                ID = "phase.advanced"
	ReviewLoopContinue           ID = "review_loop.continue"
	ReviewLoopDecide             ID = "review_loop.decide"
	FeedbackPending              ID = "feedback.pending"
	FeedbackReceived             ID = "feedback.received"
	WorkerTaskStarted            ID = "worker.task.started"
	WorkerLegStarted             ID = "worker.leg.started"
	WaitTimerFired               ID = "wait.timer.fired"
	OverlayPromoteComplete       ID = "overlay.promote.complete"
	ScanFinished                 ID = "scan.finished"
	ScanDelta                    ID = "scan.delta"
	ProcessFinished              ID = "process.finished"
	ProcessRefused               ID = "process.refused"
	WorkerBudgetRequested        ID = "worker.budget.requested"
	WorkerBudgetRaised           ID = "worker.budget.raised"
	WorkerBudgetDeclined         ID = "worker.budget.declined"
	EditFollowUpRepeat           ID = "edit.follow_up.repeat"
	CoordinatorCloseout          ID = "coordinator.closeout"
	CoordinatorCitationGrounding ID = "coordinator.citation.grounding"
	CoordinatorReportDocument    ID = "coordinator.report.document"
	ProjectRootsChanged          ID = "project.roots.changed"
	ProjectRootDetachCanceled    ID = "project.root.detach_canceled"
	WorkerCloseout               ID = "worker.closeout"
	WorkerIterationsLow          ID = "worker.iterations.low"
	WorkerCancelCloseout         ID = "worker.cancel.closeout"
	WorkerSummaryTrim            ID = "worker.summary.trim"
	WorkerCitationGrounding      ID = "worker.citation.grounding"
	ProgressMissing              ID = "progress.missing"
	ProgressStale                ID = "progress.stale"
	PhaseExitRequired            ID = "phase.exit_required"
	AuthzSealFailed              ID = "authz.seal_failed"
	TurnCloseout                 ID = "turn.closeout"
	TurnIterationsLow            ID = "turn.iterations.low"
	TurnSurveyStreak             ID = "turn.survey.streak"
	TurnSpendRunwayLow           ID = "turn.spend.runway_low"
	TurnSpendSoftStop            ID = "turn.spend.soft_stop"
	OutboundSecretWithheld       ID = "outbound.secret.withheld"
	LoopWake                     ID = "loop.wake"
	BoardChanged                 ID = "board.changed"
	PhaseEntered                 ID = "phase.entered"
	InjectActiveWorkflow         ID = "inject.active_workflow"
	InjectWorkerBoard            ID = "inject.worker_board"
	InjectWorkerLeg              ID = "inject.worker_leg"
	InjectAgentsMD               ID = "inject.agents_md"
	InjectTransition             ID = "inject.transition"
	InjectCommandJobs            ID = "inject.command_jobs"
	InjectToolProcedures         ID = "inject.tool_procedures"
	InjectSkillProcedure         ID = "inject.skill_procedure"
	InjectSkillPointer           ID = "inject.skill_pointer"
	InjectSourceChanges          ID = "inject.source_changes"
	InjectSynthesisEvidence      ID = "inject.synthesis_evidence"
	InjectImplementSpawn         ID = "inject.implement_spawn"
	InjectBlueprint              ID = "inject.blueprint"
	InjectWorkerTaskAssignment   ID = "inject.worker_task_assignment"
	InjectWorkerTaskPreamble     ID = "inject.worker_task_preamble"
	InjectScanGuidance           ID = "inject.scan_guidance"
)

// MustAnchor returns ID or panics if id is not in the installed catalog or
// is not a current Binding.render identifier.
func MustAnchor(id string) ID {
	a, ok := ParseID(id)
	if !ok {
		panic(fmt.Sprintf("anchor: unknown id %q", id))
	}
	return a
}

// ParseID accepts a catalog dotted id or a current Binding.render identifier.
// Requires anchorcatalog.InstallFile.
func ParseID(s string) (ID, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if !anchorcatalog.Loaded() {
		return "", false
	}
	if anchorcatalog.Has(s) {
		return ID(s), true
	}
	if r := DefaultRegistry(); r != nil {
		if a, ok := r.LookupRender(s); ok && anchorcatalog.Has(string(a)) {
			return a, true
		}
	}
	return "", false
}

// InformRender returns the Binding.render stem for id (empty if unknown).
func InformRender(id ID) string {
	if r := DefaultRegistry(); r != nil {
		return r.InformRender(id)
	}
	return ""
}

// SameInform reports whether queued/pending kickID refers to the same inform Anchor.
func SameInform(kickID string, id ID) bool {
	kickID = strings.TrimSpace(kickID)
	if kickID == "" || id == "" {
		return false
	}
	a, ok := ParseID(kickID)
	return ok && a == id
}

// String returns the catalog id.
func (id ID) String() string { return string(id) }
