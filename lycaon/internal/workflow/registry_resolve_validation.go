package workflow

import (
	"strings"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
)

// Bundled entries are already valid without overlays. If their effective form
// breaks, reject the nearest overriding ancestor that changed that form.
func (g *manifestGraph) overridingAncestor(key string) string {
	seen := map[string]bool{}
	for key != "" && !seen[key] {
		seen[key] = true
		candidate, ok := g.candidates[key]
		if !ok {
			return ""
		}
		if candidate.scope != string(api.WorkflowScopeBundled) {
			return key
		}
		key = strings.TrimSpace(candidate.manifest.Extends)
	}
	return ""
}

func (g *manifestGraph) rejectEffectiveManifest(key string, diags []api.ComposeValidationError) {
	rejected := g.overridingAncestor(key)
	if rejected == "" {
		rejected = key
	}
	if rejected != key {
		for i := range diags {
			diags[i].Field = g.candidates[rejected].path + ": " + key + ": " + diags[i].Field
		}
	}
	g.failures[rejected] = diags
}

func (g *manifestGraph) validateInvocations() {
	// Restore bundled contracts before judging callers that may become valid
	// when an incompatible target override is removed.
	for _, bundled := range []bool{true, false} {
		for _, key := range g.keys {
			if (g.candidates[key].scope == string(api.WorkflowScopeBundled)) == bundled {
				g.validateManifestInvocations(key)
			}
		}
		if len(g.failures) > 0 {
			return
		}
	}
}

func (g *manifestGraph) validateManifestInvocations(key string) {
	parent := g.resolved[key]
	for _, phase := range parent.PhaseDefs {
		err := workflowdef.ValidateWorkflowInvocation(parent, phase, g.resolved)
		if err == nil {
			continue
		}
		rejected := g.overridingAncestor(key)
		if rejected == "" && phase.InvokeWorkflow != nil {
			target := workflowdef.ManifestKey(phase.InvokeWorkflow.WorkflowID, phase.InvokeWorkflow.Version)
			rejected = g.overridingAncestor(target)
		}
		if rejected == "" {
			rejected = key
		}
		path := g.candidates[rejected].path
		g.failures[rejected] = append(g.failures[rejected], workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"), path,
			map[string]any{"detail": err.Error()}))
	}
}
