package toolhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/pkg/api"
)

// captureFixture runs composed programs over two attached roots and collects
// each call's outputs.
type captureFixture struct {
	*programtest.Fixture
	docs string
	out  *tools.ToolInvocationOut
}

func newCaptureFixture(t *testing.T) *captureFixture {
	t.Helper()
	f := &captureFixture{Fixture: newComposedProgramFixture(t), docs: t.TempDir()}
	f.Configure = func(tc *tools.ToolContext) {
		f.out = &tools.ToolInvocationOut{}
		tc.Effects.Out = f.out
		tc.Source.Roots = append(tc.Source.Roots, projectroot.RootRef{ID: "r2", Label: "docs", Path: f.docs})
	}
	return f
}

func (f *captureFixture) seed(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	testutil.FailErr(t, "create parent of "+name, os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "seed "+name, os.WriteFile(path, []byte(content), 0o644))
}

func (f *captureFixture) run(t *testing.T, source string) *tools.ToolInvocationOut {
	t.Helper()
	if outcome := callComposedProgram(t, f.Fixture, source); outcome.Status != 0 || outcome.Refusal != nil {
		t.Fatalf("%q: %+v refusal %+v", source, outcome, outcome.Refusal)
	}
	return f.out
}

func TestCommandTextMutationsRecordRootQualifiedEdits(t *testing.T) {
	for name, tc := range map[string]struct {
		secondary bool
		source    string
		want      api.FileEditSnapshot
	}{
		"edit in the primary root": {
			source: `edit --path notes.txt --old-string alpha --new-string beta`,
			want:   api.FileEditSnapshot{RootID: "r1", Path: "notes.txt", After: "beta\n"},
		},
		"redirect in the primary root": {
			source: `printf 'gamma\n' > notes.txt`,
			want:   api.FileEditSnapshot{RootID: "r1", Path: "notes.txt", After: "gamma\n"},
		},
		"edit in a second root": {
			secondary: true,
			source:    `edit --path @docs/notes.txt --old-string alpha --new-string beta`,
			want:      api.FileEditSnapshot{RootID: "r2", Path: "notes.txt", After: "beta\n"},
		},
		"removal in the primary root": {
			source: `rm notes.txt`,
			want:   api.FileEditSnapshot{RootID: "r1", Path: "notes.txt", Deleted: true},
		},
		"removal in a second root": {
			secondary: true,
			source:    `rm @docs/notes.txt`,
			want:      api.FileEditSnapshot{RootID: "r2", Path: "notes.txt", Deleted: true},
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCaptureFixture(t)
			dir := f.Dir
			if tc.secondary {
				dir = f.docs
			}
			f.seed(t, dir, "notes.txt", "alpha\n")
			edits := f.run(t, tc.source).FileEdits
			if len(edits) != 1 || edits[0].Before == nil || *edits[0].Before != "alpha\n" {
				t.Fatalf("recorded %+v, want one edit based on the prior text", edits)
			}
			if got := edits[0]; got.RootID != tc.want.RootID || got.Path != tc.want.Path || got.After != tc.want.After || got.Deleted != tc.want.Deleted {
				t.Fatalf("recorded %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Only a removal of text the host serves for editing has a diff to show.
func TestCommandMutationsWithoutEditableTextRecordNoEdit(t *testing.T) {
	for name, tc := range map[string]struct {
		seed   func(t *testing.T, f *captureFixture)
		source string
	}{
		"binary file removal": {
			seed:   func(t *testing.T, f *captureFixture) { f.seed(t, f.Dir, "image.bin", "\x00\x01\x02\xff\x00") },
			source: `rm image.bin`,
		},
		"oversized file removal": {
			seed: func(t *testing.T, f *captureFixture) {
				f.seed(t, f.Dir, "large.txt", strings.Repeat("a", int(sourceview.AccessMutate.MaxBytes())+1))
			},
			source: `rm large.txt`,
		},
		"link removal": {
			seed: func(t *testing.T, f *captureFixture) {
				f.seed(t, f.Dir, "target.txt", "alpha\n")
				testutil.FailErr(t, "link", os.Symlink("target.txt", filepath.Join(f.Dir, "link.txt")))
			},
			source: `rm link.txt`,
		},
		"directory removal": {
			seed:   func(t *testing.T, f *captureFixture) { f.seed(t, f.Dir, "old/notes.txt", "alpha\n") },
			source: `rm -r old`,
		},
		"move": {
			seed:   func(t *testing.T, f *captureFixture) { f.seed(t, f.Dir, "notes.txt", "alpha\n") },
			source: `mv notes.txt moved.txt`,
		},
		"copy": {
			seed:   func(t *testing.T, f *captureFixture) { f.seed(t, f.Dir, "notes.txt", "alpha\n") },
			source: `cp notes.txt copy.txt`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCaptureFixture(t)
			tc.seed(t, f)
			if got := f.run(t, tc.source).FileEdits; len(got) != 0 {
				t.Fatalf("recorded %+v, want no edit", got)
			}
		})
	}
}

func numberedLines(count int, needles ...int) string {
	var b strings.Builder
	for i := 1; i <= count; i++ {
		word := "plain"
		for _, n := range needles {
			if n == i {
				word = "needle"
			}
		}
		b.WriteString(word + "\n")
	}
	return b.String()
}

// A program's reads are the lines that reached the model from the stage that
// read the file, not the lines a stage scanned on the way.
func TestCommandReadsRecordDeliveredLinesOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		source string
		want   []agentpresence.Span
	}{
		"the first lines of a file": {
			source: `head -n 5 notes.txt`,
			want:   []agentpresence.Span{{StartLine: 1, EndLine: 5}},
		},
		"a search delivers its matching lines": {
			source: `rg needle notes.txt`,
			want:   []agentpresence.Span{{StartLine: 3, EndLine: 3}, {StartLine: 17, EndLine: 17}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCaptureFixture(t)
			f.seed(t, f.Dir, "notes.txt", numberedLines(20, 3, 17))
			out := f.run(t, tc.source)
			if len(out.SourceReads) != 1 {
				t.Fatalf("recorded %d reads, want one for the file: %+v", len(out.SourceReads), out.SourceReads)
			}
			read := out.SourceReads[0]
			if read.Target != (agentpresence.Target{RootID: "r1", Path: "notes.txt"}) || read.Extent != api.AgentPresenceExtentRange {
				t.Fatalf("read %+v, want a line range of the delivered file", read)
			}
			if len(read.Spans) != len(tc.want) {
				t.Fatalf("spans %+v, want %+v", read.Spans, tc.want)
			}
			for i, span := range read.Spans {
				if span.StartLine != tc.want[i].StartLine || span.EndLine != tc.want[i].EndLine || span.StartCharacter != nil || span.EndCharacter != nil {
					t.Fatalf("spans %+v, want whole lines %+v", read.Spans, tc.want)
				}
			}
			if out.SourceContext == nil || len(out.SourceContext.Locations) != 1 ||
				out.SourceContext.Locations[0].RootID != "r1" || out.SourceContext.Locations[0].Path != "notes.txt" {
				t.Fatalf("source context %+v, want the delivered file", out.SourceContext)
			}
		})
	}
}

// Text that reaches the model through a later stage carries no source lines,
// so it is stated as no read rather than as the whole file.
func TestCommandReadsThroughAnotherStageRecordNoRead(t *testing.T) {
	f := newCaptureFixture(t)
	f.seed(t, f.Dir, "notes.txt", numberedLines(20))
	if out := f.run(t, `cat notes.txt | head -n 5`); len(out.SourceReads) != 0 || out.SourceContext != nil {
		t.Fatalf("recorded reads %+v and context %+v for piped text", out.SourceReads, out.SourceContext)
	}
}
