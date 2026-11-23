package sessions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/keylock"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workscope"
)

// credentialRoot is one admitted root. known belongs to the holder of the
// root's access lock; the remaining state is guarded by credentialIndex.mu.
type credentialRoot struct {
	projectID, id, path, label string
	known                      map[string]bool

	epoch     repochange.Epoch
	ready     bool
	validated time.Time
	err       error
	full      bool
	queue     []repochange.Event
	running   bool
	cancel    context.CancelFunc
	withdrawn bool
	passes    int
	settled   chan struct{}
}

// credentialStep is one unit of root work. A step with no paths and no full
// pass only records its epoch.
type credentialStep struct {
	epoch repochange.Epoch
	full  bool
	paths []string
}

type credentialIndex struct {
	files    *credentialFiles
	access   keylock.Group
	work     workscope.Group
	coverage func(string) repochange.WatchCoverage
	now      func() time.Time
	stop     func()

	mu    sync.Mutex
	roots map[string]*credentialRoot
}

func newCredentialIndex(files *credentialFiles) *credentialIndex {
	return &credentialIndex{
		files:    files,
		coverage: repochange.Coverage,
		now:      time.Now,
		roots:    map[string]*credentialRoot{},
	}
}

func newCredentialRoot(row db.ProjectRoots) *credentialRoot {
	return &credentialRoot{
		projectID: row.ProjectID, id: row.ID, path: row.Path, label: row.Label,
		known:   map[string]bool{},
		settled: make(chan struct{}),
	}
}

func credentialRootKey(projectID, rootID string) string { return projectID + "\x00" + rootID }

func (r *credentialRoot) key() string { return credentialRootKey(r.projectID, r.id) }

func (c *credentialFiles) indexing() bool {
	return c != nil && c.index != nil && c.harvest != nil && c.fp != nil
}

// Startup restores durable evidence and subscribes to changes; it never walks
// attached roots.
func (c *credentialFiles) startCredentialIndex() {
	if !c.indexing() {
		return
	}
	i := c.index
	i.stop = repochange.RegisterObserver(func(_ context.Context, event repochange.Event) { i.changed(event) })
}

func (c *credentialFiles) closeCredentialIndex(ctx context.Context) error {
	if c == nil || c.index == nil {
		return nil
	}
	c.index.work.Stop()
	if c.index.stop != nil {
		c.index.stop()
	}
	return c.index.work.Wait(ctx)
}

// Start begins indexing the project's roots without waiting for them.
func (c *credentialFiles) Start(projectID string) {
	if !c.indexing() || projectID == "" {
		return
	}
	i := c.index
	ctx, finish, err := i.work.Begin(context.Background())
	if err != nil {
		return
	}
	go func() {
		defer finish()
		if _, err := i.admit(ctx, projectID); err != nil && !errors.Is(err, context.Canceled) {
			slog.WarnContext(ctx, "credential-file index unavailable", "project_id", projectID, "error", err)
		}
	}()
}

// Await returns once every admitted root of the project is current. A waiter
// that stops early leaves the builds running.
func (c *credentialFiles) Await(ctx context.Context, wait tools.CredentialWait) error {
	if !c.indexing() || wait.ProjectID == "" {
		return nil
	}
	ctx, finish, err := c.index.work.Begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	for {
		roots, err := c.index.admit(ctx, wait.ProjectID)
		if err != nil {
			return err
		}
		replaced := false
		for _, root := range roots {
			withdrawn, err := c.index.await(ctx, root)
			if err != nil {
				return err
			}
			replaced = replaced || withdrawn
		}
		if !replaced {
			return nil
		}
	}
}

// admit reconciles the project's attached roots and schedules each stale one.
func (i *credentialIndex) admit(ctx context.Context, projectID string) ([]*credentialRoot, error) {
	rows, err := i.files.queries.ListProjectRoots(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var admitted []db.ProjectRoots
	for _, row := range rows {
		// Agents cannot write in an editor-only root, so it needs no index.
		if ok, _ := (project.Root{Path: row.Path}).Admission(); ok {
			admitted = append(admitted, row)
			// The watcher binds before any pass reads its epoch.
			repochange.EnsureRoot(ctx, row.Path)
		}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	attached := make(map[string]bool, len(admitted))
	roots := make([]*credentialRoot, 0, len(admitted))
	for _, row := range admitted {
		key := credentialRootKey(row.ProjectID, row.ID)
		attached[key] = true
		root := i.roots[key]
		if root == nil || root.path != row.Path {
			if root != nil {
				i.withdraw(root)
			}
			root = newCredentialRoot(row)
			i.roots[key] = root
		}
		root.label = row.Label
		roots = append(roots, root)
	}
	for key, root := range i.roots {
		if root.projectID == projectID && !attached[key] {
			i.withdraw(root)
		}
	}
	for _, root := range roots {
		i.kick(root)
	}
	return roots, nil
}

// await blocks until root is current. It reports a withdrawn root so the
// caller can admit its replacement.
func (i *credentialIndex) await(ctx context.Context, root *credentialRoot) (bool, error) {
	kicked := false
	for {
		i.mu.Lock()
		if root.withdrawn {
			i.mu.Unlock()
			return true, nil
		}
		if i.current(root) {
			i.mu.Unlock()
			return false, nil
		}
		if !root.running {
			if root.err != nil && kicked {
				err := root.err
				i.mu.Unlock()
				return false, fmt.Errorf("credential-file index: %w", err)
			}
			kicked = true
			i.kick(root)
			i.mu.Unlock()
			continue
		}
		settled := root.settled
		i.mu.Unlock()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-settled:
		}
	}
}

// current requires i.mu.
func (i *credentialIndex) current(root *credentialRoot) bool {
	return root.ready && root.err == nil && len(root.queue) == 0 &&
		!root.coverageExpired(i.coverage(root.path), i.now()) &&
		repochange.EpochCurrent(root.path, root.epoch)
}

func (r *credentialRoot) coverageExpired(coverage repochange.WatchCoverage, now time.Time) bool {
	return !coverage.Complete && now.Sub(r.validated) >= repochange.CoverageRevalidationInterval
}

// kick clears a failed pass and schedules a stale root. It requires i.mu.
func (i *credentialIndex) kick(root *credentialRoot) {
	if root.running || root.withdrawn || i.current(root) {
		return
	}
	root.err = nil
	i.schedule(root)
}

// schedule starts the root's runner unless one is active. It requires i.mu.
func (i *credentialIndex) schedule(root *credentialRoot) {
	if root.running || root.withdrawn {
		return
	}
	ctx, finish, err := i.work.Begin(context.Background())
	if err != nil {
		root.err = err
		i.signal(root)
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	root.running, root.cancel = true, cancel
	go func() {
		defer finish()
		defer cancel()
		i.run(ctx, root)
	}()
}

// signal wakes the root's waiters. It requires i.mu.
func (i *credentialIndex) signal(root *credentialRoot) {
	close(root.settled)
	root.settled = make(chan struct{})
}

func (i *credentialIndex) run(ctx context.Context, root *credentialRoot) {
	for {
		i.mu.Lock()
		step, ok := i.next(root)
		if !ok {
			root.running, root.cancel = false, nil
			i.signal(root)
			i.mu.Unlock()
			return
		}
		i.mu.Unlock()
		err := i.apply(ctx, root, step)
		i.mu.Lock()
		i.settle(root, step, err)
		i.mu.Unlock()
	}
}

// next chooses the root's next step. It requires i.mu.
func (i *credentialIndex) next(root *credentialRoot) (credentialStep, bool) {
	for {
		if root.withdrawn || root.err != nil {
			return credentialStep{}, false
		}
		if !root.ready || root.full || root.coverageExpired(i.coverage(root.path), i.now()) {
			root.full = false
			epoch := repochange.CurrentEpoch(root.path)
			root.queue = slices.DeleteFunc(root.queue, func(event repochange.Event) bool {
				return eventEpoch(event).BootID == epoch.BootID && event.WorktreeEpoch <= epoch.Value
			})
			return credentialStep{epoch: epoch, full: true}, true
		}
		if len(root.queue) == 0 {
			if repochange.EpochCurrent(root.path, root.epoch) {
				return credentialStep{}, false
			}
			root.full = true
			continue
		}
		event := root.queue[0]
		root.queue = root.queue[1:]
		epoch := eventEpoch(event)
		if epoch.BootID == root.epoch.BootID && epoch.Value <= root.epoch.Value {
			continue
		}
		if epoch.BootID != root.epoch.BootID || epoch.Value != root.epoch.Value+1 {
			root.full = true
			continue
		}
		switch {
		case event.Kind == repochange.IndexChanged:
			return credentialStep{epoch: epoch}, true
		case event.Kind == repochange.HeadMoved && i.coverage(root.path).Complete:
			// The watcher delivers the changed worktree paths as their own events.
			return credentialStep{epoch: epoch}, true
		case event.Kind == repochange.WorktreeChanged && len(event.Paths) > 0:
			return credentialStep{epoch: epoch, paths: event.Paths}, true
		}
		root.full = true
	}
}

// settle records a step's outcome. It requires i.mu.
func (i *credentialIndex) settle(root *credentialRoot, step credentialStep, err error) {
	if err != nil {
		root.err, root.ready = err, false
	} else {
		root.epoch = step.epoch
		if step.full {
			root.ready, root.validated = true, i.now()
			root.passes++
		}
	}
	i.signal(root)
}

func (i *credentialIndex) apply(ctx context.Context, root *credentialRoot, step credentialStep) error {
	if !step.full && len(step.paths) == 0 {
		return nil
	}
	release, err := i.access.Acquire(ctx, root.key())
	if err != nil {
		return err
	}
	defer release()
	if step.full {
		return i.refresh(ctx, root, []string{"."})
	}
	return i.refresh(ctx, root, step.paths)
}

func eventEpoch(event repochange.Event) repochange.Epoch {
	return repochange.Epoch{BootID: event.EpochBootID, Value: event.WorktreeEpoch}
}

// changed queues a watcher event for every admitted root it names.
func (i *credentialIndex) changed(event repochange.Event) {
	dir := filepath.Clean(event.ProjectDir)
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, root := range i.roots {
		// A root that is neither ready nor building gets a full pass at its next admission.
		if filepath.Clean(root.path) != dir || !root.ready && !root.running {
			continue
		}
		at, _ := slices.BinarySearchFunc(root.queue, event.WorktreeEpoch, func(queued repochange.Event, value uint64) int {
			return compareUint(queued.WorktreeEpoch, value)
		})
		root.queue = slices.Insert(root.queue, at, event)
		i.schedule(root)
	}
}

func compareUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// withdraw stops a root's work and retires its bindings in the background. It
// requires i.mu.
func (i *credentialIndex) withdraw(root *credentialRoot) {
	i.detach(root)
	ctx, finish, err := i.work.Begin(context.Background())
	if err != nil {
		return
	}
	go func() {
		defer finish()
		if err := i.retire(ctx, root); err != nil && !errors.Is(err, context.Canceled) {
			slog.WarnContext(ctx, "credential-file evidence retirement failed", "root", root.path, "error", err)
		}
	}()
}

// detach removes a root from the index and cancels its runner. It requires i.mu.
func (i *credentialIndex) detach(root *credentialRoot) {
	root.withdrawn = true
	if root.cancel != nil {
		root.cancel()
	}
	if i.roots[root.key()] == root {
		delete(i.roots, root.key())
	}
	i.signal(root)
}
