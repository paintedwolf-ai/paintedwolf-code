package contribution

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/filekind"
)

// ActionKind is the closed command action union.
type ActionKind string

const (
	ActionEditorAction    ActionKind = "editor_action"
	ActionWorkflowStart   ActionKind = "workflow_start"
	ActionMCPTool         ActionKind = "mcp_tool"
	ActionComposerPrefill ActionKind = "composer_prefill"
	ActionNavigate        ActionKind = "navigate"
	ActionExternalLink    ActionKind = "external_link"
	ActionNativeUI        ActionKind = "native_ui"
	ActionOperation       ActionKind = "operation"
)

var actionKinds = map[ActionKind]bool{
	ActionEditorAction: true, ActionWorkflowStart: true, ActionMCPTool: true,
	ActionComposerPrefill: true, ActionNavigate: true, ActionExternalLink: true,
	ActionNativeUI: true, ActionOperation: true,
}

// ResultTreatment is the closed sink for a successful command result.
type ResultTreatment string

const (
	ResultOutput  ResultTreatment = "output"
	ResultDiscard ResultTreatment = "discard"
	ResultEffect  ResultTreatment = "effect"
	ResultReceipt ResultTreatment = "receipt"
)

// InteractionKind is the closed host-rendered step vocabulary.
type InteractionKind string

const (
	InteractionString       InteractionKind = "string"
	InteractionNumber       InteractionKind = "number"
	InteractionBoolean      InteractionKind = "boolean"
	InteractionChoice       InteractionKind = "choice"
	InteractionProjectPath  InteractionKind = "project_path"
	InteractionConfirmation InteractionKind = "confirmation"
)

var interactionKinds = map[InteractionKind]bool{
	InteractionString: true, InteractionNumber: true, InteractionBoolean: true,
	InteractionChoice: true, InteractionProjectPath: true, InteractionConfirmation: true,
}

type ChoiceSourceKind string

const ChoiceSourceMCP ChoiceSourceKind = "mcp"

var searchResultKinds = map[string]bool{
	"": true, "item": true, "code": true, "issue": true, "document": true, "artifact": true,
}

// Native actions are stock-only.
var nativeActionKinds = map[ActionKind]bool{
	ActionNativeUI: true,
}

// ProviderPolicy is the closed per-action-kind admissibility rule.
type ProviderPolicy string

const (
	// PolicyTrustedPack admits any effective pack.
	PolicyTrustedPack ProviderPolicy = "trusted_pack"
	// PolicyStockOnly requires the stock trust root.
	PolicyStockOnly ProviderPolicy = "stock_only"
)

// ProviderPolicyFor returns the sole admissibility policy for an action kind.
func ProviderPolicyFor(kind ActionKind) ProviderPolicy {
	if nativeActionKinds[kind] {
		return PolicyStockOnly
	}
	return PolicyTrustedPack
}

// Executor identifies the runtime for an action kind.
type Executor string

const (
	ExecutorDen  Executor = "den"
	ExecutorHost Executor = "host"
)

// ExecutorFor returns the sole executor for an action kind.
func ExecutorFor(kind ActionKind) Executor {
	if kind == ActionNativeUI {
		return ExecutorDen
	}
	return ExecutorHost
}

// InvocationScope identifies an action's invoke route.
type InvocationScope string

const (
	InvocationDen     InvocationScope = "den"
	InvocationProject InvocationScope = "project"
	InvocationSession InvocationScope = "session"
)

// InvocationScopeFor returns the fixed invocation scope for an action kind.
func InvocationScopeFor(kind ActionKind) InvocationScope {
	switch kind {
	case ActionNativeUI:
		return InvocationDen
	case ActionComposerPrefill, ActionNavigate, ActionExternalLink:
		return InvocationProject
	default:
		return InvocationSession
	}
}

// Command scopes match keybinding dispatch strata.
var commandScopes = map[string]bool{
	"global": true, "files": true, "composer": true, "overlay": true,
}

// MenuSlot is the closed menu and context-menu placement vocabulary.
type MenuSlot string

var menuSlots = map[MenuSlot]bool{
	"editor.context.analysis": true,
	"editor.context.edit":     true,
	"editor.toolbar":          true,
	"file.context":            true,
	"project.context":         true,
	"session.context":         true,
	"composer.actions":        true,
	// Application menu slots.
	"app_menu.app":       true,
	"app_menu.file":      true,
	"app_menu.edit":      true,
	"app_menu.selection": true,
	"app_menu.view":      true,
	"app_menu.go":        true,
	"app_menu.window":    true,
	"app_menu.help":      true,
}

// Keybinding scopes follow descending dispatch precedence.
var keybindingScopes = map[string]bool{
	"global": true, "files": true, "composer": true, "overlay": true,
}

var bindingPlatforms = map[string]bool{
	"macos": true, "windows": true, "linux": true,
}

// TargetKind is the closed editor-action target vocabulary.
type TargetKind string

const (
	TargetCaret       TargetKind = "caret"
	TargetFile        TargetKind = "file"
	TargetSelection   TargetKind = "selection"
	TargetSymbol      TargetKind = "symbol"
	TargetFinding     TargetKind = "finding"
	TargetInstruction TargetKind = "instruction"
)

var targetKinds = map[TargetKind]bool{
	TargetCaret: true, TargetFile: true, TargetSelection: true,
	TargetSymbol: true, TargetFinding: true, TargetInstruction: true,
}

// PresetID names a host-managed execution boundary.
type PresetID string

const (
	PresetInspectFile PresetID = "inspect_file"
	PresetEditFile    PresetID = "edit_file"
	PresetEditSibling PresetID = "edit_sibling"
	PresetFixFinding  PresetID = "fix_finding"
)

var presetIDs = map[PresetID]bool{
	PresetInspectFile: true, PresetEditFile: true, PresetEditSibling: true, PresetFixFinding: true,
}

// PresetIDs returns the closed preset vocabulary, sorted.
func PresetIDs() []PresetID {
	out := make([]PresetID, 0, len(presetIDs))
	for id := range presetIDs {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// PropertyType is the closed configuration value universe.
type PropertyType string

const (
	PropertyBoolean     PropertyType = "boolean"
	PropertyString      PropertyType = "string"
	PropertyNumber      PropertyType = "number"
	PropertyEnum        PropertyType = "enum"
	PropertyStringList  PropertyType = "string_list"
	PropertyProjectPath PropertyType = "project_path"
)

var propertyTypes = map[PropertyType]bool{
	PropertyBoolean: true, PropertyString: true, PropertyNumber: true,
	PropertyEnum: true, PropertyStringList: true, PropertyProjectPath: true,
}

var propertyScopes = map[string]bool{"device": true, "project": true}

// FactPlane separates host and shell observables.
type FactPlane string

const (
	PlaneHost  FactPlane = "host"
	PlaneShell FactPlane = "shell"
)

// OperandKind types a fact's comparison operand.
type OperandKind string

const (
	OperandNone          OperandKind = ""
	OperandLanguage      OperandKind = "language"
	OperandRequirement   OperandKind = "requirement"
	OperandConfiguration OperandKind = "configuration"
	OperandShellView     OperandKind = "shell_view"
	OperandRegion        OperandKind = "region"
	OperandWorkspace     OperandKind = "workspace_kind"
	OperandFeature       OperandKind = "context_feature"
)

// FactSpec is one entry of the closed condition vocabulary.
type FactSpec struct {
	Plane   FactPlane
	Operand OperandKind
}

// facts is the complete condition vocabulary.
var facts = map[string]FactSpec{
	"project_open":          {Plane: PlaneHost},
	"session_exists":        {Plane: PlaneHost},
	"session_idle":          {Plane: PlaneHost},
	"activity_live":         {Plane: PlaneHost},
	"editor_active":         {Plane: PlaneHost},
	"editor_editable":       {Plane: PlaneHost},
	"editor_has_selection":  {Plane: PlaneHost},
	"editor_has_symbol":     {Plane: PlaneHost},
	"editor_has_finding":    {Plane: PlaneHost},
	"editor_language":       {Plane: PlaneHost, Operand: OperandLanguage},
	"mcp_requirement_ready": {Plane: PlaneHost, Operand: OperandRequirement},
	// Configuration facts read the effective catalog.
	"configuration_on": {Plane: PlaneHost, Operand: OperandConfiguration},
	"active_view":      {Plane: PlaneShell, Operand: OperandShellView},
	"view_mounted":     {Plane: PlaneShell, Operand: OperandRegion},
	"workspace_kind":   {Plane: PlaneShell, Operand: OperandWorkspace},
	"peer_workspace":   {Plane: PlaneShell},
	"context_feature":  {Plane: PlaneShell, Operand: OperandFeature},
	"composer_focused": {Plane: PlaneShell},
	// Availability tracks reachable destinations, not mounted regions.
	"chat_navigable":    {Plane: PlaneShell},
	"context_navigable": {Plane: PlaneShell},
	"sidebar_expanded":  {Plane: PlaneShell},
	// Explicitly hidden split panes.
	"context_collapsed":      {Plane: PlaneShell},
	"conversation_collapsed": {Plane: PlaneShell},
	// A stage and the conversation share the window as a split.
	"split_live":         {Plane: PlaneShell},
	"files_stage_active": {Plane: PlaneShell},
	// The Files navigator is hidden in this window.
	"files_tree_collapsed": {Plane: PlaneShell},
	// File summaries are on, and whether their setting has loaded at all.
	"file_summaries_on":    {Plane: PlaneShell},
	"file_summaries_known": {Plane: PlaneShell},
	// The active Files editor shows a retained or committed past version.
	"files_version_historical": {Plane: PlaneShell},
	// Peer-window observables.
	"peer_open_available": {Plane: PlaneShell},
	"peer_views_present":  {Plane: PlaneShell},
}

// Facts returns the condition vocabulary sorted by fact id.
func Facts() map[string]FactSpec {
	out := make(map[string]FactSpec, len(facts))
	for id, spec := range facts {
		out[id] = spec
	}
	return out
}

func hostFactNames() []string {
	out := make([]string, 0, len(facts))
	for id, spec := range facts {
		if spec.Plane == PlaneHost {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// RequireHostFactCoverage checks exact host binder coverage.
func RequireHostFactCoverage(bound []string) error {
	have := make(map[string]bool, len(bound))
	for _, name := range bound {
		have[name] = true
	}
	var missing, foreign []string
	for _, name := range hostFactNames() {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	for _, name := range bound {
		if facts[name].Plane != PlaneHost {
			foreign = append(foreign, name)
		}
	}
	sort.Strings(foreign)
	switch {
	case len(missing) > 0 && len(foreign) > 0:
		return fmt.Errorf("host facts %s have no binder; %s are not host facts",
			strings.Join(missing, ", "), strings.Join(foreign, ", "))
	case len(missing) > 0:
		return fmt.Errorf("host facts %s have no binder", strings.Join(missing, ", "))
	case len(foreign) > 0:
		return fmt.Errorf("%s are not host facts", strings.Join(foreign, ", "))
	}
	return nil
}

// Shell vocabularies mirror typed shell state.
var shellViews = map[string]bool{
	"chat": true, "files": true, "context": true, "settings": true, "welcome": true,
}

var focusRegions = map[string]bool{
	"sidebar": true, "chat": true, "context": true, "composer": true,
	"files": true, "filesTree": true, "filesTabs": true, "find": true, "settings": true,
}

var workspaceKinds = map[string]bool{
	"main": true, "session": true, "file": true, "context": true,
}

// contextFeatures validates context navigation operands.
var contextFeatures = map[string]bool{
	"files": true, "search": true, "security": true, "cost": true,
	"artifacts": true, "blueprints": true, "extensions": true,
}

// settingsSections validates settings navigation operands.
var settingsSections = map[string]bool{
	"general": true, "providers": true, "approvals": true, "edit-review": true,
	"tests": true, "mcp": true, "scanners": true, "web-research": true,
	"extensions": true, "cost": true, "debug": true,
}

// navigateFixedDestinations require no operand.
var navigateFixedDestinations = map[string]bool{
	"home": true, "chat": true, "files": true, "composer": true,
	"sidebar": true, "all_chats": true,
}

var supportedLanguageSet = func() map[string]bool {
	out := map[string]bool{}
	for _, language := range filekind.SupportedLanguages() {
		out[language] = true
	}
	return out
}()

// reservedChords protects window, application, and dismissal shortcuts.
var reservedChords = map[string]bool{
	"Mod+Q": true, "Mod+W": true, "Mod+N": true, "Mod+,": true, "Escape": true,
}

func bindingSystemReserved(platform, binding string) bool {
	if reservedChords[chordIdentity(binding)] {
		return true
	}
	if platform != "windows" && platform != "linux" {
		return false
	}
	parts := strings.Fields(binding)
	if len(parts) != 1 {
		return false
	}
	tokens := strings.Split(parts[0], "+")
	held := map[string]bool{}
	for _, token := range tokens[:len(tokens)-1] {
		held[token] = true
	}
	return held["Super"] || (held["Alt"] && (held["Mod"] || held["Ctrl"]))
}

// osInterceptedChords lists chords each platform consumes before an app window
// sees them. A spec ending in "+*" covers every chord holding its modifiers.
var osInterceptedChords = map[string][]string{
	"macos": {
		"Mod+Space", "Mod+Alt+Space", "Mod+Ctrl+Space", "Ctrl+Space",
		"Mod+Tab", "Mod+Alt+Escape", "Mod+Shift+Q", "Mod+Ctrl+Q", "Mod+Alt+D",
		"Mod+Shift+3", "Mod+Shift+4", "Mod+Shift+5",
		"Ctrl+ArrowUp", "Ctrl+ArrowDown", "Ctrl+ArrowLeft", "Ctrl+ArrowRight",
	},
	"windows": {"Alt+Tab", "Mod+Shift+Escape", "Mod+Alt+Delete", "Super+*"},
	"linux":   {"Super+*", "Alt+Tab", "Mod+Alt+Delete"},
}

// assistiveReservedKeys belong to assistive navigation on every platform.
var assistiveReservedKeys = []string{"CapsLock", "Insert", "ScrollLock"}

// OSInterceptedChords returns the per-platform OS-consumed chord specs.
func OSInterceptedChords() map[string][]string {
	out := make(map[string][]string, len(osInterceptedChords))
	for platform, specs := range osInterceptedChords {
		out[platform] = append([]string(nil), specs...)
	}
	return out
}

// AssistiveReservedKeys returns the keys no binding may claim.
func AssistiveReservedKeys() []string {
	return append([]string(nil), assistiveReservedKeys...)
}

func bindingOSIntercepted(platform, binding string) bool {
	parts := strings.Fields(binding)
	if len(parts) != 1 {
		return false
	}
	chord := canonicalChord(parts[0])
	tokens := strings.Split(chord, "+")
	held := map[string]bool{}
	for _, token := range tokens[:len(tokens)-1] {
		held[token] = true
	}
	for _, spec := range osInterceptedChords[platform] {
		if family, ok := strings.CutSuffix(spec, "+*"); ok {
			if allHeld(held, strings.Split(family, "+")) {
				return true
			}
			continue
		}
		if canonicalChord(spec) == chord {
			return true
		}
	}
	return false
}

func allHeld(held map[string]bool, modifiers []string) bool {
	for _, modifier := range modifiers {
		if !held[modifier] {
			return false
		}
	}
	return true
}

func bindingAssistiveReserved(binding string) bool {
	parts := strings.Fields(binding)
	for _, part := range parts {
		tokens := strings.Split(part, "+")
		if slices.Contains(assistiveReservedKeys, tokens[len(tokens)-1]) {
			return true
		}
	}
	return false
}
