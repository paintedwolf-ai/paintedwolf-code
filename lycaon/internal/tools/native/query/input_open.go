package query

import (
	"bufio"
	"bytes"
	"io"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

func (in *inputs) close() {
	if in.stream != nil {
		in.stream.Close()
		in.stream = nil
	}
	if in.rawWork != nil {
		in.rawWork.Close()
		in.rawWork = nil
	}
	if in.currentCloser != nil {
		_ = in.currentCloser.Close()
		in.currentCloser = nil
	}
}

// openStream binds the reached operand through native revision capture.
func (in *inputs) openStream(operand commandprogram.Field) {
	reader := in.inv.input
	if operand.Value == "-" {
		in.label = "<stdin>"
	} else if substReader, ok := operand.ProcessSubstitution().OpenReader(); ok {
		in.currentCloser = substReader
		reader = substReader
	} else {
		bound, err := in.inv.paths.Bind(in.inv.ctx, operand.Value, false)
		if err != nil {
			in.openFailure(operand.Value, err)
			return
		}
		if bound.Null {
			reader = bytes.NewReader(nil)
		} else {
			if tools.IsSensitivePath(bound.Resolved.DisplayPath) {
				in.stop = safecmd.Reject("JQ_PATH_DENIED", map[string]any{"path": bound.Resolved.DisplayPath})
				return
			}
			stream, err := in.inv.owners.Revisions.OpenTextStream(in.inv.ctx, in.inv.tc, bound.Resolved.Abs)
			if err != nil {
				in.openFailure(operand.Value, err)
				return
			}
			in.currentCloser = stream.Reader
			if err := in.inv.recordPin(stream.Revision); err != nil {
				in.stop = err
				return
			}
			reader = stream.Reader
		}
	}
	in.lastLine = 0
	var memory nativejq.EventMemory
	if in.inv.owners.Revisions.Memory != nil {
		memory = in.inv.owners.Revisions.Memory
	}
	ctx := toolcontract.WithMemoryPhase(in.inv.ctx, toolcontract.QueryDecoding)
	tracked := queryInputReader{input: reader, owner: in}
	if in.raw {
		in.rawReader = bufio.NewReader(tracked)
		in.rawLines = 0
	} else {
		if in.eventMode {
			in.stream = nativejq.NewEventStream(ctx, tracked, nativejq.EventLimits{MaximumDepth: int(in.inv.owners.limit("query_depth")), MaximumScalarBytes: in.inv.owners.limit("query_value_bytes")}, memory)
		} else {
			in.stream = nativejq.NewBoundedValueStream(ctx, tracked, nativejq.EventLimits{MaximumDepth: int(in.inv.owners.limit("query_depth")), MaximumScalarBytes: in.inv.owners.limit("query_value_bytes")}, memory, in.inv.owners.limit("query_value_bytes"))
		}
	}
}

func (in *inputs) openFailure(operand string, err error) {
	if reason, ok := ordinary(err); ok {
		in.inv.fail("Could not open file %s: %s", operand, reason)
		in.openErrors = true
		return
	}
	in.stop = err
}

// Reader failures remain terminal even when a complete value is buffered.
type queryInputReader struct {
	input io.Reader
	owner *inputs
}

func (r queryInputReader) Read(p []byte) (int, error) {
	n, err := r.input.Read(p)
	if n > 0 {
		r.owner.readBytes += int64(n)
		var failure error
		if r.owner.readBytes > r.owner.limit {
			failure = tools.CommandLimitExceeded("stream_throughput_bytes", r.owner.limit, r.owner.readBytes)
		}
		if failure != nil {
			r.owner.stop = failure
			return 0, failure
		}
	}
	if err != nil && err != io.EOF {
		r.owner.stop = err
	}
	return n, err
}
