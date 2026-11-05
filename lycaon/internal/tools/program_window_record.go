package tools

import (
	"context"
	"sync"
)

// ProgramWindow is a source window a host operation returned in a program's output,
// from the operation's own result. Body holds the host's numbered rows.
type ProgramWindow struct {
	Path string
	Body string
}

// ProgramWindows collects one program's windows until its output is delivered.
type ProgramWindows struct {
	mu   sync.Mutex
	rows []ProgramWindow
}

// Take returns the windows recorded so far and clears them.
func (w *ProgramWindows) Take() []ProgramWindow {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	rows := w.rows
	w.rows = nil
	return rows
}

type programWindowsKey struct{}

// WithProgramWindows binds a program's collector to its operations.
func WithProgramWindows(ctx context.Context, windows *ProgramWindows) context.Context {
	return context.WithValue(ctx, programWindowsKey{}, windows)
}

// RecordProgramWindow notes a window; outside a program it does nothing.
func RecordProgramWindow(ctx context.Context, window ProgramWindow) {
	windows, _ := ctx.Value(programWindowsKey{}).(*ProgramWindows)
	if windows == nil {
		return
	}
	windows.mu.Lock()
	defer windows.mu.Unlock()
	windows.rows = append(windows.rows, window)
}
