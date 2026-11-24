package sessions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/keylock"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/secretmatch"
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
	applying  bool
	cancel    context.CancelFunc
	withdrawn bool
	passes    int
	settled   chan struct{}
	lastStats credentialBuildStats

	// Counts of the running step, read by waiters without the root's lock.
	directories, credentialFiles atomic.Int64
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
	events   activityPublisher
	coverage func(string) repochange.WatchCoverage
	now      func() time.Time
	after    func(time.Duration) <-chan time.Time
	stop     func()
	onScan   func(rootPath, path string)
	onDir    func(rootPath, rel string)

	mu    sync.Mutex
	roots map[string]*credentialRoot
}

func newCredentialIndex(files *credentialFiles, events activityPublisher) *credentialIndex {
	return &credentialIndex{
		files:    files,
		events:   events,
		coverage: repochange.Coverage,
		now:      time.Now,
		after:    time.After,
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

// Startup restores durable evidence and subscribes to changes; it never walks attached roots.
func (c *credentialFiles) startCredentialIndex() {
	if !c.indexing() {
		return
	}
	i := c.index
	i.stop = repochange.RegisterObserver(func(ctx context.Context, event repochange.Event) { i.changed(ctx, event) })
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
	var localTrace secretmatch.EvidenceTrace
	callerID := uuid.NewString()
	shownAt := c.index.now().Add(credentialWaitShown)
	for {
		roots, err := c.index.admitWithStarter(ctx, wait.ProjectID, callerID)
		if err != nil {
			emitReadinessPhase(ctx, wait.ProjectID, &localTrace, true)
			return err
		}
		replaced := false
		for _, root := range roots {
			withdrawn, err := c.index.await(ctx, root, callerID, &localTrace, c.index.waitActivity(wait, root), shownAt)
			if err != nil {
				emitReadinessPhase(ctx, wait.ProjectID, &localTrace, true)
				return err
			}
			replaced = replaced || withdrawn
		}
		if !replaced {
			emitReadinessPhase(ctx, wait.ProjectID, &localTrace, false)
			return nil
		}
	}
}

func (i *credentialIndex) admit(ctx context.Context, projectID string) ([]*credentialRoot, error) {
	return i.admitWithStarter(ctx, projectID, "")
}

// admitWithStarter reconciles the project's attached roots and schedules each stale one.
func (i *credentialIndex) admitWithStarter(ctx context.Context, projectID, starterID string) ([]*credentialRoot, error) {
	rows, err := i.files.queries.ListProjectRoots(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var admitted []db.ProjectRoots
	for _, row := range rows {
		if ok, _ := (project.Root{Path: row.Path}).Admission(); ok {
			admitted = append(admitted, row)
			repochange.EnsureRoot(ctx, row.Path)
			repochange.FlushDir(ctx, row.Path)
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
				i.withdraw(ctx, root)
			}
			root = newCredentialRoot(row)
			i.roots[key] = root
		}
		root.label = row.Label
		roots = append(roots, root)
	}
	for key, root := range i.roots {
		if root.projectID == projectID && !attached[key] {
			i.withdraw(ctx, root)
		}
	}
	for _, root := range roots {
		i.kick(ctx, root, starterID)
	}
	return roots, nil
}

// await blocks until root is current.
func (i *credentialIndex) await(
	ctx context.Context,
	root *credentialRoot,
	callerID string,
	localTrace *secretmatch.EvidenceTrace,
	activity *credentialWaitActivity,
	shownAt time.Time,
) (bool, error) {
	started := i.now()
	var tick <-chan time.Time
	if activity != nil {
		tick = i.after(max(0, shownAt.Sub(started)))
		defer activity.finish(ctx)
	}
	kicked, waited := false, false
	for {
		i.mu.Lock()
		if root.withdrawn {
			i.mu.Unlock()
			return true, nil
		}
		if i.current(root) {
			stats, gen := root.lastStats, root.epoch.Value
			i.mu.Unlock()
			noteRootReadiness(ctx, localTrace, i.now().Sub(started).Milliseconds(), root.id, gen, waited, callerID, stats)
			return false, nil
		}
		waited = true
		if !root.running {
			if root.err != nil && kicked {
				err, stats, gen := root.err, root.lastStats, root.epoch.Value
				i.mu.Unlock()
				noteRootReadiness(ctx, localTrace, i.now().Sub(started).Milliseconds(), root.id, gen, true, callerID, stats)
				return false, fmt.Errorf("credential-file index: %w", err)
			}
			kicked = true
			i.kick(ctx, root, callerID)
			i.mu.Unlock()
			continue
		}
		settled := root.settled
		i.mu.Unlock()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-settled:
		case <-tick:
			activity.show(ctx)
			tick = i.after(credentialWaitShown)
		}
	}
}

func (i *credentialIndex) current(root *credentialRoot) bool {
	return root.ready && root.err == nil && len(root.queue) == 0 && !root.applying &&
		!root.coverageExpired(i.coverage(root.path), i.now()) &&
		repochange.EpochCurrent(root.path, root.epoch)
}

func (r *credentialRoot) coverageExpired(coverage repochange.WatchCoverage, now time.Time) bool {
	return !coverage.Complete && now.Sub(r.validated) >= repochange.CoverageRevalidationInterval
}

func sameCredentialRootDir(left, right string) bool {
	if filepath.Clean(left) == filepath.Clean(right) {
		return true
	}
	lCanon, rCanon := gitrepo.CanonicalDir(left), gitrepo.CanonicalDir(right)
	return lCanon != "" && lCanon == rCanon
}
