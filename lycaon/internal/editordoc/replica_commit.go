package editordoc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/textfile"
)

var (
	ErrReplicaEpoch    = errors.New("document replica epoch does not match the accepted document")
	ErrReplicaIdentity = errors.New("document replica does not belong to this client")
)

type ReplicaSubmission struct {
	SessionID   string
	Turn        int
	ClientID    string
	ReplicaID   uint32
	OperationID string
	Epoch       int64
	Update      []byte
	Vector      []byte
}

type ReplicaResult struct {
	Document         *Document
	AcceptedRevision int64
}

type replicaCommit struct {
	contribution *sourceledger.TextContribution
	undoUnits    uint64
	undoFloor    int64
	undo         []byte
	reverts      string
	revision     int64
	epoch        int64
	operationID  string
	actor        textActor
	update       []byte
	vector       []byte
	checkpoint   []byte
	inputDigest  string
}

func (s *Service) prepareTextTransition(ctx context.Context, next *Document, previous int64, actor textActor, operationID string) error {
	before, err := s.store.Get(ctx, next.ID)
	if err != nil {
		return err
	}
	if before.Revision != previous {
		return ErrRevisionConflict
	}
	s.replicas.mu.Lock()
	defer s.replicas.mu.Unlock()
	entry, err := s.loadReplica(ctx, before)
	if err != nil {
		return err
	}
	edits := next.plannedEdits
	if edits == nil {
		edits = documentcore.TextEdits(before.Draft, next.Draft)
	}
	if operationID == "" {
		operationID = uuid.NewString()
	}
	client, err := s.hostTextParticipant(ctx, next, actor)
	if err != nil {
		return err
	}
	if err := s.captureCommandHistory(ctx, next, entry, operationID, next.historyVector); err != nil {
		return err
	}
	snapshot, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "edit", Handle: entry.handle,
		Client: client, Edits: edits, CaptureUndo: len(edits) > 0 && actor.kind != actorExternal, TrackChanges: len(edits) > 0, Checkpoint: next.Revision-entry.head.CheckpointRevision >= 64})
	if err != nil {
		s.replicas.evict(ctx, next.ID)
		return err
	}
	if next.CommandHistory != nil {
		next.CommandHistory.Update = snapshot.Update
	}
	if err := s.accountReplicaSnapshot(ctx, entry, &snapshot); err != nil {
		s.replicas.evict(ctx, next.ID)
		return err
	}
	next.replicaCommit = &replicaCommit{undoUnits: snapshot.UndoUnits, undoFloor: entry.undoFloor, undo: snapshot.Undo, revision: next.Revision, epoch: entry.head.Epoch, operationID: operationID,
		actor: actor, update: snapshot.Update, vector: snapshot.Vector, checkpoint: snapshot.Checkpoint}
	next.Epoch, next.CRDTUpdate, next.StateVector = entry.head.Epoch, snapshot.Update, snapshot.Vector
	next.replicaCommit.contribution = textContribution(next, &snapshot, actor, operationID)
	next.PublishedRevision = entry.head.PublishedRevision
	entry.revision, entry.head.Vector = next.Revision, snapshot.Vector
	s.replicas.trim(ctx, next.ID, 0)
	if len(snapshot.Checkpoint) > 0 {
		entry.head.CheckpointRevision = next.Revision
	}
	return nil
}

func (s *Service) SubmitReplica(ctx context.Context, projectID, documentID string, in ReplicaSubmission) (*ReplicaResult, error) {
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.docLocks.lock(documentLockKey(documentID))
	defer unlock()
	d, err := s.checked(ctx, documentID, projectID)
	if err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(in.OperationID); err != nil || in.ClientID == "" || in.ReplicaID == 0 || len(in.Update) == 0 {
		return nil, ErrReplicaIdentity
	}
	digestInput := in
	digestInput.Vector = nil
	input, err := json.Marshal(digestInput)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(input)
	inputDigest := hex.EncodeToString(digest[:])
	receipt, err := s.store.replicaReceipt(ctx, d.ID, in.OperationID)
	if err == nil {
		if receipt.InputDigest != inputDigest {
			return nil, ErrOperationConflict
		}
		head, err := s.store.replicaHead(ctx, d.ID)
		if err != nil {
			return nil, err
		}
		vector := in.Vector
		if receipt.Epoch != head.Epoch {
			vector = nil
		}
		if err := s.replicaProjection(ctx, d, vector); err != nil {
			return nil, err
		}
		return &ReplicaResult{Document: d, AcceptedRevision: receipt.AcceptedRevision}, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err := s.acceptReplica(ctx, d, in, inputDigest); err != nil {
		s.recoverHistoryCapacity(ctx, d.ID, err)
		return nil, err
	}
	s.changed(ctx, s.withParticipants(d), true)
	if err := s.replicaProjection(ctx, d, in.Vector); err != nil {
		return nil, err
	}
	return &ReplicaResult{Document: d, AcceptedRevision: d.Revision}, nil
}

func (s *Service) acceptReplica(ctx context.Context, d *Document, in ReplicaSubmission, inputDigest string) error {
	actor, err := s.personActor(ctx, actorUser, in.ClientID)
	if err != nil {
		return err
	}
	var registered uint32
	if err := s.store.db.QueryRowContext(ctx, `SELECT replica_id FROM editor_replicas WHERE document_id=? AND role='person' AND client_id=? AND person_id=? AND replica_id=?`, d.ID, in.ClientID, actor.personID, in.ReplicaID).Scan(&registered); err != nil {
		return ErrReplicaIdentity
	}
	if registered != in.ReplicaID {
		return ErrReplicaIdentity
	}
	s.replicas.mu.Lock()
	defer s.replicas.mu.Unlock()
	entry, err := s.loadReplica(ctx, d)
	if err != nil {
		return err
	}
	if entry.head.Epoch != in.Epoch {
		return ErrReplicaEpoch
	}
	previous := d.Revision
	snapshot, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "apply", Handle: entry.handle,
		Client: in.ReplicaID, Update: in.Update, TrackChanges: true, Checkpoint: d.Revision+1-entry.head.CheckpointRevision >= 64})
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
	d.Draft = snapshot.Text
	d.Dirty = d.Draft != d.BaseContent || d.EOL != d.BaseEOL || d.MixedEOL != d.BaseMixedEOL
	d.Revision++
	d.UpdatedAt = time.Now().UTC()
	d.Epoch, d.CRDTUpdate, d.StateVector = entry.head.Epoch, in.Update, snapshot.Vector
	d.replicaCommit = &replicaCommit{undoFloor: entry.undoFloor, revision: d.Revision, epoch: entry.head.Epoch, operationID: in.OperationID, actor: actor,
		update: in.Update, vector: snapshot.Vector, checkpoint: snapshot.Checkpoint, inputDigest: inputDigest}
	d.authorship = authorshipContext{sessionID: in.SessionID, turn: in.Turn}
	d.replicaCommit.contribution = textContribution(d, &snapshot, actor, in.OperationID)
	if err := s.store.UpdateCAS(ctx, d, previous); err != nil {
		s.replicas.evict(ctx, d.ID)
		return err
	}
	entry.revision, entry.head.Vector = d.Revision, snapshot.Vector
	s.replicas.trim(ctx, d.ID, 0)
	if len(snapshot.Checkpoint) > 0 {
		entry.head.CheckpointRevision = d.Revision
	}
	return nil
}

func commitReplica(ctx context.Context, tx *sql.Tx, d *Document) error {
	c := d.replicaCommit
	if c == nil || c.revision != d.Revision {
		return nil
	}
	ts := d.UpdatedAt.Format(time.RFC3339Nano)
	if c.contribution != nil {
		if err := sourceledger.RecordTextContributionTx(ctx, tx, *c.contribution); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO editor_replica_updates
		(document_id,revision,epoch,operation_id,actor_kind,person_id,client_id,update_bytes,vector,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, d.ID, d.Revision, c.epoch, c.operationID, c.actor.kind, nullableString(c.actor.personID), c.actor.clientID, c.update, c.vector, ts)
	if err != nil {
		return fmt.Errorf("record document update: %w", err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE editor_replica_heads SET epoch=?,vector=? WHERE document_id=?`, c.epoch, c.vector, d.ID)
	if err != nil {
		return err
	}
	if len(c.checkpoint) > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE editor_replica_heads SET checkpoint=?,checkpoint_revision=? WHERE document_id=?`, c.checkpoint, d.Revision, d.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM editor_replica_updates WHERE document_id=? AND revision<=?`, d.ID, d.Revision); err != nil {
			return err
		}
	}
	if len(c.undo) > 0 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO editor_document_changes
   (document_id,revision,epoch,operation_id,actor_kind,person_id,client_id,undo_bytes,undo_units,created_at)
   VALUES(?,?,?,?,?,?,?,?,?,?)`, d.ID, d.Revision, c.epoch, c.operationID, c.actor.kind, nullableString(c.actor.personID), c.actor.clientID, c.undo, c.undoUnits, ts); err != nil {
			return err
		}
	}
	if c.undoFloor > 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM editor_document_changes WHERE document_id=? AND revision<=?`, d.ID, c.undoFloor); err != nil {
			return err
		}
	}
	if c.reverts != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE editor_document_changes SET reverted_by=? WHERE document_id=? AND operation_id=?`, c.operationID, d.ID, c.reverts); err != nil {
			return err
		}
	}

	if c.inputDigest != "" {
		history, historyErr := commandHistoryJSON(d.CommandHistory)
		if historyErr != nil {
			return historyErr
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO editor_replica_receipts (document_id,operation_id,input_digest,accepted_revision,epoch,created_at,command_history)
			VALUES(?,?,?,?,?,?,?)`, d.ID, c.operationID, c.inputDigest, d.Revision, c.epoch, ts, history)
	}
	return err
}
