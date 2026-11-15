package tools

import (
	"context"
	"github.com/lycaon/lycaon/internal/commandprogram"
)

// CommandOutputAdmission begins the write receipt before destination capture for
// the resolved path and its workspace display path. Publish admits the original
// captured write; abandonment has no destination effect.
type CommandOutputAdmission func(ctx context.Context, destination commandprogram.OutputDestination, path, displayPath string) (CommandOutputWrite, error)
type CommandOutputWrite struct {
	Publish func(context.Context, func(context.Context, ToolContext) (commandprogram.OutputPublished, error)) (commandprogram.OutputPublished, error)
	Abandon func(context.Context, error) error
}
