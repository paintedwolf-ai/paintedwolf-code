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
)

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

func reconcileProjectSpills(ctx context.Context, s *sqlCore, projectID string) {
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
	done, err := pruneProjectSpillBatch(ctx, s, projectID, state)
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

func pruneProjectSpillBatch(ctx context.Context, s *sqlCore, projectID string, state *spillReconcileState) (bool, error) {
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
