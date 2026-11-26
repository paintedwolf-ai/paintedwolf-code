package pythonevidence

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
)

// StopPythonToolRequired is the recoverable routing code emitted when a bare
// interpreter command is attempted while the direct Python tool is offered.
const StopPythonToolRequired pythonruntime.StopCode = "PYTHON_TOOL_REQUIRED"

// ReachableSurface describes the caller's currently reachable tool and
// environment surface. Recovery advice MUST be derived strictly from this
// surface and NEVER suggest an unreachable tool or unavailable route.
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

// RecoveryStopContext captures the stopped operation and prior effects needed
// to build truthful OAR recovery advice.
type RecoveryStopContext struct {
	StopCode              pythonruntime.StopCode          `json:"stop_code"`
	StoppedOperationID    string                          `json:"stopped_operation_id"`
	StoppedOperation      string                          `json:"stopped_operation,omitempty"`
	PriorCompletedEffects int                             `json:"prior_completed_effects,omitempty"`
	Target                string                          `json:"target,omitempty"`
	Conflict              *pythonruntime.RevisionConflict `json:"conflict,omitempty"`
}

// RecoveryRoute describes one concrete, currently reachable recovery action.
type RecoveryRoute struct {
	Kind           string `json:"kind"`
	ToolName       string `json:"tool_name,omitempty"`
	CallableForm   string `json:"callable_form"`
	RequiresLoader string `json:"requires_loader,omitempty"`
	LoadArgument   string `json:"load_argument,omitempty"`
	Reason         string `json:"reason"`
}

// RecoveryAdvice holds the reachability-filtered recovery options for a stop.
type RecoveryAdvice struct {
	StopCode              pythonruntime.StopCode `json:"stop_code"`
	StoppedOperationID    string                 `json:"stopped_operation_id"`
	PriorEffectsPreserved int                    `json:"prior_effects_preserved"`
	Routes                []RecoveryRoute        `json:"routes,omitempty"`
	NoRecoveryRoute       bool                   `json:"no_recovery_route,omitempty"`
	Summary               string                 `json:"summary"`
}

// BuildRecoveryAdvice constructs OAR recovery advice strictly from surface.
// Unreachable tools, unavailable project runners, and mutating/external routes
// on read-only surfaces are never suggested.
func BuildRecoveryAdvice(surface ReachableSurface, ctx RecoveryStopContext) RecoveryAdvice {
	advice := RecoveryAdvice{
		StopCode:              ctx.StopCode,
		StoppedOperationID:    ctx.StoppedOperationID,
		PriorEffectsPreserved: ctx.PriorCompletedEffects,
	}

	switch ctx.StopCode {
	case StopPythonToolRequired:
		advice.Routes = routesForPythonToolRequired(surface)
	case pythonruntime.StopRevisionConflict:
		advice.Routes = routesForRevisionConflict(surface, ctx)
	case pythonruntime.StopEffectNotAllowed:
		advice.Routes = routesForEffectNotAllowed(surface)
	default:
		advice.Routes = routesForGenericStop(surface)
	}

	if len(advice.Routes) == 0 {
		advice.NoRecoveryRoute = true
		advice.Summary = fmt.Sprintf(
			"operation %s stopped with %s (%d prior completed effect(s) preserved); no reachable recovery route is available on the current surface",
			ctx.StoppedOperationID, ctx.StopCode, ctx.PriorCompletedEffects,
		)
	} else {
		advice.Summary = fmt.Sprintf(
			"operation %s stopped with %s (%d prior completed effect(s) preserved); %d reachable recovery route(s) available",
			ctx.StoppedOperationID, ctx.StopCode, ctx.PriorCompletedEffects, len(advice.Routes),
		)
	}
	return advice
}

func routesForPythonToolRequired(surface ReachableSurface) []RecoveryRoute {
	var routes []RecoveryRoute
	if surface.DirectPythonOffered {
		routes = append(routes, RecoveryRoute{
			Kind:         "direct_python",
			ToolName:     "python",
			CallableForm: `python(mode="code"|"script"|"module", ...)`,
			Reason:       "run agent working script through the offered Portable Python tool",
		})
	} else if surface.RequestToolsAvailable && slices.Contains(surface.RequestableTools, "python") {
		routes = append(routes, RecoveryRoute{
			Kind:           "request_tool",
			ToolName:       "request_tools",
			RequiresLoader: "request_tools",
			LoadArgument:   "python",
			CallableForm:   `request_tools(tools=["python"])`,
			Reason:         "load the requestable Portable Python tool before invoking it",
		})
	}
	if !surface.ReadOnly && surface.CommandOffered && surface.ProjectRunnerAvailable {
		cmd := strings.TrimSpace(surface.ProjectRunnerCommand)
		if cmd == "" {
			cmd = "external -- python3"
		}
		routes = append(routes, RecoveryRoute{
			Kind:         "project_runner",
			ToolName:     "command",
			CallableForm: fmt.Sprintf(`command(command=%q)`, cmd),
			Reason:       "execute the user's project CLI or tests with the declared project environment",
		})
	}
	return routes
}

func routesForRevisionConflict(surface ReachableSurface, ctx RecoveryStopContext) []RecoveryRoute {
	var routes []RecoveryRoute
	target := ctx.Target
	if target == "" && ctx.Conflict != nil {
		target = ctx.Conflict.Target.RelPath
	}
	if surface.NativeReadOffered {
		routes = append(routes, RecoveryRoute{
			Kind:         "native_read",
			ToolName:     "read",
			CallableForm: fmt.Sprintf(`read(path=%q)`, target),
			Reason:       "read the current document revision before retrying against fresh state",
		})
	}
	if !surface.ReadOnly && surface.DirectPythonOffered {
		routes = append(routes, RecoveryRoute{
			Kind:         "direct_python",
			ToolName:     "python",
			CallableForm: `python(mode="code"|"script"|"module", ...)`,
			Reason:       "start a new calculation against the current document revision",
		})
	}
	return routes
}

func routesForEffectNotAllowed(surface ReachableSurface) []RecoveryRoute {
	var routes []RecoveryRoute
	if surface.ReadOnly {
		if surface.NativeReadOffered {
			routes = append(routes, RecoveryRoute{
				Kind:         "native_read",
				ToolName:     "read",
				CallableForm: `read(path=...)`,
				Reason:       "inspect workspace files within read-only surface limits",
			})
		}
		return routes
	}
	if surface.CommandOffered && surface.ProjectRunnerAvailable {
		cmd := strings.TrimSpace(surface.ProjectRunnerCommand)
		if cmd == "" {
			cmd = "external -- python3"
		}
		routes = append(routes, RecoveryRoute{
			Kind:         "project_runner",
			ToolName:     "command",
			CallableForm: fmt.Sprintf(`command(command=%q)`, cmd),
			Reason:       "run the admitted project runner when external execution is required",
		})
	}
	return routes
}

func routesForGenericStop(surface ReachableSurface) []RecoveryRoute {
	if surface.ReadOnly {
		return nil
	}
	if surface.DirectPythonOffered {
		return []RecoveryRoute{{
			Kind:         "direct_python",
			ToolName:     "python",
			CallableForm: `python(mode="code"|"script"|"module", ...)`,
			Reason:       "retry with bounded input or reduced scope via Portable Python",
		}}
	}
	return nil
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
	Reason            string                 `json:"reason"`
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
	if hint.StartLine <= 0 || hint.EndLine < hint.StartLine {
		res.Reason = "discovery hint has invalid line range"
		return res
	}
	if ledger == nil {
		res.Reason = "no execution ledger provided"
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
			res.Reason = "grounded by revision-bound native source range delivery"
			return res
		}
		staleMatch = &copied
	}

	if staleMatch != nil {
		res.Delivery = staleMatch
		res.Grounded = false
		res.NeedsRevalidation = true
		res.Reason = "document revision changed since native source range delivery; revalidation required"
		return res
	}

	res.Reason = "python discovery hint requires bounded native source range delivery before grounding"
	return res
}
