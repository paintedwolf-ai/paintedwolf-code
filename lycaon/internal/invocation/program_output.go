package invocation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

// ProgramBlobStore includes receipt references in the existing blob-store floor.
// It preserves other durable users' callback and never substitutes a new claim.
func (r *SQLRecorder) ProgramBlobStore(store blobstore.Store) blobstore.Store {
	previous := store.Retained
	store.Retained = func(id string) (bool, error) {
		if previous != nil {
			retained, err := previous(id)
			if err != nil || retained {
				return retained, err
			}
		}
		if r == nil || r.database == nil {
			return false, fmt.Errorf("invocation recorder is not configured")
		}
		var present bool
		err := r.database.QueryRowContext(context.Background(), `SELECT EXISTS (SELECT 1 FROM invocation_program_outputs WHERE json_extract(blob_json,'$.ID')=?)`, id).Scan(&present)
		return present, err
	}
	return store
}

// AttachProgramOutput durably names the SAME per-reference body claim before
// TransferBody detaches its leases. On ambiguous commit error the wrapper remains
// owned and must be reconciled, never discarded on the assumption of rollback.
func (r *SQLRecorder) AttachProgramOutput(ctx context.Context, programID string, output RetainedOutput, spill *tooloutput.StreamSpill, memory tooloutput.StreamMemoryAdmission) (*tooloutput.RetainedStreamBody, error) {
	if r == nil || r.database == nil || spill == nil || memory == nil || programID == "" || output.ExecutionID == "" || output.Reference == "" {
		return nil, fmt.Errorf("output attachment requires receipt, stream and admission")
	}
	switch output.Stream {
	case "stdout", "stderr", "detail", "source", "arguments":
	default:
		return nil, fmt.Errorf("invalid retained program stream")
	}
	if output.Complete && !spill.Complete {
		return nil, fmt.Errorf("incomplete retained bytes cannot become complete output")
	}
	// This bounds encoding scratch before json.Marshal duplicates candidate metadata.
	limits, err := receiptLimits()
	if err != nil {
		return nil, err
	}
	if int64(len(output.Mappings)) > limits.events {
		return nil, fmt.Errorf("output source mapping event ceiling exceeded")
	}
	estimate := int64(4096)
	for _, mapping := range output.Mappings {
		p := mapping.Source
		if !validDigest(p.SHA256) || p.SourceID == "" || p.Revision == "" || p.StartByte < 0 || p.EndByte < p.StartByte || mapping.SourceStartByte < p.StartByte || mapping.SourceEndByte < mapping.SourceStartByte || mapping.SourceEndByte > p.EndByte || mapping.OutputStartByte < 0 || mapping.OutputEndByte < mapping.OutputStartByte || mapping.OutputEndByte > spill.Blob.Size || mapping.SourceEndByte-mapping.SourceStartByte != mapping.OutputEndByte-mapping.OutputStartByte {
			return nil, fmt.Errorf("invalid exact source mapping")
		}
		estimate += 1024 + 6*int64(len(p.SourceID)+len(p.Revision))
		if estimate > limits.bytes {
			return nil, fmt.Errorf("output mapping byte ceiling exceeded")
		}
	}
	release, err := memory.Reserve(ctx, estimate)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("output metadata admission returned no lease")
	}
	defer release()
	mappings, err := json.Marshal(output.Mappings)
	if err != nil {
		return nil, err
	}
	return spill.TransferBody(ctx, func(ctx context.Context, reference tooloutput.RetainedStreamReference) error {
		if output.RetentionClaimID != "" && output.RetentionClaimID != reference.RetentionClaimID {
			return fmt.Errorf("output actual retention claim mismatch")
		}
		blob, err := json.Marshal(reference.Blob)
		if err != nil {
			return err
		}
		tx, err := r.database.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		_, err = tx.ExecContext(ctx, `INSERT INTO invocation_program_outputs (id,program_id,execution_id,stream,reference,retention_claim_id,initiating_claim_id,blob_json,complete,redacted,mappings_json,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, uuid.NewString(), programID, output.ExecutionID, output.Stream, output.Reference, reference.RetentionClaimID, reference.InitiatingClaimID, string(blob), output.Complete, output.Redacted, string(mappings), time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		return tx.Commit()
	})
}
