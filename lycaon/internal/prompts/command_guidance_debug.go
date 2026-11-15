package prompts

import (
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolsurface"
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
	profiles, _ := sandbox.LoadToolProfiles()
	profileID := strings.TrimSpace(in.ProfileID)
	if profileID == "" || in.IsCoordinator || strings.TrimSpace(in.SurfaceID) != "" {
		profileID = CoordinatorProfileID
	}
	var found bool
	for _, p := range profiles {
		if p.ID == profileID {
			found = true
			break
		}
	}
	if !found {
		profiles = append(profiles, sandbox.ToolProfile{
			ID:             profileID,
			CommandEffects: ScopeCommandEffects(scopeID),
		})
	}
	reader, err := NewReader(ReaderInput{
		ProfileID: profileID,
		SurfaceID: strings.TrimSpace(in.SurfaceID),
		Profiles:  profiles,
		Plan:      toolsurface.Compile(offered, deferred),
		Loaded:    toolNameSet(offered),
		Visible:   append(slices.Clone(offered), deferred...),
		RootCount: in.RootCount,
		WebSearch: true,
	})
	vars := map[string]any{}
	if err == nil {
		vars = reader.Facts()
	}
	vars["tool_profile"] = strings.TrimSpace(in.ProfileID)
	vars["is_coordinator"] = in.IsCoordinator
	vars["platform_os"] = runtime.GOOS
	vars["root_count"] = in.RootCount
	vars["project_dir"] = strings.TrimSpace(in.ProjectDir)
	vars["session_scratch_dir"] = strings.TrimSpace(in.SessionScratchDir)
	vars["reviews_confined_writes"] = in.Posture.ReviewsConfinedWrites()
	vars["command_external_hidden"] = in.CommandExternalHidden
	syncCommandExternalFacts(vars)

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
		templates = append(templates, "partials/command-surface.md")
	}
	if strings.TrimSpace(in.AgentType) == "implementer" {
		templates = append(templates, "partials/sandbox-confinement.md")
	}
	if in.IsCoordinator && (cmdMutation || hasVerify || hasTerminal) {
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
