package prompts

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// AuthorRenderRequest describes an authored preview.
type AuthorRenderRequest struct {
	ModuleRoot  string         // host configuration root
	ProjectDir  string         // "" = no project overlay
	TemplateRef string         // xor AgentID xor SkillName
	AgentID     string         // renders the full persona through the contract
	SkillName   string         // renders one skill body or resource; AgentID is the reader
	Resource    string         // skill-relative resource path; "" = the SKILL.md body
	SurfaceID   string         // coordinator turn surface for skill renders
	Vars        map[string]any // typed facts merged last into the render context
	Check       bool           // also enforce heading contract + byte budget
}

// AuthorRenderResult carries output and stable violations.
type AuthorRenderResult struct {
	Output     string
	Violations []string
	Revision   string
}

// AuthorRender renders a template or persona preview.
func AuthorRender(ctx context.Context, req AuthorRenderRequest) (AuthorRenderResult, error) {
	moduleRoot := strings.TrimSpace(req.ModuleRoot)
	if moduleRoot == "" {
		return AuthorRenderResult{}, fmt.Errorf("module root required")
	}
	ref := strings.TrimSpace(req.TemplateRef)
	agentID := strings.TrimSpace(req.AgentID)
	if skillName := strings.TrimSpace(req.SkillName); skillName != "" {
		if ref != "" {
			return AuthorRenderResult{}, fmt.Errorf("template ref and --skill are mutually exclusive")
		}
		return renderSkillPreview(ctx, req, skillName, agentID)
	}
	if ref == "" && agentID == "" {
		return AuthorRenderResult{}, fmt.Errorf("a template ref or --agent is required")
	}
	if ref != "" && agentID != "" {
		return AuthorRenderResult{}, fmt.Errorf("template ref and --agent are mutually exclusive")
	}

	engine := NewFileTemplateEngineLayers(PromptLayers{
		ModuleRoot: moduleRoot,
		Site:       SitePromptFilesDir(moduleRoot),
	})
	if project := strings.TrimSpace(req.ProjectDir); project != "" {
		engine = engine.WithProjectOverlays([]string{project})
	}
	engine, err := engine.Snapshot()
	if err != nil {
		return AuthorRenderResult{}, err
	}

	vars := make(map[string]any, len(req.Vars))
	for k, v := range req.Vars {
		vars[k] = v
	}

	out := AuthorRenderResult{Revision: engine.Revision()}
	if agentID != "" {
		body, err := renderPersona(ctx, engine, agentID, vars, false)
		if err != nil {
			return AuthorRenderResult{}, err
		}
		out.Output = body
	} else {
		if err := mergeAuthorUnits(ctx, engine, vars); err != nil {
			return AuthorRenderResult{}, err
		}
		body, err := engine.Render(ctx, ref, vars)
		if err != nil {
			return AuthorRenderResult{}, err
		}
		out.Output = body
	}

	if !req.Check {
		return out, nil
	}
	violations, err := ValidatePersonaRender(agentID, out.Output)
	if err != nil {
		return AuthorRenderResult{}, err
	}
	out.Violations = violations
	return out, nil
}

// renderSkillPreview renders one skill the way skills_read delivers it: project
// skills literal, pack bodies and references/*.md through their template with
// the caller surface vars and pack configuration.
func renderSkillPreview(ctx context.Context, req AuthorRenderRequest, skillName, agentID string) (AuthorRenderResult, error) {
	sk, err := loadRenderSkill(skillName, req.ProjectDir)
	if err != nil {
		return AuthorRenderResult{}, err
	}
	resource := strings.TrimSpace(req.Resource)
	source := sk.Body
	if resource != "" {
		body, err := sk.ReadResource(resource)
		if err != nil {
			return AuthorRenderResult{}, err
		}
		source = string(body)
	}
	profileID, surfaceID, err := skillPreviewProfile(agentID, req.SurfaceID)
	if err != nil {
		return AuthorRenderResult{}, err
	}
	out := AuthorRenderResult{}
	templated := !sk.Project && (resource == "" || sk.TemplatesResource(resource))
	if templated {
		vars, err := skillPreviewVars(req.Vars, profileID, surfaceID, sk)
		if err != nil {
			return AuthorRenderResult{}, err
		}
		tpl, err := pongoplain.Compile(source)
		if err != nil {
			return AuthorRenderResult{}, fmt.Errorf("skill %q: %w", sk.Name, err)
		}
		rendered, err := pongoplain.Execute(ctx, tpl, vars)
		if err != nil {
			return AuthorRenderResult{}, fmt.Errorf("skill %q: %w", sk.Name, err)
		}
		out.Output = rendered
	} else {
		out.Output = source
	}
	if req.Check {
		violations, err := checkSkillRender(sk, resource, source, out.Output, templated, profileID, surfaceID)
		if err != nil {
			return AuthorRenderResult{}, err
		}
		out.Violations = violations
	}
	return out, nil
}

// skillPreviewProfile resolves the reader profile and turn surface a skill
// preview renders with: the agent's tool profile, with the routing surface as
// the coordinator default.
func skillPreviewProfile(agentID, surfaceID string) (string, string, error) {
	if strings.TrimSpace(agentID) == "" {
		agentID = "coordinator"
	}
	profileID, err := ToolProfileForAgent(agentID)
	if err != nil {
		return "", "", err
	}
	surfaceID = strings.TrimSpace(surfaceID)
	if profileID == CoordinatorProfileID && surfaceID == "" {
		surfaceID = spawn.SurfaceImplementRouting
	}
	return profileID, surfaceID, nil
}

// skillPreviewVars builds the render context one pack skill read carries: the
// caller surface vars, the pack's effective configuration, and the explicit
// preview vars on top.
func skillPreviewVars(explicit map[string]any, profileID, surfaceID string, sk skills.Skill) (map[string]any, error) {
	vars, err := SkillSurfaceTemplateVars(SkillSurfaceVarsInput{
		ProfileID:        profileID,
		SurfaceID:        surfaceID,
		HasGit:           true,
		RootCount:        1,
		DelegationBudget: spawn.DefaultWorkerToolBudget(),
	})
	if err != nil {
		return nil, err
	}
	configuration, err := packConfigurationVars(sk.PackID)
	if err != nil {
		return nil, err
	}
	if configuration == nil {
		configuration = map[string]any{}
	}
	vars["configuration"] = configuration
	for k, v := range explicit {
		vars[k] = v
	}
	return vars, nil
}

// loadRenderSkill finds one skill in the effective catalog, project skills
// combined over the device catalog the way a session combines them.
func loadRenderSkill(name, projectDir string) (skills.Skill, error) {
	loaded, err := loadRenderSkills(projectDir)
	if err != nil {
		return skills.Skill{}, err
	}
	for _, sk := range loaded {
		if sk.Name == name {
			return sk, nil
		}
	}
	return skills.Skill{}, fmt.Errorf("skill %q not found in the effective catalog", name)
}

func loadRenderSkills(projectDir string) ([]skills.Skill, error) {
	eff, err := extpacks.CatalogForConsumers()
	if err != nil {
		return nil, err
	}
	device, _ := extpacks.LoadEffectiveSkills(eff)
	if dir := strings.TrimSpace(projectDir); dir != "" {
		project, notes := skills.DiscoverProject([]string{dir})
		loaded, _ := extpacks.CombineSkills(device, nil, project, notes)
		return loaded, nil
	}
	return device, nil
}

// packConfigurationVars resolves one pack's effective configuration values the
// way the runtime's skills_read does, from the active catalog's configuration
// contributions and desired state.
func packConfigurationVars(packID string) (map[string]any, error) {
	packID = strings.TrimSpace(packID)
	if packID == "" {
		return map[string]any{}, nil
	}
	eff, err := extpacks.CatalogForConsumers()
	if err != nil {
		return nil, err
	}
	var inputs []contribution.Input
	for _, id := range eff.LoadedUnitIDs() {
		u, ok := eff.Loaded[id]
		if !ok {
			continue
		}
		kind, ok := contribution.KindForUnitRoot(u.Kind)
		if !ok || kind != contribution.KindConfiguration {
			continue
		}
		origin := ""
		if path, ok := eff.UnitPath(id); ok {
			origin = path.String()
		}
		inputs = append(inputs, contribution.Input{
			UnitID:         id,
			Kind:           kind,
			ProviderPackID: u.WinnerPackID,
			Body:           u.Content,
			Origin:         origin,
		})
	}
	set, err := contribution.Compile(contribution.CompileInput{
		Units:         inputs,
		Configuration: eff.Desired.Configuration,
		UnitProvider: func(id string) (string, bool) {
			_, provider, ok := eff.UnitContent(id)
			return provider, ok
		},
		PackPresent:   eff.PackContributed,
		PackDependsOn: eff.PackDependsOn,
		ProviderRank: func(packID string) contribution.ProviderRank {
			if eff.StockAuthority(packID) {
				return contribution.RankStock
			}
			return contribution.RankDevice
		},
	})
	if err != nil {
		return nil, err
	}
	return set.SettingsForPack(packID), nil
}

// checkSkillRender records the authoring violations a skill preview must fix:
// the byte budget, template markers that leaked outside verbatim protection,
// and tool names the rendered copy tells its reader to call but the target
// profile and surface cannot reach.
func checkSkillRender(sk skills.Skill, resource, source, rendered string, templated bool, profileID, surfaceID string) ([]string, error) {
	var violations []string
	budget := skills.BundledBodyBudget
	name := sk.Name
	if resource != "" {
		budget = skills.BundledResourceBudget
		name = sk.Name + "/" + resource
	}
	if len(rendered) > budget {
		violations = append(violations, fmt.Sprintf("budget_exceeded:%s:%d/%d", name, len(rendered), budget))
	}
	if !templated {
		return violations, nil
	}
	if leftoverTemplateMarkers(source, rendered) {
		violations = append(violations, "leftover_template_markers")
	}
	if sk.UserProvided {
		return violations, nil
	}
	reachable, err := skillReachableTools(profileID, surfaceID)
	if err != nil {
		return nil, err
	}
	declared, err := skillDeclaredNames()
	if err != nil {
		return nil, err
	}
	for _, tool := range skillUnreachableNames(rendered, declared, reachable) {
		violations = append(violations, "unreachable_tool:"+tool)
	}
	return violations, nil
}

// skillReachableTools names every tool the target reader can reach: a
// coordinator surface's floor plus its loadable set, or a worker profile's
// enabled plus requestable tools, along with ReachCommandMutation when the
// scope permits mutating command programs.
func skillReachableTools(profileID, surfaceID string) (map[string]bool, error) {
	if profileID == CoordinatorProfileID && surfaceID != "" {
		floor, err := LoadCoordinatorSurfaceFloor(surfaceID)
		if err != nil {
			return nil, err
		}
		loadable, err := LoadCoordinatorSurfaceLoadable(surfaceID)
		if err != nil {
			return nil, err
		}
		offered, requestable := OfferedTools(floor, loadable, nil)
		out := make(map[string]bool, len(offered)+len(requestable)+1)
		for _, name := range offered {
			out[name] = true
		}
		for _, name := range requestable {
			out[name] = true
		}
		out[toolcontract.ReachCommandMutation] = out["command"] && CoordinatorSurfaceAllowsCommandMutation(surfaceID)
		return out, nil
	}
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		return nil, err
	}
	surface, err := LoadAgentToolSurface(profileID, nil, nil, SurfaceTurn{}, profiles)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(surface.Tools)+len(surface.Requestable)+1)
	for _, tool := range surface.Tools {
		out[tool.Name] = true
	}
	for _, row := range surface.Requestable {
		out[row.Name] = true
	}
	out[toolcontract.ReachCommandMutation] = out["command"] && ScopeAllowsCommandMutation(profileID)
	return out, nil
}

// skillDeclaredNames builds the declared tool vocabulary the unreachable check
// resolves names against.
func skillDeclaredNames() (map[string]bool, error) {
	cfg, err := nativemanifest.Load()
	if err != nil {
		return nil, fmt.Errorf("native tool manifest: %w", err)
	}
	names := map[string]bool{}
	for _, list := range [][]string{cfg.AllTools(), cfg.HostProducedTools, cfg.AllOperations()} {
		for _, name := range list {
			if name = strings.TrimSpace(name); name != "" {
				names[name] = true
			}
		}
	}
	eff, err := extpacks.CatalogForConsumers()
	if err != nil {
		return nil, err
	}
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	if err != nil {
		return nil, err
	}
	for name := range schemas.Tools {
		if name = strings.TrimSpace(name); name != "" {
			names[name] = true
		}
	}
	return names, nil
}

// namedToolCall matches backticked names — how copy tells a reader to call
// something — the same shape toolvocab scans for.
var namedToolCall = regexp.MustCompile("`([a-z][a-z0-9_]*)(?:`|[ \t]*\\()")

// skillUnreachableNames returns the declared tools, operations, and command
// forms rendered text names that reachable cannot satisfy, sorted.
func skillUnreachableNames(text string, declared, reachable map[string]bool) []string {
	seen := map[string]bool{}
	for _, m := range namedToolCall.FindAllStringSubmatch(text, -1) {
		name := m[1]
		if !declared[name] {
			continue
		}
		if reachable[name] || toolcontract.CommandLanguageReachable(name, reachable) {
			continue
		}
		seen[name] = true
	}
	for _, name := range toolcontract.HyphenatedCommandFormsIn(text) {
		if reachable[name] || toolcontract.CommandLanguageReachable(name, reachable) {
			continue
		}
		seen[name] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// verbatimBlock matches the verbatim protection a template wraps literal
// marker text in.
var verbatimBlock = regexp.MustCompile(`(?s)\{% verbatim %\}(.*?)\{% endverbatim %\}`)

// leftoverTemplateMarkers reports markers that survived rendering outside
// verbatim protection: verbatim contents are preserved literally, so they are
// stripped from the output before the scan.
func leftoverTemplateMarkers(source, rendered string) bool {
	for _, match := range verbatimBlock.FindAllStringSubmatch(source, -1) {
		rendered = strings.ReplaceAll(rendered, match[1], "")
	}
	return strings.Contains(rendered, "{{") || strings.Contains(rendered, "{%")
}

// mergeAuthorUnits renders the unit slots a preview's facts select, so a
// template that carries a slot previews the way the host renders it. A tool
// counts as offered when the preview says profile_has_<tool> is true; an
// explicit units var is kept.
func mergeAuthorUnits(ctx context.Context, engine *FileTemplateEngine, vars map[string]any) error {
	if _, ok := vars["units"]; ok {
		return nil
	}
	catalog, err := UnitCatalogForEngine(engine)
	if err != nil {
		return fmt.Errorf("unit catalog: %w", err)
	}
	var offered []string
	for key, value := range vars {
		if name, ok := strings.CutPrefix(key, "profile_has_"); ok && value == true {
			offered = append(offered, name)
		}
	}
	mode, _ := vars["execution_mode"].(string)
	sel := UnitSelectionVars(promptunit.HostCoordinator, mode, offered, offered, nil)
	blocks, err := RenderUnitSlots(ctx, UnitRendererFor(engine), catalog, sel, vars)
	if err != nil {
		return err
	}
	MergeUnitVars(vars, blocks)
	return nil
}

// ValidatePersonaRender checks the final composed persona, not source fragments.
func ValidatePersonaRender(agentID, body string) ([]string, error) {
	violations, err := validatePersonaContractRender(agentID, body)
	if err != nil {
		return nil, err
	}
	budgets, err := LoadPromptBudgets()
	if err != nil {
		return nil, fmt.Errorf("prompt budgets: %w", err)
	}
	if capBytes := budgets.Sizes.Cap("worker_personas", agentID); len(body) > capBytes {
		violations = append(violations, fmt.Sprintf("budget_exceeded:%s:%d/%d", agentID, len(body), capBytes))
	}
	return violations, nil
}

// validatePersonaContractRender enforces invariants required for a worker to
// run. Per-artifact byte caps stay in ValidatePersonaRender for authoring and
// release checks because runtime render variables can add legitimate content.
func validatePersonaContractRender(agentID, body string) ([]string, error) {
	if agentID == "" {
		return nil, nil
	}
	contract, err := LoadPersonaContract()
	if err != nil {
		return nil, fmt.Errorf("persona contract: %w", err)
	}
	var violations []string
	for _, heading := range contract.RequiredHeadingsRendered {
		heading = strings.TrimSpace(heading)
		if heading == "" {
			continue
		}
		if !strings.Contains(body, heading) {
			violations = append(violations, "missing_heading:"+heading)
		}
	}
	if def, ok := contract.Agents[agentID]; ok {
		for _, must := range def.MustSubstringsRendered {
			must = strings.TrimSpace(must)
			if must != "" && !strings.Contains(body, must) {
				violations = append(violations, "missing_substring:"+must)
			}
		}
	}
	return violations, nil
}
