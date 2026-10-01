package editordoc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

const (
	snapshotPayloadLimit = 64 << 20
	snapshotCacheBytes   = 128 << 20
	snapshotCacheEntries = 4096
	// snapshotFormat names the payload shape; a stored payload of another
	// format is refused rather than reinterpreted.
	snapshotFormat = 2
)

// Snapshot encoding is independent of runtime Document fields. Changing it is a
// durable format change, even when the containing SQL table shape is unchanged.
type snapshotPayload struct {
	Format            int       `json:"format"`
	ID                string    `json:"id"`
	ProjectID         string    `json:"project_id"`
	FileID            string    `json:"file_id"`
	RootID            string    `json:"root_id"`
	BranchID          string    `json:"branch_id"`
	Path              string    `json:"path"`
	Draft             string    `json:"draft"`
	BaseContent       string    `json:"base_content"`
	BaseSHA256        string    `json:"base_sha256"`
	Encoding          string    `json:"encoding"`
	EOL               string    `json:"eol"`
	BaseEOL           string    `json:"base_eol"`
	MixedEOL          bool      `json:"mixed_eol"`
	BaseMixedEOL      bool      `json:"base_mixed_eol"`
	Dirty             bool      `json:"dirty"`
	Diverged          bool      `json:"diverged"`
	Absent            bool      `json:"absent"`
	HeldVersion       string    `json:"held_version"`
	Revision          int64     `json:"revision"`
	Epoch             int64     `json:"epoch"`
	PublishedRevision int64     `json:"published_revision"`
	SizeBytes         int64     `json:"size_bytes"`
	Checkpoint        []byte    `json:"checkpoint"`
	Vector            []byte    `json:"vector"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func encodeSnapshot(d *Document, head *ReplicaHead, checkpoint []byte) ([]byte, int, error) {
	state := snapshotPayload{Format: snapshotFormat, ID: d.ID, ProjectID: d.ProjectID, FileID: d.FileID, RootID: d.RootID,
		BranchID: string(d.BranchID), Path: d.Path, Draft: d.Draft, BaseContent: d.BaseContent, BaseSHA256: d.BaseSHA256,
		Encoding: d.Encoding, EOL: d.EOL, BaseEOL: d.BaseEOL, MixedEOL: d.MixedEOL, BaseMixedEOL: d.BaseMixedEOL,
		Dirty: d.Dirty, Diverged: d.Diverged, Absent: d.Absent, HeldVersion: d.HeldAgentVersionID, Revision: d.Revision,
		Epoch: head.Epoch, PublishedRevision: head.PublishedRevision, SizeBytes: d.SizeBytes,
		Checkpoint: checkpoint, Vector: head.Vector, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, 0, err
	}
	if len(raw) > snapshotPayloadLimit {
		return nil, 0, &documentcore.Rejected{Code: "document_history_capacity"}
	}
	payload, err := zstdcodec.Compress(bytes.NewReader(raw))
	return payload, len(raw), err
}

func decodeSnapshot(payload []byte) (*Document, error) {
	raw, err := zstdcodec.DecompressBounded(payload, snapshotPayloadLimit)
	if err != nil {
		return nil, err
	}
	var state snapshotPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("editor snapshot has trailing data")
	}
	if state.Format != snapshotFormat {
		return nil, fmt.Errorf("unsupported editor snapshot format %d", state.Format)
	}
	d := &Document{ID: state.ID, ProjectID: state.ProjectID, FileID: state.FileID, RootID: state.RootID,
		Path: state.Path, Draft: state.Draft, BaseContent: state.BaseContent, BaseSHA256: state.BaseSHA256,
		Encoding: state.Encoding, EOL: state.EOL, BaseEOL: state.BaseEOL, MixedEOL: state.MixedEOL,
		BaseMixedEOL: state.BaseMixedEOL, Dirty: state.Dirty, Diverged: state.Diverged, Absent: state.Absent,
		HeldAgentVersionID: state.HeldVersion, Revision: state.Revision, Epoch: state.Epoch,
		PublishedRevision: state.PublishedRevision, SizeBytes: state.SizeBytes, CRDTUpdate: state.Checkpoint,
		StateVector: state.Vector, CreatedAt: state.CreatedAt, UpdatedAt: state.UpdatedAt,
		pinnedCheckpoint: state.Checkpoint}
	d.BranchID = sourcebranch.ID(state.BranchID)
	return d, nil
}

type savePin struct{ operationID, clientID string }

func (s *Store) pinSnapshot(ctx context.Context, d *Document, head *ReplicaHead, checkpoint []byte, pin *savePin) error {
	payload, logical, err := encodeSnapshot(d, head, checkpoint)
	if err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, err := tx.ExecContext(ctx, `INSERT INTO editor_document_snapshots(document_id,revision,payload,logical_bytes,created_at)
          VALUES(?,?,?,?,?) ON CONFLICT(document_id,revision) DO UPDATE SET created_at=excluded.created_at`, d.ID, d.Revision, payload, logical, now)
		if err != nil {
			return err
		}
		if pin != nil {
			if _, err = tx.ExecContext(ctx, `INSERT INTO editor_save_pins(operation_id,document_id,revision,client_id,created_at) VALUES(?,?,?,?,?)`, pin.operationID, d.ID, d.Revision, pin.clientID, now); err != nil {
				return err
			}
			// Reserving can only shrink the unreserved cache. Its transfer to
			// the save journal performs eviction when these bytes rejoin it.
			return nil
		}
		return trimSnapshots(ctx, tx, d.ID, d.Revision)
	})
}

// Save reservations prevent eviction until editor_mutations owns the bytes.
func trimSnapshots(ctx context.Context, tx *sql.Tx, keep string, revision int64) error {
	// Materialize the small accounting projection before window-function sorts;
	// otherwise SQLite carries and copies snapshot payloads through the sorters.
	_, err := tx.ExecContext(ctx, `WITH candidates AS MATERIALIZED (
      SELECT s.document_id,s.revision,s.created_at,length(s.payload) AS payload_bytes
      FROM editor_document_snapshots s WHERE NOT EXISTS(SELECT 1 FROM editor_save_pins p WHERE p.document_id=s.document_id AND p.revision=s.revision)
    ) DELETE FROM editor_document_snapshots WHERE (document_id,revision) IN (
      SELECT document_id,revision FROM (
        SELECT document_id,revision,
          ROW_NUMBER() OVER(PARTITION BY document_id ORDER BY created_at DESC,revision DESC) AS document_position,
          SUM(payload_bytes) OVER(PARTITION BY document_id ORDER BY created_at DESC,revision DESC) AS document_bytes,
          ROW_NUMBER() OVER(ORDER BY created_at DESC,document_id,revision DESC) AS position,
          SUM(payload_bytes) OVER(ORDER BY created_at DESC,document_id,revision DESC) AS retained_bytes
        FROM candidates
      ) WHERE (document_position>256 OR document_bytes>33554432 OR position>? OR retained_bytes>?) AND NOT(document_id=? AND revision=?)
    )`, snapshotCacheEntries, snapshotCacheBytes, keep, revision)
	return err
}

func (s *Store) savePinRevision(ctx context.Context, id, clientID, operationID string) (int64, error) {
	var document, client string
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT document_id,client_id,revision FROM editor_save_pins WHERE operation_id=?`, operationID).Scan(&document, &client, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if document != id || client != clientID {
		return 0, ErrOperationConflict
	}
	return revision, nil
}
