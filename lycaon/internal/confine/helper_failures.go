package confine

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

const (
	// HelperFailureExit is the helper's exit status for every failure except an
	// unresolvable argv0 and a boundary the host cannot provide.
	HelperFailureExit = 126
	// HelperUnavailableExit is the helper's exit status when the kernel lacks a
	// capability the boundary needs; stderr carries the typed reason.
	HelperUnavailableExit = 125
	execDeniedText        = "exec denied"
)

// ExecDeniedError is a resolved argv0 that syscall.Exec refused with EACCES or EPERM.
type ExecDeniedError struct {
	Path string
}

func (e *ExecDeniedError) Error() string {
	if e == nil {
		return execDeniedText
	}
	return fmt.Sprintf("%s: %q", execDeniedText, e.Path)
}

// ReportsExecDenied requires both the helper's exec-denied marker and its exit code.
func ReportsExecDenied(exitCode int, output string) bool {
	if exitCode != HelperFailureExit {
		return false
	}
	return strings.Contains(output, HelperStderrPrefix+execDeniedText+":")
}

// ClassifyExecError maps EACCES/EPERM from syscall.Exec to ExecDeniedError;
// syscall.Errno matches fs.ErrPermission for exactly those two.
func ClassifyExecError(path string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrPermission) {
		return &ExecDeniedError{Path: path}
	}
	return fmt.Errorf("exec %q: %w", path, err)
}

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
