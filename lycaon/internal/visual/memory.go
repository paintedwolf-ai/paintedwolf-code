package visual

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/storageusage"
	"github.com/lycaon/lycaon/pkg/api"
)

// MemoryStore is the visual hot cache.
type MemoryStore struct {
	mu    sync.Mutex
	trees map[string]*treeBucket
}

type treeBucket struct {
	entries map[string]*storedArtifact
	order   []string
	mem     int64
}

type storedArtifact struct {
	meta      api.VisualArtifact
	bytes     []byte
	updatedAt time.Time
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an in-memory visual hot cache (no disk spill).
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{trees: make(map[string]*treeBucket)}
}

func (s *MemoryStore) Put(_ context.Context, rootSessionID string, entry Entry) (api.VisualArtifact, error) {
	if s == nil {
		return api.VisualArtifact{}, fmt.Errorf("visual store not configured")
	}
	key := normalizeKey(rootSessionID)
	if key == "" {
		return api.VisualArtifact{}, fmt.Errorf("root session id is required")
	}
	meta, raw, err := prepareEntry(entry)
	if err != nil {
		return api.VisualArtifact{}, err
	}
	id := meta.ID

	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.bucketLocked(key)
	// Existing IDs represent replaceable slots or retried writes.
	if prev, ok := b.entries[id]; ok {
		b.mem -= int64(len(prev.bytes))
		b.removeOrderLocked(id)
	}
	art := &storedArtifact{meta: meta, bytes: raw, updatedAt: time.Now().UTC()}
	b.mem += int64(len(raw))
	b.entries[id] = art
	b.touchLocked(id)
	for b.mem > MaxTreeMemoryBytes {
		if !b.dropOldestUnlocked() {
			break
		}
	}
	wire := art.meta
	wire.StoreRef = true
	return wire, nil
}

func (s *MemoryStore) Resolve(_ context.Context, rootSessionID, artifactID string) Resolution {
	if s == nil {
		return Absent(AbsenceUnknown)
	}
	key := normalizeKey(rootSessionID)
	id := normalizeID(artifactID)
	if key == "" || id == "" {
		return Absent(AbsenceUnknown)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.trees[key]
	if b == nil {
		return s.absentElsewhereLocked(key, id)
	}
	art, ok := b.entries[id]
	if !ok {
		return s.absentElsewhereLocked(key, id)
	}
	b.touchLocked(id)
	return Present(art.meta, append([]byte(nil), art.bytes...))
}

// absentElsewhereLocked detects cross-tree IDs.
func (s *MemoryStore) absentElsewhereLocked(excludeRoot, artifactID string) Resolution {
	for root, b := range s.trees {
		if root == excludeRoot || b == nil {
			continue
		}
		if art, ok := b.entries[artifactID]; ok && art != nil {
			return Absent(AbsenceForeign)
		}
	}
	return Absent(AbsenceUnknown)
}

// ListProject has no project index in the memory store.
func (s *MemoryStore) ListProject(_ context.Context, _ string, _ ArtifactPageQuery) (ArtifactPage, error) {
	return ArtifactPage{Items: []api.ArtifactListItem{}}, nil
}

func (s *MemoryStore) ListTree(_ context.Context, rootSessionID string) ([]api.ArtifactListItem, error) {
	if s == nil {
		return []api.ArtifactListItem{}, nil
	}
	root := normalizeKey(rootSessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.trees[root]
	if b == nil {
		return []api.ArtifactListItem{}, nil
	}
	out := make([]api.ArtifactListItem, 0, len(b.entries))
	for _, id := range b.order {
		art := b.entries[id]
		if art == nil {
			continue
		}
		out = append(out, api.ArtifactListItem{
			ID:              art.meta.ID,
			Mime:            art.meta.Mime,
			Source:          art.meta.Source,
			Caption:         art.meta.Caption,
			EvidenceHandle:  art.meta.EvidenceHandle,
			PageID:          art.meta.PageID,
			SessionID:       root,
			ToolCallID:      art.meta.ToolCallID,
			OriginMessageID: art.meta.OriginMessageID,
			CreatedAt:       art.updatedAt,
			RecordedAt:      art.meta.RecordedAt,
			DurationMS:      art.meta.DurationMS,
		})
	}
	return out, nil
}

// HandleBinder stamps a host-minted evidence handle onto a stored artifact.
type HandleBinder interface {
	BindEvidenceHandle(ctx context.Context, artifactID, handle string) error
}

var _ HandleBinder = (*MemoryStore)(nil)

// BindEvidenceHandle stamps a minted handle onto a hot artifact.
func (s *MemoryStore) BindEvidenceHandle(_ context.Context, artifactID, handle string) error {
	id, handle := normalizeID(artifactID), strings.TrimSpace(handle)
	if s == nil || id == "" || handle == "" {
		return fmt.Errorf("artifact id and evidence handle required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.trees {
		if b == nil {
			continue
		}
		if art, ok := b.entries[id]; ok && art != nil {
			art.meta.EvidenceHandle = handle
			return nil
		}
	}
	return fmt.Errorf("bind evidence handle %s: artifact %s not found", handle, id)
}

// FindByEvidenceHandle returns the newest hot match.
func (s *MemoryStore) FindByEvidenceHandle(_ context.Context, rootSessionID, handle string) (string, error) {
	handle = strings.TrimSpace(handle)
	if s == nil || handle == "" {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.trees[normalizeKey(rootSessionID)]
	if b == nil {
		return "", nil
	}
	for i := len(b.order) - 1; i >= 0; i-- {
		art := b.entries[b.order[i]]
		if art == nil {
			continue
		}
		if strings.TrimSpace(art.meta.EvidenceHandle) == handle {
			return art.meta.ID, nil
		}
	}
	return "", nil
}

// Delete drops the hot copy.
func (s *MemoryStore) Delete(_ context.Context, _, artifactID, _ string) (ArtifactDeleteResult, bool, error) {
	if s == nil {
		return ArtifactDeleteResult{}, false, nil
	}
	id := normalizeID(artifactID)
	if id == "" || !s.forget(id) {
		return ArtifactDeleteResult{}, false, nil
	}
	return ArtifactDeleteResult{
		ID:         id,
		DeletedAt:  time.Now().UTC(),
		References: []ArtifactReference{},
	}, true, nil
}

func (s *MemoryStore) Discard(_ context.Context, _, artifactID string) error {
	s.forget(artifactID)
	return nil
}

// forget removes an ID from every tree bucket.
func (s *MemoryStore) forget(artifactID string) bool {
	id := normalizeID(artifactID)
	if s == nil || id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := false
	for _, b := range s.trees {
		art, ok := b.entries[id]
		if !ok {
			continue
		}
		b.mem -= int64(len(art.bytes))
		delete(b.entries, id)
		b.removeOrderLocked(id)
		removed = true
	}
	return removed
}

// StorageUsage reports no durable bytes for the hot cache.
func (s *MemoryStore) StorageUsage(_ context.Context, _ string) (storageusage.Usage, error) {
	return storageusage.Usage{Lane: storageusage.LaneArtifacts, Scope: storageusage.ScopeProject}, nil
}

func (s *MemoryStore) bucketLocked(key string) *treeBucket {
	b, ok := s.trees[key]
	if !ok {
		b = &treeBucket{entries: make(map[string]*storedArtifact)}
		s.trees[key] = b
	}
	return b
}

func (b *treeBucket) touchLocked(id string) {
	b.removeOrderLocked(id)
	b.order = append(b.order, id)
}

func (b *treeBucket) removeOrderLocked(id string) {
	for i, cur := range b.order {
		if cur == id {
			b.order = append(b.order[:i], b.order[i+1:]...)
			return
		}
	}
}

func (b *treeBucket) dropOldestUnlocked() bool {
	for _, id := range b.order {
		art := b.entries[id]
		if art == nil {
			continue
		}
		b.mem -= int64(len(art.bytes))
		delete(b.entries, id)
		b.removeOrderLocked(id)
		return true
	}
	return false
}
