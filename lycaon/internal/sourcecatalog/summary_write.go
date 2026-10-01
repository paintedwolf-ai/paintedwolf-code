package sourcecatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/textrank"
)

// TreeNode includes the exact material represented below one indexed path.
type TreeNode struct {
	Boundary bool
	Pruned   int
	Entry
	Files          int
	Bytes          int64
	Children       int
	Representative string
}

// summaryWriter writes changed nodes and tracks visited paths for sweeping.
type summaryWriter struct {
	root  *os.Root
	tx    *sql.Tx
	store *summaryStore
	// budget accounts this reconciliation against the store's policy.
	budget *sandbox.WalkBudget
	get    *sql.Stmt
	put    *sql.Stmt
	term   *sql.Stmt
	seen   *sql.Stmt
	// RowsWritten counts node rows that reached the database.
	RowsWritten int
	// RowsVisited counts nodes compared against the stored generation.
	RowsVisited int
}

// reconcile rebuilds the orientation projection in one transaction and
// publishes a complete generation or none.
func (s *summaryStore) reconcile(ctx context.Context, epoch repochange.Epoch) error {
	full, changed := s.takeWork()
	db, err := openTreeDB(ctx, s.file)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	w, err := newSummaryWriter(ctx, tx, s)
	if err != nil {
		return err
	}
	defer w.close()
	paths, valid := reconciliationPaths(s.root, changed)
	if full || !valid {
		if _, err = w.scan(ctx, "."); err != nil {
			return err
		}
		err = w.sweep(ctx, ".")
	} else {
		err = w.updatePaths(ctx, paths)
	}
	if err != nil {
		return err
	}
	status, err := publishGeneration(ctx, tx, true)
	if err != nil {
		return err
	}
	truncateTreeWAL(ctx, s.file)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishFinalLocked(status, epoch)
	return nil
}

func newSummaryWriter(ctx context.Context, tx *sql.Tx, s *summaryStore) (*summaryWriter, error) {
	w := &summaryWriter{tx: tx, store: s, budget: sandbox.NewWalkBudget(s.policy.budgets)}
	var err error
	w.root, err = os.OpenRoot(s.root.Path)
	if err != nil {
		return nil, err
	}
	// The visited set lives with the connection; the transaction owns it.
	if _, err = tx.ExecContext(ctx, "CREATE TEMP TABLE IF NOT EXISTS seen(path TEXT PRIMARY KEY) WITHOUT ROWID; DELETE FROM temp.seen"); err != nil {
		_ = w.root.Close()
		return nil, err
	}
	statements := []struct {
		dst *(*sql.Stmt)
		sql string
	}{
		{&w.get, "SELECT " + treeColumns + " FROM nodes WHERE path=?"},
		{&w.put, `INSERT OR REPLACE INTO nodes(path,parent,name,depth,is_dir,symlink,vcs,size,mode,modified,files,bytes,children,representative,rank,boundary,pruned) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`},
		{&w.term, "INSERT OR IGNORE INTO terms(term,path) VALUES(?,?)"},
		{&w.seen, "INSERT OR IGNORE INTO temp.seen(path) VALUES(?)"},
	}
	for _, statement := range statements {
		*statement.dst, err = tx.PrepareContext(ctx, statement.sql) //nolint:sqlclosecheck // summaryWriter.close owns these statements.
		if err != nil {
			w.close()
			return nil, err
		}
	}
	return w, nil
}

func (w *summaryWriter) close() {
	for _, stmt := range []*sql.Stmt{w.get, w.put, w.term, w.seen} {
		if stmt != nil {
			_ = stmt.Close()
		}
	}
	_ = w.root.Close()
}

// write tracks visited nodes and updates changed rows. Path terms change only with boundaries.
func (w *summaryWriter) write(ctx context.Context, n TreeNode) error {
	if _, err := w.seen.ExecContext(ctx, n.Path); err != nil {
		return err
	}
	old, err := scanTreeNode(w.get.QueryRowContext(ctx, n.Path))
	stored := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	w.RowsVisited++
	if stored && sameTreeNode(old, n) {
		return nil
	}
	_, err = w.put.ExecContext(ctx, n.Path, n.Parent, n.Name, n.Depth, n.IsDir, n.IsSymlink, n.IsVCSRoot,
		n.Size, n.Mode, n.Modified.UnixNano(), n.Files, n.Bytes, n.Children, n.Representative, treeRank(n), n.Boundary, n.Pruned)
	if err != nil {
		return err
	}
	w.RowsWritten++
	if n.Boundary || (stored && !old.Boundary) {
		return nil
	}
	for _, term := range textrank.AnalyzeQuery(n.Name, true, true) {
		if _, err = w.term.ExecContext(ctx, term, n.Path); err != nil {
			return err
		}
	}
	return nil
}

// Rank is derived from the compared fields.
func sameTreeNode(a, b TreeNode) bool {
	return a.Parent == b.Parent && a.Name == b.Name && a.Depth == b.Depth &&
		a.IsDir == b.IsDir && a.IsSymlink == b.IsSymlink && a.IsVCSRoot == b.IsVCSRoot &&
		a.Size == b.Size && a.Mode == b.Mode && a.Modified.Equal(b.Modified) &&
		a.Files == b.Files && a.Bytes == b.Bytes && a.Children == b.Children &&
		a.Representative == b.Representative && a.Boundary == b.Boundary && a.Pruned == b.Pruned
}

// sweep removes the stored rows below rel that this walk did not visit. The
// root sweeps the whole generation.
func (w *summaryWriter) sweep(ctx context.Context, rel string) error {
	scope, args := "", []any{}
	if rel != "." {
		scope = " AND (path=? OR (path>=? AND path<?))"
		args = []any{rel, rel + "/", rel + "0"}
	}
	vanished := "SELECT path FROM nodes WHERE path NOT IN (SELECT path FROM temp.seen)" + scope
	// #nosec G202 -- scope is an internal SQL template; values are bound parameters.
	if _, err := w.tx.ExecContext(ctx, "DELETE FROM terms WHERE path IN ("+vanished+")", args...); err != nil {
		return err
	}
	// #nosec G202 -- scope is an internal SQL template; values are bound parameters.
	_, err := w.tx.ExecContext(ctx, "DELETE FROM nodes WHERE path NOT IN (SELECT path FROM temp.seen)"+scope, args...)
	return err
}

// scan traverses directories under the walk and subtree budgets.
func (w *summaryWriter) scan(ctx context.Context, rel string) (TreeNode, error) {
	if repochange.IsPrivatePath(filepath.Join(w.root.Name(), filepath.FromSlash(rel))) {
		return TreeNode{}, nil
	}
	if err := nextMetadataEntry(ctx); err != nil {
		return TreeNode{}, err
	}
	info, err := w.root.Lstat(filepath.FromSlash(rel))
	if errors.Is(err, os.ErrNotExist) {
		return TreeNode{}, nil
	}
	if err != nil {
		return TreeNode{}, err
	}
	n := TreeNode{Entry: w.entry(rel, info)}
	if rel == "." {
		n.Parent = ""
	}
	if rel != "." && (sandbox.ShouldSkipDir(rel, n.Name) || (w.store.scope.Filter != nil && !w.store.scope.Filter(rel, n.IsDir))) {
		return TreeNode{}, nil
	}
	if rel != "." && w.store.scope.PruneNestedVCS && n.IsVCSRoot {
		n.Boundary = true
		n.Pruned = 1
		return n, w.write(ctx, n)
	}
	if !n.IsDir {
		if info.Mode().IsRegular() {
			n.Files = 1
			n.Bytes = n.Size
			n.Representative = rel
		}
		return n, w.write(ctx, n)
	}
	if w.budget.Exhausted() {
		// The walk budget is spent: the directory is known, its subtree is not.
		n.Boundary = true
		n.Pruned = 1
		return n, w.write(ctx, n)
	}
	f, err := w.root.Open(filepath.FromSlash(rel))
	if err != nil {
		return TreeNode{}, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return TreeNode{}, err
	}
	if !os.SameFile(info, opened) {
		return TreeNode{}, errors.New("catalog directory changed during discovery")
	}
	entries, overflow, err := readTreeListing(f, w.budget.ListingLimit())
	if err != nil {
		return TreeNode{}, err
	}
	if overflow {
		// The directory exceeds its entry cap.
		n.Boundary = true
		n.Pruned = 1
		return n, w.write(ctx, n)
	}
	depth := w.budget.EnterDir()
	defer w.budget.LeaveDir()
	bestRank := ""
	for _, entry := range w.ordered(rel, entries) {
		if cut, _ := w.budget.Charge(1); cut >= 0 {
			if cut == 0 || cut == depth {
				// Retain observed entries and mark the remaining subtree as a boundary.
				n.Boundary = true
				break
			}
			return TreeNode{}, treeCut{depth: cut}
		}
		child, childErr := w.scan(ctx, path.Join(rel, entry.Name()))
		if childErr != nil {
			var cut treeCut
			if errors.As(childErr, &cut) && cut.depth == depth {
				n.Boundary = true
				break
			}
			return TreeNode{}, childErr
		}
		n.Pruned += child.Pruned
		if child.Path == "" || child.Boundary {
			continue
		}
		n.Children++
		n.Files += child.Files
		n.Bytes += child.Bytes
		if child.Representative != "" && (bestRank == "" || treeRank(child) < bestRank) {
			n.Representative = child.Representative
			bestRank = treeRank(child)
		}
	}
	return n, w.write(ctx, n)
}

// treeCut unwinds a scan to the ancestor whose subtree cap fell.
type treeCut struct{ depth int }

func (c treeCut) Error() string { return "catalog subtree cap reached" }

// readTreeListing sorts a directory within its cap and reports overflow.
func readTreeListing(f *os.File, limit int) ([]os.DirEntry, bool, error) {
	entries := make([]os.DirEntry, 0, 64)
	for {
		batch := 256
		if limit > 0 && limit-len(entries) < batch {
			batch = limit - len(entries)
		}
		if batch <= 0 {
			break
		}
		chunk, readErr := f.ReadDir(batch)
		entries = append(entries, chunk...)
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			if errors.Is(readErr, os.ErrClosed) {
				return nil, false, readErr
			}
			return nil, false, readErr
		}
		if limit > 0 && len(entries) >= limit {
			return entries, true, nil
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, false, nil
}

// ordered visits the directories the policy defers after their siblings.
func (w *summaryWriter) ordered(rel string, entries []os.DirEntry) []os.DirEntry {
	if w.store.policy.scope == nil {
		return entries
	}
	var deferred []os.DirEntry
	kept := entries[:0]
	for _, entry := range entries {
		if entry.IsDir() && w.store.policy.deferDir(path.Join(rel, entry.Name())) {
			deferred = append(deferred, entry)
			continue
		}
		kept = append(kept, entry)
	}
	return append(kept, deferred...)
}

func (w *summaryWriter) updatePaths(ctx context.Context, paths []string) error {
	seen := map[string]bool{}
	for _, rel := range paths {
		for depth, part := range strings.Split(rel, "/") {
			if depth > 0 && sandbox.IsVCSDirBaseName(part) {
				rel = strings.Join(strings.Split(rel, "/")[:depth], "/")
				break
			}
		}
		// A changed ancestor can replace a directory with a symlink or file.
		parts := strings.Split(rel, "/")
		for depth := 1; depth < len(parts); depth++ {
			parent := strings.Join(parts[:depth], "/")
			info, err := w.root.Lstat(filepath.FromSlash(parent))
			if errors.Is(err, os.ErrNotExist) || (err == nil && !info.IsDir()) {
				rel = parent
				break
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			ancestor, readErr := readTreeNode(ctx, w.tx, parent)
			err = readErr
			if err == nil && ancestor.Boundary && !ancestor.IsVCSRoot {
				// Changes within unobserved subtrees do not affect this generation.
				rel = ""
				break
			}
			if errors.Is(err, sql.ErrNoRows) || ancestor.Boundary {
				rel = parent
				break
			}
			if err != nil {
				return err
			}
		}
		if rel == "" {
			continue
		}
		covered := false
		for p := rel; p != "."; p = path.Dir(p) {
			if seen[p] {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		seen[rel] = true
		if err := w.updatePath(ctx, rel); err != nil {
			return err
		}
	}
	return nil
}

// updatePath rescans one subtree against its stored rows, sweeps what the
// rescan did not reach, and folds the delta into every ancestor.
func (w *summaryWriter) updatePath(ctx context.Context, rel string) error {
	old, err := readTreeNode(ctx, w.tx, rel)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	n, err := w.scan(ctx, rel)
	if err != nil {
		return err
	}
	if err := w.sweep(ctx, rel); err != nil {
		return err
	}
	childDelta := 0
	if old.Path != "" && !old.Boundary {
		childDelta--
	}
	if n.Path != "" && !n.Boundary {
		childDelta++
	}
	for parent := path.Dir(rel); ; parent = path.Dir(parent) {
		ancestor, readErr := readTreeNode(ctx, w.tx, parent)
		if errors.Is(readErr, sql.ErrNoRows) {
			info, statErr := w.root.Lstat(filepath.FromSlash(parent))
			if statErr != nil {
				return statErr
			}
			ancestor = TreeNode{Entry: w.entry(parent, info)}
		} else if readErr != nil {
			return readErr
		}
		ancestor.Files += n.Files - old.Files
		ancestor.Bytes += n.Bytes - old.Bytes
		ancestor.Pruned += n.Pruned - old.Pruned
		ancestor.Children += childDelta
		ancestor.Representative = ""
		if ancestor.Files > 0 {
			err = w.tx.QueryRowContext(ctx, "SELECT representative FROM nodes WHERE parent=? AND boundary=0 ORDER BY rank LIMIT 1", parent).Scan(&ancestor.Representative)
			if err != nil {
				return err
			}
		}
		if parent == "." {
			ancestor.Parent = ""
		}
		if err := w.write(ctx, ancestor); err != nil {
			return err
		}
		if parent == "." {
			break
		}
		childDelta = 0
		if errors.Is(readErr, sql.ErrNoRows) {
			childDelta = 1
		}
	}
	return nil
}

func (w *summaryWriter) entry(rel string, info os.FileInfo) Entry {
	e := Entry{RootID: w.store.root.ID, Path: rel, Parent: normalizeDir(path.Dir(rel)), Name: path.Base(rel),
		Depth: pathDepth(rel), IsDir: info.IsDir(), IsSymlink: info.Mode()&os.ModeSymlink != 0,
		Size: info.Size(), Mode: uint32(info.Mode()), Modified: info.ModTime().UTC()}
	if e.IsDir {
		marker, err := w.root.Lstat(filepath.FromSlash(path.Join(rel, ".git")))
		e.IsVCSRoot = err == nil && (marker.IsDir() || marker.Mode().IsRegular())
	}
	return e
}

func treeUnder(candidate, base string) bool {
	return base == "." || candidate == base || strings.HasPrefix(candidate, base+"/")
}

func treeRank(n TreeNode) string {
	return fmt.Sprintf("%020d%020d%s", int64(1<<63-1)-int64(n.Files), int64(1<<63-1)-n.Bytes, n.Path)
}

// truncateTreeWAL checkpoints committed pages; pinned readers defer truncation.
// A truncating checkpoint holds the writer lock while it waits for readers, so
// it runs on its own connection that never waits: a reader pinned to an earlier
// generation leaves the log for a later pass instead of stalling every writer
// for the busy timeout.
func truncateTreeWAL(ctx context.Context, file string) {
	db, err := openTreeDBWaiting(ctx, file, 0)
	if err != nil {
		slog.DebugContext(ctx, "catalog wal checkpoint failed", "file", file, "err", err)
		return
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	if err != nil {
		slog.DebugContext(ctx, "catalog wal checkpoint failed", "file", file, "err", err)
		return
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var busy, logFrames, checkpointed int
		if err := rows.Scan(&busy, &logFrames, &checkpointed); err != nil {
			return
		}
		if busy != 0 {
			slog.DebugContext(ctx, "catalog wal checkpoint left frames pinned", "file", file,
				"log_frames", logFrames, "checkpointed_frames", checkpointed)
		}
	}
	if err := rows.Err(); err != nil {
		slog.DebugContext(ctx, "catalog wal checkpoint failed", "file", file, "err", err)
	}
}
