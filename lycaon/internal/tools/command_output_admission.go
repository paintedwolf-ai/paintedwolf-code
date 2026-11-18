package tools

import (
	"context"
	"github.com/lycaon/lycaon/internal/commandprogram"
)

// CommandOutputTarget identifies the resolved redirect destination.
type CommandOutputTarget struct {
	Path, DisplayPath, AttachedRootID string
}

// CommandOutputAdmission checks a destination and opens its receipt for staging.
type CommandOutputAdmission struct {
	Check func(context.Context, commandprogram.OutputDestination, CommandOutputTarget) error
	Begin func(context.Context, commandprogram.OutputDestination, CommandOutputTarget) (CommandOutputWrite, error)
}

type CommandOutputWrite struct {
	Publish func(context.Context, func(context.Context, ToolContext) (commandprogram.OutputPublished, error)) (commandprogram.OutputPublished, error)
	Abandon func(context.Context, error) error
}
