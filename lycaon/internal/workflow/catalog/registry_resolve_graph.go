package catalog

import (
	"sort"
	"strings"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

type manifestCandidate struct {
	manifest workflowdef.Manifest
	path     string
	scope    string
}

// Each identity keeps its precedence stack so rejecting an override exposes the
// lower tier. Dependents are resolved again against that effective catalog.
type manifestCandidates map[string][]manifestCandidate

func (c manifestCandidates) add(candidate manifestCandidate) {
	key := workflowdef.ManifestKey(candidate.manifest.ID, candidate.manifest.Version)
	c[key] = append(c[key], candidate)
}

func (c manifestCandidates) resolve() (map[string]workflowdef.Manifest, map[string]string, []api.ExcludedWorkflow) {
	var excluded []api.ExcludedWorkflow
	for {
		graph := newManifestGraph(c)
		for _, key := range graph.keys {
			graph.visit(key)
		}
		if len(graph.failures) == 0 {
			graph.validateInvocations()
		}
		if len(graph.failures) == 0 {
			return graph.resolved, graph.scopes, excluded
		}
		for _, key := range graph.keys {
			diags, failed := graph.failures[key]
			if !failed {
				continue
			}
			stack := c[key]
			candidate := stack[len(stack)-1]
			excluded = append(excluded, newExcludedWorkflow(candidate.manifest.ID, candidate.manifest.Version, candidate.path, diags))
			if len(stack) == 1 {
				delete(c, key)
			} else {
				c[key] = stack[:len(stack)-1]
			}
		}
	}
}

type manifestGraph struct {
	candidates map[string]manifestCandidate
	raw        map[string]workflowdef.Manifest
	keys       []string
	visiting   []string
	visited    map[string]bool
	resolved   map[string]workflowdef.Manifest
	scopes     map[string]string
	failures   map[string][]api.ComposeValidationError
}

func newManifestGraph(c manifestCandidates) *manifestGraph {
	g := &manifestGraph{
		candidates: map[string]manifestCandidate{}, raw: map[string]workflowdef.Manifest{},
		visited: map[string]bool{}, resolved: map[string]workflowdef.Manifest{},
		scopes: map[string]string{}, failures: map[string][]api.ComposeValidationError{},
	}
	for key, stack := range c {
		candidate := stack[len(stack)-1]
		g.candidates[key] = candidate
		g.raw[key] = candidate.manifest
		g.keys = append(g.keys, key)
	}
	sort.Strings(g.keys)
	return g
}

// Visit validates parents first. A rejected dependency blocks its children for
// this pass without rejecting them; the next pass uses the parent's lower tier.
func (g *manifestGraph) visit(key string) bool {
	if g.visited[key] {
		_, ok := g.resolved[key]
		return ok
	}
	for i, active := range g.visiting {
		if active == key {
			g.rejectCycle(g.visiting[i:])
			return false
		}
	}
	candidate := g.candidates[key]
	g.visiting = append(g.visiting, key)
	defer func() { g.visiting = g.visiting[:len(g.visiting)-1]; g.visited[key] = true }()
	parent := strings.TrimSpace(candidate.manifest.Extends)
	if _, exists := g.candidates[parent]; parent != "" && exists && !g.visit(parent) {
		return false
	}
	effective, err := workflowdef.ResolveManifestChain(candidate.manifest, g.raw)
	if err != nil {
		g.failures[key] = extendsChainErrorWithPrefix(candidate.path, err)
		return false
	}
	if diags := validateResolvedManifest(candidate.path, effective); len(diags) > 0 {
		g.rejectEffectiveManifest(key, diags)
		return false
	}

	g.resolved[key] = effective
	g.scopes[key] = candidate.scope
	return true
}

func (g *manifestGraph) rejectCycle(keys []string) {
	for _, key := range keys {
		candidate := g.candidates[key]
		// The bundled graph was checked before overlays. A cycle necessarily has an
		// overlay member; removing those members restores its trusted lower tier.
		if candidate.scope == string(api.WorkflowScopeBundled) {
			continue
		}
		err := &workflowdef.ExtendsError{Kind: workflowdef.ExtendsErrorCycle, Ref: key,
			ManifestID: candidate.manifest.ID, ManifestVersion: candidate.manifest.Version}
		g.failures[key] = extendsChainErrorWithPrefix(candidate.path, err)
	}
}
