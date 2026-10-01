package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/db"
)

var ErrCheckpointMissing = errors.New("session checkpoint missing")

// CheckpointObject joins a rewind path to shared source content.
type CheckpointObject struct {
	Path, SHA256, Rel, GitSHA1, GitSHA256 string
	Size, StoredSize, Mode                int64
}

// CheckpointRepository owns manifest publication and its content references.
type CheckpointRepository interface {
	PutCheckpoint(context.Context, string, string, string, string, string, []CheckpointObject) error
	ReadCheckpoint(context.Context, string, string, string) (string, error)
	ReadCheckpointForRewind(context.Context, string, string) (string, error)
	DropRootCheckpoints(context.Context, string, string) error
	DropCheckpoint(context.Context, string, string) error
}

func (s *SQL) PutCheckpoint(ctx context.Context, root, session, anchor, sealed, manifest string, objects []CheckpointObject) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	pruned, err := q.GetCheckpointPrunedAt(ctx, db.GetCheckpointPrunedAtParams{SessionID: session, AnchorID: anchor})
	if err != nil && !db.IsNoRows(err) {
		return err
	}
	if pruned != "" {
		return fmt.Errorf("checkpoint was pruned")
	}
	count, err := q.PutCheckpointAnchor(ctx, db.PutCheckpointAnchorParams{
		SessionID: session, AnchorID: anchor, RootKey: root, SealedAt: sealed, ManifestJson: manifest,
	})
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("checkpoint session is missing or belongs to another root")
	}
	for _, object := range objects {
		if object.Rel != "" {
			err = q.InsertCheckpointSourceObject(ctx, db.InsertCheckpointSourceObjectParams{
				Sha256: object.SHA256, Size: object.Size, StoredSize: object.StoredSize,
				StorageRelpath: object.Rel, GitOidSha1: object.GitSHA1, GitOidSha256: object.GitSHA256,
			})
			if err != nil {
				return err
			}
		}
		err = q.InsertCheckpointObjectRef(ctx, db.InsertCheckpointObjectRefParams{
			SessionID: session, AnchorID: anchor, Path: object.Path, Sha256: object.SHA256,
			OriginalSize: object.Size, Mode: object.Mode,
		})
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQL) ReadCheckpoint(ctx context.Context, root, session, anchor string) (string, error) {
	row, err := s.queries.GetCheckpointManifest(ctx, db.GetCheckpointManifestParams{
		RootKey: root, SessionID: session, AnchorID: anchor,
	})
	if db.IsNoRows(err) {
		return "", ErrCheckpointMissing
	}
	if err != nil {
		return "", err
	}
	if row.PrunedAt != "" {
		return "", fmt.Errorf("checkpoint content was pruned at %s", row.PrunedAt)
	}
	return row.ManifestJson, nil
}

func (s *SQL) DropCheckpoint(ctx context.Context, session, anchor string) error {
	return s.queries.DeleteCheckpointAnchors(ctx, db.DeleteCheckpointAnchorsParams{SessionID: session, AnchorID: anchor})
}

func (s *Memory) PutCheckpoint(ctx context.Context, root, session, anchor, _, manifest string, _ []CheckpointObject) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checkpoints == nil {
		s.checkpoints = make(map[string]map[string]memoryCheckpoint)
	}
	if s.checkpoints[session] == nil {
		s.checkpoints[session] = make(map[string]memoryCheckpoint)
	}
	if previous, exists := s.checkpoints[session][anchor]; exists && previous.root != root {
		return fmt.Errorf("checkpoint belongs to another source root")
	}
	var projectID string
	if owner := s.sessions[session]; owner != nil {
		projectID = owner.ProjectID
	}
	s.checkpoints[session][anchor] = memoryCheckpoint{root: root, manifest: manifest, projectID: projectID}
	return nil
}

func (s *Memory) ReadCheckpoint(ctx context.Context, root, session, anchor string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	manifest, ok := s.checkpoints[session][anchor]
	if !ok || manifest.root != root {
		return "", ErrCheckpointMissing
	}
	return manifest.manifest, nil
}

func (s *Memory) DropCheckpoint(ctx context.Context, session, anchor string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if anchor == "" {
		delete(s.checkpoints, session)
	} else {
		delete(s.checkpoints[session], anchor)
	}
	return nil
}

type memoryCheckpoint struct{ root, manifest, projectID string }

func (s *SQL) DropRootCheckpoints(ctx context.Context, projectID, root string) error {
	return s.queries.DeleteRootCheckpointAnchors(ctx, db.DeleteRootCheckpointAnchorsParams{ProjectID: projectID, RootKey: root})
}

func (s *Memory) DropRootCheckpoints(ctx context.Context, projectID, root string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for sessionID, anchors := range s.checkpoints {
		for anchorID, checkpoint := range anchors {
			if checkpoint.projectID == projectID && checkpoint.root == root {
				delete(anchors, anchorID)
			}
		}
		if len(anchors) == 0 {
			delete(s.checkpoints, sessionID)
		}
	}
	return nil
}

// ReadCheckpointForRewind reads metadata by its durable session and anchor,
// independently of the project's current primary root or blob retention.
func (s *SQL) ReadCheckpointForRewind(ctx context.Context, session, anchor string) (string, error) {
	raw, err := s.queries.GetCheckpointRewindManifest(ctx, db.GetCheckpointRewindManifestParams{
		SessionID: session, AnchorID: anchor,
	})
	if db.IsNoRows(err) {
		return "", ErrCheckpointMissing
	}
	return raw, err
}
func (s *Memory) ReadCheckpointForRewind(ctx context.Context, session, anchor string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	checkpoint, ok := s.checkpoints[session][anchor]
	if !ok {
		return "", ErrCheckpointMissing
	}
	return checkpoint.manifest, nil
}
