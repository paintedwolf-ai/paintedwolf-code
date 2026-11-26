package promptrender

import (
	"path"
	"sync"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
)

// renderInputs knows which root variables a template and the templates it
// includes read, so a render is keyed by the inputs that can change its bytes.
type renderInputs struct {
	catalog   *extpacks.EffectiveCatalog
	templates map[string]string
	mu        sync.Mutex
	memo      map[string]inputRoots
}

type inputRoots struct {
	roots map[string]bool
	ok    bool
}

// alwaysKeyedInputs are read outside the template: the render recipient the
// condition ledger attributes, and the cap RenderKick derives sizing hints
// from.
var alwaysKeyedInputs = []string{"is_coordinator", "max_in_flight"}

var stockRenderInputsMemo sync.Map

// stockRenderInputs shares one index per catalog revision.
func stockRenderInputs(s *Stock) *renderInputs {
	if cached, ok := stockRenderInputsMemo.Load(s.Catalog.Revision); ok {
		return cached.(*renderInputs)
	}
	in := newRenderInputs(s.Catalog)
	actual, _ := stockRenderInputsMemo.LoadOrStore(s.Catalog.Revision, in)
	return actual.(*renderInputs)
}

// overlayRenderInputs indexes the catalog that renders for overlay.
func overlayRenderInputs(s *Stock, overlay hoststate.PackOverlay) *renderInputs {
	if overlay.Catalog == nil {
		return stockRenderInputs(s)
	}
	if cached, ok := stockRenderInputsMemo.Load(overlay.Catalog.Revision); ok {
		return cached.(*renderInputs)
	}
	in := newRenderInputs(overlay.Catalog)
	actual, _ := stockRenderInputsMemo.LoadOrStore(overlay.Catalog.Revision, in)
	return actual.(*renderInputs)
}

func newRenderInputs(catalog *extpacks.EffectiveCatalog) *renderInputs {
	templates, err := interactions.TemplateUnits(catalog)
	if err != nil {
		templates = nil
	}
	return &renderInputs{catalog: catalog, templates: templates, memo: map[string]inputRoots{}}
}

// project keeps the values of full that refs can read, or returns full when
// any ref's inputs cannot be resolved statically.
func (r *renderInputs) project(full map[string]any, refs ...string) map[string]any {
	roots := map[string]bool{}
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		in := r.of(ref)
		if !in.ok {
			return full
		}
		for name := range in.roots {
			roots[name] = true
		}
	}
	out := make(map[string]any, len(roots)+len(alwaysKeyedInputs))
	for name := range roots {
		if v, ok := full[name]; ok {
			out[name] = v
		}
	}
	for _, name := range alwaysKeyedInputs {
		if v, ok := full[name]; ok {
			out[name] = v
		}
	}
	return out
}

func (r *renderInputs) of(ref string) inputRoots {
	if r == nil || r.templates == nil {
		return inputRoots{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if in, ok := r.memo[ref]; ok {
		return in
	}
	roots := map[string]bool{}
	ok := r.collect(ref, roots, map[string]bool{})
	in := inputRoots{roots: roots, ok: ok}
	r.memo[ref] = in
	return in
}

func (r *renderInputs) collect(ref string, roots, visited map[string]bool) bool {
	id, ok := interactions.TemplateIdentity(r.catalog, r.templates, ref)
	if !ok {
		return false
	}
	if visited[id] {
		return true
	}
	visited[id] = true
	source, ok := r.templates[id]
	if !ok {
		return false
	}
	analysis, err := pongoplain.Inspect(source, pongoplain.Composed)
	if err != nil {
		return false
	}
	for _, selector := range pongoplain.VariableReferences(source) {
		roots[selectorRoot(selector)] = true
	}
	for _, dep := range analysis.Dependencies {
		if path.Ext(dep) == "" {
			dep += ".md"
		}
		if !r.collect(dep, roots, visited) {
			return false
		}
	}
	return true
}

func selectorRoot(selector string) string {
	for i := 0; i < len(selector); i++ {
		if selector[i] == '.' || selector[i] == ' ' {
			return selector[:i]
		}
	}
	return selector
}
