package git

import (
	"strings"
)

const commitFailedOutputTailLines = 80

// CommitFailed preserves a failed commit capture.
type CommitFailed struct {
	Output   string
	ExitCode int
}

// NewCommitFailed builds an error from combined commit output.
func NewCommitFailed(output []byte, exitCode int) *CommitFailed {
	out := strings.TrimSpace(string(output))
	return &CommitFailed{Output: out, ExitCode: exitCode}
}

func (e *CommitFailed) Error() string {
	if e == nil {
		return "git commit failed"
	}
	var b strings.Builder
	b.WriteString(e.Summary())
	if tail := e.Tail(); tail != "" {
		b.WriteByte('\n')
		b.WriteString(tail)
	}
	if hint := e.Hint(); hint != "" {
		b.WriteByte('\n')
		b.WriteString(hint)
	}
	return b.String()
}

// Summary is a single-line agent-facing label (no capture body).
func (e *CommitFailed) Summary() string {
	return "git commit failed"
}

// Tail returns the trailing capture lines for wire/UI diagnostics.
func (e *CommitFailed) Tail() string {
	if e == nil {
		return ""
	}
	return tailLines(e.Output, commitFailedOutputTailLines)
}

// Hint returns the generic commit recovery action.
func (e *CommitFailed) Hint() string {
	if e == nil {
		return ""
	}
	return "Review output_tail for Git or hook diagnostics, correct the reported failure, and retry the commit."
}

func tailLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" || n <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
