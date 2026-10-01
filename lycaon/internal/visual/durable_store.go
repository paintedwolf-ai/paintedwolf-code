package visual

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/storageusage"
	"github.com/lycaon/lycaon/pkg/api"
)

// ActiveRunLookup returns a session's active workflow run.
type ActiveRunLookup func(ctx context.Context, sessionID string) (string, error)

// DurableConfig wires the two-tier visual store.
type DurableConfig struct {
	DataDir string
	// ArtifactsDir resolves project artifact storage.
	ArtifactsDir ArtifactsDirFunc
	Lookup       SessionProjectLookup
	ActiveRun    ActiveRunLookup
	Records      *Records
	Hot          *MemoryStore
}

// ArtifactsDirFunc resolves project artifact storage.
type ArtifactsDirFunc func(projectID string) (string, error)

// DurableStore combines a hot cache, records, and project blob storage.
type DurableStore struct {
	lifecycle       *bloblifecycle.Lifecycle
	hot             *MemoryStore
	artifactsDir    ArtifactsDirFunc
	lookup          SessionProjectLookup
	activeRun       ActiveRunLookup
	records         *Records
	rootProj        scopedstore.LRU[string]
	storageLocks    projectStorageLockRegistry
	reconciled      scopedstore.LRU[struct{}]
	reconcileStates artifactRepairRegistry
	garbage         artifactGCState
}

var _ Store = (*DurableStore)(nil)

// NewDurableStore constructs the two-tier store.
func NewDurableStore(cfg DurableConfig) *DurableStore {
	hot := cfg.Hot
	if hot == nil {
		hot = NewMemoryStore()
	}
	if cfg.Records != nil {
		cfg.Records.onHandleBound = func(artifactID, handle string) {
			// An artifact the hot tier does not hold has nothing to refresh.
			_ = hot.BindEvidenceHandle(context.Background(), artifactID, handle)
		}
	}
	return &DurableStore{
		lifecycle:    bloblifecycle.ForDevice(cfg.DataDir),
		hot:          hot,
		artifactsDir: cfg.ArtifactsDir,
		lookup:       cfg.Lookup,
		activeRun:    cfg.ActiveRun,
		records:      cfg.Records,
	}
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
	rec := ArtifactRecord{
		ID:              wire.ID,
		ProjectID:       projectID,
		RootSessionID:   normalizeKey(rootSessionID),
		SessionID:       holder,
		WorkflowRunID:   s.runForSession(ctx, entry.WorkflowRunID, holder),
		ToolCallID:      wire.ToolCallID,
		OriginMessageID: wire.OriginMessageID,
		OperationID:     entry.OperationID,
		NaturalKey:      entry.NaturalKey,
		ContentHash:     artifactContentHash(raw),
		ByteSize:        int64(len(raw)),
		Mime:            wire.Mime,
		Source:          string(wire.Source),
		Caption:         wire.Caption,
		EvidenceHandle:  wire.EvidenceHandle,
		PageID:          wire.PageID,
		Perceive:        wire.Perceive,
		DurationMS:      wire.DurationMS,
		Width:           wire.Width,
		Height:          wire.Height,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}
	if entry.AutomaticRecording {
		rec.RetentionClass = "recording"
	}
	if hadPrior {
		rec.CreatedAt = prior.CreatedAt
	}
	if wire.RecordedAt != nil {
		rec.RecordedAt = wire.RecordedAt.UTC().Format(time.RFC3339Nano)
	}
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

// identify resolves a scoped write ID without reviving tombstones.
func (s *DurableStore) identify(ctx context.Context, projectID, rootSessionID string, entry Entry) (string, error) {
	claimed, err := s.records.ClaimedID(ctx, projectID, entry.OperationID, entry.NaturalKey)
	if err != nil {
		return "", err
	}
	id := normalizeID(entry.Meta.ID)
	if claimed != "" {
		id = claimed
	}
	if id == "" {
		return "", nil
	}
	existing, found, err := s.records.GetInProject(ctx, projectID, id)
	if err != nil {
		return "", err
	}
	if !found {
		return id, nil
	}
	if normalizeKey(existing.RootSessionID) != normalizeKey(rootSessionID) {
		return "", fmt.Errorf("artifact id %s belongs to another session tree", id)
	}
	if existing.Deleted() {
		return "", nil
	}
	return id, nil
}

// runForSession resolves the producing workflow run.
func (s *DurableStore) runForSession(ctx context.Context, declared, sessionID string) string {
	if runID := strings.TrimSpace(declared); runID != "" {
		return runID
	}
	if s.activeRun == nil || sessionID == "" {
		return ""
	}
	runID, err := s.activeRun(ctx, sessionID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(runID)
}

// dropSupersededBlob removes orphaned replacement bytes.
func (s *DurableStore) dropSupersededBlob(ctx context.Context, projectID, dir string, prior ArtifactRecord, next ArtifactRecord) {
	old := strings.TrimSpace(prior.ContentHash)
	if old == "" || old == next.ContentHash {
		return
	}
	s.removeBlobIfOrphan(ctx, projectID, dir, old, next.ID)
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
	// Re-warm is best effort; the bytes are already in hand.
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

// Delete tombstones the record and drops orphaned bytes.
func (s *DurableStore) Delete(ctx context.Context, projectID, artifactID, reason string) (ArtifactDeleteResult, bool, error) {
	if s == nil {
		return ArtifactDeleteResult{}, false, fmt.Errorf("visual store not configured")
	}
	projectID = strings.TrimSpace(projectID)
	artifactID = normalizeID(artifactID)
	unlock := s.lockProjectStorage(projectID)
	defer unlock()
	rec, refs, err := s.records.SoftDelete(ctx, projectID, artifactID, reason)
	if err != nil || rec.ID == "" {
		return ArtifactDeleteResult{}, false, err
	}
	s.hot.forget(artifactID)
	if dir, derr := s.artifactsDir(projectID); derr == nil {
		s.removeBlobIfOrphan(ctx, projectID, dir, rec.ContentHash, rec.ID)
	} else {
		s.resetProjectStorageReconciliation(projectID)
	}
	deletedAt, _ := time.Parse(time.RFC3339Nano, rec.DeletedAt)
	if refs == nil {
		refs = []ArtifactReference{}
	}
	return ArtifactDeleteResult{ID: rec.ID, DeletedAt: deletedAt, References: refs}, true, nil
}

func (s *DurableStore) Discard(ctx context.Context, projectID, artifactID string) error {
	if s == nil {
		return fmt.Errorf("visual store not configured")
	}
	projectID = strings.TrimSpace(projectID)
	artifactID = normalizeID(artifactID)
	unlock := s.lockProjectStorage(projectID)
	defer unlock()
	rec, found, err := s.records.Discard(ctx, projectID, artifactID)
	if err != nil || !found {
		return err
	}
	s.hot.forget(artifactID)
	if dir, derr := s.artifactsDir(projectID); derr == nil {
		s.removeBlobIfOrphan(ctx, projectID, dir, rec.ContentHash, rec.ID)
	} else {
		s.resetProjectStorageReconciliation(projectID)
	}
	return nil
}

func (s *DurableStore) reconcileProjectStorage(ctx context.Context, projectID, dir string) error {
	if _, ok := s.reconciled.Load(projectID); ok {
		return nil
	}
	unlock := s.lockProjectStorage(projectID)
	defer unlock()
	if _, ok := s.reconciled.Load(projectID); ok {
		return nil
	}
	state, release := s.reconcileStates.acquire(projectID)
	defer release()
	if !s.lifecycle.TryLock() {
		return nil
	}
	defer s.lifecycle.Unlock()
	done, err := pruneArtifactBlobBatch(dir, state, func(hash string) (bool, error) {
		return s.records.HashIsOrphan(ctx, projectID, hash, "")
	})
	if err != nil {
		s.reconcileStates.delete(projectID)
		return err
	}
	if done {
		s.reconcileStates.delete(projectID)
		s.reconciled.Store(projectID, struct{}{})
	}
	return nil
}

func (s *DurableStore) lockProjectStorage(projectID string) func() {
	return s.storageLocks.lock(projectID)
}

func (s *DurableStore) removeBlob(ctx context.Context, projectID, dir, hash string) {
	if err := removeArtifactBlob(dir, hash); err != nil {
		s.resetProjectStorageReconciliation(projectID)
		return
	}
	if _, err := s.records.sqlDB.ExecContext(ctx, `DELETE FROM artifact_gc_queue WHERE project_id=? AND content_hash=?`, projectID, hash); err != nil {
		s.resetProjectStorageReconciliation(projectID)
	}
}

func (s *DurableStore) removeBlobIfOrphan(ctx context.Context, projectID, dir, hash, excludeID string) {
	if !s.lifecycle.TryLock() {
		s.resetProjectStorageReconciliation(projectID)
		return
	}
	defer s.lifecycle.Unlock()
	orphan, err := s.records.HashIsOrphan(ctx, projectID, hash, excludeID)
	if err != nil {
		s.resetProjectStorageReconciliation(projectID)
		return
	}
	if orphan {
		s.removeBlob(ctx, projectID, dir, hash)
	}
}

func (s *DurableStore) resetProjectStorageReconciliation(projectID string) {
	s.reconciled.Delete(projectID)
	s.reconcileStates.delete(projectID)
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

func (s *DurableStore) resolveProject(ctx context.Context, rootSessionID string) (string, error) {
	rootSessionID = normalizeKey(rootSessionID)
	if rootSessionID == "" {
		return "", nil
	}
	if id, ok := s.rootProj.Load(rootSessionID); ok && id != "" {
		return id, nil
	}
	if s.lookup == nil {
		return "", nil
	}
	id, err := s.lookup(ctx, rootSessionID)
	id = strings.TrimSpace(id)
	if err != nil {
		return "", err
	}
	if id != "" {
		s.rootProj.Store(rootSessionID, id)
	}
	return id, nil
}

func listItems(records []ArtifactRecord, origins map[string]ArtifactOrigin, refs map[string][]api.ArtifactReferenceCount) []api.ArtifactListItem {
	out := make([]api.ArtifactListItem, 0, len(records))
	for _, rec := range records {
		origin := origins[rec.ID]
		originMessageID := strings.TrimSpace(rec.OriginMessageID)
		if originMessageID == "" {
			originMessageID = strings.TrimSpace(origin.MessageID)
		}
		// Claim references can supply a missing tool call.
		toolCallID := strings.TrimSpace(rec.ToolCallID)
		if toolCallID == "" {
			toolCallID = strings.TrimSpace(origin.ToolCallID)
		}
		item := api.ArtifactListItem{
			ID:              rec.ID,
			Mime:            rec.Mime,
			Source:          api.VisualArtifactSource(rec.Source),
			Caption:         rec.Caption,
			EvidenceHandle:  rec.EvidenceHandle,
			PageID:          rec.PageID,
			SessionID:       rec.SessionID,
			WorkflowRunID:   rec.WorkflowRunID,
			ToolCallID:      toolCallID,
			DurationMS:      rec.DurationMS,
			OriginMessageID: originMessageID,
			References:      refs[rec.ID],
		}
		if ts, err := time.Parse(time.RFC3339Nano, rec.CreatedAt); err == nil {
			item.CreatedAt = ts
		}
		if rec.RecordedAt != "" {
			if ts, err := time.Parse(time.RFC3339Nano, rec.RecordedAt); err == nil {
				item.RecordedAt = &ts
			}
		}
		out = append(out, item)
	}
	return out
}

func metaFromRecord(rec ArtifactRecord) api.VisualArtifact {
	meta := api.VisualArtifact{
		ID:              rec.ID,
		Mime:            rec.Mime,
		Source:          api.VisualArtifactSource(rec.Source),
		Caption:         rec.Caption,
		EvidenceHandle:  rec.EvidenceHandle,
		PageID:          rec.PageID,
		ToolCallID:      rec.ToolCallID,
		OriginMessageID: rec.OriginMessageID,
		DurationMS:      rec.DurationMS,
		Width:           rec.Width,
		Height:          rec.Height,
		Perceive:        rec.Perceive,
		StoreRef:        true,
	}
	if rec.RecordedAt != "" {
		if ts, err := time.Parse(time.RFC3339Nano, rec.RecordedAt); err == nil {
			meta.RecordedAt = &ts
		}
	}
	return meta
}
