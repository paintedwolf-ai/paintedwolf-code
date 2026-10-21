package textutil

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// invocation is one utility run: bound argv, metered output and status.
type invocation struct {
	ctx      context.Context
	owners   *Owners
	ports    Ports
	request  commandprogram.OperationRequest
	spec     toolcontract.CommandSpecification
	argv     commandprogram.BoundArgv
	input    io.Reader
	stderr   io.Writer
	out      *nativeutility.Output
	errors   strings.Builder
	status   int
	releases []func()
}

func newInvocation(ctx context.Context, p Prepared, ports Ports, streams commandprogram.Streams) *invocation {
	inv := &invocation{ctx: ctx, owners: p.owners, ports: ports, request: p.request, spec: p.spec, argv: p.Argv, input: streams.Input}
	if inv.input == nil {
		inv.input = commandprogram.NullInput()
	}
	inv.stderr = streams.Error
	if inv.stderr == nil {
		inv.stderr = io.Discard
	}
	inv.out = nativeutility.NewOutput(ctx, streams.Output, p.owners.limits["stream_bytes"])
	return inv
}

func (inv *invocation) flag(key string) bool {
	_, ok := inv.argv.Last(key)
	return ok
}

func (inv *invocation) value(key string) (commandprogram.Field, bool) {
	option, ok := inv.argv.Last(key)
	if !ok || len(option.Values) == 0 {
		return commandprogram.Field{}, false
	}
	return option.Values[0], true
}

func (inv *invocation) operands() commandprogram.Fields { return inv.argv.Operands() }

func (inv *invocation) limit(id string) int64 { return inv.owners.limits[id] }

func (inv *invocation) usage(word commandprogram.Field, expected string) error {
	return nativeutility.Usage(inv.spec, word, expected)
}

func unsupported(inv *invocation, word commandprogram.Field, expected string) error {
	return nativeutility.Unsupported(inv.spec, word, expected)
}

// commandWord locates refusals that concern the whole invocation.
func (inv *invocation) commandWord() commandprogram.Field { return inv.request.Argv[0] }

// reserve charges working storage to the program before it is allocated.
func (inv *invocation) reserve(n int64) error {
	release, err := inv.ports.Memory.Reserve(inv.ctx, n)
	if err != nil {
		return err
	}
	inv.releases = append(inv.releases, release)
	return nil
}

func (inv *invocation) close() {
	for i := len(inv.releases) - 1; i >= 0; i-- {
		inv.releases[i]()
	}
	inv.releases = nil
}

// fail records an ordinary per-operand failure on stderr; the run continues.
func (inv *invocation) fail(status int, operand, reason string) {
	fmt.Fprintf(&inv.errors, "%s: %s: %s\n", inv.spec.Name, operand, reason)
	inv.status = max(inv.status, status)
}

// source is one input: standard input or a PATH operand opened through the
// invocation's path authority.
type source struct {
	name   string
	reader io.ReadCloser
}

// openInputs opens each PATH operand ("-" is standard input), or standard
// input when there are none. A missing or unreadable file is an ordinary
// failure reported on stderr; an authority refusal stops the run.
func (inv *invocation) openInputs(operands commandprogram.Fields) ([]source, error) {
	if len(operands) == 0 {
		return []source{{name: "-", reader: io.NopCloser(inv.input)}}, nil
	}
	var paths *projectpaths.ProgramPaths
	var out []source
	for _, operand := range operands {
		if operand.Value == "-" {
			out = append(out, source{name: "-", reader: io.NopCloser(inv.input)})
			continue
		}
		if paths == nil {
			var err error
			paths, err = projectpaths.NewProgramPaths(inv.ctx, inv.owners.Boundary, inv.ports.ToolContext, inv.request.Invocation.Scope.Directory)
			if err != nil {
				closeSources(out)
				return nil, err
			}
		}
		bound, err := paths.Bind(inv.ctx, operand.Value, false)
		var reader io.ReadCloser
		if err == nil {
			reader, err = bound.OpenReader()
		}
		if err != nil {
			if reason, ok := ordinary(err); ok {
				inv.fail(1, operand.Value, reason)
				continue
			}
			closeSources(out)
			return nil, pathRejection(err, operand)
		}
		out = append(out, source{name: operand.Value, reader: reader})
	}
	return out, nil
}

// pathRejection projects a path owner's typed refusal with the operand span.
func pathRejection(err error, operand commandprogram.Field) error {
	var pathErr *projectpaths.PathError
	if !errors.As(err, &pathErr) || toolrejection.AsToolReject(err) != nil {
		return err
	}
	return errors.Join(err, toolrejection.RejectInvalidArguments(pathErr.Code, map[string]any{
		"path": pathErr.Operand, "limit_id": pathErr.LimitID, "maximum": pathErr.Maximum, "observed": pathErr.Observed,
		"source_start_byte": operand.Span.StartByte, "source_end_byte": operand.Span.EndByte, "source_line": operand.Span.Line, "source_column": operand.Span.Column,
	}))
}

func closeSources(sources []source) {
	for _, s := range sources {
		_ = s.reader.Close()
	}
}

// ordinary classifies an input failure the utility reports and survives.
func ordinary(err error) (string, bool) {
	if toolrejection.AsToolReject(err) != nil {
		return "", false
	}
	var pathErr *projectpaths.PathError
	if errors.As(err, &pathErr) {
		return "", false
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "No such file or directory", true
	case errors.Is(err, fs.ErrPermission):
		return "Permission denied", true
	}
	if errors.Is(err, syscall.EISDIR) {
		return "Is a directory", true
	}
	return "", false
}

// records reads delimiter-terminated records under record_bytes; a final
// record without its delimiter is still a record. The returned slice is
// valid until the next call.
type records struct {
	inv       *invocation
	reader    *bufio.Reader
	delimiter byte
	buffer    []byte
	maximum   int64
}

func (inv *invocation) records(r io.Reader, delimiter byte) (*records, error) {
	if err := inv.reserve(64 << 10); err != nil {
		return nil, err
	}
	return &records{inv: inv, reader: bufio.NewReaderSize(r, 64<<10), delimiter: delimiter, maximum: inv.limit("record_bytes")}, nil
}

// next returns the record without its delimiter, or io.EOF.
func (r *records) next() ([]byte, error) {
	r.buffer = r.buffer[:0]
	for {
		if err := r.inv.ctx.Err(); err != nil {
			return nil, err
		}
		chunk, err := r.reader.ReadSlice(r.delimiter)
		if int64(len(r.buffer)+len(chunk)) > r.maximum+1 {
			return nil, nativeutility.LimitExceeded("record_bytes", r.maximum, int64(len(r.buffer)+len(chunk)))
		}
		if cap(r.buffer) < len(r.buffer)+len(chunk) {
			if reserve := r.inv.reserve(int64(2 * (len(r.buffer) + len(chunk)))); reserve != nil {
				return nil, reserve
			}
		}
		r.buffer = append(r.buffer, chunk...)
		switch {
		case err == nil:
			record := r.buffer[:len(r.buffer)-1]
			if int64(len(record)) > r.maximum {
				return nil, nativeutility.LimitExceeded("record_bytes", r.maximum, int64(len(record)))
			}
			return record, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(r.buffer) == 0 {
				return nil, io.EOF
			}
			if int64(len(r.buffer)) > r.maximum {
				return nil, nativeutility.LimitExceeded("record_bytes", r.maximum, int64(len(r.buffer)))
			}
			return r.buffer, nil
		default:
			return nil, err
		}
	}
}

// writeRecord emits a record with its delimiter.
func (inv *invocation) writeRecord(record []byte, delimiter byte) error {
	if _, err := inv.out.Write(record); err != nil {
		return err
	}
	_, err := inv.out.Write([]byte{delimiter})
	return err
}
