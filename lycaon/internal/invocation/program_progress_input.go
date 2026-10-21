package invocation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

// Progress input belongs to the actual envelope execution, with no fabricated
// syntax node or native command. The progress receipt separately records that
// execution identity immediately before its current owner is invoked.
func (r *SQLRecorder) attachProgressInput(ctx context.Context, identity toolcontract.ProgramProgressIdentity, reference string, spill *tooloutput.StreamSpill, redacted bool) (*tooloutput.RetainedStreamBody, error) {
	if r == nil || r.database == nil || spill == nil || !spill.Complete || reference == "" {
		return nil, fmt.Errorf("progress input requires complete screened retained bytes")
	}
	return spill.TransferBody(ctx, func(ctx context.Context, body tooloutput.RetainedStreamReference) error {
		if body.InitiatingClaimID != identity.ExecutionID {
			return fmt.Errorf("progress input provenance disagrees with actual envelope execution")
		}
		encoded, err := json.Marshal(body.Blob)
		if err != nil {
			return err
		}
		tx, err := r.database.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		result, err := tx.ExecContext(ctx, `INSERT INTO invocation_program_outputs(id,program_id,execution_id,stream,reference,retention_claim_id,initiating_claim_id,blob_json,complete,redacted,mappings_json,created_at) SELECT ?,p.id,NULL,'arguments',?,?,?,?,1,?,'[]',? FROM invocation_programs p JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE p.id=? AND r.session_id=? AND r.status='running' AND p.state='running'`, uuid.NewString(), reference, body.RetentionClaimID, body.InitiatingClaimID, string(encoded), redacted, time.Now().UTC().Format(time.RFC3339Nano), identity.ProgramID, identity.SessionID)
		if err != nil {
			return err
		}
		if err = oneProgramTransition(result); err != nil {
			return err
		}
		return tx.Commit()
	})
}
