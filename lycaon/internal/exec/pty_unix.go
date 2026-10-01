//go:build unix

package exec

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// resizePTY sets the main window size.
func resizePTY(main *os.File, size WinSize) error {
	return pty.Setsize(main, &pty.Winsize{Cols: size.Cols, Rows: size.Rows})
}

// startWithPTY returns the controller for a child terminal session.
func startWithPTY(cmd *exec.Cmd, size WinSize) (*os.File, error) {
	main, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: size.Cols, Rows: size.Rows})
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}
	return main, nil
}

func (p *ptySession) Resize(size WinSize) error {
	if p.main == nil {
		return fmt.Errorf("pty main closed")
	}
	return resizePTY(p.main, normalizeWinSize(size))
}
