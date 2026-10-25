package mutation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// Publication is one program's redirect owner: commandprogram.OutputPublication
// for >, >>, 2>, &> and &>>, and commandprogram.InputRedirection for <. Output
// destinations are captured when opened, before their producer starts; bytes
// are staged outside the destination and published only through the pinned
// write door at that capture. Nothing is truncated early and nothing reruns.
type Publication struct {
	Owners    *Owners
	Program   Program
	Tools     tools.ToolContext
	Admission tools.CommandOutputAdmission
}

var (
	_ commandprogram.OutputPublication = (*Publication)(nil)
	_ commandprogram.InputRedirection  = (*Publication)(nil)
	_ commandprogram.SourceCapture     = (*Publication)(nil)
)

// Open captures one output destination. The null device is a typed sink that
// publishes nothing and touches no file.
func (p *Publication) Open(ctx context.Context, destination commandprogram.OutputDestination) (commandprogram.StagedOutput, error) {
	bound, err := p.Program.Paths.Bind(ctx, destination.Target.Value, true)
	if err != nil {
		return nil, redirectError(destination.Redirect.Operator, destination.Target, err)
	}
	if bound.Null {
		return &nullStage{}, nil
	}
	if p.Admission == nil {
		return nil, errors.New("redirect publication admission required")
	}
	admission, err := p.Admission(ctx, destination, bound.Resolved.Abs)
	if err != nil {
		return nil, err
	}
	target, err := p.Owners.captureDestination(ctx, p.Tools, destination.Invocation.Identity.ExecutionID, bound.Resolved.Abs)
	if err != nil {
		_ = admission.Abandon(ctx, err)
		return nil, redirectError(destination.Redirect.Operator, destination.Target, err)
	}
	target.append = strings.HasSuffix(destination.Redirect.Operator, ">>")
	buffer, err := p.Owners.Revisions.NewBufferedBytes(ctx, p.Owners.limit("stream_bytes"), "stream_bytes")
	if err != nil {
		_ = admission.Abandon(ctx, err)
		return nil, redirectError(destination.Redirect.Operator, destination.Target, err)
	}
	return &fileStage{owners: p.Owners, target: target, admission: admission, operator: destination.Redirect.Operator, field: destination.Target, buffer: buffer}, nil
}

// OpenInput serves a < operand from its pinned published-disk capture. The
// pin is recorded first.
func (p *Publication) OpenInput(ctx context.Context, destination commandprogram.InputDestination) (io.ReadCloser, error) {
	bound, err := p.Program.Paths.Bind(ctx, destination.Target.Value, false)
	if err != nil {
		return nil, redirectError(destination.Redirect.Operator, destination.Target, err)
	}
	if bound.Null {
		return commandprogram.NullInput(), nil
	}
	source, err := p.Owners.Revisions.OpenStream(ctx, p.Tools, bound.Resolved.Abs, 0)
	if err != nil {
		return nil, redirectError(destination.Redirect.Operator, destination.Target, err)
	}
	if err := p.Owners.Pins.AppendRevisionPin(ctx, destination.Invocation.Identity.ExecutionID, source.Revision); err != nil {
		_ = source.Reader.Close()
		return nil, redirectError(destination.Redirect.Operator, destination.Target, err)
	}
	return source.Reader, nil
}

// Capture pins one source script from its accepted document or published disk.
func (p *Publication) Capture(ctx context.Context, inv toolcontract.OperationInvocation, request commandprogram.SourceCaptureRequest) (commandprogram.CapturedSource, error) {
	bound, err := p.Program.Paths.Bind(ctx, request.Path, false)
	if err != nil {
		return commandprogram.CapturedSource{}, wrapPathError("source", err)
	}
	if bound.Null {
		sum := sha256.Sum256(nil)
		return commandprogram.CapturedSource{
			Pin: toolcontract.SourcePin{
				SourceID:       "/dev/null",
				Revision:       "null",
				Representation: request.Representation,
				SHA256:         hex.EncodeToString(sum[:]),
			},
		}, nil
	}
	source, err := p.Owners.Revisions.CaptureSource(ctx, p.Tools, bound.Resolved.Abs)
	if err != nil {
		return commandprogram.CapturedSource{}, wrapPathError("source", err)
	}
	if err := p.Owners.Pins.AppendRevisionPin(ctx, inv.Identity.ExecutionID, source.Revision); err != nil {
		return commandprogram.CapturedSource{}, wrapPathError("source", err)
	}
	data := []byte(source.Content)
	sum := sha256.Sum256(data)
	rev := source.Revision.Identity()
	if rev == "" {
		rev = hex.EncodeToString(sum[:])
	}
	return commandprogram.CapturedSource{
		Pin: toolcontract.SourcePin{
			SourceID:       bound.Resolved.DisplayPath,
			Revision:       rev,
			Representation: request.Representation,
			SHA256:         hex.EncodeToString(sum[:]),
			StartByte:      0,
			EndByte:        int64(len(data)),
		},
		Bytes: data,
	}, nil
}

func redirectError(operator string, target commandprogram.Field, err error) error {
	if err == nil {
		return nil
	}
	err = tools.CommandResourceRejection(err)
	cmd := "redirect"
	if operator != "" {
		cmd = "redirect " + operator
	}
	err = wrapPathError(cmd, err)
	if reject := toolrejection.AsToolReject(err); reject != nil {
		if reject.Data == nil {
			reject.Data = map[string]any{}
		}
		reject.Data["command"] = cmd
		reject.Data["word"] = target.Value
		reject.Data["source_start_byte"] = target.Span.StartByte
		reject.Data["source_end_byte"] = target.Span.EndByte
		reject.Data["source_line"] = target.Span.Line
		reject.Data["source_column"] = target.Span.Column
	}
	return err
}

func wrapPathError(command string, err error) error {
	var pathErr *projectpaths.PathError
	if errors.As(err, &pathErr) && toolrejection.AsToolReject(err) == nil {
		reason := pathErr.Error()
		if pathErr.Cause != nil {
			reason = pathErr.Cause.Error()
		}
		return errors.Join(err, &toolrejection.ToolReject{Code: pathErr.Code, Data: map[string]any{
			"command": command, "word": pathErr.Operand, "path": pathErr.Operand, "reason": reason,
			"limit_id": pathErr.LimitID, "maximum": pathErr.Maximum, "observed": pathErr.Observed, "resource_unit": pathErr.ResourceUnit,
		}})
	}
	return err
}

// pinnedTarget is a destination captured before any bytes are prepared for it.
type pinnedTarget struct {
	abs    string
	pin    toolcontract.ExecutionRevision
	append bool
}

// captureDestination pins abs and appends the pin at the execution's next
// position, whatever its other owners captured before it.
func (o *Owners) captureDestination(ctx context.Context, tc tools.ToolContext, executionID string, abs string) (pinnedTarget, error) {
	pin, err := o.Revisions.CaptureDestination(ctx, tc, abs)
	if err != nil {
		return pinnedTarget{}, err
	}
	if err := o.Pins.AppendRevisionPin(ctx, executionID, pin); err != nil {
		return pinnedTarget{}, err
	}
	return pinnedTarget{abs: abs, pin: pin}, nil
}

// publish lands content byte for byte at the captured revision through the
// shared write operation. A moved destination is COMMAND_REVISION_CONFLICT;
// nothing is refreshed or retried.
func (o *Owners) publish(ctx context.Context, tc tools.ToolContext, target pinnedTarget, content []byte) (native.WriteResult, error) {
	// Appending to a captured absence creates the file from the new bytes.
	appendBytes := target.append && target.pin.Mode != toolcontract.AbsentRevision
	return o.Operations.Write.Apply(ctx, native.WriteInput{Path: target.abs, Exact: true, Bytes: content, Append: appendBytes, Memory: o.Revisions.Memory,
		Admission: native.WriteExecutionPinned, Destination: target.pin}, tc)
}

// fileStage holds one destination's bytes until the evaluator publishes or
// abandons them. Staging is bounded by stream_bytes and never touches the file.
type fileStage struct {
	owners    *Owners
	target    pinnedTarget
	operator  string
	field     commandprogram.Field
	admission tools.CommandOutputWrite

	mu      sync.Mutex
	buffer  *native.BufferedBytes
	settled bool
}

func (s *fileStage) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled {
		return 0, io.ErrClosedPipe
	}
	return s.buffer.Write(b)
}

func (s *fileStage) Publish(ctx context.Context) (commandprogram.OutputPublished, error) {
	s.mu.Lock()
	if s.settled {
		s.mu.Unlock()
		return commandprogram.OutputPublished{}, io.ErrClosedPipe
	}
	s.settled = true
	content := s.buffer.Bytes
	s.mu.Unlock()
	defer s.buffer.Close()
	published, err := s.admission.Publish(ctx, func(ctx context.Context, tc tools.ToolContext) (commandprogram.OutputPublished, error) {
		result, err := s.owners.publish(ctx, tc, s.target, content)
		if err != nil {
			return commandprogram.OutputPublished{}, err
		}
		published := commandprogram.OutputPublished{PublishedRevision: result.PublishedSHA256}
		if result.AcceptedRevision > 0 {
			published.AcceptedRevision = strconv.FormatInt(result.AcceptedRevision, 10)
		}
		return published, nil
	})
	if err != nil {
		return commandprogram.OutputPublished{}, redirectError(s.operator, s.field, err)
	}
	return published, nil
}

func (s *fileStage) Abandon(ctx context.Context) error {
	s.mu.Lock()
	if s.settled {
		s.mu.Unlock()
		return nil
	}
	s.settled = true
	s.mu.Unlock()
	s.buffer.Close()
	return s.admission.Abandon(ctx, nil)
}

// nullStage is the virtual null device: writes are discarded, nothing publishes.
type nullStage struct{}

func (*nullStage) Write(b []byte) (int, error) { return len(b), nil }
func (*nullStage) Publish(context.Context) (commandprogram.OutputPublished, error) {
	return commandprogram.OutputPublished{}, nil
}
func (*nullStage) Abandon(context.Context) error { return nil }
