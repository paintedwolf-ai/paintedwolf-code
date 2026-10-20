package invocation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

// BeginProgramWithSource attaches the submitted source's actual retained body in
// the same transaction as the program and source identities. No synthetic child
// execution is created to retain an input which has not executed. On ambiguous
// commit error the caller keeps the spill and reconciles attachment before close.
func (r *SQLRecorder) BeginProgramWithSource(ctx context.Context, in ProgramStart, spill *tooloutput.StreamSpill, redacted bool) (*tooloutput.RetainedStreamBody, error) {
	if r == nil || r.database == nil || spill == nil || !spill.Complete {
		return nil, fmt.Errorf("program source requires complete retained body")
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	if in.SourceExact && (redacted || spill.EmittedSHA256 != in.Version.SourceDigest) {
		return nil, fmt.Errorf("retained source bytes differ from exact program identity")
	}
	return spill.TransferBody(ctx, func(ctx context.Context, reference tooloutput.RetainedStreamReference) error {
		blob, err := json.Marshal(reference.Blob)
		if err != nil {
			return err
		}
		tx, err := r.database.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err = insertProgram(ctx, tx, in); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO invocation_program_outputs(id,program_id,execution_id,stream,reference,retention_claim_id,initiating_claim_id,blob_json,complete,redacted,mappings_json,created_at) VALUES(?,?,NULL,'source',?,?,?,?,1,?,'[]',?)`, uuid.NewString(), in.Version.ProgramID, in.Version.SourceReference, reference.RetentionClaimID, reference.InitiatingClaimID, string(blob), redacted, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		return tx.Commit()
	})
}
