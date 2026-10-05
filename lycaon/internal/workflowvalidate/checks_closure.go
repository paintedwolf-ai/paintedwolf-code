package workflowvalidate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
)

func checkClosure(
	opts CatalogValidateOptions,
	condReg *conditions.ConditionRegistry,
	path string,
	m workflowdef.Manifest,
	overlayRules bool,
) []api.ComposeValidationError {
	if m.Sealed {
		return checkSealedClosure(path, m)
	}
	var out []api.ComposeValidationError
	out = append(out, checkInvoke(opts, path, m)...)
	out = append(out, checkPrompts(opts, path, m)...)
	out = append(out, checkKicks(opts, path, m)...)
	out = append(out, checkReadiness(condReg, path, m)...)
	out = append(out, checkEvidenceKeys(path, m)...)
	// Review-loop fixtures exist only in bundled source trees.
	if opts.Mode == ModeBundled && !overlayRules {
		out = append(out, checkReviewLoopBundled(opts, path, m)...)
	}
	return out
}

func checkOverlayGateKit(path string, m workflowdef.Manifest) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	for _, p := range m.PhaseDefs {
		for _, id := range collectLeaves(p) {
			if id == workflowdef.CompleteWhenGatesSatisfied {
				continue
			}
			if strings.HasPrefix(id, workflowdef.CompleteWhenGateSatisfied) {
				continue
			}
			if !workflow.IsGenericGateKitLeaf(id) {
				out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("domain_leaf_on_overlay"),
					fmt.Sprintf("%s: phases[%s]", path, p.ID),
					map[string]any{"leaf": id}))
			}
		}
	}
	return out
}

func checkOverlayExplicitSurfaces(path string, m workflowdef.Manifest) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	for _, p := range m.PhaseDefs {
		if strings.TrimSpace(p.CoordinatorSurface) == "" ||
			strings.TrimSpace(p.SurfaceTemplate) == "" ||
			len(p.ModeRefs) == 0 {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("overlay_requires_explicit_surface"),
				fmt.Sprintf("%s: phases[%s]", path, p.ID),
				map[string]any{"phase": p.ID}))
		}
	}
	return out
}

func checkOverlayAttachSessionCreate(path string, m workflowdef.Manifest) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	if m.Attach.Policy == workflowdef.AttachPolicySessionCreate {
		field := "attach.policy"
		if path != "" {
			field = fmt.Sprintf("%s: attach.policy", path)
		}
		out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("attach_session_create_on_overlay"),
			field,
			map[string]any{"policy": string(m.Attach.Policy)}))
	}
	return out
}

func checkInvoke(opts CatalogValidateOptions, path string, m workflowdef.Manifest) []api.ComposeValidationError {
	reg, err := workflowdef.RegistryFromDirs(opts.ProjectDir)
	if err != nil {
		return nil
	}
	var out []api.ComposeValidationError
	for _, p := range m.PhaseDefs {
		if p.InvokeWorkflow == nil {
			continue
		}
		id := p.InvokeWorkflow.WorkflowID
		ver := p.InvokeWorkflow.Version
		child, err := reg.Get(id, ver)
		if err != nil {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("invoke_unresolved"),
				fmt.Sprintf("%s: phases[%s].invoke_workflow", path, p.ID),
				map[string]any{"workflow_id": id, "version": ver}))
			continue
		}
		if workflowdef.ManifestHasInvokeWorkflow(child) {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("invoke_child_has_invoke"),
				fmt.Sprintf("%s: phases[%s].invoke_workflow", path, p.ID),
				map[string]any{"workflow_id": id, "version": ver}))
		}
	}
	return out
}

func checkPrompts(opts CatalogValidateOptions, path string, m workflowdef.Manifest) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	for _, p := range m.PhaseDefs {
		binding, err := workflowdef.ResolveSurfaceBinding(m, p.ID, m.ID, opts.ConfigRoot)
		if err != nil {
			continue
		}
		if tmpl := strings.TrimSpace(binding.SurfaceTemplate); tmpl != "" {
			if !promptExists(opts.ConfigRoot, tmpl) {
				out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("missing_surface_template"),
					fmt.Sprintf("%s: phases[%s].surface_template", path, p.ID),
					map[string]any{"path": tmpl}))
			}
		}
		for _, ref := range binding.ModeRefs {
			modePath := surface.ModeTemplateRef(ref)
			if !promptExists(opts.ConfigRoot, modePath) {
				out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("missing_mode_ref"),
					fmt.Sprintf("%s: phases[%s].mode_refs", path, p.ID),
					map[string]any{"mode_ref": ref, "path": modePath}))
			}
		}
	}
	return out
}

func promptExists(configRoot, rel string) bool {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return false
	}
	rel = strings.TrimPrefix(rel, "agents/")
	candidates := []extpacks.Source{
		extpacks.Bundled(config.PlatformShared.Join(rel)),
		extpacks.Bundled(config.Rel(rel)),
	}
	packs, err := extpacks.DiscoverStock()
	if err == nil {
		for _, dir := range extpacks.KindDirs(packs, "agents") {
			candidates = append(candidates,
				dir.Join("prompts", rel),
				dir.Join(rel),
			)
		}
	} else {
		candidates = append(candidates,
			extpacks.Bundled(config.PlatformPrompts.Join(rel)),
		)
	}
	for _, p := range candidates {
		if _, err := p.Stat(); err == nil {
			return true
		}
	}
	return false
}

// checkKicks resolves on_reenter.inject_kick the way the host does at re-enter:
// the value names a catalog anchor (or its binding's render id), and the binding
// renders a template that exists.
func checkKicks(opts CatalogValidateOptions, path string, m workflowdef.Manifest) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	guidanceDirs := guidanceSearchDirs(opts, path)
	var registry *anchor.Registry
	for _, p := range m.PhaseDefs {
		kick := strings.TrimSpace(p.OnReenter.InjectKick)
		if kick == "" {
			continue
		}
		if registry == nil {
			loaded, err := kickRegistry()
			if err != nil {
				return append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"),
					fmt.Sprintf("%s: phases[%s].on_reenter", path, p.ID),
					map[string]any{"detail": err.Error()}))
			}
			registry = loaded
		}
		if id, ok := kickAnchor(registry, kick); !ok || !renderTemplateExistsAny(guidanceDirs, registry.InformRender(id)) {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("missing_kick"),
				fmt.Sprintf("%s: phases[%s].on_reenter", path, p.ID),
				map[string]any{"kick": kick}))
		}
	}
	for i, inj := range m.Injects {
		render := strings.TrimSpace(inj.Render)
		if render == "" {
			continue
		}
		if !renderTemplateExistsAny(guidanceDirs, render) {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("missing_kick"),
				fmt.Sprintf("%s: injects[%d]", path, i),
				map[string]any{"kick": render}))
		}
	}
	return out
}

func stockGuidanceDirs() []extpacks.Source {
	packs, err := extpacks.DiscoverStock()
	if err != nil {
		return []extpacks.Source{extpacks.Bundled(config.PlatformGuidance)}
	}
	return extpacks.KindDirs(packs, "guidance")
}

// guidanceSearchDirs includes stock and manifest-local guidance.
func guidanceSearchDirs(opts CatalogValidateOptions, manifestPath string) []extpacks.Source {
	dirs := stockGuidanceDirs()
	seen := map[string]bool{}
	for _, d := range dirs {
		seen[d.String()] = true
	}
	add := func(candidates ...extpacks.Source) {
		for _, dir := range candidates {
			if dir.Empty() || seen[dir.String()] {
				continue
			}
			if !dir.IsDir() {
				continue
			}
			seen[dir.String()] = true
			dirs = append(dirs, dir)
		}
	}
	// A manifest root may contain pack or project guidance.
	if manifestPath != "" {
		root := filepath.Dir(filepath.Dir(filepath.Dir(manifestPath)))
		add(extpacks.OnDisk(filepath.Join(root, "guidance")))
		promptFiles := filepath.Join(root, "prompt_files")
		add(extpacks.OnDisk(filepath.Join(promptFiles, "kicks")), extpacks.OnDisk(filepath.Join(promptFiles, "inject")), extpacks.OnDisk(filepath.Join(promptFiles, "guidance")))
	}
	if project := strings.TrimSpace(opts.ProjectDir); project != "" {
		overlay := filepath.Join(project, settingsoverlay.DirName(), "prompt_files")
		add(extpacks.OnDisk(filepath.Join(overlay, "kicks")), extpacks.OnDisk(filepath.Join(overlay, "inject")), extpacks.OnDisk(filepath.Join(overlay, "guidance")))
	}
	return dirs
}

// kickRegistry is the process registry when the host installed one, else the
// effective bindings from the config root.
func kickRegistry() (*anchor.Registry, error) {
	if r := anchor.DefaultRegistry(); r != nil {
		return r, nil
	}
	r, err := anchor.LoadRegistryFromConfigRoot()
	if err != nil {
		return nil, fmt.Errorf("anchor bindings: %w", err)
	}
	return r, nil
}

// kickAnchor accepts a catalog id or a binding render id, as anchor.ParseID does.
func kickAnchor(registry *anchor.Registry, kick string) (anchor.ID, bool) {
	if anchorcatalog.Has(kick) {
		return anchor.ID(kick), true
	}
	if id, ok := registry.LookupRender(kick); ok && anchorcatalog.Has(string(id)) {
		return id, true
	}
	return "", false
}

func renderTemplateExistsAny(dirs []extpacks.Source, render string) bool {
	render = strings.TrimSpace(render)
	if render == "" {
		return false
	}
	for _, dir := range dirs {
		for _, name := range []string{render + ".md", render + ".yaml", render + ".yml"} {
			if _, err := dir.Join(name).Stat(); err == nil {
				return true
			}
		}
	}
	return false
}

func checkReadiness(condReg *conditions.ConditionRegistry, path string, m workflowdef.Manifest) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	for _, p := range m.PhaseDefs {
		if p.HumanApproval == nil {
			continue
		}
		leaf := strings.TrimSpace(p.HumanApproval.Readiness)
		if leaf == "" {
			continue
		}
		if condReg != nil && !condReg.Has(leaf) && !workflowdef.IsKnownGateLeaf(leaf) {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("unknown_readiness"),
				fmt.Sprintf("%s: phases[%s].human_approval.readiness", path, p.ID),
				map[string]any{"leaf": leaf}))
		}
	}
	return out
}

func checkEvidenceKeys(path string, m workflowdef.Manifest) []api.ComposeValidationError {
	known := knownEvidenceKeys()
	var out []api.ComposeValidationError
	for _, p := range m.PhaseDefs {
		for _, id := range collectLeaves(p) {
			if !strings.HasPrefix(id, "evidence_passed:") {
				continue
			}
			key := strings.TrimPrefix(id, "evidence_passed:")
			if _, ok := known[key]; !ok {
				out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("unknown_evidence_key"),
					fmt.Sprintf("%s: phases[%s]", path, p.ID),
					map[string]any{"key": key}))
			}
		}
		if p.ReviewLoop != nil {
			key := strings.TrimSpace(p.ReviewLoop.EvidenceKey)
			if key != "" {
				if _, ok := known[key]; !ok {
					out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("unknown_evidence_key"),
						fmt.Sprintf("%s: phases[%s].review_loop", path, p.ID),
						map[string]any{"key": key}))
				}
			}
		}
	}
	return out
}

func knownEvidenceKeys() map[string]struct{} {
	out := map[string]struct{}{}
	for _, k := range evidence.AllGateTypes() {
		out[string(k)] = struct{}{}
	}
	return out
}

func checkSealedClosure(path string, m workflowdef.Manifest) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	if strings.TrimSpace(m.ArchiveDir) == "" {
		return append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"),
			path, map[string]any{"detail": fmt.Sprintf("%s: sealed archive directory not set", path)}))
	}
	if _, err := os.Stat(m.ArchiveDir); err != nil {
		return append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"),
			path, map[string]any{"detail": fmt.Sprintf("%s: sealed archive directory %q does not exist: %v", path, m.ArchiveDir, err)}))
	}
	for _, inj := range m.Injects {
		render := strings.TrimSpace(inj.Render)
		if render == "" {
			continue
		}
		candidateMD := filepath.Join(m.ArchiveDir, "guidance", render+".md")
		candidateYAML := filepath.Join(m.ArchiveDir, "guidance", render+".yaml")
		_, errMD := os.Stat(candidateMD)
		_, errYAML := os.Stat(candidateYAML)
		if errMD != nil && errYAML != nil {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"),
				path, map[string]any{"detail": fmt.Sprintf("%s: sealed workflow missing prompt %q in archive", path, render)}))
		}
	}
	return out
}

func checkReviewLoopBundled(opts CatalogValidateOptions, path string, m workflowdef.Manifest) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	for _, p := range m.PhaseDefs {
		if p.ReviewLoop == nil {
			continue
		}
		key := strings.TrimSpace(p.ReviewLoop.EvidenceKey)
		fixture := filepath.Join(opts.ConfigRoot, "internal", "workflow", "testdata", "review_loop", key+".json")
		if _, err := os.Stat(fixture); err != nil {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("review_loop_incomplete"),
				fmt.Sprintf("%s: phases[%s].review_loop", path, p.ID),
				map[string]any{"key": key}))
		}
	}
	return out
}

func validateBundledExtras(opts CatalogValidateOptions, condReg *conditions.ConditionRegistry, reg *workflowdef.Registry) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	out = append(out, checkAmbient(opts, reg)...)
	out = append(out, checkSpawnSubset(opts, reg)...)
	out = append(out, checkGateFeedbackCoverage(opts, reg)...)
	out = append(out, checkComposePolicy(opts, reg)...)
	out = append(out, checkTemplates(opts, condReg)...)
	return out
}

func checkAmbient(opts CatalogValidateOptions, reg *workflowdef.Registry) []api.ComposeValidationError {
	ref, err := workflowdef.LoadRegistryConfig(extpacks.Bundled(config.PlatformFlows))
	if err != nil {
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"), "registry",
			map[string]any{"detail": err.Error()})}
	}
	if _, err := reg.Get(ref.ID, ref.Version); err != nil {
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("ambient_unresolved"), "registry",
			map[string]any{"workflow_id": ref.ID, "version": ref.Version})}
	}
	return nil
}

func checkSpawnSubset(_ CatalogValidateOptions, reg *workflowdef.Registry) []api.ComposeValidationError {
	impl, err := reg.Get("implement", "1.0.0")
	if err != nil {
		return nil
	}
	allowed := map[string]struct{}{}
	for _, a := range impl.AllowedAgents {
		allowed[a] = struct{}{}
	}
	var out []api.ComposeValidationError
	for _, a := range spawn.AmbientAllowedAgents() {
		if _, ok := allowed[a]; !ok {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("spawn_not_subset"), "implement-default-spawn",
				map[string]any{"agent": a}))
		}
	}
	return out
}

// checkSurfaceProgress validates progress-gated surfaces.
func checkSurfaceProgress(_ CatalogValidateOptions) []api.ComposeValidationError {
	surfaces, err := surface.CompileToolPlans(1)
	if err != nil {
		return nil
	}
	var out []api.ComposeValidationError
	for id, plan := range surfaces {
		if progress.SurfaceMissingUpdateProgress(plan.AddressableNames()) {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("surface_missing_update_progress"),
				"coordinator-surfaces.yaml",
				map[string]any{"surface": id}))
		}
	}
	return out
}

func checkGateFeedbackCoverage(opts CatalogValidateOptions, reg *workflowdef.Registry) []api.ComposeValidationError {
	fbCat, err := feedback.LoadGateFeedbackCatalog()
	if err != nil {
		return nil
	}
	// Feedback closure covers ambient and primary catalog workflows.
	scoped := map[string]bool{"implement": true, "plan": true, "options": true}
	var out []api.ComposeValidationError
	for key, m := range reg.All() {
		if !scoped[m.ID] {
			continue
		}
		for _, p := range m.PhaseDefs {
			for _, leaf := range collectLeaves(p) {
				if !workflowdef.IsKnownGateLeaf(leaf) && !workflowdef.IsKnownCompleteWhen(leaf) {
					continue
				}
				if !fbCat.Has(leaf) {
					out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("missing_gate_feedback"),
						key+": "+p.ID,
						map[string]any{"leaf": leaf}))
				}
			}
		}
	}
	return out
}

func checkComposePolicy(opts CatalogValidateOptions, reg *workflowdef.Registry) []api.ComposeValidationError {
	policy, err := workflow.LoadComposePolicy()
	if err != nil {
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"), "compose-policy",
			map[string]any{"detail": err.Error()})}
	}
	var out []api.ComposeValidationError
	for key, m := range reg.All() {
		extends := strings.TrimSpace(m.Extends)
		if extends == "" {
			// Posture/extends policy applies to composed overlays, not root recipes.
			continue
		}
		errs := policy.Apply(workflow.ComposePolicyInput{
			Raw:         m,
			Effective:   m,
			ExtendsRef:  extends,
			PersistTier: true,
		})
		for _, e := range errs {
			e.Field = key + ": " + e.Field
			out = append(out, e)
		}
	}
	return out
}

func checkTemplates(opts CatalogValidateOptions, condReg *conditions.ConditionRegistry) []api.ComposeValidationError {
	dir := filepath.Join(opts.ConfigRoot, "config", "packs", "painted-wolf", "platform", "workflows", "_templates")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []api.ComposeValidationError
	for _, ent := range ents {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".yaml") {
			continue
		}
		// Template files are compose wrappers; load as manifests when possible.
		p := filepath.Join(dir, ent.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		m, err := workflowdef.ParseManifestYAML(data)
		if err != nil {
			// Templates may not be full manifests — skip unparseable.
			continue
		}
		for _, d := range workflow.ValidateComposeManifest(condReg, shippedObligationSpecs(), m) {
			d.Field = p + ": " + d.Field
			out = append(out, d)
		}
	}
	return out
}

func collectLeaves(p workflowdef.PhaseDef) []string {
	var exprs []string
	if cw := strings.TrimSpace(p.CompleteWhen); cw != "" && cw != workflowdef.CompleteWhenGatesSatisfied {
		exprs = append(exprs, cw)
	}
	if ew := strings.TrimSpace(p.EntryWhen); ew != "" {
		exprs = append(exprs, ew)
	}
	for _, g := range p.Gates {
		if g = strings.TrimSpace(g); g != "" {
			exprs = append(exprs, g)
		}
	}
	seen := map[string]struct{}{}
	var out []string
	for _, e := range exprs {
		seen[e] = struct{}{}
	}
	for id := range seen {
		out = append(out, id)
	}
	return out
}
