package confine

import (
	"errors"
	"fmt"
	"strings"
)

// Helper failures cross the process boundary through an exit code and marker.
const (
	// HelperStderrPrefix heads every line the helper writes before exiting.
	HelperStderrPrefix = "confine: "
	// commandNotFoundText is the message body for an unresolvable argv0.
	commandNotFoundText = "command not found"
	// CommandNotFoundExit is the helper's exit status for it (Unix convention).
	CommandNotFoundExit = 127
)

// CommandNotFoundError is returned when the confine helper cannot resolve argv0 on PATH
// before applying the sandbox.
type CommandNotFoundError struct {
	Name string
}

func (e *CommandNotFoundError) Error() string {
	if e == nil {
		return commandNotFoundText
	}
	return fmt.Sprintf("%s: %q", commandNotFoundText, e.Name)
}

// IsCommandNotFound reports whether err is or wraps a CommandNotFoundError.
func IsCommandNotFound(err error) bool {
	var nf *CommandNotFoundError
	return errors.As(err, &nf)
}

// ReportsCommandNotFound requires both the helper marker and its exit code.
func ReportsCommandNotFound(exitCode int, output string) bool {
	if exitCode != CommandNotFoundExit {
		return false
	}
	return strings.Contains(output, HelperStderrPrefix+commandNotFoundText)
}
