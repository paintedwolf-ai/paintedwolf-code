package feedback

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/flosch/pongo2/v6"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/pongoplain"
)

// gateFeedbackTemplate renders every gate response.
const gateFeedbackTemplate = config.GateFeedbackTmplDir + "/_gate.md.tmpl"

// GateFeedbackDef is per-gate structured feedback.
type GateFeedbackDef struct {
	ID                 string              `yaml:"id"`
	Kind               string              `yaml:"kind"`
	Purpose            string              `yaml:"purpose"`
	MissingSignals     []GateMissingSignal `yaml:"missing_signals"`
	Satisfy            []string            `yaml:"satisfy"`
	SatisfyAuto        []string            `yaml:"satisfy_auto"`
	SatisfyCoordinator []string            `yaml:"satisfy_coordinator"`
}

// GateMissingSignal is one conditional missing-signal row in gate feedback YAML.
type GateMissingSignal struct {
	ID     string `yaml:"id"`
	When   string `yaml:"when"`
	Detail string `yaml:"detail"`
	Fix    string `yaml:"fix"`
}

// GateFeedbackCatalog loads and renders per-gate feedback YAML.
type GateFeedbackCatalog struct {
	defs     map[string]GateFeedbackDef
	template *pongo2.Template
}

// LoadGateFeedbackCatalog loads effective gate feedback.
func LoadGateFeedbackCatalog() (*GateFeedbackCatalog, error) {
	catalog, err := extpacks.CatalogForConsumers()
	if err != nil {
		return nil, err
	}
	return LoadGateFeedbackCatalogWithCatalog(catalog)
}

// LoadGateFeedbackCatalogWithCatalog compiles captured feedback units.
func LoadGateFeedbackCatalogWithCatalog(catalog *extpacks.EffectiveCatalog) (*GateFeedbackCatalog, error) {
	if catalog == nil {
		return nil, fmt.Errorf("gate-feedback: effective catalog required")
	}
	defs := map[string]GateFeedbackDef{}
	for _, id := range catalog.LoadedUnitIDs() {
		if !strings.HasPrefix(id, extpacks.GateFeedbackUnitIDPrefix) {
			continue
		}
		content, _, ok := catalog.UnitContent(id)
		if !ok {
			continue
		}
		at, _ := catalog.UnitPath(id)
		var def GateFeedbackDef
		if err := config.DecodeYAML(content, &def); err != nil {
			return nil, fmt.Errorf("%s: %w", at, err)
		}
		if err := validateGateFeedbackDef(at, at.Base(), def); err != nil {
			return nil, err
		}
		// Body identifiers must remain unique across unit identifiers.
		if _, ok := defs[def.ID]; ok {
			return nil, fmt.Errorf("%s: duplicate gate-feedback id %q", at, def.ID)
		}
		defs[def.ID] = def
	}
	if len(defs) == 0 {
		return nil, fmt.Errorf("gate-feedback: no definitions in the effective catalog")
	}
	tmplData, err := config.Read(gateFeedbackTemplate)
	if err != nil {
		return nil, err
	}
	tmpl, err := pongoplain.Compile(string(tmplData))
	if err != nil {
		return nil, fmt.Errorf("gate feedback template: %w", err)
	}
	return &GateFeedbackCatalog{defs: defs, template: tmpl}, nil
}

func validateGateFeedbackDef(at extpacks.Source, name string, def GateFeedbackDef) error {
	id := strings.TrimSpace(def.ID)
	if id == "" {
		return fmt.Errorf("%s: id required", at)
	}
	if GateFeedbackFilename(id) != name {
		return fmt.Errorf("%s: filename must be %s for gate id %q", at, GateFeedbackFilename(id), id)
	}
	if strings.TrimSpace(def.Purpose) == "" {
		return fmt.Errorf("%s: purpose required for gate %q", at, id)
	}
	if len(def.Satisfy) == 0 && len(def.SatisfyAuto) == 0 && len(def.SatisfyCoordinator) == 0 {
		return fmt.Errorf("%s: satisfy (or satisfy_auto / satisfy_coordinator) required for gate %q", at, id)
	}
	for _, s := range def.MissingSignals {
		if strings.TrimSpace(s.Detail) == "" || strings.TrimSpace(s.Fix) == "" {
			return fmt.Errorf("%s: missing_signals detail+fix required for gate %q", at, id)
		}
		// Validate expressions with their catalog location.
		if _, err := parseGateSignalWhen(s.When); err != nil {
			return fmt.Errorf("%s: gate %q missing_signals %q: %w", at, id, s.ID, err)
		}
	}
	return nil
}

// GateFeedbackFilename returns the pack filename for a gate leaf id.
func GateFeedbackFilename(leafID string) string {
	return strings.NewReplacer(":", "-", "/", "-").Replace(strings.TrimSpace(leafID)) + ".yaml"
}

// GateIDs returns sorted gate ids with authored feedback YAML.
func (c *GateFeedbackCatalog) GateIDs() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.defs))
	for id := range c.defs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Has returns whether feedback exists for leafID.
func (c *GateFeedbackCatalog) Has(leafID string) bool {
	if c == nil {
		return false
	}
	_, ok := c.defs[strings.TrimSpace(leafID)]
	return ok
}

// Def returns the authored gate-feedback definition for leafID.
func (c *GateFeedbackCatalog) Def(leafID string) (GateFeedbackDef, bool) {
	if c == nil {
		return GateFeedbackDef{}, false
	}
	def, ok := c.defs[strings.TrimSpace(leafID)]
	return def, ok
}

// GateObligation is a compact purpose + satisfy projection for inject/kicks.
type GateObligation struct {
	ID       string
	Purpose  string
	Satisfy  []string
	Missing  []string // matched missing_signals detail lines (optional)
	Required []string // exact required labels the gate enforces (shown before write)
}

const (
	// MaxGateObligations caps how many unsatisfied leaves ride inject/kick turns.
	MaxGateObligations = 4
	// MaxSatisfyStepsPerObligation caps numbered satisfy bullets per leaf.
	MaxSatisfyStepsPerObligation = 4
)

// ProjectObligations builds compact feedback for unsatisfied leaves.
func (c *GateFeedbackCatalog) ProjectObligations(ctx context.Context, leafIDs []string, advanceWhenGateMet string, extras map[string]any) []GateObligation {
	if c == nil || len(leafIDs) == 0 {
		return nil
	}
	feedbackCtx := GateFeedbackContext(WorkflowEvaluationContext{
		AdvanceWhenGateMet: strings.TrimSpace(advanceWhenGateMet),
	}, extras)
	out := make([]GateObligation, 0, len(leafIDs))
	for _, leaf := range leafIDs {
		leaf = strings.TrimSpace(leaf)
		if leaf == "" {
			continue
		}
		def, ok := c.defs[leaf]
		if !ok {
			continue
		}
		satisfy := renderGateFeedbackSatisfy(ctx, def, feedbackCtx)
		if len(satisfy) > MaxSatisfyStepsPerObligation {
			satisfy = satisfy[:MaxSatisfyStepsPerObligation]
		}
		missing := projectMissingDetails(ctx, def, feedbackCtx)
		required := requiredLabelsForLeaf(leaf, feedbackCtx)
		out = append(out, GateObligation{
			ID:       def.ID,
			Purpose:  shortenGatePurpose(def.Purpose),
			Satisfy:  satisfy,
			Missing:  missing,
			Required: required,
		})
		if len(out) >= MaxGateObligations {
			break
		}
	}
	return out
}

func requiredLabelsForLeaf(leaf string, ctx map[string]any) []string {
	if leaf == "plan_stub_valid" {
		if raw, ok := ctx["required_stub_labels"].([]string); ok && len(raw) > 0 {
			return append([]string(nil), raw...)
		}
		return conditions.PlanStubRequiredLabels()
	}
	return nil
}

func projectMissingDetails(ctx context.Context, def GateFeedbackDef, feedbackCtx map[string]any) []string {
	matched, err := matchMissingSignals(def.MissingSignals, feedbackCtx)
	if err != nil || len(matched) == 0 {
		return nil
	}
	out := make([]string, 0, len(matched))
	for _, s := range matched {
		detail, err := renderGateSignalText(ctx, s.Detail, feedbackCtx)
		if err != nil || strings.TrimSpace(detail) == "" {
			continue
		}
		out = append(out, detail)
		if len(out) >= MaxSatisfyStepsPerObligation {
			break
		}
	}
	return out
}

func shortenGatePurpose(purpose string) string {
	purpose = strings.TrimSpace(purpose)
	if purpose == "" {
		return ""
	}
	if i := strings.IndexByte(purpose, '\n'); i >= 0 {
		purpose = strings.TrimSpace(purpose[:i])
	}
	return purpose
}

// RenderGateFeedback renders structured feedback for a failed gate leaf.
func (c *GateFeedbackCatalog) RenderGateFeedback(ctx context.Context, leafID string, feedbackCtx map[string]any) (string, error) {
	if c == nil || c.template == nil {
		return "", nil
	}
	def, ok := c.defs[strings.TrimSpace(leafID)]
	if !ok {
		return "", nil
	}
	if feedbackCtx == nil {
		feedbackCtx = map[string]any{}
	}
	matched, err := matchMissingSignals(def.MissingSignals, feedbackCtx)
	if err != nil {
		return "", err
	}
	satisfy := renderGateFeedbackSatisfy(ctx, def, feedbackCtx)
	signals, err := matchedSignalsForRender(ctx, matched, feedbackCtx)
	if err != nil {
		return "", err
	}
	out, err := pongoplain.Execute(ctx, c.template, map[string]any{
		"gate": map[string]any{
			"id":      def.ID,
			"kind":    def.Kind,
			"purpose": def.Purpose,
			"satisfy": satisfy,
		},
		"matched_signals": signals,
		"context":         feedbackCtx,
	})
	if err != nil {
		return "", fmt.Errorf("render gate %q: %w", leafID, err)
	}
	return strings.TrimSpace(out), nil
}

func selectGateFeedbackSatisfy(def GateFeedbackDef, ctx map[string]any) []string {
	mode, _ := ctx["advance_when_gate_met"].(string)
	mode = strings.TrimSpace(mode)
	if mode == "coordinator" && len(def.SatisfyCoordinator) > 0 {
		return append([]string(nil), def.SatisfyCoordinator...)
	}
	if mode == "auto" && len(def.SatisfyAuto) > 0 {
		return append([]string(nil), def.SatisfyAuto...)
	}
	return append([]string(nil), def.Satisfy...)
}

// renderGateFeedbackSatisfy selects the satisfy arm and renders each step
// through the signal template pipeline, so authored copy can reference
// context values (for example `{{ context.review_agents }}`).
func renderGateFeedbackSatisfy(ctx context.Context, def GateFeedbackDef, feedbackCtx map[string]any) []string {
	selected := selectGateFeedbackSatisfy(def, feedbackCtx)
	out := make([]string, 0, len(selected))
	for _, step := range selected {
		rendered, err := renderGateSignalText(ctx, step, feedbackCtx)
		if err != nil || strings.TrimSpace(rendered) == "" {
			continue
		}
		out = append(out, rendered)
	}
	return out
}

func matchMissingSignals(signals []GateMissingSignal, ctx map[string]any) ([]GateMissingSignal, error) {
	if len(signals) == 0 {
		return nil, nil
	}
	var matched []GateMissingSignal
	for _, s := range signals {
		node, err := parseGateSignalWhen(s.When)
		if err != nil {
			return nil, fmt.Errorf("missing_signals %q: %w", s.ID, err)
		}
		if evalGateSignalWhen(node, ctx) {
			matched = append(matched, s)
		}
	}
	return matched, nil
}

func matchedSignalsForRender(ctx context.Context, signals []GateMissingSignal, feedbackCtx map[string]any) ([]map[string]string, error) {
	out := make([]map[string]string, 0, len(signals))
	for _, s := range signals {
		detail, err := renderGateSignalText(ctx, s.Detail, feedbackCtx)
		if err != nil {
			return nil, err
		}
		fix, err := renderGateSignalText(ctx, s.Fix, feedbackCtx)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]string{
			"id":     s.ID,
			"detail": detail,
			"fix":    fix,
		})
	}
	return out, nil
}

func renderGateSignalText(ctx context.Context, tmpl string, feedbackCtx map[string]any) (string, error) {
	tmpl = strings.TrimSpace(tmpl)
	if tmpl == "" {
		return "", nil
	}
	if !strings.Contains(tmpl, "{{") && !strings.Contains(tmpl, "{%") {
		return tmpl, nil
	}
	t, err := pongoplain.Compile(tmpl)
	if err != nil {
		return "", fmt.Errorf("signal template: %w", err)
	}
	out, err := pongoplain.Execute(ctx, t, map[string]any{"context": feedbackCtx})
	if err != nil {
		return "", fmt.Errorf("signal template: %w", err)
	}
	return strings.TrimSpace(out), nil
}
