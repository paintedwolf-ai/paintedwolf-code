package editordoc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type ReplicaHead struct {
	Epoch               int64
	HostClient          uint32
	FilesystemClient    uint32
	PublishedCheckpoint []byte
	Checkpoint          []byte
	CheckpointRevision  int64
	Vector              []byte
	PublishedRevision   int64
}

type ReplicaReceipt struct {
	CommandHistory   *CommandHistory
	InputDigest      string
	AcceptedRevision int64
	Epoch            int64
}

func (s *Store) replicaHead(ctx context.Context, id string) (*ReplicaHead, error) {
	var head ReplicaHead
	err := s.db.QueryRowContext(ctx, `SELECT epoch, host_client, filesystem_client, published_checkpoint, checkpoint,
		checkpoint_revision, vector, published_revision FROM editor_replica_heads WHERE document_id=?`, id).
		Scan(&head.Epoch, &head.HostClient, &head.FilesystemClient, &head.PublishedCheckpoint, &head.Checkpoint, &head.CheckpointRevision, &head.Vector, &head.PublishedRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &head, err
}

func (s *Store) replicaUpdates(ctx context.Context, id string, after int64) ([][]byte, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT update_bytes FROM editor_replica_updates
		WHERE document_id=? AND revision>? ORDER BY revision`, id, after)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var updates [][]byte
	for rows.Next() {
		var update []byte
		if err := rows.Scan(&update); err != nil {
			return nil, err
		}
		updates = append(updates, update)
	}
	return updates, rows.Err()
}

func (s *Store) replicaReceipt(ctx context.Context, id, operationID string) (*ReplicaReceipt, error) {
	var receipt ReplicaReceipt
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT input_digest, accepted_revision, epoch, command_history
		FROM editor_replica_receipts WHERE document_id=? AND operation_id=?`, id, operationID).
		Scan(&receipt.InputDigest, &receipt.AcceptedRevision, &receipt.Epoch, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err == nil && len(raw) > 0 {
		err = json.Unmarshal(raw, &receipt.CommandHistory)
	}
	return &receipt, err
}

func insertReplicaHead(ctx context.Context, tx *sql.Tx, id string, head *ReplicaHead) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO editor_replica_heads
		(document_id, epoch, host_client, filesystem_client, published_checkpoint, checkpoint, checkpoint_revision, vector, published_revision)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, head.Epoch, head.HostClient, head.FilesystemClient, head.PublishedCheckpoint,
		head.Checkpoint, head.CheckpointRevision, head.Vector, head.PublishedRevision)
	return err
}
