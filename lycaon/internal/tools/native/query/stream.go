package query

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"time"

	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

// inputs yields every complete input value in operand order, decoding each
// file only when the query reaches it. It backs both the top-level loop and
// gojq's input/inputs builtins, so they share one position. A malformed
// input stops the stream after the values before it; stop is a typed refusal
// (budget, authority) that ends the run.
type inputs struct {
	inv      *invocation
	operands []string
	yaml     bool
	pending  []any
	stream   *nativejq.ValueStream
	label    string
	limit    int64
	stop     error
	broken   bool
}

func (in *inputs) Next() (any, bool) {
	for !in.broken && in.stop == nil {
		if len(in.pending) > 0 {
			v := in.pending[0]
			in.pending = in.pending[1:]
			return v, true
		}
		if in.stream != nil {
			v, err := in.stream.Decode()
			switch {
			case errors.Is(err, io.EOF):
				in.stream = nil
				continue
			case err != nil:
				in.malformed(err)
				return nil, false
			}
			if in.stream.InputOffset() > in.limit {
				in.stop = safecmd.Reject("JQ_INPUT_TOO_LARGE", map[string]any{"path": in.label, "max_bytes": in.limit})
				return nil, false
			}
			if !in.admit(v) {
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
func (in *inputs) open(operand string) {
	in.label = operand
	var data io.Reader
	format := "json"
	if operand == "-" {
		in.label = "<stdin>"
		data = io.LimitReader(in.inv.input, in.limit+1)
		if in.yaml {
			format = "yaml"
		}
	} else {
		captured, err := in.inv.capture(operand)
		if err != nil {
			in.stop = err
			return
		}
		if captured == nil {
			return
		}
		data = strings.NewReader(captured.text)
		if in.yaml {
			if format, err = nativejq.DetectFormat(operand, ""); err != nil || format == "auto" {
				format = "yaml"
			}
		}
	}
	if format == "json" {
		in.stream = nativejq.NewValueStream(data)
		return
	}
	raw, err := io.ReadAll(data)
	if err != nil {
		in.stop = err
		return
	}
	if int64(len(raw)) > in.limit {
		in.stop = safecmd.Reject("JQ_INPUT_TOO_LARGE", map[string]any{"path": in.label, "max_bytes": in.limit})
		return
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return
	}
	values, _, err := nativejq.DecodeDocuments(raw, format)
	if err != nil {
		in.malformed(err)
		return
	}
	for _, v := range values {
		if !in.admit(v) {
			return
		}
	}
	in.pending = values
}

func (in *inputs) malformed(err error) {
	in.inv.fail("%s (while parsing %s)", err, in.label)
	in.broken = true
}

// admit bounds one input value's nesting by query_depth.
func (in *inputs) admit(v any) bool {
	if maximum := in.inv.owners.limit("query_depth"); int64(nativejq.ValueDepth(v)) > maximum {
		in.stop = limitExceeded("query_depth", maximum, int64(nativejq.ValueDepth(v)))
		return false
	}
	return true
}

func runJq(inv *invocation) error { return runQuery(inv, false) }
func runYq(inv *invocation) error { return runQuery(inv, true) }

// runQuery compiles QUERY once and evaluates it over the input stream
// (-n: one null input, -s: one array of every input).
func runQuery(inv *invocation, yaml bool) error {
	operands := inv.argv.Operands().Values()
	if len(operands) == 0 {
		return inv.usage(inv.name(), "QUERY")
	}
	query, paths := operands[0], operands[1:]
	if len(paths) == 0 {
		paths = []string{"-"}
	}
	variables, err := inv.variables()
	if err != nil {
		return err
	}
	stream := &inputs{inv: inv, operands: paths, yaml: yaml, limit: safecmd.JQCaps().InputBytes}
	program, err := nativejq.CompileProgram(query, variables, stream)
	if err != nil {
		return safecmd.Reject("JQ_QUERY_INVALID", map[string]any{"query": query, "detail": err.Error()})
	}
	budget := time.Duration(inv.owners.limit("query_cpu_ms")) * time.Millisecond
	ctx, cancel := context.WithTimeout(inv.ctx, budget)
	defer cancel()
	emitter := &emitter{inv: inv, yaml: yaml, raw: inv.flag("-r"), compact: inv.flag("-c"), maxOutputs: inv.owners.limit("query_outputs"), maxValue: inv.owners.limit("query_value_bytes")}
	evaluate := func(input any) (bool, error) {
		halt, err := program.Run(ctx, input, emitter.emit)
		var queryErr *nativejq.QueryError
		switch {
		case errors.As(err, &queryErr):
			inv.fail("%s", queryErr.Err)
			return true, nil
		case errors.Is(err, context.DeadlineExceeded) && inv.ctx.Err() == nil:
			return false, limitExceeded("query_cpu_ms", inv.owners.limit("query_cpu_ms"), inv.owners.limit("query_cpu_ms")+1)
		case err != nil:
			return false, err
		case halt != nil:
			return false, inv.halt(halt)
		}
		return true, nil
	}
	switch {
	case inv.flag("-n"):
		if _, err := evaluate(nil); err != nil {
			return err
		}
	case inv.flag("-s"):
		all := []any{}
		for v, ok := stream.Next(); ok; v, ok = stream.Next() {
			all = append(all, v)
		}
		if stream.stop != nil {
			return stream.stop
		}
		if stream.broken {
			return nil
		}
		if _, err := evaluate(all); err != nil {
			return err
		}
	default:
		for v, ok := stream.Next(); ok; v, ok = stream.Next() {
			more, err := evaluate(v)
			if err != nil || !more {
				return err
			}
		}
	}
	if stream.stop != nil {
		return stream.stop
	}
	if inv.flag("-e") && inv.status == 0 {
		switch {
		case emitter.count == 0:
			inv.status = 4
		case emitter.last == nil || emitter.last == false:
			inv.status = 1
		}
	}
	return nil
}

// variables reads --arg (string) and --argjson (JSON) in argv order; a later
// definition of a name replaces the earlier one.
func (inv *invocation) variables() ([]nativejq.Variable, error) {
	var out []nativejq.Variable
	index := map[string]int{}
	for _, item := range inv.argv.Items {
		option := item.Option
		if option == nil || (option.Key != "--arg" && option.Key != "--argjson") || len(option.Values) != 2 {
			continue
		}
		name := option.Values[0].Value
		var value any = option.Values[1].Value
		if option.Key == "--argjson" {
			decoded, _, err := nativejq.DecodeDocuments([]byte(option.Values[1].Value), "json")
			if err != nil || len(decoded) != 1 {
				return nil, inv.usage(option.Values[1].Value, "one JSON value for --argjson "+name)
			}
			value = decoded[0]
		}
		if i, ok := index[name]; ok {
			out[i].Value = value
			continue
		}
		index[name] = len(out)
		out = append(out, nativejq.Variable{Name: name, Value: value})
	}
	return out, nil
}

// halt applies halt and halt_error: the exit code, and a reported value on
// stderr (a string raw, anything else as JSON with a newline).
func (inv *invocation) halt(h *nativejq.Halt) error {
	inv.status = h.Code
	if !h.Message {
		return nil
	}
	if s, ok := h.Value.(string); ok {
		_, err := io.WriteString(inv.stderr, s)
		return err
	}
	raw, err := nativejq.EncodeJSON(h.Value, false)
	if err != nil {
		return err
	}
	_, err = inv.stderr.Write(append(raw, '\n'))
	return err
}

// emitter writes each result in the catalogued encoding.
type emitter struct {
	inv                  *invocation
	yaml, raw, compact   bool
	count                int64
	last                 any
	maxOutputs, maxValue int64
	previousCollection   bool
}

func (e *emitter) emit(v any) error {
	e.count++
	if e.maxOutputs > 0 && e.count > e.maxOutputs {
		return limitExceeded("query_outputs", e.maxOutputs, e.count)
	}
	e.last = v
	encoded, collection, err := e.encode(v)
	if err != nil {
		return err
	}
	if e.maxValue > 0 && int64(len(encoded)) > e.maxValue {
		return limitExceeded("query_value_bytes", e.maxValue, int64(len(encoded)))
	}
	if e.yaml && !e.compact && e.count > 1 && (collection || e.previousCollection) {
		if _, err := io.WriteString(e.inv.out, "---\n"); err != nil {
			return err
		}
	}
	e.previousCollection = collection
	_, err = e.inv.out.Write(encoded)
	return err
}

// encode is -r raw strings, -c compact JSON, else jq's indented JSON or, for
// yq, a plain YAML document. None of these preserve source formatting.
func (e *emitter) encode(v any) ([]byte, bool, error) {
	_, isArray := v.([]any)
	_, isObject := v.(map[string]any)
	collection := isArray || isObject
	if s, ok := v.(string); ok && e.raw {
		return []byte(s + "\n"), false, nil
	}
	if e.yaml && !e.compact {
		out, err := nativejq.EncodeYAML(v)
		return out, collection, err
	}
	out, err := nativejq.EncodeJSON(v, !e.compact)
	if err != nil {
		return nil, false, err
	}
	return append(out, '\n'), collection, nil
}
