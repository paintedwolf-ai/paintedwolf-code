package sourcecatalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// indexSchema stores admitted paths for file queries. Secondary indexes use
// row IDs to avoid repeating paths; the unique path column serves lookups.
const indexSchema = `
CREATE TABLE IF NOT EXISTS meta (
 id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL, validated INTEGER NOT NULL, complete INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS nodes (
 path TEXT NOT NULL UNIQUE, search_path TEXT NOT NULL, search_name TEXT NOT NULL,
 parent TEXT NOT NULL, name TEXT NOT NULL, depth INTEGER NOT NULL,
 indexed INTEGER NOT NULL DEFAULT 1, agent_metadata INTEGER NOT NULL DEFAULT 0, first_listed INTEGER NOT NULL DEFAULT 0,
 is_dir INTEGER NOT NULL, symlink INTEGER NOT NULL, regular INTEGER NOT NULL, hidden INTEGER NOT NULL,
 size INTEGER NOT NULL, mode INTEGER NOT NULL, modified INTEGER NOT NULL,
 -- refused records indexing boundaries independently of partial observations.
 refused TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS node_children ON nodes(parent, path);
CREATE INDEX IF NOT EXISTS admitted_nodes ON nodes(path) WHERE indexed=1;
CREATE INDEX IF NOT EXISTS refused_dirs ON nodes(refused) WHERE refused<>'';

CREATE INDEX IF NOT EXISTS visible_files ON nodes(path, search_path) WHERE indexed=1 AND regular=1 AND hidden=0;
CREATE INDEX IF NOT EXISTS all_files ON nodes(path, search_path, search_name) WHERE indexed=1 AND regular=1;
CREATE INDEX IF NOT EXISTS file_address ON nodes(length(CAST(path AS BLOB)), path) WHERE indexed=1 AND regular=1;
CREATE INDEX IF NOT EXISTS file_name ON nodes(search_name, path) WHERE indexed=1 AND regular=1;
CREATE INDEX IF NOT EXISTS folded_path ON nodes(lower(path)) WHERE indexed=1 AND regular=1;

CREATE TABLE IF NOT EXISTS frontier (id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE, deferred INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS frontier_order ON frontier(deferred, id);
CREATE TABLE IF NOT EXISTS faults (path TEXT PRIMARY KEY) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS charges (path TEXT PRIMARY KEY, observed INTEGER NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS totals (
 id INTEGER PRIMARY KEY CHECK(id=1), files INTEGER NOT NULL, observed INTEGER NOT NULL,
 visible_files INTEGER NOT NULL, agent_files INTEGER NOT NULL, agent_visible_files INTEGER NOT NULL);
INSERT OR IGNORE INTO totals VALUES(1,0,0,0,0,0);
CREATE TRIGGER IF NOT EXISTS totals_insert AFTER INSERT ON nodes
 BEGIN UPDATE totals SET
 files=files+new.regular*new.indexed,
 visible_files=visible_files+new.regular*new.indexed*(1-new.hidden),
 agent_files=agent_files+new.regular*new.indexed*(1-new.agent_metadata),
 agent_visible_files=agent_visible_files+new.regular*new.indexed*(1-new.hidden)*(1-new.agent_metadata) WHERE id=1; END;
CREATE TRIGGER IF NOT EXISTS totals_delete AFTER DELETE ON nodes
 BEGIN UPDATE totals SET
 files=files-old.regular*old.indexed,
 visible_files=visible_files-old.regular*old.indexed*(1-old.hidden),
 agent_files=agent_files-old.regular*old.indexed*(1-old.agent_metadata),
 agent_visible_files=agent_visible_files-old.regular*old.indexed*(1-old.hidden)*(1-old.agent_metadata) WHERE id=1; END;
CREATE TRIGGER IF NOT EXISTS totals_update AFTER UPDATE OF regular,indexed,hidden,agent_metadata ON nodes
 BEGIN UPDATE totals SET
 files=files+new.regular*new.indexed-old.regular*old.indexed,
 visible_files=visible_files+new.regular*new.indexed*(1-new.hidden)-old.regular*old.indexed*(1-old.hidden),
 agent_files=agent_files+new.regular*new.indexed*(1-new.agent_metadata)-old.regular*old.indexed*(1-old.agent_metadata),
 agent_visible_files=agent_visible_files+new.regular*new.indexed*(1-new.hidden)*(1-new.agent_metadata)-old.regular*old.indexed*(1-old.hidden)*(1-old.agent_metadata) WHERE id=1; END;

`

// The persistent frontier supports partial queries and resumes interrupted discovery.
type indexStore struct {
	pageCacheID     uint64
	writer          chan struct{}
	publicationGate chan struct{}
	structureFile   string
	structure       *structuralGeneration
	completed       *structuralGeneration
	checkpoint      structuralCheckpoint
	invalidation    observationInvalidation
	catalog         *Catalog
	inventory       inventoryWork
	navigation      navigationResources
	pins            generationPins
	collapse        collapseCache
	storeCore
	observations         pagedview.Preparation[string, DirectoryObservation]
	observationInterests observationInterests
	// content holds the per-projection bloom builds this index feeds.
	content map[string]*contentBuild
	// projections counts open ProjectionRows, whose rows live in this store's
	// presentation; guarded by mu. The store stays admitted while any is open.
	projections int
	// holders counts open views that keep this root resident between reads;
	// guarded by mu. Eviction passes over a held store.
	holders int
}

func (s *indexStore) schema() string { return indexSchema }

// Frontier batches are readable before discovery completes.
func (s *indexStore) partial() bool { return true }

func (s *indexStore) workKey() string { return s.projectID + ":" + s.root.ID + ":index" }

func (s *indexStore) contentBuilds() []*contentBuild {
	out := make([]*contentBuild, 0, len(s.content))
	for _, build := range s.content {
		out = append(out, build)
	}
	return out
}

// Search enrichment excludes VCS metadata independently of structural discovery.
func (s *indexStore) scopeChanges(changed []string) ([]string, bool) {
	if len(changed) == 0 {
		return nil, true
	}
	paths, valid := reconciliationPaths(s.root, changed)
	if !valid {
		return nil, true
	}
	kept := paths[:0]
	for _, rel := range paths {
		if !skippedIndexPath(rel) && validateObservationDirectory(s.root, rel) == nil {
			kept = append(kept, rel)
		}
	}
	return kept, len(kept) > 0
}

// Search enrichment does not traverse VCS metadata.
func skippedIndexPath(rel string) bool {
	for _, segment := range strings.Split(rel, "/") {
		if sandbox.IsVCSDirBaseName(segment) {
			return true
		}
	}
	return false
}

func agentMetadataPath(rel string) bool {
	for ancestor := rel; ancestor != "."; ancestor = path.Dir(ancestor) {
		if sandbox.ShouldSkipDir(ancestor, path.Base(ancestor)) {
			return true
		}
	}
	return false
}

// hiddenIndexPath reports whether any segment of rel is a hidden name.
func hiddenIndexPath(rel string) bool {
	for _, segment := range strings.Split(rel, "/") {
		if sandbox.IsHiddenName(segment) {
			return true
		}
	}
	return false
}

func (c *Catalog) indexStore(ctx context.Context, projectID string, root Root) (*indexStore, error) {
	c.treeLifecycle.RLock()
	defer c.treeLifecycle.RUnlock()
	cleaned, err := cleanRoots([]Root{root})
	if err != nil {
		return nil, err
	}
	root = cleaned[0]
	key := "index\x00" + rootKey(projectID, root)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.trees == nil {
		c.trees = make(map[string]projectionStore)
	}
	if found, ok := c.trees[key].(*indexStore); ok && found != nil {
		found.lastUsed = time.Now()
		return found, nil
	}
	dir, err := c.treeDirPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(indexSchema + "\x00" + key))
	// Projects attached to the same filesystem root share its structural checkpoint.
	structureSum := sha256.Sum256([]byte(structuralFormat + "\x00" + rangePageFormat + "\x00" + root.Path))
	s := &indexStore{catalog: c, pageCacheID: pageCacheSerial.Add(1), structureFile: filepath.Join(dir, hex.EncodeToString(structureSum[:])+structuralFileSuffix), storeCore: storeCore{
		projectID: projectID, root: root, file: filepath.Join(dir, hex.EncodeToString(sum[:])+treeFileSuffix),
		full: true, lastUsed: time.Now(), policy: c.policyFor(ctx, root.Path),
	}}
	s.invalidateObservationsLocked(nil)
	c.trees[key] = s
	c.evictTreeStores(key)
	return s, nil
}

// IndexReader pins one read transaction over a published file index generation.
type IndexReader struct {
	db     *sql.DB
	tx     *sql.Tx
	Status TreeStatus
	// RowsRead counts rows this reader returned across its queries.
	RowsRead int
	store    *indexStore
}

// Close releases the pinned generation.
func (r *IndexReader) Close() error {
	if r == nil {
		return nil
	}
	_ = r.tx.Rollback()
	return r.db.Close()
}

// IndexStatus reads publication metadata without opening a reader.
func (c *Catalog) IndexStatus(ctx context.Context, projectID string, root Root) (TreeStatus, error) {
	s, err := c.indexStore(ctx, projectID, root)
	if err != nil {
		return TreeStatus{}, err
	}
	return openStore(ctx, s, 0, c.broker)
}

// RootFiles is what a root's index knows about its file population.
type RootFiles struct {
	// Count is the regular files the published generation holds.
	Count int
	// Measured makes Count a complete tree count. Otherwise the population is unknown.
	Measured bool
}

// RootFileCount counts files through the shared index, starting discovery if needed.
// Wait limits the caller's wait without canceling discovery.
func (c *Catalog) RootFileCount(ctx context.Context, projectID string, root Root, scope FileScope, wait time.Duration) (RootFiles, error) {
	reader, _, err := c.OpenIndex(ctx, projectID, root, wait)
	if err != nil || reader == nil {
		return RootFiles{}, err
	}
	defer func() { _ = reader.Close() }()
	count, err := reader.FileCount(ctx, scope)
	if err != nil {
		return RootFiles{}, err
	}
	coverage, err := reader.Coverage(ctx)
	if err != nil {
		return RootFiles{}, err
	}
	return RootFiles{Count: count, Measured: coverage.Exhaustive()}, nil
}

// OpenIndex returns the latest generation, or nil before the first batch.
// Status.Complete marks discovery completion; Coverage reports omissions.
// Wait limits waiting without canceling discovery.
func (c *Catalog) OpenIndex(ctx context.Context, projectID string, root Root, wait time.Duration) (*IndexReader, TreeStatus, error) {
	s, err := c.indexStore(ctx, projectID, root)
	if err != nil {
		return nil, TreeStatus{}, err
	}
	status, err := openStore(ctx, s, wait, c.broker)
	if err != nil || status.State != StateReady {
		return nil, status, err
	}
	db, tx, status, err := s.readTx(ctx, status)
	if err != nil {
		return nil, status, err
	}
	return &IndexReader{db: db, tx: tx, Status: status, store: s}, status, nil
}
