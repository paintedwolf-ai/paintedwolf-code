package store

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	spillReconcileBatchSize  = 128
	maxIdleSpillRepairStates = 32
	spillReclaimGrace        = time.Minute
)

type messageSpillRef struct {
	ProjectID string
	RelPath   string
}

type spillReconcileState struct {
	mu       sync.Mutex
	dir      *os.File
	deferred bool
}

type spillRepairRegistry struct {
	mu      sync.Mutex
	entries map[string]*spillRepairEntry
}

type spillRepairEntry struct {
	state spillReconcileState
	users int
	drop  bool
}

func (r *spillRepairRegistry) acquire(key string) (*spillReconcileState, func()) {
	r.mu.Lock()
	if r.entries == nil {
		r.entries = make(map[string]*spillRepairEntry)
	}
	entry := r.entries[key]
	if entry == nil {
		entry = &spillRepairEntry{}
		r.entries[key] = entry
	}
	entry.users++
	r.mu.Unlock()
	entry.state.mu.Lock()
	return &entry.state, func() {
		entry.state.mu.Unlock()
		r.release(key, entry)
	}
}

func (r *spillRepairRegistry) release(key string, entry *spillRepairEntry) {
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

func (r *spillRepairRegistry) delete(key string) {
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

func (r *spillRepairRegistry) trim() {
	idle := 0
	for _, entry := range r.entries {
		if entry.users == 0 && entry.state.dir != nil {
			idle++
		}
	}
	for key, entry := range r.entries {
		if idle <= maxIdleSpillRepairStates {
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

func writeMessageSpillRefs(ctx context.Context, q *db.Queries, projectID string, msg api.Message) error {
	for _, relPath := range spillPathsForMessage(msg) {
		if err := q.InsertMessageSpillRef(ctx, db.InsertMessageSpillRefParams{
			MessageID: msg.ID,
			ProjectID: projectID,
			RelPath:   relPath,
		}); err != nil {
			return err
		}
	}
	return nil
}

func replaceMessageSpillRefs(ctx context.Context, q *db.Queries, projectID string, msg api.Message) ([]messageSpillRef, error) {
	rows, err := q.ListMessageSpillRefs(ctx, msg.ID)
	if err != nil {
		return nil, err
	}
	oldRefs := spillRefsFromMessageRows(rows)
	if err := q.DeleteMessageSpillRefs(ctx, msg.ID); err != nil {
		return nil, err
	}
	if err := writeMessageSpillRefs(ctx, q, projectID, msg); err != nil {
		return nil, err
	}
	return oldRefs, nil
}

func writeCompactionSpillRefs(ctx context.Context, q *db.Queries, sessionID, projectID string, messages []api.Message) error {
	seen := make(map[string]struct{})
	for _, msg := range messages {
		for _, relPath := range spillPathsForMessage(msg) {
			if _, ok := seen[relPath]; ok {
				continue
			}
			seen[relPath] = struct{}{}
			if err := q.InsertCompactionSpillRef(ctx, db.InsertCompactionSpillRefParams{
				SessionID: sessionID,
				ProjectID: projectID,
				RelPath:   relPath,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func spillPathsForMessage(msg api.Message) []string {
	if msg.Role != api.MessageRoleTool {
		return nil
	}
	seen := make(map[string]struct{})
	var paths []string
	appendPaths := func(content string) {
		for _, relPath := range tooloutput.SpillPaths(content) {
			if _, ok := seen[relPath]; ok {
				continue
			}
			seen[relPath] = struct{}{}
			paths = append(paths, relPath)
		}
	}
	appendPaths(msg.Content)
	if msg.ToolResult != nil {
		appendPaths(msg.ToolResult.Content)
	}
	return paths
}

func (s *SQL) reclaimUnreferencedSpills(ctx context.Context, refs []messageSpillRef) {
	lifecycle := bloblifecycle.ForDevice(s.dataDir)
	if !lifecycle.TryLock() {
		for _, ref := range refs {
			s.spillReconciled.Delete(ref.ProjectID)
		}
		return
	}
	defer lifecycle.Unlock()
	cutoff := time.Now().Add(-spillReclaimGrace)
	for _, ref := range refs {
		count, err := s.queries.CountMessageSpillRefsByPath(ctx, db.CountMessageSpillRefsByPathParams{
			TargetProjectID: ref.ProjectID,
			TargetRelPath:   ref.RelPath,
		})
		if err != nil {
			slog.WarnContext(ctx, "check tool output reference", "project_id", ref.ProjectID, "path", ref.RelPath, "error", err)
			continue
		}
		if count != 0 {
			continue
		}
		hostDataDir := project.HostDataDir(s.dataDir, ref.ProjectID)
		removed, err := tooloutput.RemoveSpillBefore(hostDataDir, ref.RelPath, cutoff)
		if err != nil {
			slog.WarnContext(ctx, "reclaim tool output", "project_id", ref.ProjectID, "path", ref.RelPath, "error", err)
		} else if !removed {
			s.spillReconciled.Delete(ref.ProjectID)
		}
	}
}

func (s *SQL) reconcileProjectSpills(ctx context.Context, projectID string) {
	projectID = strings.TrimSpace(projectID)
	if s == nil || projectID == "" || strings.TrimSpace(s.dataDir) == "" {
		return
	}
	if _, ok := s.spillReconciled.Load(projectID); ok {
		return
	}
	state, release := s.spillReconcileStates.acquire(projectID)
	defer release()
	if _, ok := s.spillReconciled.Load(projectID); ok {
		return
	}
	done, err := s.pruneProjectSpillBatch(ctx, projectID, state)
	if err != nil {
		state.close()
		s.spillReconcileStates.delete(projectID)
		slog.WarnContext(ctx, "reconcile retained tool output", "project_id", projectID, "error", err)
		return
	}
	if done {
		state.close()
		s.spillReconciled.Store(projectID, struct{}{})
		s.spillReconcileStates.delete(projectID)
	}
}

func (s *SQL) pruneProjectSpillBatch(ctx context.Context, projectID string, state *spillReconcileState) (bool, error) {
	lifecycle := bloblifecycle.ForDevice(s.dataDir)
	if !lifecycle.TryLock() {
		return false, nil
	}
	defer lifecycle.Unlock()
	hostDataDir := project.HostDataDir(s.dataDir, projectID)
	spillDir := filepath.Join(hostDataDir, tooloutput.ToolOutputSpillDir)
	if state.dir == nil {
		dir, err := os.Open(spillDir)
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		state.dir = dir
	}
	entries, readErr := state.dir.ReadDir(spillReconcileBatchSize)
	done := errors.Is(readErr, os.ErrNotExist) || errors.Is(readErr, io.EOF)
	if readErr != nil && !done {
		return false, readErr
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		relPath := tooloutput.ToolOutputSpillDir + "/" + entry.Name()
		if !tooloutput.IsContentAddressedToolOutputSpillRel(relPath) {
			continue
		}
		count, err := s.queries.CountMessageSpillRefsByPath(ctx, db.CountMessageSpillRefsByPathParams{
			TargetProjectID: projectID,
			TargetRelPath:   relPath,
		})
		if err != nil {
			return false, err
		}
		if count != 0 {
			continue
		}
		removed, err := tooloutput.RemoveSpillBefore(hostDataDir, relPath, time.Now().Add(-spillReclaimGrace))
		if err != nil {
			return false, err
		}
		state.deferred = state.deferred || !removed
	}
	if done && state.deferred {
		state.close()
		return false, nil
	}
	return done, nil
}

func (s *spillReconcileState) close() {
	if s.dir != nil {
		_ = s.dir.Close()
		s.dir = nil
	}
	s.deferred = false
}

func spillRefsFromTreeRows(rows []db.ListSessionTreeSpillRefsRow) []messageSpillRef {
	out := make([]messageSpillRef, 0, len(rows))
	for _, row := range rows {
		out = appendSpillRef(out, row.ProjectID, row.RelPath)
	}
	return out
}

func spillRefsFromMessageRows(rows []db.ListMessageSpillRefsRow) []messageSpillRef {
	out := make([]messageSpillRef, 0, len(rows))
	for _, row := range rows {
		out = appendSpillRef(out, row.ProjectID, row.RelPath)
	}
	return out
}

func spillRefsFromOrdRows(rows []db.ListSessionSpillRefsFromOrdRow) []messageSpillRef {
	out := make([]messageSpillRef, 0, len(rows))
	for _, row := range rows {
		out = appendSpillRef(out, row.ProjectID, row.RelPath)
	}
	return out
}

func spillRefsFromCompactionRows(rows []db.ListSessionCompactionSpillRefsRow) []messageSpillRef {
	out := make([]messageSpillRef, 0, len(rows))
	for _, row := range rows {
		out = appendSpillRef(out, row.ProjectID, row.RelPath)
	}
	return out
}

func appendSpillRef(refs []messageSpillRef, projectID, relPath string) []messageSpillRef {
	projectID = strings.TrimSpace(projectID)
	relPath = strings.TrimSpace(relPath)
	if projectID == "" || relPath == "" {
		return refs
	}
	return append(refs, messageSpillRef{ProjectID: projectID, RelPath: relPath})
}
