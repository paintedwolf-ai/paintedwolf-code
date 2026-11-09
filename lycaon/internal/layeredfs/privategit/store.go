package privategit

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type leaseRecord struct {
	id     string
	kind   LeaseKind
	holder string
	oids   map[string]struct{}
	packs  map[string]struct{}
}

type storeStock struct {
	packOIDs        map[string]map[string]struct{}
	packSizes       map[string]int64
	looseSizes      map[string]int64
	logicalOIDRefs  map[string]int
	looseBytesTotal int64
	packBytesTotal  int64
	startupDuration time.Duration
}

// RetainedObjectStore manages the engine-retained content-addressed Git object store (#1671).
type RetainedObjectStore struct {
	rootDir    string
	objectsDir string
	importsDir string

	mu        sync.Mutex
	nextLease uint64
	leases    map[string]*leaseRecord
	stock     storeStock

	objectsImported   atomic.Int64
	objectsReused     atomic.Int64
	bytesImported     atomic.Int64
	bytesReused       atomic.Int64
	bytesNewlyWritten atomic.Int64
}

// NewRetainedObjectStore initializes or opens an engine-retained Git object store at rootDir.
func NewRetainedObjectStore(rootDir string) (*RetainedObjectStore, error) {
	return NewRetainedObjectStoreContext(context.Background(), rootDir)
}

// NewRetainedObjectStoreContext initializes or opens an engine-retained Git object store at rootDir,
// honoring ctx cancellation while reconstructing preexisting stock on disk.
func NewRetainedObjectStoreContext(ctx context.Context, rootDir string) (*RetainedObjectStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(rootDir) == "" {
		return nil, fmt.Errorf("privategit: retained store rootDir is required")
	}
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}
	objectsDir := filepath.Join(absRoot, "objects")
	importsDir := filepath.Join(absRoot, "imports")
	for _, d := range []string{
		absRoot,
		objectsDir,
		filepath.Join(objectsDir, "info"),
		filepath.Join(objectsDir, "pack"),
		filepath.Join(absRoot, "refs"),
		importsDir,
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("privategit: create store dir %s: %w", d, err)
		}
	}
	headPath := filepath.Join(absRoot, "HEAD")
	if _, err := os.Stat(headPath); os.IsNotExist(err) {
		_ = os.WriteFile(headPath, []byte("ref: refs/heads/main\n"), 0o644)
	}
	s := &RetainedObjectStore{
		rootDir:    absRoot,
		objectsDir: objectsDir,
		importsDir: importsDir,
		leases:     make(map[string]*leaseRecord),
		stock: storeStock{
			packOIDs:       make(map[string]map[string]struct{}),
			packSizes:      make(map[string]int64),
			looseSizes:     make(map[string]int64),
			logicalOIDRefs: make(map[string]int),
		},
	}
	if err := initStockFromDiskLocked(ctx, s); err != nil {
		return nil, err
	}
	// A store with nothing retained has nothing to protect, and read-only directories
	// would stop its host directory from being removed.
	if storeHoldsObjects(objectsDir) {
		lockStoreReadOnlyBoundary(s)
	}
	return s, nil
}

// RootDir returns the store's top-level directory.
func (s *RetainedObjectStore) RootDir() string {
	return s.rootDir
}

// ObjectsDir returns the read-only content-addressed objects directory used for alternates.
func (s *RetainedObjectStore) ObjectsDir() string {
	return s.objectsDir
}

// Metrics returns an O(1) snapshot of the store's cumulative and retained stock counters.
func (s *RetainedObjectStore) Metrics() StoreMetrics {
	s.mu.Lock()
	retainedObjects := int64(len(s.stock.logicalOIDRefs))
	retainedBytes := s.stock.looseBytesTotal + s.stock.packBytesTotal
	startupDur := s.stock.startupDuration
	s.mu.Unlock()
	return StoreMetrics{
		ObjectsImported:     s.objectsImported.Load(),
		ObjectsReused:       s.objectsReused.Load(),
		BytesImported:       s.bytesImported.Load(),
		BytesReused:         s.bytesReused.Load(),
		BytesNewlyWritten:   s.bytesNewlyWritten.Load(),
		RetainedObjects:     retainedObjects,
		RetainedBytes:       retainedBytes,
		StartupInitDuration: startupDur,
	}
}

// RecordPrivateWriteBytes adds newly written private object bytes to the store metrics.
func (s *RetainedObjectStore) RecordPrivateWriteBytes(n int64) {
	if n > 0 {
		s.bytesNewlyWritten.Add(n)
	}
}

// RetainForView creates an active object retention lease for a mounted or materialized view.
func (s *RetainedObjectStore) RetainForView(viewID string, oids []string) (string, error) {
	return createStoreLease(s, LeaseView, viewID, oids)
}

// RetainForCheckpoint creates an immutable checkpoint retention lease.
func (s *RetainedObjectStore) RetainForCheckpoint(checkpointID string, oids []string) (string, error) {
	return createStoreLease(s, LeaseCheckpoint, checkpointID, oids)
}

// RetainForStackedChild creates an active lease for a stacked child workspace over a checkpoint.
func (s *RetainedObjectStore) RetainForStackedChild(childViewID, parentCheckpointID string, oids []string) (string, error) {
	return createStoreLease(s, LeaseStackedChild, childViewID+":"+parentCheckpointID, oids)
}

// RetainForRefImport creates an active lease protecting objects during trunk ref import.
func (s *RetainedObjectStore) RetainForRefImport(importID string, oids []string) (string, error) {
	return createStoreLease(s, LeaseRefImport, importID, oids)
}

func createStoreLease(s *RetainedObjectStore, kind LeaseKind, holder string, oids []string) (string, error) {
	if strings.TrimSpace(holder) == "" {
		return "", fmt.Errorf("privategit: lease holder is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextLease++
	id := fmt.Sprintf("lease-%s-%06d", kind, s.nextLease)
	oidSet := make(map[string]struct{}, len(oids))
	packSet := make(map[string]struct{})
	for _, raw := range oids {
		oid := strings.ToLower(strings.TrimSpace(raw))
		if oid == "" {
			continue
		}
		if strings.HasPrefix(oid, "pack:") {
			packSet[strings.TrimPrefix(oid, "pack:")] = struct{}{}
			continue
		}
		oidSet[oid] = struct{}{}
		for packName, members := range s.stock.packOIDs {
			if _, ok := members[oid]; ok {
				packSet[packName] = struct{}{}
			}
		}
	}
	for packName := range s.stock.packOIDs {
		if len(s.stock.packOIDs[packName]) == 0 {
			packSet[packName] = struct{}{}
		}
	}
	s.leases[id] = &leaseRecord{
		id:     id,
		kind:   kind,
		holder: holder,
		oids:   oidSet,
		packs:  packSet,
	}
	return id, nil
}

// ReleaseLease releases an active retention lease so unreferenced objects may be collected.
func (s *RetainedObjectStore) ReleaseLease(leaseID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.leases[leaseID]; !ok {
		return fmt.Errorf("privategit: unknown or already released lease %q", leaseID)
	}
	delete(s.leases, leaseID)
	return nil
}

// ActiveLeases returns the count of currently active retention leases.
func (s *RetainedObjectStore) ActiveLeases() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.leases)
}

// Close restores write permissions on store directories, so the engine's data directory stays removable
// once it stops. Opening the store again restores the read-only boundary.
func (s *RetainedObjectStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlockStoreWriteBoundary(s)
	return nil
}

func storeHoldsObjects(objectsDir string) bool {
	entries, err := os.ReadDir(objectsDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "info":
		case "pack":
			packs, packErr := os.ReadDir(filepath.Join(objectsDir, "pack"))
			if packErr != nil || len(packs) > 0 {
				return true
			}
		default:
			if entry.IsDir() {
				sub, subErr := os.ReadDir(filepath.Join(objectsDir, entry.Name()))
				if subErr != nil || len(sub) > 0 {
					return true
				}
				continue
			}
			return true
		}
	}
	return false
}

func unlockStoreWriteBoundary(s *RetainedObjectStore) {
	_ = filepath.WalkDir(s.objectsDir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(path, 0o755)
		}
		return nil
	})
}

func lockStoreReadOnlyBoundary(s *RetainedObjectStore) {
	var dirs []string
	_ = filepath.WalkDir(s.objectsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, path)
		} else {
			_ = os.Chmod(path, 0o444)
		}
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool {
		return len(dirs[i]) > len(dirs[j])
	})
	for _, dir := range dirs {
		_ = os.Chmod(dir, 0o555)
	}
}
