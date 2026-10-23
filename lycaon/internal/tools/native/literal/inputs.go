package literal

import (
	"bytes"
	"fmt"
	"io"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// input serves one pinned operand. Ordinary disk files stream through a held
// descriptor; only whole-file operations gather their input.
type input struct {
	label  string
	data   []byte
	stream io.Reader
	pin    *toolcontract.SourcePin
	bound  projectpaths.BoundPath
	stdin  bool
}

// reader returns the bytes in order, whichever way they were acquired.
func (in *input) reader() io.Reader {
	if in.stream != nil {
		return in.stream
	}
	return bytes.NewReader(in.data)
}

// operandsOrStdin applies the "no PATH reads stdin" rule.
func (inv *invocation) operandsOrStdin() []string {
	operands := inv.operands()
	if len(operands) == 0 {
		return []string{"-"}
	}
	return operands
}

// open acquires one operand. It returns nil after reporting an ordinary
// failure (missing, directory, permission) so the utility continues; any
// other error is a typed stop. whole gathers within the whole-input bound.
func (inv *invocation) open(operand string, whole bool) (*input, error) {
	inv.closeCurrentStream()
	if err := inv.ctx.Err(); err != nil {
		return nil, err
	}
	if operand == "-" {
		in := &input{label: "-", stdin: true, stream: inv.input}
		if !whole {
			return in, nil
		}
		captured, err := inv.owners.Revisions.ReadWholeInput(inv.ctx, in.stream, inv.owners.readBytes())
		if err != nil {
			return nil, err
		}
		inv.closers = append(inv.closers, captured)
		in.data, in.stream = captured.Bytes, nil
		return in, nil
	}
	bound, err := inv.paths.Bind(inv.ctx, operand, false)
	if err != nil {
		return inv.ordinaryInput(operand, err)
	}
	if bound.Null {
		return &input{label: operand, data: []byte{}, bound: bound}, nil
	}
	if !whole {
		streamed, err := inv.owners.Revisions.OpenStream(inv.ctx, inv.tc, bound.Resolved.Abs, 0)
		if err != nil {
			return inv.ordinaryInput(operand, err)
		}
		inv.currentStream = streamed.Reader
		if err := inv.recordPin(streamed.Revision); err != nil {
			return nil, err
		}
		pin, ok := streamed.Revision.SourcePin()
		in := &input{label: operand, stream: streamed.Reader, bound: bound}
		if ok {
			in.pin = &pin
		}
		return in, nil
	}
	captured, err := inv.owners.Revisions.CaptureBytes(inv.ctx, inv.tc, bound.Resolved.Abs, inv.owners.readBytes())
	if err != nil {
		return inv.ordinaryInput(operand, err)
	}
	inv.closers = append(inv.closers, &captured)
	// A credential file's bytes reach no stream until their exposure is recorded.
	if err := inv.tc.ObserveCredentialRead(inv.ctx, bound.Resolved.Abs, captured.Path, string(captured.Bytes)); err != nil {
		return nil, fmt.Errorf("record credential file read: %w", err)
	}
	if err := inv.recordPin(captured.Revision); err != nil {
		return nil, err
	}
	pin, ok := captured.Revision.SourcePin()
	in := &input{label: operand, data: captured.Bytes, bound: bound}
	if ok {
		in.pin = &pin
	}
	return in, nil
}

func (inv *invocation) ordinaryInput(operand string, err error) (*input, error) {
	err = projectpaths.PathOperationError(operand, err)
	if reason, ok := ordinary(err); ok {
		inv.fail(inv.failureStatus(), operand, reason)
		return nil, nil
	}
	return nil, err
}

// failureStatus is the catalog's ordinary failure status: diff reserves 1
// for differences, so its trouble is 2.
func (inv *invocation) failureStatus() int {
	if inv.name() == "diff" {
		return 2
	}
	return 1
}

// recordPin retains every capture of this execution before its bytes are used.
// A domain owner may open files concurrently and more than once; one
// unchanged capture is recorded once.
func (inv *invocation) recordPin(revision toolcontract.ExecutionRevision) error {
	execution := inv.request.Invocation.Identity.ExecutionID
	if inv.owners.Pins == nil || execution == "" {
		return fmt.Errorf("literal reads require a program execution and pin recorder")
	}
	inv.pinMu.Lock()
	defer inv.pinMu.Unlock()
	seen, err := inv.pins.remember(inv.ctx, inv.owners.Revisions.Memory, revision)
	if err != nil || seen {
		return err
	}
	return inv.owners.Pins.RecordRevisionPins(inv.ctx, execution, len(inv.pins.entries)-1, []toolcontract.ExecutionRevision{revision})
}
