package prompts

import (
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/gate"
)

// CommandGuidanceFactKeys lists every structured fact branched on by the
// command schema and prompt guidance templates.
func CommandGuidanceFactKeys() []string {
	return []string{
		"command_external_hidden",
		"command_external_shown",
		"command_on_floor",
		"command_output_files",
		"is_coordinator",
		"platform_os",
		"profile_command_mutation",
		"profile_has_capture_page",
		"profile_has_command",
		"profile_has_command_output",
		"profile_has_command_stop",
		"profile_has_http_request",
		"profile_has_process_list",
		"profile_has_process_signal",
		"profile_has_terminal_open",
		"profile_has_verify",
		"profile_has_wait",
		"profile_mutation_capable",
		"project_dir",
		"reviews_confined_writes",
		"root_count",
		"runner_direct_ip_tools",
		"runner_host_execution_tools",
		"runner_host_resource_tools",
		"runner_local_listen_tools",
		"runner_loopback_connect_tools",
		"runner_read_path_tools",
		"runner_socket_tools",
		"runner_write_root_tools",
		"session_scratch_dir",
		"tool_profile",
		"workspace_roots",
	}
}

// ExtractCommandGuidanceFacts returns a map containing the structured facts
// from vars that command guidance templates branch on.
func ExtractCommandGuidanceFacts(vars map[string]any) map[string]any {
	out := make(map[string]any, len(CommandGuidanceFactKeys()))
	for _, k := range CommandGuidanceFactKeys() {
		if v, ok := vars[k]; ok {
			out[k] = v
		}
	}
	return out
}

// CommandGuidanceSnapshotInput describes the turn facts used to record
// command-guidance debug metadata in llm-requests.jsonl.
type CommandGuidanceSnapshotInput struct {
	ProfileID             string
	SurfaceID             string
	AgentType             string
	IsCoordinator         bool
	OfferedTools          []string
	DeferredTools         []string
	OmittedUnits          map[string]bool
	RootCount             int
	ProjectDir            string
	SessionScratchDir     string
	Posture               gate.Posture
	CommandExternalHidden bool
}

// BuildCommandGuidanceSnapshot computes the rendered command-guidance
// template/partial/unit list and structured fact map for a turn request.
func BuildCommandGuidanceSnapshot(in CommandGuidanceSnapshotInput) ([]string, map[string]any) {
	offered := append([]string(nil), in.OfferedTools...)
	sort.Strings(offered)
	deferred := append([]string(nil), in.DeferredTools...)
	sort.Strings(deferred)

	scopeID := strings.TrimSpace(in.ProfileID)
	if strings.TrimSpace(in.SurfaceID) != "" {
		scopeID = strings.TrimSpace(in.SurfaceID)
	}
	vars := VisibleToolsNativePartialVars(offered)
	vars["tool_profile"] = strings.TrimSpace(in.ProfileID)
	vars["is_coordinator"] = in.IsCoordinator
	vars["platform_os"] = runtime.GOOS
	vars["root_count"] = in.RootCount
	vars["project_dir"] = strings.TrimSpace(in.ProjectDir)
	vars["session_scratch_dir"] = strings.TrimSpace(in.SessionScratchDir)
	vars["reviews_confined_writes"] = in.Posture.ReviewsConfinedWrites()
	vars["command_external_hidden"] = in.CommandExternalHidden

	if in.IsCoordinator && strings.TrimSpace(in.SurfaceID) != "" {
		floor, _ := LoadCoordinatorSurfaceFloor(in.SurfaceID)
		vars["command_on_floor"] = slices.Contains(floor, "command")
		ApplyCoordinatorSurfaceCommandEffects(in.SurfaceID, offered, deferred, vars)
	} else {
		vars["command_on_floor"] = slices.Contains(offered, "command")
		if !ScopeAllowsCommandMutation(scopeID) {
			ApplyReadOnlyCommandEffects(offered, deferred, vars)
		}
	}
	MergeRunnerCapabilityVars(offered, vars)
	syncCommandExternalFacts(vars)

	hasCommandOffered := slices.Contains(offered, "command")
	hasCommandAny := hasCommandOffered || slices.Contains(deferred, "command")
	cmdMutation, _ := vars["profile_command_mutation"].(bool)
	hasVerify, _ := vars["profile_has_verify"].(bool)
	hasTerminal, _ := vars["profile_has_terminal_open"].(bool)
	hasHTTP, _ := vars["profile_has_http_request"].(bool)

	var templates []string
	if hasCommandOffered {
		templates = append(templates, "tools/schemas/command.yaml")
	}
	if hasCommandAny {
		templates = append(templates, "partials/command-surface.md")
	}
	if strings.TrimSpace(in.AgentType) == "implementer" {
		templates = append(templates, "partials/sandbox-confinement.md")
	}
	if in.IsCoordinator && (cmdMutation || hasVerify || hasTerminal) && !in.OmittedUnits["coordinator-host-runner"] {
		templates = append(templates, "units/coordinator-host-runner.md")
	}
	if (cmdMutation || hasTerminal || hasHTTP) && (hasCommandOffered || hasTerminal || hasHTTP) && !in.OmittedUnits["secure-by-default-run"] {
		templates = append(templates, "units/secure-by-default-run.md")
	}
	if (cmdMutation || hasVerify || hasTerminal) && (hasCommandOffered || hasVerify || hasTerminal) && !in.OmittedUnits["runner-operating-procedure"] {
		templates = append(templates, "units/runner-operating-procedure.md", "partials/capability-request-grammar.md")
	}
	return templates, ExtractCommandGuidanceFacts(vars)
}
