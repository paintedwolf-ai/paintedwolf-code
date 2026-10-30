package literal

import (
	"bytes"
	"fmt"
	"io"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// input serves one pinned operand. Ordinary disk files stream through a held
// descriptor; only whole-file operations gather their input.
type input struct {
	label  string
	data   []byte
	stream io.Reader
	closer io.Closer
	pin    *toolcontract.SourcePin
	bound  projectpaths.BoundPath
	stdin  bool
}

func (in *input) close() {
	if in != nil && in.closer != nil {
		_ = in.closer.Close()
		in.closer = nil
	}
}

// reader returns the bytes in order, whichever way they were acquired.
func (in *input) reader() io.Reader {
	if in.stream != nil {
		return in.stream
	}
	return bytes.NewReader(in.data)
}

// operandsOrStdin applies the "no PATH reads stdin" rule.
func (inv *invocation) operandsOrStdin() commandprogram.Fields {
	operands := inv.argv.Operands()
	if len(operands) == 0 {
		return commandprogram.Fields{{Value: "-"}}
	}
	return operands
}

// open acquires one string operand.
func (inv *invocation) open(operand string, whole bool) (*input, error) {
	return inv.openField(commandprogram.Field{Value: operand}, whole)
}

// openField acquires one operand. It returns nil after reporting an ordinary
// failure (missing, directory, permission) so the utility continues; any
// other error is a typed stop. whole gathers within the whole-input bound.
func (inv *invocation) openField(field commandprogram.Field, whole bool) (*input, error) {
	inv.closeCurrentStream()
	if err := inv.ctx.Err(); err != nil {
		return nil, err
	}
	operand := field.Value
	if operand == "-" {
		in := &input{label: "-", stdin: true, stream: inv.input}
		if !whole {
			return in, nil
		}
		captured, err := inv.owners.Revisions.ReadWholeInput(inv.ctx, in.stream, inv.owners.readBytes())
		if err != nil {
			return nil, err
		}
		inv.releases = append(inv.releases, captured.Close)
		in.data, in.stream = captured.Bytes, nil
		return in, nil
	}
	if reader, ok := field.ProcessSubstitution().OpenReader(); ok {
		in := &input{label: operand, stdin: true, stream: reader, closer: reader}
		if !whole {
			inv.currentStream = reader
			return in, nil
		}
		captured, err := inv.owners.Revisions.ReadWholeInput(inv.ctx, reader, inv.owners.readBytes())
		_ = reader.Close()
		if err != nil {
			return nil, err
		}
		inv.releases = append(inv.releases, captured.Close)
		in.data, in.stream, in.closer = captured.Bytes, nil, nil
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
	inv.releases = append(inv.releases, captured.Close)
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

// failureStatus is the catalog's ordinary failure status: diff and cmp
// reserve 1 for differences, so their trouble is 2.
func (inv *invocation) failureStatus() int {
	if inv.name() == "diff" || inv.name() == "cmp" {
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
	return inv.owners.Pins.AppendRevisionPin(inv.ctx, execution, revision)
}

// admitPaths binds every PATH operand and its link target before the first is
// opened, so a refused operand stops the comparison before anything is pinned.
// Ordinary failures are left for the open to report.
func (inv *invocation) admitPaths(fields []commandprogram.Field) error {
	for _, field := range fields {
		if field.Value == "-" || field.ProcessSubstitution() != nil {
			continue
		}
		bound, err := inv.paths.Bind(inv.ctx, field.Value, false)
		if err == nil && !bound.Null {
			_, err = bound.Stat(true)
		}
		if err != nil {
			if _, ok := ordinary(projectpaths.PathOperationError(field.Value, err)); !ok {
				return err
			}
		}
	}
	return nil
}

// openHeld acquires one streamed operand that stays open beside later ones,
// as cmp reads two inputs in step. The invocation closes it when it ends.
func (inv *invocation) openHeld(field commandprogram.Field) (*input, error) {
	in, err := inv.openField(field, false)
	if inv.currentStream != nil {
		inv.closers = append(inv.closers, inv.currentStream)
		inv.currentStream = nil
	}
	return in, err
}
