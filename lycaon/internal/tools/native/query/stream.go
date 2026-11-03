package query

import (
	"bufio"
	"bytes"
	"errors"
	"io"

	"github.com/itchyny/gojq"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/tools"
	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
)

type inputDecoder interface {
	Decode() (any, error)
	InputOffset() int64
	Position() (int, int)
	InputLine() int
	Retain() func()
	Close()
}

// inputs shares one cursor between top-level values and the input builtins.
type inputs struct {
	inv      *invocation
	operands commandprogram.Fields
	// raw yields each line as a string (-R) instead of JSON values; yaml
	// decodes each operand's YAML documents (gojq --yaml-input, python yq).
	raw       bool
	yaml      bool
	yamlDoc   int
	roundtrip *nativejq.YAMLRoundtrip
	// Raw and YAML values use the compiled event iterator.
	eventMode          bool
	eventProgram       *nativejq.Program
	eventIterator      gojq.Iter
	pending            []any
	stream             inputDecoder
	rawReader          *bufio.Reader
	rawWork            *queryWorking
	rawLines, lastLine int
	label              string
	limit              int64
	readBytes          int64
	stop               error
	broken             bool
	// openErrors records an operand that could not be read (jq's status 2).
	openErrors    bool
	currentCloser io.Closer
	text          string
	captured      *source
}

// Next yields the next input, or with --stream the next event of it.
func (in *inputs) Next() (any, bool) {
	if in.eventProgram == nil {
		return in.nextValue()
	}
	for {
		if in.eventIterator != nil {
			value, ok := in.eventIterator.Next()
			if ok {
				if err, failed := value.(error); failed {
					in.stop = err
					return nil, false
				}
				if err := in.inv.step(); err != nil {
					in.stop = err
					return nil, false
				}
				return value, true
			}
			in.eventIterator = nil
		}
		value, ok := in.nextValue()
		if !ok {
			return nil, false
		}
		in.eventIterator = in.eventProgram.Iterate(in.inv.ctx, value)
	}
}

func (in *inputs) nextValue() (any, bool) {
	for !in.broken && in.stop == nil {
		if len(in.pending) > 0 {
			v := in.pending[0]
			in.pending = in.pending[1:]
			if in.yaml {
				in.yamlDoc++
			}
			if err := in.inv.step(); err != nil {
				in.stop = err
				return nil, false
			}
			return v, true
		}
		if in.rawReader != nil {
			line, err := in.rawLine()
			if errors.Is(err, io.EOF) {
				in.rawReader = nil
				continue
			}
			if err != nil {
				in.stop = err
				return nil, false
			}
			if err := in.inv.step(); err != nil {
				in.stop = err
				return nil, false
			}
			return line, true
		}
		if in.stream != nil {
			v, err := in.stream.Decode()
			if in.stop != nil {
				return nil, false
			}
			switch {
			case errors.Is(err, io.EOF):
				in.lastLine = in.stream.InputLine()
				in.stream.Close()
				in.stream = nil
				continue
			case err != nil:
				in.parseFailure(err)
				return nil, false
			}

			if !in.admit(v) {
				return nil, false
			}
			if err := in.inv.step(); err != nil {
				in.stop = err
				return nil, false
			}
			return v, true
		}
		if len(in.operands) == 0 {
			return nil, false
		}
		operand := in.operands[0]
		in.operands = in.operands[1:]
		in.open(operand)
	}
	return nil, false
}

// lineColumn is jq's parse error location: 1-based line, 0-based column.
func lineColumn(data []byte, offset int64) (int, int) {
	offset = min(max(offset, 0), int64(len(data)))
	before := data[:offset]
	line := bytes.Count(before, []byte{'\n'}) + 1
	return line, len(before) - (bytes.LastIndexByte(before, '\n') + 1)
}

// admit bounds one input value's nesting by query_depth.
func (in *inputs) admit(v any) bool {
	if maximum := in.inv.owners.limit("query_depth"); int64(nativejq.ValueDepth(v)) > maximum {
		in.stop = tools.CommandLimitExceeded("query_depth", maximum, int64(nativejq.ValueDepth(v)))
		return false
	}
	return true
}
