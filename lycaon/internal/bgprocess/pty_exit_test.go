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
			running:             true,
			silent:              true,
			processRuntimeState: processRuntimeState{done: make(chan struct{})},
			processIOState: processIOState{
				pty:           exitedPTY{},
				ptyOutputDone: make(chan struct{}),
				screen:        newPTYScreen(80, 24),
			},
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

func TestHasPTYHandlesRetainsExitedTerminalUntilClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := NewRegistry(DefaultConfig(), Hooks{})
		const sessionID = "sess-pty-retained"
		const handle = "pty-1"
		proc := &Process{
			Handle:              handle,
			SessionID:           sessionID,
			RootSessionID:       sessionID,
			kind:                processKindPTY,
			mode:                JobModeBackground,
			running:             true,
			processRuntimeState: processRuntimeState{done: make(chan struct{})},
			processIOState: processIOState{
				buffer:        NewRingBuffer(DefaultRingBufferBytes),
				pty:           exitedPTY{},
				ptyOutputDone: make(chan struct{}),
				screen:        newPTYScreen(80, 24),
			},
		}
		reg.jobs.sessions[sessionID] = map[string]*Process{handle: proc}
		if !reg.HasPTYHandles(sessionID) || reg.CountLivePTYs(sessionID) != 1 {
			t.Fatalf("live pty: HasPTYHandles=%v CountLivePTYs=%d", reg.HasPTYHandles(sessionID), reg.CountLivePTYs(sessionID))
		}
		close(proc.ptyOutputDone)
		go reg.Terminal.waitPTY(t.Context(), proc)
		synctest.Wait()
		if reg.CountLivePTYs(sessionID) != 0 {
			t.Fatalf("CountLivePTYs after exit = %d, want 0", reg.CountLivePTYs(sessionID))
		}
		if !reg.HasPTYHandles(sessionID) {
			t.Fatal("HasPTYHandles after exit = false, want true while retained for terminal_read")
		}
		if _, err := reg.Terminal.ClosePTY(sessionID, handle); err != nil {
			t.Fatalf("ClosePTY: %v", err)
		}
		if reg.HasPTYHandles(sessionID) {
			t.Fatal("HasPTYHandles after ClosePTY = true, want false")
		}
	})
}
