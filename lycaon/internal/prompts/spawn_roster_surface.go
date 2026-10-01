package prompts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
)

// SpawnAgentView is one worker row exposed to the coordinator.
type SpawnAgentView struct {
	ID           string
	Name         string
	Description  string
	ToolProfile  string
	EnabledTools []string
	DeniedTools  []string
	CanEdit      bool
	CanCommand   bool
	// ReadsProject reports whether the agent holds any tool that reads project files.
	ReadsProject bool
	// Delegates reports whether the agent may dispatch work.
	Delegates bool
	// SurfaceVariable reports whether the active surface supplies its tools.
	SurfaceVariable bool
}

// SpawnRosterData is the coordinator's effective spawn roster.
type SpawnRosterData struct {
	SpawnAgents        []SpawnAgentView
	NotSpawnableAgents []SpawnAgentView
	MaxInFlight        int
}

// LoadSpawnRosterSurface builds the effective spawn roster. A nil catalog
// resolves committed device state rather than reading pack directories.
func LoadSpawnRosterSurface(allowedAgentIDs []string, maxInFlight int, surfaceTools map[string][]string, catalog *extpacks.EffectiveCatalog, profiles []sandbox.ToolProfile) (SpawnRosterData, error) {
	if catalog == nil {
		resolved, err := extpacks.CatalogForConsumers()
		if err != nil {
			return SpawnRosterData{}, err
		}
		catalog = resolved
	}
	allAgents, err := agentdef.LoadEffectiveWithCatalog(catalog)
	if err != nil {
		return SpawnRosterData{}, err
	}
	if len(allAgents) == 0 {
		return SpawnRosterData{}, fmt.Errorf("no agent profiles found in effective catalog")
	}
	byID := make(map[string]agentdef.Profile, len(allAgents))
	for _, a := range allAgents {
		byID[a.ID] = a
	}

	if len(profiles) == 0 {
		profiles, err = sandbox.LoadToolProfiles()
		if err != nil {
			return SpawnRosterData{}, fmt.Errorf("load tool profiles: %w", err)
		}
	}
	profileByID := make(map[string]sandbox.ToolProfile, len(profiles))
	for _, p := range profiles {
		profileByID[p.ID] = p
	}

	allowedSet := make(map[string]struct{}, len(allowedAgentIDs))
	spawnAgents := make([]SpawnAgentView, 0, len(allowedAgentIDs))
	for _, id := range allowedAgentIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		agent, ok := byID[id]
		if !ok {
			return SpawnRosterData{}, fmt.Errorf("spawn allowlist agent %q not found in stock agents", id)
		}
		view, err := spawnAgentView(agent, profileByID, surfaceTools)
		if err != nil {
			return SpawnRosterData{}, err
		}
		spawnAgents = append(spawnAgents, view)
		allowedSet[id] = struct{}{}
	}

	notSpawnable := make([]SpawnAgentView, 0)
	for _, agent := range allAgents {
		if _, ok := allowedSet[agent.ID]; ok {
			continue
		}
		view, err := spawnAgentView(agent, profileByID, surfaceTools)
		if err != nil {
			return SpawnRosterData{}, err
		}
		notSpawnable = append(notSpawnable, view)
	}
	sort.Slice(notSpawnable, func(i, j int) bool { return notSpawnable[i].ID < notSpawnable[j].ID })

	if maxInFlight <= 0 {
		maxInFlight = spawn.MaxInFlightTaskWorkers
	}
	return SpawnRosterData{
		SpawnAgents:        spawnAgents,
		NotSpawnableAgents: notSpawnable,
		MaxInFlight:        maxInFlight,
	}, nil
}

func spawnAgentView(
	agent agentdef.Profile,
	profiles map[string]sandbox.ToolProfile,
	surfaceTools map[string][]string,
) (SpawnAgentView, error) {
	tp := strings.TrimSpace(agent.ToolProfile)
	prof, ok := profiles[tp]
	if !ok {
		return SpawnAgentView{}, fmt.Errorf("agent %q: tool_profile %q not found", agent.ID, tp)
	}
	if tools, variable := surfaceTools[agent.ID]; variable {
		return surfaceVariableAgentView(agent, tp, tools), nil
	}
	enabled := enabledToolNames(prof)
	denied := append([]string(nil), prof.DenyTools...)
	sort.Strings(denied)
	return SpawnAgentView{
		ID:           agent.ID,
		Name:         strings.TrimSpace(agent.Name),
		Description:  strings.TrimSpace(agent.Description),
		ToolProfile:  tp,
		EnabledTools: enabled,
		DeniedTools:  denied,
		CanEdit:      hasEditTool(prof.Tools),
		CanCommand:   prof.Tools["command"],
		ReadsProject: len(orderedEnabledTools(surveyToolDisplayOrder, prof.Tools)) > 0,
	}, nil
}

// editTools are the product-write tools; holding any one makes a write leg.
var editTools = []string{"write", "edit", "replace_lines", "code_rewrite", "jq_edit"}

func hasEditTool(enabled map[string]bool) bool {
	for _, name := range editTools {
		if enabled[name] {
			return true
		}
	}
	return false
}

// A surface-variable agent takes its tools from the turn surface, so no profile deny list applies.
func surfaceVariableAgentView(agent agentdef.Profile, toolProfile string, tools []string) SpawnAgentView {
	set := make(map[string]bool, len(tools))
	enabled := make([]string, 0, len(tools))
	for _, name := range tools {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		set[name] = true
		enabled = append(enabled, name)
	}
	sort.Strings(enabled)
	return SpawnAgentView{
		ID:              agent.ID,
		Name:            strings.TrimSpace(agent.Name),
		Description:     strings.TrimSpace(agent.Description),
		ToolProfile:     toolProfile,
		EnabledTools:    enabled,
		CanEdit:         hasEditTool(set),
		CanCommand:      set["command"],
		ReadsProject:    len(orderedEnabledTools(surveyToolDisplayOrder, set)) > 0,
		Delegates:       set["task"] || set["delegate_dispatch"],
		SurfaceVariable: true,
	}
}

func enabledToolNames(prof sandbox.ToolProfile) []string {
	enabled := make([]string, 0, len(prof.Tools))
	for name, ok := range prof.Tools {
		if ok {
			enabled = append(enabled, name)
		}
	}
	sort.Strings(enabled)
	return enabled
}

// SpawnRosterTemplateVars builds the visible spawn roster.
func SpawnRosterTemplateVars(data SpawnRosterData, excludedDisclosures []SpawnAgentView) map[string]any {
	spawnCtx := make([]map[string]any, len(data.SpawnAgents))
	for i, a := range data.SpawnAgents {
		spawnCtx[i] = spawnAgentTemplateVars(a)
	}
	excludedCtx := make([]map[string]any, len(excludedDisclosures))
	for i, a := range excludedDisclosures {
		excludedCtx[i] = spawnAgentTemplateVars(a)
	}
	return map[string]any{
		"spawn_agents":         spawnCtx,
		"excluded_disclosures": excludedCtx,
		"max_in_flight":        data.MaxInFlight,
	}
}

// SpawnRosterRoleVars splits the spawnable roster into read and write worker ids
// so mode partials can name roles from the effective catalog. Surface-variable
// rows are the coordinator itself, not a worker.
func SpawnRosterRoleVars(data SpawnRosterData) map[string]any {
	readIDs := make([]string, 0, len(data.SpawnAgents))
	writeIDs := make([]string, 0, len(data.SpawnAgents))
	for _, a := range data.SpawnAgents {
		if a.SurfaceVariable {
			continue
		}
		if a.CanEdit {
			writeIDs = append(writeIDs, a.ID)
			continue
		}
		readIDs = append(readIDs, a.ID)
	}
	sort.Strings(readIDs)
	sort.Strings(writeIDs)
	return map[string]any{
		"spawn_read_agent_ids":  readIDs,
		"spawn_write_agent_ids": writeIDs,
	}
}

func spawnAgentTemplateVars(a SpawnAgentView) map[string]any {
	return map[string]any{
		"id":               a.ID,
		"name":             a.Name,
		"description":      a.Description,
		"tool_profile":     a.ToolProfile,
		"enabled_tools":    a.EnabledTools,
		"denied_tools":     a.DeniedTools,
		"can_edit":         a.CanEdit,
		"can_command":      a.CanCommand,
		"reads_project":    a.ReadsProject,
		"delegates":        a.Delegates,
		"surface_variable": a.SurfaceVariable,
	}
}

// SpawnRosterFingerprint hashes roster inputs for assembly cache invalidation.
func SpawnRosterFingerprint(data SpawnRosterData, excludedDisclosures []SpawnAgentView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "max_in_flight=%d\n", data.MaxInFlight)
	for _, a := range data.SpawnAgents {
		writeSpawnAgentFingerprint(&b, a)
	}
	for _, a := range excludedDisclosures {
		writeSpawnAgentFingerprint(&b, a)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func writeSpawnAgentFingerprint(b *strings.Builder, a SpawnAgentView) {
	fmt.Fprintf(b, "id=%s profile=%s edit=%t command=%t delegates=%t variable=%t desc=%s tools=%s denied=%s\n",
		a.ID, a.ToolProfile, a.CanEdit, a.CanCommand, a.Delegates, a.SurfaceVariable, a.Description,
		strings.Join(a.EnabledTools, ","),
		strings.Join(a.DeniedTools, ","),
	)
}
