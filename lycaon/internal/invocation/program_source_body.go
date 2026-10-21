package invocation

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

// BeginProgramWithSource attaches the submitted source's actual retained body in
// the same transaction as the program and source identities. No synthetic child
// execution is created to retain an input which has not executed. On ambiguous
// commit error the caller keeps the spill and reconciles attachment before close.
func (r *SQLRecorder) BeginProgramWithSource(ctx context.Context, in toolcontract.ProgramStart, spill *tooloutput.StreamSpill, redacted bool) (*tooloutput.RetainedStreamBody, error) {
	if r == nil || r.database == nil || spill == nil || !spill.Complete {
		return nil, fmt.Errorf("program source requires complete retained body")
	}
	if err := validateProgramStart(in); err != nil {
		return nil, err
	}
	if in.SourceExact && (redacted || spill.EmittedSHA256 != in.Version.SourceDigest) {
		return nil, fmt.Errorf("retained source bytes differ from exact program identity")
	}
	return spill.TransferBody(ctx, func(ctx context.Context, reference tooloutput.RetainedStreamReference) error {
		tx, err := r.database.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err = insertProgram(ctx, tx, in); err != nil {
			return err
		}
		if err = insertProgramOutput(ctx, tx, in.Version.ProgramID, RetainedOutput{Stream: "source", Reference: in.Version.SourceReference, Complete: true, Redacted: redacted}, reference, ""); err != nil {
			return err
		}
		return tx.Commit()
	})
}
