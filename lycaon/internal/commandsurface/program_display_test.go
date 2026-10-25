package commandsurface_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStatementDisplayElidesOnlyItsOwnHereDocumentBodies(t *testing.T) {
	cases := []struct {
		name   string
		source string
		pick   func(commandsurface.ProgramSnapshot) []commandsurface.Statement
		want   []string
		hidden string
	}{
		{
			name:   "quoted delimiter before a later redirect",
			source: "cat <<'EOF' > ./notes.txt\nsecret body $(not run)\nsecond line\nEOF\n",
			want:   []string{"cat <<'EOF' ... > ./notes.txt"},
			hidden: "secret body",
		},
		{
			name:   "pipeline header continues after the delimiter",
			source: "cat <<EOF | grep two\none\ntwo\nEOF\n",
			pick: func(p commandsurface.ProgramSnapshot) []commandsurface.Statement {
				pipe := p.Statements[0].Command.(commandsurface.BinaryCommand)
				return []commandsurface.Statement{pipe.Left, pipe.Right}
			},
			want:   []string{"cat <<EOF ...", "grep two"},
			hidden: "one",
		},
		{
			name:   "multiple bodies keep their own delimiters",
			source: "cat <<A <<-B\nfirst body\nA\n\tsecond body\n\tB\n",
			want:   []string{"cat <<A ... <<-B ..."},
			hidden: "body",
		},
		{
			name:   "terminated statement leaves the next statement intact",
			source: "cat <<EOF; printf done\nhidden body\nEOF\n",
			want:   []string{"cat <<EOF ...", "printf done"},
			hidden: "hidden",
		},
		{
			name:   "statement without here-documents is its original source",
			source: "printf '%s\\n' 'a | b' > ./out 2>> ./err",
			want:   []string{"printf '%s\\n' 'a | b' > ./out 2>> ./err"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := parseProgram(t, tc.source)
			statements := p.Statements
			if tc.pick != nil {
				statements = tc.pick(p)
			}
			if len(statements) != len(tc.want) {
				t.Fatalf("got %d statements, want %d", len(statements), len(tc.want))
			}
			for i, stmt := range statements {
				got, err := stmt.DisplayText([]byte(tc.source))
				testutil.FailErr(t, "render display", err)
				if got != tc.want[i] {
					t.Fatalf("display %d = %q, want %q", i, got, tc.want[i])
				}
				if tc.hidden != "" && strings.Contains(got, tc.hidden) {
					t.Fatalf("display %q echoed here-document body", got)
				}
			}
		})
	}
}

func TestStatementDisplayRefusesSpansOutsideTheSource(t *testing.T) {
	source := "cat <<EOF > ./out\nbody\nEOF\n"
	p := parseProgram(t, source)
	if _, err := p.Statements[0].DisplayText([]byte("cat")); err == nil {
		t.Fatal("display rendered a span beyond the supplied source")
	}
}
