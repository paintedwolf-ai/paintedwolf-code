package contribframe

import (
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/theme"
	"github.com/lycaon/lycaon/pkg/api"
)

// Project builds the wire projection for a captured frame.
func Project(f *Frame) api.ContributionFrameResponse {
	set := f.View.Contributions
	out := api.ContributionFrameResponse{
		FrameRevision:   f.Revision,
		Commands:        []api.ContributionCommand{},
		Menus:           []api.ContributionMenu{},
		Keybindings:     []api.ContributionKeybinding{},
		BindingDefaults: []api.ContributionBindingDefault{},
		EditorActions:   []api.ContributionEditorAction{},
		Themes:          []api.ContributionTheme{},
		Configuration:   []api.ContributionConfigurationProperty{},
		Requirements:    []api.ContributionRequirement{},
		SearchSources:   []api.ContributionSearchSource{},
		Operations:      []api.ContributionOperation{},
		Notes:           []api.ContributionNote{},
	}

	for _, command := range set.Commands() {
		id, err := contribution.ParseID(command.ID)
		if err != nil {
			continue
		}
		out.Commands = append(out.Commands, projectCommand(set, command, id))
	}

	for _, menu := range set.Menus() {
		out.Menus = append(out.Menus, api.ContributionMenu{
			ID:         menu.ID,
			Slot:       string(menu.Slot),
			Command:    menu.Command,
			Group:      menu.Group,
			Order:      menu.Order,
			Label:      menu.Label,
			When:       projectCondition(menu.When),
			State:      projectCondition(menu.State),
			StateLabel: menu.StateLabel,
		})
	}

	for _, keybinding := range set.Keybindings() {
		bindings := map[string][]string{}
		for platform, chords := range keybinding.Bindings {
			bindings[platform] = append([]string(nil), chords...)
		}
		out.Keybindings = append(out.Keybindings, api.ContributionKeybinding{
			ID:           keybinding.ID,
			Command:      keybinding.Command,
			Scope:        keybinding.Scope,
			AllowInInput: keybinding.AllowInInput,
			Bindings:     bindings,
			When:         projectCondition(keybinding.When),
		})
	}

	for _, def := range set.BindingDefaults() {
		row := api.ContributionBindingDefault{
			Platform: def.Platform,
			Scope:    def.Scope,
			Chord:    def.Chord,
		}
		if def.Active != nil {
			row.Active = def.Active.String()
		}
		for _, candidate := range def.Candidates {
			row.Candidates = append(row.Candidates, candidate.String())
		}
		out.BindingDefaults = append(out.BindingDefaults, row)
	}

	for _, action := range set.EditorActions() {
		out.EditorActions = append(out.EditorActions, api.ContributionEditorAction{
			ID:             action.ID,
			Title:          action.Title,
			TargetKind:     string(action.Target.Kind),
			TargetRequired: action.Target.Required,
			Preset:         string(action.Execution.Preset),
		})
	}

	for _, declared := range set.Themes() {
		compiled, err := declared.Compiled()
		if err != nil {
			continue
		}
		out.Themes = append(out.Themes, projectTheme(declared, compiled))
	}

	// Include an effective value for every setting.
	for _, setting := range set.Settings() {
		property := setting.Property
		scope := append([]string(nil), property.Scope...)
		if len(scope) == 0 {
			scope = []string{"device"}
		}
		out.Configuration = append(out.Configuration, api.ContributionConfigurationProperty{
			ID:          property.ID,
			Type:        string(property.Type),
			Description: property.Description,
			Default:     property.Default,
			Value:       setting.Value,
			IsDefault:   setting.Default,
			Scope:       scope,
			Enum:        append([]string(nil), property.Enum...),
			Min:         property.Min,
			Max:         property.Max,
		})
	}

	declared := map[contribution.ID]*contribution.MCPRequirement{}
	for _, req := range set.MCPRequirements() {
		if id, err := contribution.ParseID(req.ID); err == nil {
			declared[id] = req
		}
	}
	for _, status := range f.RequirementStatuses() {
		row := api.ContributionRequirement{
			ID:           status.ID.String(),
			Ready:        status.Ready,
			Reason:       status.Reason,
			MissingTools: append([]string(nil), status.MissingTools...),
		}
		if req := declared[status.ID]; req != nil {
			row.ProviderID = req.ProviderID
			row.Purpose = req.Purpose
		}
		out.Requirements = append(out.Requirements, row)
	}

	execution := projectExecutionProjection(f, set)
	out.SearchSources = execution.searchSources
	out.Operations = execution.operations

	for _, note := range set.Notes() {
		out.Notes = append(out.Notes, api.ContributionNote{
			Code:    note.Code,
			Message: note.Message,
			UnitID:  note.UnitID,
			PackID:  note.PackID,
		})
	}
	return out
}

type executionProjection struct {
	searchSources []api.ContributionSearchSource
	operations    []api.ContributionOperation
}

func projectExecutionProjection(f *Frame, set *contribution.Set) executionProjection {
	out := executionProjection{
		searchSources: []api.ContributionSearchSource{},
		operations:    []api.ContributionOperation{},
	}
	for _, source := range set.SearchSources() {
		id, idErr := contribution.ParseID(source.ID)
		requirementID, requirementErr := contribution.ParseID(source.Requirement)
		if idErr != nil || requirementErr != nil {
			continue
		}
		status, _ := f.Requirement(requirementID)
		ready, disabledReason := searchSourceReadiness(f, set, source, status)
		out.searchSources = append(out.searchSources, api.ContributionSearchSource{
			ID: source.ID, Provider: id.Provider, Label: source.Label, Prefix: source.Prefix,
			Requirement: source.Requirement, Tool: source.Tool,
			MinQueryLength: source.Query.MinLength, MaxResults: source.Query.MaxResults,
			Ready: ready, DisabledReason: disabledReason,
			ActivationCommand: source.Activation.Command,
		})
	}
	for _, operation := range set.Operations() {
		id, err := contribution.ParseID(operation.ID)
		if err != nil {
			continue
		}
		row := api.ContributionOperation{
			ID: operation.ID, Provider: id.Provider, Input: projectInputFields(operation.Input),
			ActionKind: string(operation.Action.Kind), ActionRef: operation.Action.Ref,
			ResultTreatment: string(operation.Result),
		}
		if row.ResultTreatment == "" {
			row.ResultTreatment = string(contribution.DefaultResultFor(operation.Action.Kind))
		}
		for _, field := range operation.Output {
			row.Output = append(row.Output, api.ContributionInputField{
				ID: field.ID, Type: string(field.Type), Required: field.Required,
				Values: append([]string(nil), field.Values...), Min: field.Min, Max: field.Max,
			})
		}
		out.operations = append(out.operations, row)
	}
	return out
}

func searchSourceReadiness(
	f *Frame,
	set *contribution.Set,
	source *contribution.SearchSource,
	status RequirementStatus,
) (bool, string) {
	if !status.Ready {
		return false, status.Reason
	}
	requirementID, err := contribution.ParseID(source.Requirement)
	if err != nil {
		return false, "tool_missing"
	}
	requirement, ok := set.MCPRequirement(requirementID)
	if !ok {
		return false, "tool_missing"
	}
	tool, ok := f.MCP.Tool(requirement.ProviderID, source.Tool)
	if !ok {
		return false, "tool_missing"
	}
	if !tool.ReadOnly {
		return false, "tool_not_read_only"
	}
	return true, ""
}

func projectCommand(
	set *contribution.Set,
	command *contribution.Command,
	id contribution.ID,
) api.ContributionCommand {
	resolved, _ := set.ResolveCommand(command)
	row := api.ContributionCommand{
		ID:              command.ID,
		Provider:        id.Provider,
		Title:           command.Title,
		Category:        command.Category,
		Scope:           command.Scope,
		Keywords:        append([]string(nil), command.Keywords...),
		Icon:            resolved.Icon,
		Executor:        string(contribution.ExecutorFor(resolved.Action.Kind)),
		Invocation:      string(contribution.InvocationScopeFor(resolved.Action.Kind)),
		ActionKind:      string(resolved.Action.Kind),
		When:            projectCondition(command.When),
		Enablement:      projectCondition(command.Enablement),
		ResultTreatment: string(resolved.Result),
	}
	if command.Palette != nil {
		palette := *command.Palette
		row.Palette = &palette
	}
	if resolved.Action.Kind == contribution.ActionNativeUI {
		row.HandlerID = resolved.Action.Handler
	}
	if resolved.Action.Ref != "" {
		row.ActionRef = resolved.Action.Ref
	}
	if len(resolved.Input) > 0 {
		row.Input = projectInputFields(resolved.Input)
	}
	if command.Interaction != nil {
		row.Interaction = projectInteraction(command.Interaction)
	}
	return row
}

func projectInputFields(fields []contribution.InputField) []api.ContributionInputField {
	out := make([]api.ContributionInputField, 0, len(fields))
	for _, field := range fields {
		out = append(out, api.ContributionInputField{
			ID: field.ID, Title: field.Title, Type: string(field.Type), Description: field.Description,
			Required: field.Required, Values: append([]string(nil), field.Values...), Default: field.Default,
			Min: field.Min, Max: field.Max,
		})
	}
	return out
}

func projectInteraction(interaction *contribution.Interaction) *api.ContributionInteraction {
	out := &api.ContributionInteraction{Steps: []api.ContributionInteractionStep{}}
	for _, step := range interaction.Steps {
		row := api.ContributionInteractionStep{
			ID: step.ID, Title: step.Title, Description: step.Description, Kind: string(step.Kind),
			Required: step.Required, Min: step.Min, Max: step.Max, Multiple: step.Multiple,
		}
		for _, choice := range step.Values {
			row.Values = append(row.Values, api.ContributionChoice{ID: choice.ID, Label: choice.Label, Description: choice.Description, Detail: choice.Detail})
		}
		if step.Source != nil {
			row.Source = &api.ContributionChoiceSource{Requirement: step.Source.Requirement, Tool: step.Source.Tool, Inputs: step.Source.Inputs}
		}
		if step.If != nil {
			row.If = &api.ContributionInteractionPredicate{Step: step.If.Step, Is: step.If.Is}
		}
		out.Steps = append(out.Steps, row)
	}
	return out
}

func projectCondition(c *contribution.Condition) *api.ContributionCondition {
	if c == nil {
		return nil
	}
	out := &api.ContributionCondition{Fact: c.Fact, Is: c.Is}
	for _, child := range c.All {
		out.All = append(out.All, *projectCondition(&child))
	}
	for _, child := range c.Any {
		out.Any = append(out.Any, *projectCondition(&child))
	}
	if c.Not != nil {
		out.Not = projectCondition(c.Not)
	}
	return out
}

// projectTheme preserves canonical color literals.
func projectTheme(declared *contribution.Theme, c theme.Compiled) api.ContributionTheme {
	row := api.ContributionTheme{
		ID:           c.ID,
		Name:         c.Name,
		Appearance:   string(c.Appearance),
		Tokens:       make(map[string]string, len(c.Tokens)),
		Syntax:       make(map[string]api.ContributionSyntaxStyle, len(c.Syntax)),
		Logomark:     string(c.Brand.Logomark),
		WindowColors: api.ContributionWindowColors{Main: c.WindowColors.Main.String(), Anchors: make([]string, 0, len(c.WindowColors.Anchors)), HueSpread: c.WindowColors.HueSpread, ChromaMin: c.WindowColors.ChromaMin, ChromaMax: c.WindowColors.ChromaMax},
		AgentColors:  api.ContributionAgentColors{Main: c.AgentColors.Main.String(), LightnessStep: c.AgentColors.LightnessStep, Chroma: c.AgentColors.Chroma},
		IconStroke: api.ContributionIconStroke{
			Weight: c.IconStroke.Weight,
			Cap:    c.IconStroke.Cap,
			Join:   c.IconStroke.Join,
		},
	}
	for _, anchor := range c.WindowColors.Anchors {
		row.WindowColors.Anchors = append(row.WindowColors.Anchors, anchor.String())
	}
	if id, err := contribution.ParseID(declared.ID); err == nil {
		row.Provider = id.Provider
	}
	for id, color := range c.Tokens {
		row.Tokens[id] = color.String()
	}
	for id, style := range c.Syntax {
		row.Syntax[id] = api.ContributionSyntaxStyle{
			Color:     style.Color.String(),
			Italic:    style.Italic,
			Bold:      style.Bold,
			Underline: style.Underline,
		}
	}
	if len(c.Icons) > 0 {
		row.Icons = make(map[string][]api.ContributionIconNode, len(c.Icons))
		for slot, nodes := range c.Icons {
			row.Icons[slot] = projectIconNodes(nodes)
		}
	}
	return row
}

func projectIconNodes(nodes []theme.IconNode) []api.ContributionIconNode {
	out := make([]api.ContributionIconNode, 0, len(nodes))
	for _, node := range nodes {
		row := api.ContributionIconNode{Tag: node.Tag}
		if len(node.Attrs) > 0 {
			row.Attrs = make(map[string]string, len(node.Attrs))
			for k, v := range node.Attrs {
				row.Attrs[k] = v
			}
		}
		if len(node.Children) > 0 {
			row.Children = projectIconNodes(node.Children)
		}
		out = append(out, row)
	}
	return out
}
