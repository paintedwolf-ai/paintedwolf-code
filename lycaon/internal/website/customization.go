package website

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"gopkg.in/yaml.v3"
)

const customizationGeneratedHeader = `# Codegened from paintedwolf-ai/lycaon — do not hand-edit.
# Source: lycaon/config/packs/painted-wolf/platform/workflows/*/workflow.yaml + data/customization.overlay.yaml
# Regenerate: ./task customization:sync (LYCAON_ROOT checkout must match data/paintedwolf.ref).
#
# layers/surfaces come from the website overlay (the prose half). workflows are
# projected from the bundled workflow catalog, so adding or editing a workflow
# YAML updates the docs table automatically.
#
# Per workflow:
#   id            workflow id (also the project-overlay filename)
#   trigger       slash command that starts it (blank = not user-triggered)
#   tier          catalog (user-facing) | system (attach.policy session_create)
#   surface       coordinator surface_profile the workflow binds
#   user_facing   true when attach.policy is not session_create
#   description    one-line summary (workflow.yaml description:, else overlay)
`

// WebsiteCustomization is the Hugo data/customization.yaml shape.
type WebsiteCustomization struct {
	Layers    []CustomizationLayer    `yaml:"layers"`
	Surfaces  []CustomizationSurface  `yaml:"surfaces"`
	Workflows []CustomizationWorkflow `yaml:"workflows"`
}

// CustomizationLayer is one row of the override-precedence table (embed → global → project).
type CustomizationLayer struct {
	ID      string `yaml:"id"`
	Label   string `yaml:"label"`
	Path    string `yaml:"path"`
	Purpose string `yaml:"purpose"`
}

// CustomizationSurface is a category of bundled YAML a user can override on disk.
type CustomizationSurface struct {
	ID          string `yaml:"id"`
	Label       string `yaml:"label"`
	Dir         string `yaml:"dir"`
	OverrideAt  string `yaml:"override_at"`
	Description string `yaml:"description"`
}

// CustomizationWorkflow is one projected workflow row in the docs catalog.
type CustomizationWorkflow struct {
	ID          string `yaml:"id"`
	Trigger     string `yaml:"trigger,omitempty"`
	Tier        string `yaml:"tier"`
	Surface     string `yaml:"surface,omitempty"`
	Name        string `yaml:"name,omitempty"`
	Description string `yaml:"description,omitempty"`
	UserFacing  bool   `yaml:"user_facing"`
}

// CustomizationOverlay is the hand-maintained paintedwolf-www overlay.
type CustomizationOverlay struct {
	Layers    []CustomizationLayer       `yaml:"layers"`
	Surfaces  []CustomizationSurface     `yaml:"surfaces"`
	Workflows map[string]OverlayWorkflow `yaml:"workflows"`
}

// OverlayWorkflow supplies website-only copy keyed by workflow id (description
// fallback when the workflow.yaml has none).
type OverlayWorkflow struct {
	Description string `yaml:"description,omitempty"`
}

// rawWorkflowYAML is the minimal slice of workflow.yaml the docs catalog reads.
type rawWorkflowYAML struct {
	ID             string      `yaml:"id"`
	Trigger        string      `yaml:"trigger"`
	Attach         *attachYAML `yaml:"attach"`
	SurfaceProfile string      `yaml:"surface_profile"`
	Name           string      `yaml:"name"`
	Description    string      `yaml:"description"`
}

type attachYAML struct {
	Policy string `yaml:"policy"`
}

// RenderCustomizationYAML projects the bundled workflow catalog plus the website
// overlay into the Hugo data/customization.yaml document.
func RenderCustomizationYAML(configRoot, overlayPath string) ([]byte, error) {
	overlay, err := LoadCustomizationOverlay(overlayPath)
	if err != nil {
		return nil, fmt.Errorf("load overlay: %w", err)
	}
	workflows, err := loadWorkflowCatalog(configRoot)
	if err != nil {
		return nil, fmt.Errorf("load workflow catalog: %w", err)
	}
	out, err := MergeCustomization(overlay, workflows)
	if err != nil {
		return nil, err
	}
	body, err := yaml.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal customization: %w", err)
	}
	return append([]byte(customizationGeneratedHeader), body...), nil
}

// LoadCustomizationOverlay reads data/customization.overlay.yaml.
func LoadCustomizationOverlay(path string) (*CustomizationOverlay, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path from caller (www sync script)
	if err != nil {
		return nil, err
	}
	var overlay CustomizationOverlay
	if err := yaml.Unmarshal(data, &overlay); err != nil {
		return nil, err
	}
	if len(overlay.Layers) == 0 {
		return nil, fmt.Errorf("overlay layers must not be empty")
	}
	if len(overlay.Surfaces) == 0 {
		return nil, fmt.Errorf("overlay surfaces must not be empty")
	}
	if overlay.Workflows == nil {
		overlay.Workflows = map[string]OverlayWorkflow{}
	}
	return &overlay, nil
}

// loadWorkflowCatalog reads workflow.yaml from every stock pack workflows/ tree.
func loadWorkflowCatalog(configRoot string) ([]rawWorkflowYAML, error) {
	packs, err := extpacks.DiscoverStock()
	if err != nil {
		return nil, err
	}
	var out []rawWorkflowYAML
	for _, wfRoot := range extpacks.KindDirs(packs, "workflows") {
		entries, err := wfRoot.List()
		if err != nil {
			return nil, err
		}
		for _, ent := range entries {
			// `_`-prefixed dirs are templates/topologies/shared, not user-facing workflows.
			if !ent.IsDir() || strings.HasPrefix(ent.Name(), "_") {
				continue
			}
			path := wfRoot.Join(ent.Name(), "workflow.yaml")
			data, err := path.Read() // #nosec G304 -- bundled catalog path
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			var raw rawWorkflowYAML
			if err := yaml.Unmarshal(data, &raw); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			if strings.TrimSpace(raw.ID) == "" {
				return nil, fmt.Errorf("%s: missing id", path)
			}
			out = append(out, raw)
		}
	}
	return out, nil
}

// MergeCustomization combines overlay prose with the projected workflow catalog.
func MergeCustomization(overlay *CustomizationOverlay, workflows []rawWorkflowYAML) (*WebsiteCustomization, error) {
	if overlay == nil {
		return nil, fmt.Errorf("overlay is nil")
	}
	rows := make([]CustomizationWorkflow, 0, len(workflows))
	for _, w := range workflows {
		attachPolicy := ""
		if w.Attach != nil {
			attachPolicy = strings.TrimSpace(w.Attach.Policy)
		}
		// IsCatalogVisible equivalent: not ambient-attach (session_create).
		userFacing := attachPolicy != "session_create"
		tier := "catalog"
		if attachPolicy == "session_create" {
			tier = "system"
		}
		desc := strings.TrimSpace(w.Description)
		if desc == "" {
			desc = strings.TrimSpace(overlay.Workflows[w.ID].Description)
		}
		rows = append(rows, CustomizationWorkflow{
			ID:          w.ID,
			Trigger:     strings.TrimSpace(w.Trigger),
			Tier:        tier,
			Surface:     strings.TrimSpace(w.SurfaceProfile),
			Name:        strings.TrimSpace(w.Name),
			Description: desc,
			UserFacing:  userFacing,
		})
	}
	// Flag overlay descriptions that no longer match a workflow (stale docs copy).
	for id := range overlay.Workflows {
		found := false
		for _, w := range workflows {
			if w.ID == id {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("overlay workflow %q has no matching bundled workflow — remove it from data/customization.overlay.yaml", id)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].UserFacing != rows[j].UserFacing {
			return rows[i].UserFacing // user-facing (catalog) first
		}
		return rows[i].ID < rows[j].ID
	})
	return &WebsiteCustomization{
		Layers:    overlay.Layers,
		Surfaces:  overlay.Surfaces,
		Workflows: rows,
	}, nil
}
