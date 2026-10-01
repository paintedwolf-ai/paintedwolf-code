package bgprocess

import (
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/x/vt"
	lycexec "github.com/lycaon/lycaon/internal/exec"
)

// ScreenSnapshot is the assertable vt virtual screen (rows × cols + cursor).
// It is the TUI half of surface_snapshot{tui}; terminal_read's ring-buffer
// cursor is independent of this view.
type ScreenSnapshot struct {
	Cols      int      `json:"cols"`
	Rows      int      `json:"rows"`
	CursorCol int      `json:"cursor_col"`
	CursorRow int      `json:"cursor_row"`
	Lines     []string `json:"lines"`
	AltScreen bool     `json:"alt_screen"`
}

// ptyScreen serializes emulator access and drains replies to unblock writes.
type ptyScreen struct {
	emu *vt.SafeEmulator

	// emuMu serialises our emulator calls that touch its closed flag.
	emuMu   sync.Mutex
	closing atomic.Bool

	replyMu sync.Mutex
	onReply func([]byte)
}

// ptyScreenWake is written to the emulator's input pipe to unblock the drain
// goroutine so it can close the emulator from its own goroutine. It is consumed
// by the drain and never forwarded.
const ptyScreenWake = "\x00"

func newPTYScreen(cols, rows int) *ptyScreen {
	if cols <= 0 {
		cols = int(lycexec.DefaultPTYCols)
	}
	if rows <= 0 {
		rows = int(lycexec.DefaultPTYRows)
	}
	s := &ptyScreen{emu: vt.NewSafeEmulator(cols, rows)}
	go s.drainReplies()
	return s
}

// setOnReply installs a callback for VT auto-replies (DSR/CPR). Forward these
// to the pty main so apps that probe with CSI 6n receive a response.
func (s *ptyScreen) setOnReply(fn func([]byte)) {
	if s == nil {
		return
	}
	s.replyMu.Lock()
	s.onReply = fn
	s.replyMu.Unlock()
}

// Close unblocks drainReplies (and any Write blocked on the reply pipe). It
// wakes the drain goroutine rather than closing the emulator here: the drain is
// parked inside the emulator's unsynchronised Read, and closing from a second
// goroutine races the flag that Read consults. Once the input pipe is closed the
// wake write fails immediately, so a repeat Close cannot block.
func (s *ptyScreen) Close() {
	if s == nil || s.emu == nil {
		return
	}
	if s.closing.Swap(true) {
		return
	}
	_, _ = io.WriteString(s.emu.InputPipe(), ptyScreenWake)
}

func (s *ptyScreen) drainReplies() {
	if s == nil || s.emu == nil {
		return
	}
	buf := make([]byte, 512)
	for {
		n, err := s.emu.Read(buf)
		if s.closing.Load() {
			// Same goroutine as Read, so the emulator's closed flag has one
			// writer and one reader in program order. The wake byte is dropped.
			s.emuMu.Lock()
			_ = s.emu.Close()
			s.emuMu.Unlock()
			return
		}
		if n > 0 {
			payload := append([]byte(nil), buf[:n]...)
			s.replyMu.Lock()
			fn := s.onReply
			s.replyMu.Unlock()
			if fn != nil {
				fn(payload)
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *ptyScreen) Write(p []byte) {
	if s == nil || s.emu == nil || len(p) == 0 {
		return
	}
	s.emuMu.Lock()
	defer s.emuMu.Unlock()
	if s.closing.Load() {
		return
	}
	_, _ = s.emu.Write(p)
}

func (s *ptyScreen) snapshot() ScreenSnapshot {
	if s == nil || s.emu == nil {
		return ScreenSnapshot{}
	}
	w := s.emu.Width()
	h := s.emu.Height()
	pos := s.emu.CursorPosition()
	lines := make([]string, 0, h)
	for y := 0; y < h; y++ {
		var b strings.Builder
		b.Grow(w)
		for x := 0; x < w; {
			cell := s.emu.CellAt(x, y)
			if cell == nil {
				b.WriteByte(' ')
				x++
				continue
			}
			content := cell.String()
			if content == "" {
				b.WriteByte(' ')
			} else {
				b.WriteString(content)
			}
			step := cell.Width
			if step < 1 {
				step = 1
			}
			// Continuation fillers preserve the terminal's column indices.
			for filler := 1; filler < step; filler++ {
				b.WriteByte(' ')
			}
			x += step
		}
		// Pad / truncate to winsize so grids are byte-identical across runs.
		line := b.String()
		runes := []rune(line)
		if len(runes) < w {
			line += strings.Repeat(" ", w-len(runes))
		} else if len(runes) > w {
			line = string(runes[:w])
		}
		lines = append(lines, line)
	}
	return ScreenSnapshot{
		Cols:      w,
		Rows:      h,
		CursorCol: pos.X,
		CursorRow: pos.Y,
		Lines:     lines,
		AltScreen: s.emu.IsAltScreen(),
	}
}
