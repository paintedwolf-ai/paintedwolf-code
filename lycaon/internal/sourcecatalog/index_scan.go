package sourcecatalog

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// indexBatchSize bounds listing memory and bulk deletes.
const indexBatchSize = 256

// Discovery batches publication to limit write-ahead log traffic and reader delay.
const (
	indexPublishEntries  = 4096
	indexPublishInterval = 250 * time.Millisecond
)

// reconcile resumes a breadth-first frontier with incremental publication.
func (s *indexStore) reconcile(ctx context.Context, epoch repochange.Epoch) error {
	full, changed := s.takeWork()
	db, root, releaseResources, err := s.acquireIndex(ctx)
	if err != nil {
		return err
	}
	defer releaseResources()
	w := &indexWalk{store: s, db: db, root: root, budget: indexBudget{limits: s.policy.budgets}}
	defer w.discard()
	if err := w.prepare(ctx, full, changed); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir, err := w.nextDir(ctx)
		if err != nil {
			return err
		}
		if dir == "" {
			break
		}
		if err := w.visitDir(ctx, dir); err != nil {
			return err
		}
	}
	return w.finish(ctx, epoch)
}

// indexWalk publishes directory changes and their frontier in the same transaction.
type indexWalk struct {
	store         *indexStore
	db            *sql.DB
	root          *os.Root
	budget        indexBudget
	tx            *sql.Tx
	releaseWriter func()
	// pending counts rows written since the last publish.
	pending     int
	lastPublish time.Time
	// Subtree passes retain coverage; root passes gain it on completion.
	covering bool
}

func (w *indexWalk) begin(ctx context.Context) error {
	if w.tx != nil {
		return nil
	}
	release, err := w.store.write(ctx)
	if err != nil {
		return err
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		release()
		return err
	}
	w.tx, w.releaseWriter = tx, release
	if w.lastPublish.IsZero() {
		w.lastPublish = time.Now()
	}
	return nil
}

// commit stamps and commits the open generation; false when nothing was open.
func (w *indexWalk) commit(ctx context.Context) (TreeStatus, bool, error) {
	if w.tx == nil {
		return TreeStatus{}, false, nil
	}
	tx := w.tx
	status, err := publishGeneration(ctx, tx, w.covering)
	if err != nil {
		_ = tx.Rollback()
	}
	w.tx = nil
	w.releaseWriter()
	w.releaseWriter = nil
	if err != nil {
		return TreeStatus{}, false, err
	}
	w.pending, w.lastPublish = 0, time.Now()
	return status, true, nil
}

func (w *indexWalk) publish(ctx context.Context) error {
	status, published, err := w.commit(ctx)
	if err != nil || !published {
		return err
	}
	w.store.setStatus(status)
	// Partial search results remain available while initial structure finishes.
	if !status.Complete {
		return w.store.awaitInitialInventory(ctx)
	}
	return nil
}

// publishFinal commits the pass's last generation together with the epoch it
// observed, so a reader never sees that generation still marked as refreshing.
func (w *indexWalk) publishFinal(ctx context.Context, epoch repochange.Epoch) error {
	status, published, err := w.commit(ctx)
	if err != nil || !published {
		return err
	}
	w.store.mu.Lock()
	w.store.publishFinalLocked(status, epoch)
	w.store.mu.Unlock()
	return nil
}

// releaseForObservation frees the writer without settling searches before file discovery.
func (w *indexWalk) releaseForObservation(ctx context.Context) error {
	if w.tx == nil {
		return nil
	}
	err := w.tx.Commit()
	if err != nil {
		_ = w.tx.Rollback()
	}
	w.tx = nil
	w.releaseWriter()
	w.releaseWriter = nil
	return err
}

func (w *indexWalk) maybePublish(ctx context.Context) error {
	if w.tx == nil || (w.pending < indexPublishEntries && time.Since(w.lastPublish) < indexPublishInterval) {
		return nil
	}
	return w.publish(ctx)
}

// Unpublished work resumes from the last committed frontier.
func (w *indexWalk) discard() {
	if w.tx == nil {
		return
	}
	_ = w.tx.Rollback()
	w.tx = nil
	w.releaseWriter()
	w.releaseWriter = nil
}

// prepare resumes an interrupted frontier or seeds a new pass.
func (w *indexWalk) prepare(ctx context.Context, full bool, changed []string) error {
	if err := w.begin(ctx); err != nil {
		return err
	}
	var complete bool
	err := w.tx.QueryRowContext(ctx, "SELECT complete FROM meta WHERE id=1").Scan(&complete)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	resumed := err == nil && !complete
	paths, valid := reconciliationPaths(w.store.root, changed)
	wholeTree := full || !valid || (!resumed && len(paths) == 0)
	if wholeTree {
		paths = []string{"."}
	}
	w.covering = !wholeTree && !resumed
	if !resumed {
		if err := w.budget.reset(ctx, w.tx); err != nil {
			return err
		}
	}
	for _, rel := range paths {
		if skippedIndexPath(rel) {
			continue
		}
		if err := w.enqueueDir(ctx, rel); err != nil {
			return err
		}
	}
	// Seeding publishes nothing: the store stays warming until the first listing.
	return nil
}

// nextDir spends the walk budget on source before deferred directories.
func (w *indexWalk) nextDir(ctx context.Context) (string, error) {
	if err := w.begin(ctx); err != nil {
		return "", err
	}
	var dir string
	err := w.tx.QueryRowContext(ctx, "SELECT path FROM frontier ORDER BY deferred, id LIMIT 1").Scan(&dir)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return dir, nil
}

func (w *indexWalk) enqueueDir(ctx context.Context, rel string) error {
	_, err := w.tx.ExecContext(ctx, "INSERT OR IGNORE INTO frontier(path,deferred) VALUES(?,?)",
		rel, w.store.policy.deferDir(rel))
	return err
}

// completeDir advances the admitted generation; shared observations prune vanished nodes.
func (w *indexWalk) completeDir(ctx context.Context, dir string, sequence int64) error {
	if err := w.begin(ctx); err != nil {
		return err
	}
	observation, err := w.store.readObservation(ctx, dir)
	if err != nil {
		return err
	}
	if observation.Sequence != sequence || !observation.Complete {
		return nil
	}
	if _, err = w.tx.ExecContext(ctx, "DELETE FROM faults WHERE path=?", dir); err != nil {
		return err
	}
	if _, err = w.tx.ExecContext(ctx, "DELETE FROM frontier WHERE path=?", dir); err != nil {
		return err
	}
	return w.maybePublish(ctx)
}

// cut records an exceeded bound. Subtree cuts retain observed entries;
// directory cuts discard the refused listing and its descendants.
func (w *indexWalk) cut(ctx context.Context, c indexCut) error {
	if c.Reason == sandbox.BoundaryWalkBudget {
		return w.exhaust(ctx)
	}
	if c.Reason == sandbox.BoundaryDirectoryCap {
		if err := w.hideIndexSubtree(ctx, c.Dir); err != nil {
			return err
		}
	}
	if err := w.clearFrontierUnder(ctx, c.Dir); err != nil {
		return err
	}
	if err := w.begin(ctx); err != nil {
		return err
	}
	if _, err := w.tx.ExecContext(ctx, "UPDATE nodes SET refused=? WHERE path=?", string(c.Reason), c.Dir); err != nil {
		return err
	}
	if _, err := w.tx.ExecContext(ctx, "DELETE FROM frontier WHERE path=?", c.Dir); err != nil {
		return err
	}
	// A refusal changes coverage, so it publishes now.
	return w.publish(ctx)
}

// exhaust marks remaining frontier entries with the walk boundary.
func (w *indexWalk) exhaust(ctx context.Context) error {
	for {
		if err := nextMetadataEntry(ctx); err != nil {
			return err
		}
		if err := w.begin(ctx); err != nil {
			return err
		}
		if _, err := w.tx.ExecContext(ctx,
			`UPDATE nodes SET refused=?
			 WHERE path IN (SELECT path FROM frontier ORDER BY id LIMIT ?)`,
			string(sandbox.BoundaryWalkBudget), indexBatchSize); err != nil {
			return err
		}
		result, err := w.tx.ExecContext(ctx,
			"DELETE FROM frontier WHERE path IN (SELECT path FROM frontier ORDER BY id LIMIT ?)", indexBatchSize)
		if err != nil {
			return err
		}
		dropped, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if dropped == 0 {
			return w.publish(ctx)
		}
		w.pending += int(dropped)
		if err := w.maybePublish(ctx); err != nil {
			return err
		}
	}
}

// faultDir records a directory the pass could not read and leaves the frontier.
func (w *indexWalk) faultDir(ctx context.Context, dir string, cause error) error {
	if errors.Is(cause, os.ErrNotExist) {
		return w.removeSubtree(ctx, dir, true)
	}
	if err := w.begin(ctx); err != nil {
		return err
	}
	if _, err := w.tx.ExecContext(ctx, "INSERT OR IGNORE INTO faults(path) VALUES(?)", dir); err != nil {
		return err
	}
	if _, err := w.tx.ExecContext(ctx, "DELETE FROM frontier WHERE path=?", dir); err != nil {
		return err
	}
	return w.maybePublish(ctx)
}

// writeLeaf replaces a frontier directory that is no longer a directory.
func (w *indexWalk) writeLeaf(ctx context.Context, rel string, info os.FileInfo) error {
	if err := w.removeSubtree(ctx, rel, false); err != nil {
		return err
	}
	if err := w.begin(ctx); err != nil {
		return err
	}
	writer, err := newIndexWriter(ctx, w.tx)
	if err != nil {
		return err
	}
	defer writer.close()
	if err := writer.write(ctx, w.node(rel, info)); err != nil {
		return err
	}
	if _, err := w.tx.ExecContext(ctx, "DELETE FROM faults WHERE path=?", rel); err != nil {
		return err
	}
	if _, err := w.tx.ExecContext(ctx, "DELETE FROM frontier WHERE path=?", rel); err != nil {
		return err
	}
	w.pending++
	return w.maybePublish(ctx)
}

// finish publishes the complete generation and settles the epoch it observed.
func (w *indexWalk) finish(ctx context.Context, epoch repochange.Epoch) error {
	if err := w.begin(ctx); err != nil {
		return err
	}
	// Charges bound one pass; only a resumed pass carries them forward.
	if _, err := w.tx.ExecContext(ctx, "DELETE FROM charges"); err != nil {
		return err
	}
	w.covering = true
	if err := w.publishFinal(ctx, epoch); err != nil {
		return err
	}
	// Checkpoint once per completed walk: a partial index publishes many times.
	truncateTreeWAL(ctx, w.store.file)
	return nil
}

// removeSubtree removes index admission and walk bookkeeping. Shared directory
// observations decide when physical facts vanish.
func (w *indexWalk) removeSubtree(ctx context.Context, rel string, include bool) error {
	if err := w.hideIndexSubtree(ctx, rel); err != nil {
		return err
	}
	if include {
		if err := w.begin(ctx); err != nil {
			return err
		}
		if _, err := w.tx.ExecContext(ctx, "UPDATE nodes SET indexed=0 WHERE path=?", rel); err != nil {
			return err
		}
	}
	for _, table := range []string{"frontier", "faults", "charges"} {
		for {
			if err := nextMetadataEntry(ctx); err != nil {
				return err
			}
			if err := w.begin(ctx); err != nil {
				return err
			}
			predicate, args := indexDescendants(rel)
			args = append(args, indexBatchSize)
			// #nosec G202 -- Identifiers and predicate come from closed internal definitions.
			result, err := w.tx.ExecContext(ctx,
				"DELETE FROM "+table+" WHERE path IN (SELECT path FROM "+table+" WHERE "+predicate+" ORDER BY path LIMIT ?)", args...)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
			w.pending += int(n)
			if err := w.maybePublish(ctx); err != nil {
				return err
			}
		}
		if include {
			if err := w.begin(ctx); err != nil {
				return err
			}
			// #nosec G202 -- table is a closed internal identifier.
			if _, err := w.tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE path=?", rel); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *indexWalk) clearFrontierUnder(ctx context.Context, rel string) error {
	for {
		if err := nextMetadataEntry(ctx); err != nil {
			return err
		}
		if err := w.begin(ctx); err != nil {
			return err
		}
		predicate, args := indexDescendants(rel)
		args = append(args, indexBatchSize)
		// #nosec G202 -- predicate is selected internally; paths are bound.
		result, err := w.tx.ExecContext(ctx,
			"DELETE FROM frontier WHERE path IN (SELECT path FROM frontier WHERE "+predicate+" ORDER BY path LIMIT ?)", args...)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		w.pending += int(n)
		if err := w.maybePublish(ctx); err != nil {
			return err
		}
	}
}

type indexNode struct {
	path, parent, name string
	depth              int
	isDir, isSymlink   bool
	regular, hidden    bool
	size               int64
	mode               uint32
	modified           time.Time
	// refused records the indexing boundary, independently of shared observations.
	refused sandbox.BoundaryReason
}

func (w *indexWalk) node(rel string, info os.FileInfo) indexNode {
	n := indexNode{
		path: rel, parent: normalizeDir(path.Dir(rel)), name: path.Base(rel), depth: pathDepth(rel),
		isDir: info.IsDir(), isSymlink: info.Mode()&os.ModeSymlink != 0, regular: info.Mode().IsRegular(),
		hidden: hiddenIndexPath(rel), size: info.Size(), mode: uint32(info.Mode()), modified: info.ModTime().UTC(),
	}
	if rel == "." {
		n.parent, n.name = "", "."
	}
	return n
}

type indexWriter struct{ put *sql.Stmt }

func newIndexWriter(ctx context.Context, tx *sql.Tx) (*indexWriter, error) {
	put, err := tx.PrepareContext(ctx, `INSERT INTO nodes
 (path,search_path,search_name,parent,name,depth,is_dir,symlink,regular,hidden,size,mode,modified,refused,indexed,agent_metadata)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET
 is_dir=excluded.is_dir,symlink=excluded.symlink,regular=excluded.regular,hidden=excluded.hidden,
 size=excluded.size,mode=excluded.mode,modified=excluded.modified,refused=excluded.refused,indexed=excluded.indexed`)
	if err != nil {
		return nil, err
	}
	return &indexWriter{put: put}, nil
}
func (w *indexWriter) close() {
	if w != nil {
		_ = w.put.Close()
	}
}
func (w *indexWriter) write(ctx context.Context, n indexNode) error {
	_, err := w.put.ExecContext(ctx, n.path, strings.ToLower(n.path), strings.ToLower(n.name), n.parent, n.name,
		n.depth, n.isDir, n.isSymlink, n.regular, n.hidden, n.size, n.mode, n.modified.UnixNano(), string(n.refused), !skippedIndexPath(n.path), agentMetadataPath(n.path))
	return err
}

// hideIndexSubtree removes search admission while retaining navigation facts.
func (w *indexWalk) hideIndexSubtree(ctx context.Context, rel string) error {
	for {
		if err := w.begin(ctx); err != nil {
			return err
		}
		predicate, args := indexDescendants(rel)
		args = append(args, indexBatchSize)
		// #nosec G202 -- predicate is one of two fixed templates; paths are bound.
		result, err := w.tx.ExecContext(ctx, "UPDATE nodes SET indexed=0 WHERE path IN (SELECT path FROM nodes WHERE indexed=1 AND "+predicate+" ORDER BY path LIMIT ?)", args...)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		w.pending += int(count)
		if err := w.maybePublish(ctx); err != nil {
			return err
		}
	}
}

func indexDescendants(rel string) (string, []any) {
	if rel == "." {
		return "path<>'.'", nil
	}
	return "path>=? AND path<?", []any{rel + "/", rel + "0"}
}
