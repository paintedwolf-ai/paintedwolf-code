package invocation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

// ProgramOutputResources is the invocation/session accounting shared with live
// streams. Boot must reconstruct all durable references before admitting work.
type ProgramOutputResources interface {
	tooloutput.SpillAdmission
	tooloutput.StreamStateAdmission
	tooloutput.StreamMemoryAdmission
}

// ProgramOutputRetention keeps actual body tokens in the invocation/output
// lifecycle. It stores no interpreter continuation and supplies no authority.
// Failed/ambiguous attachments keep their original wrapper and leases until an
// authoritative database read establishes attachment or safe collection.
type ProgramOutputRetention struct {
	recorder  *SQLRecorder
	store     blobstore.Store
	resources ProgramOutputResources
	mu        sync.Mutex
	entries   map[string]*programRetainedBody
	ready     bool
	release   func()
}

type programRetainedBody struct {
	reference tooloutput.RetainedStreamReference
	body      *tooloutput.RetainedStreamBody
	pending   *tooloutput.StreamSpill
	release   func()
}

func NewProgramOutputRetention(ctx context.Context, recorder *SQLRecorder, store blobstore.Store, resources ProgramOutputResources) (*ProgramOutputRetention, error) {
	if ctx == nil || recorder == nil || recorder.database == nil || resources == nil {
		return nil, fmt.Errorf("program output retention requires recorder and resource admission")
	}
	release, err := resources.ReserveProgramState(ctx, 4096)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("output lifetime admission returned no lease")
	}
	store = recorder.ProgramBlobStore(store)
	store.Dir = tooloutput.ToolOutputSpillDir
	return &ProgramOutputRetention{recorder: recorder, store: store, resources: resources, entries: make(map[string]*programRetainedBody), release: release}, nil
}

// Attach retains the actual body after the existing receipt operation commits.
// A caller owns spill until this method reserves its lifetime entry. Afterwards
// every outcome remains owned here, including an ambiguous commit failure.
func (r *ProgramOutputRetention) Attach(ctx context.Context, program string, output RetainedOutput, spill *tooloutput.StreamSpill) error {
	if r == nil || ctx == nil || spill == nil {
		return fmt.Errorf("output attachment requires retained stream")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ready || r.release == nil {
		return fmt.Errorf("program output recovery is incomplete")
	}
	claim := spill.RetentionClaimID()
	if r.entries[claim] != nil {
		return fmt.Errorf("output retention claim is already owned")
	}
	release, err := r.resources.ReserveProgramState(ctx, 8192+int64(len(claim)+len(program)+len(output.Reference))*4)
	if err != nil {
		return err
	}
	if release == nil {
		return fmt.Errorf("output lifetime entry returned no lease")
	}
	entry := &programRetainedBody{pending: spill, release: release}
	// Retention identity is checked against the actual transfer callback, never
	// reconstructed from operation provenance or a replacement retention marker.
	entry.reference = tooloutput.RetainedStreamReference{Blob: spill.Blob, RetentionClaimID: claim, InitiatingClaimID: spill.InitiatingClaimID()}
	r.entries[claim] = entry
	body, err := r.recorder.AttachProgramOutput(ctx, program, output, spill, r.resources)
	if err != nil {
		return err
	}
	entry.pending = nil
	entry.body = body
	return nil
}

// Open names an actual durable reference in this session/program. It neither
// establishes delivered-source evidence nor treats readable bytes as completion.
func (r *ProgramOutputRetention) Open(ctx context.Context, session, program, reference string) (io.ReadCloser, error) {
	if r == nil || ctx == nil {
		return nil, fmt.Errorf("output read requires context")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ready || r.release == nil {
		return nil, fmt.Errorf("program output recovery is incomplete")
	}
	scratch, err := r.resources.Reserve(ctx, 64<<10)
	if err != nil {
		return nil, err
	}
	if scratch == nil {
		return nil, fmt.Errorf("output query admission returned no lease")
	}
	defer scratch()
	tx, err := beginOutputRead(ctx, r.recorder.database)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	const selection = ` FROM invocation_program_outputs o JOIN invocation_programs p ON p.id=o.program_id JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE o.program_id=? AND o.reference=? AND r.session_id=?`
	var size int64
	if err = tx.QueryRowContext(ctx, `SELECT length(CAST(o.retention_claim_id AS BLOB))`+selection, program, reference, session).Scan(&size); err != nil {
		return nil, err
	}
	if size < 0 || size > 1<<30 {
		return nil, fmt.Errorf("output claim exceeds supported read bound")
	}
	release, err := r.resources.ReserveProgramState(ctx, 4096+size*4)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("output read admission returned no lease")
	}
	defer release()
	var claim string
	if err = tx.QueryRowContext(ctx, `SELECT o.retention_claim_id`+selection, program, reference, session).Scan(&claim); err != nil {
		return nil, err
	}
	entry := r.entries[claim]
	if entry == nil || entry.body == nil {
		return nil, fmt.Errorf("durable output body has not been reconciled")
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return entry.body.Open(ctx)
}

// Collect rechecks durable references before touching claims. It never closes a
// still-attached token: even a failed Close would gate that token's future reads.
// Active readers and other blob references keep the original leases for retry.
func (r *ProgramOutputRetention) Collect(ctx context.Context) error {
	if r == nil || ctx == nil || r.recorder == nil || r.resources == nil {
		return fmt.Errorf("output collection requires context and initialized lifetime")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var failures error
	for claim, entry := range r.entries {
		var retained bool
		err := r.recorder.database.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM invocation_program_outputs WHERE retention_claim_id=?)`, claim).Scan(&retained)
		if err != nil {
			return err
		}
		if retained {
			continue
		}
		if entry.pending != nil {
			err = entry.pending.Close()
		} else {
			err = entry.body.Close()
		}
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		entry.release()
		delete(r.entries, claim)
	}
	return failures
}

// Close succeeds only after callers release durable references and active reads.
// Shutdown does not erase database refs or convert failed collection into free
// quota. A blocked Close keeps this lifetime available for reconciliation.
func (r *ProgramOutputRetention) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if err := r.Collect(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.entries) != 0 {
		return fmt.Errorf("durable program outputs still retain this lifetime")
	}
	if r.release != nil {
		r.release()
		r.release = nil
	}
	r.ready = false
	return nil
}
