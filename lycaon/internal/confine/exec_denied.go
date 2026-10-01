package confine

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

const (
	// HelperFailureExit is the helper's exit status for every failure except an
	// unresolvable argv0.
	HelperFailureExit = 126
	execDeniedText    = "exec denied"
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
