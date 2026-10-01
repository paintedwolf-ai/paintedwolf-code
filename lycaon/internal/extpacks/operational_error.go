package extpacks

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os/exec"
)

// OperationalFailure distinguishes host and transport failures from candidate diagnostics.
func OperationalFailure(err error) bool {
	var path *fs.PathError
	var network net.Error
	var process *exec.ExitError
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &path) ||
		errors.As(err, &network) || errors.As(err, &process)
}
