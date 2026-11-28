package tools

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/hostmarker"
)

// ProgramWindow is a source window a host operation returned in a program's output,
// from the operation's own result. Body holds the host's numbered rows.
type ProgramWindow struct {
	Path             string
	RootID           string
	ScopeRel         string
	DocumentID       string
	DocumentRevision int64
	StartLine        int
	EndLine          int
	Body             string
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

// MatchesWholeDocumentWindow reports whether a 1-based numbered window covers
// the exact complete text of doc. For CRLF documents, summarize preserves '\r'
// at CRLF line ends while EditorDocumentText.Text is LF-normalized, so CRLF
// documents match either LF-normalized or exact CRLF-suffixed numbered rows.
func MatchesWholeDocumentWindow(doc EditorDocumentText, lines []string, startLine, endLine int, body string) bool {
	if startLine != 1 || body == "" || endLine != len(lines) {
		return false
	}
	if body == hostmarker.FormatNumberedLines(lines, 1) {
		return true
	}
	if doc.EOL != "crlf" || len(lines) == 0 {
		return false
	}
	hasTrailingNewline := strings.HasSuffix(doc.Text, "\n")
	crlfLines := make([]string, len(lines))
	for i, line := range lines {
		if hasTrailingNewline || i < len(lines)-1 {
			crlfLines[i] = line + "\r"
		} else {
			crlfLines[i] = line
		}
	}
	return body == hostmarker.FormatNumberedLines(crlfLines, 1)
}
