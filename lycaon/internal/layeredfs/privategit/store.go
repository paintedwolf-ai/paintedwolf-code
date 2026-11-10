package privategit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
)

type leaseRecord struct {
	id     string
	kind   LeaseKind
	holder string
	oids   map[string]struct{}
	packs  map[string]struct{}
}

type persistedLeaseRecord struct {
	FormatVersion int       `json:"format_version"`
	ID            string    `json:"id"`
	Kind          LeaseKind `json:"kind"`
	Holder        string    `json:"holder"`
	OIDs          []string  `json:"oids,omitempty"`
	Packs         []string  `json:"packs,omitempty"`
}

type storeStock struct {
	packOIDs              map[string]map[string]struct{}
	compactPacks          map[string]*compactPackIndex
	packSizes             map[string]int64
	looseSizes            map[string]int64
	logicalOIDRefs        map[string]int
	compactObjectsTotal   int64
	nonCompactOnlyObjects int64
	looseBytesTotal       int64
	packBytesTotal        int64
	startupDuration       time.Duration
}

// RetainedObjectStore manages the engine-retained content-addressed Git object store (#1671).
type RetainedObjectStore struct {
	rootDir    string
	objectsDir string
	importsDir string

	mu             sync.Mutex
	nextLease      uint64
	leases         map[string]*leaseRecord
	releasedLeases map[string]struct{}
	stock          storeStock

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
		filepath.Join(absRoot, "leases"),
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
		rootDir:        absRoot,
		objectsDir:     objectsDir,
		importsDir:     importsDir,
		leases:         make(map[string]*leaseRecord),
		releasedLeases: make(map[string]struct{}),
		stock: storeStock{
			packOIDs:       make(map[string]map[string]struct{}),
			compactPacks:   make(map[string]*compactPackIndex),
			packSizes:      make(map[string]int64),
			looseSizes:     make(map[string]int64),
			logicalOIDRefs: make(map[string]int),
		},
	}
	if err := initStockFromDiskLocked(ctx, s); err != nil {
		return nil, err
	}
	advanceNextLeaseFromDiskLocked(s)
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
	retainedObjects := s.stock.compactObjectsTotal + s.stock.nonCompactOnlyObjects
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
			base := strings.TrimPrefix(oid, "pack:")
			base = strings.TrimSuffix(strings.TrimSuffix(base, ".pack"), ".idx")
			if base != "" {
				packSet[base] = struct{}{}
			}
			continue
		}
		oidSet[oid] = struct{}{}
		for packName, members := range s.stock.packOIDs {
			if _, ok := members[oid]; ok {
				packSet[packName] = struct{}{}
			}
		}
		for packName, comp := range s.stock.compactPacks {
			if comp.containsHex(oid) {
				packSet[packName] = struct{}{}
			}
		}
	}
	for packName := range s.stock.packOIDs {
		if len(s.stock.packOIDs[packName]) == 0 {
			packSet[packName] = struct{}{}
		}
	}
	for packName, comp := range s.stock.compactPacks {
		if comp.count() == 0 {
			packSet[packName] = struct{}{}
		}
	}
	rec := &leaseRecord{
		id:     id,
		kind:   kind,
		holder: holder,
		oids:   oidSet,
		packs:  packSet,
	}
	if err := persistStoreLeaseRecordLocked(s, rec); err != nil {
		return "", err
	}
	s.leases[id] = rec
	delete(s.releasedLeases, id)
	return id, nil
}

// ReleaseLease releases an active retention lease so unreferenced objects may be collected.
func (s *RetainedObjectStore) ReleaseLease(leaseID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(leaseID) == "" {
		return fmt.Errorf("privategit: unknown or already released lease %q", leaseID)
	}
	_, inMem := s.leases[leaseID]
	delete(s.leases, leaseID)
	s.releasedLeases[leaseID] = struct{}{}
	diskErr := os.Remove(storeLeaseRecordPath(s, leaseID))
	if !inMem && diskErr != nil {
		return fmt.Errorf("privategit: unknown or already released lease %q", leaseID)
	}
	return nil
}

func storeLeaseRecordPath(s *RetainedObjectStore, leaseID string) string {
	return filepath.Join(s.rootDir, "leases", filepath.Base(leaseID)+".json")
}

func advanceNextLeaseFromDiskLocked(s *RetainedObjectStore) {
	entries, err := os.ReadDir(filepath.Join(s.rootDir, "leases"))
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".json")
		if idx := strings.LastIndexByte(name, '-'); idx >= 0 {
			if n, err := strconv.ParseUint(name[idx+1:], 10, 64); err == nil && n > s.nextLease {
				s.nextLease = n
			}
		}
	}
}

func persistStoreLeaseRecordLocked(s *RetainedObjectStore, rec *leaseRecord) error {
	if s == nil || s.rootDir == "" || rec == nil {
		return nil
	}
	oids := make([]string, 0, len(rec.oids))
	for oid := range rec.oids {
		oids = append(oids, oid)
	}
	sort.Strings(oids)
	packs := make([]string, 0, len(rec.packs))
	for p := range rec.packs {
		packs = append(packs, p)
	}
	sort.Strings(packs)
	payload, err := json.Marshal(persistedLeaseRecord{
		FormatVersion: 1,
		ID:            rec.id,
		Kind:          rec.kind,
		Holder:        rec.holder,
		OIDs:          oids,
		Packs:         packs,
	})
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(storeLeaseRecordPath(s, rec.id)),
		Source:   bytes.NewReader(payload),
		Mode:     0o600,
		DirMode:  0o755,
	})
	return err
}

func reacquirePersistedStoreLease(s *RetainedObjectStore, holder, leaseID string) error {
	if s == nil || s.rootDir == "" || strings.TrimSpace(leaseID) == "" {
		return fmt.Errorf("privategit: lease id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, released := s.releasedLeases[leaseID]; released {
		return fmt.Errorf("privategit: lease %q was released", leaseID)
	}
	if l, ok := s.leases[leaseID]; ok && l != nil {
		if holder != "" && l.holder != holder {
			return fmt.Errorf("privategit: lease %q holder mismatch (%q != %q)", leaseID, l.holder, holder)
		}
		return nil
	}
	raw, err := os.ReadFile(storeLeaseRecordPath(s, leaseID))
	if err != nil {
		return fmt.Errorf("privategit: read lease record %q: %w", leaseID, err)
	}
	var p persistedLeaseRecord
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return fmt.Errorf("privategit: decode lease record %q: %w", leaseID, err)
	}
	if p.FormatVersion != 1 || p.ID != leaseID || (holder != "" && p.Holder != holder) {
		return fmt.Errorf("privategit: invalid lease record %q", leaseID)
	}
	if len(p.OIDs) == 0 && len(p.Packs) == 0 {
		return fmt.Errorf("privategit: empty lease record %q", leaseID)
	}
	oidSet := make(map[string]struct{}, len(p.OIDs))
	for _, oid := range p.OIDs {
		oidSet[oid] = struct{}{}
	}
	packSet := make(map[string]struct{}, len(p.Packs))
	for _, pk := range p.Packs {
		packSet[pk] = struct{}{}
	}
	s.leases[leaseID] = &leaseRecord{
		id:     p.ID,
		kind:   p.Kind,
		holder: p.Holder,
		oids:   oidSet,
		packs:  packSet,
	}
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

func storeLeaseIntactOnDisk(s *RetainedObjectStore, leaseID string) bool {
	return storeLeaseBoundAndIntactOnDisk(s, "", leaseID)
}

func storeLeaseBoundAndIntactOnDisk(s *RetainedObjectStore, holder, leaseID string) bool {
	if s == nil || s.objectsDir == "" || strings.TrimSpace(leaseID) == "" {
		return false
	}
	if _, err := os.Stat(s.objectsDir); err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[leaseID]
	if !ok || l == nil {
		return false
	}
	if holder != "" && l.holder != holder {
		return false
	}
	if len(l.oids) == 0 && len(l.packs) == 0 {
		return false
	}
	for p := range l.packs {
		if _, err := os.Stat(filepath.Join(s.objectsDir, "pack", p+".pack")); err != nil {
			return false
		}
		if _, err := os.Stat(filepath.Join(s.objectsDir, "pack", p+".idx")); err != nil {
			return false
		}
	}
	for oid := range l.oids {
		if !storeHasObjectLockedOnDisk(s, oid) {
			return false
		}
	}
	return true
}

func storeHasObjectOnDisk(s *RetainedObjectStore, oid string) bool {
	if s == nil || s.objectsDir == "" || len(oid) != 40 || !isHexString(oid) {
		return false
	}
	if _, err := os.Stat(s.objectsDir); err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return storeHasObjectLockedOnDisk(s, oid)
}

func storeHasObjectLockedOnDisk(s *RetainedObjectStore, oid string) bool {
	if len(oid) != 40 || !isHexString(oid) {
		return false
	}
	if _, err := os.Stat(filepath.Join(s.objectsDir, oid[:2], oid[2:])); err == nil {
		return true
	}
	for base, members := range s.stock.packOIDs {
		if _, ok := members[oid]; ok {
			if _, err := os.Stat(filepath.Join(s.objectsDir, "pack", base+".pack")); err == nil {
				if _, err := os.Stat(filepath.Join(s.objectsDir, "pack", base+".idx")); err == nil {
					return true
				}
			}
		}
	}
	for base, comp := range s.stock.compactPacks {
		if comp.containsHex(oid) {
			if _, err := os.Stat(filepath.Join(s.objectsDir, "pack", base+".pack")); err == nil {
				if _, err := os.Stat(filepath.Join(s.objectsDir, "pack", base+".idx")); err == nil {
					return true
				}
			}
		}
	}
	return false
}
