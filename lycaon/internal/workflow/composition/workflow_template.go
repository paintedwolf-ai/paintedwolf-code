package composition

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	"github.com/lycaon/lycaon/pkg/api"
	"os"
	"regexp"
	"strings"
)

var templateParamRef = regexp.MustCompile(`\$\{params\.([a-zA-Z0-9_]+)\}`)

// WorkflowTemplate is a bundled compose starting point with parameter substitution.
type WorkflowTemplate struct {
	ID          string
	Description string
	Parameters  map[string]WorkflowTemplateParameter
	ManifestRaw string
}

// WorkflowTemplateParameter describes one template parameter.
type WorkflowTemplateParameter struct {
	Type     string
	Required bool
	Default  any
}

// TemplateCatalog is a loaded workflow template library.
type TemplateCatalog map[string]*WorkflowTemplate

// LoadTemplatesFromDir loads templates from one source without pack unit gating.
func LoadTemplatesFromDir(dir extpacks.Source) (TemplateCatalog, error) {
	out := TemplateCatalog{}
	entries, err := dir.List()
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".yaml") {
			continue
		}
		at := dir.Join(ent.Name())
		data, err := at.Read()
		if err != nil {
			return nil, err
		}
		if err := addWorkflowTemplate(out, ent.Name(), data); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// LoadTemplatesEffective compiles workflows/_templates units from the resolved
// winners' captured bytes; the view never rereads pack files after resolve.
func LoadTemplatesEffective(catalog *extpacks.EffectiveCatalog) (TemplateCatalog, error) {
	if catalog == nil {
		return nil, fmt.Errorf("workflow templates: effective catalog required")
	}
	out := TemplateCatalog{}
	for _, id := range catalog.LoadedUnitIDs() {
		if !strings.HasPrefix(id, extpacks.WorkflowTemplateUnitIDPrefix) {
			continue
		}
		content, _, ok := catalog.UnitContent(id)
		if !ok {
			continue
		}
		at, _ := catalog.UnitPath(id)
		if err := addWorkflowTemplate(out, at.String(), content); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func addWorkflowTemplate(out TemplateCatalog, origin string, data []byte) error {
	t, err := parseWorkflowTemplate(data)
	if err != nil {
		return fmt.Errorf("%s: %w", origin, err)
	}
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("%s: template id required", origin)
	}
	if prev, dup := out[t.ID]; dup && prev != nil {
		return fmt.Errorf("%s: duplicate workflow template id %q", origin, t.ID)
	}
	out[t.ID] = t
	return nil
}

func parseWorkflowTemplate(data []byte) (*WorkflowTemplate, error) {
	var raw struct {
		ID          string                               `yaml:"id"`
		Description string                               `yaml:"description"`
		Parameters  map[string]workflowTemplateParamYAML `yaml:"parameters"`
		Manifest    yaml.Node                            `yaml:"manifest"`
	}
	if err := config.DecodeYAML(data, &raw); err != nil {
		return nil, err
	}
	manifestBytes, err := yaml.Marshal(&raw.Manifest)
	if err != nil {
		return nil, err
	}
	params := map[string]WorkflowTemplateParameter{}
	for name, p := range raw.Parameters {
		params[name] = WorkflowTemplateParameter{
			Type:     strings.TrimSpace(p.Type),
			Required: p.Required,
			Default:  p.Default,
		}
	}
	return &WorkflowTemplate{
		ID:          strings.TrimSpace(raw.ID),
		Description: strings.TrimSpace(raw.Description),
		Parameters:  params,
		ManifestRaw: string(manifestBytes),
	}, nil
}

type workflowTemplateParamYAML struct {
	Type     string `yaml:"type"`
	Required bool   `yaml:"required"`
	Default  any    `yaml:"default"`
}

// List returns template metadata for catalog HTTP responses.
func (c TemplateCatalog) List() []api.WorkflowTemplateSummary {
	if len(c) == 0 {
		return nil
	}
	out := make([]api.WorkflowTemplateSummary, 0, len(c))
	for _, t := range c {
		if t == nil {
			continue
		}
		out = append(out, t.Summary())
	}
	return out
}

// Summary returns API metadata for one template.
func (t *WorkflowTemplate) Summary() api.WorkflowTemplateSummary {
	if t == nil {
		return api.WorkflowTemplateSummary{}
	}
	params := map[string]api.WorkflowTemplateParameter{}
	for name, p := range t.Parameters {
		params[name] = api.WorkflowTemplateParameter{
			Type:     p.Type,
			Required: p.Required,
			Default:  p.Default,
		}
	}
	return api.WorkflowTemplateSummary{
		ID:          t.ID,
		Description: t.Description,
		Parameters:  params,
	}
}

// Expand substitutes ${params.name} placeholders and returns manifest YAML bytes.
func (t *WorkflowTemplate) Expand(params map[string]any) ([]byte, error) {
	if t == nil {
		return nil, fmt.Errorf("template required")
	}
	resolved := map[string]any{}
	for name, spec := range t.Parameters {
		if v, ok := params[name]; ok && v != nil && fmt.Sprint(v) != "" {
			resolved[name] = v
			continue
		}
		if spec.Required {
			return nil, fmt.Errorf("required template parameter %q missing", name)
		}
		if spec.Default != nil {
			resolved[name] = spec.Default
		}
	}
	for name, spec := range t.Parameters {
		if spec.Required {
			if _, ok := resolved[name]; !ok {
				return nil, fmt.Errorf("required template parameter %q missing", name)
			}
		}
	}
	expanded := templateParamRef.ReplaceAllStringFunc(t.ManifestRaw, func(match string) string {
		sub := templateParamRef.FindStringSubmatch(match)
		if len(sub) != 2 {
			return match
		}
		v, ok := resolved[sub[1]]
		if !ok {
			return match
		}
		return fmt.Sprint(v)
	})
	if strings.Contains(expanded, "${params.") {
		return nil, fmt.Errorf("unresolved template parameter placeholder")
	}
	return []byte(expanded), nil
}

// ComposeFromTemplateRequest is input to template-based compose.
type ComposeFromTemplateRequest struct {
	SessionID      string
	ProjectDir     string
	TemplateID     string
	Params         map[string]any
	SessionPosture api.SessionPosture
	CreatedBy      workflowdrafts.Actor
	DryRun         bool
}

// ComposeFromTemplate expands a template and runs the compose pipeline.
func (c *Composer) ComposeFromTemplate(ctx context.Context, req ComposeFromTemplateRequest) (*ComposeResult, error) {
	if c == nil || c.Templates == nil {
		return nil, fmt.Errorf("workflow templates not configured")
	}
	templateID := strings.TrimSpace(req.TemplateID)
	t, ok := c.Templates[templateID]
	if !ok {
		return nil, &ComposeValidationFailed{Errors: []api.ComposeValidationError{{
			Field:   "template_id",
			Code:    "unknown_template",
			Message: fmt.Sprintf("unknown workflow template %q", templateID),
		}}}
	}
	manifestYAML, err := t.Expand(req.Params)
	if err != nil {
		return nil, &ComposeValidationFailed{Errors: []api.ComposeValidationError{{
			Field:   "params",
			Code:    "template_param_error",
			Message: err.Error(),
		}}}
	}
	return c.Compose(ctx, ComposeRequest{
		SessionID:      req.SessionID,
		ProjectDir:     req.ProjectDir,
		ManifestYAML:   manifestYAML,
		SessionPosture: req.SessionPosture,
		CreatedBy:      req.CreatedBy,
		DryRun:         req.DryRun,
	})
}
