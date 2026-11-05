package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ProgramSourceObserver is the existing evidence owner's complete operation.
// Only its confirmation can create observation evidence; output receipts cannot.
type ProgramSourceObserver interface {
	CommitDeliveredSource(context.Context, string, evidence.DeliveredSourceObservation) (string, error)
}

// ProgramSourceDelivery binds an actually delivered page to its retained output
// position. Observation is source-owner-confirmed against captured bytes outside
// stdout. Output positions refer to the full retained stream, not its preview.
type ProgramSourceDelivery struct {
	SessionID, ProgramID, OutputReference string
	OutputStartByte, OutputEndByte        int64
	// NumberedLines replaces the output range when numbered output delivered one source
	// window through many stream positions, each in stream coordinates.
	NumberedLines []toolcontract.StreamSourceMapping
	Observation   evidence.DeliveredSourceObservation
}

// spans lists the stream positions of a delivery in source order.
func (in ProgramSourceDelivery) spans(fact *evidence.SourceDelivery) []toolcontract.StreamSourceMapping {
	if len(in.NumberedLines) > 0 {
		return in.NumberedLines
	}
	return []toolcontract.StreamSourceMapping{{Source: fact.Source, SourceStartByte: fact.SourceStartByte, SourceEndByte: fact.SourceEndByte, OutputStartByte: in.OutputStartByte, OutputEndByte: in.OutputEndByte}}
}

// ProgramDeliverySettlement keeps the authoritative source-owner acknowledgement
// separate from linking its receipt. A receipt fault never retracts observation
// or supplies permission to replay delivery, command execution or a mutation.
type ProgramDeliverySettlement struct {
	SourceOwnerReference string
	ReceiptRecorded      bool
}

func (r *SQLRecorder) CommitProgramSourceDelivery(ctx context.Context, in ProgramSourceDelivery, owner ProgramSourceObserver, admission ProgramFeedbackAdmission) (out ProgramDeliverySettlement, err error) {
	if r == nil || r.database == nil || ctx == nil || owner == nil || admission == nil || in.SessionID == "" || in.ProgramID == "" || in.OutputReference == "" {
		return out, fmt.Errorf("source delivery requires actual retained output and evidence owner")
	}
	size, err := in.Observation.RecordStateBytes()
	if err != nil {
		return out, err
	}
	release, err := admission.ReserveProgramState(ctx, size+(64<<10))
	if err != nil {
		return out, err
	}
	if release == nil {
		return out, fmt.Errorf("source delivery admission returned no lease")
	}
	defer release()
	record := in.Observation.Record()
	fact := record.SourceDelivery
	if fact == nil {
		return out, fmt.Errorf("source delivery has no exact page identity")
	}
	spans := in.spans(fact)
	for _, span := range spans {
		if span.OutputStartByte < 0 || span.OutputEndByte <= span.OutputStartByte || span.OutputEndByte-span.OutputStartByte != span.SourceEndByte-span.SourceStartByte {
			return out, fmt.Errorf("source delivery has no exact page identity")
		}
	}
	outputID, mappings, releaseMapping, err := r.bindProgramDelivery(ctx, in, spans, admission)
	if err != nil {
		return out, err
	}
	defer releaseMapping()
	out.SourceOwnerReference, err = owner.CommitDeliveredSource(ctx, in.SessionID, in.Observation)
	if out.SourceOwnerReference == "" {
		if err == nil {
			err = fmt.Errorf("source owner did not acknowledge delivered observation")
		}
		return out, err
	}
	// Preserve a known owner acknowledgement even when its post-commit work or
	// this receipt transaction fails. Acknowledged delivery is not retracted.
	ownerErr := err
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return out, errors.Join(ownerErr, err)
	}
	defer func() { _ = tx.Rollback() }()
	for i, mapping := range mappings {
		if err = recordDeliveryReceipt(ctx, tx, in.ProgramID, outputID, out.SourceOwnerReference, fact, i, mapping); err != nil {
			return out, errors.Join(ownerErr, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return out, errors.Join(ownerErr, err)
	}
	out.ReceiptRecorded = true
	return out, ownerErr
}

// recordDeliveryReceipt links one stream position to its owner's acknowledgement.
// A numbered delivery's later positions extend its identity with their index.
func recordDeliveryReceipt(ctx context.Context, tx *sql.Tx, programID, outputID, reference string, fact *evidence.SourceDelivery, index int, mapping toolcontract.StreamSourceMapping) error {
	encoded, err := json.Marshal([]toolcontract.StreamSourceMapping{mapping})
	if err != nil {
		return err
	}
	deliveryID := fact.DeliveryID
	if index > 0 {
		deliveryID = fmt.Sprintf("%s#%d", deliveryID, index)
	}
	ins, err := tx.ExecContext(ctx, `INSERT INTO invocation_program_deliveries(id,output_id,response_id,delivery_id,start_byte,end_byte,source_owner_ref,mappings_json,created_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(output_id,response_id,delivery_id) DO NOTHING`, uuid.NewString(), outputID, fact.ResponseID, deliveryID, mapping.OutputStartByte, mapping.OutputEndByte, reference, string(encoded), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	written, err := ins.RowsAffected()
	if err != nil {
		return err
	}
	if written > 0 {
		if err = chargeProgramTrace(ctx, tx, programID, 1, int64(len(fact.ResponseID)+len(deliveryID)+len(reference)+len(encoded)), traceChargeNonTerminal); err != nil {
			return err
		}
	}
	var start, end int64
	var storedRef, storedMappings string
	err = tx.QueryRowContext(ctx, `SELECT start_byte,end_byte,source_owner_ref,mappings_json FROM invocation_program_deliveries WHERE output_id=? AND response_id=? AND delivery_id=?`, outputID, fact.ResponseID, deliveryID).Scan(&start, &end, &storedRef, &storedMappings)
	if err != nil {
		return err
	}
	if start != mapping.OutputStartByte || end != mapping.OutputEndByte || storedRef != reference || storedMappings != string(encoded) {
		return fmt.Errorf("delivery identity conflicts with existing exact receipt")
	}
	return nil
}

func (r *SQLRecorder) bindProgramDelivery(ctx context.Context, in ProgramSourceDelivery, spans []toolcontract.StreamSourceMapping, admission ProgramFeedbackAdmission) (string, []toolcontract.StreamSourceMapping, func(), error) {
	var none []toolcontract.StreamSourceMapping
	tx, err := beginOutputRead(ctx, r.database)
	if err != nil {
		return "", none, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	const selection = ` FROM invocation_program_outputs o JOIN invocation_programs p ON p.id=o.program_id JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE o.program_id=? AND o.reference=? AND r.session_id=?`
	var size, count int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(length(CAST(o.id AS BLOB))+length(CAST(o.mappings_json AS BLOB))),0),count(*)`+selection, in.ProgramID, in.OutputReference, in.SessionID).Scan(&size, &count); err != nil {
		return "", none, nil, err
	}
	limits, err := receiptLimits()
	if err != nil {
		return "", none, nil, err
	}
	if count != 1 || size < 0 || size > limits.bytes {
		return "", none, nil, fmt.Errorf("delivery requires one bounded retained output identity")
	}
	release, err := admission.ReserveProgramState(ctx, 4096+size*32)
	if err != nil {
		return "", none, nil, err
	}
	if release == nil {
		return "", none, nil, fmt.Errorf("delivery mapping admission returned no lease")
	}
	accepted := false
	defer func() {
		if !accepted {
			release()
		}
	}()
	var output, encoded string
	var bytes int64
	if err := tx.QueryRowContext(ctx, `SELECT o.id,o.mappings_json,json_extract(o.blob_json,'$.Size')`+selection, in.ProgramID, in.OutputReference, in.SessionID).Scan(&output, &encoded, &bytes); err != nil {
		return "", none, nil, err
	}
	for _, span := range spans {
		if span.OutputEndByte > bytes {
			return "", none, nil, fmt.Errorf("delivered page exceeds actual retained body")
		}
	}
	var candidates []toolcontract.StreamSourceMapping
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&candidates); err != nil {
		return "", none, nil, err
	}
	var trailer any
	if err := decoder.Decode(&trailer); !errors.Is(err, io.EOF) {
		return "", none, nil, fmt.Errorf("stored output mappings have trailing data")
	}
	// Record() is already admitted by the caller; this second detached copy is
	// covered separately because binding and owner publication overlap its life.
	size, err = in.Observation.RecordStateBytes()
	if err != nil {
		return "", none, nil, err
	}
	releaseRecord, err := admission.ReserveProgramState(ctx, size)
	if err != nil {
		return "", none, nil, err
	}
	if releaseRecord == nil {
		return "", none, nil, fmt.Errorf("delivery identity admission returned no lease")
	}
	defer releaseRecord()
	fact := in.Observation.Record().SourceDelivery
	if fact == nil {
		return "", none, nil, fmt.Errorf("source owner confirmation missing")
	}
	exact := make([]toolcontract.StreamSourceMapping, 0, len(spans))
	for _, span := range spans {
		bound, ok := exactDeliveryMapping(candidates, fact.Source, span)
		if !ok {
			return "", none, nil, fmt.Errorf("delivered page has no matching native source mapping")
		}
		exact = append(exact, bound)
	}
	if err := tx.Commit(); err != nil {
		return "", none, nil, err
	}
	accepted = true
	return output, exact, release, nil
}

// exactDeliveryMapping finds the retained mapping holding one delivered span.
func exactDeliveryMapping(candidates []toolcontract.StreamSourceMapping, source toolcontract.SourcePin, span toolcontract.StreamSourceMapping) (toolcontract.StreamSourceMapping, bool) {
	for _, candidate := range candidates {
		if candidate.Source != source || candidate.SourceStartByte > span.SourceStartByte || candidate.SourceEndByte < span.SourceEndByte {
			continue
		}
		start := candidate.OutputStartByte + span.SourceStartByte - candidate.SourceStartByte
		end := candidate.OutputStartByte + span.SourceEndByte - candidate.SourceStartByte
		if start == span.OutputStartByte && end == span.OutputEndByte {
			exact := candidate
			exact.SourceStartByte, exact.SourceEndByte = span.SourceStartByte, span.SourceEndByte
			exact.OutputStartByte, exact.OutputEndByte = start, end
			return exact, true
		}
	}
	return toolcontract.StreamSourceMapping{}, false
}
