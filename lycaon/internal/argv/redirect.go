package argv

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// DevNull is the one redirection target the host maps to a discarded or empty stream.
const DevNull = "/dev/null"

// RedirectOp is a redirection operator in its canonical spelling.
type RedirectOp string

// Redirection operators the grammar accepts.
const (
	RedirectOut    RedirectOp = ">"
	RedirectAppend RedirectOp = ">>"
	RedirectIn     RedirectOp = "<"
	// RedirectDup points Fd at the stream Target's descriptor names at that moment.
	RedirectDup RedirectOp = ">&"
)

// Redirect is one redirection in the order it was written.
type Redirect struct {
	// Fd is 0, 1, or 2; -1 names stdout and stderr together (`&>`).
	Fd     int        `json:"fd"`
	Op     RedirectOp `json:"op"`
	Target string     `json:"target"`
}

// String renders the redirection so it parses back to itself.
func (r Redirect) String() string {
	switch {
	case r.Op == RedirectDup:
		return strconv.Itoa(r.Fd) + ">&" + r.Target
	case r.Fd == fdBoth:
		return "&" + string(r.Op) + " " + quoteCommandLineArg(r.Target)
	case (r.Op == RedirectIn && r.Fd == 0) || (r.Op != RedirectIn && r.Fd == 1):
		return string(r.Op) + " " + quoteCommandLineArg(r.Target)
	default:
		return strconv.Itoa(r.Fd) + string(r.Op) + " " + quoteCommandLineArg(r.Target)
	}
}

// SinkKind names where an output descriptor writes.
type SinkKind uint8

// Sink kinds. The zero value is the descriptor's own default stream.
const (
	SinkDefault SinkKind = iota
	// SinkStdout is the stage's default stdout: the next pipe stage, or the call's output.
	SinkStdout
	// SinkStderr is the call's stderr stream.
	SinkStderr
	SinkNull
	SinkFile
)

// Sink is one output descriptor's destination.
type Sink struct {
	Kind   SinkKind `json:"kind,omitempty"`
	Path   string   `json:"path,omitempty"`
	Append bool     `json:"append,omitempty"`
}

// Source is a stage's stdin; the zero value inherits the plan's stream.
type Source struct {
	Null bool   `json:"null,omitempty"`
	Path string `json:"path,omitempty"`
}

// Streams is a stage's descriptor table after its redirections apply in order.
type Streams struct {
	Stdout Sink   `json:"stdout,omitzero"`
	Stderr Sink   `json:"stderr,omitzero"`
	Stdin  Source `json:"stdin,omitzero"`
}

// StdoutSink resolves the default so callers see a concrete destination.
func (s Streams) StdoutSink() Sink {
	if s.Stdout.Kind == SinkDefault {
		return Sink{Kind: SinkStdout}
	}
	return s.Stdout
}

// StderrSink resolves the default so callers see a concrete destination.
func (s Streams) StderrSink() Sink {
	if s.Stderr.Kind == SinkDefault {
		return Sink{Kind: SinkStderr}
	}
	return s.Stderr
}

// Terminal reports streams a terminal can honour: no file, no discard, inherited stdin.
func (s Streams) Terminal() bool {
	for _, sink := range []Sink{s.StdoutSink(), s.StderrSink()} {
		if sink.Kind == SinkFile || sink.Kind == SinkNull {
			return false
		}
	}
	return s.Stdin == Source{}
}

// FeedsStdout reports whether either descriptor writes the stage's default stdout.
func (s Streams) FeedsStdout() bool {
	return s.StdoutSink().Kind == SinkStdout || s.StderrSink().Kind == SinkStdout
}

// WritePaths lists the files the stage writes, stdout first.
func (s Streams) WritePaths() []string {
	var out []string
	for _, sink := range []Sink{s.StdoutSink(), s.StderrSink()} {
		if sink.Kind == SinkFile && (len(out) == 0 || out[0] != sink.Path) {
			out = append(out, sink.Path)
		}
	}
	return out
}

// resolveStreams applies redirections left to right, as a shell opens them.
func resolveStreams(redirects []Redirect) (Streams, error) {
	var s Streams
	for _, r := range redirects {
		switch r.Op {
		case RedirectIn:
			if r.Target == DevNull {
				s.Stdin = Source{Null: true}
			} else {
				s.Stdin = Source{Path: r.Target}
			}
		case RedirectOut, RedirectAppend:
			sink := Sink{Kind: SinkFile, Path: r.Target, Append: r.Op == RedirectAppend}
			if r.Target == DevNull {
				sink = Sink{Kind: SinkNull}
			}
			switch r.Fd {
			case 1:
				s.Stdout = sink
			case 2:
				s.Stderr = sink
			default:
				s.Stdout, s.Stderr = sink, sink
			}
		case RedirectDup:
			from := s.StdoutSink()
			if r.Target == "2" {
				from = s.StderrSink()
			}
			if r.Fd == 1 {
				s.Stdout = from
			} else {
				s.Stderr = from
			}
		}
	}
	out, errSink := s.StdoutSink(), s.StderrSink()
	if out.Kind == SinkFile && errSink.Kind == SinkFile && out.Path == errSink.Path && out.Append != errSink.Append {
		return s, &RedirectionError{Issue: IssueModeConflict, Operator: out.Path}
	}
	return s, nil
}

// RenderRedirects writes redirections in order after a stage's argv.
func RenderRedirects(redirects []Redirect) string {
	parts := make([]string, len(redirects))
	for i, r := range redirects {
		parts[i] = r.String()
	}
	return strings.Join(parts, " ")
}

// RedirectionIssue names why the host will not run a redirection.
type RedirectionIssue string

// Redirection issues are agent-public rejection facts.
const (
	// IssueDescriptorUnsupported: only 0, 1, and 2 can be redirected.
	IssueDescriptorUnsupported RedirectionIssue = "descriptor_unsupported"
	// IssueOperatorUnsupported: closing, read-write, and descriptor-input forms.
	IssueOperatorUnsupported RedirectionIssue = "operator_unsupported"
	// IssueModeConflict: one file named for truncation and for append.
	IssueModeConflict RedirectionIssue = "mode_conflict"
	// IssuePipeStdout: a piped stage sends none of its output into the pipe.
	IssuePipeStdout RedirectionIssue = "pipe_stage_stdout"
	// IssuePipeStdin: a stage reading a pipe also names another stdin.
	IssuePipeStdin RedirectionIssue = "pipe_stage_stdin"
	// IssueStdoutConflict: inline stdout redirection alongside stdout_to.
	IssueStdoutConflict RedirectionIssue = "stdout_to_conflict"
	// IssueStderrConflict: inline stderr redirection alongside stderr_to.
	IssueStderrConflict RedirectionIssue = "stderr_to_conflict"
	// IssueStdinConflict: inline stdin redirection alongside stdin or stdin_from.
	IssueStdinConflict RedirectionIssue = "stdin_conflict"
	// IssueSurfaceUnsupported: the surface runs in a terminal, which owns every stream.
	IssueSurfaceUnsupported RedirectionIssue = "surface_unsupported"
)

// ErrRedirectionUnsupported matches every RedirectionError.
var ErrRedirectionUnsupported = errors.New("unsupported redirection")

// RedirectionError reports a redirection the host does not run as written.
type RedirectionError struct {
	Issue RedirectionIssue
	// Operator is the operator or target as written.
	Operator string
}

func (e *RedirectionError) Error() string {
	if e.Operator == "" {
		return fmt.Sprintf("%s: %s", ErrRedirectionUnsupported, e.Issue)
	}
	return fmt.Sprintf("%s: %s (`%s`)", ErrRedirectionUnsupported, e.Issue, e.Operator)
}

// Is lets errors.Is match ErrRedirectionUnsupported.
func (e *RedirectionError) Is(target error) bool { return target == ErrRedirectionUnsupported }
