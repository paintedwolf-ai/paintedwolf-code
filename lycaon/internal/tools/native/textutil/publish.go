package textutil

import (
	"context"
	"errors"
	"io"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func newPaths(inv *invocation) (*projectpaths.ProgramPaths, error) {
	return projectpaths.NewProgramPaths(inv.ctx, inv.owners.Revisions.Boundary, inv.ports.ToolContext, inv.request.Invocation.Scope.Directory)
}

// openDestination stages one sed -i file or uniq OUTPUT through the program's
// redirect owner, which pins the destination at the execution's next
// position and later publishes exact bytes at that pin. A destination that
// moved since capture is COMMAND_REVISION_CONFLICT; nothing reruns.
func (inv *invocation) openDestination(operand commandprogram.Field) (commandprogram.StagedOutput, error) {
	if inv.ports.Publication == nil {
		return nil, toolrejection.RejectInvalidArguments("COMMAND_PLATFORM_UNAVAILABLE", map[string]any{"command": inv.spec.Name, "capability": "the program's redirect publication owner"})
	}
	stage, err := inv.ports.Publication.Open(inv.ctx, commandprogram.OutputDestination{
		Invocation: inv.request.Invocation,
		Redirect:   commandsurface.ProgramRedirect{SyntaxLocation: commandsurface.SyntaxLocation{Span: operand.Span}, Operator: ">", Descriptor: -1},
		Target:     operand,
	})
	if err != nil {
		return nil, pathRejection(err, operand)
	}
	return stage, nil
}

// settleStage publishes a complete stage or abandons it on failure.
func settleStage(ctx context.Context, stage commandprogram.StagedOutput, err error) error {
	if err != nil {
		return errors.Join(err, stage.Abandon(context.WithoutCancel(ctx)))
	}
	_, err = stage.Publish(ctx)
	return err
}

// captureExisting pins an in-place file's source; a missing or unreadable file
// is an ordinary failure that stops the run before anything is staged for it.
func captureExisting(inv *invocation, paths *projectpaths.ProgramPaths, operand commandprogram.Field) (io.ReadCloser, error) {
	reader, _, err := inv.capture(paths, operand)
	if err == nil {
		return reader, nil
	}
	if reason, ok := ordinary(err); ok {
		inv.fail(2, operand.Value, reason)
		return nil, nil
	}
	return nil, pathRejection(err, operand)
}

// uniqOutput stages OUTPUT after INPUT is pinned and publishes uniq's output.
func uniqOutput(inv *invocation, operands commandprogram.Fields, run func(*nativeutility.Output) error) error {
	stage, err := inv.openDestination(operands[1])
	if err != nil {
		return err
	}
	return settleStage(inv.ctx, stage, run(nativeutility.NewOutput(inv.ctx, stage, inv.limit("stream_bytes"))))
}

// effectMode names the catalog mode of commands with several: sed -i and
// inline w files, sort -o and uniq OUTPUT write; every other form reads.
func (o *Owners) effectMode(ctx context.Context, argv commandprogram.BoundArgv) string {
	switch argv.Command {
	case "sed":
		return o.sedMode(ctx, argv)
	case "sort":
		if argvFlag(argv, "-o") {
			return "-o"
		}
		return "always"
	case "uniq":
		if len(argv.Operands()) > 1 {
			return "OUTPUT-supplied"
		}
		return "stdin-input-only"
	}
	return ""
}
