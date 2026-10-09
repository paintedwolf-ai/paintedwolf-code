package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
)

// treeTouchInterval bounds how often an open stamps last use on the file.
const treeTouchInterval = 24 * time.Hour

// Journal companions count toward the generation's disk budget.
var treeSidecarSuffixes = []string{"-wal", "-shm", "-journal"}

// Structural scratch is unlinked as it is created, so one still visible this
// long after its last write was left behind by an interrupted engine.
const structuralScratchGrace = time.Minute

// ReconcileTreeStores expires unused generations and reclaims inactive storage toward the retention target.
func (c *TreeStores) ReconcileTreeStores(ctx context.Context) (int, error) {
	return c.reconcileTreeStores(ctx, defaultTreeStorePolicy())
}

func (c *TreeStores) reconcileTreeStores(ctx context.Context, policy treeStorePolicy) (int, error) {
	if c == nil || policy.retention <= 0 {
		return 0, nil
	}
	c.treeLifecycle.Lock()
	defer c.treeLifecycle.Unlock()
	dir, err := c.treeDirPath()
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	open := make(map[string]struct{}, len(c.trees))
	for _, s := range c.trees {
		open[s.core().file] = struct{}{}
		if index, ok := s.(*indexStore); ok {
			open[index.structureFile] = struct{}{}
		}
	}
	c.mu.Unlock()
	cutoff := time.Now().Add(-policy.retention)
	removed := 0
	var errs []error
	type generation struct {
		file     string
		lastUsed time.Time
		bytes    int64
	}
	var kept []generation
	var total int64
	scratch := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		name := entry.Name()
		if entry.IsDir() {
			continue
		}
		file := filepath.Join(dir, name)
		if strings.HasPrefix(name, "structural-") && strings.HasSuffix(name, ".tmp") {
			info, err := entry.Info()
			if err != nil || time.Since(info.ModTime()) < structuralScratchGrace {
				continue
			}
			if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
				continue
			}
			scratch++
			continue
		}
		if !strings.HasSuffix(name, treeFileSuffix) && !strings.HasSuffix(name, structuralFileSuffix) {
			continue
		}
		if _, live := open[file]; live {
			total += treeStoreBytes(file)
			continue
		}
		if lastTreeUse(file).Before(cutoff) {
			if err := removeTreeStore(file); err != nil {
				errs = append(errs, err)
				continue
			}
			removed++
			continue
		}
		if err := checkpointTreeStore(ctx, file, policy.vacuumPages); err != nil {
			errs = append(errs, err)
		}
		size := treeStoreBytes(file)
		total += size
		kept = append(kept, generation{file: file, lastUsed: lastTreeUse(file), bytes: size})
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].lastUsed.Before(kept[j].lastUsed) })
	for _, g := range kept {
		if total <= policy.maxBytes {
			break
		}
		if err := removeTreeStore(g.file); err != nil {
			errs = append(errs, err)
			continue
		}
		total -= g.bytes
		removed++
	}
	if scratch > 0 {
		slog.DebugContext(ctx, "Removed orphaned structural scratch", "files", scratch)
	}
	return removed, errors.Join(errs...)
}

// treeStoreBytes is the size of a generation with its sidecars.
func treeStoreBytes(file string) int64 {
	var total int64
	for _, candidate := range append([]string{file}, treeSidecarPaths(file)...) {
		if info, err := os.Stat(candidate); err == nil {
			total += info.Size()
		}
	}
	return total
}

// checkpointTreeStore reclaims the write-ahead log of an idle generation.
func checkpointTreeStore(ctx context.Context, file string, vacuumPages int) error {
	if strings.HasSuffix(file, structuralFileSuffix) {
		return nil
	}
	db, err := openTreeDB(ctx, file)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA incremental_vacuum(%d)", vacuumPages)); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// ClearTreeStores drains all cache writers and runs clearStorage while new
// stores are excluded. Existing generation pins keep their immutable storage
// until the last reader releases them.
func (c *TreeStores) ClearTreeStores(ctx context.Context, clearStorage func() error) error {
	if c == nil {
		return nil
	}
	c.treeLifecycle.Lock()
	defer c.treeLifecycle.Unlock()
	cleanupCtx := context.WithoutCancel(ctx)
	c.mu.Lock()
	for _, store := range c.trees {
		store.core().mu.Lock()
		store.core().retired = true
		store.core().mu.Unlock()
	}
	c.mu.Unlock()
	if err := c.Drain(ctx); err != nil {
		// Retirement is irreversible. Finish joining canceled writers before
		// admitting a replacement store, while preserving the caller's error.
		_ = c.Drain(cleanupCtx)
		releaseWriters := c.lockTreeWriters(cleanupCtx)
		c.mu.Lock()
		c.trees = nil
		c.mu.Unlock()
		releaseWriters()
		return err
	}
	releaseWriters := c.lockTreeWriters(cleanupCtx)
	defer releaseWriters()
	c.mu.Lock()
	c.trees = nil
	var err error
	if clearStorage != nil {
		err = clearStorage()
	}
	c.mu.Unlock()
	return err
}

// ReleaseTreeRoot retires rebuildable stores after the last project detaches a
// filesystem root. Readers that already pinned a generation keep their open
// handles; no new writer can enter the retired stores.
func (c *TreeStores) ReleaseTreeRoot(ctx context.Context, rootPath string) error {
	if c == nil {
		return nil
	}
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	if rootPath == "" || rootPath == "." {
		return nil
	}
	c.treeLifecycle.Lock()
	defer c.treeLifecycle.Unlock()

	var pending []<-chan struct{}
	var files []string
	var retiredStores []projectionStore
	c.mu.Lock()
	for key, store := range c.trees {
		core := store.core()
		if filepath.Clean(core.root.Path) != rootPath {
			continue
		}
		core.mu.Lock()
		core.retired = true
		if index, ok := store.(*indexStore); ok {
			if index.inventory.cancel != nil {
				index.inventory.cancel()
				pending = append(pending, index.inventory.done)
			}
			pending = append(pending, index.observations.CancelAll()...)
			if done := index.checkpoint.Drain(); done != nil {
				pending = append(pending, done)
			}
			index.pins.drained = true
			if index.structure != nil && index.pins.held[index.structure.id] == nil {
				index.structure.close()
				index.structure = nil
			}
			index.releaseCompletedStructureLocked()
			if idle := index.navigation.Drain(); idle != nil {
				pending = append(pending, idle)
			}
			files = append(files, index.structureFile)
		}
		if core.building {
			core.cancel()
			pending = append(pending, core.done)
		}
		for _, build := range store.contentBuilds() {
			build.cancel()
			pending = append(pending, build.done)
		}
		files = append(files, core.file)
		retiredStores = append(retiredStores, store)
		core.mu.Unlock()
		delete(c.trees, key)
	}
	c.mu.Unlock()
	var cancelErr error
	for _, done := range pending {
		if cancelErr == nil {
			select {
			case <-ctx.Done():
				cancelErr = ctx.Err()
			case <-done:
				continue
			}
		}
		<-done
	}
	// Retirement cannot be rolled back. Preserve context values while joining
	// writers even when the initiating request has been canceled.
	releaseWriters := lockProjectionWriters(context.WithoutCancel(ctx), retiredStores)
	defer releaseWriters()
	if cancelErr != nil {
		return cancelErr
	}
	var errs []error
	for _, file := range files {
		if err := removeTreeStore(file); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *TreeStores) lockTreeWriters(ctx context.Context) func() {
	c.mu.Lock()
	stores := make([]projectionStore, 0, len(c.trees))
	for _, store := range c.trees {
		stores = append(stores, store)
	}
	c.mu.Unlock()
	return lockProjectionWriters(ctx, stores)
}

func lockProjectionWriters(ctx context.Context, stores []projectionStore) func() {
	var releases []func()
	for _, store := range stores {
		index, ok := store.(*indexStore)
		if !ok {
			continue
		}
		release, err := index.writer.Lock(ctx)
		if err == nil {
			releases = append(releases, release)
		}
	}
	return func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
}

// lastTreeUse is the newest modification among a generation's files.
func lastTreeUse(file string) time.Time {
	var newest time.Time
	for _, candidate := range append([]string{file}, treeSidecarPaths(file)...) {
		if info, err := os.Stat(candidate); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

func treeSidecarPaths(file string) []string {
	out := make([]string, 0, len(treeSidecarSuffixes))
	for _, suffix := range treeSidecarSuffixes {
		out = append(out, file+suffix)
	}
	return out
}

func removeTreeStore(file string) error {
	var errs []error
	for _, candidate := range append([]string{file}, treeSidecarPaths(file)...) {
		if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Rate-limited timestamps keep actively read generations eligible for retention.
func (s *storeCore) touch(now time.Time) {
	if now.Sub(s.touched) < treeTouchInterval {
		return
	}
	s.touched = now
	_ = os.Chtimes(s.file, now, now)
}

// SuspendProjectStores retires in-memory projection stores for a parked project
// while keeping the persisted SQLite generation files intact on disk.
func (c *TreeStores) SuspendProjectStores(projectID string) {
	if c == nil {
		return
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, s := range c.trees {
		core := s.core()
		if core.projectID == projectID {
			if retireTreeStore(s) {
				delete(c.trees, key)
			}
		}
	}
}

func (c *TreeStores) evictTreeStores(keep string) {
	limit := max(1, c.limit)
	for len(c.trees) > limit {
		oldest := ""
		for key, s := range c.trees {
			core := s.core()
			core.mu.Lock()
			eligible := key != keep && treeStoreEvictableLocked(s)
			core.mu.Unlock()
			if eligible && (oldest == "" || core.lastUsed.Before(c.trees[oldest].core().lastUsed)) {
				oldest = key
			}
		}
		if oldest == "" {
			return
		}
		if !retireTreeStore(c.trees[oldest]) {
			continue
		}
		delete(c.trees, oldest)
	}
}

// Drain cancels in-flight catalog builds and waits for them and the tree
// stores to settle, or for ctx to end.
func (c *Catalog) Drain(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	var pending []<-chan struct{}
	for _, rec := range c.records {
		if rec.building {
			rec.cancel()
			pending = append(pending, rec.done)
		}
	}

	c.mu.Unlock()
	for _, done := range pending {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
		}
	}
	return c.Trees.Drain(ctx)
}

func (c *TreeStores) Drain(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	var pending []<-chan struct{}
	for _, s := range c.trees {
		core := s.core()
		core.mu.Lock()
		if index, ok := s.(*indexStore); ok {
			if index.inventory.cancel != nil {
				index.inventory.cancel()
				pending = append(pending, index.inventory.done)
			}
			pending = append(pending, index.observations.CancelAll()...)
			if done := index.checkpoint.Drain(); done != nil {
				pending = append(pending, done)
			}
			index.pins.drained = true
			index.releaseCompletedStructureLocked()
			if index.structure != nil && index.pins.held[index.structure.id] == nil {
				index.structure.close()
				index.structure = nil
			}
			if idle := index.navigation.Drain(); idle != nil {
				pending = append(pending, idle)
			}
		}
		if core.building {
			core.cancel()
			pending = append(pending, core.done)
		}
		for _, build := range s.contentBuilds() {
			build.cancel()
			pending = append(pending, build.done)
		}
		core.mu.Unlock()
	}
	c.mu.Unlock()
	for _, done := range pending {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
		}
	}
	return nil
}

// TreeStores owns rebuildable disk generations and their writer lifetimes.
type TreeStores struct {
	mu            sync.Mutex
	trees         map[string]projectionStore
	limit         int
	treeDir       string
	treeLifecycle sync.RWMutex
	broker        *backgroundwork.Broker
	Directories   *Directories
	scopesMu      sync.RWMutex
	scopes        ScopeProvider
}
