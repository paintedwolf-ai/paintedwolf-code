package bgprocess

import (
	"strings"
	"testing"
	"testing/synctest"

	"github.com/lycaon/lycaon/internal/exec"
)

type exitedPTY struct{ exec.PTY }

func (exitedPTY) Wait() error  { return nil }
func (exitedPTY) Close() error { return nil }

func TestPTYCompletionWaitsForTerminalOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := NewRegistry(DefaultConfig(), Hooks{})
		proc := &Process{
			pty: exitedPTY{}, ptyOutputDone: make(chan struct{}),
			done: make(chan struct{}), running: true, silent: true,
			screen: newPTYScreen(80, 24),
		}
		go reg.Terminal.waitPTY(t.Context(), proc)
		synctest.Wait()
		select {
		case <-proc.done:
			t.Fatal("terminal completed before its output drained")
		default:
		}
		proc.screen.Write([]byte("READY\r\n"))
		close(proc.ptyOutputDone)
		synctest.Wait()
		select {
		case <-proc.done:
		default:
			t.Fatal("terminal did not complete after output drained")
		}
		if proc.finalScreen == nil || !strings.Contains(strings.Join(proc.finalScreen.Lines, "\n"), "READY") {
			t.Fatalf("final screen lost terminal output: %+v", proc.finalScreen)
		}
	})
}
