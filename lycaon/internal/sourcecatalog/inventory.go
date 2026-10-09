package sourcecatalog

import (
	"context"
	"log/slog"
	"path"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
)

type inventoryWork struct {
	cancel         context.CancelFunc
	done           chan struct{}
	wake           chan struct{}
	initial        chan struct{}
	initialSettled bool
	initialError   error
	dirty          map[string]struct{}
	full           bool
	running        bool
	changed        chan struct{}
	passError      error
	interests      backgroundwork.PriorityGroup
}

func (w *inventoryWork) invalidate(paths []string) {
	if w.wake == nil {
		return
	}
	if w.dirty == nil {
		w.dirty = make(map[string]struct{})
	}
	if len(paths) == 0 {
		w.dirty = map[string]struct{}{".": {}}
		w.full = true
	} else {
		for _, rel := range paths {
			w.dirty[path.Dir(rel)] = struct{}{}
		}
		if len(w.dirty) > maxPendingTreePaths {
			coarsened, escalated := coarsenDirtyDirectories(w.dirty, maxPendingTreePaths)
			// Coarsening preserves any queued whole-tree scan.
			w.dirty, w.full = coarsened, w.full || escalated
		}
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// WarmNavigation prepares shared structure independently of search enrichment.
func (c *Directories) WarmNavigation(ctx context.Context, project string, root Root) error {
	s, err := c.trees.indexStore(ctx, project, root)
	if err != nil {
		return err
	}
	return s.startInventory(ctx)
}

func (s *indexStore) startInventory(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pins.drained {
		return pagedview.ErrExpired
	}
	if s.inventory.cancel != nil {
		return nil
	}
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.inventory = inventoryWork{cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), initial: make(chan struct{}), changed: make(chan struct{})}
	// What the first pass owes is decided once a restored checkpoint is known.
	go s.runInventory(lifetime)
	return nil
}

// AwaitNavigation waits for initial structural coverage without waiting for enrichment.
func (c *Directories) AwaitNavigation(ctx context.Context, project string, root Root) error {
	s, err := c.trees.indexStore(ctx, project, root)
	if err != nil {
		return err
	}
	if err := s.startInventory(ctx); err != nil {
		return err
	}
	return s.awaitInitialInventory(ctx)
}

func (s *indexStore) awaitInitialInventory(ctx context.Context) error {
	s.mu.Lock()
	initial, done := s.inventory.initial, s.inventory.done
	s.mu.Unlock()
	if initial == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return pagedview.ErrExpired
	case <-initial:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.inventory.initialError
	}
}

// Initial readers settle on failure; the inventory keeps retrying in the background.
func (s *indexStore) settleInitialInventory(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inventory.initialError = err
	if !s.inventory.initialSettled {
		s.inventory.initialSettled = true
		close(s.inventory.initial)
	}
}

func (s *indexStore) runInventory(ctx context.Context) {
	defer close(s.inventory.done)
	s.seedInventoryWork(ctx)
	retry := time.NewTimer(time.Hour)
	retry.Stop()
	defer retry.Stop()
	cooldown := time.NewTimer(time.Hour)
	cooldown.Stop()
	defer cooldown.Stop()
	delay := time.Second
	initial := true
	wake := s.inventory.wake
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-retry.C:
		}
		s.mu.Lock()
		s.inventory.running = true
		s.mu.Unlock()
		started := time.Now()
		err := s.inventoryPass(ctx)
		s.mu.Lock()
		s.inventory.running = false
		s.inventory.passError = err
		close(s.inventory.changed)
		s.inventory.changed = make(chan struct{})
		s.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if initial {
			s.settleInitialInventory(err)
			initial = err != nil
		}
		if err != nil {
			slog.WarnContext(ctx, "Source inventory failed", "root", s.root.ID, "error", err)
			// New changes remain queued until the failed pass can retry.
			wake = nil
			retry.Reset(delay)
			delay = min(30*time.Second, delay*2)
		} else {
			retry.Stop()
			wake = s.inventory.wake
			delay = time.Second
		}
		// A pass rests for as long as it ran, within a floor that lets a burst of
		// writes coalesce and a ceiling that keeps a quiet root responsive.
		rest := min(max(time.Since(started), structuralPassRestFloor), structuralPassRestCeiling)
		cooldown.Reset(rest)
		select {
		case <-ctx.Done():
			cooldown.Stop()
			return
		case <-cooldown.C:
		}
	}
}

// The rest ceiling bounds write latency after expensive scans; the floor
// matches the watcher's own coalescing so one burst becomes one pass.
const (
	structuralPassRestFloor   = 250 * time.Millisecond
	structuralPassRestCeiling = 2 * time.Second
)

// seedInventoryWork restores what a checkpoint still vouches for and owes the
// first pass the rest: the directories whose stamps moved, or the whole tree
// when nothing was restored.
func (s *indexStore) seedInventoryWork(ctx context.Context) {
	restored, stale := s.restoreStructure(ctx)
	s.mu.Lock()
	if restored {
		s.invalidateListingsLocked(stale)
	} else {
		s.inventory.invalidate(nil)
	}
	s.mu.Unlock()
	// One pass runs even with nothing stale; it settles readers waiting on
	// initial coverage.
	select {
	case s.inventory.wake <- struct{}{}:
	default:
	}
}

// A root scan runs directly from directory entries; persistence never selects its frontier.
func (s *indexStore) inventoryPass(ctx context.Context) error {
	s.mu.Lock()
	dirty := s.inventory.dirty
	full := s.inventory.full
	s.inventory.full = false
	s.inventory.dirty = nil
	s.mu.Unlock()
	if len(dirty) == 0 {
		return nil
	}
	err := s.buildStructure(ctx, dirty, full)
	if err != nil {
		s.mu.Lock()
		s.inventory.requeueFull()
		s.mu.Unlock()
	}
	return err
}

func (w *inventoryWork) requeueFull() {
	w.dirty = map[string]struct{}{".": {}}
	w.full = true
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
