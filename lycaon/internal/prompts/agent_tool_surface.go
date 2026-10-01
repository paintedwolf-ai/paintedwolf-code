package prompts

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolschema"
)

const (
	// AgentToolSurfacePartialRef renders worker tools and rejection codes.
	AgentToolSurfacePartialRef = "partials/agent-tool-surface.md"
)

// preventiveRejectEmitters surface recoverable codes before tool use.
var preventiveRejectEmitters = map[string]bool{
	"guard:command_habit_redirect": true,
	"guard:write_scope":            true,
	"guard:command_surface":        true,
}

// PreventiveRejectEmitters returns preflight rejection guards.
func PreventiveRejectEmitters() []string {
	out := make([]string, 0, len(preventiveRejectEmitters))
	for emit := range preventiveRejectEmitters {
		out = append(out, emit)
	}
	sort.Strings(out)
	return out
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
	// Skills supply availability and named reference checks; they are not listed.
	Skills               []AgentSkillView
	HostResources        []AgentHostResourceView
	HostResourcesOmitted int // rows the ambient budget dropped
	Fingerprint          string
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
		"agent_host_resources":         groupHostResources(surface.HostResources),
		"agent_host_resources_omitted": surface.HostResourcesOmitted,
		"prompt_surface_fingerprint":   surface.Fingerprint,
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

// LoadAgentToolSurface builds one profile's prompt surface. turn.Loaded names
// the profile's deferred tools the session ledger has loaded; they are offered
// on this call like sticky ones, and the rest list as requestable with the
// description turn.Schemas holds for them.
func LoadAgentToolSurface(toolProfileID string, visibleTools []string, hintCodes map[string]hintCodeRow, turn SurfaceTurn, profiles []sandbox.ToolProfile) (AgentToolSurfaceData, error) {
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

	toolNames := visibleToolNames(profile, visibleTools)
	toolViews := make([]AgentToolArgView, 0, len(toolNames))
	var requestable []string
	for _, name := range toolNames {
		if (profile.ToolDeferred(name) && !turn.Loaded[name]) || !profile.ToolAllowed(name) {
			requestable = append(requestable, name)
			continue
		}
		view := AgentToolArgView{Name: name}
		if turn.Schemas != nil {
			if meta, ok := turn.Schemas.ToolMeta(name); ok {
				view.RequiredArgs, _ = toolschema.ArgFieldSummary(meta.ArgsSchema)
			}
		}
		toolViews = append(toolViews, view)
	}
	roster, err := RequestableTools(requestable, turn.Schemas)
	if err != nil {
		return AgentToolSurfaceData{}, err
	}

	return AgentToolSurfaceData{
		ToolProfile: profileID,
		Tools:       toolViews,
		Requestable: roster,
		RejectCodes: recoverableRejectCodes(hintCodes, toolNames),
		WriteGlobs:  append([]string(nil), profile.WriteGlobs...),
	}, nil
}

func visibleToolNames(profile sandbox.ToolProfile, visible []string) []string {
	if len(visible) > 0 {
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

// AgentToolSurfaceTemplateVars builds tool-surface template data.
func AgentToolSurfaceTemplateVars(data AgentToolSurfaceData) map[string]any {
	toolsCtx := make([]map[string]any, len(data.Tools))
	for i, t := range data.Tools {
		toolsCtx[i] = map[string]any{
			"name":          t.Name,
			"required_args": t.RequiredArgs,
		}
	}
	codesCtx := make([]map[string]any, len(data.RejectCodes))
	for i, c := range data.RejectCodes {
		codesCtx[i] = map[string]any{
			"code":               c.Code,
			"branch_instruction": c.BranchInstruction,
		}
	}
	skillsCtx := make([]map[string]any, len(data.Skills))
	for i, s := range data.Skills {
		skillsCtx[i] = map[string]any{
			"name":        s.Name,
			"description": s.Description,
		}
	}
	out := map[string]any{
		"tool_profile":             data.ToolProfile,
		"agent_tools":              toolsCtx,
		"agent_tool_names":         toolViewNames(data.Tools),
		"requestable_tools":        requestableToolVars(data.Requestable),
		"requestable_capabilities": RequestableCapabilities(data.Requestable),
		"agent_skills":             skillsCtx,
		"reject_codes":             codesCtx,
		"write_globs":              append([]string(nil), data.WriteGlobs...),
	}
	for k, v := range toolProfileCapabilityTemplateVars(data) {
		out[k] = v
	}
	return out
}

// MergeAgentToolSurfaceVars adds profile data to a render map.
func MergeAgentToolSurfaceVars(toolProfileID string, visibleTools []string, hintCodes map[string]hintCodeRow, turn SurfaceTurn, into map[string]any, profiles []sandbox.ToolProfile) error {
	if into == nil {
		return fmt.Errorf("nil template vars map")
	}
	skills := agentSkillViewsFromVars(into)
	visible := visibleTools
	if len(skills) == 0 {
		visible = filterToolName(visible, "skills_read")
	}
	data, err := LoadAgentToolSurface(toolProfileID, visible, hintCodes, turn, profiles)
	if err != nil {
		return err
	}
	if !surfaceGrantsSkillsRead(data) || len(skills) == 0 {
		data.Tools = filterAgentTool(data.Tools, "skills_read")
		data.Skills = nil
	} else {
		data.Skills = skills
	}
	for k, v := range AgentToolSurfaceTemplateVars(data) {
		into[k] = v
	}
	mergeVisualShowVarsFromSurface(data, into)
	return nil
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

// surfaceGrantsSkillsRead reports whether skills_read is on the sticky schema.
func surfaceGrantsSkillsRead(data AgentToolSurfaceData) bool {
	for _, t := range data.Tools {
		if t.Name == "skills_read" {
			return true
		}
	}
	return false
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

func filterAgentTool(views []AgentToolArgView, drop string) []AgentToolArgView {
	out := make([]AgentToolArgView, 0, len(views))
	for _, v := range views {
		if v.Name == drop {
			continue
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
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
