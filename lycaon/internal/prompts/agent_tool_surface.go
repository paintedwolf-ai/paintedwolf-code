package prompts

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
)

const (
	// AgentToolSurfacePartialRef renders worker tools and rejection codes.
	AgentToolSurfacePartialRef = "partials/agent-tool-surface.md"
)

// preventiveRejectEmitters surface recoverable codes before tool use.
var preventiveRejectEmitters = map[string]bool{
	"guard:write_scope":     true,
	"guard:command_surface": true,
}

type AgentToolArgView struct {
	Name         string
	RequiredArgs []string
}

// AgentSkillView describes one indexed skill.
type AgentSkillView struct {
	Name        string
	Description string
}

// AgentHostResourceView is one prompt row. Paths and destinations stay host-side.
type AgentHostResourceView struct {
	ID       string
	Label    string
	Category string
	Status   string
	Access   string
	Guidance string
}

// AgentPromptSurface holds skill availability and host resources for prompts.
type AgentPromptSurface struct {
	// Skills are the applicable catalog; bodies remain on demand.
	Skills                []AgentSkillView
	FullCatalog           bool
	HostResources         []AgentHostResourceView
	HostResourcesOmitted  int // rows the ambient budget dropped
	Fingerprint           string
	SessionScratchDir     string
	Posture               gate.Posture
	CommandExternalHidden bool
}

// AgentRejectCodeView describes a recoverable tool rejection.
type AgentRejectCodeView struct {
	Code              string
	Emit              string
	BranchInstruction string
}

type AgentToolSurfaceData struct {
	ToolProfile string
	Tools       []AgentToolArgView
	Requestable []RequestableToolView
	Skills      []AgentSkillView
	RejectCodes []AgentRejectCodeView
	WriteGlobs  []string
	// CommandEffects bounds what command may do on this profile.
	CommandEffects  toolcontract.CommandEffects
	CommandDeferred bool
}

// AgentPromptSurfaceTemplateVars maps the prompt surface to template vars.
func AgentPromptSurfaceTemplateVars(surface AgentPromptSurface) map[string]any {
	skills := make([]map[string]any, len(surface.Skills))
	for i, skill := range surface.Skills {
		skills[i] = map[string]any{
			"name":        skill.Name,
			"description": skill.Description,
		}
	}
	return map[string]any{
		"agent_skills":                 skills,
		"full_skill_catalog":           surface.FullCatalog,
		"agent_host_resources":         groupHostResources(surface.HostResources),
		"agent_host_resources_omitted": surface.HostResourcesOmitted,
		"prompt_surface_fingerprint":   surface.Fingerprint,
		"session_scratch_dir":          surface.SessionScratchDir,
		"reviews_confined_writes":      surface.Posture.ReviewsConfinedWrites(),
		"command_external_hidden":      surface.CommandExternalHidden,
		"command_external_shown":       !surface.CommandExternalHidden,
	}
}

func groupHostResources(resources []AgentHostResourceView) []map[string]any {
	if len(resources) == 0 {
		return nil
	}
	var groups []map[string]any
	var current string
	var items []map[string]any
	flush := func() {
		if current == "" && len(items) == 0 {
			return
		}
		groups = append(groups, map[string]any{
			"category":  current,
			"resources": items,
		})
	}
	for _, resource := range resources {
		if resource.Category != current && len(items) > 0 {
			flush()
			items = nil
		}
		current = resource.Category
		items = append(items, map[string]any{
			"id": resource.ID, "label": resource.Label, "category": resource.Category,
			"status": resource.Status, "access": resource.Access, "guidance": resource.Guidance,
		})
	}
	flush()
	return groups
}

// LoadAgentToolSurface builds one profile's prompt surface. in.Loaded names
// the profile's deferred tools the session ledger has loaded; they are offered
// on this call like sticky ones, and the rest list as requestable with the
// description in.Schemas holds for them.
func LoadAgentToolSurface(toolProfileID string, visibleTools []string, hintCodes map[string]hintCodeRow, in ReaderInput, profiles []sandbox.ToolProfile) (AgentToolSurfaceData, error) {
	profileID := strings.TrimSpace(toolProfileID)
	if profileID == "" {
		return AgentToolSurfaceData{}, fmt.Errorf("tool profile id required")
	}
	if len(profiles) == 0 {
		return AgentToolSurfaceData{}, fmt.Errorf("tool profiles required for catalog-bound surface")
	}
	var profile sandbox.ToolProfile
	for _, p := range profiles {
		if p.ID == profileID {
			profile = p
			break
		}
	}
	if profile.ID == "" {
		return AgentToolSurfaceData{}, fmt.Errorf("tool profile %q not found", profileID)
	}

	toolNames := availableToolsInPlan(visibleToolNames(profile, visibleTools), in.Plan)
	toolViews := make([]AgentToolArgView, 0, len(toolNames))
	var requestable []string
	for _, name := range toolNames {
		if (profile.ToolDeferred(name) && !in.Loaded[name]) || !profile.ToolAllowed(name) {
			requestable = append(requestable, name)
			continue
		}
		view := AgentToolArgView{Name: name}
		if in.Schemas != nil {
			if meta, ok := in.Schemas.ToolMeta(name); ok {
				view.RequiredArgs, _ = toolschema.ArgFieldSummary(meta.ArgsSchema)
			}
		}
		toolViews = append(toolViews, view)
	}
	roster, err := RequestableTools(requestable, in.Schemas)
	if err != nil {
		return AgentToolSurfaceData{}, err
	}

	return AgentToolSurfaceData{
		ToolProfile: profileID,
		Tools:       toolViews,
		Requestable: roster,
		RejectCodes: recoverableRejectCodes(hintCodes, toolNames),
		WriteGlobs:  append([]string(nil), profile.WriteGlobs...),
		// Read-only profiles offer command for inspection only.
		CommandEffects:  profile.CommandEffects,
		CommandDeferred: profile.ToolDeferred("command"),
	}, nil
}

func visibleToolNames(profile sandbox.ToolProfile, visible []string) []string {
	if visible != nil {
		out := append([]string(nil), visible...)
		sort.Strings(out)
		return out
	}
	enabled := make([]string, 0, len(profile.Tools))
	for name, ok := range profile.Tools {
		// Wildcards require runtime expansion.
		if ok && !strings.HasSuffix(name, "*") {
			enabled = append(enabled, name)
		}
	}
	sort.Strings(enabled)
	return enabled
}

func recoverableRejectCodes(hints map[string]hintCodeRow, toolNames []string) []AgentRejectCodeView {
	if len(hints) == 0 {
		return nil
	}
	allowed := map[string]bool{"*": true}
	for _, name := range toolNames {
		allowed[name] = true
	}
	type row struct {
		code string
		view AgentRejectCodeView
	}
	var rows []row
	for code, entry := range hints {
		if entry.Category != "recoverable" {
			continue
		}
		emit := strings.TrimSpace(entry.Emit)
		if !preventiveRejectEmitters[emit] {
			continue
		}
		if !hintAppliesToTools(entry.Tools, allowed) {
			continue
		}
		branch := strings.TrimSpace(entry.Instead)
		if branch == "" {
			continue
		}
		// Runtime templates need invocation facts unavailable during preflight.
		if strings.Contains(branch, "{{") || strings.Contains(branch, "{%") || strings.Contains(branch, "{#") {
			continue
		}
		rows = append(rows, row{
			code: code,
			view: AgentRejectCodeView{Code: code, Emit: emit, BranchInstruction: branch},
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].code < rows[j].code })
	out := make([]AgentRejectCodeView, len(rows))
	for i, r := range rows {
		out[i] = r.view
	}
	return out
}

func hintAppliesToTools(toolsList []string, allowed map[string]bool) bool {
	if len(toolsList) == 0 {
		return true
	}
	for _, t := range toolsList {
		if t == "*" || allowed[t] {
			return true
		}
	}
	return false
}

func agentSkillViewsFromVars(into map[string]any) []AgentSkillView {
	if into == nil {
		return nil
	}
	raw, ok := into["agent_skills"]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []AgentSkillView:
		return append([]AgentSkillView(nil), v...)
	case []map[string]any:
		out := make([]AgentSkillView, 0, len(v))
		for _, row := range v {
			name, _ := row["name"].(string)
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			desc, _ := row["description"].(string)
			out = append(out, AgentSkillView{
				Name:        name,
				Description: strings.TrimSpace(desc),
			})
		}
		return out
	default:
		return nil
	}
}

func filterToolName(names []string, drop string) []string {
	if len(names) == 0 {
		return names
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n == drop {
			continue
		}
		out = append(out, n)
	}
	return out
}

// TemplateRefForAgent resolves the persona template file agentID renders.
func TemplateRefForAgent(agentID string) (string, error) {
	agentID = strings.TrimSpace(agentID)
	ref, ok := agentdef.SystemPromptTemplateFor(agentID)
	if !ok {
		return "", fmt.Errorf("%w: agent %q is not registered", ErrUnknownAgent, agentID)
	}
	if ref = strings.TrimSpace(ref); ref == "" {
		return "", fmt.Errorf("agent %q: missing system_prompt_template", agentID)
	}
	return ref, nil
}

// ToolProfileForAgent resolves the tool profile agentID runs under.
func ToolProfileForAgent(agentID string) (string, error) {
	agentID = strings.TrimSpace(agentID)
	id, ok := agentdef.ToolProfileFor(agentID)
	if !ok {
		return "", fmt.Errorf("%w: agent %q is not registered", ErrUnknownAgent, agentID)
	}
	if id = strings.TrimSpace(id); id == "" {
		return "", fmt.Errorf("agent %q: missing tool_profile", agentID)
	}
	return id, nil
}
