package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceblob"
)

// Checkpoints retain pre-turn state for rewind coverage checks.
const (
	ManifestVersion          = 1
	checkpointAnchorsDirName = "anchors"

	checkpointDirPerm os.FileMode = 0o750

	MaxPaths               = 500
	checkpointMaxFileBytes = sourceblob.MaxRevisionContentBytes
)

// PathOp distinguishes captured content from an absent path.
type PathOp string

const (
	// OpSnapshot references captured file content.
	OpSnapshot PathOp = "snapshot"
	// OpAbsent records a path that did not exist before the turn.
	OpAbsent PathOp = "absent"
)

// PathEntry is one path's pre-turn state.
type PathEntry struct {
	Op     PathOp `json:"op"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
}

// Manifest is the sealed pre-turn state for one prompt anchor.
type Manifest struct {
	Version         int                  `json:"version"`
	SessionID       string               `json:"session_id"`
	AnchorMessageID string               `json:"anchor_message_id"`
	SealedAt        time.Time            `json:"sealed_at"`
	ProjectDir      string               `json:"project_dir"`
	Paths           map[string]PathEntry `json:"paths"`
	// Skipped lists paths that could not be captured.
	Skipped []string `json:"skipped,omitempty"`
	// Truncated marks a manifest that exceeded the path cap.
	Truncated bool `json:"truncated,omitempty"`
	// BlueprintPath detects binding changes during the selected turns.
	BlueprintPath string `json:"blueprint_path,omitempty"`
}

// Store keeps pre-images outside the project tree.
type Store struct {
	stateRoot     string
	root          string
	projectDir    string
	references    store.CheckpointRepository
	objects       *sourceblob.Store
	pendingObject store.CheckpointObject
}

// New binds checkpoints to project and engine state roots.
func New(stateRoot, projectDir string, references store.CheckpointRepository) *Store {
	stateRoot = strings.TrimSpace(stateRoot)
	projectDir = strings.TrimSpace(projectDir)
	if stateRoot == "" || projectDir == "" || references == nil {
		return nil
	}
	projectDir = filepath.Clean(projectDir)
	return &Store{
		stateRoot:  stateRoot,
		root:       enginepaths.ProjectCheckpointDir(enginepaths.SessionCheckpointsRootUnder(stateRoot), projectDir),
		projectDir: projectDir,
		references: references,
		objects:    sourceblob.New(filepath.Join(stateRoot, enginepaths.SourceContentDirName)),
	}
}

// anchorsRoot holds checkpoint directory markers for session cleanup.
func (s *Store) anchorsRoot() string {
	return filepath.Join(s.root, checkpointAnchorsDirName)
}

// safeIDSegment accepts one plain path segment.
func safeIDSegment(id string) (string, bool) {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." {
		return "", false
	}
	if strings.ContainsAny(id, `/\`) || strings.HasPrefix(id, ".") {
		return "", false
	}
	if filepath.Base(id) != id || filepath.IsAbs(id) {
		return "", false
	}
	return id, true
}

func (s *Store) sessionDir(sessionID string) (string, bool) {
	id, ok := safeIDSegment(sessionID)
	if !ok {
		return "", false
	}
	return filepath.Join(s.anchorsRoot(), id), true
}

func (s *Store) anchorDir(sessionID, anchorMessageID string) (string, bool) {
	sessDir, ok := s.sessionDir(sessionID)
	if !ok {
		return "", false
	}
	anchor, ok := safeIDSegment(anchorMessageID)
	if !ok {
		return "", false
	}
	return filepath.Join(sessDir, anchor), true
}

// skipCheckpointPath rejects paths outside the project tree.
func skipCheckpointPath(rel string) bool {
	rel = strings.TrimSpace(filepath.ToSlash(rel))
	if rel == "" || rel == "." {
		return true
	}
	return strings.HasPrefix(rel, "../") || rel == ".." || filepath.IsAbs(rel)
}

// Open starts a copy-on-write checkpoint for one prompt.
func (s *Store) Open(ctx context.Context, sessionID, anchorMessageID string) (*Manifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("checkpoint store not configured")
	}
	dir, ok := s.anchorDir(sessionID, anchorMessageID)
	if !ok {
		return nil, fmt.Errorf("checkpoint open requires plain session and anchor ids")
	}
	sessionID = strings.TrimSpace(sessionID)
	anchorMessageID = strings.TrimSpace(anchorMessageID)
	man := &Manifest{
		Version:         ManifestVersion,
		SessionID:       sessionID,
		AnchorMessageID: anchorMessageID,
		SealedAt:        time.Now().UTC(),
		ProjectDir:      s.projectDir,
		Paths:           make(map[string]PathEntry),
	}
	if err := os.MkdirAll(dir, checkpointDirPerm); err != nil {
		return nil, err
	}
	if err := s.write(ctx, man); err != nil {
		return nil, err
	}
	return man, nil
}

// CapturePreImage records the first pre-turn state for a path.
func (s *Store) CapturePreImage(ctx context.Context, sessionID, anchorMessageID, rel string) error {
	if s == nil {
		return nil
	}
	release := s.objects.AcquireReferenceLease()
	defer release()
	rel = NormalizePath(rel)
	if rel == "" || skipCheckpointPath(rel) {
		return nil
	}
	man, err := s.Load(ctx, sessionID, anchorMessageID)
	if err != nil {
		return err
	}
	if _, done := man.Paths[rel]; done {
		return nil
	}
	for _, skipped := range man.Skipped {
		if skipped == rel {
			return nil
		}
	}
	if len(man.Paths) >= MaxPaths {
		if man.Truncated {
			return nil
		}
		man.Truncated = true
		return s.write(ctx, man)
	}
	entry, skip, err := s.capturePath(rel)
	if err != nil {
		return err
	}
	if skip {
		man.Skipped = append(man.Skipped, rel)
		sort.Strings(man.Skipped)
	} else {
		man.Paths[rel] = entry
	}
	return s.write(ctx, man)
}

// CaptureBlueprintBinding records the pre-turn bound path once per anchor.
func (s *Store) CaptureBlueprintBinding(ctx context.Context, sessionID, anchorMessageID, path string) error {
	if s == nil {
		return nil
	}
	path = NormalizePath(path)
	if path == "" {
		return nil
	}
	man, err := s.Load(ctx, sessionID, anchorMessageID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(man.BlueprintPath) != "" {
		return nil
	}
	man.BlueprintPath = path
	return s.write(ctx, man)
}

// MarkTruncated records incomplete path coverage.
func (s *Store) MarkTruncated(ctx context.Context, sessionID, anchorMessageID string) error {
	if s == nil {
		return nil
	}
	man, err := s.Load(ctx, sessionID, anchorMessageID)
	if err != nil {
		return err
	}
	if man.Truncated {
		return nil
	}
	man.Truncated = true
	return s.write(ctx, man)
}

func (s *Store) write(ctx context.Context, man *Manifest) error {
	raw, err := json.Marshal(man)
	if err != nil {
		return err
	}
	objects := make([]store.CheckpointObject, 0, len(man.Paths))
	for path, entry := range man.Paths {
		if entry.Op != OpSnapshot {
			continue
		}
		object := store.CheckpointObject{Path: path, SHA256: entry.SHA256, Size: entry.Size, Mode: int64(entry.Mode)}
		if s.pendingObject.SHA256 == entry.SHA256 {
			object.Rel = s.pendingObject.Rel
			object.StoredSize = s.pendingObject.StoredSize
			object.GitSHA1 = s.pendingObject.GitSHA1
			object.GitSHA256 = s.pendingObject.GitSHA256
		}
		objects = append(objects, object)
	}
	return s.references.PutCheckpoint(ctx, enginepaths.ProjectKey(s.projectDir), man.SessionID, man.AnchorMessageID, man.SealedAt.UTC().Format(time.RFC3339Nano), string(raw), objects)
}

// capturePath snapshots readable regular files within the byte cap.
func (s *Store) capturePath(rel string) (entry PathEntry, skip bool, err error) {
	abs := filepath.Join(s.projectDir, filepath.FromSlash(rel))
	info, statErr := os.Lstat(abs)
	if os.IsNotExist(statErr) {
		return PathEntry{Op: OpAbsent}, false, nil
	}
	if statErr != nil {
		return PathEntry{}, true, nil //nolint:nilerr // uncapturable path is skipped, not an error
	}
	if info.IsDir() || !info.Mode().IsRegular() {
		return PathEntry{}, true, nil
	}
	if info.Size() > checkpointMaxFileBytes {
		return PathEntry{}, true, nil
	}
	data, readErr := os.ReadFile(abs)
	if readErr != nil {
		return PathEntry{}, true, nil //nolint:nilerr // unreadable path is skipped, not an error
	}
	digest := sourceblob.ContentSHA(data)
	objectRel, storedSize, oids, err := s.objects.Put(digest, data)
	if err != nil {
		return PathEntry{}, false, err
	}
	s.pendingObject = store.CheckpointObject{SHA256: digest, Rel: objectRel, StoredSize: storedSize, GitSHA1: oids.SHA1, GitSHA256: oids.SHA256}
	return PathEntry{
		Op:     OpSnapshot,
		SHA256: digest,
		Size:   info.Size(),
		Mode:   uint32(info.Mode().Perm()),
	}, false, nil
}

// Load reads a sealed manifest.
func (s *Store) Load(ctx context.Context, sessionID, anchorMessageID string) (*Manifest, error) {
	if s == nil {
		return nil, store.ErrCheckpointMissing
	}
	if _, ok := s.anchorDir(sessionID, anchorMessageID); !ok {
		return nil, store.ErrCheckpointMissing
	}
	raw, err := s.references.ReadCheckpoint(ctx, enginepaths.ProjectKey(s.projectDir), sessionID, anchorMessageID)
	if err != nil {
		return nil, err
	}
	var man Manifest
	if err := json.Unmarshal([]byte(raw), &man); err != nil {
		return nil, err
	}
	if man.Version != ManifestVersion {
		return nil, fmt.Errorf("checkpoint manifest version %d unsupported", man.Version)
	}
	man.ProjectDir = s.projectDir
	return &man, nil
}

// DropAnchor removes an anchor and releases its shared content references.
func (s *Store) DropAnchor(ctx context.Context, sessionID, anchorMessageID string) error {
	if s == nil {
		return nil
	}
	anchorMessageID = strings.TrimSpace(anchorMessageID)
	if anchorMessageID == "" {
		return nil
	}
	dir, ok := s.anchorDir(sessionID, anchorMessageID)
	if !ok {
		return nil
	}
	if err := s.references.DropCheckpoint(ctx, sessionID, anchorMessageID); err != nil {
		return err
	}
	return bloblifecycle.RemoveTree(s.stateRoot, dir)
}

// ListSessions returns the session ids that have a checkpoint directory.
func (s *Store) ListSessions() ([]string, error) {
	if s == nil {
		return nil, nil
	}
	entries, err := os.ReadDir(s.anchorsRoot())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// DropSession removes every checkpoint for a session.
func (s *Store) DropSession(ctx context.Context, sessionID string) error {
	if s == nil {
		return nil
	}
	dir, ok := s.sessionDir(sessionID)
	if !ok {
		return nil
	}
	if err := s.references.DropCheckpoint(ctx, sessionID, ""); err != nil {
		return err
	}
	return bloblifecycle.RemoveTree(s.stateRoot, dir)
}

// RemoveProjectCheckpoints deletes all checkpoint data for one source root.
func RemoveProjectCheckpoints(stateRoot, projectDir string) error {
	if strings.TrimSpace(stateRoot) == "" || strings.TrimSpace(projectDir) == "" {
		return nil
	}
	return bloblifecycle.RemoveTree(stateRoot, enginepaths.ProjectCheckpointDir(enginepaths.SessionCheckpointsRootUnder(stateRoot), filepath.Clean(projectDir)))
}

// ReconcileRoots removes checkpoint trees for source roots that are
// no longer attached to any durable project.
func ReconcileRoots(stateRoot string, liveProjectDirs []string) (int, error) {
	if strings.TrimSpace(stateRoot) == "" {
		return 0, fmt.Errorf("checkpoint reconciliation requires state root")
	}
	root := enginepaths.SessionCheckpointsRootUnder(stateRoot)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	live := make(map[string]struct{}, len(liveProjectDirs))
	for _, dir := range liveProjectDirs {
		if strings.TrimSpace(dir) != "" {
			live[enginepaths.ProjectKey(dir)] = struct{}{}
		}
	}
	var removed int
	var errs []error
	for _, entry := range entries {
		if _, ok := live[entry.Name()]; ok {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if err := bloblifecycle.RemoveTree(stateRoot, path); err != nil {
			errs = append(errs, fmt.Errorf("remove orphan checkpoint root %s: %w", path, err))
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

func (s *Store) ProjectDir() string { return s.projectDir }
