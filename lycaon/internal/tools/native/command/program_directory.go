package command

import (
	"context"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

var (
	_ commandprogram.DirectoryFrame   = (*ProgramDispatcher)(nil)
	_ commandprogram.LogicalAddresses = (*ProgramDispatcher)(nil)
)

// Directory is this frame's absolute working directory, which PWD holds.
func (d *ProgramDispatcher) Directory() string { return d.paths.Directory() }

// Address converts an unquoted @label or @scratch prefix to the folder's
// native absolute path through the frame's path owner; it grants nothing.
func (d *ProgramDispatcher) Address(ctx context.Context, label string) (string, bool, error) {
	return d.paths.Address(ctx, label)
}
