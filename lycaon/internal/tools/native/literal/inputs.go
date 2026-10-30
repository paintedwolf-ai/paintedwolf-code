package literal

import (
	"bytes"
	"fmt"
	"io"
	"slices"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// input is one operand's bytes. A file is captured completely and pinned
// before any output; stdin streams unless the utility needs it whole.
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
// other error is a typed stop. whole gathers stdin within the read budget.
func (inv *invocation) open(operand string, whole bool) (*input, error) {
	if err := inv.ctx.Err(); err != nil {
		return nil, err
	}
	if operand == "-" {
		in := &input{label: "-", stdin: true, stream: inv.input}
		if !whole {
			return in, nil
		}
		data, err := readBounded(in.stream, inv.owners.streamBytes(), "stream_bytes")
		if err != nil {
			return nil, err
		}
		in.data, in.stream = data, nil
		return in, nil
	}
	bound, err := inv.paths.Bind(inv.ctx, operand, false)
	if err != nil {
		return inv.ordinaryInput(operand, err)
	}
	if bound.Null {
		return &input{label: operand, data: []byte{}, bound: bound}, nil
	}
	captured, err := inv.owners.Revisions.CaptureBytes(inv.ctx, inv.tc, bound.Resolved.Abs, inv.owners.readBytes())
	if err != nil {
		return inv.ordinaryInput(operand, err)
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
	if slices.Contains(inv.pins, revision) {
		return nil
	}
	inv.pins = append(inv.pins, revision)
	return inv.owners.Pins.AppendRevisionPin(inv.ctx, execution, revision)
}

// readBounded gathers a stream whole, refusing rather than truncating input
// above maximum.
func readBounded(r io.Reader, maximum int64, limit string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, limitExceeded(limit, maximum, int64(len(data)))
	}
	return data, nil
}
