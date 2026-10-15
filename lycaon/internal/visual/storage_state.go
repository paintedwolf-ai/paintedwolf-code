package visual

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

const maxIdleArtifactRepairStates = 32

type projectStorageLockRegistry struct {
	mu      sync.Mutex
	entries map[string]*projectStorageLock
}

type projectStorageLock struct {
	mu   sync.Mutex
	refs int
}

func (r *projectStorageLockRegistry) lock(key string) func() {
	r.mu.Lock()
	if r.entries == nil {
		r.entries = make(map[string]*projectStorageLock)
	}
	entry := r.entries[key]
	if entry == nil {
		entry = &projectStorageLock{}
		r.entries[key] = entry
	}
	entry.refs++
	r.mu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		r.mu.Lock()
		entry.refs--
		if entry.refs == 0 && r.entries[key] == entry {
			delete(r.entries, key)
		}
		r.mu.Unlock()
	}
}

type artifactRepairRegistry struct {
	mu      sync.Mutex
	entries map[string]*artifactRepairEntry
}

type artifactRepairEntry struct {
	state artifactPruneState
	users int
	drop  bool
}

func (r *artifactRepairRegistry) acquire(key string) (*artifactPruneState, func()) {
	r.mu.Lock()
	if r.entries == nil {
		r.entries = make(map[string]*artifactRepairEntry)
	}
	entry := r.entries[key]
	if entry == nil {
		entry = &artifactRepairEntry{}
		r.entries[key] = entry
	}
	entry.users++
	r.mu.Unlock()
	return &entry.state, func() { r.release(key, entry) }
}

func (r *artifactRepairRegistry) release(key string, entry *artifactRepairEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry.users--
	if entry.users == 0 && (entry.drop || entry.state.dir == nil) {
		entry.state.close()
		if r.entries[key] == entry {
			delete(r.entries, key)
		}
	}
	r.trim()
}

func (r *artifactRepairRegistry) delete(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[key]
	if entry == nil {
		return
	}
	entry.drop = true
	if entry.users == 0 {
		entry.state.close()
		delete(r.entries, key)
	}
}

func (r *artifactRepairRegistry) trim() {
	idle := 0
	for _, entry := range r.entries {
		if entry.users == 0 && entry.state.dir != nil {
			idle++
		}
	}
	for key, entry := range r.entries {
		if idle <= maxIdleArtifactRepairStates {
			return
		}
		if entry.users != 0 || entry.state.dir == nil {
			continue
		}
		entry.state.close()
		delete(r.entries, key)
		idle--
	}
}

func (b durableBlobs) newArtifactRecord(ctx context.Context, projectID, rootSessionID, holder string, entry Entry, wire api.VisualArtifact, raw []byte, prior ArtifactRecord, hadPrior bool) ArtifactRecord {
	rec := ArtifactRecord{
		ArtifactScope: ArtifactScope{
			ID:              wire.ID,
			ProjectID:       projectID,
			RootSessionID:   normalizeKey(rootSessionID),
			SessionID:       holder,
			WorkflowRunID:   b.runForSession(ctx, entry.WorkflowRunID, holder),
			ToolCallID:      wire.ToolCallID,
			OriginMessageID: wire.OriginMessageID,
			OperationID:     entry.OperationID,
			NaturalKey:      entry.NaturalKey,
		},
		ArtifactContent: ArtifactContent{
			ContentHash: artifactContentHash(raw),
			ByteSize:    int64(len(raw)),
			Mime:        wire.Mime,
			Source:      string(wire.Source),
			Caption:     wire.Caption,
			Width:       wire.Width,
			Height:      wire.Height,
		},
		ArtifactLifecycle: ArtifactLifecycle{
			EvidenceHandle: wire.EvidenceHandle,
			PageID:         wire.PageID,
			Perceive:       wire.Perceive,
			DurationMS:     wire.DurationMS,
			CreatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		},
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
	return rec
}

// identify resolves a scoped write ID without reviving tombstones.
func (b durableBlobs) identify(ctx context.Context, projectID, rootSessionID string, entry Entry) (string, error) {
	s := b.s
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
func (b durableBlobs) runForSession(ctx context.Context, declared, sessionID string) string {
	s := b.s
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

func (b durableBlobs) resolveProject(ctx context.Context, rootSessionID string) (string, error) {
	s := b.s
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

