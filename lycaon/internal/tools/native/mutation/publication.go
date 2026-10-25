package mutation

import (
	"bytes"
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
	Owners  *Owners
	Program Program
	Tools   tools.ToolContext
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
	target, err := p.Owners.captureDestination(ctx, p.Tools, destination.Invocation.Identity.ExecutionID, bound.Resolved.Abs)
	if err != nil {
		return nil, redirectError(destination.Redirect.Operator, destination.Target, err)
	}
	target.append = strings.HasSuffix(destination.Redirect.Operator, ">>")
	return &fileStage{owners: p.Owners, tools: p.Tools, barrier: p.Program.Barrier, target: target, operator: destination.Redirect.Operator, field: destination.Target, maximum: p.Owners.limit("stream_bytes")}, nil
}

// OpenInput serves a < operand from its pinned capture: the accepted document
// when one is open, otherwise published disk. The pin is recorded first.
func (p *Publication) OpenInput(ctx context.Context, destination commandprogram.InputDestination) (io.ReadCloser, error) {
	bound, err := p.Program.Paths.Bind(ctx, destination.Target.Value, false)
	if err != nil {
		return nil, redirectError(destination.Redirect.Operator, destination.Target, err)
	}
	if bound.Null {
		return commandprogram.NullInput(), nil
	}
	source, err := p.Owners.Revisions.CaptureSource(ctx, p.Tools, bound.Resolved.Abs)
	if err != nil {
		return nil, redirectError(destination.Redirect.Operator, destination.Target, err)
	}
	if err := p.Owners.Pins.AppendRevisionPin(ctx, destination.Invocation.Identity.ExecutionID, source.Revision); err != nil {
		return nil, redirectError(destination.Redirect.Operator, destination.Target, err)
	}
	return io.NopCloser(strings.NewReader(source.Content)), nil
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
	rev := hex.EncodeToString(sum[:])
	switch {
	case source.Revision.DocumentID != "":
		rev = "document:" + source.Revision.DocumentID + "@" + strconv.FormatInt(source.Revision.DocumentRevision, 10)
	case source.Revision.ContentSHA256 != "":
		rev = "sha256:" + source.Revision.ContentSHA256
	case source.Revision.DiskSHA256 != "":
		rev = "disk:" + source.Revision.DiskSHA256
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
	cmd := "redirect"
	if operator != "" {
		cmd = "redirect " + operator
	}
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
		return err
	}
	var pathErr *projectpaths.PathError
	if errors.As(err, &pathErr) {
		reason := pathErr.Error()
		if pathErr.Cause != nil {
			reason = pathErr.Cause.Error()
		}
		return errors.Join(err, toolrejection.RejectInvalidArguments(pathErr.Code, map[string]any{
			"command": cmd, "word": target.Value, "path": pathErr.Operand, "reason": reason,
			"limit_id": pathErr.LimitID, "maximum": pathErr.Maximum, "observed": pathErr.Observed, "resource_unit": pathErr.ResourceUnit,
			"source_start_byte": target.Span.StartByte, "source_end_byte": target.Span.EndByte,
			"source_line": target.Span.Line, "source_column": target.Span.Column,
		}))
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
// shared write operation and tracks it on the program's publication barrier. A moved
// destination is COMMAND_REVISION_CONFLICT; nothing is refreshed or retried.
func (o *Owners) publish(ctx context.Context, tc tools.ToolContext, barrier *native.PublicationBarrier, target pinnedTarget, content []byte) (native.WriteResult, error) {
	// Appending to a captured absence creates the file from the new bytes.
	appendBytes := target.append && target.pin.Mode != toolcontract.AbsentRevision
	result, err := o.Operations.Write.Apply(ctx, native.WriteInput{Path: target.abs, Exact: true, Bytes: content, Append: appendBytes,
		Admission: native.WriteExecutionPinned, Destination: target.pin}, tc)
	if err != nil {
		return result, err
	}
	return result, barrier.Track(result)
}

// fileStage holds one destination's bytes until the evaluator publishes or
// abandons them. Staging is bounded by stream_bytes and never touches the file.
type fileStage struct {
	owners   *Owners
	tools    tools.ToolContext
	barrier  *native.PublicationBarrier
	target   pinnedTarget
	operator string
	field    commandprogram.Field
	maximum  int64

	mu      sync.Mutex
	buffer  bytes.Buffer
	settled bool
}

func (s *fileStage) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled {
		return 0, io.ErrClosedPipe
	}
	if observed := int64(s.buffer.Len() + len(b)); observed > s.maximum {
		return 0, &commandprogram.StreamLimitError{Limit: "stream_bytes", Maximum: s.maximum, Observed: observed}
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
	content := bytes.Clone(s.buffer.Bytes())
	s.buffer.Reset()
	s.mu.Unlock()
	result, err := s.owners.publish(ctx, s.tools, s.barrier, s.target, content)
	if err != nil {
		return commandprogram.OutputPublished{}, redirectError(s.operator, s.field, err)
	}
	published := commandprogram.OutputPublished{PublishedRevision: result.PublishedSHA256}
	if result.AcceptedRevision > 0 {
		published.AcceptedRevision = strconv.FormatInt(result.AcceptedRevision, 10)
	}
	return published, nil
}

func (s *fileStage) Abandon(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settled = true
	s.buffer.Reset()
	return nil
}

// nullStage is the virtual null device: writes are discarded, nothing publishes.
type nullStage struct{}

func (*nullStage) Write(b []byte) (int, error) { return len(b), nil }
func (*nullStage) Publish(context.Context) (commandprogram.OutputPublished, error) {
	return commandprogram.OutputPublished{}, nil
}
func (*nullStage) Abandon(context.Context) error { return nil }
