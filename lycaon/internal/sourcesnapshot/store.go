package sourcesnapshot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourceobservation"
	"github.com/lycaon/lycaon/internal/sourcescope"
)

const (
	// maxConvergePasses bounds one publication.
	maxConvergePasses = 8
	// maxStalledPasses detects a moving tree.
	maxStalledPasses = 2
)

type buildJob struct {
	done     chan struct{}
	snapshot Snapshot
	err      error
}

// Store publishes and reads immutable manifests. It copies no source bytes:
// an entry names its content, and Bytes finds them in the retained revision
// store, in git's object store, or in the live file.
type Store struct {
	db      db.Handle
	queries *db.Queries
	// blobs is the retained revision store, read when a consumer needs the
	// bytes of a version the ledger captured.
	blobs        *sourceblob.Store
	observations *sourceobservation.Store
	broker       *backgroundwork.Broker
	// onCapture reports files read from disk.
	onCapture func(abs string)
	// onSurvey reports roots walked in full.
	onSurvey func(root string)

	mu   sync.Mutex
	jobs map[string]*buildJob

	// Storage admission orders publications, which read their base chunks,
	// against the sweep that removes unreferenced generations.
	storageGateOnce sync.Once
	storageGate     *semaphore.Weighted
	rootStorage     map[string]*rootStorageGate

	// scopes decides what each root's capture observes.
	scopes ScopeProvider
	// deltas turns tree changes into the paths the next publication re-reads.
	deltas    *deltaTracker
	unobserve func()
}

// New builds a snapshot store over the shared database. blobs is the
// retained revision store the ledger writes; the snapshot store only reads
// it. The capture scope defaults to the bundled source scope with no project
// declarations; SetScopes installs the host's provider.
func New(sqlDB db.Handle, blobs *sourceblob.Store, observationPath string, broker *backgroundwork.Broker) *Store {
	if sqlDB == nil {
		return nil
	}
	s := &Store{
		db: sqlDB, queries: db.New(sqlDB), blobs: blobs, observations: sourceobservation.New(observationPath), broker: broker,
		jobs:   make(map[string]*buildJob),
		scopes: defaultScopes(), deltas: newDeltaTracker(),
	}
	s.deltas.admit = func(ctx context.Context, root, rel string) bool {
		return s.scopeFor(ctx, Request{}, Root{Path: root}).AdmitPath(rel, false)
	}
	s.unobserve = repochange.RegisterObserver(s.deltas.observe)
	return s
}

// SetScopes installs the provider that decides what each root's capture
// observes. Scopes built so far are not revisited; the next publication
// uses the new provider.
func (s *Store) SetScopes(scopes ScopeProvider) {
	if s == nil || scopes == nil {
		return
	}
	s.mu.Lock()
	s.scopes = scopes
	s.mu.Unlock()
}

// defaultScopes is the bundled scope with no floor and no project
// declarations, which is what a store has before the host wires one.
func defaultScopes() ScopeProvider {
	cfg, err := sourcescope.DefaultConfig()
	if err != nil {
		slog.Warn("bundled source scope unreadable; captures run unbounded", "error", err)
		return bundledScopes{}
	}
	provider, err := sourcescope.NewProvider(cfg, nil)
	if err != nil {
		slog.Warn("excludes floor unreadable; captures run without it", "error", err)
		return bundledScopes{plane: cfg.Capture}
	}
	return provider
}

// bundledScopes is the fallback provider when the bundled config cannot be
// read: the plane it holds, or an unbounded one.
type bundledScopes struct{ plane sourcescope.Plane }

func (b bundledScopes) Capture(_ context.Context, root string) *sourcescope.Scope {
	return sourcescope.New(root, sourcescope.Options{Plane: b.plane})
}

// Close releases the observation cache and the revision store cursors.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	if s.unobserve != nil {
		s.unobserve()
		s.unobserve = nil
	}
	return errors.Join(s.observations.Close(), s.blobs.Close())
}

// DiscardObservations drops one root's rebuildable stat-to-digest reuse index
// while keeping its snapshots. Attach uses this; detach and delete use
// ReleaseRoots.
func (s *Store) DiscardObservations(ctx context.Context, rootPath string) error {
	if s == nil || s.observations == nil {
		return nil
	}
	return s.observations.DeleteRoot(ctx, filepath.Clean(strings.TrimSpace(rootPath)))
}

// ClearObservations drops the rebuildable stat-to-digest reuse index.
func (s *Store) ClearObservations(ctx context.Context) error {
	if s == nil || s.observations == nil {
		return nil
	}
	return s.observations.Clear(ctx)
}

// rootsKey coalesces consumers of the same canonical roots.
func rootsKey(roots []Root) string {
	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		paths = append(paths, filepath.Clean(root.Path))
	}
	sort.Strings(paths)
	sum := sha256.Sum256([]byte(strings.Join(paths, "\x00")))
	return hex.EncodeToString(sum[:])
}

// normalizeRoots cleans and de-duplicates a request's roots in stable order.
func normalizeRoots(roots []Root) []Root {
	seen := make(map[string]struct{}, len(roots))
	out := make([]Root, 0, len(roots))
	for _, root := range roots {
		path := filepath.Clean(strings.TrimSpace(root.Path))
		if path == "" || path == "." {
			continue
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, Root{Path: path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Ensure publishes the admitted files for one root set.
func (s *Store) Ensure(ctx context.Context, req Request) (Snapshot, error) {
	if s == nil {
		return Snapshot{}, errors.New("source snapshot store is not configured")
	}
	req.Roots = normalizeRoots(req.Roots)
	if len(req.Roots) == 0 {
		return Snapshot{}, errors.New("source snapshot request is incomplete")
	}
	req.Verify = req.Verify.Normalized()
	key := rootsKey(req.Roots) + "\x00" + string(req.Verify)

admit:
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	if existing := s.jobs[key]; existing != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		case <-existing.done:
			// Another consumer's canceled capture does not cancel this request.
			if errors.Is(existing.err, context.Canceled) || errors.Is(existing.err, context.DeadlineExceeded) {
				goto admit
			}
			return existing.snapshot, existing.err
		}
	}
	job := &buildJob{done: make(chan struct{})}
	s.jobs[key] = job
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.jobs, key)
		close(job.done)
		s.mu.Unlock()
	}()
	job.snapshot, job.err = s.converge(ctx, req)
	if job.err == nil {
		job.err = s.Sweep(ctx, time.Now().UTC().Add(-Retention))
		if errors.Is(job.err, ErrMaintenanceDeferred) {
			job.err = nil
		}
	}
	return job.snapshot, job.err
}

// EnsurePath publishes one path-scoped snapshot for security scanning.
func (s *Store) EnsurePath(ctx context.Context, projectDir string, verify Verify) (Snapshot, error) {
	abs, err := filepath.Abs(strings.TrimSpace(projectDir))
	if err != nil {
		return Snapshot{}, err
	}
	return s.Ensure(ctx, Request{Roots: []Root{{Path: filepath.Clean(abs)}}, Verify: verify})
}

// HeadIDForPath returns the last manifest for projectDir.
func (s *Store) HeadIDForPath(ctx context.Context, projectDir string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(projectDir))
	if err != nil {
		return "", err
	}
	id, err := s.queries.GetSourceSnapshotHeadID(ctx, rootsKey([]Root{{Path: filepath.Clean(abs)}}))
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// SourceSnapshotID publishes projectDir when necessary and returns its identity.
func (s *Store) SourceSnapshotID(ctx context.Context, projectDir string) (string, error) {
	snapshot, err := s.EnsurePath(ctx, projectDir, VerifyStat)
	return snapshot.ID, err
}

// ChangeTokenTrusted reports whether every root has complete watcher coverage.
func ChangeTokenTrusted(roots []Root) bool {
	cleaned := normalizeRoots(roots)
	for _, root := range cleaned {
		if !repochange.Coverage(root.Path).Complete {
			return false
		}
	}
	return len(cleaned) > 0
}

// ChangeToken is a scheduling hint, not a freshness proof.
func ChangeToken(roots []Root) string {
	parts := make([]string, 0, len(roots))
	for _, root := range normalizeRoots(roots) {
		epoch := repochange.CurrentEpoch(root.Path)
		parts = append(parts, root.Path+"\x00"+epoch.BootID+"\x00"+strconv.FormatUint(epoch.Value, 10))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
