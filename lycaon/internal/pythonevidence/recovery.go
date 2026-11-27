package pythonevidence

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/toolreach"
)

// OAR policy and ordinary-failure codes handled by the PY05 recovery contract.
const (
	StopPythonToolRequired   pythonruntime.StopCode = "PYTHON_TOOL_REQUIRED"
	StopHTTPHostDenied       pythonruntime.StopCode = "HTTP_REQUEST_HOST_DENIED"
	StopWritePathDenied      pythonruntime.StopCode = "WRITE_PATH_DENIED"
	StopPythonException      pythonruntime.StopCode = "PYTHON_RUNTIME_EXCEPTION"
	StopProjectRunnerFailure pythonruntime.StopCode = "PROJECT_RUNNER_FAILURE"
	StopShellStageFailure    pythonruntime.StopCode = "SHELL_STAGE_FAILURE"
)

// RuntimeKind classifies the child operation or stage independently of the
// initiating model tool (`Invocation.Tool`).
const (
	RuntimeKindPortablePython = "portable_python"
	RuntimeKindProjectRunner  = "project_runner"
	RuntimeKindShellStage     = "shell_stage"
	RuntimeKindHTTP           = "http"
	RuntimeKindWorkspaceWrite = "workspace_write"
)

// ReachableSurface describes the caller's currently reachable tool and
// environment surface. Pack OAR facts MUST be derived strictly from this
// surface and NEVER reference an unreachable tool or unadmitted runner.
type ReachableSurface struct {
	DirectPythonOffered    bool     `json:"direct_python_offered"`
	CommandOffered         bool     `json:"command_offered"`
	ProjectRunnerAvailable bool     `json:"project_runner_available"`
	ProjectRunnerCommand   string   `json:"project_runner_command,omitempty"`
	RequestToolsAvailable  bool     `json:"request_tools_available"`
	RequestableTools       []string `json:"requestable_tools,omitempty"`
	NativeReadOffered      bool     `json:"native_read_offered"`
	ReadOnly               bool     `json:"read_only"`
}

// ReachableSurfaceFromRequest derives a ReachableSurface directly from Painted
// Wolf's frozen toolreach.Request without any parallel reachability table.
func ReachableSurfaceFromRequest(req toolreach.Request, readOnly bool, admittedProjectRunner string) ReachableSurface {
	surface := ReachableSurface{
		DirectPythonOffered:   req.Status("python") == toolreach.Callable,
		CommandOffered:        req.Status(toolreach.ProgramTool) == toolreach.Callable,
		RequestToolsAvailable: req.Status(toolreach.LoaderTool) == toolreach.Callable,
		NativeReadOffered:     req.Status("read") == toolreach.Callable,
		ReadOnly:              readOnly,
	}
	if req.Status("python") == toolreach.Loadable {
		surface.RequestableTools = []string{"python"}
	}
	if cmd := strings.TrimSpace(admittedProjectRunner); cmd != "" && !readOnly && surface.CommandOffered {
		surface.ProjectRunnerAvailable = true
		surface.ProjectRunnerCommand = cmd
	}
	return surface
}

// RecoveryStopContext captures the initiating tool, stopped stage/child facts,
// and prior effects needed to populate pack-owned OAR recovery facts.
type RecoveryStopContext struct {
	PolicyCode            pythonruntime.StopCode          `json:"policy_code"`
	InitiatingTool        string                          `json:"initiating_tool,omitempty"`
	StageName             string                          `json:"stage_name,omitempty"`
	ChildOperation        string                          `json:"child_operation,omitempty"`
	RuntimeKind           string                          `json:"runtime_kind,omitempty"`
	StoppedOperationID    string                          `json:"stopped_operation_id"`
	StoppedOperation      string                          `json:"stopped_operation,omitempty"`
	PriorCompletedEffects int                             `json:"prior_completed_effects,omitempty"`
	ExitCode              int                             `json:"exit_code,omitempty"`
	HeldOperationID       string                          `json:"held_operation_id,omitempty"`
	Target                string                          `json:"target,omitempty"`
	Conflict              *pythonruntime.RevisionConflict `json:"conflict,omitempty"`
}

// DirectPythonCallForm declares the exact projected schema arguments for the
// direct `python` tool (`code` | `script` | `module`, with optional `cwd`, `args`).
type DirectPythonCallForm struct {
	Tool         string   `json:"tool"`
	ModeArgs     []string `json:"mode_args"`
	OptionalArgs []string `json:"optional_args"`
	CallableForm string   `json:"callable_form"`
}

// RequestToolsCallForm declares the exact projected schema call for loading a
// requestable tool via `request_tools(need=...)`.
type RequestToolsCallForm struct {
	Tool         string `json:"tool"`
	Need         string `json:"need"`
	CallableForm string `json:"callable_form"`
}

// AdmittedExternalRunnerForm declares an explicitly admitted project runner
// callable through the `command` tool.
type AdmittedExternalRunnerForm struct {
	Tool         string `json:"tool"`
	Command      string `json:"command"`
	CallableForm string `json:"callable_form"`
}

// NativeReadCallForm declares a bounded native `read` tool call for refreshing
// a conflicted or ungrounded document revision.
type NativeReadCallForm struct {
	Tool         string `json:"tool"`
	Path         string `json:"path"`
	CallableForm string `json:"callable_form"`
}

// OARRecoveryFacts supplies structured, reachability-filtered facts consumed by
// pack-owned OAR policy templates and model-visible recovery reporting.
// InitiatingTool (`Invocation.Tool`) and stage/runtime facts are preserved
// independently; no inline Go recovery prose is generated.
type OARRecoveryFacts struct {
	PolicyCode             pythonruntime.StopCode          `json:"policy_code"`
	InitiatingTool         string                          `json:"initiating_tool,omitempty"`
	StageName              string                          `json:"stage_name,omitempty"`
	ChildOperation         string                          `json:"child_operation,omitempty"`
	RuntimeKind            string                          `json:"runtime_kind,omitempty"`
	StoppedOperationID     string                          `json:"stopped_operation_id"`
	StoppedOperation       string                          `json:"stopped_operation,omitempty"`
	PriorEffects           int                             `json:"prior_effects"`
	ExitCode               int                             `json:"exit_code,omitempty"`
	DiagnosticPreserved    bool                            `json:"diagnostic_preserved,omitempty"`
	PendingHeldOperationID string                          `json:"pending_held_operation_id,omitempty"`
	ReachableDirectPython  *DirectPythonCallForm           `json:"reachable_direct_python,omitempty"`
	RequestableNeed        *RequestToolsCallForm           `json:"requestable_need,omitempty"`
	AdmittedExternalRunner *AdmittedExternalRunnerForm     `json:"admitted_external_runner,omitempty"`
	ReachableNativeRead    *NativeReadCallForm             `json:"reachable_native_read,omitempty"`
	Conflict               *pythonruntime.RevisionConflict `json:"conflict,omitempty"`
	NoRecoveryRoute        bool                            `json:"no_recovery_route"`
}

// BuildOARRecoveryFacts populates structured pack OAR facts strictly from
// surface. Unreachable tools, unadmitted project runners, cross-language
// permission bypasses, and mutating routes on read-only surfaces are omitted.
func BuildOARRecoveryFacts(surface ReachableSurface, ctx RecoveryStopContext) OARRecoveryFacts {
	facts := OARRecoveryFacts{
		PolicyCode:             ctx.PolicyCode,
		InitiatingTool:         strings.TrimSpace(ctx.InitiatingTool),
		StageName:              strings.TrimSpace(ctx.StageName),
		ChildOperation:         strings.TrimSpace(ctx.ChildOperation),
		RuntimeKind:            strings.TrimSpace(ctx.RuntimeKind),
		StoppedOperationID:     ctx.StoppedOperationID,
		StoppedOperation:       ctx.StoppedOperation,
		PriorEffects:           ctx.PriorCompletedEffects,
		ExitCode:               ctx.ExitCode,
		PendingHeldOperationID: strings.TrimSpace(ctx.HeldOperationID),
		Conflict:               ctx.Conflict,
	}

	switch {
	case ctx.PolicyCode == StopPythonToolRequired:
		populatePythonToolRequired(surface, &facts)
	case ctx.PolicyCode == StopPythonException || (facts.InitiatingTool == "python" && facts.RuntimeKind == RuntimeKindPortablePython && ctx.PolicyCode == ""):
		populateDirectPythonException(surface, &facts)
	case ctx.PolicyCode == StopProjectRunnerFailure || facts.RuntimeKind == RuntimeKindProjectRunner:
		populateProjectRunnerFailure(surface, &facts)
	case ctx.PolicyCode == StopShellStageFailure || facts.RuntimeKind == RuntimeKindShellStage:
		facts.DiagnosticPreserved = true
	case ctx.PolicyCode == StopHTTPHostDenied || ctx.PolicyCode == StopWritePathDenied:
		// Switching languages cannot grant permission; preserve denial and any held op.
	case ctx.PolicyCode == pythonruntime.StopRevisionConflict:
		populateRevisionConflict(surface, ctx, &facts)
	case ctx.PolicyCode == pythonruntime.StopEffectNotAllowed:
		populateEffectNotAllowed(surface, ctx, &facts)
	default:
		if !surface.ReadOnly && surface.DirectPythonOffered && facts.PendingHeldOperationID == "" {
			facts.ReachableDirectPython = directPythonForm()
		}
	}

	facts.NoRecoveryRoute = facts.PendingHeldOperationID == "" &&
		facts.ReachableDirectPython == nil &&
		facts.RequestableNeed == nil &&
		facts.AdmittedExternalRunner == nil &&
		facts.ReachableNativeRead == nil
	return facts
}

func directPythonForm() *DirectPythonCallForm {
	return &DirectPythonCallForm{
		Tool:         "python",
		ModeArgs:     []string{"code", "script", "module"},
		OptionalArgs: []string{"cwd", "args"},
		CallableForm: `python(code=...|script=...|module=..., cwd=..., args=[...])`,
	}
}

func requestPythonNeedForm() *RequestToolsCallForm {
	return &RequestToolsCallForm{
		Tool:         "request_tools",
		Need:         "python",
		CallableForm: `request_tools(need="python")`,
	}
}

func admittedRunnerForm(surface ReachableSurface) *AdmittedExternalRunnerForm {
	if surface.ReadOnly || !surface.CommandOffered || !surface.ProjectRunnerAvailable {
		return nil
	}
	cmd := strings.TrimSpace(surface.ProjectRunnerCommand)
	if cmd == "" {
		return nil
	}
	return &AdmittedExternalRunnerForm{
		Tool:         "command",
		Command:      cmd,
		CallableForm: fmt.Sprintf(`command(command=%q)`, cmd),
	}
}

func populateDirectPythonException(surface ReachableSurface, facts *OARRecoveryFacts) {
	facts.DiagnosticPreserved = true
	if surface.DirectPythonOffered {
		facts.ReachableDirectPython = directPythonForm()
	} else if surface.RequestToolsAvailable && slices.Contains(surface.RequestableTools, "python") {
		facts.RequestableNeed = requestPythonNeedForm()
	}
}

func populateProjectRunnerFailure(surface ReachableSurface, facts *OARRecoveryFacts) {
	facts.DiagnosticPreserved = true
	facts.AdmittedExternalRunner = admittedRunnerForm(surface)
}

func populatePythonToolRequired(surface ReachableSurface, facts *OARRecoveryFacts) {
	if surface.DirectPythonOffered {
		facts.ReachableDirectPython = directPythonForm()
	} else if surface.RequestToolsAvailable && slices.Contains(surface.RequestableTools, "python") {
		facts.RequestableNeed = requestPythonNeedForm()
	}
	facts.AdmittedExternalRunner = admittedRunnerForm(surface)
}

func populateRevisionConflict(surface ReachableSurface, ctx RecoveryStopContext, facts *OARRecoveryFacts) {
	target := ctx.Target
	if target == "" && ctx.Conflict != nil {
		target = ctx.Conflict.Target.RelPath
	}
	if surface.NativeReadOffered {
		facts.ReachableNativeRead = &NativeReadCallForm{
			Tool:         "read",
			Path:         target,
			CallableForm: fmt.Sprintf(`read(path=%q)`, target),
		}
	}
	if !surface.ReadOnly && surface.DirectPythonOffered {
		facts.ReachableDirectPython = directPythonForm()
	}
}

func populateEffectNotAllowed(surface ReachableSurface, ctx RecoveryStopContext, facts *OARRecoveryFacts) {
	if facts.PendingHeldOperationID != "" {
		return
	}
	if surface.ReadOnly && surface.NativeReadOffered {
		facts.ReachableNativeRead = &NativeReadCallForm{
			Tool:         "read",
			Path:         ctx.Target,
			CallableForm: fmt.Sprintf(`read(path=%q)`, ctx.Target),
		}
	}
}

// DiscoveryHint represents a Python-emitted AST or line coordinate. It is
// strictly a discovery hint and NEVER grounds a finding on its own.
type DiscoveryHint struct {
	ExecutionID string                        `json:"execution_id"`
	Target      pythonruntime.CanonicalTarget `json:"target"`
	StartLine   int                           `json:"start_line"`
	EndLine     int                           `json:"end_line"`
	Symbol      string                        `json:"symbol,omitempty"`
	Note        string                        `json:"note,omitempty"`
}

// GroundedFindingCheckResult reports whether a DiscoveryHint has been backed
// by a bounded native source range delivery at the current document revision.
type GroundedFindingCheckResult struct {
	Hint              DiscoveryHint          `json:"hint"`
	Delivery          *NativeSourceRangeFact `json:"delivery,omitempty"`
	Grounded          bool                   `json:"grounded"`
	NeedsRevalidation bool                   `json:"needs_revalidation"`
}

// CheckGroundedFinding verifies that a Python-emitted discovery hint is backed
// by a bounded native source range delivery bound to currentRevision.
// Guest reads and screened stdout NEVER satisfy grounding. If the document
// revision changed after delivery, Grounded is false and NeedsRevalidation is true.
func CheckGroundedFinding(
	hint DiscoveryHint,
	ledger *ExecutionLedger,
	currentRevision pythondocuments.DocumentRevision,
) GroundedFindingCheckResult {
	res := GroundedFindingCheckResult{Hint: hint}
	if hint.StartLine <= 0 || hint.EndLine < hint.StartLine || ledger == nil {
		return res
	}

	hintKey := pythondocuments.CanonicalKeyOf(hint.Target)
	var staleMatch *NativeSourceRangeFact

	for _, rng := range ledger.ModelSourceCitations() {
		if pythondocuments.CanonicalKeyOf(rng.Target) != hintKey {
			continue
		}
		if rng.StartLine > hint.StartLine || rng.EndLine < hint.EndLine {
			continue
		}
		if strings.TrimSpace(rng.HostHandle) == "" {
			continue
		}
		copied := rng
		if rng.Revision.Matches(currentRevision) {
			res.Delivery = &copied
			res.Grounded = true
			res.NeedsRevalidation = false
			return res
		}
		staleMatch = &copied
	}

	if staleMatch != nil {
		res.Delivery = staleMatch
		res.Grounded = false
		res.NeedsRevalidation = true
	}
	return res
}
