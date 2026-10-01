package commandsurface

import (
	"errors"

	"github.com/lycaon/lycaon/internal/argv"
)

// IsCommandSurfaceError reports quote, newline, and metacharacter parse failures.
func IsCommandSurfaceError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, argv.ErrUnterminatedQuote) ||
		errors.Is(err, argv.ErrUnquotedNewline) ||
		errors.Is(err, argv.ErrShellMetacharacters) ||
		errors.Is(err, argv.ErrCommandRequired) ||
		errors.Is(err, argv.ErrBackgroundOperator) ||
		errors.Is(err, argv.ErrEmptySequenceElement) ||
		errors.Is(err, argv.ErrRedirectionTargetRequired) ||
		errors.Is(err, ErrDirectoryChangeCommand) ||
		errors.Is(err, argv.ErrEnvAssignmentCommand) ||
		errors.Is(err, ErrStdinWithSequence) ||
		errors.Is(err, ErrSequenceUnsupported)
}
