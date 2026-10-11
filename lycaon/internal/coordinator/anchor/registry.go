package anchor

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"gopkg.in/yaml.v3"
)

// Registry indexes Binding docs by Anchor (inform + block/transform).
type Registry struct {
	mu sync.RWMutex

	byAnchor map[ID][]*Binding
	// informPrimary is the builtin inform Binding per Anchor (Emit resolution).
	informPrimary map[ID]*Binding
	// renderToAnchor maps Binding.render stem → Anchor.
	renderToAnchor map[string]ID

	loader *oar.Loader
}

var (
	defaultRegMu sync.RWMutex
	defaultReg   *Registry
)

// SetDefaultRegistry installs the process-wide Binding registry (Emit / ParseID).
func SetDefaultRegistry(r *Registry) {
	defaultRegMu.Lock()
	defaultReg = r
	defaultRegMu.Unlock()
}

// DefaultRegistry returns the process-wide Binding registry.
func DefaultRegistry() *Registry {
	defaultRegMu.RLock()
	defer defaultRegMu.RUnlock()
	return defaultReg
}

// RegistryFor resolves the session or device registry.
func RegistryFor(ctx context.Context, sessionID string) *Registry {
	anchorsForMu.RLock()
	var fn AnchorsForFunc
	if anchorsFor != nil {
		fn = anchorsFor.resolve
	}
	anchorsForMu.RUnlock()
	if fn != nil {
		if r := fn(ctx, strings.TrimSpace(sessionID)); r != nil {
			return r
		}
	}
	return DefaultRegistry()
}

// LoadRegistry loads inform bindings from bindingsDir.
func LoadRegistry(bindingsDir extpacks.Source, schemaDir string) (*Registry, error) {
	if !anchorcatalog.Loaded() {
		if err := anchorcatalog.InstallBundled(); err != nil {
			return nil, fmt.Errorf("anchor catalog required before LoadRegistry: %w", err)
		}
	}
	loader, err := oar.NewLoader(schemaDir)
	if err != nil {
		return nil, fmt.Errorf("oar loader: %w", err)
	}
	r := &Registry{
		byAnchor:       make(map[ID][]*Binding),
		informPrimary:  make(map[ID]*Binding),
		renderToAnchor: make(map[string]ID),
		loader:         loader,
	}
	if err := r.loadInformDir(bindingsDir); err != nil {
		return nil, err
	}
	return r, nil
}

// LoadRegistryFromConfigRoot loads effective anchor bindings.
func LoadRegistryFromConfigRoot() (*Registry, error) {
	catalog, err := extpacks.CatalogForConsumers()
	if err != nil {
		return nil, err
	}
	return LoadRegistryFromConfigRootWithCatalog(catalog)
}

// LoadRegistryFromConfigRootWithCatalog compiles captured binding units.
func LoadRegistryFromConfigRootWithCatalog(catalog *extpacks.EffectiveCatalog) (*Registry, error) {
	if catalog == nil {
		return nil, fmt.Errorf("anchor registry: effective catalog required")
	}
	if err := anchorcatalog.InstallBundled(); err != nil {
		return nil, err
	}
	schemaDir := configlayout.SchemasDir(configlayout.FindModuleRoot())
	loader, err := oar.NewLoader(schemaDir)
	if err != nil {
		return nil, err
	}
	r := &Registry{
		byAnchor:       make(map[ID][]*Binding),
		informPrimary:  make(map[ID]*Binding),
		renderToAnchor: make(map[string]ID),
		loader:         loader,
	}
	for _, id := range catalog.LoadedUnitIDs() {
		content, _, ok := catalog.UnitContent(id)
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(id, "host/bindings/"):
			var doc bindingDoc
			if err := yaml.Unmarshal(content, &doc); err != nil {
				return nil, fmt.Errorf("%s: %w", id, err)
			}
			b, err := r.parseBindingDoc(doc)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", id, err)
			}
			r.add(b)
		case workflowManifestUnit(id), archivedManifestUnit(id):
			if err := r.loadWorkflowInjects(id, content); err != nil {
				return nil, err
			}
		}
	}
	return r, nil
}

func workflowManifestUnit(id string) bool {
	rel, ok := strings.CutPrefix(strings.TrimSpace(id), "workflows/")
	return ok && rel != "" && !strings.HasPrefix(rel, "_") && !strings.Contains(rel, "/")
}

func archivedManifestUnit(id string) bool {
	_, rest, ok := extpacks.SplitArchiveUnitID(id)
	return ok && rest == "workflow"
}

type bindingDoc struct {
	On         string            `yaml:"on"`
	Selector   Selector          `yaml:"selector"`
	Effect     string            `yaml:"effect"`
	When       string            `yaml:"when"`
	Render     string            `yaml:"render"`
	Tier       string            `yaml:"tier"`
	Invariants BindingInvariants `yaml:"invariants"`
}

// loadInformDir loads a bare bindings directory — a test fixture or overlay,
// not pack content, so no catalog gate applies.
func (r *Registry) loadInformDir(dir extpacks.Source) error {
	if dir.Empty() {
		return fmt.Errorf("anchor: empty bindings dir")
	}
	entries, err := dir.List()
	if err != nil {
		return fmt.Errorf("read bindings dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		raw, err := dir.Join(e.Name()).Read()
		if err != nil {
			return err
		}
		var doc bindingDoc
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
		b, err := r.parseBindingDoc(doc)
		if err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
		r.add(b)
	}
	return nil
}

func (r *Registry) parseBindingDoc(doc bindingDoc) (*Binding, error) {
	on := ID(strings.TrimSpace(doc.On))
	if on == "" {
		return nil, fmt.Errorf("missing on")
	}
	if err := anchorcatalog.Require(string(on)); err != nil {
		return nil, err
	}
	effect := strings.TrimSpace(doc.Effect)
	if effect == "" {
		return nil, fmt.Errorf("missing effect")
	}
	b := &Binding{
		On:         on,
		Selector:   doc.Selector,
		Effect:     effect,
		When:       strings.TrimSpace(doc.When),
		Render:     strings.TrimSpace(doc.Render),
		Tier:       strings.TrimSpace(doc.Tier),
		Invariants: doc.Invariants,
	}
	if b.Tier == "" {
		b.Tier = "builtin"
	}
	if b.IsInform() && b.Render == "" {
		return nil, fmt.Errorf("inform Binding requires render")
	}
	if r.loader != nil && b.When != "" {
		if err := r.loader.ValidateCondition(b.When); err != nil {
			return nil, fmt.Errorf("when: %w", err)
		}
	}
	return b, nil
}

func (r *Registry) add(b *Binding) {
	if r == nil || b == nil {
		return
	}
	r.byAnchor[b.On] = append(r.byAnchor[b.On], b)
	if b.IsInform() {
		// The primary index contains device bindings only.
		if !strings.EqualFold(strings.TrimSpace(b.Tier), "workflow") {
			if existing, ok := r.informPrimary[b.On]; !ok || (existing.Tier != "builtin" && b.Tier == "builtin") {
				r.informPrimary[b.On] = b
			}
		}
		if b.Render != "" {
			r.renderToAnchor[b.Render] = b.On
		}
	}
}

// Inform returns the primary inform Binding for id (builtin preferred).
func (r *Registry) Inform(id ID) (*Binding, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.informPrimary[id]
	return b, ok
}

// InformRender returns the Binding.render stem for id.
func (r *Registry) InformRender(id ID) string {
	b, ok := r.Inform(id)
	if !ok || b == nil {
		return ""
	}
	return b.Render
}

// ResolveInform picks the most specific matching binding.
// Workflow bindings precede device bindings.
// If ctx specifies a workflow without a workflow version, ResolveInform fails closed with ErrWorkflowVersionMissing.
func (r *Registry) ResolveInform(id ID, ctx MatchContext) (*Binding, error) {
	if r == nil {
		return nil, nil
	}
	if strings.TrimSpace(ctx.Workflow) != "" && strings.TrimSpace(ctx.WorkflowVersion) == "" {
		return nil, ErrWorkflowVersionMissing
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var workflowHits, builtinHits []*Binding
	for _, b := range r.byAnchor[id] {
		if b == nil || !b.IsInform() {
			continue
		}
		if !b.Matches(ctx) {
			continue
		}
		if b.IsWorkflowTier() {
			workflowHits = append(workflowHits, b)
		} else {
			builtinHits = append(builtinHits, b)
		}
	}
	if pick := pickMostSpecificInform(workflowHits); pick != nil {
		return pick, nil
	}
	if pick := pickMostSpecificInform(builtinHits); pick != nil {
		return pick, nil
	}
	if ctx == (MatchContext{}) {
		if b, ok := r.informPrimary[id]; ok && b != nil && !strings.EqualFold(strings.TrimSpace(b.Tier), "workflow") {
			return b, nil
		}
	}
	return nil, nil
}

func (r *Registry) checkWorkflowDuplicate(b *Binding) error {
	if b == nil || !b.IsWorkflowTier() {
		return nil
	}
	wf := ""
	if b.Selector.Workflow != nil {
		wf = strings.TrimSpace(*b.Selector.Workflow)
	}
	phase := ""
	if b.Selector.Phase != nil {
		phase = strings.TrimSpace(*b.Selector.Phase)
	}
	for _, existing := range r.byAnchor[b.On] {
		if existing == nil || !existing.IsWorkflowTier() {
			continue
		}
		exWf := ""
		if existing.Selector.Workflow != nil {
			exWf = strings.TrimSpace(*existing.Selector.Workflow)
		}
		exPhase := ""
		if existing.Selector.Phase != nil {
			exPhase = strings.TrimSpace(*existing.Selector.Phase)
		}
		if strings.TrimSpace(existing.Selector.Surface) == strings.TrimSpace(b.Selector.Surface) &&
			exPhase == phase &&
			exWf == wf &&
			strings.TrimSpace(existing.workflowVersion) == strings.TrimSpace(b.workflowVersion) {
			return fmt.Errorf("duplicate workflow anchor binding on %s: workflow=%q, version=%q, phase=%q, surface=%q",
				b.On, wf, b.workflowVersion, phase, b.Selector.Surface)
		}
	}
	return nil
}

func pickMostSpecificInform(cands []*Binding) *Binding {
	if len(cands) == 0 {
		return nil
	}
	best := cands[0]
	bestScore := selectorSpecificity(best.Selector)
	for _, b := range cands[1:] {
		if s := selectorSpecificity(b.Selector); s > bestScore {
			best, bestScore = b, s
		}
	}
	return best
}

func selectorSpecificity(s Selector) int {
	n := 0
	if s.Workflow != nil && strings.TrimSpace(*s.Workflow) != "" {
		n += 4
	}
	if s.Phase != nil && strings.TrimSpace(*s.Phase) != "" {
		n += 2
	}
	if strings.TrimSpace(s.Surface) != "" {
		n++
	}
	return n
}

// IndexBinding adds a Binding (workflow-tier injects or tests). Thread-safe.
func (r *Registry) IndexBinding(b *Binding) {
	if r == nil || b == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.add(b)
}

// WhenMatches evaluates Binding.when against gc (empty/nil ⇒ true).
func (r *Registry) WhenMatches(b *Binding, gc *oar.GuardContext) bool {
	if b == nil {
		return true
	}
	ok, err := oar.EvaluateCondition(b.When, gc)
	return err == nil && ok
}

// BindingsOn returns all Bindings indexed under anchor (inform + block).
func (r *Registry) BindingsOn(id ID) []*Binding {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := append([]*Binding(nil), r.byAnchor[id]...)
	return out
}

// InformAnchors returns every Anchor that has an inform Binding.
func (r *Registry) InformAnchors() []ID {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ID, 0, len(r.informPrimary))
	for id := range r.informPrimary {
		out = append(out, id)
	}
	return out
}

// LookupRender maps a Binding.render identifier to its Anchor.
func (r *Registry) LookupRender(s string) (ID, bool) {
	if r == nil {
		return "", false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if id, ok := r.renderToAnchor[s]; ok {
		return id, true
	}
	return "", false
}

// AllInform returns every primary inform Binding.
func (r *Registry) AllInform() []*Binding {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Binding, 0, len(r.informPrimary))
	for _, b := range r.informPrimary {
		out = append(out, b)
	}
	return out
}
