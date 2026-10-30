package invocation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

const maxInlineProgramInput = 16 << 10

func (r *ProgramOutputRetention) prepareChildInline(ctx context.Context, in toolcontract.ProgramChildStart, body []byte, redacted bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ready || r.release == nil {
		return fmt.Errorf("program output recovery is incomplete")
	}
	return r.recorder.prepareChildInline(ctx, in, body, redacted)
}

// prepareChildInline commits screened input bytes and the prepared child in
// one database transaction. The reference remains scoped to that execution.
func (r *SQLRecorder) prepareChildInline(ctx context.Context, in toolcontract.ProgramChildStart, body []byte, redacted bool) error {
	if r == nil || r.database == nil || len(body) > maxInlineProgramInput {
		return fmt.Errorf("inline child input requires recorder and bounded body")
	}
	if err := validateChildStart(in); err != nil {
		return err
	}
	if !redacted {
		digest := sha256.Sum256(body)
		if hex.EncodeToString(digest[:]) != in.ExpandedInputs.ArgumentsDigest() {
			return fmt.Errorf("expanded inspection body disagrees with its actual input identity")
		}
	}
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertProgramChild(ctx, tx, in); err != nil {
		return err
	}
	if err := insertInlineProgramInput(ctx, tx, in.Invocation.Identity.ProgramID, in.Invocation.Identity.ExecutionID, in.Invocation.Identity.ExecutionID, in.ArgumentsReference, body, redacted); err != nil {
		return err
	}
	return tx.Commit()
}

func insertInlineProgramInput(ctx context.Context, tx *sql.Tx, program, execution, initiating, reference string, body []byte, redacted bool) error {
	claim := "inline:" + uuid.NewString()
	digest := sha256.Sum256(body)
	encodedDigest := hex.EncodeToString(digest[:])
	if err := chargeProgramTrace(ctx, tx, program, 1, int64(len(program)+len(initiating)+len(reference)+len(claim)+len(body)+len(encodedDigest)), traceChargeNonTerminal); err != nil {
		return err
	}
	var child any
	if execution != "" {
		child = execution
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO invocation_program_outputs(id,program_id,execution_id,stream,reference,retention_claim_id,initiating_claim_id,blob_json,inline_digest,inline_body,complete,redacted,mappings_json,created_at) VALUES(?,?,?,'arguments',?,?,?,'{}',?,?,1,?,'[]',?)`, uuid.NewString(), program, child, reference, claim, initiating, encodedDigest, body, redacted, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

type inlineInputReader struct {
	*bytes.Reader
	release func()
}

func (r *inlineInputReader) Close() error {
	if r.release != nil {
		r.release()
		r.release = nil
	}
	return nil
}

var _ io.ReadCloser = (*inlineInputReader)(nil)
