package prompts

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
)

// FileTemplateEngine loads markdown templates from layered prompt_files roots.
type FileTemplateEngine struct {
	layers     PromptLayers
	registered map[string]string
	onRender   func(ref string, prov []UnitProvenanceRecord)
}

// NewFileTemplateEngineLayers creates an engine with site/project/bundled overlay order.
func NewFileTemplateEngineLayers(layers PromptLayers) *FileTemplateEngine {
	layers.ModuleRoot = strings.TrimSpace(layers.ModuleRoot)
	return &FileTemplateEngine{
		layers:     layers,
		registered: make(map[string]string),
	}
}

// WithProvenanceRecorder binds a callback invoked on each template execution with its provenance records.
func (e *FileTemplateEngine) WithProvenanceRecorder(fn func(ref string, prov []UnitProvenanceRecord)) *FileTemplateEngine {
	if e == nil {
		return nil
	}
	derived := *e
	derived.onRender = fn
	return &derived
}

// WithProjectOverlay returns an engine scoped to projectDir.
func (e *FileTemplateEngine) WithProjectOverlay(projectDir string) *FileTemplateEngine {
	if strings.TrimSpace(projectDir) == "" {
		return e
	}
	return e.WithProjectOverlays([]string{projectDir})
}

// WithProjectOverlays replaces the project overlay set.
func (e *FileTemplateEngine) WithProjectOverlays(rootPaths []string) *FileTemplateEngine {
	if e == nil {
		return nil
	}
	derived := *e
	derived.layers.overlaySnapshot = nil
	derived.layers.revision = ""
	derived.layers.ProjectPrimary = ""
	derived.layers.ProjectActive = ""
	var primary, active string
	switch len(rootPaths) {
	case 0:
		return e
	case 1:
		derived.layers.ProjectPrimary = ProjectPromptFilesDir(rootPaths[0])
	default:
		primary = ProjectPromptFilesDir(rootPaths[0])
		active = ProjectPromptFilesDir(rootPaths[len(rootPaths)-1])
		derived.layers.ProjectPrimary = primary
		derived.layers.ProjectActive = active
		if primary == active {
			derived.layers.ProjectActive = ""
		}
	}
	return &derived
}

// WithWorkflowArchive binds a sealed workflow archive directory.
func (e *FileTemplateEngine) WithWorkflowArchive(archiveDir string) *FileTemplateEngine {
	if e == nil {
		return nil
	}
	derived := *e
	derived.layers.overlaySnapshot = nil
	derived.layers.revision = ""
	derived.layers.WorkflowArchive = strings.TrimSpace(archiveDir)
	return &derived
}

// WithEffectiveCatalog returns an engine bound to eff.
func (e *FileTemplateEngine) WithEffectiveCatalog(eff *extpacks.EffectiveCatalog) *FileTemplateEngine {
	if e == nil {
		return nil
	}
	derived := *e
	derived.layers.Catalog = eff
	derived.layers.overlaySnapshot = nil
	derived.layers.revision = ""
	return &derived
}

// Snapshot returns an engine bound to one immutable prompt source revision.
func (e *FileTemplateEngine) Snapshot() (*FileTemplateEngine, error) {
	if e == nil {
		return nil, fmt.Errorf("prompt engine not configured")
	}
	layers, err := e.layers.Snapshot()
	if err != nil {
		return nil, err
	}
	derived := *e
	derived.layers = layers
	derived.registered = maps.Clone(e.registered)
	encoded, err := json.Marshal(derived.registered)
	if err != nil {
		return nil, err
	}
	derived.layers.revision = fmt.Sprintf("%x", sha256.Sum256(append([]byte(layers.Revision()+"\x00"), encoded...)))
	return &derived, nil
}

// Revision identifies the exact prompt source bound to this engine snapshot.
func (e *FileTemplateEngine) Revision() string {
	if e == nil {
		return ""
	}
	return e.layers.Revision()
}

// Render resolves and executes templateRef.
func (e *FileTemplateEngine) Render(ctx context.Context, templateRef string, data map[string]any) (string, error) {
	ref := strings.TrimSpace(templateRef)
	if ref == "" {
		return "", fmt.Errorf("empty template ref")
	}
	return e.executeTemplate(ctx, ref, data)
}

// RenderWithProvenance resolves and executes templateRef, returning rendered output and provenance records.
func (e *FileTemplateEngine) RenderWithProvenance(ctx context.Context, templateRef string, data map[string]any) (string, []UnitProvenanceRecord, error) {
	ref := strings.TrimSpace(templateRef)
	if ref == "" {
		return "", nil, fmt.Errorf("empty template ref")
	}
	return e.executeTemplateWithProvenance(ctx, ref, data)
}

// Register stores an in-memory template.
// RenderUnit renders one unit template with data.
func (e *FileTemplateEngine) RenderUnit(ctx context.Context, ref string, data map[string]any) (string, error) {
	return e.Render(ctx, ref, data)
}

func (e *FileTemplateEngine) Register(name, template string) error {
	if e.layers.Revision() != "" {
		return fmt.Errorf("cannot register a template on an immutable prompt snapshot")
	}
	ref := strings.TrimSpace(name)
	if ref == "" {
		return fmt.Errorf("empty template name")
	}
	e.registered = maps.Clone(e.registered)
	if e.registered == nil {
		e.registered = make(map[string]string)
	}
	e.registered[ref] = template
	return nil
}

// ModuleRoot returns the lycaon module root the engine was wired with.
func (e *FileTemplateEngine) ModuleRoot() string {
	if e == nil {
		return ""
	}
	return e.layers.ModuleRoot
}

// Layers returns a copy of the configured overlay layers.
func (e *FileTemplateEngine) Layers() PromptLayers {
	if e == nil {
		return PromptLayers{}
	}
	return e.layers
}
