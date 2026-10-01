package projectcontrib

// SurfaceGroup places a surface in the Trust panel.
type SurfaceGroup string

const (
	// GroupSteering shapes how the agent works.
	GroupSteering SurfaceGroup = "steering"
	// GroupReplacesDefaults substitutes app behavior.
	GroupReplacesDefaults SurfaceGroup = "replaces_defaults"
	// GroupSuggestion contains device install proposals.
	GroupSuggestion SurfaceGroup = "suggestion"
)

// Surface IDs, also the wire values.
const (
	SurfaceAgentsMD             = "agents_md"
	SurfaceSkills               = "skills"
	SurfaceProjectSettings      = "project_settings"
	SurfaceProjectMCP           = "project_mcp"
	SurfaceScanConfig           = "scan_config"
	SurfacePromptOverrides      = "prompt_overrides"
	SurfaceExtensionConfig      = "extension_config"
	SurfaceExtensionSuggestions = "extension_suggestions"
)

// Spec defines one project trust surface.
type Spec struct {
	ID    string
	Label string
	Group SurfaceGroup
}

// Registry order is the render order.
var registry = []Spec{
	{ID: SurfaceAgentsMD, Label: "Instructions", Group: GroupSteering},
	{ID: SurfaceSkills, Label: "Skills", Group: GroupSteering},
	{ID: SurfaceProjectSettings, Label: "Approvals & limits", Group: GroupSteering},
	{ID: SurfaceProjectMCP, Label: "MCP providers", Group: GroupSteering},
	{ID: SurfaceScanConfig, Label: "Scanning and ignores", Group: GroupReplacesDefaults},
	{ID: SurfacePromptOverrides, Label: "App prompts", Group: GroupReplacesDefaults},
	{ID: SurfaceExtensionConfig, Label: "Extension settings", Group: GroupReplacesDefaults},
	{ID: SurfaceExtensionSuggestions, Label: "Suggested extensions", Group: GroupSuggestion},
}

// Registry returns the ordered surface registry.
func Registry() []Spec {
	out := make([]Spec, len(registry))
	copy(out, registry)
	return out
}

// Lookup finds one registry row.
func Lookup(surfaceID string) (Spec, bool) {
	for _, row := range registry {
		if row.ID == surfaceID {
			return row, true
		}
	}
	return Spec{}, false
}
