package query

import (
	"bufio"
	"bytes"
	"io"

	"github.com/lycaon/lycaon/internal/tools"
	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

func (in *inputs) close() {
	if in.rawWork != nil {
		_ = in.rawWork.Close()
		in.rawWork = nil
	}
	if in.currentCloser != nil {
		_ = in.currentCloser.Close()
		in.currentCloser = nil
	}
	if in.positions != nil {
		in.positions.close()
		in.positions = nil
	}
}

// openStream binds exactly the reached operand and delegates its physical read,
// revision pin and representation to the existing native capture owner.
func (in *inputs) openStream(operand string) {
	var reader io.Reader = in.inv.input
	if operand == "-" {
		in.label = "<stdin>"
	} else {
		bound, err := in.inv.paths.Bind(in.inv.ctx, operand, false)
		if err != nil {
			in.openFailure(operand, err)
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
				in.openFailure(operand, err)
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
	positions, err := newInputPositions(in.inv, in.limit, in.label)
	if err != nil {
		in.stop = err
		return
	}
	in.positions = positions
	tracked := queryInputReader{input: reader, owner: in}
	if in.raw {
		in.rawReader = bufio.NewReader(tracked)
		in.rawOffset = 0
	} else {
		in.stream = nativejq.NewValueStream(tracked)
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

// A source/limit failure remains terminal even if a decoder has already read a
// complete value before surfacing that reader error on its next Decode.
type queryInputReader struct {
	input io.Reader
	owner *inputs
}

func (r queryInputReader) Read(p []byte) (int, error) {
	n, err := r.input.Read(p)
	if n > 0 {
		if _, failure := r.owner.positions.Write(p[:n]); failure != nil {
			r.owner.stop = failure
			return 0, failure
		}
	}
	if err != nil && err != io.EOF {
		r.owner.stop = err
	}
	return n, err
}
