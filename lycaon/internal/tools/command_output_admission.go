package tools

import (
	"context"
	"github.com/lycaon/lycaon/internal/commandprogram"
)

// CommandOutputTarget identifies the resolved redirect destination.
type CommandOutputTarget struct {
	Path, DisplayPath, RootID string
}

// CommandOutputAdmission checks and records the redirect before the producer starts.
type CommandOutputAdmission func(context.Context, commandprogram.OutputDestination, CommandOutputTarget) (CommandOutputWrite, error)

type CommandOutputWrite struct {
	Publish func(context.Context, func(context.Context, ToolContext) (commandprogram.OutputPublished, error)) (commandprogram.OutputPublished, error)
	Abandon func(context.Context, error) error
}
