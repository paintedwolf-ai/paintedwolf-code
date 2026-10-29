//go:build unix

package exec

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// resizePTY sets the main window size.
func resizePTY(main *os.File, size WinSize) error {
	return pty.Setsize(main, &pty.Winsize{Cols: size.Cols, Rows: size.Rows})
}

// startWithPTY returns the controller for a child terminal session.
func startWithPTY(cmd *exec.Cmd, size WinSize) (*os.File, error) {
	winsize := &pty.Winsize{Cols: size.Cols, Rows: size.Rows}
	if relayedCommand(cmd) {
		// The supervisor the broker starts takes the terminal as its own
		// session's; the relay standing in for it leads a session without
		// one, so the terminal's signals reach only the program on it.
		main, err := pty.StartWithAttrs(cmd, winsize, &syscall.SysProcAttr{Setsid: true})
		if err != nil {
			return nil, fmt.Errorf("start pty: %w", err)
		}
		return main, nil
	}
	main, err := pty.StartWithSize(cmd, winsize)
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
