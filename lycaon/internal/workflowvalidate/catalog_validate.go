package workflowvalidate

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/vocabulary"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// CatalogValidateMode selects which catalog slice to validate.
type CatalogValidateMode int

const (
	ModeBundled CatalogValidateMode = iota // shipped catalog + surfaces + templates + spawn SSOT
	ModeProject                            // {project}/<overlay>/workflows (third-party surface)
	ModePaths                              // explicit author yaml paths (same overlay rules as Project)
)

// CatalogValidateOptions configures ValidateCatalog.
type CatalogValidateOptions struct {
	ConfigRoot string   // lycaon module root (…/lycaon)
	ProjectDir string   // optional; loads {project}/<overlay>/workflows
	Paths      []string // optional explicit yaml paths (author files)
	Mode       CatalogValidateMode
}

// ValidateCatalog runs the full host-runnable catalog gate.
// Returns diagnostics for content problems; error only on resolve/IO failure
// that prevents running checks (e.g. missing config root).
func ValidateCatalog(ctx context.Context, opts CatalogValidateOptions) ([]api.ComposeValidationError, error) {
	if strings.TrimSpace(opts.ConfigRoot) == "" {
		opts.ConfigRoot = configlayout.FindModuleRootFrom(".")
	}
	if _, err := workflowdiag.Load(); err != nil {
		return nil, fmt.Errorf("workflow diagnostics catalog: %w", err)
	}

	condReg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	if err != nil {
		return nil, fmt.Errorf("condition registry: %w", err)
	}
	if err := rules.RegisterRuleConditions(condReg); err != nil {
		return nil, fmt.Errorf("rule conditions: %w", err)
	}

	var out []api.ComposeValidationError

	if opts.Mode == ModePaths {
		if len(opts.Paths) == 0 {
			return nil, fmt.Errorf("ModePaths requires Paths")
		}
		base, err := workflowdef.RegistryFromDirs("")
		if err != nil {
			return nil, err
		}
		agents, err := loadAgents(ctx, opts.ConfigRoot)
		if err != nil {
			return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"), "agents", //nolint:nilerr // the load error is returned as a api.ComposeValidationError, which is this API's error channel
				map[string]any{"detail": err.Error()})}, nil
		}
		for _, p := range opts.Paths {
			m, err := workflowdef.LoadManifestFromFile(p)
			if err != nil {
				out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"), p,
					map[string]any{"detail": err.Error()}))
				continue
			}
			// Validate reachability before orphan phases are pruned.
			for _, d := range workflowvalidation.ValidatePhaseReachability(m) {
				if d.Field == "" {
					d.Field = p
				} else {
					d.Field = p + ": " + d.Field
				}
				out = append(out, d)
			}
			eff, err := resolveAgainstBase(m, base)
			if err != nil {
				out = append(out, extendsDiags(p, err)...)
				continue
			}
			out = append(out, validateOne(opts, condReg, agents, p, eff, true)...)
		}
		out = append(out, checkSurfaceProgress(opts)...)
		return out, nil
	}

	reg, sources, err := CatalogSources(opts)
	if err != nil {
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"), "catalog", //nolint:nilerr // the load error is returned as a api.ComposeValidationError, which is this API's error channel
			map[string]any{"detail": err.Error()})}, nil
	}
	if err := assertSourceBijection(reg, sources); err != nil {
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"), "catalog", //nolint:nilerr // the bijection error is returned as a api.ComposeValidationError, which is this API's error channel
			map[string]any{"detail": err.Error()})}, nil
	}

	ruleConfigs, err := rules.LoadBundledRuleConfigs()
	if err != nil {
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"), "rules", //nolint:nilerr // the load error is returned as a api.ComposeValidationError, which is this API's error channel
			map[string]any{"detail": err.Error()})}, nil
	}
	out = append(out, vocabulary.ValidateBundled(condReg, reg, ruleConfigs)...)

	agents, err := loadAgents(ctx, opts.ConfigRoot)
	if err != nil {
		out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"), "agents",
			map[string]any{"detail": err.Error()}))
	}

	srcByKey := map[string]WorkflowSource{}
	for _, s := range sources {
		srcByKey[s.Key] = s
	}
	// Overlay rules apply to author-written manifests only; the shipped base
	// legitimately uses bundled-only leaves and shipped surface profiles.
	for key, m := range reg.All() {
		src := srcByKey[key]
		path := src.Path
		if path == "" {
			path = key
		}
		if src.Origin == workflowdef.OriginArchive {
			// Released bytes are not held to rules written after they shipped.
			out = append(out, checkArchivedGuidance(path, m)...)
			continue
		}
		out = append(out, validateOne(opts, condReg, agents, path, m, src.Origin == workflowdef.OriginOverlay)...)
	}

	// Surface progress is host-catalog SSOT (one emitter for all modes).
	out = append(out, checkSurfaceProgress(opts)...)
	if opts.Mode == ModeBundled {
		out = append(out, validateBundledExtras(opts, condReg, reg)...)
	}
	return out, nil
}

func validateOne(
	opts CatalogValidateOptions,
	condReg *conditions.ConditionRegistry,
	agents orchestration.AgentRegistry,
	path string,
	m workflowdef.Manifest,
	overlayRules bool,
) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	prefix := func(field string) string {
		if field == "" {
			return path
		}
		return path + ": " + field
	}
	for _, d := range workflowvalidation.ValidateComposeManifest(condReg, shippedObligationSpecs(), m) {
		d.Field = prefix(d.Field)
		out = append(out, d)
	}
	for _, d := range workflowvalidation.ValidatePhaseReachability(m) {
		d.Field = prefix(d.Field)
		out = append(out, d)
	}
	moduleRoot := opts.ConfigRoot
	for _, d := range workflowvalidation.ValidateAllowedAgents(agents, m.AllowedAgents) {
		d.Field = prefix(d.Field)
		out = append(out, d)
	}
	projectDir := opts.ProjectDir
	for _, d := range workflowvalidation.ValidateRulesPaths(moduleRoot, projectDir, m.Rules) {
		d.Field = prefix(d.Field)
		out = append(out, d)
	}
	out = append(out, checkLeaveability(opts, path, m)...)
	out = append(out, checkClosure(opts, condReg, path, m, overlayRules)...)
	if overlayRules {
		out = append(out, checkOverlayGateKit(path, m)...)
		out = append(out, checkOverlayExplicitSurfaces(path, m)...)
		out = append(out, checkOverlayAttachSessionCreate(path, m)...)
	}
	return out
}

func resolveAgainstBase(m workflowdef.Manifest, base *workflowdef.Registry) (workflowdef.Manifest, error) {
	catalog := map[string]workflowdef.Manifest{}
	if base != nil {
		for k, v := range base.All() {
			catalog[k] = v
		}
	}
	key := m.ID + "@" + m.Version
	catalog[key] = m
	return workflowdef.ResolveManifestChain(m, catalog)
}

func extendsDiags(path string, err error) []api.ComposeValidationError {
	return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("extends_error"), path,
		map[string]any{"detail": err.Error(), "extends": ""})}
}

func assertSourceBijection(reg *workflowdef.Registry, sources []WorkflowSource) error {
	regKeys := map[string]struct{}{}
	for k := range reg.All() {
		regKeys[k] = struct{}{}
	}
	srcKeys := map[string]struct{}{}
	for _, s := range sources {
		if _, dup := srcKeys[s.Key]; dup {
			return fmt.Errorf("duplicate source key %s", s.Key)
		}
		srcKeys[s.Key] = struct{}{}
		if _, ok := regKeys[s.Key]; !ok {
			return fmt.Errorf("source %s missing from registry", s.Key)
		}
	}
	for k := range regKeys {
		if _, ok := srcKeys[k]; !ok {
			return fmt.Errorf("registry key %s missing source", k)
		}
	}
	return nil
}

func loadAgents(ctx context.Context, configRoot string) (orchestration.AgentRegistry, error) {
	reg := orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(ctx, reg); err != nil {
		return nil, err
	}
	return reg, nil
}
