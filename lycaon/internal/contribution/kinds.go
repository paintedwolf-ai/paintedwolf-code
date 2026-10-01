package contribution

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/theme"
	"gopkg.in/yaml.v3"
)

// Command pairs immutable presentation metadata with exactly one typed action.
type Command struct {
	ID         string          `yaml:"id" json:"id"`
	Title      string          `yaml:"title" json:"title"`
	Category   string          `yaml:"category,omitempty" json:"category,omitempty"`
	Scope      string          `yaml:"scope,omitempty" json:"scope,omitempty"`
	Keywords   []string        `yaml:"keywords,omitempty" json:"keywords,omitempty"`
	Icon       string          `yaml:"icon,omitempty" json:"icon,omitempty"`
	Palette    *bool           `yaml:"palette,omitempty" json:"palette,omitempty"`
	When       *Condition      `yaml:"when,omitempty" json:"when,omitempty"`
	Enablement *Condition      `yaml:"enablement,omitempty" json:"enablement,omitempty"`
	Action     Action          `yaml:"action" json:"action"`
	Result     ResultTreatment `yaml:"result,omitempty" json:"result,omitempty"`
	// HostInvoked permits stock panel activation.
	HostInvoked bool         `yaml:"host_invoked,omitempty"`
	Input       []InputField `yaml:"input,omitempty"`
	Interaction *Interaction `yaml:"interaction,omitempty"`
}

// InPalette applies the default-on palette rule.
func (c Command) InPalette() bool { return c.Palette == nil || *c.Palette }

// Action is the closed union; exactly the fields its kind declares are legal.
type Action struct {
	Kind        ActionKind `yaml:"kind" json:"kind"`
	Ref         string     `yaml:"ref,omitempty" json:"ref,omitempty"`
	Workflow    string     `yaml:"workflow,omitempty" json:"workflow,omitempty"`
	Requirement string     `yaml:"requirement,omitempty" json:"requirement,omitempty"`
	Tool        string     `yaml:"tool,omitempty" json:"tool,omitempty"`
	Text        string     `yaml:"text,omitempty" json:"text,omitempty"`
	Destination string     `yaml:"destination,omitempty" json:"destination,omitempty"`
	URL         string     `yaml:"url,omitempty" json:"url,omitempty"`
	Handler     string     `yaml:"handler,omitempty" json:"handler,omitempty"`
}

// InputField types one human argument for a host-backed action.
type InputField struct {
	ID          string       `yaml:"id" json:"id"`
	Title       string       `yaml:"title" json:"title,omitempty"`
	Type        PropertyType `yaml:"type" json:"type"`
	Description string       `yaml:"description,omitempty" json:"description,omitempty"`
	Required    bool         `yaml:"required,omitempty" json:"required,omitempty"`
	Values      []string     `yaml:"values,omitempty" json:"values,omitempty"`
	Default     any          `yaml:"default,omitempty" json:"default,omitempty"`
	Min         *float64     `yaml:"min,omitempty" json:"min,omitempty"`
	Max         *float64     `yaml:"max,omitempty" json:"max,omitempty"`
}

// Interaction is a bounded acyclic sequence of host-rendered questions.
type Interaction struct {
	Steps []InteractionStep `yaml:"steps" json:"steps"`
}

// InteractionStep is one typed question with prior-answer conditions.
type InteractionStep struct {
	ID          string                `yaml:"id" json:"id"`
	Title       string                `yaml:"title" json:"title"`
	Description string                `yaml:"description,omitempty" json:"description,omitempty"`
	Kind        InteractionKind       `yaml:"kind" json:"kind"`
	Required    bool                  `yaml:"required,omitempty" json:"required,omitempty"`
	Values      []Choice              `yaml:"values,omitempty" json:"values,omitempty"`
	Min         *float64              `yaml:"min,omitempty" json:"min,omitempty"`
	Max         *float64              `yaml:"max,omitempty" json:"max,omitempty"`
	Multiple    bool                  `yaml:"multiple,omitempty" json:"multiple,omitempty"`
	Source      *DynamicChoiceSource  `yaml:"source,omitempty" json:"source,omitempty"`
	If          *InteractionPredicate `yaml:"if,omitempty" json:"if,omitempty"`
}

// Choice is the common bounded row shape for static and dynamic pickers.
type Choice struct {
	ID          string `yaml:"id" json:"id"`
	Label       string `yaml:"label" json:"label"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Detail      string `yaml:"detail,omitempty" json:"detail,omitempty"`
}

// DynamicChoiceSource maps prior answers into an admitted MCP tool.
type DynamicChoiceSource struct {
	Kind        ChoiceSourceKind  `yaml:"kind" json:"kind"`
	Requirement string            `yaml:"requirement" json:"requirement"`
	Tool        string            `yaml:"tool" json:"tool"`
	Inputs      map[string]string `yaml:"inputs,omitempty" json:"inputs,omitempty"`
}

// InteractionPredicate includes a later step based on an earlier answer.
type InteractionPredicate struct {
	Step string `yaml:"step" json:"step"`
	Is   any    `yaml:"is" json:"is"`
}

// SearchSource is one explicitly activated provider-backed result lane.
type SearchSource struct {
	ID          string                 `yaml:"id"`
	Label       string                 `yaml:"label"`
	Prefix      string                 `yaml:"prefix"`
	Requirement string                 `yaml:"requirement"`
	Tool        string                 `yaml:"tool"`
	Query       SearchQuery            `yaml:"query"`
	Result      SearchResultProjection `yaml:"result"`
	Activation  SearchActivation       `yaml:"activation"`
}

type SearchQuery struct {
	MinLength  int `yaml:"min_length,omitempty"`
	MaxResults int `yaml:"max_results,omitempty"`
}

// SearchResultProjection names keys in provider JSON result objects.
type SearchResultProjection struct {
	ID          string `yaml:"id"`
	Title       string `yaml:"title"`
	Description string `yaml:"description,omitempty"`
	Detail      string `yaml:"detail,omitempty"`
	Kind        string `yaml:"kind,omitempty"`
	Arguments   string `yaml:"arguments,omitempty"`
}

type SearchActivation struct {
	Command string        `yaml:"command"`
	Input   []OutputField `yaml:"input,omitempty"`
}

// Operation is a presentation-free, typed, composable action.
type Operation struct {
	ID     string          `yaml:"id"`
	Input  []InputField    `yaml:"input,omitempty"`
	Output []OutputField   `yaml:"output,omitempty"`
	Action Action          `yaml:"action"`
	Result ResultTreatment `yaml:"result,omitempty"`
}

type OutputField struct {
	ID       string       `yaml:"id" json:"id"`
	Type     PropertyType `yaml:"type" json:"type"`
	Required bool         `yaml:"required,omitempty" json:"required,omitempty"`
	Values   []string     `yaml:"values,omitempty" json:"values,omitempty"`
	Min      *float64     `yaml:"min,omitempty" json:"min,omitempty"`
	Max      *float64     `yaml:"max,omitempty" json:"max,omitempty"`
}

// Menu places a command in a closed slot.
type Menu struct {
	ID      string     `yaml:"id" json:"id"`
	Slot    MenuSlot   `yaml:"slot" json:"slot"`
	Command string     `yaml:"command" json:"command"`
	Group   string     `yaml:"group,omitempty" json:"group,omitempty"`
	Order   int        `yaml:"order,omitempty" json:"order,omitempty"`
	Label   string     `yaml:"label,omitempty" json:"label,omitempty"`
	When    *Condition `yaml:"when,omitempty" json:"when,omitempty"`
	// State is the condition a toggle reflects: StateLabel replaces Label
	// while it holds, and without StateLabel the item carries a checkmark.
	State      *Condition `yaml:"state,omitempty" json:"state,omitempty"`
	StateLabel string     `yaml:"state_label,omitempty" json:"state_label,omitempty"`
}

// Keybinding declares default chords for a command per platform and scope.
type Keybinding struct {
	ID           string              `yaml:"id" json:"id"`
	Command      string              `yaml:"command" json:"command"`
	Scope        string              `yaml:"scope" json:"scope"`
	AllowInInput bool                `yaml:"allow_in_input,omitempty" json:"allow_in_input,omitempty"`
	Bindings     map[string][]string `yaml:"bindings" json:"bindings"`
	When         *Condition          `yaml:"when,omitempty" json:"when,omitempty"`
}

// EditorAction is a prompt-driven action inside a host preset boundary.
type EditorAction struct {
	ID        string          `yaml:"id"`
	Title     string          `yaml:"title"`
	Target    EditorTarget    `yaml:"target"`
	Execution EditorExecution `yaml:"execution"`
}

type EditorTarget struct {
	Kind     TargetKind `yaml:"kind"`
	Required bool       `yaml:"required,omitempty"`
}

type EditorExecution struct {
	Preset    PresetID `yaml:"preset"`
	PromptRef string   `yaml:"prompt_ref"`
}

// ConfigurationProperty is one namespaced non-secret setting.
type ConfigurationProperty struct {
	ID          string       `yaml:"id"`
	Type        PropertyType `yaml:"type"`
	Description string       `yaml:"description"`
	Default     any          `yaml:"default"`
	// Device scope is implicit.
	Scope []string `yaml:"scope,omitempty"`
	Enum  []string `yaml:"enum,omitempty"`
	Min   *float64 `yaml:"min,omitempty"`
	Max   *float64 `yaml:"max,omitempty"`
}

// MCPRequirement declares a required provider tool set.
type MCPRequirement struct {
	ID            string   `yaml:"id"`
	ProviderID    string   `yaml:"provider_id"`
	RequiredTools []string `yaml:"required_tools"`
	Purpose       string   `yaml:"purpose"`
}

// decodeStrict rejects unknown fields and multiple documents.
func decodeStrict(body []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple YAML documents are not allowed")
		}
		return err
	}
	return nil
}

// authorityError marks a declaration that only a stock provider may make.
type authorityError struct{ msg string }

func (e authorityError) Error() string { return e.msg }

func validateCommand(c *Command, stock bool) error {
	if strings.TrimSpace(c.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if strings.TrimSpace(c.Category) == "" && c.Category != "" {
		return fmt.Errorf("category must be non-empty when set")
	}
	if c.Scope != "" && !commandScopes[c.Scope] {
		return fmt.Errorf("unknown scope %q", c.Scope)
	}
	if c.Icon != "" {
		if _, ok := theme.IconSlotByID(c.Icon); !ok {
			return fmt.Errorf("unknown icon %q", c.Icon)
		}
	}
	for _, keyword := range c.Keywords {
		if strings.TrimSpace(keyword) == "" {
			return fmt.Errorf("keywords must be non-empty")
		}
	}
	if err := validateAction(c.Action, stock); err != nil {
		return err
	}
	if err := validateResult(c.Action.Kind, c.Result); err != nil {
		return err
	}
	if c.HostInvoked && !stock {
		return authorityError{"host_invoked is stock-only"}
	}
	if len(c.Input) > 0 && c.Interaction != nil {
		return fmt.Errorf("input and interaction are mutually exclusive")
	}
	if len(c.Input) > 0 && c.Action.Kind != ActionMCPTool && c.Action.Kind != ActionEditorAction && c.Action.Kind != ActionOperation {
		return fmt.Errorf("action %s does not consume input", c.Action.Kind)
	}
	if err := validateInputFields("command input", c.Input, true); err != nil {
		return err
	}
	if c.Interaction != nil && c.Action.Kind != ActionMCPTool && c.Action.Kind != ActionEditorAction && c.Action.Kind != ActionOperation {
		return fmt.Errorf("action %s does not consume an interaction", c.Action.Kind)
	}
	if err := validateInteraction(c.Interaction); err != nil {
		return err
	}
	if c.Action.Kind == ActionEditorAction {
		fields := c.Input
		if c.Interaction != nil {
			fields = InteractionFields(c.Interaction)
		}
		for _, field := range fields {
			if field.ID != "instruction" || field.Type != PropertyString {
				return fmt.Errorf("editor_action input supports only the string field instruction")
			}
		}
	}
	return nil
}

func validateAction(a Action, stock bool) error {
	if !actionKinds[a.Kind] {
		return fmt.Errorf("unknown action kind %q", a.Kind)
	}
	if nativeActionKinds[a.Kind] && !stock {
		return authorityError{fmt.Sprintf("action %s is stock-only", a.Kind)}
	}
	type fieldRule struct {
		name  string
		value string
		want  bool
	}
	need := func(kind ActionKind, field string) bool { return a.Kind == kind }
	rules := []fieldRule{
		{"ref", a.Ref, a.Kind == ActionEditorAction || a.Kind == ActionOperation},
		{"workflow", a.Workflow, need(ActionWorkflowStart, "workflow")},
		{"requirement", a.Requirement, need(ActionMCPTool, "requirement")},
		{"tool", a.Tool, need(ActionMCPTool, "tool")},
		{"text", a.Text, need(ActionComposerPrefill, "text")},
		{"destination", a.Destination, need(ActionNavigate, "destination")},
		{"url", a.URL, need(ActionExternalLink, "url")},
		{"handler", a.Handler, a.Kind == ActionNativeUI},
	}
	for _, rule := range rules {
		has := strings.TrimSpace(rule.value) != ""
		if rule.want && !has {
			return fmt.Errorf("action %s requires %s", a.Kind, rule.name)
		}
		if !rule.want && has {
			return fmt.Errorf("action %s does not take %s", a.Kind, rule.name)
		}
	}
	if a.Kind == ActionNavigate {
		if err := validateDestination(a.Destination); err != nil {
			return err
		}
	}
	if a.Kind == ActionExternalLink {
		parsed, err := url.Parse(a.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return fmt.Errorf("external_link url must be absolute https")
		}
	}
	return nil
}

func validateDestination(destination string) error {
	if navigateFixedDestinations[destination] {
		return nil
	}
	if feature, ok := strings.CutPrefix(destination, "context."); ok {
		if contextFeatures[feature] {
			return nil
		}
		return fmt.Errorf("unknown context destination %q", destination)
	}
	if section, ok := strings.CutPrefix(destination, "settings."); ok {
		if settingsSections[section] {
			return nil
		}
		return fmt.Errorf("unknown settings destination %q", destination)
	}
	return fmt.Errorf("unknown destination %q", destination)
}

func validateMenu(m *Menu) error {
	if !menuSlots[m.Slot] {
		return fmt.Errorf("unknown slot %q", m.Slot)
	}
	if strings.TrimSpace(m.Command) == "" {
		return fmt.Errorf("command is required")
	}
	if m.Order < 0 {
		return fmt.Errorf("order must be non-negative")
	}
	if m.StateLabel != "" && m.State == nil {
		return fmt.Errorf("state_label requires state")
	}
	return nil
}

func validateKeybinding(k *Keybinding, stock bool) error {
	if strings.TrimSpace(k.Command) == "" {
		return fmt.Errorf("command is required")
	}
	if !keybindingScopes[k.Scope] {
		return fmt.Errorf("unknown scope %q", k.Scope)
	}
	if len(k.Bindings) == 0 {
		return fmt.Errorf("bindings is required")
	}
	for platform, bindings := range k.Bindings {
		if !bindingPlatforms[platform] {
			return fmt.Errorf("unknown platform %q", platform)
		}
		if len(bindings) == 0 {
			return fmt.Errorf("platform %s lists no bindings", platform)
		}
		for _, binding := range bindings {
			if err := validateBinding(binding); err != nil {
				return err
			}
			if bindingAssistiveReserved(binding) {
				return authorityError{fmt.Sprintf("chord %q is reserved for assistive technology", binding)}
			}
			if bindingOSIntercepted(platform, binding) {
				return authorityError{fmt.Sprintf("chord %q is reserved by %s, which consumes it before the app sees it", binding, platform)}
			}
			if !stock && bindingSystemReserved(platform, binding) {
				return authorityError{fmt.Sprintf("chord %q is reserved and cannot be claimed by extension defaults", binding)}
			}
			if !bindingProducible(platform, binding) {
				return fmt.Errorf("chord %q cannot be produced on %s", binding, platform)
			}
		}
	}
	return nil
}

func validateEditorAction(a *EditorAction) error {
	if strings.TrimSpace(a.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if !targetKinds[a.Target.Kind] {
		return fmt.Errorf("unknown target kind %q", a.Target.Kind)
	}
	if !presetIDs[a.Execution.Preset] {
		return fmt.Errorf("unknown preset %q", a.Execution.Preset)
	}
	if strings.TrimSpace(a.Execution.PromptRef) == "" {
		return fmt.Errorf("execution.prompt_ref is required")
	}
	// fix_finding requires a recorded finding.
	if (a.Execution.Preset == PresetFixFinding) != (a.Target.Kind == TargetFinding) {
		return fmt.Errorf("preset %s and target %s do not combine", a.Execution.Preset, a.Target.Kind)
	}
	return nil
}

func validateConfigurationProperty(p *ConfigurationProperty) error {
	if !propertyTypes[p.Type] {
		return fmt.Errorf("unknown type %q", p.Type)
	}
	if strings.TrimSpace(p.Description) == "" {
		return fmt.Errorf("description is required")
	}
	for _, scope := range p.Scope {
		if !propertyScopes[scope] {
			return fmt.Errorf("unknown scope %q", scope)
		}
	}
	if err := validateEnumAndBounds("property", p.Type, p.Enum, p.Min, p.Max); err != nil {
		return err
	}
	if p.Default == nil {
		return fmt.Errorf("default is required")
	}
	return checkPropertyValue("default", p.Type, p.Enum, p.Min, p.Max, p.Default)
}

func validateEnumAndBounds(label string, t PropertyType, enum []string, min, max *float64) error {
	if t == PropertyEnum && len(enum) == 0 {
		return fmt.Errorf("%s: enum values are required", label)
	}
	if t != PropertyEnum && len(enum) > 0 {
		return fmt.Errorf("%s: enum values only apply to enum", label)
	}
	if t != PropertyNumber && (min != nil || max != nil) {
		return fmt.Errorf("%s: bounds only apply to number", label)
	}
	if min != nil && max != nil && *min > *max {
		return fmt.Errorf("%s: min exceeds max", label)
	}
	return nil
}

// checkPropertyValue types one value against a property declaration.
func checkPropertyValue(label string, t PropertyType, enum []string, min, max *float64, value any) error {
	switch t {
	case PropertyBoolean:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", label)
		}
	case PropertyString, PropertyProjectPath:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", label)
		}
	case PropertyEnum:
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", label)
		}
		for _, member := range enum {
			if member == s {
				return nil
			}
		}
		return fmt.Errorf("%s must be one of the enum values", label)
	case PropertyNumber:
		n, ok := asNumber(value)
		if !ok {
			return fmt.Errorf("%s must be a number", label)
		}
		if min != nil && n < *min {
			return fmt.Errorf("%s is below min", label)
		}
		if max != nil && n > *max {
			return fmt.Errorf("%s is above max", label)
		}
	case PropertyStringList:
		switch v := value.(type) {
		case []string:
		case []any:
			for _, member := range v {
				if _, ok := member.(string); !ok {
					return fmt.Errorf("%s members must be strings", label)
				}
			}
		default:
			return fmt.Errorf("%s must be a string list", label)
		}
	}
	return nil
}

func asNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint64:
		return float64(v), true
	case float64:
		return v, true
	default:
		return 0, false
	}
}

func validateMCPRequirement(r *MCPRequirement) error {
	if strings.TrimSpace(r.ProviderID) == "" {
		return fmt.Errorf("provider_id is required")
	}
	// This is an exact device-catalog identity, not a contribution ID or tool name.
	// MCP settings permit provider names containing spaces and punctuation.
	if len(r.RequiredTools) == 0 {
		return fmt.Errorf("required_tools is required")
	}
	seen := map[string]bool{}
	for _, tool := range r.RequiredTools {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			return fmt.Errorf("required_tools entries must be non-empty")
		}
		if seen[tool] {
			return fmt.Errorf("required_tools lists %q twice", tool)
		}
		seen[tool] = true
	}
	if strings.TrimSpace(r.Purpose) == "" {
		return fmt.Errorf("purpose is required")
	}
	return nil
}
