package invocation

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// Pin storage. The newest pins of an execution are rows; every
// revisionPinSegment of them are sealed into one gzip-compressed JSON segment
// with the digest of its exact body, so a walk that pins every file it visits
// costs one compressed segment per thousand files rather than a row each.
// Segments cascade with their execution, as rows do.
const (
	revisionPinSegment  = 1024
	revisionPinEncoding = "gzip-json-v1"
	// maxSegmentBody bounds one segment's decompressed body.
	maxSegmentBody = 16 << 20
)

// maxExecutionRevisionPins bounds what one dynamic execution may pin: every
// entry a walk may visit plus every operand an argv may name. No owner reads
// more inputs than that in one execution.
var maxExecutionRevisionPins = sync.OnceValues(func() (int, error) {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return 0, err
	}
	var visits, fields int64
	for _, limit := range catalog.Language.Limits {
		switch limit.ID {
		case "traversal_visits":
			visits = limit.Maximum
		case "expanded_fields":
			fields = limit.Maximum
		}
	}
	if visits < 1 || fields < 1 {
		return 0, fmt.Errorf("compiled language is missing traversal or field bounds")
	}
	return int(visits + fields), nil
})

// storedPin is a pin's segment encoding; its capture time stays with it.
type storedPin struct {
	Role             toolcontract.ExecutionRevisionRole  `json:"role"`
	Mode             toolcontract.ExecutionRevisionMode  `json:"mode"`
	Basis            toolcontract.ExecutionRevisionBasis `json:"basis"`
	RootID           string                              `json:"root_id"`
	Path             string                              `json:"path"`
	DocumentID       string                              `json:"document_id"`
	DocumentRevision int64                               `json:"document_revision"`
	DiskSHA256       string                              `json:"disk_sha256"`
	Representation   toolcontract.SourceRepresentation   `json:"representation"`
	ContentSHA256    string                              `json:"content_sha256"`
	ContentBytes     int64                               `json:"content_bytes"`
	CapturedAt       string                              `json:"captured_at"`
}

func (p storedPin) revision() toolcontract.ExecutionRevision {
	return toolcontract.ExecutionRevision{Role: p.Role, Mode: p.Mode, Basis: p.Basis, RootID: p.RootID, Path: p.Path, DocumentID: p.DocumentID,
		DocumentRevision: p.DocumentRevision, DiskSHA256: p.DiskSHA256, Representation: p.Representation, ContentSHA256: p.ContentSHA256, ContentBytes: p.ContentBytes}
}

const pinColumns = `role,mode,basis,root_id,path,document_id,document_revision,disk_sha256,representation,content_sha256,content_bytes`

// RecordRevisionPins retains the revisions one execution captured before its
// owner prepared an effect. pins hold ordinals first, first+1, ... in capture
// order. A position already recorded must hold the same capture: resubmitting
// a recorded range is idempotent and a different capture is refused rather
// than refreshed. first may not leave a gap after the recorded pins, and pins
// append only while the execution is unsettled.
func (r *SQLRecorder) RecordRevisionPins(ctx context.Context, executionID string, first int, pins []toolcontract.ExecutionRevision) error {
	if first < 0 {
		return fmt.Errorf("revision pins require an execution, a first ordinal and at least one capture")
	}
	return r.recordRevisionPins(ctx, executionID, &first, pins)
}

// AppendRevisionPin records one capture at the execution's next position. An
// execution's owners capture independently (each redirect destination, the
// utility's own sources and destinations), so none of them assigns ordinals.
func (r *SQLRecorder) AppendRevisionPin(ctx context.Context, executionID string, pin toolcontract.ExecutionRevision) error {
	return r.recordRevisionPins(ctx, executionID, nil, []toolcontract.ExecutionRevision{pin})
}

func (r *SQLRecorder) recordRevisionPins(ctx context.Context, executionID string, firstAt *int, pins []toolcontract.ExecutionRevision) error {
	if r == nil || r.database == nil {
		return fmt.Errorf("invocation recorder is not configured")
	}
	if executionID == "" || len(pins) == 0 {
		return fmt.Errorf("revision pins require an execution, a first ordinal and at least one capture")
	}
	for _, pin := range pins {
		if err := pin.Validate(); err != nil {
			return err
		}
	}
	maximum, err := maxExecutionRevisionPins()
	if err != nil {
		return err
	}
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var program, state string
	if err := tx.QueryRowContext(ctx, `SELECT program_id,state FROM invocation_program_executions WHERE id=?`, executionID).Scan(&program, &state); err != nil {
		return err
	}
	if state != "prepared" && state != "waiting" && state != "running" {
		return fmt.Errorf("settled execution cannot gain revision pins")
	}
	sealed, unsealed, err := revisionPinCounts(ctx, tx, executionID)
	if err != nil {
		return err
	}
	recorded := sealed + unsealed
	first := recorded
	if firstAt != nil {
		first = *firstAt
	}
	if first > recorded {
		return fmt.Errorf("revision pin %d would leave a gap after %d recorded captures", first, recorded)
	}
	if overlap := min(len(pins), recorded-first); overlap > 0 {
		existing, err := readRevisionPinRange(ctx, tx, executionID, first, first+overlap)
		if err != nil {
			return err
		}
		// The reader requires exactly overlap pins; bound both slices explicitly.
		for i := 0; i < len(existing) && i < len(pins); i++ {
			if existing[i] != pins[i] {
				return fmt.Errorf("revision pin %d conflicts with the recorded capture", first+i)
			}
		}
		pins = pins[overlap:]
	}
	if recorded+len(pins) > maximum {
		return fmt.Errorf("execution exceeds %d revision pins", maximum)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 0; i < len(pins); {
		if unsealed == 0 && len(pins)-i >= revisionPinSegment {
			if err := insertRevisionPinSegment(ctx, tx, program, executionID, sealed, pins[i:i+revisionPinSegment], now); err != nil {
				return err
			}
			sealed += revisionPinSegment
			i += revisionPinSegment
			continue
		}
		batch := min(len(pins)-i, revisionPinSegment-unsealed)
		if err := insertRevisionPinRows(ctx, tx, program, executionID, recorded+i, pins[i:i+batch], now); err != nil {
			return err
		}
		unsealed += batch
		i += batch
		if unsealed == revisionPinSegment {
			if err := sealRevisionPins(ctx, tx, program, executionID, sealed); err != nil {
				return err
			}
			sealed, unsealed = sealed+revisionPinSegment, 0
		}
	}
	return tx.Commit()
}

func insertRevisionPinRows(ctx context.Context, tx *sql.Tx, program, executionID string, first int, pins []toolcontract.ExecutionRevision, capturedAt string) error {
	const maxRowsPerInsert = 64
	for start := 0; start < len(pins); start += maxRowsPerInsert {
		chunk := pins[start:min(start+maxRowsPerInsert, len(pins))]
		var query strings.Builder
		query.WriteString(`INSERT INTO invocation_program_revision_pins(program_id,execution_id,ordinal,` + pinColumns + `,captured_at) VALUES`)
		args := make([]any, 0, len(chunk)*15)
		for idx, pin := range chunk {
			if idx > 0 {
				query.WriteByte(',')
			}
			query.WriteString(`(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
			args = append(args, program, executionID, first+start+idx, string(pin.Role), string(pin.Mode), string(pin.Basis), pin.RootID, pin.Path, pin.DocumentID, pin.DocumentRevision, pin.DiskSHA256, string(pin.Representation), pin.ContentSHA256, pin.ContentBytes, capturedAt)
		}
		if _, err := tx.ExecContext(ctx, query.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

func insertRevisionPinSegment(ctx context.Context, tx *sql.Tx, program, executionID string, first int, pins []toolcontract.ExecutionRevision, capturedAt string) error {
	stored := make([]storedPin, len(pins))
	for i, pin := range pins {
		stored[i] = storedPin{
			Role: pin.Role, Mode: pin.Mode, Basis: pin.Basis, RootID: pin.RootID, Path: pin.Path,
			DocumentID: pin.DocumentID, DocumentRevision: pin.DocumentRevision, DiskSHA256: pin.DiskSHA256,
			Representation: pin.Representation, ContentSHA256: pin.ContentSHA256, ContentBytes: pin.ContentBytes,
			CapturedAt: capturedAt,
		}
	}
	return writeRevisionPinSegment(ctx, tx, program, executionID, first, stored)
}

// RevisionPins returns one session's execution captures in recorded order.
func (r *SQLRecorder) RevisionPins(ctx context.Context, sessionID, executionID string) ([]toolcontract.ExecutionRevision, error) {
	if r == nil || r.database == nil {
		return nil, fmt.Errorf("invocation recorder is not configured")
	}
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM invocation_program_executions e JOIN invocation_programs p ON p.id=e.program_id JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE e.id=? AND r.session_id=?`, executionID, sessionID).Scan(&count); err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, fmt.Errorf("execution is not visible to this session")
	}
	sealed, unsealed, err := revisionPinCounts(ctx, tx, executionID)
	if err != nil {
		return nil, err
	}
	return readRevisionPinRange(ctx, tx, executionID, 0, sealed+unsealed)
}

// revisionPinCounts reports the sealed segment prefix and the rows after it.
func revisionPinCounts(ctx context.Context, tx *sql.Tx, executionID string) (sealed, unsealed int, err error) {
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(first_ordinal+pin_count),0) FROM invocation_program_revision_pin_segments WHERE execution_id=?`, executionID).Scan(&sealed); err != nil {
		return 0, 0, err
	}
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM invocation_program_revision_pins WHERE execution_id=?`, executionID).Scan(&unsealed)
	return sealed, unsealed, err
}

type pinSegment struct {
	first, count int
	body         []byte
	digest       string
}

// readRevisionPinRange returns ordinals [from, to) from segments and rows.
func readRevisionPinRange(ctx context.Context, tx *sql.Tx, executionID string, from, to int) ([]toolcontract.ExecutionRevision, error) {
	out := make([]toolcontract.ExecutionRevision, 0, to-from)
	segments, err := tx.QueryContext(ctx, `SELECT first_ordinal,pin_count,body,body_sha256 FROM invocation_program_revision_pin_segments WHERE execution_id=? AND first_ordinal<? AND first_ordinal+pin_count>? ORDER BY first_ordinal`, executionID, to, from)
	if err != nil {
		return nil, err
	}
	defer func() { _ = segments.Close() }()
	var found []pinSegment
	for segments.Next() {
		var s pinSegment
		if err := segments.Scan(&s.first, &s.count, &s.body, &s.digest); err != nil {
			_ = segments.Close()
			return nil, err
		}
		found = append(found, s)
	}
	if err := segments.Err(); err != nil {
		return nil, err
	}
	if err := segments.Close(); err != nil {
		return nil, err
	}
	for _, s := range found {
		pins, err := decodeRevisionPinSegment(s.body, s.digest, s.count)
		if err != nil {
			return nil, fmt.Errorf("revision pin segment at %d: %w", s.first, err)
		}
		for i, pin := range pins {
			if ordinal := s.first + i; ordinal >= from && ordinal < to {
				out = append(out, pin.revision())
			}
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+pinColumns+` FROM invocation_program_revision_pins WHERE execution_id=? AND ordinal>=? AND ordinal<? ORDER BY ordinal`, executionID, from, to)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var pin toolcontract.ExecutionRevision
		if err := rows.Scan(&pin.Role, &pin.Mode, &pin.Basis, &pin.RootID, &pin.Path, &pin.DocumentID, &pin.DocumentRevision, &pin.DiskSHA256, &pin.Representation, &pin.ContentSHA256, &pin.ContentBytes); err != nil {
			return nil, err
		}
		out = append(out, pin)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) != to-from {
		return nil, fmt.Errorf("revision pins %d..%d are incomplete: %d recorded", from, to, len(out))
	}
	return out, nil
}

// sealRevisionPins moves one full run of rows into a compressed segment.
func sealRevisionPins(ctx context.Context, tx *sql.Tx, program, executionID string, first int) error {
	rows, err := tx.QueryContext(ctx, `SELECT `+pinColumns+`,captured_at FROM invocation_program_revision_pins WHERE execution_id=? AND ordinal>=? AND ordinal<? ORDER BY ordinal`, executionID, first, first+revisionPinSegment)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	pins := make([]storedPin, 0, revisionPinSegment)
	for rows.Next() {
		var p storedPin
		if err := rows.Scan(&p.Role, &p.Mode, &p.Basis, &p.RootID, &p.Path, &p.DocumentID, &p.DocumentRevision, &p.DiskSHA256, &p.Representation, &p.ContentSHA256, &p.ContentBytes, &p.CapturedAt); err != nil {
			_ = rows.Close()
			return err
		}
		pins = append(pins, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(pins) != revisionPinSegment {
		return fmt.Errorf("revision pin segment at %d holds %d of %d rows", first, len(pins), revisionPinSegment)
	}
	if err := writeRevisionPinSegment(ctx, tx, program, executionID, first, pins); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM invocation_program_revision_pins WHERE execution_id=? AND ordinal>=? AND ordinal<?`, executionID, first, first+revisionPinSegment)
	return err
}

func writeRevisionPinSegment(ctx context.Context, tx *sql.Tx, program, executionID string, first int, pins []storedPin) error {
	plain, err := json.Marshal(pins)
	if err != nil {
		return err
	}
	var body bytes.Buffer
	writer := gzip.NewWriter(&body)
	if _, err := writer.Write(plain); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	digest := sha256.Sum256(plain)
	_, err = tx.ExecContext(ctx, `INSERT INTO invocation_program_revision_pin_segments(program_id,execution_id,first_ordinal,pin_count,encoding,body,body_sha256) VALUES(?,?,?,?,?,?,?)`,
		program, executionID, first, len(pins), revisionPinEncoding, body.Bytes(), hex.EncodeToString(digest[:]))
	return err
}

// decodeRevisionPinSegment verifies a segment's digest over its exact
// decompressed body and its declared count before any pin is used.
func decodeRevisionPinSegment(body []byte, digest string, count int) ([]storedPin, error) {
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	plain, err := io.ReadAll(io.LimitReader(reader, maxSegmentBody+1))
	if err != nil {
		return nil, err
	}
	if len(plain) > maxSegmentBody {
		return nil, fmt.Errorf("segment body exceeds %d bytes", maxSegmentBody)
	}
	sum := sha256.Sum256(plain)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, fmt.Errorf("segment digest mismatch")
	}
	var pins []storedPin
	if err := json.Unmarshal(plain, &pins); err != nil {
		return nil, err
	}
	if len(pins) != count {
		return nil, fmt.Errorf("segment holds %d pins, declared %d", len(pins), count)
	}
	for _, pin := range pins {
		if err := pin.revision().Validate(); err != nil {
			return nil, err
		}
	}
	return pins, nil
}
