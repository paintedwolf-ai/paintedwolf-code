package query

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/tools"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

// inputs yields every complete input value in operand order, decoding each
// file only when the query reaches it. It backs both the top-level loop and
// gojq's input/inputs builtins, so they share one position. A malformed
// input stops the stream after the values before it (jq's status 5); stop is
// a typed refusal (budget, authority) that ends the run.
type inputs struct {
	inv      *invocation
	operands commandprogram.Fields
	// raw yields each line as a string (-R) instead of JSON values; yaml
	// decodes each operand's YAML documents (gojq --yaml-input, python yq).
	raw       bool
	yaml      bool
	yamlDoc   int
	roundtrip *nativejq.YAMLRoundtrip
	// events turns each value into its --stream events; nil passes values.
	events        *nativejq.Program
	pendingEvents []any
	pending       []any
	stream        *nativejq.ValueStream
	rawReader     *bufio.Reader
	rawWork       *queryWorking
	rawOffset     int64
	// data holds the current operand's bytes read so far, for positions.
	data   *bytes.Buffer
	label  string
	limit  int64
	stop   error
	broken bool
	// openErrors records an operand that could not be read (jq's status 2).
	openErrors    bool
	positions     *inputPositions
	currentCloser io.Closer
	text          string
	captured      *source
}

// Next yields the next input, or with --stream the next event of it.
func (in *inputs) Next() (any, bool) {
	if in.events == nil {
		return in.nextValue()
	}
	for len(in.pendingEvents) == 0 {
		v, ok := in.nextValue()
		if !ok {
			return nil, false
		}
		if _, err := in.events.Run(in.inv.ctx, v, func(event any) error {
			in.pendingEvents = append(in.pendingEvents, event)
			return nil
		}); err != nil {
			in.stop = err
			return nil, false
		}
	}
	event := in.pendingEvents[0]
	in.pendingEvents = in.pendingEvents[1:]
	return event, true
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
				in.stream = nil
				continue
			case err != nil:
				in.malformed(err)
				return nil, false
			}
			if in.stream.InputOffset() > in.limit {
				in.stop = safecmd.Reject("JQ_INPUT_TOO_LARGE", map[string]any{"path": in.label, "max_bytes": in.limit, "bytes": in.stream.InputOffset()})
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

// open starts one operand: stdin streams; a file is captured and pinned.
func (in *inputs) open(operand commandprogram.Field) {
	in.close()
	in.label = operand.Value
	in.text = ""
	in.data = nil
	if !in.yaml {
		in.openStream(operand)
		return
	}
	in.data = &bytes.Buffer{}
	var data io.Reader
	if operand.Value == "-" {
		in.label = "<stdin>"
		data = io.TeeReader(io.LimitReader(in.inv.input, in.limit+1), in.data)
	} else {
		captured := in.captured
		var err error
		if captured == nil {
			captured, err = in.inv.capture(operand)
		}
		if err != nil {
			in.stop = err
			return
		}
		if captured == nil {
			in.openErrors = true
			return
		}
		in.data = nil
		in.text = captured.text
		data = strings.NewReader(captured.text)
	}

	text, err := in.readAll(data, operand.Value == "-")
	if err != nil {
		in.stop = err
		return
	}
	if in.raw {
		in.pending = append(in.pending, rawLines(text)...)
		return
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	values, _, err := nativejq.DecodeDocuments([]byte(text), "yaml")
	if err != nil {
		_, _ = fmt.Fprintf(in.inv.stderr, "%s: error: %s (while parsing %s)\n", in.inv.name(), err, in.label)
		in.broken = true
		return
	}
	for _, v := range values {
		if !in.admit(v) {
			return
		}
	}
	if in.inv.flag("-Y") {
		if in.roundtrip == nil {
			in.roundtrip = &nativejq.YAMLRoundtrip{}
		}
		in.roundtrip.Add(text)
	}
	in.pending = append(in.pending, values...)
}

// readAll reads one operand whole within the input bound.
func (in *inputs) readAll(data io.Reader, stdin bool) (string, error) {
	if !stdin {
		return in.text, nil
	}
	if _, err := io.Copy(io.Discard, data); err != nil {
		return "", err
	}
	if int64(in.data.Len()) > in.limit {
		return "", safecmd.Reject("JQ_INPUT_TOO_LARGE", map[string]any{"path": in.label, "max_bytes": in.limit})
	}
	return in.data.String(), nil
}

// rawLines is -R's input: each line without its newline; a final line
// without one still counts.
func rawLines(text string) []any {
	var out []any
	for text != "" {
		line, rest, found := strings.Cut(text, "\n")
		out = append(out, line)
		if !found {
			break
		}
		text = rest
	}
	return out
}

// malformed reports an input that is not JSON as jq does and stops reading.
func (in *inputs) malformed(err error) {
	offset := in.stream.InputOffset()
	message := err.Error()
	var syntax *json.SyntaxError
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF):
		message = "Unfinished JSON term at EOF"
		if in.positions != nil {
			offset = in.positions.read
		} else {
			offset = int64(in.data.Len())
		}
	case errors.As(err, &syntax):
		offset = syntax.Offset
	}
	var line, column int
	if in.positions != nil {
		line, column = in.positions.lineColumn(offset)
	} else {
		line, column = lineColumn(in.data.Bytes(), offset)
	}
	_, _ = fmt.Fprintf(in.inv.stderr, "jq: parse error: %s at line %d, column %d\n", message, line, column)
	in.broken = true
}

// position is jq's "(at FILE:LINE)" for the value last read: jq reads input
// a line at a time, so LINE counts every newline through the line where
// the value ended. In python yq, jq reads one JSON document per line from
// its stdin pipe, so positions are "<stdin>:DOC".
func (in *inputs) position() string {
	if in.label == "" {
		return "<unknown>"
	}
	if in.inv.pythonYq() {
		return fmt.Sprintf("<stdin>:%d", in.yamlDoc)
	}
	_, line := in.current()
	return fmt.Sprintf("%s:%d", in.label, line)
}

// current is input_filename and input_line_number: the file being read
// ("<stdin>" for stdin; null when no input has been read) and the lines
// read from it so far. In python yq, jq sees "<stdin>" and the 1-based YAML
// document index.
func (in *inputs) current() (any, int) {
	if in.label == "" {
		return nil, 0
	}
	if in.inv.pythonYq() {
		return "<stdin>", in.yamlDoc
	}
	end := int64(len(in.text))
	if in.positions != nil {
		end = in.positions.read
	} else if in.data != nil {
		end = int64(in.data.Len())
	}
	if in.stream != nil {
		end = in.stream.InputOffset()
	} else if in.rawReader != nil {
		end = in.rawOffset
	}
	var name any = in.label
	if in.label == "<stdin>" {
		name = nil
	}
	return name, in.lineAt(end)
}

func (in *inputs) lineAt(offset int64) int {
	if in.positions != nil {
		return in.positions.lineAt(offset)
	}
	if in.data == nil {
		offset = min(max(offset, 0), int64(len(in.text)))
		if i := strings.IndexByte(in.text[offset:], '\n'); i >= 0 {
			offset += int64(i) + 1
		} else {
			offset = int64(len(in.text))
		}
		return strings.Count(in.text[:offset], "\n")
	}
	data := in.data.Bytes()
	offset = min(max(offset, 0), int64(len(data)))
	if i := bytes.IndexByte(data[offset:], '\n'); i >= 0 {
		offset += int64(i) + 1
	} else {
		offset = int64(len(data))
	}
	return bytes.Count(data[:offset], []byte{'\n'})
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
