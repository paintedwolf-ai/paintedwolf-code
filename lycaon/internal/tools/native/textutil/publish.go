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

// openDestination stages one sed -i file, sort or iconv -o, or uniq OUTPUT
// through the program's redirect owner, which pins the destination at the
// execution's next position and later publishes exact bytes at that pin. A
// destination that moved since capture is COMMAND_REVISION_CONFLICT; nothing
// reruns.
func (inv *invocation) openDestination(operand commandprogram.Field) (commandprogram.StagedOutput, error) {
	if writer, ok := operand.ProcessSubstitution().OpenWriter(); ok {
		return commandprogram.NewProcessSubstitutionStagedOutput(writer), nil
	}
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
func (inv *invocation) settleStage(stage commandprogram.StagedOutput, err error) error {
	if err != nil {
		return errors.Join(err, stage.Abandon(context.WithoutCancel(inv.ctx)))
	}
	published, err := stage.Publish(inv.ctx)
	if published.AcceptedRevision != "" || published.PublishedRevision != "" {
		inv.published = true
	}
	if err != nil {
		inv.uncertain = true
	}
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
	return inv.settleStage(stage, run(nativeutility.NewOutput(inv.ctx, stage, "stream_bytes", inv.limit("stream_bytes"))))
}

// effectMode names the catalog mode of commands with several: sort -o,
// iconv -o and uniq OUTPUT write; every other form reads. sed's mode is
// resolved by its admitted prepared plan.
func (o *Owners) effectMode(argv commandprogram.BoundArgv) string {
	switch argv.Command {
	case "sort", "iconv":
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
