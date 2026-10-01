package editordoc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/textfile"
)

type DocumentChange struct {
	SessionID    string
	SessionTitle string
	Turn         int
	Revision     int64
	Epoch        int64
	OperationID  string
	ActorKind    string
	PersonID     string
	ClientID     string
	RevertedBy   string
	CreatedAt    string
	undo         []byte
}

type RevertChange struct {
	HistoryVector     []byte
	SessionID         string
	Turn              int
	ClientID          string
	OperationID       string
	ChangeOperationID string
	Epoch             int64
}

// Changes lists agent and restore changes newest first, older than before
// when it is positive, at most limit of them.
func (s *Service) Changes(ctx context.Context, projectID, id string, before int64, limit int) ([]DocumentChange, error) {
	if _, err := s.checked(ctx, id, projectID); err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT h.revision,h.epoch,h.operation_id,h.actor_kind,COALESCE(h.person_id,''),h.client_id,COALESCE(h.reverted_by,''),h.created_at,
 COALESCE(c.session_id,''),COALESCE(s.title,''),COALESCE(c.turn,0)
 FROM editor_document_changes h
 LEFT JOIN source_text_contributions c ON c.document_id=h.document_id AND c.epoch=h.epoch AND c.operation_id=h.operation_id
 LEFT JOIN sessions s ON s.id=c.session_id
 WHERE h.document_id=? AND h.actor_kind IN ('agent','restore') AND (?=0 OR h.revision<?) ORDER BY h.revision DESC LIMIT ?`, id, before, before, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]DocumentChange, 0)
	for rows.Next() {
		var change DocumentChange
		if err := rows.Scan(&change.Revision, &change.Epoch, &change.OperationID, &change.ActorKind, &change.PersonID, &change.ClientID, &change.RevertedBy, &change.CreatedAt, &change.SessionID, &change.SessionTitle, &change.Turn); err != nil {
			return nil, err
		}
		result = append(result, change)
	}
	return result, rows.Err()
}

// Revert selectively undoes a semantic edit. Later replicas retain their own
// insertions and deletions; the resulting draft uses the ordinary save command.
func (s *Service) Revert(ctx context.Context, projectID, id string, in RevertChange) (*Document, error) {
	if strings.TrimSpace(in.ClientID) == "" {
		return nil, ErrReplicaIdentity
	}
	if _, err := uuid.Parse(in.OperationID); err != nil {
		return nil, ErrOperationConflict
	}
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.docLocks.lock(documentLockKey(id))
	defer unlock()
	d, err := s.checked(ctx, id, projectID)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	inputDigest := "revert:" + hex.EncodeToString(digest[:])
	receipt, err := s.store.replicaReceipt(ctx, id, in.OperationID)
	if err == nil {
		if receipt.InputDigest != inputDigest {
			return nil, ErrOperationConflict
		}
		if err := s.replicaProjection(ctx, d, nil); err != nil {
			return nil, err
		}
		d.CommandHistory = receipt.CommandHistory
		return s.withParticipants(d), nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	var change DocumentChange
	err = s.store.db.QueryRowContext(ctx, `SELECT epoch,undo_bytes,COALESCE(reverted_by,'') FROM editor_document_changes WHERE document_id=? AND operation_id=?`, id, in.ChangeOperationID).
		Scan(&change.Epoch, &change.undo, &change.RevertedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if change.RevertedBy != "" {
		return nil, ErrOperationConflict
	}
	if err := s.prepareRevert(ctx, d, in, &change, inputDigest); err != nil {
		return nil, err
	}
	previous := d.Revision - 1
	if err := s.store.UpdateCAS(ctx, d, previous); err != nil {
		return nil, err
	}
	s.changed(ctx, d, true)
	return s.withParticipants(d), nil
}

func (s *Service) prepareRevert(ctx context.Context, d *Document, in RevertChange, change *DocumentChange, inputDigest string) error {
	actor, err := s.personActor(ctx, actorRestore, in.ClientID)
	if err != nil {
		return err
	}
	s.replicas.mu.Lock()
	defer s.replicas.mu.Unlock()
	entry, err := s.loadReplica(ctx, d)
	if err != nil {
		return err
	}
	if change.Epoch != entry.head.Epoch || in.Epoch != entry.head.Epoch {
		return ErrReplicaEpoch
	}
	client, err := s.hostTextParticipant(ctx, d, actor)
	if err != nil {
		return err
	}
	if err := s.captureCommandHistory(ctx, d, entry, in.OperationID, in.HistoryVector); err != nil {
		return err
	}
	snapshot, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "undo", Handle: entry.handle, Client: client, Undo: change.undo, Checkpoint: true})
	if err != nil {
		s.replicas.evict(ctx, d.ID)
		return err
	}
	if _, err := textfile.EncodeBounded(serializeEOL(snapshot.Text, d.EOL), d.Encoding, textfile.LimitsForRaw(project.SourceWriteMaxBytes)); err != nil {
		s.replicas.evict(ctx, d.ID)
		return err
	}
	if err := s.accountReplicaSnapshot(ctx, entry, &snapshot); err != nil {
		s.replicas.evict(ctx, d.ID)
		return err
	}
	if d.CommandHistory != nil {
		d.CommandHistory.Update = snapshot.Update
	}
	d.Draft = snapshot.Text
	d.Dirty = d.Draft != d.BaseContent || d.EOL != d.BaseEOL || d.MixedEOL != d.BaseMixedEOL
	d.Revision++
	d.UpdatedAt = time.Now().UTC()
	d.Epoch, d.CRDTUpdate, d.StateVector = entry.head.Epoch, snapshot.Update, snapshot.Vector
	d.PublishedRevision = entry.head.PublishedRevision
	d.replicaCommit = &replicaCommit{undoUnits: snapshot.UndoUnits, undoFloor: entry.undoFloor, revision: d.Revision, epoch: d.Epoch, operationID: in.OperationID, actor: actor,
		update: snapshot.Update, vector: snapshot.Vector, checkpoint: snapshot.Checkpoint, undo: snapshot.Undo, reverts: in.ChangeOperationID, inputDigest: inputDigest}
	d.authorship = authorshipContext{sessionID: in.SessionID, turn: in.Turn}
	d.replicaCommit.contribution = textContribution(d, &snapshot, actor, in.OperationID)
	entry.revision, entry.head.Vector, entry.head.CheckpointRevision = d.Revision, snapshot.Vector, d.Revision
	s.replicas.trim(ctx, d.ID, 0)
	return nil
}
