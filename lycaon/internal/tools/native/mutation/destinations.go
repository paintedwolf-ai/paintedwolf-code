package mutation

import (
	"context"

	"github.com/lycaon/lycaon/internal/tools"
)

// Destinations lets another command frontend (curl -o, -D, -c) publish one
// execution's file outputs through the same pinned exact-bytes door: each
// destination is captured before the producer runs, its pin recorded for the
// execution in capture order, and published only at that capture.
type Destinations struct {
	owners    *Owners
	program   Program
	tools     tools.ToolContext
	execution string
}

// Destination is one captured output; a null-device destination publishes
// nothing and touches no file.
type Destination struct {
	null   bool
	target pinnedTarget
}

func (o *Owners) Destinations(program Program, tc tools.ToolContext, executionID string) *Destinations {
	return &Destinations{owners: o, program: program, tools: tc, execution: executionID}
}

// Capture binds operand through the invocation cwd under mutation scope and
// pins it before any bytes are prepared for it.
func (d *Destinations) Capture(ctx context.Context, operand string) (Destination, error) {
	bound, err := d.program.Paths.Bind(ctx, operand, true)
	if err != nil {
		return Destination{}, err
	}
	if bound.Null {
		return Destination{null: true}, nil
	}
	target, err := d.owners.captureDestination(ctx, d.tools, d.execution, bound.Resolved.Abs)
	if err != nil {
		return Destination{}, err
	}
	return Destination{target: target}, nil
}

// Publish lands data at the captured destination; a moved destination
// conflicts with COMMAND_REVISION_CONFLICT and nothing is retried.
func (d *Destinations) Publish(ctx context.Context, destination Destination, data []byte) error {
	if destination.null {
		return nil
	}
	_, err := d.owners.publish(ctx, d.tools, destination.target, data)
	return err
}

// ReadSource pins and returns one text input (for example a cookie file),
// serving the accepted document when one is open.
func (d *Destinations) ReadSource(ctx context.Context, operand string) (string, error) {
	bound, err := d.program.Paths.Bind(ctx, operand, false)
	if err != nil {
		return "", err
	}
	source, err := d.owners.Revisions.CaptureSource(ctx, d.tools, bound.Resolved.Abs)
	if err != nil {
		return "", err
	}
	if err := d.owners.Pins.AppendRevisionPin(ctx, d.execution, source.Revision); err != nil {
		return "", err
	}
	return source.Content, nil
}
