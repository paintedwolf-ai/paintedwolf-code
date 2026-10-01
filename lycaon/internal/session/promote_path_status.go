package session

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/pkg/api"
)

type overlayPreviewSnapshot struct {
	PromoteOrder  api.WorkerPromoteOrderKind
	PromoteAfter  []string
	BlockedBy     []string
	CleanPaths    []string
	ConflictPaths []string
}

// promotePathSessionStore holds one session's promote bookkeeping.
type promotePathSessionStore struct {
	byJob      map[string][]api.WorkerPromotePathStatus // jobID -> path status
	previewJob map[string]overlayPreviewSnapshot        // jobID -> preview snapshot
}

// RecordPromotePathStatus caches path status for board injection.
func (m *Manager) RecordPromotePathStatus(sessionID, jobID string, statuses []api.WorkerPromotePathStatus) {
	if m == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	jobID = strings.TrimSpace(jobID)
	if sessionID == "" || jobID == "" || len(statuses) == 0 {
		return
	}
	store, ok := m.promotePathStatus.Load(sessionID)
	if !ok || store == nil {
		store = &promotePathSessionStore{
			byJob:      map[string][]api.WorkerPromotePathStatus{},
			previewJob: map[string]overlayPreviewSnapshot{},
		}
	}
	if store.byJob == nil {
		store.byJob = map[string][]api.WorkerPromotePathStatus{}
	}
	if store.previewJob == nil {
		store.previewJob = map[string]overlayPreviewSnapshot{}
	}
	store.byJob[jobID] = append([]api.WorkerPromotePathStatus(nil), statuses...)
	m.promotePathStatus.Store(sessionID, store)
}

// RecordOverlayPreviewSummary caches merge-planning fields.
func (m *Manager) RecordOverlayPreviewSummary(sessionID, jobID string, out *api.WorkerMergeResult) {
	if m == nil || out == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	jobID = strings.TrimSpace(jobID)
	if sessionID == "" || jobID == "" {
		return
	}
	store, ok := m.promotePathStatus.Load(sessionID)
	if !ok || store == nil {
		store = &promotePathSessionStore{
			byJob:      map[string][]api.WorkerPromotePathStatus{},
			previewJob: map[string]overlayPreviewSnapshot{},
		}
	}
	if store.previewJob == nil {
		store.previewJob = map[string]overlayPreviewSnapshot{}
	}
	snap := overlayPreviewSnapshot{
		PromoteOrder: out.PromoteOrder,
		PromoteAfter: append([]string(nil), out.PromoteAfter...),
		BlockedBy:    append([]string(nil), out.BlockedBy...),
		CleanPaths:   append([]string(nil), out.CleanPaths...),
	}
	for _, row := range out.PathStatus {
		if row.Status == api.WorkerPromotePathOutcomeConflict {
			snap.ConflictPaths = append(snap.ConflictPaths, row.Path)
		}
	}
	for _, row := range out.ConflictDigest {
		if row.Path != "" {
			snap.ConflictPaths = append(snap.ConflictPaths, row.Path)
		}
	}
	sort.Strings(snap.ConflictPaths)
	snap.ConflictPaths = dedupeSortedStrings(snap.ConflictPaths)
	store.previewJob[jobID] = snap
	m.promotePathStatus.Store(sessionID, store)
}

// OverlayPreviewSnapshot returns cached overlay-level preview fields for merge planning.
func (m *Manager) OverlayPreviewSnapshot(sessionID, jobID string) (api.WorkerPromoteOrderKind, []string, []string, []string, []string, bool) {
	if m == nil {
		return "", nil, nil, nil, nil, false
	}
	sessionID = strings.TrimSpace(sessionID)
	jobID = strings.TrimSpace(jobID)
	if sessionID == "" || jobID == "" {
		return "", nil, nil, nil, nil, false
	}
	store, ok := m.promotePathStatus.Load(sessionID)
	if !ok || store == nil || store.previewJob == nil {
		return "", nil, nil, nil, nil, false
	}
	snap, ok := store.previewJob[jobID]
	if !ok {
		return "", nil, nil, nil, nil, false
	}
	return snap.PromoteOrder,
		append([]string(nil), snap.PromoteAfter...),
		append([]string(nil), snap.BlockedBy...),
		append([]string(nil), snap.CleanPaths...),
		append([]string(nil), snap.ConflictPaths...),
		true
}

func dedupeSortedStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	compact := out[:0]
	var prev string
	for i, s := range out {
		if i == 0 || s != prev {
			compact = append(compact, s)
			prev = s
		}
	}
	return compact
}

// ClearPromotePathStatus drops cached path status when a job fully merges or aborts.
func (m *Manager) ClearPromotePathStatus(sessionID, jobID string) {
	if m == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	jobID = strings.TrimSpace(jobID)
	if sessionID == "" {
		return
	}
	store, ok := m.promotePathStatus.Load(sessionID)
	if !ok || store == nil || store.byJob == nil {
		return
	}
	if jobID == "" {
		m.promotePathStatus.Delete(sessionID)
		return
	}
	delete(store.byJob, jobID)
	if store.previewJob != nil {
		delete(store.previewJob, jobID)
	}
	if len(store.byJob) == 0 && len(store.previewJob) == 0 {
		m.promotePathStatus.Delete(sessionID)
	}
}

// PromotePathBoardLines returns cached promote path rows for pending write jobs.
func (m *Manager) PromotePathBoardLines(sessionID string) []api.WorkerPromoteJobPathStatus {
	if m == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	store, ok := m.promotePathStatus.Load(sessionID)
	if !ok || store == nil || len(store.byJob) == 0 {
		return nil
	}
	ids := make([]string, 0, len(store.byJob))
	for id := range store.byJob {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]api.WorkerPromoteJobPathStatus, 0, len(ids))
	for _, id := range ids {
		paths := store.byJob[id]
		if len(paths) == 0 {
			continue
		}
		out = append(out, api.WorkerPromoteJobPathStatus{
			WorkerID: id,
			Paths:    append([]api.WorkerPromotePathStatus(nil), paths...),
		})
	}
	return out
}

// NormalizePromotePathStatusRows dedupes and normalizes path_status rows for cache storage.
func NormalizePromotePathStatusRows(statuses []api.WorkerPromotePathStatus) []api.WorkerPromotePathStatus {
	seen := map[string]struct{}{}
	var out []api.WorkerPromotePathStatus
	for _, row := range statuses {
		p := filepath.ToSlash(filepath.Clean(strings.TrimSpace(row.Path)))
		if p == "" || p == "." || sandbox.HasParentTraversal(p) {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		row.Path = p
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
