package textutil

import (
	"errors"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"io"
	"io/fs"
	"strings"
	"syscall"
)

// source is one input: standard input or a PATH operand opened through the
// invocation's path authority.
type source struct {
	name   string
	reader io.ReadCloser
}

// openInputs describes operands without opening them. A stream acquires its
// pinned input on first use and releases it at EOF or explicit close.
func (inv *invocation) openInputs(operands commandprogram.Fields) ([]source, error) {
	if len(operands) == 0 {
		return []source{{name: "-", reader: io.NopCloser(inv.streams.Input)}}, nil
	}
	if err := inv.reserve(int64(len(operands)) * 128); err != nil {
		return nil, err
	}
	var paths *projectpaths.ProgramPaths
	out := make([]source, 0, len(operands))
	for _, operand := range operands {
		reader := io.NopCloser(inv.streams.Input)
		if substReader, ok := operand.ProcessSubstitution().OpenReader(); ok {
			reader = substReader
		} else if operand.Value != "-" {
			if paths == nil {
				var err error
				paths, err = projectpaths.NewProgramPaths(inv.ctx, inv.owners.Revisions.Boundary, inv.ports.ToolContext, inv.request.Invocation.Scope.Directory)
				if err != nil {
					closeSources(out)
					return nil, err
				}
			}
			reader = &operandReader{inv: inv, paths: paths, operand: operand}
		}
		out = append(out, source{name: operand.Value, reader: reader})
	}
	return out, nil
}

// operandReader keeps deferred opening inside the same capture authority.
type operandReader struct {
	inv     *invocation
	paths   *projectpaths.ProgramPaths
	operand commandprogram.Field
	reader  io.ReadCloser
	pin     *toolcontract.SourcePin
	closed  bool
}

func (r *operandReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, io.EOF
	}
	if r.reader == nil {
		reader, revision, err := r.inv.capture(r.paths, r.operand)
		if err != nil {
			r.closed = true
			if reason, ok := ordinary(err); ok {
				status := 1
				if r.inv.spec.Name == "sort" || r.inv.spec.Name == "sed" {
					status = 2
				}
				r.inv.fail(status, r.operand.Value, reason)
				return 0, io.EOF
			}
			return 0, pathRejection(err, r.operand)
		}
		if revision != nil {
			release, err := r.inv.ports.Memory.ReserveProgramState(r.inv.ctx, int64(512+len(revision.RootID)+len(revision.Path)+len(revision.DocumentID)+len(revision.DiskSHA256)+len(revision.ContentSHA256)))
			if err != nil {
				return 0, errors.Join(err, reader.Close())
			}
			r.inv.releases = append(r.inv.releases, release)
			if pin, ok := revision.SourcePin(); ok {
				r.pin = &pin
			}
		}
		r.reader = reader
	}
	n, err := r.reader.Read(p)
	if errors.Is(err, io.EOF) {
		_ = r.Close()
	}
	return n, err
}
func (r *operandReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.reader != nil {
		return r.reader.Close()
	}
	return nil
}

// capture binds one operand under the invocation cwd, pins its accepted
// revision and returns a reader over its bytes.
func (inv *invocation) capture(paths *projectpaths.ProgramPaths, operand commandprogram.Field) (io.ReadCloser, *toolcontract.ExecutionRevision, error) {
	if reader, ok := operand.ProcessSubstitution().OpenReader(); ok {
		return reader, nil, nil
	}
	bound, err := paths.Bind(inv.ctx, operand.Value, false)
	if err != nil {
		return nil, nil, err
	}
	if bound.Null {
		return io.NopCloser(strings.NewReader("")), nil, nil
	}
	tc := inv.ports.ToolContext
	if inv.preparing {
		captured, err := inv.owners.Revisions.CaptureSource(inv.ctx, tc, bound.Resolved.Abs)
		if err != nil {
			return nil, nil, err
		}
		if err := inv.reserve(int64(len(captured.Content))); err != nil {
			return nil, nil, err
		}
		inv.sed.pins = append(inv.sed.pins, captured.Revision)
		return io.NopCloser(strings.NewReader(captured.Content)), &captured.Revision, nil
	}
	streamed, err := inv.owners.Revisions.OpenStream(inv.ctx, tc, bound.Resolved.Abs, 0)
	if err != nil {
		return nil, nil, err
	}
	execution := inv.request.Invocation.Identity.ExecutionID
	if execution == "" {
		_ = streamed.Reader.Close()
		return nil, nil, errors.New("file operands require a program execution identity")
	}
	if err := inv.owners.Pins.AppendRevisionPin(inv.ctx, execution, streamed.Revision); err != nil {
		_ = streamed.Reader.Close()
		return nil, nil, err
	}
	return streamed.Reader, &streamed.Revision, nil
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
	if reject := toolrejection.AsToolReject(err); reject != nil {
		switch reject.Code {
		case "READ_PATH_NOT_FOUND":
			return "No such file or directory", true
		case "READ_IS_DIRECTORY":
			return "Is a directory", true
		}
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
