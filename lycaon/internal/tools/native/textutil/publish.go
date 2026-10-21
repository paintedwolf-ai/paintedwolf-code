package textutil

import (
	"bytes"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// destination is one pinned sed -i file or uniq OUTPUT. Its revision, or its
// absence, is captured before any bytes are prepared for it, in the
// execution's capture order; publication lands exact bytes at that pin.
type destination struct {
	operand commandprogram.Field
	abs     string
	pin     toolcontract.ExecutionRevision
	sources []toolcontract.ExecutionRevision
}

func (inv *invocation) paths() (*projectpaths.ProgramPaths, error) {
	return projectpaths.NewProgramPaths(inv.ctx, inv.owners.Revisions.Boundary, inv.ports.ToolContext, inv.request.Invocation.Scope.Directory)
}

// pinnedOutputs captures and publishes one invocation's destinations.
type pinnedOutputs struct{ inv *invocation }

// capture binds the operand under mutation authority and pins it.
func (o pinnedOutputs) capture(paths *projectpaths.ProgramPaths, operand commandprogram.Field) (destination, error) {
	inv := o.inv
	if inv.ports.Write == nil {
		return destination{}, toolrejection.RejectInvalidArguments("COMMAND_PLATFORM_UNAVAILABLE", map[string]any{"command": inv.spec.Name, "capability": "the program's pinned write owner"})
	}
	bound, err := paths.Bind(inv.ctx, operand.Value, true)
	if err != nil {
		return destination{}, pathRejection(err, operand)
	}
	if bound.Null {
		return destination{}, inv.usage(operand, "a file; the null device holds no publication")
	}
	pin, err := inv.owners.Revisions.CaptureDestination(inv.ctx, inv.ports.ToolContext, bound.Resolved.Abs)
	if err != nil {
		return destination{}, pathRejection(err, operand)
	}
	if err := inv.recordPin(pin); err != nil {
		return destination{}, err
	}
	return destination{operand: operand, abs: bound.Resolved.Abs, pin: pin}, nil
}

// publish lands content at the destination's pin through the shared pinned
// write door and the program's publication barrier. A destination that moved
// since capture is COMMAND_REVISION_CONFLICT; nothing is refreshed or rerun.
func (o pinnedOutputs) publish(d destination, content []byte) error {
	inv := o.inv
	result, err := inv.ports.Write.Apply(inv.ctx, native.WriteInput{Path: d.abs, Content: string(content),
		Admission: native.WriteExecutionPinned, Destination: d.pin, Sources: d.sources}, inv.ports.ToolContext)
	if err != nil {
		return err
	}
	if inv.ports.Barrier != nil {
		return inv.ports.Barrier.Track(result)
	}
	return nil
}

// staged collects one destination's complete bytes under stream_bytes.
func staged(inv *invocation) (*bytes.Buffer, *nativeutility.Output) {
	var buffer bytes.Buffer
	return &buffer, nativeutility.NewOutput(inv.ctx, &buffer, inv.limit("stream_bytes"))
}

// sedInPlace edits each file separately: line numbers, $ and ranges restart
// per file. Every file's destination and source are pinned before it is read,
// and its complete output is published before the next file starts; the first
// failure stops the run and earlier files stay published. q ends the run after
// publishing the current file's output so far.
func sedInPlace(inv *invocation, commands []*sedCommand, files commandprogram.Fields) error {
	if len(files) == 0 {
		return inv.usage(inv.commandWord(), "a PATH to edit in place")
	}
	paths, err := inv.paths()
	if err != nil {
		return err
	}
	for _, operand := range files {
		if operand.Value == "-" {
			return inv.usage(operand, "a file; standard input cannot be edited in place")
		}
		target, err := pinnedOutputs{inv}.capture(paths, operand)
		if err != nil {
			return err
		}
		reader, source, err := inv.capture(paths, operand)
		if err != nil {
			if reason, ok := ordinary(err); ok {
				inv.fail(2, operand.Value, reason)
				return nil
			}
			return pathRejection(err, operand)
		}
		if source != nil {
			target.sources = []toolcontract.ExecutionRevision{*source}
		}
		records, err := inv.records(reader, '\n')
		if err != nil {
			return err
		}
		for _, c := range commands {
			c.active = false
		}
		buffer, out := staged(inv)
		run := &sedRun{inv: inv, commands: commands, quiet: inv.flag("-n"), records: records, out: out}
		quit, err := run.cycles()
		err = regexBudget(err)
		if err == nil {
			err = pinnedOutputs{inv}.publish(target, buffer.Bytes())
		}
		if err != nil || quit {
			return err
		}
	}
	return nil
}

// uniqOutput pins OUTPUT after INPUT and publishes uniq's complete output there.
func uniqOutput(inv *invocation, operands commandprogram.Fields, run func(*nativeutility.Output) error) error {
	paths, err := inv.paths()
	if err != nil {
		return err
	}
	sources := len(inv.pins)
	target, err := pinnedOutputs{inv}.capture(paths, operands[1])
	if err != nil {
		return err
	}
	target.sources = append([]toolcontract.ExecutionRevision(nil), inv.pins[:sources]...)
	buffer, out := staged(inv)
	if err := run(out); err != nil {
		return err
	}
	return pinnedOutputs{inv}.publish(target, buffer.Bytes())
}
