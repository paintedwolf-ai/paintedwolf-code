package sourcetree

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

type PreparationError struct{ Err error }

func (e *PreparationError) Error() string { return e.Err.Error() }
func (e *PreparationError) Unwrap() error { return e.Err }

type directoryDemand struct {
	root      sourcecatalog.Root
	path      string
	recursive bool
}

// A complete head wins; unfinished updates can use the last complete structure.
func (v *View) openPreparedNavigation(ctx context.Context, root sourcecatalog.Root, rules *Rules) (*sourcecatalog.Navigation, error) {
	navigation, err := v.catalog.OpenNavigation(ctx, v.scope.Project, root)
	if err != nil {
		return nil, err
	}
	projection := Projection{Root: root.ID, Navigation: navigation, Rules: rules}
	pending, err := projection.unresolved(ctx, ".")
	if err != nil {
		_ = navigation.Close()
		return nil, err
	}
	if pending == 0 {
		return navigation, nil
	}
	completed, err := v.catalog.OpenCompletedNavigation(ctx, v.scope.Project, root)
	if errors.Is(err, pagedview.ErrPreparing) {
		return navigation, nil
	}
	if err != nil {
		_ = navigation.Close()
		return nil, err
	}
	applicable, err := rulesAvailableInNavigation(ctx, root.ID, rules, completed)
	if err != nil {
		_ = navigation.Close()
		_ = completed.Close()
		return nil, err
	}
	if !applicable {
		_ = completed.Close()
		return navigation, nil
	}
	_ = navigation.Close()
	if err := v.catalog.RequestCoverage(ctx, v.scope.Project, root, "."); err != nil {
		_ = completed.Close()
		return nil, err
	}
	return completed, nil
}

func rulesAvailableInNavigation(ctx context.Context, root string, rules *Rules, navigation *sourcecatalog.Navigation) (bool, error) {
	var required []string
	visitRules(rules.root, func(value ruleValue) {
		if value.address.Root == root && value.address.Path != "." && (value.open || value.hasRecursive && value.recursive) {
			required = append(required, value.address.Path)
		}
	})
	for _, path := range required {
		entry, err := navigation.Entry(ctx, path)
		if errors.Is(err, pagedview.ErrMissing) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !entry.IsDir {
			return false, nil
		}
	}
	return true, nil
}

func (v *View) coverageDemand(ctx context.Context) ([]directoryDemand, Rules, error) {
	v.mu.Lock()
	rules := v.rules.Clone()
	v.mu.Unlock()
	demands := make([]directoryDemand, 0, sourcecatalog.DirectoryBatchLimit)
	for _, root := range v.rootOrder {
		nav, err := v.openPreparedNavigation(ctx, root.Root, &rules)
		if err != nil {
			return nil, rules, err
		}
		projection := Projection{Root: root.ID, Navigation: nav, Rules: &rules}
		err = projection.collectDemand(ctx, ".", root.Root, &demands)
		_ = nav.Close()
		if err != nil {
			return nil, rules, err
		}
		if len(demands) >= sourcecatalog.DirectoryBatchLimit {
			break
		}
	}
	return demands, rules, nil
}

func (p *Projection) collectDemand(ctx context.Context, dir string, root sourcecatalog.Root, demands *[]directoryDemand) error {
	if len(*demands) >= sourcecatalog.DirectoryBatchLimit {
		return nil
	}
	rule, err := p.rule(ctx, dir)
	if err != nil || !rule.Open {
		return err
	}
	state, err := p.Navigation.State(ctx, dir)
	if err != nil || state.Failure != "" {
		return err
	}
	if rule.Recursive {
		pending, err := p.unresolved(ctx, dir)
		if err != nil {
			return err
		}
		if pending > 0 {
			*demands = append(*demands, directoryDemand{root: root, path: dir, recursive: true})
		}
		return nil
	}
	if !state.Complete {
		*demands = append(*demands, directoryDemand{root: root, path: dir})
		return nil
	}
	for _, child := range p.Rules.Branches(Address{Root: p.Root, Path: dir}) {
		if err := p.collectDemand(ctx, child, root, demands); err != nil && !errors.Is(err, pagedview.ErrMissing) {
			return err
		}
	}
	return nil
}

func (v *View) resetPreparationLocked() {
	if v.prepareCancel != nil {
		v.prepareCancel()
	}
	v.prepareDone, v.prepareCancel, v.prepareError = nil, nil, nil
}

//nolint:contextcheck,nolintlint // Shared preparation follows the retained view lifetime.
func (v *View) Prepare() <-chan struct{} {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.prepareDone != nil {
		return v.prepareDone
	}
	done := make(chan struct{})
	v.prepareDone = done
	if v.closed {
		close(done)
		return done
	}
	ctx, cancel := context.WithCancel(v.ctx)
	v.prepareCancel = cancel
	v.workers.Add(1)
	go func() {
		defer v.workers.Done()
		defer close(done)
		defer cancel()
		err := v.completeCoverage(ctx)
		v.mu.Lock()
		current := v.prepareDone == done
		if current {
			v.prepareError = err
			v.prepareDone = nil
			v.prepareCancel = nil
		}
		v.mu.Unlock()
		if current && v.notify != nil {
			v.notify()
		}
	}()
	return done
}

func (v *View) completeCoverage(ctx context.Context) error {
	for {
		demands, rules, err := v.coverageDemand(ctx)
		if err != nil || len(demands) == 0 {
			return err
		}
		for _, demand := range demands {
			if demand.recursive {
				if err := v.awaitDisclosedSubtree(ctx, demand, &rules); err != nil {
					return err
				}
			} else {
				if err := v.catalog.ObserveDirectories(ctx, v.scope.Project, demand.root, []string{demand.path}, backgroundwork.PriorityInteractive); err != nil {
					return err
				}
			}
		}
	}
}

// A recursive demand waits for the rows its own rules admit. A closed boundary
// resolves the subtree beneath it, so the opened tree finishes while collapsed
// trees are still being discovered.
func (v *View) awaitDisclosedSubtree(ctx context.Context, demand directoryDemand, rules *Rules) error {
	return v.catalog.AwaitSubtree(ctx, v.scope.Project, demand.root, demand.path,
		func(ctx context.Context, navigation *sourcecatalog.Navigation) (bool, error) {
			projection := Projection{Root: demand.root.ID, Navigation: navigation, Rules: rules}
			pending, err := projection.unresolved(ctx, demand.path)
			return pending == 0, err
		})
}
