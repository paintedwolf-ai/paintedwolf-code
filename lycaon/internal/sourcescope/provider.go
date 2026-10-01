package sourcescope

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/scan/rules"
)

// Provider combines device policy with trusted project declarations.
type Provider struct {
	config Config
	floor  []string
	// projectApplies checks scan-config trust; nil leaves project declarations unapplied.
	projectApplies func(ctx context.Context, root string) bool

	mu      sync.Mutex
	warned  map[string]struct{}
	declare func(root string) (Declared, error)
}

// NewProvider wires a provider over the bundled excludes floor.
func NewProvider(cfg Config, projectApplies func(ctx context.Context, root string) bool) (*Provider, error) {
	excludes, err := rules.LoadPathExcludes()
	if err != nil {
		return nil, err
	}
	return &Provider{
		config: cfg, floor: excludes.Patterns(), projectApplies: projectApplies,
		warned: make(map[string]struct{}), declare: LoadDeclared,
	}, nil
}

// Capture applies the exclusion floor, project rules, and capture budgets.
func (p *Provider) Capture(ctx context.Context, root string) *Scope {
	if p == nil {
		return New(root, Options{})
	}
	declared := p.declared(ctx, root)
	plane := p.config.Capture
	plane.Budgets = applyDeclaredBudgets(plane.Budgets, declared.Capture)
	return New(root, Options{Plane: plane, Floor: p.floor, Declared: declared})
}

// Catalog applies budgets and traversal priority without exclusions.
func (p *Provider) Catalog(ctx context.Context, root string) *Scope {
	if p == nil {
		return New(root, Options{})
	}
	declared := p.declared(ctx, root)
	plane := p.config.Catalog
	plane.IgnoreFiles = false
	plane.Budgets = applyDeclaredBudgets(plane.Budgets, declared.Catalog)
	return New(root, Options{Plane: plane})
}

func (p *Provider) declared(ctx context.Context, root string) Declared {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." || p.projectApplies == nil || !p.projectApplies(ctx, root) {
		return Declared{}
	}
	declared, err := p.declare(root)
	if err != nil {
		p.warnOnce(ctx, root, err)
		return Declared{}
	}
	return declared
}

// warnOnce logs an unusable overlay once per root rather than per observation.
func (p *Provider) warnOnce(ctx context.Context, root string, err error) {
	p.mu.Lock()
	_, seen := p.warned[root]
	if !seen {
		p.warned[root] = struct{}{}
	}
	p.mu.Unlock()
	if !seen {
		slog.WarnContext(ctx, "project source scope not applied", "root", root, "error", err)
	}
}
