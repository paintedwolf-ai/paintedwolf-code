package editordoc

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/documentcore"
)

// Pin freezes a bounded read against an exact accepted CRDT state. Later edits
// cannot change either its text or the filesystem precondition it observed.
func (s *Service) Pin(ctx context.Context, projectID, id string, expected int64) (*Document, error) {
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.docLocks.lock(documentLockKey(id))
	defer unlock()
	d, err := s.checked(ctx, id, projectID)
	if err != nil {
		return nil, err
	}
	if expected > 0 && expected != d.Revision {
		return s.pinnedDocument(ctx, d, expected)
	}
	return s.pinCurrent(ctx, d, nil)
}

func (s *Service) pinCurrent(ctx context.Context, d *Document, pin *savePin) (*Document, error) {
	s.replicas.mu.Lock()
	defer s.replicas.mu.Unlock()
	entry, err := s.loadReplica(ctx, d)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "inspect", OmitText: true, Handle: entry.handle, Checkpoint: true})
	if err != nil {
		return nil, err
	}
	if err := s.store.pinSnapshot(ctx, d, &entry.head, snapshot.Checkpoint, pin); err != nil {
		return nil, err
	}
	d.pinnedCheckpoint = snapshot.Checkpoint
	d.CRDTUpdate, d.StateVector, d.Epoch = snapshot.Checkpoint, snapshot.Vector, entry.head.Epoch
	d.PublishedRevision = entry.head.PublishedRevision
	s.projectWorkspacePresentation(ctx, d)
	return s.withParticipants(d), nil
}

func (s *Service) pinnedDocument(ctx context.Context, current *Document, revision int64) (*Document, error) {
	var payload []byte
	err := s.store.db.QueryRowContext(ctx, `SELECT payload FROM editor_document_snapshots WHERE document_id=? AND revision=?`, current.ID, revision).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRevisionConflict
	}
	if err != nil {
		return nil, err
	}
	d, err := decodeSnapshot(payload)
	if err != nil {
		return nil, err
	}
	if d.ID != current.ID || d.ProjectID != current.ProjectID || d.Revision != revision {
		return nil, ErrRevisionConflict
	}
	s.projectWorkspacePresentation(ctx, d)
	return d, nil
}

// PinSave reserves the exact draft for one save, independently of subsequent typing.
func (s *Service) PinSave(ctx context.Context, projectID, id, clientID, operationID string) (*Document, error) {
	if _, err := uuid.Parse(operationID); err != nil {
		return nil, ErrOperationConflict
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return nil, ErrReplicaIdentity
	}
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.docLocks.lock(documentLockKey(id))
	defer unlock()
	d, err := s.checked(ctx, id, projectID)
	if err != nil {
		return nil, err
	}
	revision, err := s.store.savePinRevision(ctx, id, clientID, operationID)
	if err != nil {
		return nil, err
	}
	if revision > 0 {
		return s.pinnedDocument(ctx, d, revision)
	}
	existing, err := s.store.Mutation(ctx, operationID)
	if err == nil {
		if existing.DocumentID != id || existing.ClientID != clientID {
			return nil, ErrOperationConflict
		}
		if err := s.replicaProjection(ctx, d, nil); err != nil {
			return nil, err
		}
		d.SaveRevision = existing.DraftRevision
		return s.withParticipants(d), nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	return s.pinCurrent(ctx, d, &savePin{operationID: operationID, clientID: clientID})
}
