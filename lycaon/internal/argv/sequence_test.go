package argv

import (
	"errors"
	"testing"
)

func TestSplitSequenceSplitsOnOperators(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want []SequenceElement
	}{
		{
			name: "and",
			line: "go build ./... && ./app -v",
			want: []SequenceElement{
				{Connector: ConnectorNone, Name: "go", Args: []string{"build", "./..."}},
				{Connector: ConnectorAnd, Name: "./app", Args: []string{"-v"}},
			},
		},
		{
			name: "or",
			line: "which docker || echo none",
			want: []SequenceElement{
				{Connector: ConnectorNone, Name: "which", Args: []string{"docker"}},
				{Connector: ConnectorOr, Name: "echo", Args: []string{"none"}},
			},
		},
		{
			name: "semicolon",
			line: "git --version; which git",
			want: []SequenceElement{
				{Connector: ConnectorNone, Name: "git", Args: []string{"--version"}},
				{Connector: ConnectorSeq, Name: "which", Args: []string{"git"}},
			},
		},
		{
			name: "mixed chain",
			line: `test -f .env && echo "EXISTS" || echo "MISSING"`,
			want: []SequenceElement{
				{Connector: ConnectorNone, Name: "test", Args: []string{"-f", ".env"}},
				{Connector: ConnectorAnd, Name: "echo", Args: []string{"EXISTS"}},
				{Connector: ConnectorOr, Name: "echo", Args: []string{"MISSING"}},
			},
		},
		{
			name: "single command has no connector",
			line: "go test ./...",
			want: []SequenceElement{
				{Connector: ConnectorNone, Name: "go", Args: []string{"test", "./..."}},
			},
		},
		{
			name: "pipe",
			line: "git log -n 10 | head -n 5",
			want: []SequenceElement{
				{Connector: ConnectorNone, Name: "git", Args: []string{"log", "-n", "10"}},
				{Connector: ConnectorPipe, Name: "head", Args: []string{"-n", "5"}},
			},
		},
		{
			name: "pipe merged and redirections",
			line: "go test ./... 2>&1 | head -n 5",
			want: []SequenceElement{
				{Connector: ConnectorNone, Name: "go", Args: []string{"test", "./..."}, Streams: Streams{Stderr: Sink{Kind: SinkStdout}}},
				{Connector: ConnectorPipe, Name: "head", Args: []string{"-n", "5"}},
			},
		},
		{
			name: "pipe merged operator",
			line: "go test ./... |& grep FAIL",
			want: []SequenceElement{
				{Connector: ConnectorNone, Name: "go", Args: []string{"test", "./..."}},
				{Connector: ConnectorPipeMerged, Name: "grep", Args: []string{"FAIL"}},
			},
		},
		{
			name: "sequence with file redirection",
			line: "go test ./... && pytest > out.log",
			want: []SequenceElement{
				{Connector: ConnectorNone, Name: "go", Args: []string{"test", "./..."}},
				{Connector: ConnectorAnd, Name: "pytest", Streams: Streams{Stdout: Sink{Kind: SinkFile, Path: "out.log"}}},
			},
		},
		{
			name: "dev null redirection",
			line: "cmd > /dev/null 2>&1",
			want: []SequenceElement{
				{Connector: ConnectorNone, Name: "cmd", Streams: Streams{Stdout: Sink{Kind: SinkNull}, Stderr: Sink{Kind: SinkNull}}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SplitSequence(tc.line)
			if err != nil {
				t.Fatalf("SplitSequence(%q): %v", tc.line, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("elements = %d, want %d (%+v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i].Connector != tc.want[i].Connector || got[i].Name != tc.want[i].Name {
					t.Fatalf("element %d = %+v, want %+v", i, got[i], tc.want[i])
				}
				if got[i].Streams != tc.want[i].Streams {
					t.Fatalf("element %d streams = %+v, want %+v", i, got[i].Streams, tc.want[i].Streams)
				}
				if len(got[i].Args) != len(tc.want[i].Args) {
					t.Fatalf("element %d args = %v, want %v", i, got[i].Args, tc.want[i].Args)
				}
				for j := range got[i].Args {
					if got[i].Args[j] != tc.want[i].Args[j] {
						t.Fatalf("element %d arg %d = %q, want %q", i, j, got[i].Args[j], tc.want[i].Args[j])
					}
				}
			}
		})
	}
}

// A quoted operator is argument data, not a connector.
func TestSplitSequenceIgnoresQuotedOperators(t *testing.T) {
	for _, line := range []string{
		`python3 -c "import x; x.run()"`,
		`grep 'a && b' file.txt`,
		`echo "a || b"`,
		`echo "a \" && rm -rf /"`,
	} {
		got, err := SplitSequence(line)
		if err != nil {
			t.Fatalf("SplitSequence(%q): %v", line, err)
		}
		if len(got) != 1 {
			t.Fatalf("SplitSequence(%q) = %d elements, want 1 (%+v)", line, len(got), got)
		}
	}
}

func TestSplitSequenceRejections(t *testing.T) {
	for _, tc := range []struct {
		line string
		want error
	}{
		{"python3 -m http.server &", ErrBackgroundOperator},
		{"go build && ", ErrEmptySequenceElement},
		{"&& go build", ErrEmptySequenceElement},
		{"go build ; ; go test", ErrEmptySequenceElement},
		// Substitution never expands, so it stays rejected inside every element.
		{"git checkout $(git rev-parse HEAD)", ErrShellMetacharacters},
		{"go build && echo `whoami`", ErrShellMetacharacters},
		// Redirection target required or invalid.
		{"go test && pytest >", ErrRedirectionTargetRequired},
		{"go test && pytest > out$log", ErrShellMetacharacters},
		{`echo "unterminated && go build`, ErrUnterminatedQuote},
	} {
		_, err := SplitSequence(tc.line)
		if !errors.Is(err, tc.want) {
			t.Fatalf("SplitSequence(%q) error = %v, want %v", tc.line, err, tc.want)
		}
	}
}

// The `||` operator contains a `|`. A plain scan finds its first half and would
// send a correct sequence to the pipeline array.
func TestHasUnquotedPipeIgnoresOrOperator(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{
		{"which docker || echo none", false},
		{"a || b || c", false},
		{"git log | head -20", true},
		{"a || b | c", true},
		{`grep "a|b" file`, false},
		{"go test ./...", false},
	} {
		if got := HasUnquotedPipe(tc.line); got != tc.want {
			t.Fatalf("HasUnquotedPipe(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestConnectorOrPipeResolvesZeroValue(t *testing.T) {
	if got := ConnectorNone.OrPipe(); got != ConnectorPipe {
		t.Fatalf("ConnectorNone.OrPipe() = %q, want %q", got, ConnectorPipe)
	}
	if got := ConnectorAnd.OrPipe(); got != ConnectorAnd {
		t.Fatalf("ConnectorAnd.OrPipe() = %q, want %q", got, ConnectorAnd)
	}
}
