package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

// ProgramBlobStore includes receipt references in the existing blob-store floor.
// It preserves other durable users' callback and never substitutes a new claim.
func (r *SQLRecorder) ProgramBlobStore(ctx context.Context, store blobstore.Store) blobstore.Store {
	// Physical body cleanup must still check durable references after cancellation.
	ctx = context.WithoutCancel(ctx)
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
		// This store callback guards collection after individual operations end.
		err := r.database.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM invocation_program_outputs WHERE blob_id=?)`, id).Scan(&present)
		return present, err
	}
	return store
}

// AttachProgramOutput durably names the SAME per-reference body claim before
// TransferBody detaches its leases. On ambiguous commit error the wrapper remains
// owned and must be reconciled, never discarded on the assumption of rollback.
// Returned mappings are the admitted durable attribution; callers must use this
// decision for immediate source credit as well as subsequent receipt reads.
func (r *SQLRecorder) AttachProgramOutput(ctx context.Context, programID string, output RetainedOutput, spill *tooloutput.StreamSpill, memory tooloutput.StreamMemoryAdmission) (*tooloutput.RetainedStreamBody, []toolcontract.StreamSourceMapping, error) {
	if r == nil || r.database == nil || spill == nil || memory == nil || programID == "" || output.Reference == "" {
		return nil, nil, fmt.Errorf("output attachment requires receipt, stream and admission")
	}
	switch output.Stream {
	case "stdout", "stderr":
	case "detail":
		if output.ExecutionID == "" && output.Reference != deliveryWindowReference(programID, "stdout") && output.Reference != deliveryWindowReference(programID, "stderr") {
			return nil, nil, fmt.Errorf("program detail requires a typed delivery descriptor")
		}
	case "source", "arguments":
		if output.ExecutionID == "" {
			return nil, nil, fmt.Errorf("output attachment requires receipt, stream and admission")
		}
	default:
		return nil, nil, fmt.Errorf("invalid retained program stream")
	}
	if output.Complete && !spill.Complete {
		return nil, nil, fmt.Errorf("incomplete retained bytes cannot become complete output")
	}
	// This bounds encoding scratch before json.Marshal duplicates candidate metadata.
	limits, err := receiptLimits()
	if err != nil {
		return nil, nil, err
	}
	omitMappings := int64(len(output.Mappings)) > limits.events
	estimate := int64(4096)
	for _, mapping := range output.Mappings {
		p := mapping.Source
		if !validDigest(p.SHA256) || p.SourceID == "" || p.Revision == "" || p.StartByte < 0 || p.EndByte < p.StartByte || mapping.SourceStartByte < p.StartByte || mapping.SourceEndByte < mapping.SourceStartByte || mapping.SourceEndByte > p.EndByte || mapping.OutputStartByte < 0 || mapping.OutputEndByte < mapping.OutputStartByte || mapping.OutputEndByte > spill.Blob.Size || mapping.SourceEndByte-mapping.SourceStartByte != mapping.OutputEndByte-mapping.OutputStartByte {
			return nil, nil, fmt.Errorf("invalid exact source mapping")
		}
		stringsBytes := int64(len(p.SourceID)) + int64(len(p.Revision)) + int64(len(p.Representation)) + int64(len(p.SHA256))
		if stringsBytes > max(0, limits.bytes-estimate-1024)/6 {
			omitMappings = true
		} else {
			estimate += 1024 + 6*stringsBytes
		}
	}
	if omitMappings {
		output.Mappings = nil
		estimate = 4096
	}
	release, err := memory.Reserve(ctx, estimate)
	if err != nil && len(output.Mappings) > 0 && ctx.Err() == nil {
		// Attribution is optional; the retained bytes still require admission.
		output.Mappings = nil
		release, err = memory.Reserve(ctx, 4096)
	}
	if err != nil {
		return nil, nil, err
	}
	if release == nil {
		return nil, nil, fmt.Errorf("output metadata admission returned no lease")
	}
	defer release()
	mappings, err := json.Marshal(output.Mappings)
	if err != nil {
		return nil, nil, err
	}
	body, err := spill.TransferBody(ctx, func(ctx context.Context, reference tooloutput.RetainedStreamReference) error {
		if output.RetentionClaimID != "" && output.RetentionClaimID != reference.RetentionClaimID {
			return fmt.Errorf("output actual retention claim mismatch")
		}
		tx, err := r.database.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err := insertProgramOutput(ctx, tx, programID, output, reference, string(mappings), spill.Preview); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return nil, nil, err
	}
	return body, output.Mappings, nil
}

// maxOutputPreviewBytes bounds the screened leading bytes a receipt page carries.
const maxOutputPreviewBytes = 2048

// outputPreview keeps whole leading runes of the spill's screened preview;
// interior invalid bytes become U+FFFD so the wire stays UTF-8.
func outputPreview(preview []byte, size int64) (string, bool) {
	cut := preview[:min(len(preview), maxOutputPreviewBytes)]
	for back := 0; back < utf8.UTFMax && len(cut) > 0 && !utf8.FullRune(cut[lastRuneStart(cut):]); back++ {
		cut = cut[:lastRuneStart(cut)]
	}
	return strings.ToValidUTF8(string(cut), "\uFFFD"), int64(len(cut)) < size
}

func lastRuneStart(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			return i
		}
	}
	return len(b)
}

func insertProgramOutput(ctx context.Context, tx *sql.Tx, program string, output RetainedOutput, reference tooloutput.RetainedStreamReference, mappings string, screenedPreview []byte) error {
	blob, err := json.Marshal(reference.Blob)
	if err != nil {
		return err
	}
	var execution any
	if output.ExecutionID != "" {
		execution = output.ExecutionID
	}
	if mappings == "" {
		mappings = "[]"
	}
	preview, truncated := outputPreview(screenedPreview, reference.Blob.Size)
	events := int64(1 + len(output.Mappings))
	bytes := int64(len(program) + len(output.ExecutionID) + len(output.Stream) + len(output.Reference) + len(reference.RetentionClaimID) + len(reference.InitiatingClaimID) + len(reference.Blob.ID) + len(blob) + len(mappings) + len(preview))
	if err := chargeProgramTrace(ctx, tx, program, events, bytes, traceChargeNonTerminal); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO invocation_program_outputs(id,program_id,execution_id,stream,reference,retention_claim_id,initiating_claim_id,blob_id,blob_json,complete,redacted,mappings_json,preview,preview_truncated,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, uuid.NewString(), program, execution, output.Stream, output.Reference, reference.RetentionClaimID, reference.InitiatingClaimID, reference.Blob.ID, string(blob), output.Complete, output.Redacted, mappings, preview, truncated, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// ProgramOutputMappings owns the admitted decode capacity until its consumer
// finishes projection. Closing it invalidates the borrowed mapping data.
type ProgramOutputMappings struct {
	Blob     blobstore.Blob
	Mappings []toolcontract.StreamSourceMapping
	release  func()
}

func (m *ProgramOutputMappings) Close() {
	if m != nil && m.release != nil {
		m.release()
		m.release = nil
		m.Mappings = nil
	}
}

// ReadProgramOutputMappings admits encoded and decoded metadata before loading
// either SQL value, using the same receipt limits as attachment.
func (r *SQLRecorder) ReadProgramOutputMappings(ctx context.Context, sessionID, programID, reference string, memory tooloutput.StreamMemoryAdmission) (*ProgramOutputMappings, error) {
	if r == nil || r.database == nil || memory == nil {
		return nil, fmt.Errorf("output mapping read requires recorder and admission")
	}
	limits, err := receiptLimits()
	if err != nil {
		return nil, err
	}
	tx, err := beginOutputRead(ctx, r.database)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	const selection = ` FROM invocation_program_outputs o JOIN invocation_programs p ON p.id=o.program_id JOIN invocation_receipts rec ON rec.id=p.parent_receipt_id WHERE o.program_id=? AND o.reference=? AND rec.session_id=?`
	var size int64
	if err := tx.QueryRowContext(ctx, `SELECT length(CAST(o.blob_json AS BLOB))+length(CAST(o.mappings_json AS BLOB))`+selection, programID, reference, sessionID).Scan(&size); err != nil {
		return nil, err
	}
	if size < 0 || size > limits.bytes {
		return nil, fmt.Errorf("output mapping encoding exceeds receipt bound")
	}
	// Covers SQL strings, JSON scratch, decoded strings and mapping capacity.
	release, err := memory.Reserve(ctx, 8192+12*size)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("output mapping read returned no lease")
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	var blobJSON, mappingsJSON string
	if err := tx.QueryRowContext(ctx, `SELECT o.blob_json,o.mappings_json`+selection, programID, reference, sessionID).Scan(&blobJSON, &mappingsJSON); err != nil {
		return nil, err
	}
	result := &ProgramOutputMappings{}
	if err := json.Unmarshal([]byte(blobJSON), &result.Blob); err != nil {
		return nil, err
	}
	if mappingsJSON != "" {
		if err := json.Unmarshal([]byte(mappingsJSON), &result.Mappings); err != nil {
			return nil, err
		}
	}
	if int64(len(result.Mappings)) > limits.events {
		return nil, fmt.Errorf("output mapping count exceeds receipt bound")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	result.release = release
	release = nil
	return result, nil
}
