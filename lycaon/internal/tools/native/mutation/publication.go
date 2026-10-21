package mutation

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
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
)

// Open captures one output destination. The null device is a typed sink that
// publishes nothing and touches no file.
func (p *Publication) Open(ctx context.Context, destination commandprogram.OutputDestination) (commandprogram.StagedOutput, error) {
	bound, err := p.Program.Paths.Bind(ctx, destination.Target.Value, true)
	if err != nil {
		return nil, err
	}
	if bound.Null {
		return &nullStage{}, nil
	}
	target, err := p.Owners.captureDestination(ctx, p.Tools, destination.Invocation.Identity.ExecutionID, nil, bound.Resolved.Abs)
	if err != nil {
		return nil, err
	}
	target.append = strings.HasSuffix(destination.Redirect.Operator, ">>")
	return &fileStage{owners: p.Owners, tools: p.Tools, barrier: p.Program.Barrier, target: target, maximum: p.Owners.limit("stream_bytes")}, nil
}

// OpenInput serves a < operand from its pinned capture: the accepted document
// when one is open, otherwise published disk. The pin is recorded first.
func (p *Publication) OpenInput(ctx context.Context, destination commandprogram.InputDestination) (io.ReadCloser, error) {
	bound, err := p.Program.Paths.Bind(ctx, destination.Target.Value, false)
	if err != nil {
		return nil, err
	}
	if bound.Null {
		return commandprogram.NullInput(), nil
	}
	source, err := p.Owners.Revisions.CaptureSource(ctx, p.Tools, bound.Resolved.Abs)
	if err != nil {
		return nil, err
	}
	if err := p.Owners.Pins.RecordRevisionPins(ctx, destination.Invocation.Identity.ExecutionID, []toolcontract.ExecutionRevision{source.Revision}); err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(source.Content)), nil
}

// pinnedTarget is a destination captured before any bytes are prepared for it.
type pinnedTarget struct {
	abs    string
	pin    toolcontract.ExecutionRevision
	append bool
}

// captureDestination pins abs and records the pin after prior's pins.
func (o *Owners) captureDestination(ctx context.Context, tc tools.ToolContext, executionID string, prior []toolcontract.ExecutionRevision, abs string) (pinnedTarget, error) {
	pin, err := o.Revisions.CaptureDestination(ctx, tc, abs)
	if err != nil {
		return pinnedTarget{}, err
	}
	if err := o.Pins.RecordRevisionPins(ctx, executionID, append(append([]toolcontract.ExecutionRevision(nil), prior...), pin)); err != nil {
		return pinnedTarget{}, err
	}
	return pinnedTarget{abs: abs, pin: pin}, nil
}

// publish lands content at the captured revision through the shared write
// operation and tracks it on the program's publication barrier. A moved
// destination is COMMAND_REVISION_CONFLICT; nothing is refreshed or retried.
func (o *Owners) publish(ctx context.Context, tc tools.ToolContext, barrier *native.PublicationBarrier, target pinnedTarget, content string) (native.WriteResult, error) {
	// Appending to a captured absence creates the file from the new bytes.
	appendBytes := target.append && target.pin.Mode != toolcontract.AbsentRevision
	result, err := o.Operations.Write.Apply(ctx, native.WriteInput{Path: target.abs, Content: content, Append: appendBytes,
		Admission: native.WriteExecutionPinned, Destination: target.pin}, tc)
	if err != nil {
		return result, err
	}
	return result, barrier.Track(result)
}

// fileStage holds one destination's bytes until the evaluator publishes or
// abandons them. Staging is bounded by stream_bytes and never touches the file.
type fileStage struct {
	owners  *Owners
	tools   tools.ToolContext
	barrier *native.PublicationBarrier
	target  pinnedTarget
	maximum int64

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
	content := s.buffer.String()
	s.buffer.Reset()
	s.mu.Unlock()
	result, err := s.owners.publish(ctx, s.tools, s.barrier, s.target, content)
	if err != nil {
		return commandprogram.OutputPublished{}, err
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
