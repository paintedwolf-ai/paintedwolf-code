package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/itchyny/gojq"

	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
)

// jq's exit statuses (jq 1.8 main.c): a run's status is its last input's.
const (
	jqOK         = 0
	jqNullKind   = -1 // -e: the last output was false or null
	jqNoOutput   = -4 // -e: the input produced no output
	jqUsage      = 2  // an operand file could not be read
	jqCompile    = 3
	jqRuntime    = 5 // a runtime error, or an input that is not JSON
	jqLastUnseen = -2
)

// jqRun is one jq invocation's options and its status bookkeeping.
type jqRun struct {
	inv     *invocation
	program *nativejq.Program
	stream  *inputs
	emitter *emitter
	ctx     context.Context
	// exitStatus is -e; ret and last follow jq's process/last_result.
	exitStatus bool
	ret, last  int
}

// jqDialect is what a jq-language command reads and prints by default: jq
// itself, or python yq (jq over YAML input, JSON output unless -y).
type jqDialect struct {
	yamlInput bool
	// pythonYAML prints -y output as PyYAML does: scalar documents end
	// with "...".
	pythonYAML bool
	// files replaces the operand files (python yq -i runs one at a time).
	files []string
}

// runJq compiles QUERY once and evaluates it over the input stream
// (-n: one null input, -s: one array of every input, -R: lines as strings).
func runJq(inv *invocation) error { return runJqLanguage(inv, jqDialect{}) }

// usageFailure is a jq command-line error: jq's message, its help pointer
// and status 2, as the utility reports it.
type usageFailure struct{ message string }

func (e *usageFailure) Error() string { return e.message }

func runJqLanguage(inv *invocation, dialect jqDialect) error {
	err := runJqProgram(inv, dialect)
	var usage *usageFailure
	if errors.As(err, &usage) {
		_, _ = fmt.Fprintf(inv.stderr, "jq: %s\nUse jq --help for help with command-line options,\nor see the jq manpage, or online docs  at https://jqlang.org\n", usage.message)
		inv.status = jqUsage
		return nil
	}
	return err
}

func runJqProgram(inv *invocation, dialect jqDialect) error {
	args, err := inv.jqArguments()
	if err != nil || args == nil {
		return err
	}
	if dialect.files != nil {
		args.files = dialect.files
	}
	layout, err := inv.layout()
	layout.pythonYAML = dialect.pythonYAML
	if err != nil {
		return err
	}
	stream := &inputs{inv: inv, operands: args.files, yaml: dialect.yamlInput || inv.flag("--yaml-input")}
	if inv.flag("--stream") {
		events, err := nativejq.CompileProgram("tostream", nativejq.ProgramOptions{})
		if err != nil {
			return err
		}
		stream.events = events
	}
	variables, err := inv.variables(stream, args.positional)
	if err != nil || stream.stop != nil {
		return errors.Join(err, stream.stop)
	}
	if stream.openErrors {
		inv.status = jqUsage
		return nil
	}
	program, err := nativejq.CompileProgram(args.query, nativejq.ProgramOptions{Variables: variables, Inputs: stream, Environ: inv.request.Environment, Stderr: inv.stderr, Position: stream.current})
	if err != nil {
		_, _ = fmt.Fprintf(inv.stderr, "jq: error: %s\njq: 1 compile error\n", err)
		inv.status = jqCompile
		return nil
	}
	// gojq has no VM step counter hook; intra-value evaluation work is bounded
	// by the query_cpu_ms deadline while host-observable input/output value
	// transitions are metered against query_work_steps.
	budget := time.Duration(inv.owners.limit("query_cpu_ms")) * time.Millisecond
	ctx, cancel := context.WithTimeout(inv.ctx, budget)
	defer cancel()
	run := &jqRun{inv: inv, program: program, stream: stream, ctx: ctx, exitStatus: inv.flag("-e"), ret: jqOK, last: jqLastUnseen,
		emitter: &emitter{inv: inv, stream: stream, layout: layout, maxOutputs: inv.owners.limit("query_outputs"), maxValue: inv.owners.limit("query_value_bytes")}}
	if err := run.evaluateAll(); err != nil {
		return err
	}
	if stream.stop != nil {
		return stream.stop
	}
	if err := run.emitter.finish(); err != nil {
		return err
	}
	inv.status = run.status()
	return nil
}

// evaluateAll runs the program once per input, or once for -n and -s.
func (r *jqRun) evaluateAll() error {
	switch {
	case r.inv.flag("-n"):
		_, err := r.evaluate(nil, false)
		return err
	case r.inv.flag("-s") && r.inv.flag("-R"):
		text, ok := r.stream.slurpRaw()
		if !ok {
			return nil
		}
		_, err := r.evaluate(text, true)
		return err
	case r.inv.flag("-s"):
		all := []any{}
		for v, ok := r.stream.Next(); ok; v, ok = r.stream.Next() {
			all = append(all, v)
		}
		if r.stream.stop != nil {
			return r.stream.stop
		}
		if r.stream.broken {
			return nil
		}
		_, err := r.evaluate(all, true)
		return err
	}
	// jq stops reading once an operand could not be opened, after
	// evaluating the value that read returned.
	for v, ok := r.stream.Next(); ok; v, ok = r.stream.Next() {
		more, err := r.evaluate(v, true)
		if err != nil || !more || r.stream.openErrors {
			return err
		}
	}
	return nil
}

// evaluate runs one input and records jq's per-input result. It reports
// false when a halt ends the run.
func (r *jqRun) evaluate(input any, positioned bool) (bool, error) {
	r.emitter.produced = false
	halt, err := r.program.Run(r.ctx, input, r.emitter.emit)
	var queryErr *nativejq.QueryError
	switch {
	case errors.As(err, &queryErr):
		r.reportError(queryErr.Err, positioned)
		r.ret = jqRuntime
		return true, nil
	case errors.Is(err, context.DeadlineExceeded) && r.inv.ctx.Err() == nil:
		maximum := r.inv.owners.limit("query_cpu_ms")
		return false, limitExceeded("query_cpu_ms", maximum, maximum+1)
	case err != nil:
		return false, err
	case halt != nil:
		r.ret = halt.Code
		return false, r.inv.halt(halt)
	}
	r.ret = jqOK
	if r.exitStatus {
		r.ret = jqNoOutput
		if r.emitter.produced {
			r.ret = jqOK
			if r.emitter.last == nil || r.emitter.last == false {
				r.ret = jqNullKind
			}
		}
	}
	if r.ret <= 0 && r.ret != jqNoOutput {
		r.last = 1
		if r.ret == jqNullKind {
			r.last = 0
		}
	}
	return true, nil
}

// status applies jq's exit rules: an unreadable operand or malformed input
// wins, -e reports the last output, and otherwise errors are 5.
func (r *jqRun) status() int {
	ret := r.ret
	if r.stream.broken {
		ret = jqRuntime
	}
	if r.stream.openErrors {
		ret = jqUsage
	}
	if !r.exitStatus {
		return max(ret, 0)
	}
	if ret != jqNoOutput {
		return abs(ret)
	}
	switch r.last {
	case jqLastUnseen:
		return 4
	case 0:
		return 1
	}
	return 0
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// reportError writes jq's runtime error line for one input.
func (r *jqRun) reportError(err error, positioned bool) {
	where := "<unknown>"
	if positioned {
		where = r.stream.position()
	}
	var valueErr gojq.ValueError
	if errors.As(err, &valueErr) {
		if s, ok := valueErr.Value().(string); ok {
			_, _ = fmt.Fprintf(r.inv.stderr, "jq: error (at %s): %s\n", where, s)
			return
		}
		raw, _ := nativejq.EncodeJSON(valueErr.Value(), false)
		_, _ = fmt.Fprintf(r.inv.stderr, "jq: error (at %s) (not a string): %s\n", where, raw)
		return
	}
	_, _ = fmt.Fprintf(r.inv.stderr, "jq: error (at %s): %s\n", where, err)
}

// jqArguments is jq's operand rule: the first operand is the filter unless
// -f names a filter file; operands after --args or --jsonargs are
// positional values; the rest are input files (stdin when none). With no
// filter jq runs ".".
type jqArgs struct {
	query      string
	files      []string
	positional []any
}

func (inv *invocation) jqArguments() (*jqArgs, error) {
	args := &jqArgs{}
	haveQuery := false
	if file, ok := inv.value("-f"); ok {
		captured, err := inv.captureSource(file)
		var failure *unreadable
		if errors.As(err, &failure) {
			_, _ = fmt.Fprintf(inv.stderr, "jq: Could not open %s: %s\n", file, failure.reason)
			inv.status = jqUsage
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		args.query, haveQuery = captured.text, true
	}
	positional := ""
	for _, item := range inv.argv.Items {
		if item.Option != nil {
			if item.Option.Key == "--args" || item.Option.Key == "--jsonargs" {
				positional = item.Option.Key
			}
			continue
		}
		word := item.Operand.Value
		switch {
		case !haveQuery:
			args.query, haveQuery = word, true
		case positional == "--args":
			args.positional = append(args.positional, word)
		case positional == "--jsonargs":
			decoded, _, err := nativejq.DecodeDocuments([]byte(word), "json")
			if err != nil || len(decoded) != 1 {
				return nil, &usageFailure{"Invalid JSON text passed to --jsonargs"}
			}
			args.positional = append(args.positional, decoded[0])
		default:
			args.files = append(args.files, word)
		}
	}
	if !haveQuery {
		args.query = "."
	}
	if len(args.files) == 0 {
		args.files = []string{"-"}
	}
	return args, nil
}

// layout reads the output options in argv order: the last of -c, --tab and
// --indent sets the indentation, as jq applies them.
func (inv *invocation) layout() (outputLayout, error) {
	layout := outputLayout{pretty: true, indent: "  ", raw: inv.flag("-r"), join: inv.flag("-j"), ascii: inv.flag("-a"), sortKeys: inv.flag("-S"),
		nul: inv.flag("--raw-output0"), yaml: inv.flag("--yaml-output") || inv.flag("-y") || inv.flag("-Y")}
	for _, item := range inv.argv.Items {
		option := item.Option
		if option == nil {
			continue
		}
		switch option.Key {
		case "-c":
			layout.pretty = false
		case "--tab":
			layout.pretty, layout.indent = true, "\t"
		case "--indent", "-I":
			n, err := strconv.Atoi(option.Values[0].Value)
			if err != nil || n < -1 || n > 7 {
				return outputLayout{}, &usageFailure{"--indent takes a number between -1 and 7"}
			}
			layout.pretty, layout.indent = n >= 0, strings.Repeat(" ", max(n, 0))
		}
	}
	return layout, nil
}

// variables reads --arg (string), --argjson (JSON), --slurpfile (a file's
// values as an array) and --rawfile (a file's text) in argv order; the first
// definition of a name wins, as in jq.
// $ARGS presents the positional values and every named one.
func (inv *invocation) variables(stream *inputs, positional []any) (out []nativejq.Variable, err error) {
	index := map[string]int{}
	named := gojq.NewObject(0)
	defer func() {
		if positional == nil {
			positional = []any{}
		}
		if err == nil {
			out = append(out, nativejq.Variable{Name: "ARGS", Value: gojq.ObjectOf("positional", positional, "named", named)})
		}
	}()
	for _, item := range inv.argv.Items {
		option := item.Option
		if option == nil || len(option.Values) != 2 {
			continue
		}
		name, text := option.Values[0].Value, option.Values[1].Value
		var value any
		switch option.Key {
		case "--arg":
			value = text
		case "--argjson":
			decoded, _, err := nativejq.DecodeDocuments([]byte(text), "json")
			if err != nil || len(decoded) != 1 {
				return nil, &usageFailure{"invalid JSON text passed to --argjson"}
			}
			value = decoded[0]
		case "--slurpfile", "--rawfile":
			captured, err := inv.captureSource(text)
			var failure *unreadable
			if errors.As(err, &failure) {
				_, _ = fmt.Fprintf(inv.stderr, "jq: Bad JSON in %s %s %s: Could not open %s: %s\n", option.Key, name, text, text, failure.reason)
				stream.openErrors = true
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			value = captured.text
			if option.Key == "--slurpfile" {
				values := []any{}
				if strings.TrimSpace(captured.text) != "" {
					decoded, _, err := nativejq.DecodeDocuments([]byte(captured.text), "json")
					if err != nil {
						_, _ = fmt.Fprintf(inv.stderr, "jq: Bad JSON in --slurpfile %s %s: %s\n", name, text, parseMessage(err, []byte(captured.text)))
						stream.openErrors = true
						return nil, nil
					}
					values = decoded
				}
				value = values
			}
		default:
			continue
		}
		if _, ok := index[name]; ok {
			continue
		}
		index[name] = len(out)
		named.Set(name, gojq.FromGo(value))
		out = append(out, nativejq.Variable{Name: name, Value: value})
	}
	return out, nil
}

// halt applies halt and halt_error: the exit code, and a reported value on
// stderr (a string raw, anything else as JSON with a newline).
func (inv *invocation) halt(h *nativejq.Halt) error {
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

// outputLayout is jq's output encoding options.
type outputLayout struct {
	pretty                     bool
	indent                     string
	raw, join, ascii, sortKeys bool
	// nul is --raw-output0; yaml is gojq --yaml-output and python yq -y.
	nul, yaml, pythonYAML bool
}

// emitter writes each result in jq's encoding within the output budgets.
type emitter struct {
	inv                  *invocation
	stream               *inputs
	layout               outputLayout
	count                int64
	produced, openEnded  bool
	last                 any
	maxOutputs, maxValue int64
}

func (e *emitter) emit(v any) error {
	if err := e.inv.step(); err != nil {
		return err
	}
	e.count++
	if e.maxOutputs > 0 && e.count > e.maxOutputs {
		return limitExceeded("query_outputs", e.maxOutputs, e.count)
	}
	e.produced, e.last = true, v
	encoded, err := e.encode(v)
	if err != nil {
		return err
	}
	if e.maxValue > 0 && int64(len(encoded)) > e.maxValue {
		return limitExceeded("query_value_bytes", e.maxValue, int64(len(encoded)))
	}
	_, err = e.inv.out.Write(encoded)
	return err
}

// finish writes PyYAML's deferred scalar document-end marker ("...") when
// the final emitted document was an unquoted scalar; preceding scalars are
// closed by the next document's "---".
func (e *emitter) finish() error {
	if !e.openEnded {
		return nil
	}
	_, err := e.inv.out.Write([]byte("...\n"))
	return err
}

// encode is jq's printer: -r and -j print strings raw unless -a asks for
// escapes, -j omits the newline, and values are JSON in the chosen layout.
func (e *emitter) encode(v any) ([]byte, error) {
	newline := "\n"
	switch {
	case e.layout.nul:
		newline = "\x00"
	case e.layout.join:
		newline = ""
	}
	if e.layout.yaml {
		var (
			out []byte
			err error
		)
		if e.stream != nil && e.stream.roundtrip != nil {
			out, err = e.stream.roundtrip.EncodeDoc(e.stream.yamlDoc-1, v)
		} else {
			out, err = nativejq.EncodeYAML(v)
		}
		if err != nil {
			return nil, err
		}
		_, array := v.([]any)
		_, object := v.(*gojq.OrderedObject)
		e.openEnded = e.layout.pythonYAML && !array && !object && len(out) > 0 && out[0] != '\'' && out[0] != '"'
		if e.count > 1 {
			out = append([]byte("---\n"), out...)
		}
		return out, nil
	}
	if s, ok := v.(string); ok && (e.layout.raw || e.layout.join || e.layout.nul) && !e.layout.ascii {
		return []byte(s + newline), nil
	}
	out, err := gojq.MarshalWith(v, gojq.MarshalOptions{Pretty: e.layout.pretty, Indent: e.layout.indent, SortKeys: e.layout.sortKeys, ASCII: e.layout.ascii})
	if err != nil {
		return nil, err
	}
	return append(out, newline...), nil
}

// parseMessage words a JSON syntax error as jq's parser does where the two
// agree on the cause, with jq's line and column.
func parseMessage(err error, data []byte) string {
	offset := int64(len(data))
	message := err.Error()
	var syntax *json.SyntaxError
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF):
		message = "Unfinished JSON term at EOF"
	case errors.As(err, &syntax):
		offset = syntax.Offset
	}
	line, column := lineColumn(data, offset)
	return fmt.Sprintf("%s at line %d, column %d", message, line, column)
}
