package sourcecatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/repochange"
	_ "modernc.org/sqlite"
)

// Cache files are keyed by layout version, schema, and root identity.
const treeGenerationDirName = "tree-v1"

const treeFileSuffix = ".db"

// The WAL retention limit applies after checkpoint reset.
const treeJournalSizeLimit = 64 << 20

// TreeStatus describes the generation available to bounded metadata queries.
type TreeStatus struct {
	// Instance distinguishes file-index incarnations whose revision counters may repeat.
	Instance uint64
	State    State
	Revision uint64
	// Complete means the generation covers every path its budget admitted.
	// Discovery can publish partial generations before completion.
	Complete bool
	// Refreshing means a newer generation is being built behind this one.
	Refreshing  bool
	ValidatedAt time.Time
	Error       string
}

// publishGeneration stamps a generation on the open transaction and commits it.
func publishGeneration(ctx context.Context, tx *sql.Tx, complete bool) (TreeStatus, error) {
	validated := time.Now()
	status := TreeStatus{State: StateReady, Complete: complete, ValidatedAt: validated}
	err := tx.QueryRowContext(ctx,
		`INSERT INTO meta(id,revision,validated,complete) VALUES(1,1,?,?)
		 ON CONFLICT(id) DO UPDATE SET revision=revision+1,validated=excluded.validated,complete=excluded.complete
		 RETURNING revision`, validated.UnixNano(), complete).Scan(&status.Revision)
	if err != nil {
		return TreeStatus{}, err
	}
	return status, tx.Commit()
}

// storeCore shares projection storage, one background reconciliation, and
// watch state for full or incremental refreshes.
type storeCore struct {
	instance  uint64
	projectID string
	mu        sync.Mutex
	root      Root
	file      string
	status    TreeStatus
	building  bool
	// settled means the running pass published its final generation and
	// recorded the epoch that generation observed; nothing newer is pending
	// unless that epoch asked for another pass.
	settled bool
	retired bool
	cancel  context.CancelFunc
	done    chan struct{}
	// readable closes on the first published generation or when reconciliation ends.
	// It is nil once a generation exists.
	readable chan struct{}
	dirty    map[string]struct{}
	full     bool
	epoch    repochange.Epoch
	observed repochange.Epoch
	lastUsed time.Time
	// touched records the last retention timestamp written to the file.
	touched time.Time
	// policy bounds and orders every walk of this store.
	policy walkPolicy
}

// projectionStore is one disk-backed view of a root: the shared lifecycle plus
// the projection's own schema, reconciliation, and change filter.
type projectionStore interface {
	core() *storeCore
	// schema is the complete DDL for this projection's file.
	schema() string
	// workKey names this projection's lane in the background broker.
	workKey() string
	// reconcile advances the projection toward the tree as it is now.
	reconcile(ctx context.Context, epoch repochange.Epoch) error
	// scopeChanges drops changed paths this projection does not represent.
	scopeChanges(changed []string) ([]string, bool)
	// contentBuilds lists the content builds included in a drain.
	contentBuilds() []*contentBuild
	// partial allows waiting callers to return after the first published generation.
	// Other projections wait for reconciliation to finish.
	partial() bool
}

func (s *storeCore) core() *storeCore { return s }

const maxPendingTreePaths = 4096

func (s *storeCore) recordChanges(paths []string) {
	if s.full {
		return
	}
	if len(paths) == 0 {
		s.full = true
		s.dirty = nil
		return
	}
	if s.dirty == nil {
		s.dirty = map[string]struct{}{}
	}
	for _, p := range paths {
		if _, exists := s.dirty[p]; exists {
			continue
		}
		if len(s.dirty) == maxPendingTreePaths {
			s.full = true
			s.dirty = nil
			return
		}
		s.dirty[p] = struct{}{}
	}
}

// setStatus releases readers after publication.
func (s *storeCore) setStatus(status TreeStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setStatusLocked(status)
}

func (s *storeCore) setStatusLocked(status TreeStatus) {
	s.status = status
	s.releaseReadableLocked()
}

// publishFinalLocked publishes a pass's final generation together with the
// epoch it observed, so no reader sees the generation without that verdict.
func (s *storeCore) publishFinalLocked(status TreeStatus, epoch repochange.Epoch) {
	s.setStatusLocked(status)
	s.settleEpoch(epoch)
	s.settled = true
}

// refreshingLocked reports whether a generation newer than the published one
// is pending: a requested pass, or a running pass that has not published its last.
func (s *storeCore) refreshingLocked() bool {
	return s.full || len(s.dirty) > 0 || s.publishingLocked()
}

// publishingLocked reports whether a running pass has yet to publish its final
// generation and settle its epoch. Once settled, a change requests its own pass
// even while the running pass finishes its tail.
func (s *storeCore) publishingLocked() bool { return s.building && !s.settled }

// releaseReadableLocked releases first-generation waiters, including when
// reconciliation ends without publishing a generation.
func (s *storeCore) releaseReadableLocked() {
	if s.readable != nil {
		close(s.readable)
		s.readable = nil
	}
}

// takeWork separates pending reconciliation from changes arriving during the pass.
func (s *storeCore) takeWork() (bool, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	full := s.full
	changed := make([]string, 0, len(s.dirty))
	for p := range s.dirty {
		changed = append(changed, p)
	}
	s.full, s.dirty = false, nil
	return full, changed
}

// treeBusyTimeoutMS is how long a tree store writer waits for the lock.
const treeBusyTimeoutMS = 10000

func openTreeDB(ctx context.Context, file string) (*sql.DB, error) {
	return openTreeDBWaiting(ctx, file, treeBusyTimeoutMS)
}

// openTreeDBWaiting opens a tree store whose statements wait busyMS for a lock;
// zero never waits.
func openTreeDBWaiting(ctx context.Context, file string, busyMS int) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: file}
	db, err := sql.Open("sqlite", u.String()+fmt.Sprintf("?_pragma=auto_vacuum(INCREMENTAL)&_pragma=recursive_triggers(1)&_pragma=busy_timeout(%d)&_pragma=cache_size(-4096)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=journal_size_limit(%d)", busyMS, treeJournalSizeLimit))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// treeDirPath resolves where generations live: the test override, else the
// catalog cache under the engine config root.
func (c *TreeStores) treeDirPath() (string, error) {
	if c.treeDir != "" {
		return c.treeDir, nil
	}
	base, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(enginepaths.SourceCatalogCacheRootUnder(base), treeGenerationDirName), nil
}

// A store without a published generation remains warming.
func loadStore(ctx context.Context, s projectionStore) error {
	core := s.core()
	db, err := openTreeDB(ctx, core.file)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if _, err = db.ExecContext(ctx, s.schema()); err != nil {
		return err
	}
	var revision uint64
	var validated int64
	var complete bool
	err = db.QueryRowContext(ctx, "SELECT revision, validated, complete FROM meta WHERE id=1").Scan(&revision, &validated, &complete)
	if errors.Is(err, sql.ErrNoRows) {
		core.status = TreeStatus{State: StateWarming}
		return nil
	}
	if err != nil {
		return err
	}
	core.status = TreeStatus{State: StateReady, Revision: revision, Complete: complete, ValidatedAt: time.Unix(0, validated)}
	return nil
}

// readTx pins one read-only transaction against the published generation.
func (s *storeCore) readTx(ctx context.Context, status TreeStatus) (*sql.DB, *sql.Tx, TreeStatus, error) {
	u := url.URL{Scheme: "file", Path: s.file}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(1000)&_pragma=cache_size(-4096)")
	if err != nil {
		return nil, nil, status, err
	}
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		_ = db.Close()
		return nil, nil, status, err
	}
	var validated int64
	if err = tx.QueryRowContext(ctx, "SELECT revision, validated, complete FROM meta WHERE id=1").
		Scan(&status.Revision, &validated, &status.Complete); err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		return nil, nil, status, err
	}
	status.ValidatedAt = time.Unix(0, validated)
	return db, tx, status, nil
}

// openStore refreshes a projection and reports whether a generation is readable.
// Wait limits the caller's wait without canceling the shared build.
func openStore(ctx context.Context, s projectionStore, wait time.Duration, broker *backgroundwork.Broker) (TreeStatus, error) {
	core := s.core()
	core.mu.Lock()
	if core.retired {
		core.mu.Unlock()
		return TreeStatus{}, pagedview.ErrExpired
	}
	var err error
	if core.status.State == "" {
		err = loadStore(ctx, s)
	}
	if err != nil {
		core.mu.Unlock()
		return TreeStatus{}, err
	}
	epoch := repochange.CurrentEpoch(core.root.Path)
	if core.status.State == StateReady {
		core.touch(time.Now())
	}
	// A settled pass still running its tail picks up the request before it exits.
	if !core.publishingLocked() && core.status.State == StateReady && !repochange.EpochCurrent(core.root.Path, core.epoch) && len(core.dirty) == 0 {
		core.full = true
	}
	// Incomplete watcher coverage requires periodic reconciliation.
	if !core.publishingLocked() && core.status.State == StateReady && !repochange.Coverage(core.root.watchRoot()).Complete &&
		time.Since(core.status.ValidatedAt) > repochange.CoverageRevalidationInterval {
		core.full = true
	}
	if !core.building && (core.full || len(core.dirty) > 0) {
		core.startRefresh(ctx, s, broker, epoch)
	}
	done, readable, building := core.done, core.readable, core.building
	core.mu.Unlock()
	if building && wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		// A nil readable channel leaves only reconciliation completion and timeout active.
		select {
		case <-ctx.Done():
			return TreeStatus{}, ctx.Err()
		case <-done:
		case <-readable:
		case <-timer.C:
		}
	}
	core.mu.Lock()
	status := core.status
	status.Instance = core.instance
	status.Refreshing = core.refreshingLocked()
	core.mu.Unlock()
	return status, nil
}

func (s *storeCore) startRefresh(ctx context.Context, store projectionStore, broker *backgroundwork.Broker, epoch repochange.Epoch) {
	if s.retired {
		return
	}
	buildCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	s.building = true
	s.settled = false
	s.done = make(chan struct{})
	if s.status.State != StateReady {
		if s.status.State != StateFailed {
			s.status.State = StateWarming
		}
		if store.partial() {
			s.readable = make(chan struct{})
		}
	}
	go refreshStore(buildCtx, store, broker, epoch)
}

// refreshStore runs reconciliation passes until the store agrees with the tree
// or the pass fails. Admission makes long discoveries yield their lane.
func refreshStore(ctx context.Context, s projectionStore, broker *backgroundwork.Broker, epoch repochange.Epoch) {
	core := s.core()
	core.mu.Lock()
	priority := backgroundwork.PriorityProactive
	if core.status.State != StateReady {
		priority = backgroundwork.PriorityInteractive
	}
	core.mu.Unlock()
	var err error
	// Index discovery uses shared directory observations, which admit each I/O quantum.
	if _, indexed := s.(*indexStore); !indexed {
		var release func()
		ctx, release, err = admitMetadata(ctx, broker, backgroundwork.Request{Key: s.workKey(), Epoch: epoch.Value, Lane: core.root.Path, Priority: priority, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata}})
		if err == nil {
			defer release()
		}
	}
	for {
		if err == nil {
			err = s.reconcile(ctx, epoch)
		}
		core.mu.Lock()
		if err == nil && ctx.Err() == nil && (core.full || len(core.dirty) > 0) {
			core.settled = false
			core.mu.Unlock()
			epoch = repochange.CurrentEpoch(core.root.Path)
			continue
		}
		if err != nil {
			core.full = true
			core.status.Error = err.Error()
			if core.status.State != StateReady {
				core.status.State = StateFailed
			}
		}
		core.building = false
		core.releaseReadableLocked()
		if core.cancel != nil {
			core.cancel()
			core.cancel = nil
		}
		close(core.done)
		core.mu.Unlock()
		return
	}
}

// settleEpoch records the epoch a completed pass observed, and asks for another
// pass when a write overtook it.
func (s *storeCore) settleEpoch(epoch repochange.Epoch) {
	s.epoch = epoch
	if repochange.EpochCurrent(s.root.Path, epoch) || len(s.dirty) > 0 {
		return
	}
	if repochange.EpochCurrent(s.root.Path, s.observed) {
		s.epoch = s.observed
		return
	}
	s.full = true
}

func (c *TreeStores) invalidateTrees(rootPath string, paths []string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.trees {
		core := s.core()
		changed, relevant := treeChangesForRoot(rootPath, core.root.Path, paths)
		if !relevant {
			continue
		}
		if index, ok := s.(*indexStore); ok {
			filtered := changed[:0]
			for _, rel := range changed {
				if index.policy.boundaryPath(rel, false) == "" {
					filtered = append(filtered, rel)
				}
			}
			if len(changed) > 0 && len(filtered) == 0 {
				continue
			}
			changed = filtered
		}
		core.mu.Lock()
		core.observed = repochange.CurrentEpoch(core.root.Path)
		if index, ok := s.(*indexStore); ok {
			index.invalidateObservationsLocked(changed)
		}
		changed, relevant = s.scopeChanges(changed)
		if !relevant {
			if !core.publishingLocked() && !core.full && len(core.dirty) == 0 {
				core.epoch = core.observed
			}
			core.mu.Unlock()
			continue
		}
		core.recordChanges(changed)
		if !core.building && core.status.State != "" {
			core.startRefresh(context.Background(), s, c.broker, repochange.CurrentEpoch(core.root.Path))
		}
		core.mu.Unlock()
	}
}

func (c *TreeStores) observeTreeEpoch(rootPath string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.trees {
		core := s.core()
		_, relevant := treeChangesForRoot(rootPath, core.root.Path, []string{"."})
		if !relevant {
			continue
		}
		core.mu.Lock()
		core.observed = repochange.CurrentEpoch(core.root.Path)
		if !core.publishingLocked() && !core.full && len(core.dirty) == 0 {
			core.epoch = core.observed
		}
		core.mu.Unlock()
	}
}

// Drain cancels catalog discovery, checkpoints, and content indexing and joins
// their workers. Existing generation pins remain readable until released.

func treeChangesForRoot(eventRoot, indexRoot string, paths []string) ([]string, bool) {
	eventRoot, indexRoot = cleanAbs(eventRoot), cleanAbs(indexRoot)
	if eventRoot == indexRoot {
		return paths, true
	}
	base, err := filepath.Rel(eventRoot, indexRoot)
	if err != nil || base == ".." || hasParentPrefix(base) {
		return nil, false
	}
	if len(paths) == 0 {
		return nil, true
	}
	var out []string
	for _, p := range paths {
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(eventRoot, filepath.FromSlash(p))
		}
		rel, e := filepath.Rel(indexRoot, abs)
		if e != nil {
			continue
		}
		if rel == "." || rel == ".." || hasParentPrefix(rel) {
			ancestor, e := filepath.Rel(abs, indexRoot)
			if e == nil && ancestor != ".." && !hasParentPrefix(ancestor) {
				return nil, true
			}
			continue
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out, len(out) > 0
}

// hasParentPrefix reports whether a relative path climbs out of its base.
func hasParentPrefix(rel string) bool {
	return strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
