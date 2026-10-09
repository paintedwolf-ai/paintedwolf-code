package sourcecatalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/textrank"
)

const treeColumns = "path,parent,name,depth,is_dir,symlink,vcs,size,mode,modified,files,bytes,children,representative,boundary,pruned"

// ErrTreeCursor means a continuation no longer identifies the requested page.
var ErrTreeCursor = errors.New("catalog continuation is invalid")

type treeScanner interface{ Scan(...any) error }

func scanTreeNode(row treeScanner) (TreeNode, error) {
	var n TreeNode
	var modified int64
	err := row.Scan(&n.Path, &n.Parent, &n.Name, &n.Depth, &n.IsDir, &n.IsSymlink, &n.IsVCSRoot, &n.Size, &n.Mode, &modified, &n.Files, &n.Bytes, &n.Children, &n.Representative, &n.Boundary, &n.Pruned)
	n.Modified = time.Unix(0, modified).UTC()
	return n, err
}

func readTreeNode(ctx context.Context, tx *sql.Tx, rel string) (TreeNode, error) {
	return scanTreeNode(tx.QueryRowContext(ctx, "SELECT "+treeColumns+" FROM nodes WHERE path=?", rel))
}

// Node reads one indexed path and its subtree aggregates.
func (r *SummaryReader) Node(ctx context.Context, rel string) (TreeNode, error) {
	r.RowsRead++
	return readTreeNode(ctx, r.tx, normalizeDir(rel))
}

// TreePage carries a bounded child selection and exact unreturned material.
type TreePage struct {
	Nodes             []TreeNode
	RemainingFiles    int
	RemainingBytes    int64
	RemainingChildren int
	Next              string
}

type treePosition struct {
	Promoted []string
	Plan     string
	Parent   string
	After    string
	Seen     int
	Files    int
	Bytes    int64
}

// Page uses indexed keyset pagination. Task and documentation candidates have
// a bounded reserved share; canonical pages omit those same promoted entries.
func (r *SummaryReader) Page(ctx context.Context, parent TreeNode, cursor, task string, preferred []string, limit int) (TreePage, error) {
	if limit <= 0 {
		return TreePage{}, errors.New("catalog page requires a positive limit")
	}
	position := treePosition{Parent: parent.Path}
	planRaw, err := json.Marshal(struct {
		Task  string
		Limit int
	}{task, limit})
	if err != nil {
		return TreePage{}, err
	}
	plan := sha256.Sum256(planRaw)
	position.Plan = base64.RawURLEncoding.EncodeToString(plan[:])
	expectedPlan := position.Plan
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return TreePage{}, fmt.Errorf("%w: %w", ErrTreeCursor, err)
		}
		if err = json.Unmarshal(raw, &position); err != nil {
			return TreePage{}, fmt.Errorf("%w: %w", ErrTreeCursor, err)
		}
		if position.Plan != expectedPlan || position.Parent != parent.Path || position.Seen < 0 || position.Seen > parent.Children {
			return TreePage{}, ErrTreeCursor
		}
	}
	promoted, err := r.pagePromotions(ctx, parent.Path, task, preferred, cursor == "", &position, min(4, max(1, limit/2)))
	if err != nil {
		return TreePage{}, err
	}
	out := TreePage{}
	if cursor == "" {
		out.Nodes = append(out.Nodes, promoted...)
	}
	query := "SELECT " + treeColumns + " FROM nodes WHERE parent=? AND boundary=0 AND rank>?"
	args := []any{parent.Path, position.After}
	for _, n := range promoted {
		query += " AND path<>?"
		args = append(args, n.Path)
	}
	query += " ORDER BY rank LIMIT ?"
	args = append(args, limit-len(out.Nodes)+1)
	rows, err := r.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return TreePage{}, err
	}
	defer func() { _ = rows.Close() }()
	more := false
	for rows.Next() {
		n, scanErr := scanTreeNode(rows)
		if scanErr != nil {
			return TreePage{}, scanErr
		}
		r.RowsRead++
		if len(out.Nodes) == limit {
			more = true
			break
		}
		out.Nodes = append(out.Nodes, n)
		position.After = treeRank(n)
	}
	if err := rows.Err(); err != nil {
		return TreePage{}, err
	}
	for _, n := range out.Nodes {
		position.Seen++
		position.Files += n.Files
		position.Bytes += n.Bytes
	}
	out.RemainingChildren = parent.Children - position.Seen
	out.RemainingFiles = parent.Files - position.Files
	out.RemainingBytes = parent.Bytes - position.Bytes
	if more {
		raw, marshalErr := json.Marshal(position)
		if marshalErr != nil {
			return TreePage{}, marshalErr
		}
		out.Next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, nil
}

func (r *SummaryReader) pagePromotions(ctx context.Context, parent, task string, preferred []string, first bool, position *treePosition, limit int) ([]TreeNode, error) {
	if first {
		nodes, err := r.promoted(ctx, parent, task, preferred, limit)
		for _, n := range nodes {
			position.Promoted = append(position.Promoted, n.Path)
		}
		return nodes, err
	}
	if len(position.Promoted) > limit {
		return nil, ErrTreeCursor
	}
	nodes := make([]TreeNode, 0, len(position.Promoted))
	for _, p := range position.Promoted {
		n, err := r.Node(ctx, p)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTreeCursor
		}
		if err != nil {
			return nil, err
		}
		if n.Parent != parent || n.Boundary {
			return nil, ErrTreeCursor
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func (r *SummaryReader) promoted(ctx context.Context, parent, task string, preferred []string, limit int) ([]TreeNode, error) {
	out := make([]TreeNode, 0, limit)
	seen := map[string]bool{}
	add := func(candidate string) error {
		if len(out) == limit || !treeUnder(candidate, parent) || candidate == parent {
			return nil
		}
		rel := candidate
		if parent != "." {
			rel = strings.TrimPrefix(candidate, parent+"/")
		}
		child := path.Join(parent, strings.SplitN(rel, "/", 2)[0])
		if seen[child] {
			return nil
		}
		seen[child] = true
		n, err := r.Node(ctx, child)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err == nil && !n.Boundary {
			out = append(out, n)
		}
		return err
	}
	for _, p := range preferred {
		if err := add(p); err != nil {
			return nil, err
		}
		if len(out) == limit {
			return out, nil
		}
	}
	terms := textrank.AnalyzeQuery(task, true, true)
	for _, term := range terms[:min(len(terms), 8)] {
		candidates, err := r.termPaths(ctx, parent, term, limit)
		if err != nil {
			return nil, err
		}
		for _, p := range candidates {
			if err := add(p); err != nil {
				return nil, err
			}
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (r *SummaryReader) termPaths(ctx context.Context, parent, term string, limit int) ([]string, error) {
	lo, hi := "", "\U0010ffff"
	if parent != "." {
		lo, hi = parent+"/", parent+"0"
	}
	rows, err := r.tx.QueryContext(ctx, "SELECT path FROM terms WHERE term=? AND path>=? AND path<? ORDER BY path LIMIT ?", term, lo, hi, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
		r.RowsRead++
	}
	return paths, rows.Err()
}

const summaryFilesPageQuery = "SELECT " + treeColumns + " FROM nodes INDEXED BY source_page WHERE is_dir=0 AND boundary=0 AND symlink=0 AND files=1 AND path>? AND path<? ORDER BY path LIMIT ?"

// FilesPage reads a bounded lexical page of regular source candidates in a scope.
// After is exclusive; callers bind it to the scope and published generation.
func (r *SummaryReader) FilesPage(ctx context.Context, base, after string, limit int) ([]TreeNode, bool, error) {
	if limit <= 0 {
		return nil, false, errors.New("catalog file page requires a positive limit")
	}
	lo, hi := "", "\U0010ffff"
	if base != "." {
		lo, hi = base+"/", base+"0"
	}
	rows, err := r.tx.QueryContext(ctx, summaryFilesPageQuery, max(lo, after), hi, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	var out []TreeNode
	for rows.Next() {
		n, err := scanTreeNode(rows)
		if err != nil {
			return nil, false, err
		}
		r.RowsRead++
		if len(out) == limit {
			return out, true, nil
		}
		out = append(out, n)
	}
	return out, false, rows.Err()
}

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

func (c *TreeStores) summaryStore(ctx context.Context, projectID string, root Root, scope TreeScope) (*summaryStore, error) {
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
	cleanup  func() error
}

// Close releases the pinned catalog generation.
func (r *SummaryReader) Close() error {
	if r == nil {
		return nil
	}
	_ = r.tx.Rollback()
	err := r.db.Close()
	if r.cleanup != nil {
		err = errors.Join(err, r.cleanup())
	}
	return err
}

// OpenSummary returns a pinned orientation view, or warming while initial
// discovery continues in the background. Wait bounds joining the shared build,
// not its work.
func (c *TreeStores) OpenSummary(ctx context.Context, projectID string, root Root, scope TreeScope, wait time.Duration) (*SummaryReader, TreeStatus, error) {
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

// OpenDependencySummary owns a fresh admitted orientation projection until Close.
func (c *TreeStores) OpenDependencySummary(ctx context.Context, projectID string, root Root, scope TreeScope) (*SummaryReader, TreeStatus, error) {
	dir, err := os.MkdirTemp("", "paintedwolf-dependency-summary-*")
	if err != nil {
		return nil, TreeStatus{}, err
	}
	temporary := New()
	temporary.Trees.treeDir, temporary.Trees.broker = dir, c.broker
	var once sync.Once
	var cleanupErr error
	cleanup := func() error {
		once.Do(func() { cleanupErr = errors.Join(temporary.Drain(context.Background()), os.RemoveAll(dir)) })
		return cleanupErr
	}
	store, err := temporary.Trees.summaryStore(ctx, projectID, root, scope)
	if err != nil {
		_ = cleanup()
		return nil, TreeStatus{}, err
	}
	if err = loadStore(ctx, store); err != nil {
		_ = cleanup()
		return nil, TreeStatus{}, err
	}
	store.policy = c.policyFor(ctx, root.Path)
	store.policy.includeDependencies = true
	if err = store.reconcile(ctx, repochange.CurrentEpoch(root.Path)); err != nil {
		_ = cleanup()
		return nil, TreeStatus{}, err
	}
	database, transaction, status, err := store.readTx(ctx, store.status)
	if err != nil {
		_ = cleanup()
		return nil, status, err
	}
	return &SummaryReader{db: database, tx: transaction, Status: status, cleanup: cleanup}, status, nil
}
