package workspacebaseline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceblob"
)

type Store struct {
	prune   pruneCursor
	db      db.Handle
	queries *db.Queries
	blobs   *sourceblob.Store
	root    string
}

// New shares the ledger's object store and reference lease. A nil database is
// reserved for transient queues whose entire storage root has one owner.
func New(database db.Handle, blobs *sourceblob.Store, root string) *Store {
	return &Store{db: database, queries: db.New(database), blobs: blobs, root: root}
}

type CaptureFile struct {
	Path string
	Abs  string
	Info os.FileInfo
}

type Walk func(context.Context, func(CaptureFile) error) error

// Capture seals a manifest before its reference may be attached to a worker.
func (s *Store) Capture(ctx context.Context, jobID string, walk Walk) (path string, err error) {
	return s.seal(ctx, jobID, func(ctx context.Context, w *manifestWriter) error {
		return walk(ctx, func(file CaptureFile) error {
			f, err := s.captureFile(ctx, w.pins, file, false)
			if err != nil {
				return err
			}
			return w.insert(ctx, file.Path, f)
		})
	})
}

// DefaultMaxOverlayFiles caps the number of changed files in an overlay.
const DefaultMaxOverlayFiles = 10_000

// DefaultMaxOverlayBytes caps the total size of changed file bodies in an overlay (100 MB).
const DefaultMaxOverlayBytes = 100 * 1024 * 1024

// ErrOverlayBudgetExceeded indicates that an overlay change set exceeds the file count or size cap.
var ErrOverlayBudgetExceeded = errors.New("overlay budget exceeded")

// OverlayBudgetExceededError provides structured detail when an overlay exceeds file or byte caps.
type OverlayBudgetExceededError struct {
	Files    int
	MaxFiles int
	Bytes    int64
	MaxBytes int64
	Reason   string
}

func (e *OverlayBudgetExceededError) Error() string {
	if e == nil {
		return ErrOverlayBudgetExceeded.Error()
	}
	return fmt.Sprintf("%s: %s", ErrOverlayBudgetExceeded, e.Reason)
}

func (e *OverlayBudgetExceededError) Unwrap() error {
	return ErrOverlayBudgetExceeded
}

// OverlayCapture reports what an overlay manifest sealed.
type OverlayCapture struct {
	Path    string
	Changed int
	Deleted int
}

// CaptureOverlay seals the worker's changes against its baseline: every path
// whose metadata moved, with a full body for each regular file and a tombstone
// for each removal. Together with the baseline it is enough to rebuild the
// branch tree, so the tree itself becomes reclaimable.
func (s *Store) CaptureOverlay(ctx context.Context, jobID, baselinePath string, roots []projectroot.RootRef, branch string) (OverlayCapture, error) {
	baseline, err := Open(ctx, baselinePath, s.blobs)
	if err != nil {
		return OverlayCapture{}, err
	}
	defer func() { _ = baseline.Close() }()
	changed, err := baseline.Changes(ctx, roots, branch)
	if err != nil {
		return OverlayCapture{}, err
	}
	if len(changed) > DefaultMaxOverlayFiles {
		return OverlayCapture{}, &OverlayBudgetExceededError{
			Files:    len(changed),
			MaxFiles: DefaultMaxOverlayFiles,
			Reason:   fmt.Sprintf("%d changed files exceeds limit of %d", len(changed), DefaultMaxOverlayFiles),
		}
	}
	out := OverlayCapture{}
	out.Path, err = s.seal(ctx, jobID, func(ctx context.Context, w *manifestWriter) error {
		return s.captureOverlayFiles(ctx, w, roots, branch, changed, &out)
	})
	if err != nil {
		return OverlayCapture{}, err
	}
	return out, nil
}

type captureItem struct {
	path    string
	file    File
	blob    *sourceblob.Capture
	size    int64
	deleted bool
	err     error
}

func (s *Store) captureOneFile(ctx context.Context, roots []projectroot.RootRef, branch, path string) captureItem {
	abs, err := branchPath(roots, branch, path)
	if err != nil {
		return captureItem{err: err}
	}
	info, err := os.Lstat(abs)
	if err != nil && !os.IsNotExist(err) {
		return captureItem{err: err}
	}
	if err != nil || info.IsDir() {
		return captureItem{
			path:    path,
			file:    File{Deleted: true},
			deleted: true,
		}
	}
	mode := info.Mode()
	f := File{
		Size:      info.Size(),
		MtimeNano: info.ModTime().UnixNano(),
		Opaque:    true,
		Mode:      uint32(mode),
	}
	if mode&os.ModeSymlink != 0 {
		target, err := os.Readlink(abs)
		if err != nil {
			return captureItem{err: fmt.Errorf("capture symlink %s: %w", path, err)}
		}
		f.LinkTarget = target
		return captureItem{path: path, file: f}
	}
	if !mode.IsRegular() {
		return captureItem{path: path, file: f}
	}
	captured, err := s.blobs.PutFile(ctx, abs)
	if err != nil {
		return captureItem{err: fmt.Errorf("capture baseline %s: %w", path, err)}
	}
	f.SHA256 = captured.SHA256
	f.Opaque = false
	return captureItem{
		path: path,
		file: f,
		blob: &captured,
		size: info.Size(),
	}
}

func (s *Store) captureOverlayFiles(
	ctx context.Context,
	w *manifestWriter,
	roots []projectroot.RootRef,
	branch string,
	changed []string,
	out *OverlayCapture,
) error {
	if len(changed) == 0 {
		return nil
	}

	numWorkers := runtime.GOMAXPROCS(0)
	if numWorkers > 8 {
		numWorkers = 8
	}
	if numWorkers < 1 {
		numWorkers = 1
	}
	if numWorkers > len(changed) {
		numWorkers = len(changed)
	}

	workCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()

	workChan := make(chan string, len(changed))
	for _, p := range changed {
		workChan <- p
	}
	close(workChan)

	resChan := make(chan captureItem, len(changed))
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range workChan {
				if workCtx.Err() != nil {
					return
				}
				item := s.captureOneFile(workCtx, roots, branch, path)
				select {
				case resChan <- item:
				case <-workCtx.Done():
					return
				}
				if item.err != nil {
					return
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(resChan)
	}()

	var totalBytes int64
	for item := range resChan {
		if item.err != nil {
			cancelWorkers()
			return item.err
		}
		totalBytes += item.size
		if totalBytes > DefaultMaxOverlayBytes {
			cancelWorkers()
			return &OverlayBudgetExceededError{
				Bytes:    totalBytes,
				MaxBytes: DefaultMaxOverlayBytes,
				Reason:   fmt.Sprintf("changed file bytes exceeds limit of %d bytes", DefaultMaxOverlayBytes),
			}
		}
		if item.deleted {
			out.Deleted++
		} else {
			out.Changed++
		}
		if item.blob != nil && s.db != nil {
			w.pins.pending = append(w.pins.pending, *item.blob)
			if len(w.pins.pending) >= 256 {
				if err := w.pins.flush(ctx); err != nil {
					cancelWorkers()
					return err
				}
			}
		}
		if err := w.insert(ctx, item.path, item.file); err != nil {
			cancelWorkers()
			return err
		}
	}
	return ctx.Err()
}

type manifestWriter struct {
	stmt *sql.Stmt
	pins *baselinePins
}

func (w *manifestWriter) insert(ctx context.Context, path string, f File) error {
	_, err := w.stmt.ExecContext(ctx, path, f.Size, f.MtimeNano, f.SHA256, f.Opaque, f.Mode, f.LinkTarget, f.Deleted)
	return err
}

// seal creates, fills, and publishes one manifest; a failure leaves nothing behind.
func (s *Store) seal(ctx context.Context, jobID string, fill func(context.Context, *manifestWriter) error) (path string, err error) {
	release := s.blobs.AcquireReferenceLease()
	defer release()
	id := uuid.NewString()
	path = filepath.Join(s.root, id+".db")
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return "", err
	}
	if s.db != nil {
		err = s.queries.InsertWorkerBaseline(ctx, db.InsertWorkerBaselineParams{ID: id, JobID: jobID, CreatedAt: db.FormatTime(time.Now())})
		if err != nil {
			return "", err
		}
	}
	capturedPath := path
	sealed := false
	defer func() {
		if !sealed {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = s.Discard(cleanupCtx, capturedPath)
		}
	}()
	manifest, err := createManifest(ctx, path)
	if err != nil {
		return "", err
	}
	defer func() { _ = manifest.Close() }()
	tx, err := manifest.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO files(path,size,mtime_ns,sha256,opaque,mode,link_target,deleted) VALUES (?,?,?,?,?,?,?,?)")
	if err != nil {
		return "", err
	}
	defer func() { _ = stmt.Close() }()
	writer := &manifestWriter{stmt: stmt, pins: &baselinePins{store: s, id: id}}
	if err := fill(ctx, writer); err != nil {
		return "", err
	}
	if err := writer.pins.flush(ctx); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	if err := manifest.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o400); err != nil {
		return "", err
	}
	if s.db != nil {
		digest, hashErr := Digest(ctx, path)
		if hashErr != nil {
			return "", hashErr
		}
		if err := s.queries.SealWorkerBaseline(ctx, db.SealWorkerBaselineParams{ManifestSha256: digest, FormatVersion: 1, ID: id}); err != nil {
			return "", err
		}
	}
	sealed = true
	return path, nil
}

func createManifest(ctx context.Context, path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", OmitHost: true, Path: filepath.ToSlash(path)}
	d, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	_, err = d.ExecContext(ctx, `PRAGMA journal_mode=DELETE; PRAGMA synchronous=EXTRA; PRAGMA cache_size=-4096;
 CREATE TABLE manifest(version INTEGER PRIMARY KEY CHECK(version=1)) STRICT;
 CREATE TABLE files(path TEXT PRIMARY KEY, size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL, sha256 TEXT NOT NULL, opaque INTEGER NOT NULL, mode INTEGER NOT NULL, link_target TEXT NOT NULL, deleted INTEGER NOT NULL) STRICT, WITHOUT ROWID;`)
	if err == nil {
		_, err = d.ExecContext(ctx, "INSERT INTO manifest VALUES(1)")
	}
	if err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

// captureFile records one entry. Bodies are pinned for regular files within
// MaxContentBytes, or of any size when unbounded; symlinks keep their target.
func (s *Store) captureFile(ctx context.Context, pins *baselinePins, file CaptureFile, unbounded bool) (File, error) {
	if err := ctx.Err(); err != nil {
		return File{}, err
	}
	f := File{Size: file.Info.Size(), MtimeNano: file.Info.ModTime().UnixNano(), Opaque: true, Mode: uint32(file.Info.Mode())}
	if file.Info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(file.Abs)
		if err != nil {
			return File{}, fmt.Errorf("capture symlink %s: %w", file.Path, err)
		}
		f.LinkTarget = target
		return f, nil
	}
	if !file.Info.Mode().IsRegular() || (!unbounded && file.Info.Size() > MaxContentBytes) {
		return f, nil
	}
	captured, err := s.blobs.PutFile(ctx, file.Abs)
	if err != nil {
		return File{}, fmt.Errorf("capture baseline %s: %w", file.Path, err)
	}
	if captured.Size != f.Size || captured.ModifiedNS != f.MtimeNano {
		return File{}, sourceblob.ErrFileChanged
	}
	f.SHA256 = captured.SHA256
	// Text decoding is deferred until a path needs merging.
	f.Opaque = false
	if s.db == nil {
		return f, nil
	}
	pins.pending = append(pins.pending, captured)
	if len(pins.pending) >= 256 {
		return f, pins.flush(ctx)
	}
	return f, nil
}

type baselinePins struct {
	store   *Store
	id      string
	pending []sourceblob.Capture
}

func (p *baselinePins) flush(ctx context.Context) error {
	if len(p.pending) == 0 {
		return nil
	}
	tx, err := p.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries := db.New(tx)
	for _, c := range p.pending {
		err = queries.UpsertSourceBlobObject(ctx, db.UpsertSourceBlobObjectParams{Sha256: c.SHA256, Size: c.Size, StoredSize: c.Stored, StorageRelpath: c.Rel, GitOidSha1: c.GitOIDs.SHA1, GitOidSha256: c.GitOIDs.SHA256})
		if err != nil {
			return err
		}
		err = queries.PinWorkerBaselineObject(ctx, db.PinWorkerBaselineObjectParams{BaselineID: p.id, Sha256: c.SHA256})
		if err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	p.pending = p.pending[:0]
	return nil
}

// Discard is for an unpublished capture. Published manifests live as long as
// the worker row, including pending promotion and completed history.
func (s *Store) Discard(ctx context.Context, path string) error {
	if filepath.Dir(path) != filepath.Clean(s.root) {
		return fmt.Errorf("baseline is outside its store")
	}
	if s.db != nil {
		id, err := ID(path)
		if err != nil {
			return err
		}
		n, err := s.queries.DiscardUnpublishedWorkerBaseline(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
