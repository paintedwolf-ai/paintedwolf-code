//go:build windows

package exec

import (
	"fmt"
	"os"
	"os/exec"
)

// startWithPTY has no Windows backend; every start returns a typed
// unsupported error.
func startWithPTY(cmd *exec.Cmd, size WinSize) (*os.File, error) {
	_ = cmd
	_ = size
	return nil, fmt.Errorf("%w: ConPTY not yet supported", ErrPTYUnsupported)
}

func (p *ptySession) Resize(size WinSize) error {
	_ = size
	return fmt.Errorf("%w: ConPTY not yet supported", ErrPTYUnsupported)
}
