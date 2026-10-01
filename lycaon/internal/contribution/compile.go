package contribution

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ProviderRank orders declaration provenance: stock over device.
type ProviderRank int

const (
	RankStock ProviderRank = iota
	RankDevice
)

// CompileInput contains winning units and their catalog context.
type CompileInput struct {
	Units []Input
	// Configuration maps pack ids to effective property values.
	Configuration map[string]map[string]any
	// UnitProvider resolves prompt and workflow providers.
	UnitProvider func(unitID string) (string, bool)
	// PackPresent gates configuration validation.
	PackPresent func(packID string) bool
	// ProviderRank orders defaults and gates stock declarations.
	ProviderRank func(packID string) ProviderRank
	// PackDependsOn reports a resolved manifest dependency.
	PackDependsOn func(packID, dependencyID string) bool
}

// Note is a non-fatal compile observation, surfaced with the set.
type Note struct {
	Code    string
	Message string
	UnitID  string
	PackID  string
}

// NoteBindingConflict marks keybinding defaults deactivated by collision.
const NoteBindingConflict = "keybinding_default_conflict"

// BindingDefault is one resolved chord slot.
type BindingDefault struct {
	Platform   string
	Scope      string
	Chord      string
	Active     *ID
	Candidates []ID
}

// Compile validates all units and returns an immutable set.
func Compile(in CompileInput) (*Set, error) {
	c := &compiler{
		in: in,
		set: &Set{
			byID: map[ID]Unit{}, kinds: map[Kind][]ID{},
			commands: map[ID]*Command{}, menus: map[ID]*Menu{},
			keybindings: map[ID]*Keybinding{}, editorActions: map[ID]*EditorAction{},
			configuration: map[ID]*ConfigurationProperty{}, requirements: map[ID]*MCPRequirement{},
			searchSources: map[ID]*SearchSource{}, operations: map[ID]*Operation{},
			themes: map[ID]*Theme{}, settings: map[ID]ConfigurationSetting{},
		},
	}
	c.identities()
	c.bounds()
	c.declarations()
	if len(c.faults) > 0 {
		return nil, &CompileError{Faults: c.faults}
	}
	c.graph()
	c.resolveBindingDefaults()
	c.configurationValues()
	if len(c.faults) > 0 {
		return nil, &CompileError{Faults: c.faults}
	}
	for _, ids := range c.set.kinds {
		sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	}
	sort.Slice(c.set.notes, func(i, j int) bool {
		if c.set.notes[i].Code != c.set.notes[j].Code {
			return c.set.notes[i].Code < c.set.notes[j].Code
		}
		return c.set.notes[i].Message < c.set.notes[j].Message
	})
	return c.set, nil
}

type compiler struct {
	in     CompileInput
	set    *Set
	faults []Fault
}

func (c *compiler) fault(unit Unit, code, message string) {
	c.faults = append(c.faults, Fault{
		PackID: unit.ProviderPackID, UnitID: unit.UnitID, Code: code, Message: message,
	})
}

func (c *compiler) rank(packID string) ProviderRank {
	if c.in.ProviderRank == nil {
		return RankDevice
	}
	return c.in.ProviderRank(packID)
}

func (c *compiler) stock(packID string) bool { return c.rank(packID) == RankStock }

// identities validates container identity and duplicates.
func (c *compiler) identities() {
	sorted := append([]Input(nil), c.in.Units...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].UnitID < sorted[j].UnitID })

	firstByID := map[ID]Input{}
	fault := func(in Input, code, message string) {
		c.faults = append(c.faults, Fault{PackID: in.ProviderPackID, UnitID: in.UnitID, Code: code, Message: message})
	}
	for _, input := range sorted {
		id, ok := compileIdentity(input, fault)
		if !ok {
			continue
		}
		if prior, dup := firstByID[id]; dup {
			fault(input, FaultDuplicateID, fmt.Sprintf("id %s already declared by %s", id, prior.UnitID))
			continue
		}
		firstByID[id] = input
		c.set.byID[id] = Unit{
			ID: id, Kind: input.Kind, ProviderPackID: input.ProviderPackID,
			UnitID: input.UnitID, Body: input.Body, Origin: input.Origin,
		}
		c.set.kinds[input.Kind] = append(c.set.kinds[input.Kind], id)
	}
}

// bounds attributes declaration limits to the pack and kind.
func (c *compiler) bounds() {
	counts := map[string]map[Kind]int{}
	for id, unit := range c.set.byID {
		if counts[id.Provider] == nil {
			counts[id.Provider] = map[Kind]int{}
		}
		counts[id.Provider][unit.Kind]++
	}
	providers := make([]string, 0, len(counts))
	for provider := range counts {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	for _, provider := range providers {
		for _, kind := range Kinds() {
			count := counts[provider][kind]
			if count <= MaxDeclarationsPerKind {
				continue
			}
			c.faults = append(c.faults, Fault{
				PackID: provider, Code: FaultBounds,
				Message: fmt.Sprintf("pack %s declares %d %s contributions, over the limit of %d",
					provider, count, kind, MaxDeclarationsPerKind),
			})
		}
	}
}

// declarations decodes each unit under its kind schema.
func (c *compiler) declarations() {
	for id, unit := range c.set.byID {
		switch unit.Kind {
		case KindCommand:
			var decl Command
			c.decode(unit, &decl, func() error { return validateCommand(&decl, c.stock(unit.ProviderPackID)) })
			c.set.commands[id] = &decl
		case KindMenu:
			var decl Menu
			c.decode(unit, &decl, func() error { return validateMenu(&decl) })
			c.set.menus[id] = &decl
		case KindKeybinding:
			var decl Keybinding
			c.decode(unit, &decl, func() error { return validateKeybinding(&decl, c.stock(unit.ProviderPackID)) })
			c.set.keybindings[id] = &decl
		case KindEditorAction:
			var decl EditorAction
			c.decode(unit, &decl, func() error { return validateEditorAction(&decl) })
			c.set.editorActions[id] = &decl
		case KindConfiguration:
			var decl ConfigurationProperty
			c.decode(unit, &decl, func() error { return validateConfigurationProperty(&decl) })
			c.set.configuration[id] = &decl
		case KindMCPRequirement:
			var decl MCPRequirement
			c.decode(unit, &decl, func() error { return validateMCPRequirement(&decl) })
			c.set.requirements[id] = &decl
		case KindSearchSource:
			var decl SearchSource
			c.decode(unit, &decl, func() error { return validateSearchSource(&decl) })
			c.set.searchSources[id] = &decl
		case KindOperation:
			var decl Operation
			c.decode(unit, &decl, func() error { return validateOperation(&decl, c.stock(unit.ProviderPackID)) })
			c.set.operations[id] = &decl
		case KindTheme:
			var decl Theme
			c.decode(unit, &decl, func() error { return validateTheme(&decl) })
			c.set.themes[id] = &decl
		}
	}
}

func (c *compiler) decode(unit Unit, out any, validate func() error) {
	if err := decodeStrict(unit.Body, out); err != nil {
		c.fault(unit, FaultSchema, err.Error())
		return
	}
	if err := validate(); err != nil {
		code := FaultSchema
		var authority authorityError
		if errors.As(err, &authority) {
			code = FaultAuthority
		}
		c.fault(unit, code, err.Error())
	}
}

// graph validates every cross-declaration reference and condition.
func (c *compiler) graph() {
	scope := conditionScope{
		RequirementExists: func(id ID) bool {
			_, ok := c.set.requirements[id]
			return ok
		},
		BooleanConfiguration: func(id ID) (bool, bool) {
			property, ok := c.set.configuration[id]
			if !ok {
				return false, false
			}
			return true, property.Type == PropertyBoolean
		},
	}
	for id, command := range c.set.commands {
		unit := c.set.byID[id]
		c.commandGraph(unit, command, scope)
	}
	c.operationGraph()
	prefixes := map[string]ID{}
	for id, source := range c.set.searchSources {
		unit := c.set.byID[id]
		if prior, exists := prefixes[source.Prefix]; exists {
			c.fault(unit, FaultReference, fmt.Sprintf("search prefix %q is already declared by %s", source.Prefix, prior))
		} else {
			prefixes[source.Prefix] = id
		}
		c.requirementToolRef(unit, "requirement", source.Requirement, source.Tool, scope)
		commandRef, ok := c.parseRef(unit, "activation.command", source.Activation.Command, func(ref ID) bool {
			_, exists := c.set.commands[ref]
			return exists
		})
		if ok {
			if commandRef.Provider != id.Provider {
				c.fault(unit, FaultAuthority, "search-source activation command must belong to the declaring pack")
			}
			command := c.set.commands[commandRef]
			resolved, resolvedOK := c.set.ResolveCommand(command)
			if !resolvedOK {
				c.fault(unit, FaultReference, "activation command operation contract cannot be resolved")
				continue
			}
			if len(resolved.Input) > 0 && strings.TrimSpace(source.Result.Arguments) == "" {
				c.fault(unit, FaultReference, "result.arguments is required when the activation command accepts input")
			}
			if !outputMatchesInput(source.Activation.Input, resolved.Input) {
				c.fault(unit, FaultReference, "activation.input must exactly match the activation command input")
			}
		}
	}
	for id, menu := range c.set.menus {
		unit := c.set.byID[id]
		if ref, ok := c.parseRef(unit, "command", menu.Command, func(ref ID) bool {
			_, exists := c.set.commands[ref]
			return exists
		}); !ok {
			continue
		} else {
			c.requireSameProvider(unit, "command", ref.Provider)
		}
		c.condition(unit, menu.When, scope)
		c.condition(unit, menu.State, scope)
	}
	for id, keybinding := range c.set.keybindings {
		unit := c.set.byID[id]
		ref, ok := c.parseRef(unit, "command", keybinding.Command, func(ref ID) bool {
			_, exists := c.set.commands[ref]
			return exists
		})
		if ok {
			c.requireSameProvider(unit, "command", ref.Provider)
		}
		c.condition(unit, keybinding.When, scope)
	}
	for id, action := range c.set.editorActions {
		unit := c.set.byID[id]
		c.promptRef(unit, action.Execution.PromptRef)
	}
	c.commandReachability()
}

func (c *compiler) commandReachability() {
	reachable := map[ID]struct{}{}
	for _, menu := range c.set.menus {
		ref, err := ParseID(menu.Command)
		if err != nil {
			continue
		}
		reachable[ref] = struct{}{}
	}
	for _, keybinding := range c.set.keybindings {
		ref, err := ParseID(keybinding.Command)
		if err != nil {
			continue
		}
		reachable[ref] = struct{}{}
	}
	ids := make([]ID, 0, len(c.set.commands))
	for id := range c.set.commands {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	for _, id := range ids {
		command := c.set.commands[id]
		if command.InPalette() || command.HostInvoked {
			continue
		}
		if _, ok := reachable[id]; ok {
			continue
		}
		unit := c.set.byID[id]
		c.fault(unit, FaultReference, fmt.Sprintf(
			"command %s is unreachable: not in the palette, has no menu, has no keybinding, and is not host_invoked", command.ID))
	}
}

func (c *compiler) commandGraph(unit Unit, command *Command, scope conditionScope) {
	switch command.Action.Kind {
	case ActionEditorAction:
		ref, ok := c.parseRef(unit, "action.ref", command.Action.Ref, func(ref ID) bool {
			_, exists := c.set.editorActions[ref]
			return exists
		})
		if ok {
			c.requireSameProvider(unit, "action.ref", ref.Provider)
		}
	case ActionWorkflowStart:
		workflowUnit := "workflows/" + strings.TrimSpace(command.Action.Workflow)
		c.requireOwnedUnit(unit, "action.workflow", workflowUnit)
	case ActionMCPTool:
		c.requirementToolRef(unit, "action.requirement", command.Action.Requirement, command.Action.Tool, scope)
	case ActionOperation:
		ref, ok := c.parseRef(unit, "action.ref", command.Action.Ref, func(ref ID) bool {
			_, exists := c.set.operations[ref]
			return exists
		})
		if ok {
			c.requirePackDependency(unit, ref.Provider)
			operation := c.set.operations[ref]
			commandSchema := command.Input
			if command.Interaction != nil {
				commandSchema = InteractionFields(command.Interaction)
			}
			if len(commandSchema) == 0 {
				command.Input = append([]InputField(nil), operation.Input...)
			} else if !sameInputSchema(commandSchema, operation.Input) {
				c.fault(unit, FaultReference, "command input must exactly match the referenced operation input")
			}
		}
	case ActionComposerPrefill, ActionNavigate, ActionExternalLink, ActionNativeUI:
	}
	c.condition(unit, command.When, scope)
	c.condition(unit, command.Enablement, scope)
	if command.Interaction != nil {
		for _, step := range command.Interaction.Steps {
			if step.Source != nil {
				c.requirementToolRef(unit, "interaction.source.requirement", step.Source.Requirement, step.Source.Tool, scope)
			}
		}
	}
}

func (c *compiler) requirementToolRef(unit Unit, field, raw, tool string, scope conditionScope) {
	ref, ok := c.parseRef(unit, field, raw, scope.RequirementExists)
	if !ok {
		return
	}
	c.requireSameProvider(unit, field, ref.Provider)
	requirement := c.set.requirements[ref]
	for _, admitted := range requirement.RequiredTools {
		if admitted == tool {
			return
		}
	}
	c.fault(unit, FaultReference, fmt.Sprintf("tool %q is not in requirement %s required_tools", tool, ref))
}

func (c *compiler) requireSameProvider(unit Unit, field, provider string) {
	if provider != unit.ProviderPackID {
		c.fault(unit, FaultAuthority, fmt.Sprintf("%s must belong to the declaring pack", field))
	}
}

func (c *compiler) requireOwnedUnit(unit Unit, field, unitID string) {
	if c.in.UnitProvider == nil {
		return
	}
	provider, ok := c.in.UnitProvider(unitID)
	if !ok {
		c.fault(unit, FaultReference, fmt.Sprintf("%s %q does not resolve in this catalog", field, unitID))
		return
	}
	c.requireSameProvider(unit, field, provider)
}

func (c *compiler) requirePackDependency(unit Unit, provider string) {
	if provider == unit.ProviderPackID {
		return
	}
	if c.in.PackDependsOn == nil || !c.in.PackDependsOn(unit.ProviderPackID, provider) {
		c.fault(unit, FaultReference, fmt.Sprintf("cross-pack reference to %s requires an explicit manifest dependency", provider))
	}
}

func (c *compiler) operationGraph() {
	state := map[ID]uint8{}
	var visit func(ID)
	visit = func(id ID) {
		if state[id] == 2 {
			return
		}
		unit := c.set.byID[id]
		if state[id] == 1 {
			c.fault(unit, FaultReference, fmt.Sprintf("operation graph contains a cycle at %s", id))
			return
		}
		state[id] = 1
		operation := c.set.operations[id]
		switch operation.Action.Kind {
		case ActionOperation:
			ref, ok := c.parseRef(unit, "action.ref", operation.Action.Ref, func(ref ID) bool {
				_, exists := c.set.operations[ref]
				return exists
			})
			if ok {
				c.requirePackDependency(unit, ref.Provider)
				if !sameInputSchema(operation.Input, c.set.operations[ref].Input) {
					c.fault(unit, FaultReference, "operation input must exactly match the referenced operation input")
				}
				if !sameOutputSchema(operation.Output, c.set.operations[ref].Output) {
					c.fault(unit, FaultReference, "operation output must exactly match the referenced operation output")
				}
				visit(ref)
			}
		case ActionMCPTool:
			scope := conditionScope{RequirementExists: func(ref ID) bool { _, ok := c.set.requirements[ref]; return ok }}
			c.requirementToolRef(unit, "action.requirement", operation.Action.Requirement, operation.Action.Tool, scope)
		case ActionEditorAction:
			ref, ok := c.parseRef(unit, "action.ref", operation.Action.Ref, func(ref ID) bool {
				_, exists := c.set.editorActions[ref]
				return exists
			})
			if ok {
				c.requireSameProvider(unit, "action.ref", ref.Provider)
			}
		case ActionWorkflowStart:
			workflowUnit := "workflows/" + strings.TrimSpace(operation.Action.Workflow)
			c.requireOwnedUnit(unit, "action.workflow", workflowUnit)
		case ActionComposerPrefill, ActionNavigate, ActionExternalLink, ActionNativeUI:
		}
		state[id] = 2
	}
	ids := make([]ID, 0, len(c.set.operations))
	for id := range c.set.operations {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	for _, id := range ids {
		visit(id)
	}
}

// parseRef resolves one full-ID reference against an existence predicate.
func (c *compiler) parseRef(unit Unit, field, raw string, exists func(ID) bool) (ID, bool) {
	ref, err := ParseID(raw)
	if err != nil {
		c.fault(unit, FaultReference, fmt.Sprintf("%s: %v", field, err))
		return ID{}, false
	}
	if !exists(ref) {
		c.fault(unit, FaultReference, fmt.Sprintf("%s %s does not resolve in this catalog", field, raw))
		return ID{}, false
	}
	return ref, true
}

func (c *compiler) promptRef(unit Unit, promptRef string) {
	promptRef = strings.TrimSpace(promptRef)
	if !strings.HasPrefix(promptRef, "guidance/") && !strings.HasPrefix(promptRef, "shared/") {
		c.fault(unit, FaultReference,
			fmt.Sprintf("execution.prompt_ref %q must name a guidance/ or shared/ unit", promptRef))
		return
	}
	c.requireOwnedUnit(unit, "execution.prompt_ref", promptRef)
}

func (c *compiler) condition(unit Unit, condition *Condition, scope conditionScope) {
	if condition == nil {
		return
	}
	scope.Provider = unit.ProviderPackID
	if err := validateCondition(*condition, scope); err != nil {
		c.fault(unit, FaultSchema, err.Error())
	}
}

// resolveBindingDefaults resolves collisions by platform, scope, and chord.
func (c *compiler) resolveBindingDefaults() {
	type slot struct{ platform, scope, chord string }
	candidates := map[slot][]ID{}
	// The published chord retains its canonical spelling.
	published := map[slot]string{}
	for id, keybinding := range c.set.keybindings {
		for platform, bindings := range keybinding.Bindings {
			for _, binding := range bindings {
				key := slot{platform, keybinding.Scope, chordIdentity(binding)}
				candidates[key] = append(candidates[key], id)
				published[key] = canonicalChord(binding)
			}
		}
	}
	slots := make([]slot, 0, len(candidates))
	for key := range candidates {
		slots = append(slots, key)
	}
	sort.Slice(slots, func(i, j int) bool {
		a, b := slots[i], slots[j]
		if a.platform != b.platform {
			return a.platform < b.platform
		}
		if a.scope != b.scope {
			return a.scope < b.scope
		}
		return a.chord < b.chord
	})
	for _, key := range slots {
		ids := candidates[key]
		sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
		best := RankDevice + 1
		for _, id := range ids {
			if rank := c.rank(id.Provider); rank < best {
				best = rank
			}
		}
		var winners []ID
		for _, id := range ids {
			if c.rank(id.Provider) == best {
				winners = append(winners, id)
			}
		}
		defaultRow := BindingDefault{
			Platform: key.platform, Scope: key.scope, Chord: published[key],
			Candidates: ids,
		}
		if len(winners) == 1 {
			winner := winners[0]
			defaultRow.Active = &winner
		}
		// A tie among bundled declarations would ship dead chords.
		if len(winners) > 1 && best == RankStock {
			for _, id := range winners {
				c.fault(c.set.byID[id], FaultReference, fmt.Sprintf(
					"%s %s in scope %s collides with another stock keybinding (candidates %s)",
					key.platform, key.chord, key.scope, joinIDs(winners)))
			}
			continue
		}
		if len(ids) > 1 {
			state := "deactivated"
			if defaultRow.Active != nil {
				state = "won by " + defaultRow.Active.String()
			}
			c.set.notes = append(c.set.notes, Note{
				Code: NoteBindingConflict,
				Message: fmt.Sprintf("%s %s in scope %s: %s (candidates %s)",
					key.platform, key.chord, key.scope, state, joinIDs(ids)),
			})
		}
		c.set.bindingDefaults = append(c.set.bindingDefaults, defaultRow)
	}
}

func joinIDs(ids []ID) string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return strings.Join(out, ", ")
}

// configurationValues validates effective values for present packs.
func (c *compiler) configurationValues() {
	byProvider := map[string]map[string]*ConfigurationProperty{}
	unitByProvider := map[string]map[string]Unit{}
	for id, property := range c.set.configuration {
		if byProvider[id.Provider] == nil {
			byProvider[id.Provider] = map[string]*ConfigurationProperty{}
			unitByProvider[id.Provider] = map[string]Unit{}
		}
		byProvider[id.Provider][id.Name] = property
		unitByProvider[id.Provider][id.Name] = c.set.byID[id]
	}
	packs := make([]string, 0, len(c.in.Configuration))
	for packID := range c.in.Configuration {
		packs = append(packs, packID)
	}
	sort.Strings(packs)
	for _, packID := range packs {
		if c.in.PackPresent != nil && !c.in.PackPresent(packID) {
			continue
		}
		properties := byProvider[packID]
		names := make([]string, 0, len(c.in.Configuration[packID]))
		for name := range c.in.Configuration[packID] {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			property, known := properties[name]
			if !known {
				c.faults = append(c.faults, Fault{
					PackID: packID, Code: FaultValue,
					Message: fmt.Sprintf("configuration %s.%s does not match any declared property", packID, name),
				})
				continue
			}
			value := c.in.Configuration[packID][name]
			if err := checkPropertyValue(
				fmt.Sprintf("configuration %s.%s", packID, name),
				property.Type, property.Enum, property.Min, property.Max, value,
			); err != nil {
				unit := unitByProvider[packID][name]
				c.fault(unit, FaultValue, err.Error())
				continue
			}
			c.set.settings[ID{Provider: packID, Name: name}] = ConfigurationSetting{
				Property: property, Value: value,
			}
		}
	}
	// Every property receives an effective value.
	for id, property := range c.set.configuration {
		if _, set := c.set.settings[id]; set {
			continue
		}
		c.set.settings[id] = ConfigurationSetting{
			Property: property, Value: property.Default, Default: true,
		}
	}
}

// Typed accessors. Every slice is sorted by full id for determinism.

func (s *Set) Commands() []*Command { return sortedDecls(s, s.commands) }
func (s *Set) Menus() []*Menu       { return sortedDecls(s, s.menus) }
func (s *Set) Keybindings() []*Keybinding {
	return sortedDecls(s, s.keybindings)
}
func (s *Set) EditorActions() []*EditorAction {
	return sortedDecls(s, s.editorActions)
}
func (s *Set) MCPRequirements() []*MCPRequirement {
	return sortedDecls(s, s.requirements)
}

func (s *Set) SearchSources() []*SearchSource { return sortedDecls(s, s.searchSources) }
func (s *Set) Operations() []*Operation       { return sortedDecls(s, s.operations) }

// Themes returns the compiled theme declarations, sorted by id.
func (s *Set) Themes() []*Theme {
	return sortedDecls(s, s.themes)
}

// Command returns one compiled command.
func (s *Set) Command(id ID) (*Command, bool) {
	if s == nil {
		return nil, false
	}
	decl, ok := s.commands[id]
	return decl, ok
}

// EditorAction returns one compiled editor action.
func (s *Set) EditorAction(id ID) (*EditorAction, bool) {
	if s == nil {
		return nil, false
	}
	decl, ok := s.editorActions[id]
	return decl, ok
}

// MCPRequirement returns one compiled requirement.
func (s *Set) MCPRequirement(id ID) (*MCPRequirement, bool) {
	if s == nil {
		return nil, false
	}
	decl, ok := s.requirements[id]
	return decl, ok
}

func (s *Set) SearchSource(id ID) (*SearchSource, bool) {
	if s == nil {
		return nil, false
	}
	decl, ok := s.searchSources[id]
	return decl, ok
}

func (s *Set) Operation(id ID) (*Operation, bool) {
	if s == nil {
		return nil, false
	}
	decl, ok := s.operations[id]
	return decl, ok
}

// BindingDefaults returns the resolved default chords.
func (s *Set) BindingDefaults() []BindingDefault {
	if s == nil {
		return nil
	}
	return append([]BindingDefault(nil), s.bindingDefaults...)
}

// Notes returns non-fatal compile observations.
func (s *Set) Notes() []Note {
	if s == nil {
		return nil
	}
	return append([]Note(nil), s.notes...)
}

func sortedDecls[T any](s *Set, m map[ID]T) []T {
	if s == nil {
		return nil
	}
	ids := make([]ID, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	out := make([]T, 0, len(ids))
	for _, id := range ids {
		out = append(out, m[id])
	}
	return out
}
