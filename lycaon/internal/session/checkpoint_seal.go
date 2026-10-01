package session

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/pkg/api"
)

// errCheckpointRootUnset identifies missing host checkpoint storage.
var errCheckpointRootUnset = errors.New("checkpoint state root not configured")

// sessionCheckpointStore binds checkpoints to the active primary root.
func (m *Manager) sessionCheckpointStore(ctx context.Context, sessionID string) (*sessioncheckpoint.Store, error) {
	if m == nil || m.store == nil {
		return nil, nil
	}
	sess, err := m.store.Get(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, nil
	}
	root := m.sessionPrimaryRoot(ctx, sess)
	if strings.TrimSpace(root) == "" {
		root = m.dataDir
	}
	store := sessioncheckpoint.New(m.dataDir, root, m.store)
	if store == nil {
		return nil, errCheckpointRootUnset
	}
	return store, nil
}

// sessionPrimaryRoot returns the on-disk primary workspace root for a session.
func (m *Manager) sessionPrimaryRoot(ctx context.Context, sess *api.Session) string {
	if sess == nil {
		return ""
	}
	if m != nil {
		if root, err := m.sessionActiveRootPath(ctx, sess); err == nil && root != "" {
			return root
		}
	}
	return strings.TrimSpace(sess.WorkspacePath)
}

// sealPromptCheckpoint opens a rewind anchor before storing the user row.
func (m *Manager) sealPromptCheckpoint(ctx context.Context, sessionID, anchorMessageID string) error {
	if m == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	anchorMessageID = strings.TrimSpace(anchorMessageID)
	if sessionID == "" || anchorMessageID == "" {
		return nil
	}
	store, err := m.sessionCheckpointStore(ctx, sessionID)
	if err != nil {
		if !errors.Is(err, errCheckpointRootUnset) {
			return err
		}
		slog.ErrorContext(ctx, "session turn runs without a rewind checkpoint",
			"component", "session", "session_id", sessionID, "error", err)
		return nil
	}
	if store == nil {
		return nil
	}
	rootID := RootSessionID(ctx, m.store, sessionID)
	return m.checkpointCapture.Open(ctx, store, rootID, anchorMessageID)
}

// RecordPrimaryMutation captures a path's pre-turn state once per anchor.
func (m *Manager) RecordPrimaryMutation(ctx context.Context, sessionID, relPath string) {
	if m == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	relPath = sessioncheckpoint.NormalizePath(relPath)
	if sessionID == "" || relPath == "" {
		return
	}
	rootID := RootSessionID(ctx, m.store, sessionID)
	store, err := m.sessionCheckpointStore(ctx, sessionID)
	if err != nil || store == nil {
		return
	}
	m.checkpointCapture.RecordPath(ctx, store, rootID, relPath)
}

// RecordBlueprintBinding stores the pre-turn bound blueprint path on the open checkpoint.
func (m *Manager) RecordBlueprintBinding(ctx context.Context, sessionID, path string) {
	if m == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	path = sessioncheckpoint.NormalizePath(path)
	if sessionID == "" || path == "" {
		return
	}
	rootID := RootSessionID(ctx, m.store, sessionID)
	store, err := m.sessionCheckpointStore(ctx, sessionID)
	if err != nil || store == nil {
		return
	}
	m.checkpointCapture.RecordBlueprint(ctx, store, rootID, path)
}

// RecordPromotedPrimaryPaths captures primary paths before overlay promotion.
func (m *Manager) RecordPromotedPrimaryPaths(ctx context.Context, sessionID string, paths []string) {
	for _, p := range paths {
		m.RecordPrimaryMutation(ctx, sessionID, p)
	}
}

// RemoveOrphanCheckpoints deletes checkpoint trees without live sessions.
func (m *Manager) RemoveOrphanCheckpoints(ctx context.Context, projectDir string) int {
	if m == nil || m.store == nil {
		return 0
	}
	store := sessioncheckpoint.New(m.dataDir, projectDir, m.store)
	if store == nil {
		return 0
	}
	stored, err := store.ListSessions()
	if err != nil || len(stored) == 0 {
		return 0
	}
	live, err := m.store.ExistingSessionIDs(ctx, stored)
	if err != nil {
		return 0
	}
	removed := 0
	for _, sessionID := range stored {
		if _, ok := live[sessionID]; ok {
			continue
		}
		if err := store.DropSession(ctx, sessionID); err == nil {
			removed++
		}
	}
	return removed
}
