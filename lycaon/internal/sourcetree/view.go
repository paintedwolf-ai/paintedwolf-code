package sourcetree

import (
	"context"
	"path"
	"slices"
	"sync"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

type Root struct {
	sourcecatalog.Root
	Label string
}

type View struct {
	live      liveView
	mu        sync.Mutex
	scope     pagedview.Scope
	roots     map[string]sourcecatalog.Root
	rootOrder []Root
	revision  string
	catalog   *sourcecatalog.Catalog
	rules     Rules
	review    *ReviewSet

	ctx           context.Context
	cancel        context.CancelFunc
	workers       sync.WaitGroup
	notify        func()
	closed        bool
	loading       map[Address]chan struct{}
	prepareDone   chan struct{}
	prepareCancel context.CancelFunc
	prepareError  error
}

// New follows the host lifetime.
func New(ctx context.Context, scope pagedview.Scope, roots []Root, catalog *sourcecatalog.Catalog, notify func()) *View {
	lifetime, cancel := context.WithCancel(ctx)
	view := &View{scope: scope, roots: make(map[string]sourcecatalog.Root), catalog: catalog, ctx: lifetime, cancel: cancel, notify: notify, rootOrder: append([]Root(nil), roots...), revision: uuid.NewString(), loading: make(map[Address]chan struct{})}
	for _, root := range roots {
		view.roots[root.ID] = root.Root
		// An open view keeps its roots resident until its lifetime ends. A root
		// that cannot be held fails the catalog reads the view makes instead.
		if release, err := catalog.HoldRoot(lifetime, scope.Project, root.Root); err == nil {
			context.AfterFunc(lifetime, release)
		}
	}
	view.startLive()
	return view
}

// RulesBasis carries a displayed disclosure state and whether it supersedes
// the view's current state before the next change.
type RulesBasis struct {
	Rules   Rules
	Replace bool
}

// Disclose publishes an ordered set of changes as one intent revision.
func (v *View) Disclose(ctx context.Context, basis *RulesBasis, changes ...IntentEntry) error {
	return v.changeDisclosures(ctx, func(rules *Rules) []IntentEntry {
		if basis != nil && basis.Replace {
			*rules = basis.Rules.Clone()
		}
		return changes
	})
}

func (v *View) Toggle(ctx context.Context, basis *RulesBasis, address Address, collapseDescendants bool) error {
	if !validAddress(address) {
		return ErrAddress
	}
	return v.changeDisclosures(ctx, func(rules *Rules) []IntentEntry {
		if basis != nil && basis.Replace {
			*rules = basis.Rules.Clone()
		}
		open := rules.At(address).Open
		if open && collapseDescendants {
			return []IntentEntry{{Address: address, Disclosure: Disclosure{Open: false, Recursive: true}},
				{Address: address, Disclosure: Disclosure{Open: true}}}
		}
		return []IntentEntry{{Address: address, Disclosure: Disclosure{Open: !open}}}
	})
}

// Intent is bounded before host-closed boundaries are added; the disclosed
// rules then carry both under their own bounds.
const (
	maxIntentRules    = 2048
	maxIntentBytes    = 4 << 20
	maxDerivedRules   = 16384
	maxDisclosedBytes = 16 << 20
)

// changeDisclosures applies a change to the current rules and installs the
// result, retrying on the newer state when another change lands first.
// Boundary derivation reads the catalog, so it runs outside the view lock.
func (v *View) changeDisclosures(ctx context.Context, change func(*Rules) []IntentEntry) error {
	for {
		v.mu.Lock()
		if v.closed {
			v.mu.Unlock()
			return pagedview.ErrExpired
		}
		if err := ctx.Err(); err != nil {
			v.mu.Unlock()
			return err
		}
		basis := v.revision
		copy := v.rules.Clone()
		v.mu.Unlock()
		rules := &copy
		changes := change(rules)
		for _, change := range changes {
			if !validAddress(change.Address) {
				return ErrAddress
			}
			if _, ok := v.roots[change.Address.Root]; !ok {
				return ErrUnknownRoot
			}
		}
		for _, change := range changes {
			rules.Set(change.Address, change.Disclosure)
		}
		if rules.count() > maxIntentRules || rules.bytes() > maxIntentBytes {
			return pagedview.ErrBudget
		}
		ready, err := v.deriveBoundaries(ctx, rules)
		if err != nil {
			return err
		}
		v.mu.Lock()
		if v.closed {
			v.mu.Unlock()
			return pagedview.ErrExpired
		}
		if v.revision != basis {
			v.mu.Unlock()
			continue
		}
		v.rules = *rules
		if len(changes) > 0 {
			v.revision = uuid.NewString()
			v.resetPreparationLocked()
		}
		for _, change := range changes {
			if v.rules.At(change.Address).Open {
				v.loadLocked(v.roots[change.Address.Root], change.Address.Path)
			}
		}
		v.mu.Unlock()
		if !ready {
			// Rule installation can follow the catalog's final publication.
			v.wakeLive()
		}
		if len(changes) > 0 && v.notify != nil {
			v.notify()
		}
		return nil
	}
}

// deriveBoundaries closes the collapse boundaries beneath recursively open paths.
// It never waits; false means the opened directories are not yet listed.
func (v *View) deriveBoundaries(ctx context.Context, rules *Rules) (bool, error) {
	anchors := rules.recursiveAnchors()
	if len(anchors) == 0 {
		rules.reconcileDerived(nil)
		return true, nil
	}
	wanted := make(map[Address]bool)
	for _, anchor := range anchors {
		root, ok := v.roots[anchor.Root]
		if !ok {
			continue
		}
		boundaries, ready, err := v.catalog.CollapseBoundaries(ctx, v.scope.Project, root, anchor.Path)
		if err != nil {
			return false, err
		}
		if !ready {
			return false, nil
		}
		for _, boundary := range boundaries {
			wanted[Address{Root: anchor.Root, Path: boundary}] = true
		}
	}
	rules.reconcileDerived(wanted)
	if rules.derivedCount() > maxDerivedRules || rules.bytes() > maxDisclosedBytes {
		return false, pagedview.ErrBudget
	}
	return true, nil
}

// refreshBoundaries installs boundaries the published structure can now name,
// including collapsed trees created after the command. Boundaries are not
// intent, so the intent revision stands and only the projection moves.
func (v *View) refreshBoundaries(ctx context.Context) {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return
	}
	basis := v.revision
	rules := v.rules.Clone()
	v.mu.Unlock()
	before := rules.Derived()
	ready, err := v.deriveBoundaries(ctx, &rules)
	if err != nil || !ready || slices.Equal(before, rules.Derived()) {
		return
	}
	v.mu.Lock()
	if v.closed || v.revision != basis {
		v.mu.Unlock()
		return
	}
	v.rules = rules
	v.resetPreparationLocked()
	v.mu.Unlock()
	if v.notify != nil {
		v.notify()
	}
}

// Reveal installs the target's ancestors as one disclosure change.
func (v *View) Reveal(ctx context.Context, address Address) error {
	if !validAddress(address) {
		return ErrAddress
	}
	var parents []IntentEntry
	for dir := path.Dir(address.Path); ; dir = path.Dir(dir) {
		parents = append(parents, IntentEntry{Address: Address{Root: address.Root, Path: dir}, Disclosure: Disclosure{Open: true}})
		if dir == "." {
			break
		}
	}
	return v.Disclose(ctx, nil, parents...)
}

func (v *View) loadLocked(root sourcecatalog.Root, dir string) <-chan struct{} {
	address := Address{Root: root.ID, Path: dir}
	if existing := v.loading[address]; existing != nil {
		return existing
	}
	done := make(chan struct{})
	v.loading[address] = done
	v.workers.Add(1)
	go v.load(root, dir, done)
	return done
}
func (v *View) load(root sourcecatalog.Root, dir string, done chan struct{}) {
	defer v.workers.Done()
	defer close(done)
	_, _ = v.catalog.ObserveDirectory(v.ctx, v.scope.Project, root, dir, sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	v.mu.Lock()
	delete(v.loading, Address{Root: root.ID, Path: dir})
	v.mu.Unlock()
	if v.notify != nil {
		v.notify()
	}
}

func (v *View) Close() {
	v.mu.Lock()
	v.closed = true
	v.cancel()
	v.mu.Unlock()
	for _, unsubscribe := range v.live.unsubscribe {
		unsubscribe()
	}
	v.workers.Wait()
	if v.review != nil {
		v.review.Close()
	}
}
