package invocation

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

// Expanded input capture and receipt insertion commit together. Neither a body
// without a receipt nor a dynamic receipt pointing at placeholder inspection
// text can be presented as a complete child preparation.
func (r *SQLRecorder) prepareChildBody(ctx context.Context, in ChildStart, spill *tooloutput.StreamSpill, redacted bool) (*tooloutput.RetainedStreamBody, error) {
	if r == nil || r.database == nil || spill == nil || !spill.Complete {
		return nil, fmt.Errorf("child preparation requires complete retained expanded inputs")
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if spill.InitiatingClaimID() != in.Invocation.Identity.ExecutionID || (!redacted && spill.EmittedSHA256 != in.ExpandedInputs.ArgumentsDigest()) {
		return nil, fmt.Errorf("expanded inspection body disagrees with its actual input identity")
	}
	return spill.TransferBody(ctx, func(ctx context.Context, reference tooloutput.RetainedStreamReference) error {
		tx, err := r.database.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err = insertProgramChild(ctx, tx, in); err != nil {
			return err
		}
		output := RetainedOutput{ExecutionID: in.Invocation.Identity.ExecutionID, Stream: "arguments", Reference: in.ArgumentsReference, Complete: true, Redacted: redacted}
		if err = insertProgramOutput(ctx, tx, in.Invocation.Identity.ProgramID, output, reference, ""); err != nil {
			return err
		}
		return tx.Commit()
	})
}
