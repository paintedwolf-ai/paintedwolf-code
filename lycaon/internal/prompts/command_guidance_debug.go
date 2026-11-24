package prompts

import (
	"runtime"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/promptunit"
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
		"profile_command_external_exec",
		"profile_command_mutation",
		"profile_command_network_egress",
		"profile_has_capture_page",
		"profile_has_command",
		"profile_has_command_output",
		"profile_has_command_stop",
		"profile_has_terminal_open",
		"profile_has_terminal_send",
		"profile_has_verify",
		"profile_has_wait",
		"profile_mutation_capable",
		"project_dir",
		"reviews_confined_writes",
		"root_count",
		"runner_direct_ip_tools",
		"runner_inline_wait_seconds",
		"runner_local_listen_tools",
		"runner_loopback_connect_tools",
		"runner_park_seconds",
		"runner_socket_tools",
		"runner_write_root_tools",
		"session_scratch_dir",
		"shared_temp_writable",
		"shared_temp_locations",
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

// CommandGuidanceSnapshotInput carries the request facts command-guidance
// debug metadata records beside the request's Reader.
type CommandGuidanceSnapshotInput struct {
	AgentType             string
	ProjectDir            string
	SessionScratchDir     string
	SharedTempLocations   []string
	Posture               gate.Posture
	CommandExternalHidden bool
}

// BuildCommandGuidanceSnapshot computes the rendered command-guidance
// template/partial/unit list and structured fact map for a request's Reader.
func BuildCommandGuidanceSnapshot(reader Reader, in CommandGuidanceSnapshotInput) ([]string, map[string]any) {
	if !reader.Valid() {
		return nil, nil
	}
	offered := reader.Offered()
	deferred := reader.Requestable()
	isCoordinator := reader.Selection().Host == promptunit.HostCoordinator
	vars := AgentPromptSurfaceTemplateVars(AgentPromptSurface{
		SessionScratchDir:     strings.TrimSpace(in.SessionScratchDir),
		Posture:               in.Posture,
		SharedTempLocations:   in.SharedTempLocations,
		CommandExternalHidden: in.CommandExternalHidden,
	})
	vars["platform_os"] = runtime.GOOS
	vars["project_dir"] = strings.TrimSpace(in.ProjectDir)
	if merged, mergeErr := Vars(reader, vars); mergeErr == nil {
		vars = merged
	}

	hasCommandOffered := slices.Contains(offered, "command")
	hasCommandAny := hasCommandOffered || slices.Contains(deferred, "command")
	cmdMutation, _ := vars["profile_command_mutation"].(bool)
	hasVerify, _ := vars["profile_has_verify"].(bool)
	hasTerminal, _ := vars["profile_has_terminal_open"].(bool)

	var templates []string
	if hasCommandOffered {
		templates = append(templates, "tools/schemas/command.yaml")
	}
	if hasCommandAny {
		if !isCoordinator {
			templates = append(templates, "partials/command-surface.md")
		}
		templates = append(templates, "partials/temporary-storage.md")
	}
	if strings.TrimSpace(in.AgentType) == "implementer" {
		templates = append(templates, "partials/sandbox-confinement.md")
	}
	if isCoordinator && (cmdMutation || hasVerify || hasTerminal) {
		templates = append(templates, "units/coordinator-host-runner.md")
	}
	if (cmdMutation || hasTerminal) && (hasCommandOffered || hasTerminal) {
		templates = append(templates, "units/secure-by-default-run.md")
	}
	if (cmdMutation || hasVerify || hasTerminal) && (hasCommandOffered || hasVerify || hasTerminal) {
		templates = append(templates, "units/runner-operating-procedure.md", "partials/capability-request-grammar.md")
	}
	return templates, ExtractCommandGuidanceFacts(vars)
}
