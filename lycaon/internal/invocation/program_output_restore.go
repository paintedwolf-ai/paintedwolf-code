package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

// Restore preserves partial successful reconstruction on refusal so its leases
// cannot disappear while durable bodies remain. Retrying verifies the same exact
// references. No new stream may enter this lifetime until a full pass succeeds.
func (r *ProgramOutputRetention) Restore(ctx context.Context) error {
	if r == nil || ctx == nil {
		return fmt.Errorf("program output recovery requires context")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.release == nil {
		return fmt.Errorf("program output lifetime is closed")
	}
	r.ready = false
	scratch, err := r.resources.Reserve(ctx, 64<<10)
	if err != nil {
		return err
	}
	if scratch == nil {
		return fmt.Errorf("output recovery query admission returned no lease")
	}
	defer scratch()
	tx, err := beginOutputRead(ctx, r.recorder.database)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var bytes, count int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(length(CAST(retention_claim_id AS BLOB))+length(CAST(initiating_claim_id AS BLOB))+length(CAST(blob_json AS BLOB))),0),count(*) FROM invocation_program_outputs`).Scan(&bytes, &count)
	if err != nil {
		return err
	}
	if bytes < 0 || bytes > 1<<30 || count < 0 || count > 1<<20 {
		return fmt.Errorf("retained output metadata exceeds supported recovery bound")
	}
	release, err := r.resources.ReserveProgramState(ctx, 64<<10+bytes*32+count*4096)
	if err != nil {
		return err
	}
	if release == nil {
		return fmt.Errorf("output recovery metadata returned no lease")
	}
	defer release()
	rows, err := tx.QueryContext(ctx, `SELECT retention_claim_id,initiating_claim_id,blob_json FROM invocation_program_outputs ORDER BY retention_claim_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var claim, initiating, encoded string
		if err = rows.Scan(&claim, &initiating, &encoded); err != nil {
			return err
		}
		if err = r.restoreBody(ctx, claim, initiating, encoded); err != nil {
			return err
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	r.ready = true
	return nil
}

func beginOutputRead(ctx context.Context, database db.Handle) (*sql.Tx, error) {
	if reader, ok := database.(db.ReadHandle); ok {
		return reader.BeginReadTx(ctx)
	}
	return database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
}

func (r *ProgramOutputRetention) restoreBody(ctx context.Context, claim, initiating, encoded string) error {
	releaseEntry, err := r.resources.ReserveProgramState(ctx, 4096+int64(len(claim)+len(initiating)+len(encoded))*32)
	if err != nil {
		return err
	}
	if releaseEntry == nil {
		return fmt.Errorf("output reference admission returned no lease")
	}
	owned := true
	defer func() {
		if owned {
			releaseEntry()
		}
	}()
	var blob blobstore.Blob
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&blob); err != nil {
		return err
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("retained output blob has trailing data")
	}
	reference := tooloutput.RetainedStreamReference{Blob: blob, RetentionClaimID: strings.Clone(claim), InitiatingClaimID: strings.Clone(initiating)}
	if existing := r.entries[claim]; existing != nil {
		if existing.reference != reference {
			return fmt.Errorf("durable output claim changed identity")
		}
		if existing.pending != nil {
			body, err := existing.pending.TransferBody(ctx, func(context.Context, tooloutput.RetainedStreamReference) error { return nil })
			if err != nil {
				return err
			}
			existing.pending = nil
			existing.body = body
		}
		return nil
	}
	if blob.Size < 0 || blob.Size > 256<<20 {
		return fmt.Errorf("invalid retained output size")
	}
	releaseDisk, err := r.resources.ReserveSpill(ctx, 3*blob.Size+(1<<20))
	if err != nil {
		return err
	}
	if releaseDisk == nil {
		return fmt.Errorf("output recovery disk admission returned no lease")
	}
	releaseBody, err := r.resources.ReserveProgramState(ctx, int64(len(claim)+len(initiating))*2+4096)
	if err != nil || releaseBody == nil {
		releaseDisk()
		if err == nil {
			err = fmt.Errorf("output body admission returned no lease")
		}
		return err
	}
	body, err := tooloutput.RestoreRetainedStreamBody(ctx, r.store, reference, releaseDisk, releaseBody, r.resources)
	if err != nil {
		return err
	}
	r.entries[claim] = &programRetainedBody{reference: reference, body: body, release: releaseEntry}
	owned = false
	return nil
}
