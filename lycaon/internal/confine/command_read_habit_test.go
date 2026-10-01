package confine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
)

func readHabitProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "den.md"), []byte("line\n"), 0o600); err != nil {
		t.Fatalf("write den.md: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o750); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "den.md"), []byte("line\n"), 0o600); err != nil {
		t.Fatalf("write docs/den.md: %v", err)
	}
	return dir
}

func TestReadHabitForMapsShellSpansToReadArgs(t *testing.T) {
	dir := readHabitProject(t)
	cases := []struct {
		name    string
		command string
		want    ReadHabit
	}{
		{
			name:    "sed line range",
			command: "sed -n '1,200p' docs/den.md",
			want:    ReadHabit{Program: "sed", Path: "docs/den.md", Offset: 1, Limit: 200},
		},
		{
			name:    "sed later span",
			command: "sed -n '400,600p' docs/den.md",
			want:    ReadHabit{Program: "sed", Path: "docs/den.md", Offset: 400, Limit: 201},
		},
		{
			name:    "sed single line",
			command: "sed -n 42p den.md",
			want:    ReadHabit{Program: "sed", Path: "den.md", Offset: 42, Limit: 1},
		},
		{
			name:    "head with -n",
			command: "head -n 40 docs/den.md",
			want:    ReadHabit{Program: "head", Path: "docs/den.md", Offset: 1, Limit: 40},
		},
		{
			name:    "head with bare count",
			command: "head -20 den.md",
			want:    ReadHabit{Program: "head", Path: "den.md", Offset: 1, Limit: 20},
		},
		{
			name:    "head default count",
			command: "head den.md",
			want:    ReadHabit{Program: "head", Path: "den.md", Offset: 1, Limit: 10},
		},
		{
			name:    "cat whole file",
			command: "cat docs/den.md",
			want:    ReadHabit{Program: "cat", Path: "docs/den.md"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ReadHabitFor(tc.command, dir)
			if !ok {
				t.Fatalf("ReadHabitFor(%q) did not match", tc.command)
			}
			if got != tc.want {
				t.Fatalf("ReadHabitFor(%q) = %+v, want %+v", tc.command, got, tc.want)
			}
		})
	}
}

func TestReadHabitForLeavesCommandsReadCannotServe(t *testing.T) {
	dir := readHabitProject(t)
	cases := []struct {
		name    string
		command string
	}{
		{"tail has no read equivalent", "tail -n 20 docs/den.md"},
		{"head byte span", "head -c 100 docs/den.md"},
		{"sed in-place edit", "sed -i 's/a/b/' docs/den.md"},
		{"sed address regex is a grep", "sed -n '/Composer/p' docs/den.md"},
		{"sed substitution print", "sed -n 's/a/b/p' docs/den.md"},
		{"sed without quiet flag", "sed -n_missing 1,20p docs/den.md"},
		{"cat with flags", "cat -n docs/den.md"},
		{"cat multiple files", "cat den.md docs/den.md"},
		{"path outside project", "cat ../../etc/hosts"},
		{"home path", "cat ~/notes.md"},
		{"glob argument", "cat docs/*.md"},
		{"missing file", "cat docs/absent.md"},
		{"directory", "cat docs"},
		{"stdin", "cat"},
		{"pipeline is a composition habit", "cat docs/den.md | head -5"},
		{"unrelated program", "go test ./..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := ReadHabitFor(tc.command, dir); ok {
				t.Fatalf("ReadHabitFor(%q) matched %+v, want no match", tc.command, got)
			}
		})
	}
}

func TestReadHabitForRequiresProjectDir(t *testing.T) {
	if _, ok := ReadHabitFor("cat docs/den.md", ""); ok {
		t.Fatal("ReadHabitFor matched without a project dir; it cannot verify the path")
	}
}

// A program name in this package reads as a floor even when it is not one, so
// the names stay in the tool-command-equivalence catalogue.
func TestReadHabitProgramsComeFromTheCatalogue(t *testing.T) {
	if len(readHabitGrammarFor) == 0 {
		t.Fatal("no read-habit programs generated; every shell read would fall through")
	}
	for program, grammar := range readHabitGrammarFor {
		switch grammar {
		case readHabitWholeFile, readHabitLeadingLines, readHabitQuietLineSpan:
		default:
			t.Errorf("program %q names grammar %q, which this package cannot parse", program, grammar)
		}
	}
}

func TestReadHabitPreservesAbsolutePathIdentity(t *testing.T) {
	project, outside := readHabitProject(t), readHabitProject(t)
	path := filepath.Join(outside, "den.md")
	got, ok := ReadHabitFor("cat "+path, project)
	if !ok || got.Path != filepath.ToSlash(fspath.CanonicalPath(path)) {
		t.Fatalf("absolute read lost identity: %+v, matched=%v", got, ok)
	}
}

func TestReadHabitPreservesQuotedFilenameIdentity(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"notes.txt", " notes.txt "} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatalf("create named file: %v", err)
		}
	}
	if got, ok := ReadHabitFor("cat ' notes.txt '", dir); ok {
		t.Fatalf("quoted filename changed during redirect: %+v", got)
	}
}
