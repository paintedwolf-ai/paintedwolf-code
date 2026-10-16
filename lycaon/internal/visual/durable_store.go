package visual

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/storageusage"
	"github.com/lycaon/lycaon/pkg/api"
)

// ActiveRunLookup returns a session's active workflow run.
type ActiveRunLookup func(ctx context.Context, sessionID string) (string, error)

// DurableConfig wires the two-tier visual store.
type DurableConfig struct {
	DataDir      string
	ArtifactsDir ArtifactsDirFunc
	Lookup       SessionProjectLookup
	ActiveRun    ActiveRunLookup
	Records      *Records
	Hot          *MemoryStore
}

// ArtifactsDirFunc resolves project artifact storage.
type ArtifactsDirFunc func(projectID string) (string, error)

type durableState struct {
	rootProj        scopedstore.LRU[string]
	storageLocks    projectStorageLockRegistry
	reconciled      scopedstore.LRU[struct{}]
	reconcileStates artifactRepairRegistry
	garbage         artifactGCState
}

// DurableStore combines a hot cache, records, and project blob storage.
type DurableStore struct {
	lifecycle    *bloblifecycle.Lifecycle
	hot          *MemoryStore
	artifactsDir ArtifactsDirFunc
	lookup       SessionProjectLookup
	activeRun    ActiveRunLookup
	records      *Records
	durableState
	durableBlobs
}

type durableBlobs struct {
	s *DurableStore
}

var _ Store = (*DurableStore)(nil)

// NewDurableStore constructs the two-tier store.
func NewDurableStore(cfg DurableConfig) *DurableStore {
	hot := cfg.Hot
	if hot == nil {
		hot = NewMemoryStore()
	}
	records := cfg.Records
	if records == nil {
		records = &Records{}
	}
	records.r = records
	records.onHandleBound = func(ctx context.Context, artifactID, handle string) {
		_ = hot.BindEvidenceHandle(ctx, artifactID, handle)
	}
	s := &DurableStore{
		lifecycle:    bloblifecycle.ForDevice(cfg.DataDir),
		hot:          hot,
		artifactsDir: cfg.ArtifactsDir,
		lookup:       cfg.Lookup,
		activeRun:    cfg.ActiveRun,
		records:      records,
	}
	s.s = s
	return s
}

// Put publishes content before its durable record.
func (s *DurableStore) Put(ctx context.Context, rootSessionID string, entry Entry) (api.VisualArtifact, error) {
	if s == nil {
		return api.VisualArtifact{}, fmt.Errorf("visual store not configured")
	}
	projectID, err := s.resolveProject(ctx, rootSessionID)
	if err != nil {
		return api.VisualArtifact{}, err
	}
	if projectID == "" {
		return s.hot.Put(ctx, rootSessionID, entry)
	}
	s.rootProj.Store(normalizeKey(rootSessionID), projectID)
	dir, err := s.artifactsDir(projectID)
	if err != nil {
		return api.VisualArtifact{}, err
	}
	if err := s.reconcileProjectStorage(ctx, projectID, dir); err != nil {
		return api.VisualArtifact{}, err
	}
	unlock := s.lockProjectStorage(projectID)
	defer unlock()

	if entry.Meta.ID, err = s.identify(ctx, projectID, rootSessionID, entry); err != nil {
		return api.VisualArtifact{}, err
	}
	wire, raw, err := prepareEntry(entry)
	if err != nil {
		return api.VisualArtifact{}, err
	}
	wire.StoreRef = true

	holder := normalizeKey(entry.ProducerSessionID)
	if holder == "" {
		holder = normalizeKey(rootSessionID)
	}
	prior, hadPrior, err := s.records.GetInProject(ctx, projectID, wire.ID)
	if err != nil {
		return api.VisualArtifact{}, err
	}
	rec := s.newArtifactRecord(ctx, projectID, rootSessionID, holder, entry, wire, raw, prior, hadPrior)
	created, storedSize, err := putArtifactBlob(dir, rec.ContentHash, raw)
	if err != nil {
		return api.VisualArtifact{}, err
	}
	rec.StoredSize = storedSize
	if err := s.records.Commit(ctx, rec); err != nil {
		if created {
			s.removeBlobIfOrphan(ctx, projectID, dir, rec.ContentHash, "")
		}
		return api.VisualArtifact{}, err
	}
	s.dropSupersededBlob(ctx, projectID, dir, prior, rec)
	// Warm the hot tier only after the record commits.
	_, _ = s.hot.Put(ctx, rootSessionID, Entry{Meta: wire, Bytes: raw, ProducerSessionID: holder})
	return wire, nil
}

// Resolve returns bytes or a typed absence.
func (s *DurableStore) Resolve(ctx context.Context, rootSessionID, artifactID string) Resolution {
	if s == nil {
		return Absent(AbsenceUnknown)
	}
	hot := s.hot.Resolve(ctx, rootSessionID, artifactID)
	projectID, err := s.resolveProject(ctx, rootSessionID)
	if hot.IsPresent() {
		if err == nil && projectID != "" {
			if dir, dirErr := s.artifactsDir(projectID); dirErr == nil {
				_ = s.reconcileProjectStorage(ctx, projectID, dir)
			}
		}
		return hot
	}
	if err != nil {
		return Absent(AbsenceUnavailable)
	}
	if projectID == "" {
		return Absent(AbsenceUnknown)
	}
	rec, found, err := s.records.Get(ctx, artifactID)
	if err != nil || !found {
		return Absent(AbsenceUnknown)
	}
	if rec.Deleted() {
		return Absent(AbsenceDeleted)
	}
	if normalizeKey(rec.RootSessionID) != normalizeKey(rootSessionID) {
		return Absent(AbsenceForeign)
	}
	dir, err := s.artifactsDir(projectID)
	if err != nil {
		return Absent(AbsenceUnavailable)
	}
	if err := s.reconcileProjectStorage(ctx, projectID, dir); err != nil {
		return Absent(AbsenceUnavailable)
	}
	if rec.ByteSize < 0 || rec.ByteSize > MaxDurableBodyBytes {
		return Absent(AbsenceUnavailable)
	}
	raw, ok := readArtifactBlob(dir, rec.ContentHash, rec.ByteSize)
	if !ok {
		return Absent(AbsenceUnavailable)
	}
	meta := metaFromRecord(rec)
	_, _ = s.hot.Put(ctx, rootSessionID, Entry{Meta: meta, Bytes: raw, ProducerSessionID: rec.SessionID})
	return Present(meta, raw)
}

func (s *DurableStore) ListProject(ctx context.Context, projectID string, query ArtifactPageQuery) (ArtifactPage, error) {
	if s == nil {
		return ArtifactPage{}, fmt.Errorf("visual store not configured")
	}
	projectID = strings.TrimSpace(projectID)
	records, hasMore, err := s.records.ListProject(ctx, projectID, query)
	if err != nil {
		return ArtifactPage{}, err
	}
	origins, err := s.records.ProjectOriginsFor(ctx, projectID, records)
	if err != nil {
		return ArtifactPage{}, err
	}
	refs, err := s.records.ReferenceCountsFor(ctx, projectID, records)
	if err != nil {
		return ArtifactPage{}, err
	}
	page := ArtifactPage{Items: listItems(records, origins, refs)}
	if hasMore && len(records) > 0 {
		last := records[len(records)-1]
		page.NextCreatedAt, page.NextID = last.CreatedAt, last.ID
	}
	return page, nil
}

func (s *DurableStore) ListTree(ctx context.Context, rootSessionID string) ([]api.ArtifactListItem, error) {
	if s == nil {
		return nil, fmt.Errorf("visual store not configured")
	}
	rootSessionID = normalizeKey(rootSessionID)
	projectID, err := s.resolveProject(ctx, rootSessionID)
	if err != nil {
		return nil, err
	}
	if projectID == "" {
		return s.hot.ListTree(ctx, rootSessionID)
	}
	records, err := s.records.ListTree(ctx, projectID, rootSessionID)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return []api.ArtifactListItem{}, nil
	}
	origins, err := s.records.TreeOrigins(ctx, rootSessionID)
	if err != nil {
		return nil, err
	}
	refs, err := s.records.ReferenceCountsFor(ctx, projectID, records)
	if err != nil {
		return nil, err
	}
	return listItems(records, origins, refs), nil
}

// FindByEvidenceHandle returns the newest durable or hot match.
func (s *DurableStore) FindByEvidenceHandle(ctx context.Context, rootSessionID, handle string) (string, error) {
	if s == nil {
		return "", nil
	}
	rootSessionID = normalizeKey(rootSessionID)
	rec, found, err := s.records.FindByEvidenceHandle(ctx, rootSessionID, handle)
	if err != nil {
		return "", err
	}
	if found {
		return rec.ID, nil
	}
	return s.hot.FindByEvidenceHandle(ctx, rootSessionID, handle)
}

// StorageUsage reports retained project artifact bytes.
func (s *DurableStore) StorageUsage(ctx context.Context, projectID string) (storageusage.Usage, error) {
	if s == nil {
		return storageusage.Usage{Lane: storageusage.LaneArtifacts, Scope: storageusage.ScopeProject}, nil
	}
	usage := storageusage.Usage{Lane: storageusage.LaneArtifacts, Scope: storageusage.ScopeProject}
	used, err := s.records.ProjectBytes(ctx, projectID)
	if err != nil {
		return usage, err
	}
	usage.UsedBytes = used
	return usage, nil
}

