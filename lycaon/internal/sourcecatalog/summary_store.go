package sourcecatalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/sandbox"
)

// TreeScope identifies one orientation projection of an attached root: which
// paths it admits, and whether nested repositories end it.
type TreeScope struct {
	Key            string
	Filter         sandbox.ReadFilter
	PruneNestedVCS bool
}

// summarySchema stores admitted paths and subtree aggregates for orientation.
const summarySchema = `
CREATE TABLE IF NOT EXISTS meta (
 id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL, validated INTEGER NOT NULL, complete INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS nodes (
 path TEXT PRIMARY KEY, parent TEXT NOT NULL, name TEXT NOT NULL, depth INTEGER NOT NULL,
 is_dir INTEGER NOT NULL, symlink INTEGER NOT NULL, vcs INTEGER NOT NULL,
 size INTEGER NOT NULL, mode INTEGER NOT NULL, modified INTEGER NOT NULL,
 files INTEGER NOT NULL, bytes INTEGER NOT NULL, children INTEGER NOT NULL,
 representative TEXT NOT NULL, rank TEXT NOT NULL,
 boundary INTEGER NOT NULL, pruned INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS child_page ON nodes(parent, boundary, rank);
CREATE INDEX IF NOT EXISTS source_page ON nodes(path) WHERE is_dir=0 AND boundary=0 AND symlink=0 AND files=1;
CREATE TABLE IF NOT EXISTS terms (term TEXT NOT NULL, path TEXT NOT NULL, PRIMARY KEY(term,path)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS term_path ON terms(path);
`

// summaryStore publishes complete generations so subtree aggregates stay consistent.
type summaryStore struct {
	storeCore
	scope TreeScope
}

func (s *summaryStore) schema() string { return summarySchema }
func (s *summaryStore) workKey() string {
	return s.projectID + ":" + s.root.ID + ":summary:" + s.scope.Key
}

// Literal blooms belong to the file index.
func (s *summaryStore) contentBuilds() []*contentBuild { return nil }

// Subtree aggregates require complete discovery.
func (s *summaryStore) partial() bool { return false }

// scopeChanges drops events outside the declared read projection before they
// can schedule discovery. Unknown root changes still require reconciliation.
func (s *summaryStore) scopeChanges(changed []string) ([]string, bool) {
	if len(changed) == 0 {
		return nil, true
	}
	paths, valid := reconciliationPaths(s.root, changed)
	if !valid {
		return nil, true
	}
	kept := paths[:0]
	for _, rel := range paths {
		if s.scope.PruneNestedVCS {
			parts := strings.Split(rel, "/")
			for depth, part := range parts {
				if sandbox.IsVCSDirBaseName(part) {
					if depth == 0 {
						return nil, true
					}
					rel = strings.Join(parts[:depth], "/")
					break
				}
			}
		}
		allowed := true
		for ancestor := rel; ancestor != "."; ancestor = path.Dir(ancestor) {
			if sandbox.ShouldSkipDir(ancestor, path.Base(ancestor)) {
				allowed = false
				break
			}
		}
		if allowed {
			kept = append(kept, rel)
		}
	}
	return kept, len(kept) > 0
}

func (c *Catalog) summaryStore(ctx context.Context, projectID string, root Root, scope TreeScope) (*summaryStore, error) {
	c.treeLifecycle.RLock()
	defer c.treeLifecycle.RUnlock()
	if scope.Key == "" {
		return nil, errors.New("catalog read scope requires an identity")
	}
	cleaned, err := cleanRoots([]Root{root})
	if err != nil {
		return nil, err
	}
	root = cleaned[0]
	key := "summary\x00" + rootKey(projectID, root) + "\x00" + scope.Key + fmt.Sprint(scope.PruneNestedVCS)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.trees == nil {
		c.trees = make(map[string]projectionStore)
	}
	if found, ok := c.trees[key].(*summaryStore); ok && found != nil {
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
	sum := sha256.Sum256([]byte(summarySchema + "\x00" + key))
	s := &summaryStore{
		storeCore: storeCore{
			projectID: projectID, root: root, file: filepath.Join(dir, hex.EncodeToString(sum[:])+treeFileSuffix),
			full: true, lastUsed: time.Now(), policy: c.policyFor(ctx, root.Path),
		},
		scope: scope,
	}
	c.trees[key] = s
	c.evictTreeStores(key)
	return s, nil
}

// SummaryReader pins one read transaction over a published summary generation.
type SummaryReader struct {
	db     *sql.DB
	tx     *sql.Tx
	Status TreeStatus
	// RowsRead counts rows this reader returned across its queries.
	RowsRead int
}

// Close releases the pinned catalog generation.
func (r *SummaryReader) Close() error {
	if r == nil {
		return nil
	}
	_ = r.tx.Rollback()
	return r.db.Close()
}

// OpenSummary returns a pinned orientation view, or warming while initial
// discovery continues in the background. Wait bounds joining the shared build,
// not its work.
func (c *Catalog) OpenSummary(ctx context.Context, projectID string, root Root, scope TreeScope, wait time.Duration) (*SummaryReader, TreeStatus, error) {
	s, err := c.summaryStore(ctx, projectID, root, scope)
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
	return &SummaryReader{db: db, tx: tx, Status: status}, status, nil
}
